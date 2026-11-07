package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookDrainRejectsBeforeAcceptance(t *testing.T) {
	s := New(Config{WebhookRoutes: []WebhookRoute{{ID: "drain-test", Token: "test-token", Action: WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"}}}})
	defer s.Close()
	s.EnableNodeDrain("test update")
	r := webhookRequest(http.MethodPost, "/webhooks/v1/drain-test", []byte(`{}`))
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("accepted during drain: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.webhookJobs) != 0 || len(s.webhookDeliveries) != 0 {
		t.Fatal("rejected delivery was persisted as accepted")
	}
}
func TestWebhookDrainWaitsForAcceptedWorkflow(t *testing.T) {
	s := New(Config{WebhookRoutes: []WebhookRoute{{ID: "drain-test", Token: "test-token", Actions: []WebhookAction{{Kind: "mcp", Target: "hub", Tool: "status"}, {Kind: "mcp", Target: "hub", Tool: "status", DelaySeconds: 0.2}}}}})
	defer s.Close()
	r := webhookRequest(http.MethodPost, "/webhooks/v1/drain-test", []byte(`{}`))
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Idempotency-Key", "original")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("not accepted: %d %s", w.Code, w.Body.String())
	}
	s.EnableNodeDrain("test update")
	var receipt map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{{`{}`, 202}, {`{"different":true}`, 409}} {
		dup := webhookRequest(http.MethodPost, "/webhooks/v1/drain-test", []byte(tc.body))
		dup.Header.Set("Authorization", "Bearer test-token")
		dup.Header.Set("Idempotency-Key", "original")
		dw := httptest.NewRecorder()
		s.Handler().ServeHTTP(dw, dup)
		if dw.Code != tc.status {
			t.Fatalf("duplicate receipt during drain: %d %s", dw.Code, dw.Body.String())
		}
		if tc.status == 202 && !strings.Contains(dw.Body.String(), receipt["job_id"].(string)) {
			t.Fatal("duplicate changed job ID")
		}
	}
	if s.NodeDrainStatus().ReadyToStop {
		t.Fatal("drain ignores accepted webhook workflow")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.NodeDrainStatus().ReadyToStop {
			s.mu.Lock()
			job := s.webhookJobs[receipt["job_id"].(string)]
			status := job.Status
			count := intFromAny(job.Result["action_count"])
			s.mu.Unlock()
			if status != "completed" || count != 2 {
				t.Fatalf("accepted workflow failed: %s actions=%d", status, count)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow did not release drain admission")
}

func TestWebhookAdmittedShellSurvivesDrain(t *testing.T) {
	s := drainTestHub(t, nil)
	drainLocalExecutor(t, s)
	permit, ok := s.beginWorkflowSubmission()
	if !ok {
		t.Fatal("initial admission failed")
	}
	defer permit.close()
	s.EnableNodeDrain("update")
	result := make(chan error, 1)
	go func() {
		_, err := s.dispatchWebhookAction(WebhookAction{Kind: "shell", Target: "shell:local", ApprovalMode: approvalModeBoundedAutonomous, Command: "printf admitted", TimeoutSeconds: 3}, map[string]any{}, permit)
		result <- err
	}()
	var id string
	waitForDrainCondition(t, s, "admitted shell queued", func(st NodeDrainStatus) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for key := range s.shellJobs {
			id = key
		}
		return id != ""
	})
	r := httptest.NewRequest(http.MethodPost, "/queue/local/result", strings.NewReader(`{"id":"`+id+`","result":{"returncode":0,"stdout":"done"}}`))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("l", 32))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("result rejected: %d %s", w.Code, w.Body.String())
	}
	if err := <-result; err != nil {
		t.Fatalf("admitted shell rejected: %v", err)
	}
	permit.close()
	if !s.NodeDrainStatus().ReadyToStop {
		t.Fatal("completed shell still blocks drain")
	}
	release, ok := s.beginPermittedNodeSubmission("shell:local", "shell_exec", permit)
	if ok {
		release()
		t.Fatal("expired workflow permit bypassed drain")
	}
}
