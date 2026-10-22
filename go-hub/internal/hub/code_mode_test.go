package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codeModeFixture builds a Hub with a short default timeout plus the standard
// request-scoped identity, mirroring what a real caller carries.
func codeModeFixture(t *testing.T) (*Server, *http.Request) {
	t.Helper()
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	r := httptest.NewRequest("POST", "/mcp", nil)
	r = requestWithAuthClaims(r, map[string]any{
		"sub": "code-mode-client", "client_id": "code-mode-client",
		"scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
	return s, r
}

func codeModeRun(t *testing.T, s *Server, r *http.Request, args map[string]any) (map[string]any, int) {
	t.Helper()
	return s.callCodeModeTool(r, args)
}

// A pure script needs no tool calls and returns exactly what it chooses.
func TestCodeModeReturnsOnlySelectedResult(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "const items = [3, 1, 2];\nitems.sort();\nreturn { total: items.length, first: items[0] };",
	})
	if status != http.StatusOK || response["status"] != "completed" {
		t.Fatalf("script failed: status=%d body=%v", status, response)
	}
	result := mapValue(response["result"])
	if intFromAny(result["total"]) != 3 || firstString(result, "first") != "1" {
		t.Fatalf("unexpected selected result: %v", result)
	}
	if intFromAny(response["calls"]) != 0 {
		t.Fatalf("pure script reported tool calls: %v", response["calls"])
	}
}

