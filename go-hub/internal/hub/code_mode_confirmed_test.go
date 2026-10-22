package hub

// The confirmed full source: a spilled command is indexed from the server's
// own record of the complete output, never from the tail it returned.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// codeModeSpilledLog builds a log that is larger than one confirmed chunk, so
// the chunked read and its completeness check are both exercised.
func codeModeSpilledLog(t *testing.T) (string, string) {
	t.Helper()
	const lines = 30000
	var builder strings.Builder
	for i := 1; i <= lines; i++ {
		switch i {
		case 1:
			builder.WriteString("RARE_START marker=START-EXACT request=fixture-000001\n")
		case lines / 2:
			builder.WriteString(fmt.Sprintf("RARE_MIDDLE marker=MIDDLE-EXACT request=fixture-%06d\n", i))
		case lines:
			builder.WriteString("RARE_END marker=END-EXACT request=fixture-030000\n")
		default:
			builder.WriteString(fmt.Sprintf("routine noise line %06d with enough padding to make the log large\n", i))
		}
	}
	path := filepath.Join(t.TempDir(), "spilled.log")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, builder.String()
}

// codeModeShellHandler answers shell_exec with a spilled tail and
// system_inspect with the confirmed spool file.
func codeModeShellHandler(path string, total int) func(tool string, args map[string]any) (any, error) {
	return func(tool string, args map[string]any) (any, error) {
		switch tool {
		case "shell_exec":
			// The tail is what the caller sees. The complete output stays in
			// the spool file.
			return map[string]any{
				"stdout": "RARE_START marker=START-EXACT request=fixture-000001\n",
				"stderr": "", "returncode": 0,
				"_spilled": true, "stdout_path": path, "stdout_bytes": total, "stdout_truncated": true,
			}, nil
		case "system_inspect":
			if firstString(args, "action") != "read_file" {
				return nil, nil
			}
			data, err := os.ReadFile(firstString(args, "path"))
			if err != nil {
				return nil, err
			}
			offset := int64(intFromAny(args["offset"]))
			limit := int64(intFromAny(args["max_bytes"]))
			if limit <= 0 {
				limit = int64(len(data))
			}
			if offset > int64(len(data)) {
				offset = int64(len(data))
			}
			end := offset + limit
			if end > int64(len(data)) {
				end = int64(len(data))
			}
			chunk := data[offset:end]
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}, "structuredContent": map[string]any{
				"inspection": map[string]any{
					"content": string(chunk), "bytes_read": len(chunk), "size": len(data),
					"offset": offset, "truncated": end < int64(len(data)),
				},
			}}, nil
		}
		return nil, nil
	}
}

// A spilled command is indexed from the confirmed full output. The exact
// markers at the beginning, the middle and the end are all found, and the
// command itself is not run a second time.
func TestConfirmedFullSourceIsIndexedInsteadOfTheTail(t *testing.T) {
	path, log := codeModeSpilledLog(t)
	host := startCodeModeShellHost(t, codeModeShellHandler(path, len(log)))
	r := codeModeHostRequest(t, host)

	got, status := codeModeExecRun(t, host, r, `return await mcp.call('shell:fixture','shell_exec',{cmd:'cat log'},{index:true,name:'spilled-log'});`, nil)
	if status != http.StatusOK || firstString(got, "status") != "completed" {
		t.Fatalf("the confirmed source was not indexed: %d %#v", status, got)
	}
	result := mapValue(got["result"])
	source := firstString(result, "source")
	if source == "" {
		t.Fatalf("no source was returned: %#v", got)
	}
	if !truthy(result["complete"]) {
		t.Fatalf("the confirmed full output was stored as incomplete: %#v", result)
	}
	if calls := host.agent.callsFor("shell_exec"); len(calls) != 1 {
		t.Fatalf("the command ran %d times", len(calls))
	}

	store, err := host.server.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Source(operationPrincipal(r), source)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Bytes != int64(len(log)) {
		t.Fatalf("stored %d bytes, the confirmed log has %d", meta.Bytes, len(log))
	}
	for _, marker := range []string{"RARE_START", "RARE_MIDDLE", "RARE_END"} {
		hits, err := store.Search(operationPrincipal(r), source, marker, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatalf("marker %s was not found in the confirmed full output", marker)
		}
	}
	// The exact line text and the line range are preserved.
	hits, err := store.Search(operationPrincipal(r), source, "RARE_END", 5)
	if err != nil {
		t.Fatal(err)
	}
	body, err := store.Read(operationPrincipal(r), source, hits[0].ChunkID, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "RARE_END marker=END-EXACT request=fixture-030000") {
		t.Fatalf("the exact end marker is missing from the indexed output")
	}
	if hits[0].LineEnd < 29990 {
		t.Fatalf("the end marker landed in the wrong line range: %+v", hits[0])
	}
}

