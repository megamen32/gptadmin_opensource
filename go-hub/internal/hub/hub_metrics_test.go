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
	for _, field := range []string{"build_version", "agents", "relay_jobs", "shell_jobs", "audit_events", "telemetry_enabled"} {
		if _, ok := metrics[field]; !ok {
			t.Fatalf("metrics missing %q: %v", field, metrics)
		}
	}
	if strings.Contains(w.Body.String(), "must-not-appear") || strings.Contains(w.Body.String(), "ctl") {
		t.Fatalf("Hub metrics leaked credential material: %s", w.Body.String())
	}
}
