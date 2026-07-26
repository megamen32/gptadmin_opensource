package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActivationTelemetryIsOptInAggregatedAndPersistent(t *testing.T) {
	configDir := t.TempDir()
	s := New(Config{ConfigDir: configDir, CtlToken: "ctl"})
	request := func(server *Server, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer ctl")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}

	initial := request(s, http.MethodGet, "/admin/api/telemetry", "")
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"enabled":false`) {
		t.Fatalf("initial telemetry status=%d body=%s", initial.Code, initial.Body.String())
	}
	if event := request(s, http.MethodPost, "/admin/api/telemetry/event", `{"event":"client_connected","token":"must-not-persist"}`); event.Code != http.StatusForbidden {
		t.Fatalf("disabled telemetry accepted event: status=%d body=%s", event.Code, event.Body.String())
	}
	enabled := request(s, http.MethodPut, "/admin/api/telemetry", `{"enabled":true}`)
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"enabled":true`) {
		t.Fatalf("enable telemetry status=%d body=%s", enabled.Code, enabled.Body.String())
	}
	for _, eventName := range []string{"client_connected", "client_connected", "first_tool", "failure"} {
		event := request(s, http.MethodPost, "/admin/api/telemetry/event", `{"event":"`+eventName+`","args":"must-not-persist"}`)
		if event.Code != http.StatusAccepted {
			t.Fatalf("telemetry event %q status=%d body=%s", eventName, event.Code, event.Body.String())
		}
	}
	state := request(s, http.MethodGet, "/admin/api/telemetry", "")
	if !strings.Contains(state.Body.String(), `"client_connected":2`) || !strings.Contains(state.Body.String(), `"first_tool":1`) || !strings.Contains(state.Body.String(), `"failure":1`) {
		t.Fatalf("aggregated telemetry counters missing: %s", state.Body.String())
	}
	if strings.Contains(state.Body.String(), "must-not-persist") {
		t.Fatalf("telemetry response exposed event payload: %s", state.Body.String())
	}
	restarted := New(Config{ConfigDir: configDir, CtlToken: "ctl"})
	persisted := request(restarted, http.MethodGet, "/admin/api/telemetry", "")
	if persisted.Code != http.StatusOK || !strings.Contains(persisted.Body.String(), `"client_connected":2`) {
		t.Fatalf("telemetry did not survive restart: status=%d body=%s", persisted.Code, persisted.Body.String())
	}
}

func TestActivationTelemetryRecordsConnectionAndFirstToolWithoutPayload(t *testing.T) {
	s := New(Config{
		ConfigDir:         t.TempDir(),
		CtlToken:          "ctl",
		OAuthClientSecret: "oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
	})
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		return response
	}
	if enabled := request(http.MethodPut, "/admin/api/telemetry", "ctl", `{"enabled":true}`); enabled.Code != http.StatusOK {
		t.Fatalf("enable telemetry status=%d body=%s", enabled.Code, enabled.Body.String())
	}
	token, err := s.signJWT(map[string]any{
		"sub": "telemetry-client", "client_id": "telemetry-client", "jti": "telemetry-jti",
		"scope": "gptadmin.read", "access_mode": accessModeReadonly,
		"aud": "https://hub.example", "resource": "https://hub.example",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page := request(http.MethodGet, "/connect", "", ""); page.Code != http.StatusOK {
		t.Fatalf("connection page status=%d body=%s", page.Code, page.Body.String())
	}
	toolCall := request(http.MethodPost, "/mcp", token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo","arguments":{"secret":"must-not-persist"}}}`)
	if toolCall.Code != http.StatusOK {
		t.Fatalf("first tool status=%d body=%s", toolCall.Code, toolCall.Body.String())
	}
	state := request(http.MethodGet, "/admin/api/telemetry", "ctl", "")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"connection_page_viewed":1`) || !strings.Contains(state.Body.String(), `"first_tool":1`) {
		t.Fatalf("automatic activation counters missing: status=%d body=%s", state.Code, state.Body.String())
	}
	if strings.Contains(state.Body.String(), "must-not-persist") || strings.Contains(state.Body.String(), "telemetry-client") {
		t.Fatalf("automatic telemetry leaked request payload or identity: %s", state.Body.String())
	}
}