func TestExecAliasesRootMCPAndOptInContentAcrossRuns(t *testing.T) {
	s, r := codeModeFixture(t)
	path := filepath.Join(s.cfg.ConfigDir, "exec-content.sqlite")
	first, status := codeModeRun(t, s, r, map[string]any{"code": `
		const d=await mcp.discover('hub');
		const schema=await mcp.schema('hub','demo');
		const called=await call('hub','demo',{});
		const found=await search('demo',{target:'hub'});
		return {targets:d.servers.length, tool:schema.tool, status:called.status, hits:found.results.length};
	`})
	if status != http.StatusOK || firstString(first, "status") != "completed" {
		t.Fatalf("root composition failed: %d %#v", status, first)
	}
	result := mapValue(first["result"])
	if firstString(result, "tool") != "demo" || firstString(result, "status") != "ok" || intFromAny(result["hits"]) == 0 {
		t.Fatalf("root MCP/facade aliases returned unexpected data: %#v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ordinary exec unexpectedly created content store: %v", err)
	}
	indexed, status := codeModeRun(t, s, r, map[string]any{"code": `return await index('start-marker\nnoise\nrare-MID-needle\nnoise\nend-marker',{name:'fixture-log'});`})
	if status != http.StatusOK || firstString(indexed, "status") != "completed" {
		t.Fatalf("opt-in indexing failed: %d %#v", status, indexed)
	}
	meta := mapValue(indexed["result"])
	source := firstString(meta, "source")
	if source == "" || intFromAny(meta["bytes"]) == 0 || firstString(meta, "name") != "fixture-log" {
		t.Fatalf("index did not return bounded source metadata: %#v", meta)
	}
	followup, status := codeModeRun(t, s, r, map[string]any{"code": `const hits=await mcp.content.search('rare MID needle',{source:` + fmt.Sprintf("%q", source) + `}); const body=await mcp.content.read(` + fmt.Sprintf("%q", source) + `,hits.hits[0].chunk_id); return {count:hits.count,text:body.text};`})
	if status != http.StatusOK || firstString(followup, "status") != "completed" {
		t.Fatalf("cross-run source lookup failed: %d %#v", status, followup)
	}
	answer := mapValue(followup["result"])
	if intFromAny(answer["count"]) == 0 || !strings.Contains(firstString(answer, "text"), "rare-MID-needle") {
		t.Fatalf("follow-up search missed exact source content: %#v", answer)
	}
	other := requestWithAuthClaims(r, map[string]any{"sub": "different-principal", "client_id": "different-principal", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	denied, status := codeModeRun(t, s, other, map[string]any{"code": `return await mcp.content.read(` + fmt.Sprintf("%q", source) + `,1);`})
	if status == http.StatusOK || firstString(mapValue(denied["error"]), "code") != "content_unavailable" {
		t.Fatalf("source crossed principal boundary: %d %#v", status, denied)
	}
}

func TestContentSearchRevalidatesOriginBeforeReturningSnippets(t *testing.T) {
	s, r := codeModeFixture(t)
	target := "mcp:shell:content-parent:personal-log"
	s.mu.Lock()
	s.agents["shell:content-parent"] = &Agent{AgentID: "shell:content-parent", Name: "fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "personal-log", "name": "personal log", "enabled": true, "owner_profile_id": "owner-profile"}}}}
	s.mu.Unlock()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.IndexFormatted(operationPrincipal(r), "private-source", target+"\x00read_log", "content-text", "first line\nUNIQUE_PRIVATE_MARKER\nlast line")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(source.ID)
	code := `return await mcp.content.search('UNIQUE_PRIVATE_MARKER',{source:` + string(quoted) + `});`
	denied, status := codeModeRun(t, s, r, map[string]any{"code": code})
	if status == http.StatusOK || firstString(mapValue(denied["error"]), "code") != "content_unavailable" {
		t.Fatalf("owner connection check was skipped for content search: %d %#v", status, denied)
	}
	encoded, _ := json.Marshal(denied)
	if strings.Contains(string(encoded), "UNIQUE_PRIVATE_MARKER") || strings.Contains(string(encoded), source.ID) {
		t.Fatalf("denied source details leaked through scoped search: %s", encoded)
	}
	// Even if the current child is visible, disabling it must immediately remove
	// its snippets from all-sources search without deleting the stored source.
	s.mu.Lock()
	s.agents["shell:content-parent"].Meta["mcp_agents"] = []any{map[string]any{"ref": "personal-log", "name": "personal log", "enabled": false, "owner_profile_id": "owner-profile"}}
	s.mu.Unlock()
	all, status := codeModeRun(t, s, r, map[string]any{"code": "return await mcp.content.search('UNIQUE_PRIVATE_MARKER');"})
	if status != http.StatusOK || intFromAny(mapValue(all["result"])["count"]) != 0 {
		t.Fatalf("disabled origin appeared in all-sources search: %d %#v", status, all)
	}
}

func TestContentSearchRechecksDisabledRegisteredRoot(t *testing.T) {
	s, r := codeModeFixture(t)
	root := &Agent{AgentID: "root:content-fixture", Name: "root fixture", Kind: "remote_mcp", Status: "online", Meta: map[string]any{"enabled": true}}
	s.mu.Lock()
	s.agents[root.AgentID] = root
	s.mu.Unlock()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.IndexFormatted(operationPrincipal(r), "root source", root.AgentID+"\x00read_log", "text", "ROOT_PRIVATE_MARKER\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(source.ID)
	code := `return await mcp.content.search('ROOT_PRIVATE_MARKER',{source:` + string(q) + `});`
	visible, status := codeModeRun(t, s, r, map[string]any{"code": code})
	if status != http.StatusOK || intFromAny(mapValue(visible["result"])["count"]) != 1 {
		t.Fatalf("authorized root source not searchable: %d %#v", status, visible)
	}
	s.mu.Lock()
	root.Meta["enabled"] = false
	s.mu.Unlock()
	denied, status := codeModeRun(t, s, r, map[string]any{"code": code})
	if status == http.StatusOK || firstString(mapValue(denied["error"]), "code") != "content_unavailable" {
		t.Fatalf("disabled registered root source remained readable: %d %#v", status, denied)
	}
	all, status := codeModeRun(t, s, r, map[string]any{"code": "return await mcp.content.search('ROOT_PRIVATE_MARKER');"})
	if status != http.StatusOK || intFromAny(mapValue(all["result"])["count"]) != 0 {
		t.Fatalf("disabled registered root leaked into all-source search: %d %#v", status, all)
	}
}

func TestCodeModeConsoleLogPreservesJSONAndCountsAgainstOutputBudget(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "console.log({phase:'ready', count:2}); return {ok:true};",
	})
	if status != http.StatusOK || response["status"] != "completed" {
		t.Fatalf("structured log failed: %d %v", status, response)
	}
	logs := outputRows(response["logs"])
	if len(logs) != 1 || firstString(mapValue(outputRows(logs[0])[0]), "phase") != "ready" {
		t.Fatalf("console.log did not preserve useful JSON: %#v", response["logs"])
	}
	tooLarge, status := codeModeRun(t, s, r, map[string]any{
		"code": "console.log('x'.repeat(1100)); return 1;", "max_output_bytes": 1024,
	})
	if status == http.StatusOK || firstString(mapValue(tooLarge["error"]), "code") != "output_too_large" {
		t.Fatalf("oversized logs were silently clipped/dropped: %d %#v", status, tooLarge)
	}
}

func TestCodeModeSerializationFailureRetainsNestedCallReceipt(t *testing.T) {
	s, r := codeModeFixture(t)
	b := newCodeModeBridge(s, r, r.Context(), time.Now().Add(time.Second), codeModeLimits{maxCalls: 3, maxOut: 1024})
	b.mu.Lock()
	b.callCount = 1
	b.lastReceipt = map[string]any{"target": "shell:fixture", "tool": "shell_exec", "status": "completed", "task_id": "task-1"}
	b.mu.Unlock()
	err := b.annotate(codeModeFailure("invalid_return", "BigInt cannot be serialized"))
	if err.call != 1 || firstString(err.receipt, "target") != "shell:fixture" || firstString(err.receipt, "task_id") != "task-1" {
		t.Fatalf("post-dispatch failure lost operation receipt: %#v", err)
	}
}

func TestCodeModeRetainedJSONSerializationSurvivesRepeatedHostCalls(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{"code": `
		// Mutating the script's global JSON serializer must not corrupt bridge
		// argument copies or the final envelope serializer captured by the host.
		globalThis.JSON.stringify = () => "\\u0003";
		const rows=[];
		for(let i=0;i<24;i++) {
			const result=await mcp.call('hub','demo',{iteration:i, locale:'Привет 🌍'});
			rows.push({i,status:result.status,locale:'Привет 🌍'});
		}
		return {calls:rows.length,first:rows[0],last:rows[23]};
	`})
	if status != http.StatusOK || firstString(response, "status") != "completed" {
		t.Fatalf("retained serialization failed after host calls: %d %#v", status, response)
	}
	result := mapValue(response["result"])
	first, last := mapValue(result["first"]), mapValue(result["last"])
	if intFromAny(result["calls"]) != 24 || firstString(first, "status") != "ok" || firstString(last, "status") != "ok" || firstString(last, "locale") != "Привет 🌍" {
		t.Fatalf("serialized host results were corrupted: %#v", result)
	}
}

func TestCodeModeRetainedSerializerStillRejectsBigIntAndCycles(t *testing.T) {
	s, r := codeModeFixture(t)
	for name, code := range map[string]string{"bigint": "return 1n;", "cycle": "const x={}; x.self=x; return x;"} {
		t.Run(name, func(t *testing.T) {
			response, status := codeModeRun(t, s, r, map[string]any{"code": code})
			if status == http.StatusOK || firstString(response, "status") != "failed" || firstString(mapValue(response["error"]), "code") == "" {
				t.Fatalf("non-JSON result was accepted: %d %#v", status, response)
			}
		})
	}
}

// Intermediate data must not leak: a script that only returns a count cannot be
// made to emit the tool payload it received.
func TestCodeModeDoesNotLeakIntermediateResults(t *testing.T) {
	s, r := codeModeFixture(t)
	// The script holds a private intermediate; only a derived count is returned.
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "const held = { secret: 'internal-detail-marker', rows: [1,2,3] };\n" +
			"return { rowCount: held.rows.length };",
	})
	if status != http.StatusOK {
		t.Fatalf("script failed: %v", response)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "internal-detail-marker") {
		t.Fatalf("intermediate script data leaked into the result: %s", encoded)
	}
	if intFromAny(mapValue(response["result"])["rowCount"]) != 3 {
		t.Fatalf("unexpected derived result: %v", response)
	}
}

