package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectionDebugIncludesSafeFleetSnapshot(t *testing.T) {
	s := New(Config{CtlToken: "ctl", PublicOrigin: "https://hub.example"})
	s.mu.Lock()
	s.agents["shell:mac-mini-2012.lan"] = &Agent{
		AgentID:      "shell:mac-mini-2012.lan",
		Name:         "Shell: mac-mini-2012.lan",
		Kind:         "virtual_shell",
		Transport:    "heartbeat",
		Status:       "online",
		LastSeen:     1234,
		Capabilities: []string{"shell", "mcp"},
		Meta: map[string]any{
			"hostname": "mac-mini-2012.lan",
			"mcp_agents": []any{map[string]any{
				"ref":      "BrowserClaw",
				"endpoint": "http://127.0.0.1:9010/mcp",
				"bearer":   "must-not-appear",
			}},
		},
	}
	s.relayJobs["job-running"] = &relayJob{ID: "job-running", AgentID: "shell:mac-mini-2012.lan", Method: "tools/call", Status: "running", CreatedAt: 1200}
	s.shellJobs["job-shell"] = &shellJob{ID: "job-shell", Server: "mac-mini-2012.lan", ToolName: "mcp_tools", Cmd: "secret command", Status: "queued", CreatedAt: 1201}
	s.audit = append(s.audit, auditEvent{Time: "2026-08-08T12:00:00Z", Name: "mcp_auth_denied", Fields: map[string]any{"trace_id": "trace-1", "reason": "connection failed", "authorization": "must-not-appear"}})
	s.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/admin/api/connection-debug?limit=20", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generated_at", "hub", "summary", "connections", "jobs", "recent_audit"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("debug snapshot missing %q: %v", field, body)
		}
	}
	if !strings.Contains(w.Body.String(), "BrowserClaw") || !strings.Contains(w.Body.String(), "trace-1") {
		t.Fatalf("debug snapshot lost useful connection evidence: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "must-not-appear") || strings.Contains(w.Body.String(), "secret command") {
		t.Fatalf("debug snapshot leaked secret material: %s", w.Body.String())
	}
}

func TestConnectionDebugRequiresCtlAndValidLimit(t *testing.T) {
	s := New(Config{CtlToken: "ctl"})
	unauthorized := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin/api/connection-debug", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/connection-debug?limit=501", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "limit must be between") {
		t.Fatalf("invalid limit status=%d body=%s", w.Code, w.Body.String())
	}
}
