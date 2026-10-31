package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActivityAuditCanHideNoisyClientBeforePagination(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir()})
	defer s.Close()
	s.mu.Lock()
	s.addAuditLocked("tool_policy_decision", map[string]any{
		"client_id": "opencode", "actor": "opencode",
		"tool": "shell_exec", "target": "shell:node-100", "result_reference": "job-1",
	})
	for i := 0; i < 550; i++ {
		s.addAuditLocked("tool_policy_decision", map[string]any{
			"client_id": "grepmesh", "actor": "grepmesh", "tool": "find_paths",
			"target": "shell:node-44", "sequence": i,
		})
	}
	s.mu.Unlock()

	// Ordinary audit memory retention is 500. The activity timeline reads a
	// broader bounded durable tail so 550 noisy events cannot hide opencode.
	url := fmt.Sprintf("/admin/api/audit?recent=1&name=tool_policy_decision&exclude_client=grepmesh&since=%d&limit=2", time.Now().Add(-time.Minute).Unix())
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Total   int            `json:"total"`
		Events  []auditEvent   `json:"events"`
		Clients map[string]int `json:"clients"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Events) != 1 || body.Events[0].Fields["client_id"] != "opencode" {
		t.Fatalf("noisy client filtering dropped the real caller: total=%d events=%+v", body.Total, body.Events)
	}
	if body.Clients["grepmesh"] != 550 || body.Clients["opencode"] != 1 {
		t.Fatalf("facet counts must include hidden clients: %+v", body.Clients)
	}
	if strings.Contains(w.Body.String(), "find_paths") {
		t.Fatal("filtered payload contained excluded client's operations")
	}
}

func TestActivityAuditRejectsInvalidSince(t *testing.T) {
	s := New(Config{CtlToken: "ctl", ConfigDir: t.TempDir()})
	defer s.Close()
	w := httptest.NewRecorder()
	s.adminAudit(w, httptest.NewRequest("GET", "/admin/api/audit?since=invalid", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", w.Code)
	}
}