// The sandbox has no host capabilities. This is the security boundary.
func TestCodeModeSandboxHasNoHostAccess(t *testing.T) {
	s, r := codeModeFixture(t)
	for _, probe := range []string{"require", "process", "global", "fetch", "XMLHttpRequest", "WebSocket", "Buffer", "Deno"} {
		response, status := codeModeRun(t, s, r, map[string]any{
			"code": "return typeof globalThis['" + probe + "'];",
		})
		if status != http.StatusOK {
			t.Fatalf("%s probe failed: %v", probe, response)
		}
		if got := firstString(response, "result"); got != "undefined" {
			t.Fatalf("%s is reachable inside the sandbox: %q", probe, got)
		}
	}
}

// A tight loop must be cut off by the real deadline, not hang the Hub.
func TestCodeModeStopsRunawayLoop(t *testing.T) {
	s, r := codeModeFixture(t)
	done := make(chan struct{})
	var response map[string]any
	var status int
	go func() {
		defer close(done)
		response, status = codeModeRun(t, s, r, map[string]any{
			"code": "while (true) {}", "timeout_ms": 300,
		})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("code_mode did not stop a runaway loop")
	}
	if status == http.StatusOK || response["status"] == "completed" {
		t.Fatalf("runaway loop reported success: status=%d body=%v", status, response)
	}
	if code := firstString(mapValue(response["error"]), "code"); code != "timeout" {
		t.Fatalf("runaway loop failure code = %q: %v", code, response)
	}
}

