package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Two independent ordinary SQLite stores. The accepting Hub owns the result of
// a standalone executor; its shell target is intentionally different.
func TestOwnerLookupMCP(t *testing.T) {
	const origin = "https://owner-lookup.test"
	config := Config{PublicOrigin: origin, MCPResource: origin, OAuthClientSecret: newID(), ConfigDir: t.TempDir()}
	owner := New(config)
	t.Cleanup(func() { owner.Close() })
	config.ConfigDir = t.TempDir()
	entry := New(config)
	t.Cleanup(func() { entry.Close() })
	token := func(name string) string {
		x, err := owner.signJWT(map[string]any{"kid": defaultJWTKeyID, "iss": origin, "aud": origin, "resource": origin, "sub": "user", "client_id": name, "jti": name,
			"scope": "gptadmin.read gptadmin.exec", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	client, other := token("submitter"), token("different-client")
	request := httptest.NewRequest("POST", "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+client)
	if !owner.mcpAuth(httptest.NewRecorder(), request) {
		t.Fatal("fixture client rejected")
	}
	id, err := encodeOwnerHandle("task", "hub-b", "new-job")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"opaque-legacy", id} {
		owner.mu.Lock()
		owner.shellJobs[key] = &shellJob{ID: key, Server: "standalone-s21", ToolName: "shell_exec", Caller: taskCallerFromRequest(request),
			Status: "completed", CreatedAt: nowFloat() - 1, DoneAt: nowFloat(), Result: map[string]any{"stdout": "owner-proof"}}
		err = owner.saveTaskStateLocked(key)
		delete(owner.shellJobs, key)
		owner.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	// An ordinary pre-cutover source has no owner prefix.
	store, err := owner.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	legacySource, err := store.Index(operationPrincipal(request), "old", contentFreeTextOrigin, "legacy content marker")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RegisterLocalExecutor("owner-b", map[string]string{"server_id": "hub-b", "public_key": "test", "fingerprint": "test"}, strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}

	var reads, unrelated atomic.Int32
	handler := owner.Handler()
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer peer.Close()
	owner.cfg.NodePublicURL = peer.URL
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unrelated.Add(1)
		w.WriteHeader(500)
	}))
	defer decoy.Close()
	entry.nodePeers = map[string]nodePeerRoute{"shell:owner-b": {NodeID: "hub-b", URL: peer.URL}, "shell:other": {NodeID: "hub-c", URL: decoy.URL}}
	call := func(cred, method string, params map[string]any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
		r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+cred)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		entry.Handler().ServeHTTP(w, r)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("MCP status %d", w.Code)
		}
		return out
	}
	get := func(cred, key string, hint map[string]any) map[string]any {
		return call(cred, "tasks/get", map[string]any{"taskId": key, "_meta": map[string]any{"io.gptadmin/owner": hint,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}})
	}
	code := func(v map[string]any) string { return firstString(mapValue(mapValue(v["error"])["data"]), "code") }
	hint := map[string]any{"owner_target": "shell:owner-b", "owner_endpoint": peer.URL}
	for _, test := range []struct {
		id   string
		hint map[string]any
	}{{id, nil}, {"opaque-legacy", hint}, {"opaque-legacy", map[string]any{"owner_node_id": "hub-b"}}} {
		got := get(client, test.id, test.hint)
		data, _ := json.Marshal(got["result"])
		if !bytes.Contains(data, []byte("owner-proof")) || firstString(mapValue(got["result"]), "taskId") != test.id {
			t.Fatalf("exact read failed, code %q", code(got))
		}
	}
	if reads.Load() != 3 {
		t.Fatalf("three reads reached owner %d times", reads.Load())
	}
	history := get(client, "opaque-legacy", nil)
	if code(history) != "history_unavailable" || reads.Load() != 3 {
		t.Fatal("blind lookup contacted a peer or claimed absence")
	}
	missing := strings.TrimSuffix(id, "new-job") + "missing"
	absent := get(client, missing, nil)
	foreign := get(other, id, nil)
	a, _ := json.Marshal(absent["error"])
	b, _ := json.Marshal(foreign["error"])
	if code(absent) != "not_found" || !bytes.Equal(a, b) {
		t.Fatal("foreign record distinguishable from missing")
	}
	before := reads.Load()
	if code(get(client, id, map[string]any{"owner_id": "hub-c"})) != "invalid_arguments" || reads.Load() != before {
		t.Fatal("conflicting identity routed")
	}
	if code(get(client, "opaque-legacy", map[string]any{"owner_target": "shell:owner-b", "owner_endpoint": decoy.URL})) != "owner_unavailable" ||
		reads.Load() != before {
		t.Fatal("caller endpoint used as a destination")
	}
	js := "return mcp.content.readAll(" + strconv.Quote(legacySource.ID) + ",{owner_id:'hub-b'}).text;"
	readContent := func(cred string) map[string]any {
		return mapValue(mapValue(call(cred, "tools/call", map[string]any{"name": "exec", "arguments": map[string]any{"code": js}})["result"])["structuredContent"])
	}
	if got := readContent(client); firstString(got, "result") != "legacy content marker" {
		t.Fatalf("legacy content failed: %s", firstString(mapValue(got["error"]), "code"))
	}
	if got := readContent(other); firstString(mapValue(got["error"]), "code") != "not_found" {
		t.Fatal("content principal was not protected")
	}
	// Public static-peer configs support a target and URL without a node_id.
	entry.nodePeers["shell:owner-b"] = nodePeerRoute{URL: peer.URL}
	before = reads.Load()
	if code(get(client, id, nil)) != "owner_unavailable" || reads.Load() != before {
		t.Fatal("ID alone must not search unpinned routes")
	}
	if got := get(client, id, hint); firstString(mapValue(got["result"]), "taskId") != id {
		t.Fatalf("owner hint failed on URL-only route: %s", code(got))
	}
	foreignID, err := encodeOwnerHandle("task", "wrong-owner", "new-job")
	if err != nil {
		t.Fatal(err)
	}
	if got := get(client, foreignID, hint); code(got) != "owner_unavailable" {
		t.Fatal("unpinned route accepted a handle issued by a different owner")
	}
	entry.nodePeers["shell:owner-b"] = nodePeerRoute{NodeID: "hub-b", URL: peer.URL}
	peer.Close()
	offline := get(client, id, nil)
	if code(offline) != "owner_unavailable" || mapValue(mapValue(offline["error"])["data"])["retryable"] != true {
		t.Fatal("offline owner reported as missing")
	}
	for _, response := range []map[string]any{history, offline} {
		if !strings.Contains(firstString(mapValue(response["error"]), "message"), "send new commands to available executors") {
			t.Fatal("receipt error did not tell the caller that new commands are available")
		}
	}
	if unrelated.Load() != 0 {
		t.Fatalf("%d unrelated peer requests", unrelated.Load())
	}
}
