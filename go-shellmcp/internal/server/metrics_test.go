package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsEndpointIsAuthenticatedBoundedAndSecretFree(t *testing.T) {
	s := New(Config{Token: "shell-secret", Name: "metrics-agent", QueueEnabled: true})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer shell-secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", w.Code, w.Body.String())
	}
	var metrics map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"build_version", "jobs", "queue_enabled", "mode", "audit_enabled"} {
		if _, ok := metrics[field]; !ok {
			t.Fatalf("metrics missing %q: %v", field, metrics)
		}
	}
	if strings.Contains(w.Body.String(), "shell-secret") || strings.Contains(w.Body.String(), "metrics-agent") {
		t.Fatalf("ShellMCP metrics leaked credential/identity material: %s", w.Body.String())
	}
}