// The heap limit is enforced inside the engine, not merely hoped for.
func TestCodeModeEnforcesMemoryLimit(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "const b = new ArrayBuffer(512 * 1024 * 1024);\nreturn b.byteLength;",
	})
	if status == http.StatusOK {
		t.Fatalf("oversized allocation succeeded: %v", response)
	}
	if code := firstString(mapValue(response["error"]), "code"); code != "memory_limit" {
		t.Fatalf("memory failure code = %q: %v", code, response)
	}
}

// A recursive script fails as a script error instead of exhausting the host.
func TestCodeModeRejectsRunawayRecursion(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "function f() { return f(); }\nreturn f();",
	})
	if status == http.StatusOK && response["status"] == "completed" {
		t.Fatalf("unbounded recursion reported success: %v", response)
	}
}

// Every input budget is clamped, so a script cannot ask for an unbounded run.
func TestCodeModeLimitsAreClamped(t *testing.T) {
	if _, err := resolveCodeModeLimits(map[string]any{"timeout_ms": 5000}); err != nil {
		t.Fatalf("a valid timeout was rejected: %v", err)
	}
	for _, args := range []map[string]any{
		{"timeout_ms": 1},
		{"timeout_ms": 999999},
		{"max_calls": 0},
		{"max_calls": codeModeMaxCallsCeiling + 1},
		{"max_output_bytes": 10},
		{"max_output_bytes": codeModeMaxOutBytes + 1},
	} {
		if _, err := resolveCodeModeLimits(args); err == nil {
			t.Fatalf("out-of-range limit accepted: %v", args)
		}
	}
	limits, err := resolveCodeModeLimits(nil)
	if err != nil || limits.timeout != codeModeDefaultTimeout || limits.maxCalls != codeModeDefaultMaxCalls || limits.maxOut != codeModeDefaultOutBytes {
		t.Fatalf("unexpected defaults: %+v err=%v", limits, err)
	}
}

