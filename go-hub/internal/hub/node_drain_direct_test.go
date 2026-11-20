package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These requests use the real public HTTP handlers, authorization, approval
// path and SQLite task store. There is deliberately no executor for the
// synthetic parent, so a rejected request cannot hide behind fast completion.
func directDrainFixture(t *testing.T, approval bool) (*Server, Agent, Agent, string) {
	t.Helper()
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "direct-drain-owner",
		OAuthClientSecret: "direct-drain-signing", PublicOrigin: "https://direct-drain.example",
		MCPResource: "https://direct-drain.example", DefaultTimeout: time.Millisecond})
	t.Cleanup(func() { _ = s.Close() })
	parent := Agent{AgentID: "shell:direct-drain", Name: "direct-drain", Kind: "virtual_shell",
		Status: "online", Meta: map[string]any{"approved": true,
			"mcp_agents": []any{map[string]any{"ref": "fixture-child", "enabled": true, "transport": "streamable-http"}}}}
	s.mu.Lock()
	s.agents[parent.AgentID] = &parent
	if err := s.saveRegistryStateLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	child := childMCPAgents(parent)[0]
	if !approval {
		return s, parent, child, "direct-drain-owner"
	}
	if _, err := s.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
		profiles["direct-drain-ask"] = AccessProfile{ID: "direct-drain-ask", Version: 1,
			AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite,
			AllowedTargets: []string{parent.AgentID}, AllowedTools: []string{"shell_exec"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	token, err := s.signJWT(map[string]any{"sub": "direct-drain-client", "client_id": "direct-drain-client",
		"jti": "direct-drain-token", "profile_id": "direct-drain-ask", "scope": "gptadmin.read gptadmin.exec",
		"aud": s.cfg.MCPResource, "resource": s.cfg.MCPResource, "kid": defaultJWTKeyID,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return s, parent, child, token
}

func directDrainHTTP(t *testing.T, s *Server, agent Agent, token, name string, arguments map[string]any, tasks, action bool) *httptest.ResponseRecorder {
	t.Helper()
	path := "/server/" + exposedAgentSlug(agent) + "/mcp"
	var body any
	if action {
		path = "/server/" + exposedAgentSlug(agent) + "/actions/tools/" + name
		body = arguments
	} else {
		params := map[string]any{"name": name, "arguments": arguments}
		if tasks {
			params["_meta"] = map[string]any{
				"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				"io.modelcontextprotocol/clientInfo":      map[string]any{"name": "direct-drain-test", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{
					"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}
		}
		body = map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if !action {
		r.Header.Set("MCP-Protocol-Version", "2026-07-28")
		r.Header.Set("Mcp-Method", "tools/call")
		r.Header.Set("Mcp-Name", name)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func directDrainLedgerCount(t *testing.T, s *Server) int {
	t.Helper()
	path := filepath.Join(s.cfg.ConfigDir, "tasks_state.sqlite")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0
	}
	db, err := openTaskReadDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM task_records WHERE kind IN ('shell','relay')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNodeDrainDirectHTTPRejectsBeforeTaskPersistence(t *testing.T) {
	for _, tc := range []struct {
		name, tool                     string
		child, tasks, action, approval bool
	}{
		{name: "legacy-child-mcp", tool: "hello", child: true},
		{name: "legacy-child-actions", tool: "hello", child: true, action: true},
		{name: "native-child-task", tool: "hello", child: true, tasks: true},
		{name: "native-shell-task", tool: "shell_exec", tasks: true},
		{name: "native-shell-input-required", tool: "shell_exec", tasks: true, approval: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, parent, child, token := directDrainFixture(t, tc.approval)
			agent := parent
			arguments := map[string]any{"cmd": "printf synthetic"}
			if tc.child {
				agent, arguments = child, map[string]any{}
			}
			before := directDrainHTTP(t, s, agent, token, tc.tool, arguments, tc.tasks, tc.action)
			if count := directDrainLedgerCount(t, s); count != 1 {
				t.Fatalf("control request did not create its one real task: count=%d status=%d body=%s", count, before.Code, before.Body.String())
			}
			if tc.approval && !strings.Contains(before.Body.String(), `"status":"input_required"`) {
				t.Fatalf("control request did not enter actual approval flow: %s", before.Body.String())
			}
			s.mu.Lock()
			approvalsBefore := len(s.approvals)
			s.mu.Unlock()
			if w := drainAdminPost(s, `{"action":"begin","reason":"direct-http-regression"}`, "direct-drain-owner"); w.Code != http.StatusOK {
				t.Fatalf("drain begin: %d %s", w.Code, w.Body.String())
			}
			during := directDrainHTTP(t, s, agent, token, tc.tool, arguments, tc.tasks, tc.action)
			if count := directDrainLedgerCount(t, s); count != 1 {
				t.Fatalf("drained direct HTTP request persisted a new task: count=%d status=%d body=%s", count, during.Code, during.Body.String())
			}
			if !strings.Contains(during.Body.String(), nodeDrainRetryDetail) || !strings.Contains(during.Body.String(), `"retryable":true`) {
				t.Fatalf("missing explicit retryable drain refusal: %d %s", during.Code, during.Body.String())
			}
			s.mu.Lock()
			approvalsAfter := len(s.approvals)
			s.mu.Unlock()
			if approvalsAfter != approvalsBefore {
				t.Fatal("drained request created an approval record")
			}
			if st := s.NodeDrainStatus(); st.Counters.ActiveSubmissions != 0 {
				t.Fatalf("submission permit leaked: %+v", st)
			}
			if w := drainAdminPost(s, `{"action":"cancel"}`, "direct-drain-owner"); w.Code != http.StatusOK {
				t.Fatalf("drain cancel: %d", w.Code)
			}
			if !tc.approval {
				directDrainHTTP(t, s, agent, token, tc.tool, arguments, tc.tasks, tc.action)
				if directDrainLedgerCount(t, s) != 2 {
					t.Fatal("cancel did not restore normal submission")
				}
			}
		})
	}
}
