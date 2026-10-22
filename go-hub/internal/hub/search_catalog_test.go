package hub

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLiteFTSSearchNamesDescriptionsLabelsAndPunctuation(t *testing.T) {
	index, err := newToolSearchIndex()
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{
		{"target": "shell:fixture", "name": "shell_exec", "title": "Run command", "description": "Execute a command in this terminal", "labels": "shell fixture", "settings": ""},
		{"target": "control:node-a", "name": "mcp_manage", "title": "Child MCP lifecycle", "description": "Enable or disable a child service", "labels": "node control", "settings": ""},
		{"target": "control:hub", "name": "failover_config_get", "title": "Failover config", "description": "Read persisted promotion limits", "labels": "proxy webhook failover", "settings": ""},
	}
	checks := []struct{ query, target, want string }{{"mcp_manage", "control:node-a", "mcp_manage"}, {"persisted promotion", "control:hub", "failover_config_get"}, {"child-service", "control:node-a", "mcp_manage"}, {"shell_exec", "shell:fixture", "shell_exec"}}
	for _, check := range checks {
		hits, err := index.search(check.query, check.target, 10, rows)
		if err != nil {
			t.Fatalf("query %q: %v", check.query, err)
		}
		if len(hits) == 0 || firstString(hits[0], "name") != check.want {
			t.Fatalf("query %q hits=%v", check.query, hits)
		}
	}
	if hits, err := index.search("mcp_manage", "control:hub", 10, rows); err != nil || len(hits) != 0 {
		t.Fatalf("target filter leaked rows: hits=%v err=%v", hits, err)
	}
	if hits, err := index.search("!!! ___ ---", "", 10, rows); err != nil || len(hits) != 0 {
		t.Fatalf("punctuation query hits=%v err=%v", hits, err)
	}
}

// The public catalog is deliberately short: search finds the capability, exec
// runs the script that batches the calls, ro runs a bounded read-only script,
// and the task readers stay published. The direct execute facade is not
// public: a client that still calls it gets the explicit migration answer,
// while the code-only spelling keeps working as the old transport alias.
func TestPublicFacadePublishesShortSurface(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir()})
	t.Cleanup(func() { _ = s.Close() })
	listed := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	tools := outputRows(mapValue(listed["result"])["tools"])
	names := map[string]bool{}
	for _, item := range tools {
		names[firstString(mapValue(item), "name")] = true
	}
	for _, required := range []string{"search", "exec", "ro", "job", "expand"} {
		if !names[required] {
			t.Fatalf("public tool list misses %q: %v", required, names)
		}
	}
	for _, retired := range []string{"execute", "callMcpTool", "call_mcp_tool", "code_exec"} {
		if names[retired] {
			t.Fatalf("public tool list publishes %q: %v", retired, names)
		}
	}
	fullNames := map[string]bool{}
	for _, tool := range appsSDKTools() {
		fullNames[firstString(tool, "name")] = true
	}
	if fullNames["execute"] {
		t.Fatalf("canonical facade list publishes the direct execute facade: %v", fullNames)
	}
	called := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"execute","arguments":{"target":"hub","tool":"settings_schema","args":{}}}}`)
	structured := mapValue(mapValue(called["result"])["structuredContent"])
	if firstString(structured, "status") != "failed" || firstString(mapValue(structured["error"]), "code") != "execute_facade_retired" {
		t.Fatalf("the direct execute facade is not refused with the migration answer: %#v", called)
	}
	legacy := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute","arguments":{"code":"return {answer:42};"}}}`)
	legacyValue := mapValue(mapValue(legacy["result"])["structuredContent"])
	if legacy["error"] != nil || intFromAny(mapValue(legacyValue["result"])["answer"]) != 42 {
		t.Fatalf("the code-only alias of exec stopped working: %#v", legacy)
	}
}