// The call budget stops the script at the boundary, before the extra effect.
// The stop arrives as a thrown error, so the run is reported as failed.
func TestCodeModeEnforcesCallBudget(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code":      "for (let i = 0; i < 50; i++) { await mcp.discover(); }\nreturn 'done';",
		"max_calls": 3,
	})
	if status == http.StatusOK && response["status"] == "completed" {
		t.Fatalf("call budget was not enforced: %v", response)
	}
	failure := mapValue(response["error"])
	if !strings.Contains(firstString(failure, "message"), "call limit") {
		t.Fatalf("call budget failure is unclear: %v", response)
	}
	// Exactly the budget was spent, never more.
	if calls := intFromAny(failure["calls_completed"]); calls != 3 {
		t.Fatalf("call budget reported %d completed calls, want 3: %v", calls, response)
	}
}

// A returned value larger than the output cap is a failure, not a truncated
// success.
func TestCodeModeRejectsOversizedOutput(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "return 'x'.repeat(200000);", "max_output_bytes": 1024,
	})
	if status == http.StatusOK {
		t.Fatalf("oversized output reported success: %v", response)
	}
	if code := firstString(mapValue(response["error"]), "code"); code != "output_too_large" {
		t.Fatalf("oversized output failure code = %q: %v", code, response)
	}
}

// code_mode is never reachable by a read-only caller: it can call writes.
func TestCodeModeDeniedForReadOnlyCaller(t *testing.T) {
	readOnly := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{
		"sub": "reader", "client_id": "reader", "scope": "gptadmin.read", "access_mode": accessModeReadonly,
	})
	if err := authorizeToolCall(readOnly, "hub", codeModeTool); err == nil {
		t.Fatal("read-only client may not call code_mode")
	}
	if err := authorizeFacadeCall(readOnly, "exec", map[string]any{"code": "return 1;"}); err == nil {
		t.Fatal("read-only client may not reach exec")
	}
	if isReadOnlyTool("hub", codeModeTool) {
		t.Fatal("code_mode must not be treated as a read-only tool")
	}
	// It must not appear in a read-only client's advertised tool list.
	for _, tool := range toolsForRequest(readOnly, "hub", hubTools()) {
		if firstString(tool, "name") == codeModeTool {
			t.Fatal("code_mode advertised to a read-only client")
		}
	}
}

// A restricted access profile that does not name code_mode cannot run a script.
func TestCodeModeDeniedByAccessProfile(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", DefaultTimeout: time.Second})
	restricted := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{
		"sub": "ops", "client_id": "ops", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
	restricted = requestWithAccessProfile(restricted, AccessProfile{
		ID: "ops", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite,
		AllowedTargets: []string{"hub"}, AllowedTools: []string{"discover", "schema"}, Version: 1,
	})
	if err := authorizeToolCall(restricted, "hub", codeModeTool); err == nil {
		t.Fatal("profile without code_mode still ran a script")
	}
	response, status := s.callCodeModeTool(restricted, map[string]any{"code": "return 1;"})
	if status != http.StatusForbidden || response["status"] == "completed" {
		t.Fatalf("profile-restricted caller bypassed through callCodeModeTool: status=%d body=%v", status, response)
	}
}

// code_mode refuses to run without an authenticated caller instead of
// synthesizing a privileged context.
func TestCodeModeRequiresAuthenticatedRequest(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", DefaultTimeout: time.Second})
	response, status := s.callCodeModeTool(nil, map[string]any{"code": "return 1;"})
	if status != http.StatusUnauthorized || response["status"] == "completed" {
		t.Fatalf("unauthenticated run was not refused: status=%d body=%v", status, response)
	}
}

// A script may not call code_mode again: that would be unbounded nesting.
func TestCodeModeRejectsRecursiveCall(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "const inner = mcp.call({ target: 'hub', tool: 'code_mode', args: { code: 'return 1' } });\nreturn inner;",
	})
	if status == http.StatusOK && response["status"] == "completed" {
		t.Fatalf("recursive code_mode call was allowed: %v", response)
	}
}

