package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubMetricsEndpointIsBoundedAndSecretFree(t *testing.T) {
	s := New(Config{CtlToken: "ctl", AdminPassword: "must-not-appear"})
	s.mu.Lock()
	s.shellJobs["working-task"] = &shellJob{ID: "working-task", Status: "running"}
	s.shellJobs["approval-task"] = &shellJob{ID: "approval-task", Status: "input_required"}
	s.relayJobs["done-task"] = &relayJob{ID: "done-task", Status: "completed"}
	s.agents["shell:online"] = &Agent{AgentID: "shell:online", Status: "online"}
	s.agents["shell:stale"] = &Agent{AgentID: "shell:stale", Status: "stale"}
	s.mu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", w.Code, w.Body.String())
	}
	var metrics map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"build_version", "agents", "relay_jobs", "shell_jobs", "task_statuses", "agent_statuses", "audit_events", "telemetry_enabled"} {
		if _, ok := metrics[field]; !ok {
			t.Fatalf("metrics missing %q: %v", field, metrics)
		}
	}
	taskStatuses := mapValue(metrics["task_statuses"])
	if intFromAny(taskStatuses["working"]) != 1 || intFromAny(taskStatuses["input_required"]) != 1 || intFromAny(taskStatuses["completed"]) != 1 {
		t.Fatalf("unexpected task statuses: %v", taskStatuses)
	}
	agentStatuses := mapValue(metrics["agent_statuses"])
	if intFromAny(agentStatuses["online"]) != 1 || intFromAny(agentStatuses["stale"]) != 1 {
		t.Fatalf("unexpected agent statuses: %v", agentStatuses)
	}
	if strings.Contains(w.Body.String(), "must-not-appear") || strings.Contains(w.Body.String(), "ctl") {
		t.Fatalf("Hub metrics leaked credential material: %s", w.Body.String())
	}
}