// The direct execute facade is not public any more. The public catalog
// publishes the short surface instead, and a client that still calls execute
// gets the explicit migration answer. The code-only spelling of execute
// keeps working as the old transport alias of exec.
func TestPublicFacadeRefusesDirectExecute(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir()})
	t.Cleanup(func() { _ = s.Close() })
	listed := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	tools := outputRows(mapValue(listed["result"])["tools"])
	names := map[string]bool{}
	for _, item := range tools {
		names[firstString(mapValue(item), "name")] = true
	}
	if names["execute"] || names["callMcpTool"] || names["call_mcp_tool"] || names["code_exec"] {
		t.Fatalf("public tool list publishes a retired facade: %v", names)
	}
	for _, required := range []string{"search", "exec", "ro"} {
		if !names[required] {
			t.Fatalf("public tool list misses %q: %v", required, names)
		}
	}
	fullNames := map[string]bool{}
	for _, tool := range appsSDKTools() {
		fullNames[firstString(tool, "name")] = true
	}
	if fullNames["execute"] {
		t.Fatalf("canonical facade list publishes the direct execute facade: %v", fullNames)
	}
	called := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"execute","arguments":{"target":"hub","tool":"settings_schema","args":{}}}}`)
	structured := mapValue(mapValue(called["result"])["structuredContent"])
	if called["error"] != nil || firstString(structured, "status") != "failed" || firstString(mapValue(structured["error"]), "code") != "execute_facade_retired" {
		t.Fatalf("the direct execute facade is not refused with the migration answer: %#v", called)
	}
	legacy := postMCPRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute","arguments":{"code":"return {answer:42};"}}}`)
	legacyValue := mapValue(mapValue(legacy["result"])["structuredContent"])
	if legacy["error"] != nil || intFromAny(mapValue(legacyValue["result"])["answer"]) != 42 {
		t.Fatalf("the code-only alias of exec stopped working: %#v", legacy)
	}
	modernBody := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"execute","arguments":{"code":"return {answer:43};"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	modernReq := httptest.NewRequest("POST", "/mcp", strings.NewReader(modernBody))
	modernReq.Header.Set("Authorization", "Bearer ctl")
	modernReq.Header.Set("MCP-Protocol-Version", "2026-07-28")
	modernReq.Header.Set("Mcp-Method", "tools/call")
	modernReq.Header.Set("Mcp-Name", "execute")
	modernRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(modernRec, modernReq)
	var modern map[string]any
	_ = json.Unmarshal(modernRec.Body.Bytes(), &modern)
	modernValue := mapValue(mapValue(modern["result"])["structuredContent"])
	if modernRec.Code != 200 || modern["error"] != nil || intFromAny(mapValue(modernValue["result"])["answer"]) != 43 {
		t.Fatalf("cached execute header was not canonicalized before routing validation: %v", modern)
	}
	for _, args := range []string{`{"code":"return 1;","target":"hub","tool":"demo"}`, `{"code":"return 1;","args":{}}`, `{"code":"return 1;","background":true}`} {
		body := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"execute","arguments":` + args + `}}`
		bad := postMCPRPC(t, s, body)
		if bad["error"] == nil {
			t.Fatalf("unsafe legacy execute args accepted: %s => %v", args, bad)
		}
	}
	reader := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"sub": "reader", "client_id": "reader", "scope": "gptadmin.read", "access_mode": accessModeReadonly})
	if err := authorizeToolCall(reader, "hub", "exec"); err == nil {
		t.Fatal("legacy alias granted a read-only caller exec")
	}
	if err := authorizeFacadeCall(reader, "ro", nil); err != nil {
		t.Fatalf("readonly scope cannot use ro: %v", err)
	}
	if err := authorizeFacadeCall(reader, "exec", nil); err == nil {
		t.Fatal("readonly scope gained exec")
	}
}

func TestSearchCatalogConcurrentProfilesAreFilteredIndependently(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Name: "Fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "build_version": 194}}
	s.mu.Unlock()
	a := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "A", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite, AllowedTargets: []string{"hub"}, AllowedTools: []string{"search", "status"}, Version: 1})
	b := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "B", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite, AllowedTargets: []string{"shell:fixture"}, AllowedTools: []string{"search", "shell_exec"}, Version: 1})
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan string, 100)
	for n := 0; n < 50; n++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			got := s.searchToolsForRequest(a, map[string]any{"query": "shell_exec", "target": "shell:fixture"})
			if len(outputRows(got["results"])) != 0 {
				errs <- "profile A saw shell tool"
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			got := s.searchToolsForRequest(b, map[string]any{"query": "shell_exec", "target": "shell:fixture"})
			if len(outputRows(got["results"])) == 0 {
				errs <- fmt.Sprintf("profile B lost its allowed shell tool: %#v", got)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	coverage, _ := json.Marshal(s.searchToolsForRequest(a, map[string]any{"query": "shell_exec", "target": "shell:fixture"})["coverage"])
	if strings.Contains(string(coverage), "secret") {
		t.Fatal("coverage metadata unexpectedly reveals secrets")
	}
}

func TestRegisteredRootSearchCacheRechecksProfileAndDescriptor(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	root := Agent{AgentID: "mcp:root-fixture", Name: "Root Fixture", Kind: "remote_mcp", Transport: "streamable-http", Status: "online", Meta: map[string]any{"enabled": true}}
	s.mu.Lock()
	s.agents[root.AgentID] = &root
	s.mu.Unlock()
	owner := requestWithAuthClaims(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"sub": "root-owner", "client_id": "root-owner", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	cache := s.registeredRootSearchCache()
	key := rootSearchCacheKey(owner, root)
	fp := rootSearchFingerprint(s, root)
	cache.entries[key] = &childSearchSchemaEntry{childID: root.AgentID, fingerprint: fp, loadedAt: time.Now(), tools: []map[string]any{{"name": "rare_root_tool", "description": "zircon root-only schema phrase", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
	got := s.searchToolsForRequest(owner, map[string]any{"query": "zircon phrase", "target": root.AgentID})
	if rows := outputRows(got["results"]); len(rows) != 1 || firstString(mapValue(rows[0]), "target") != root.AgentID {
		t.Fatalf("warm registered root schema not searchable: %#v", got)
	}
	denied := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), AccessProfile{ID: "narrow", AccessMode: accessModeFull, AllowedTargets: []string{"hub"}, AllowedTools: []string{"search"}})
	got = s.searchToolsForRequest(denied, map[string]any{"query": "zircon phrase"})
	if len(outputRows(got["results"])) != 0 {
		t.Fatalf("root schema leaked across target profile: %#v", got)
	}
	s.mu.Lock()
	root.Meta["enabled"] = false
	s.mu.Unlock()
	got = s.searchToolsForRequest(owner, map[string]any{"query": "zircon phrase", "target": root.AgentID})
	if len(outputRows(got["results"])) != 0 {
		t.Fatalf("disabled registered root schema remained searchable: %#v", got)
	}
}