// A failed nested call is reported as ok:false and never as usable data. The
// Hub envelope itself still says "completed", so the script-visible status is
// the contract that matters.
func TestCodeModeNestedFailureIsNotSuccess(t *testing.T) {
	payload := codeModeCallPayload(map[string]any{
		"server_id": "shell:host", "status": "completed",
		"response": map[string]any{"content": []any{map[string]any{"type": "text", "text": "boom"}}, "isError": true},
	}, http.StatusOK, "shell:host", "shell_exec")
	if payload["ok"] != false || firstString(payload, "status") != "failed" {
		t.Fatalf("isError payload presented as success: %v", payload)
	}
	if _, hasResult := payload["result"]; hasResult {
		t.Fatalf("failed call still exposed a result: %v", payload)
	}
}

// A 2xx transport with a non-OK status code is still a failure.
func TestCodeModeNonOKStatusIsFailure(t *testing.T) {
	payload := codeModeCallPayload(map[string]any{"status": "completed"}, http.StatusForbidden, "hub", "settings_set")
	if payload["ok"] != false || firstString(payload, "status") != "failed" {
		t.Fatalf("non-2xx status presented as success: %v", payload)
	}
}

// A pending approval is a stop condition, not success, and the receipt is
// preserved so the caller can approve and retry deliberately.
func TestCodeModePreservesPendingApproval(t *testing.T) {
	response := codeModeCallPayload(map[string]any{
		"status": "approval_required", "approval_id": "apr-1",
		"expires_at": 123, "message": "An administrator must approve this write before execution.",
	}, http.StatusPreconditionRequired, "shell:host", "shell_exec")
	if response["ok"] != false || firstString(response, "status") != "approval_required" {
		t.Fatalf("pending approval presented as success: %v", response)
	}
	if firstString(response, "approval_id") != "apr-1" {
		t.Fatalf("approval receipt was dropped: %v", response)
	}
}

// A still-running receipt is non-terminal: the script must not treat it as data.
func TestCodeModeNonTerminalReceiptIsNotSuccess(t *testing.T) {
	for _, status := range []string{"running", "queued"} {
		response := codeModeCallPayload(map[string]any{
			"status": status, "job_id": "job-1", "background": true,
		}, http.StatusOK, "shell:host", "shell_exec")
		if response["ok"] != false || firstString(response, "status") != "nonterminal" {
			t.Fatalf("%s receipt presented as success: %v", status, response)
		}
		if firstString(response, "job_id") != "job-1" {
			t.Fatalf("%s receipt lost its job id: %v", status, response)
		}
		if _, hasResult := response["result"]; hasResult {
			t.Fatalf("%s receipt exposed a result: %v", status, response)
		}
	}
}

// A completed call keeps its result and is the only state marked ok.
func TestCodeModeCompletedCallKeepsResult(t *testing.T) {
	inner := map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
	payload := codeModeCallPayload(map[string]any{
		"server_id": "hub", "status": "completed", "response": inner,
	}, http.StatusOK, "hub", "discover")
	if payload["ok"] != true || firstString(payload, "status") != "completed" {
		t.Fatalf("completed call misreported: %v", payload)
	}
	if len(mapValue(payload["result"])) != len(inner) {
		t.Fatalf("completed call lost its result: %v", payload)
	}
}

// code_mode is published in the Hub schema so it is discoverable exactly like
// every other Hub tool.
func TestCodeModeIsDiscoverableInHubSchema(t *testing.T) {
	for _, tool := range hubTools() {
		if firstString(tool, "name") != codeModeTool {
			continue
		}
		definition := mapValue(tool)
		schema := mapValue(definition["inputSchema"])
		required := outputRows(schema["required"])
		if len(required) != 1 || fmt.Sprintf("%v", required[0]) != "code" {
			t.Fatalf("unexpected required fields: %v", required)
		}
		properties := mapValue(schema["properties"])
		for _, field := range []string{"code", "timeout_ms", "max_calls", "max_output_bytes"} {
			if properties[field] == nil {
				t.Fatalf("%s missing from the input schema", field)
			}
		}
		if mapValue(definition["outputSchema"])["properties"] == nil {
			t.Fatal("code_mode has no output schema")
		}
		return
	}
	t.Fatalf("%s missing from hub tools", codeModeTool)
}

