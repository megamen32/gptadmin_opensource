package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func accessToolTest(t *testing.T, s *Server, token, name string, args map[string]any) (map[string]any, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"target": "hub", "tool": name, "args": args, "detail": "full"})
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if v, ok := result["response"].(map[string]any); ok {
		return v, w.Code
	}
	return result, w.Code
}

func TestAccessManagementProfileMCPRoundTrip(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "fixture-owner", PublicOrigin: "https://hub.example"})
	defer s.Close()
	args := map[string]any{"action": "create", "id": "ops", "profile": map[string]any{"name": "Operations", "access_mode": "full", "approval_mode": "ask_before_write", "allowed_targets": []string{"shell:host"}, "allowed_tools": []string{"shell_exec"}, "workspace_refs": []map[string]any{{"machine_id": "host", "workspace_path": "/work", "startup_document": "AGENTS.md", "shell_target": "shell:host"}}}}
	response, status := accessToolTest(t, s, "fixture-owner", "access_profiles", args)
	if status != 200 || response["id"] != "ops" {
		t.Fatalf("MCP profile creation failed: %d %v", status, response)
	}
	restored := New(s.cfg)
	defer restored.Close()
	response, status = accessToolTest(t, restored, "fixture-owner", "access_profiles", map[string]any{"action": "get", "id": "ops"})
	if status != 200 || response["approval_mode"] != "ask_before_write" || len(response["workspace_refs"].([]any)) != 1 {
		t.Fatalf("profile lost on restart: %d %v", status, response)
	}
}

func TestAccessClientIssueBindingAndHistorySurviveRestart(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "fixture-owner", PublicOrigin: "https://hub.example"})
	defer s.Close()
	profile, status := accessToolTest(t, s, "fixture-owner", "access_profiles", map[string]any{"action": "create", "id": "reader", "profile": map[string]any{"access_mode": "readonly", "allowed_targets": []string{"shell:host"}, "allowed_tools": []string{"system_inspect"}}})
	if status != 200 {
		t.Fatalf("create profile: %d %v", status, profile)
	}
	client, status := accessToolTest(t, s, "fixture-owner", "access_clients", map[string]any{"action": "issue", "client_id": "named-reader", "profile_id": "reader", "access_mode": "readonly", "ttl_days": 7})
	if status != 200 || client["token_id"] == nil {
		t.Fatalf("create client: %d", status)
	}
	value := firstString(client, "access_token")
	restored := New(s.cfg)
	defer restored.Close()
	if restored.managedMCP[firstString(client, "token_id")].ProfileID != "reader" {
		t.Fatal("issue and binding were not persisted together")
	}
	_, status = accessToolTest(t, restored, value, "access_profiles", map[string]any{"action": "create", "id": "escalate", "profile": map[string]any{"access_mode": "full"}})
	if status != 403 {
		t.Fatalf("ordinary reader may administer profiles: %d", status)
	}
	history, status := accessToolTest(t, restored, "fixture-owner", "operations", map[string]any{"limit": 50})
	if status != 200 {
		t.Fatalf("history status=%d", status)
	}
	rows := history["operations"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected two persisted operations, got %d", len(rows))
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["status"] != "completed" || row["actor"] != "legacy_ctl" {
			t.Fatalf("incomplete operation: %v", row)
		}
	}
	encoded, _ := json.Marshal(history)
	if bytes.Contains(encoded, []byte(value)) {
		t.Fatal("credential leaked into history")
	}
}

func TestAccessNativeMCPProfileTool(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir()})
	defer s.Close()
	_ = postMCPRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute","arguments":{"target":"hub","tool":"access_profiles","arguments":{"action":"create","id":"native","profile":{"access_mode":"full","approval_mode":"unrestricted","allowed_targets":["*"],"allowed_tools":["*"]}}}}}`)
	if s.accessProfiles["native"].ApprovalMode != "unrestricted" {
		t.Fatal("native MCP tool did not persist a profile")
	}
}

func TestAccessOperationIntentRemainsVisibleAfterRestart(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "fixture-owner"})
	defer s.Close()
	if err := s.appendAccessOperation(map[string]any{"operation_id": "interrupted", "action": "PUT", "target": "/admin/api/access-profiles/example", "actor": "fixture", "status": "started"}); err != nil {
		t.Fatal(err)
	}
	restored := New(s.cfg)
	defer restored.Close()
	history, status := accessToolTest(t, restored, "fixture-owner", "operations", map[string]any{})
	if status != 200 || history["operations"].([]any)[0].(map[string]any)["status"] != "started" {
		t.Fatal("unfinished operation disappeared")
	}
}

func TestAccessFullOperatorDoesNotGainOwnerAPI(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "fixture-owner", PublicOrigin: "https://hub.example"})
	defer s.Close()
	token, _, err := s.issueManagedMCPToken("ordinary-operator", 7, "https://hub.example", "https://hub.example")
	if err != nil {
		t.Fatal(err)
	}
	_, status := accessToolTest(t, s, token, "access_profiles", map[string]any{"action": "create", "id": "forbidden", "profile": map[string]any{"access_mode": "full"}})
	if status != 403 {
		t.Fatalf("tool execution permission promoted to owner: %d", status)
	}
}
