package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestInstructionSetToolRoundTrip(t *testing.T) {
	s, cfg := instructionSetServer(t)
	defer s.Close()
	got, status := accessToolTest(t, s, "ctl", "instruction_sets", map[string]any{"action": "get"})
	if status != 200 || got["id"] != "default" || firstString(got, "etag") == "" {
		t.Fatalf("get default: %d %v", status, got)
	}
	old := firstString(got, "etag")
	for _, tag := range []string{"", "*", "stale"} {
		_, status = accessToolTest(t, s, "ctl", "instruction_sets", map[string]any{"action": "update", "id": "default", "etag": tag, "content": "must not save"})
		if status != 428 && status != 412 {
			t.Fatalf("bad etag %q: %d", tag, status)
		}
	}
	got, status = accessToolTest(t, s, "ctl", "instruction_sets", map[string]any{"action": "update", "id": "default", "etag": old, "content": "new shared instructions"})
	if status != 200 || got["content"] != "new shared instructions" || firstString(got, "etag") == old {
		t.Fatalf("update default: %d %v", status, got)
	}
	content, err := os.ReadFile(cfg.StartupInstructionsFile)
	if err != nil || string(bytes.TrimSpace(content)) != "new shared instructions" {
		t.Fatalf("persisted: %q %v", content, err)
	}
	listed, status := accessToolTest(t, s, "ctl", "instruction_sets", map[string]any{"action": "list"})
	if status != 200 || len(listed["instruction_sets"].([]any)) != 1 {
		t.Fatalf("list: %d %v", status, listed)
	}
	named := instructionSetRequest(t, s, http.MethodPut, namedInstructionSetPath, []byte(`{"content":"ops instructions"}`), "*")
	if named.Code != 200 {
		t.Fatalf("named fixture: %d %s", named.Code, named.Body.String())
	}
	got, status = accessToolTest(t, s, "ctl", "instruction_sets", map[string]any{"action": "get", "id": "ops"})
	if status != 200 || got["content"] != "ops instructions" {
		t.Fatalf("get named: %d %v", status, got)
	}
	got, status = accessToolTest(t, s, "ctl", "instruction_sets", map[string]any{"action": "update", "id": "ops", "etag": got["etag"], "content": "updated ops"})
	if status != 200 || got["content"] != "updated ops" {
		t.Fatalf("update named: %d %v", status, got)
	}
	restored := New(cfg)
	defer restored.Close()
	got, status = accessToolTest(t, restored, "ctl", "instruction_sets", map[string]any{"action": "get", "id": "default"})
	if status != 200 || got["content"] != "new shared instructions" {
		t.Fatalf("restore: %d %v", status, got)
	}
}

func TestInstructionSetToolAuthorization(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir(), PublicOrigin: "https://hub.example"})
	defer s.Close()
	token, _, err := s.issueManagedMCPToken("ordinary", 7, "https://hub.example", "https://hub.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"list", "get", "update"} {
		_, status := accessToolTest(t, s, token, "instruction_sets", map[string]any{"action": action, "etag": "x", "content": "not authorized"})
		if status != 403 {
			t.Fatalf("ordinary %s: %d", action, status)
		}
	}
	_, status := accessToolTest(t, s, "ctl", "access_profiles", map[string]any{"action": "create", "id": "instruction-reader", "profile": map[string]any{"access_mode": "full", "allowed_targets": []string{"hub"}, "allowed_tools": []string{"instruction_sets"}, "tool_rules": []map[string]any{{"target": "hub", "tool": "instruction_sets", "actions": []string{"get", "list"}}}}})
	if status != 200 {
		t.Fatalf("profile: %d", status)
	}
	issued, status := accessToolTest(t, s, "ctl", "access_clients", map[string]any{"action": "issue", "client_id": "delegated", "role": "admin", "profile_id": "instruction-reader", "access_mode": "full", "ttl_days": 7})
	if status != 200 {
		t.Fatalf("issue: %d %v", status, issued)
	}
	delegated := firstString(issued, "access_token")
	got, status := accessToolTest(t, s, delegated, "instruction_sets", map[string]any{"action": "get"})
	if status != 200 {
		t.Fatalf("delegated get: %d %v", status, got)
	}
	_, status = accessToolTest(t, s, delegated, "instruction_sets", map[string]any{"action": "update", "etag": got["etag"], "content": "not allowed"})
	if status != 403 {
		t.Fatalf("delegated update: %d", status)
	}
	req := httptest.NewRequest(http.MethodPut, instructionSetPath, bytes.NewBufferString(`{"content":"not allowed"}`))
	req.Header.Set("Authorization", "Bearer "+delegated)
	req.Header.Set("If-Match", firstString(got, "etag"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("direct HTTP update bypass: %d", w.Code)
	}
}

func TestInstructionSetToolNativeMCP(t *testing.T) {
	s, _ := instructionSetServer(t)
	defer s.Close()
	code := `const set = await mcp.call("hub","instruction_sets",{action:"get",id:"default"}); return await mcp.call("hub","instruction_sets",{action:"update",id:"default",etag:set.etag,content:"native MCP instructions"});`
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "exec", "arguments": map[string]any{"code": code}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := postMCPRPC(t, s, string(payload))
	structured := mapValue(mapValue(body["result"])["structuredContent"])
	if firstString(structured, "status") != "completed" || firstString(mapValue(structured["result"]), "content") != "native MCP instructions" {
		t.Fatalf("native MCP exec: %#v", body)
	}
}