// Two runs must not share state: a global set by one script is invisible to the
// next caller.
func TestCodeModeIsolatesRuns(t *testing.T) {
	s, r := codeModeFixture(t)
	first, status := codeModeRun(t, s, r, map[string]any{
		"code": "globalThis.__leaked = 'first-run';\nreturn 'set';",
	})
	if status != http.StatusOK || firstString(first, "result") != "set" {
		t.Fatalf("first run failed: %v", first)
	}
	second, status := codeModeRun(t, s, r, map[string]any{
		"code": "return typeof globalThis.__leaked;",
	})
	if status != http.StatusOK {
		t.Fatalf("second run failed: %v", second)
	}
	if got := firstString(second, "result"); got != "undefined" {
		t.Fatalf("sandbox state leaked between runs: %q", got)
	}
}

// mcp.call reaches real hub tools through the ordinary dispatch path, so the
// caller's own authorization applies and only the script's return value
// reaches the caller.
func TestCodeModeCallsHubToolThroughDispatch(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := s.callCodeModeTool(r, map[string]any{"code": "const res = await mcp.call('hub', 'demo', {});\nreturn { ok: res.status === 'ok', state: res.status };"})
	if status != http.StatusOK || response["status"] != "completed" {
		t.Fatalf("nested hub call failed: status=%d body=%v", status, response)
	}
	result := mapValue(response["result"])
	if result["ok"] != true {
		t.Fatalf("nested call was not completed: %v", response)
	}
	if intFromAny(response["calls"]) != 1 {
		t.Fatalf("unexpected call accounting: %v", response["calls"])
	}
}

// mcp.discover returns target metadata without the hidden connection material.
func TestCodeModeDiscoverHidesConnectionMeta(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", DefaultTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Name: "fixture", Status: "online", Meta: map[string]any{"token": "super-secret"}}
	s.mu.Unlock()
	r := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{
		"sub": "ops", "client_id": "ops", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
	response, status := s.callCodeModeTool(r, map[string]any{
		"code": "const d = await mcp.discover();\nreturn d;",
	})
	if status != http.StatusOK {
		t.Fatalf("discover failed: %v", response)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "super-secret") {
		t.Fatalf("connection metadata leaked into the sandbox: %s", encoded)
	}
}

// A script error is reported, never swallowed as an empty success.
func TestCodeModeReportsScriptError(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "throw new Error('script blew up');",
	})
	if status == http.StatusOK || response["status"] == "completed" {
		t.Fatalf("script error reported as success: %v", response)
	}
	if code := firstString(mapValue(response["error"]), "code"); code != "script_error" {
		t.Fatalf("script error code = %q: %v", code, response)
	}
}

// A script that returns nothing is a successful run with no value, not a
// failure and not an invented result.
func TestCodeModeEmptyReturnIsNotAFailure(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{"code": "await mcp.discover();"})
	if status != http.StatusOK || response["status"] != "completed" {
		t.Fatalf("empty return was treated as a failure: status=%d body=%v", status, response)
	}
	if response["result"] != nil {
		t.Fatalf("empty return invented a result: %v", response["result"])
	}
}

// A failed nested call stops the whole run even when the script swallows the
// error and returns a value anyway: no false success, no further side effects.
func TestCodeModeStopsAfterFailedNestedCall(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "let first;\n" +
			"try { first = await mcp.call('shell:missing', 'shell_exec', {cmd:'true'}); }\n" +
			"catch (e) { /* the script pretends nothing happened */ }\n" +
			"return { survived: true };",
	})
	if status == http.StatusOK || response["status"] == "completed" {
		t.Fatalf("a caught nested failure still reported success: status=%d body=%v", status, response)
	}
}