// A second search over the same source does not rebuild its index.
func TestConfirmedFullSourceSearchDoesNotRebuildTheIndex(t *testing.T) {
	path, log := codeModeSpilledLog(t)
	host := startCodeModeShellHost(t, codeModeShellHandler(path, len(log)))
	r := codeModeHostRequest(t, host)
	got, status := codeModeExecRun(t, host, r, `return await mcp.call('shell:fixture','shell_exec',{cmd:'cat log'},{index:true,name:'spilled-log'});`, nil)
	if status != http.StatusOK {
		t.Fatalf("indexing failed: %d %#v", status, got)
	}
	source := firstString(mapValue(got["result"]), "source")
	store, err := host.server.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	principal := operationPrincipal(r)
	before, err := store.SearchIndexRows(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("the confirmed source was not indexed")
	}
	for i := 0; i < 3; i++ {
		if _, err = store.Search(principal, source, "RARE_MIDDLE", 5); err != nil {
			t.Fatal(err)
		}
	}
	after, err := store.SearchIndexRows(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a repeated search rebuilt the index: %d rows became %d", before, after)
	}
}

// Without a confirmed full source the caller learns that the output is a part,
// and nothing is indexed silently.
func TestMissingConfirmedSourceYieldsIncompleteAndNoSilentIndex(t *testing.T) {
	host := startCodeModeShellHost(t, func(tool string, args map[string]any) (any, error) {
		if tool == "shell_exec" {
			// A tail without a spool path: the server knows no full source.
			return map[string]any{"stdout": "only the tail\n", "stderr": "", "returncode": 0, "_spilled": true, "stdout_bytes": 4096, "stdout_truncated": true}, nil
		}
		return nil, nil
	})
	r := codeModeHostRequest(t, host)

	got, status := codeModeExecRun(t, host, r, `return await mcp.call('shell:fixture','shell_exec',{cmd:'cat log'},{index:true,name:'tail-log'});`, nil)
	if status == http.StatusOK || firstString(got, "status") == "completed" {
		t.Fatalf("a tail was indexed as if it were the full log: %d %#v", status, got)
	}
	failure := mapValue(got["error"])
	if code := firstString(failure, "code"); code != "content_index_partial" {
		t.Fatalf("unexpected failure code %q: %#v", code, failure)
	}
	receipt := mapValue(failure["receipt"])
	if truthy(receipt["complete"]) {
		t.Fatalf("the failure did not report an incomplete source: %#v", receipt)
	}
	if firstString(receipt, "reason") == "" {
		t.Fatalf("the failure did not give a reason: %#v", receipt)
	}
	store, err := host.server.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.Sources(operationPrincipal(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("a partial source was stored without explicit consent: %#v", sources)
	}
}

// With explicit consent a part may be stored, but the incompleteness marker
// is permanent.
func TestPartialIndexNeedsConsentAndKeepsTheMarker(t *testing.T) {
	host := startCodeModeShellHost(t, func(tool string, args map[string]any) (any, error) {
		if tool == "shell_exec" {
			return map[string]any{"stdout": "only the tail\n", "stderr": "", "returncode": 0, "_spilled": true, "stdout_bytes": 4096, "stdout_truncated": true}, nil
		}
		return nil, nil
	})
	r := codeModeHostRequest(t, host)

	got, status := codeModeExecRun(t, host, r, `return await mcp.call('shell:fixture','shell_exec',{cmd:'cat log'},{index:true,name:'tail-log',allowPartial:true});`, nil)
	if status != http.StatusOK || firstString(got, "status") != "completed" {
		t.Fatalf("the consented partial index failed: %d %#v", status, got)
	}
	result := mapValue(got["result"])
	if truthy(result["complete"]) {
		t.Fatalf("a partial source was stored as complete: %#v", result)
	}
	source := firstString(result, "source")
	store, err := host.server.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Source(operationPrincipal(r), source)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Complete {
		t.Fatal("the incompleteness marker was not stored with the source")
	}
}

// The confirmed reader is verified against the size metadata of the confirmed
// resource. A resource that ends early is reported as incomplete.
func TestConfirmedReaderDetectsAnIncompleteResource(t *testing.T) {
	calls := 0
	reader := func(r *http.Request, uri, owner string) map[string]any {
		calls++
		return map[string]any{
			"contents":  []any{map[string]any{"uri": uri, "mimeType": "text/plain", "text": "only part"}},
			"offset":    0,
			"bytesRead": 8,
			"totalSize": 4096,
		}
	}
	source := codeModeConfirmedSource{URI: "gptadmin://task/job-1/stdout", Owner: "shell:fixture"}
	output := readCodeModeConfirmedOutput(nil, reader, source, codeModeConfirmedMaxBytes)
	if output.Complete {
		t.Fatalf("a short resource was reported as complete: %+v", output)
	}
	if output.Reason == "" {
		t.Fatal("an incomplete resource was reported without a reason")
	}
	if calls != 1 {
		t.Fatalf("the reader was called %d times", calls)
	}
}

// A resource that reports a different total size halfway through is refused.
func TestConfirmedReaderDetectsAChangingResource(t *testing.T) {
	calls := 0
	reader := func(r *http.Request, uri, owner string) map[string]any {
		calls++
		size := int64(10)
		if calls > 1 {
			size = 20
		}
		return map[string]any{
			"contents":  []any{map[string]any{"uri": uri, "text": "0123456789"}},
			"offset":    0,
			"bytesRead": 10,
			"totalSize": size,
			"nextUri":   "gptadmin://task/job-1/stdout?offset=10",
		}
	}
	source := codeModeConfirmedSource{URI: "gptadmin://task/job-1/stdout", Owner: "shell:fixture"}
	output := readCodeModeConfirmedOutput(nil, reader, source, codeModeConfirmedMaxBytes)
	if output.Complete {
		t.Fatalf("a changing resource was reported as complete: %+v", output)
	}
}

// The confirmed source is only offered for a job the server really holds and
// that ran on the host the caller addressed.
func TestConfirmedSourceRequiresTheServerSideJobRecord(t *testing.T) {
	host := startCodeModeShellHost(t, func(tool string, args map[string]any) (any, error) { return nil, nil })
	if _, ok := host.server.codeModeConfirmedSourceFor("no-such-job", "shell:fixture"); ok {
		t.Fatal("a confirmed source was offered for an unknown job")
	}
	host.server.mu.Lock()
	host.server.shellJobs["job-1"] = &shellJob{ID: "job-1", Server: "fixture", Status: "completed", Result: map[string]any{"_spilled": true, "stdout_path": "/tmp/spool/out"}}
	host.server.mu.Unlock()
	if _, ok := host.server.codeModeConfirmedSourceFor("job-1", "shell:other"); ok {
		t.Fatal("a confirmed source was offered for another host")
	}
	if _, ok := host.server.codeModeConfirmedSourceFor("job-1", "hub"); ok {
		t.Fatal("a confirmed source was offered for a call that addressed no host")
	}
	source, ok := host.server.codeModeConfirmedSourceFor("job-1", "shell:fixture")
	if !ok {
		t.Fatal("the confirmed source of a known job was not offered")
	}
	if source.URI != "gptadmin://task/job-1/stdout" {
		t.Fatalf("unexpected confirmed uri %q", source.URI)
	}
	if source.Owner != "shell:fixture" {
		t.Fatalf("unexpected confirmed owner %q", source.Owner)
	}
}

// The origin encoding keeps the historical single origin working and records
// every source of a derived result.
func TestContentOriginEncodingKeepsSingleOriginsAndTracksEverySource(t *testing.T) {
	single := contentOriginSingle("shell:fixture", "shell_exec")
	if single != "shell:fixture\x00shell_exec" {
		t.Fatalf("unexpected single origin %q", single)
	}
	list := contentOriginList(single)
	if len(list) != 1 || list[0] != single {
		t.Fatalf("the single origin did not decode: %#v", list)
	}
	multi := contentOriginMerge(single, contentOriginSingle("file:fixture", "file_editor"))
	decoded := contentOriginList(multi)
	if len(decoded) != 2 || decoded[0] != single || decoded[1] != "file:fixture\x00file_editor" {
		t.Fatalf("the multi origin did not decode: %#v", decoded)
	}
	if got := contentOriginMerge(single, single); got != single {
		t.Fatalf("a repeated origin was not merged: %q", got)
	}
	if got := contentOriginMerge(); got != contentFreeTextOrigin {
		t.Fatalf("an empty merge is %q", got)
	}
	if got := contentOriginMerge(contentFreeTextOrigin, single); got != single {
		t.Fatalf("the free text origin was mixed with a real source: %q", got)
	}
	// A source written before this change still decodes as one origin.
	if got := contentOriginList("exec:index"); len(got) != 1 || got[0] != "exec:index" {
		t.Fatalf("the historical free text origin did not decode: %#v", got)
	}
}

// A right that is withdrawn from one source of a derived result withdraws the
// whole result.
func TestDerivedResultIsRevokedWithAnyOfItsSources(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	// A second source that really exists, so the derived result is readable
	// until its right is withdrawn.
	s.mu.Lock()
	s.agents["shell:secondary"] = &Agent{AgentID: "shell:secondary", Name: "secondary", Kind: "shell", Transport: "long_poll", Status: "online", Meta: map[string]any{"approved": true, "build_version": 400}}
	s.mu.Unlock()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	principal := operationPrincipal(r)
	origin := contentOriginMerge(contentOriginSingle("hub", "demo"), contentOriginSingle("file:secondary", "file_editor"))
	source, err := store.SaveFormatted(principal, "derived", origin, "text", "DERIVED_MARKER\nsecond line\n")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(source.ID)

	read, status := codeModeRun(t, s, r, map[string]any{"code": `return await mcp.content.read(` + string(quoted) + `,1);`})
	if status != http.StatusOK || firstString(read, "status") != "completed" {
		t.Fatalf("the derived source was not readable: %d %#v", status, read)
	}

	// The second source of the derived result is denied: the whole result is.
	denied := requestWithAccessProfile(r, AccessProfile{ID: "narrow", AccessMode: accessModeFull, AllowedTargets: []string{"hub"}, AllowedTools: []string{"demo"}})
	revoked, status := codeModeRun(t, s, denied, map[string]any{"code": `return await mcp.content.read(` + string(quoted) + `,1);`})
	if status == http.StatusOK || firstString(revoked, "status") == "completed" {
		t.Fatalf("a revoked source stayed readable: %d %#v", status, revoked)
	}
	search, status := codeModeRun(t, s, denied, map[string]any{"code": `return await mcp.content.search('DERIVED_MARKER',{source:` + string(quoted) + `});`})
	if status == http.StatusOK || firstString(search, "status") == "completed" {
		t.Fatalf("a revoked source stayed searchable: %d %#v", status, search)
	}
}

// A stored source does not outlive its TTL, and another principal never sees
// it.
func TestStoredSourceExpiryAndPrincipalIsolation(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	principal := operationPrincipal(r)
	store.limits.TTL = time.Millisecond
	source, err := store.SaveFormatted(principal, "expiring", contentFreeTextOrigin, "text", "TTL_MARKER\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Source("other-principal", source.ID); err == nil {
		t.Fatal("another principal saw the source")
	}
	time.Sleep(5 * time.Millisecond)
	if _, err = store.Source(principal, source.ID); err == nil {
		t.Fatal("the expired source stayed visible")
	}
	if hits, err := store.Search(principal, source.ID, "TTL_MARKER", 5); err != nil || len(hits) != 0 {
		t.Fatalf("searching an expired source failed: %v %v", hits, err)
	}
}

// The content store survives a restart: a stored handle is still readable and
// its index is not rebuilt.
func TestStoredHandleSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.db")
	store, err := OpenContentStore(path, ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.SaveFormatted("owner", "kept", contentFreeTextOrigin, "text", "RESTART_MARKER\nlast line\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenContentStore(path, ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hits, err := store.Search("owner", source.ID, "RESTART_MARKER", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("the stored handle did not survive the restart: %v %v", hits, err)
	}
	rows, err := store.SearchIndexRows("owner", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rows != source.Chunks {
		t.Fatalf("the restart rebuilt the index: %d rows, want %d", rows, source.Chunks)
	}
}

// The executor stores an oversized result in full and hands back a small
// descriptor. The first search builds the index; a repeated search does not
// rebuild it, and the source tool is not called again.
func TestOversizedExecResultIsSavedUnindexedAndIndexedOnFirstSearch(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := codeModeRun(t, s, r, map[string]any{
		"code":             "return 'MARKER_START\\n'+('padding line for the log\\n'.repeat(20000))+'MARKER_END\\n';",
		"max_output_bytes": 4096,
	})
	if status != http.StatusOK || firstString(got, "status") != "completed" {
		t.Fatalf("the oversized result was not stored: %d %#v", status, got)
	}
	result := mapValue(got["result"])
	for _, key := range []string{"source", "bytes", "format", "complete", "indexed", "expires_at"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("the descriptor is missing %q: %#v", key, result)
		}
	}
	if !truthy(result["saved"]) || !truthy(result["complete"]) {
		t.Fatalf("the descriptor does not report a saved complete result: %#v", result)
	}
	if truthy(result["indexed"]) {
		t.Fatalf("saving built the index: %#v", result)
	}
	source := firstString(result, "source")
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	principal := operationPrincipal(r)
	rows, err := store.SearchIndexRows(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("saving created %d index rows", rows)
	}
	meta, err := store.Source(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Indexed {
		t.Fatal("the stored source is already indexed")
	}

	// The first search builds the index.
	hits, err := store.Search(principal, source, "MARKER_END", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("the first search did not find the marker: %v %v", hits, err)
	}
	rows, err = store.SearchIndexRows(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	if rows != meta.Chunks {
		t.Fatalf("the first search indexed %d rows, want %d", rows, meta.Chunks)
	}
	// A repeated search does not rebuild it.
	for i := 0; i < 3; i++ {
		if _, err = store.Search(principal, source, "MARKER_START", 5); err != nil {
			t.Fatal(err)
		}
	}
	again, err := store.SearchIndexRows(principal, source)
	if err != nil {
		t.Fatal(err)
	}
	if again != rows {
		t.Fatalf("a repeated search rebuilt the index: %d rows became %d", rows, again)
	}
}

// The descriptor names the concrete calls the model should use.
func TestOversizedResultDescriptorNamesTheReadAndSearchCalls(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := codeModeRun(t, s, r, map[string]any{
		"code": "return 'x'.repeat(200000);", "max_output_bytes": 4096,
	})
	if status != http.StatusOK {
		t.Fatalf("the oversized result was not stored: %d %#v", status, got)
	}
	result := mapValue(got["result"])
	source := firstString(result, "source")
	for _, key := range []string{"read", "search", "index"} {
		entry := mapValue(result[key])
		if firstString(entry, "call") == "" {
			t.Fatalf("the descriptor does not name the %s call: %#v", key, result)
		}
		args := mapValue(entry["args"])
		if firstString(args, "source") != source {
			t.Fatalf("the %s call does not point at the stored source: %#v", key, entry)
		}
	}
	readArgs := mapValue(mapValue(result["read"])["args"])
	if intFromAny(readArgs["chunk_id"]) != 1 {
		t.Fatalf("the read call does not name a chunk: %#v", result["read"])
	}
}

// A result that cannot be stored fails honestly and reports the operations
// that already executed. The script is not run again.
func TestUnstorableResultFailsHonestly(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := codeModeRun(t, s, r, map[string]any{
		"code": "await mcp.call('hub','demo',{}); return 'y'.repeat(3000000);", "max_output_bytes": 4096,
	})
	if status == http.StatusOK || firstString(got, "status") == "completed" {
		t.Fatalf("an unstorable result reported success: %d %#v", status, got)
	}
	failure := mapValue(got["error"])
	if code := firstString(failure, "code"); code != "content_storage_limit" {
		t.Fatalf("unexpected failure code %q: %#v", code, failure)
	}
	if intFromAny(failure["calls_completed"]) != 1 {
		t.Fatalf("the failure did not report the executed operations: %#v", failure)
	}
	if len(mapValue(failure["receipt"])) == 0 {
		t.Fatalf("the failure did not keep the receipt of the executed call: %#v", failure)
	}
}

// A read-only run never stores an oversized result: it must not write into
// the user's sources.
func TestReadOnlyRunDoesNotStoreAnOversizedResult(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := s.callCodeModeToolMode(r, map[string]any{
		"code": "return 'z'.repeat(200000);", "max_output_bytes": 1024,
	}, true)
	if status == http.StatusOK || firstString(got, "status") == "completed" {
		t.Fatalf("a read-only run stored an oversized result: %d %#v", status, got)
	}
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.Sources(operationPrincipal(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("a read-only run wrote into the stored sources: %#v", sources)
	}
}

// The exec result boundary keeps the logs and the receipts of the run.
func TestOversizedResultKeepsTheLogs(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := codeModeRun(t, s, r, map[string]any{
		"code": "console.log('kept'); return 'x'.repeat(200000);", "max_output_bytes": 4096,
	})
	if status != http.StatusOK {
		t.Fatalf("the oversized result was not stored: %d %#v", status, got)
	}
	logs := outputRows(got["logs"])
	if len(logs) != 1 {
		t.Fatalf("the logs were lost: %#v", got["logs"])
	}
}

// The small result boundary is unchanged: the value comes back exactly as it
// was returned, with no descriptor and no store write.
func TestSmallResultIsReturnedExactlyAsBefore(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := codeModeRun(t, s, r, map[string]any{"code": "return {sum:1+1,list:[1,2,3]};"})
	if status != http.StatusOK || firstString(got, "status") != "completed" {
		t.Fatalf("the small result failed: %d %#v", status, got)
	}
	result := mapValue(got["result"])
	if intFromAny(result["sum"]) != 2 || len(outputRows(result["list"])) != 3 {
		t.Fatalf("the small result changed: %#v", result)
	}
	if _, ok := result["saved"]; ok {
		t.Fatalf("a small result produced a descriptor: %#v", result)
	}
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := store.Sources(operationPrincipal(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("a small result wrote into the store: %#v", sources)
	}
}

// The store is not bound to a one-shot runtime: the handle stays usable after
// the run that produced it is over.
func TestStoredHandleOutlivesTheRun(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	got, status := codeModeRun(t, s, r, map[string]any{
		"code": "return 'p'.repeat(200000);", "max_output_bytes": 4096,
	})
	if status != http.StatusOK {
		t.Fatalf("the oversized result was not stored: %d %#v", status, got)
	}
	source := firstString(mapValue(got["result"]), "source")
	read, status := codeModeRun(t, s, r, map[string]any{"code": `return await mcp.content.read(` + strconv.Quote(source) + `,1);`})
	if status != http.StatusOK || firstString(read, "status") != "completed" {
		t.Fatalf("the stored handle was not readable in a later run: %d %#v", status, read)
	}
}
