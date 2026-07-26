package hub

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newPolicyBoundaryServer(t *testing.T) *Server {
	t.Helper()
	s := New(Config{
		ConfigDir:      t.TempDir(),
		CtlToken:       "ctl",
		BridgeKey:      "bridge",
		AdminPassword:  "pw",
		PublicOrigin:   "https://hub.example",
		DefaultTimeout: time.Second,
		PollMaxTimeout: time.Second,
	})
	s.mu.Lock()
	s.agents["shell:target"] = &Agent{AgentID: "shell:target", Status: "online"}
	s.agents["mcp:target"] = &Agent{AgentID: "mcp:target", Status: "online"}
	s.mu.Unlock()
	return s
}

func policyJobCounts(s *Server) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shellJobs), len(s.relayJobs)
}

func TestMCPPromptCallCannotBypassProfilePolicy(t *testing.T) {
	s := newPolicyBoundaryServer(t)
	body := `{"tool":"execute","args":{"target":"shell:target","tool":"shell_exec","arguments":{"cmd":"id"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp-prompt/call?key=bridge", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && !strings.Contains(rec.Body.String(), "policy") && !strings.Contains(rec.Body.String(), "read-only") {
		t.Fatalf("legacy bridge write was not denied: status=%d body=%s", rec.Code, rec.Body.String())
	}
	shellJobs, relayJobs := policyJobCounts(s)
	if shellJobs != 0 || relayJobs != 0 {
		t.Fatalf("legacy bridge bypass queued work: shell=%d relay=%d body=%s", shellJobs, relayJobs, rec.Body.String())
	}
}

func TestWebhookActionCannotBypassApprovalPolicy(t *testing.T) {
	s := newPolicyBoundaryServer(t)
	result, err := s.dispatchWebhookAction(WebhookAction{Kind: "shell", Target: "shell:target", Command: "id"}, map[string]any{})
	if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "approval") && !strings.Contains(strings.ToLower(err.Error()), "policy")) {
		t.Fatalf("webhook write bypassed policy: result=%v err=%v", result, err)
	}
	shellJobs, relayJobs := policyJobCounts(s)
	if shellJobs != 0 || relayJobs != 0 {
		t.Fatalf("webhook bypass queued work: shell=%d relay=%d", shellJobs, relayJobs)
	}
}

func TestBulkExecUsesPolicyGate(t *testing.T) {
	s := newPolicyBoundaryServer(t)
	req := httptest.NewRequest(http.MethodPost, "/bulk/exec", bytes.NewBufferString(`{"servers":["target"],"cmd":"id"}`))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && !strings.Contains(strings.ToLower(rec.Body.String()), "approval") && !strings.Contains(strings.ToLower(rec.Body.String()), "policy") {
		t.Fatalf("bulk write was not denied or gated: status=%d body=%s", rec.Code, rec.Body.String())
	}
	shellJobs, relayJobs := policyJobCounts(s)
	if shellJobs != 0 || relayJobs != 0 {
		t.Fatalf("bulk endpoint bypass queued work: shell=%d relay=%d", shellJobs, relayJobs)
	}
}

func TestAdminMCPResourceReadHonorsTargetPolicy(t *testing.T) {
	s := newPolicyBoundaryServer(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/resources/read", bytes.NewBufferString(`{"target":"mcp:target","uri":"resource://secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req = requestWithAccessProfile(req, AccessProfile{ID: "restricted", AccessMode: accessModeFull, ApprovalMode: approvalModeReadOnly, AllowedTargets: []string{"mcp:allowed"}, AllowedTools: []string{"resources/read"}, Version: 1})
	rec := httptest.NewRecorder()
	s.adminMCPResourceRead(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("resource read ignored target policy: status=%d body=%s", rec.Code, rec.Body.String())
	}
	shellJobs, relayJobs := policyJobCounts(s)
	if shellJobs != 0 || relayJobs != 0 {
		t.Fatalf("resource read policy bypass queued work: shell=%d relay=%d", shellJobs, relayJobs)
	}
}

func TestActionsOpenAPIIncludesNetworkProxyContract(t *testing.T) {
	s := New(Config{PublicOrigin: "https://hub.example"})
	req := httptest.NewRequest(http.MethodGet, "/actions/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("actions OpenAPI status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/proxy-control/v1/request", "/proxy-control/v1/approve", "/proxy-control/v1/issue", "/proxy-control/v1/open", "/proxy-control/v1/status", "/proxy-control/v1/revoke"} {
		if !strings.Contains(rec.Body.String(), path) {
			t.Fatalf("actions OpenAPI missing %s", path)
		}
	}
}