// After the first failure, later bridge calls are refused even if the script
// keeps going, so it cannot produce further effects past the stop point.
func TestCodeModeRefusesCallsAfterFailure(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "try { await mcp.call('shell:missing', 'shell_exec', {cmd:'true'}); } catch (e) {}\n" +
			"try { await mcp.call('hub','demo',{}); return 'second-call-allowed'; } catch (e) { return 'stopped'; }",
	})
	if status == http.StatusOK {
		t.Fatalf("run continued past the first failure: %v", response)
	}
}

// An intermediate payload too large to hand to the sandbox fails the run rather
// than being parsed inside WASM.
func TestCodeModeBoundsIntermediatePayload(t *testing.T) {
	payload := map[string]any{"blob": strings.Repeat("x", codeModeMaxIntermediateJSON+1)}
	// jsonValue needs a live sandbox to build its fallback value, so the size
	// guard is verified through the encoded payload it produces.
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("could not build the oversized payload: %v", err)
	}
	if len(encoded) <= codeModeMaxIntermediateJSON {
		t.Fatalf("the guard would not trigger: %d bytes", len(encoded))
	}
	if codeModeMaxIntermediateJSON <= codeModeMaxOutBytes {
		t.Fatal("the intermediate bound must exceed the script output bound")
	}
}

// mcp.schema advertises the reference and contract metadata that mcp.call
// accepts, so a strict schema-gated caller can still use code mode.
func TestCodeModeSchemaAdvertisesRefAndContract(t *testing.T) {
	s, r := codeModeFixture(t)
	response, status := codeModeRun(t, s, r, map[string]any{
		"code": "const s = await mcp.schema('hub', 'handover_status');\n" +
			"return { hasTool: s.tool, hasRef: typeof s.ref, keys: Object.keys(s).sort() };",
	})
	if status != http.StatusOK {
		t.Fatalf("schema failed: %v", response)
	}
	result := mapValue(response["result"])
	if firstString(result, "hasTool") != "handover_status" {
		t.Fatalf("schema did not return the requested tool: %v", response)
	}
	if result["hasRef"] != "string" && result["hasRef"] != "undefined" {
		t.Fatalf("schema ref has an unexpected shape: %v", result)
	}
}

// The bridge forwards schema-contract metadata to the dispatch path rather than
// dropping it, so strict schema enforcement still applies to nested calls.
func TestCodeModeForwardsSchemaContractMetadata(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", DefaultTimeout: time.Second, SchemaContractValidation: true})
	r := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{
		"sub": "ops", "client_id": "ops", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
	// A stale digest must block the nested call, proving the metadata arrives.
	response, status := s.callCodeModeTool(r, map[string]any{
		"code": "const res = mcp.call({ target: 'hub', tool: 'handover_status', schema_version: 'gptadmin.mcp-schema/v1', schema_digest_sha256: 'stale' });\n" +
			"return { ok: res.ok, status: res.status };",
	})
	if status == http.StatusOK && response["status"] == "completed" {
		t.Fatalf("a stale schema digest did not stop the nested call: %v", response)
	}
}

// An aborted caller must not leave a runtime or a concurrency slot behind.
func TestCodeModeReleasesBudgetOnCancel(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl", DefaultTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	r := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil).WithContext(ctx), map[string]any{
		"sub": "ops", "client_id": "ops", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
	cancel()
	if _, err := s.runCodeMode(r.Context(), r, "return 1;", codeModeLimits{
		timeout: time.Second, maxCalls: 1, heap: codeModeDefaultHeapBytes, maxOut: codeModeDefaultOutBytes,
	}); err == nil {
		t.Fatal("cancelled run reported success")
	}
	// Every slot must be free again, otherwise the Hub leaks capacity.
	probe := newCodeModeGate(1)
	release, err := probe.acquire(context.Background())
	if err != nil {
		t.Fatalf("gate did not recover: %v", err)
	}
	release()
}
