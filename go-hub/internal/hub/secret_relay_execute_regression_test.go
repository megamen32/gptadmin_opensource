package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The hub schema exposes secret_request and secret_status to the generic
// target=hub execution facade. Both must dispatch with the *caller's* request:
// without that identity a created secret reference cannot be polled safely.
func TestSecretRelayExecuteCanRequestAndPollWithoutExposingValue(t *testing.T) {
	s := newSecretTestServer(t)
	call := func(body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer ctl")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var decoded map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("invalid response %d: %v", w.Code, err)
		}
		return w.Code, decoded
	}

	status, issued := call(`{"target":"hub","tool":"secret_request","args":{"label":"Diagnostic","env_name":"DIAGNOSTIC_SECRET","ttl_seconds":60}}`)
	if status != http.StatusOK || issued["status"] != "completed" {
		t.Fatalf("issue status=%d response=%v", status, issued)
	}
	result := mapValue(issued["response"])
	ref, url := firstString(result, "secret_ref"), firstString(result, "input_url")
	if ref == "" || !strings.HasPrefix(url, "https://hub.example/secret-input/") || result["status"] != "pending" {
		t.Fatalf("unexpected secret request receipt: %v", result)
	}
	if _, secretErr := s.secretStore.Status(ref, secretOwnerFingerprintValue("bearer:ctl")); secretErr != nil {
		t.Fatalf("caller cannot see its own secret receipt: %v", secretErr)
	}

	status, polled := call(`{"target":"hub","tool":"secret_status","args":{"secret_ref":"` + ref + `"}}`)
	meta := mapValue(polled["response"])
	if status != http.StatusOK || polled["status"] != "completed" || meta["status"] != "pending" || meta["secret_ref"] != ref {
		t.Fatalf("poll status=%d response=%v", status, polled)
	}
	// The one-time browser page can consume the token, but no plaintext ever
	// comes back through secret_status metadata.
	token := strings.TrimPrefix(url, "https://hub.example/secret-input/")
	if token == url || token == "" {
		t.Fatal("missing one-time browser token")
	}
	input := httptest.NewRequest(http.MethodPost, "/secret-input/"+token, strings.NewReader("value=synthetic-unit-test-value"))
	input.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	inputRecorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(inputRecorder, input)
	if inputRecorder.Code != http.StatusOK {
		t.Fatalf("one-time browser input status=%d", inputRecorder.Code)
	}
	status, ready := call(`{"target":"hub","tool":"secret_status","args":{"secret_ref":"` + ref + `"}}`)
	if status != http.StatusOK || mapValue(ready["response"])["status"] != "ready" {
		t.Fatalf("ready metadata status=%d: %v", status, ready)
	}
	if strings.Contains(string(mustMarshalForSecretTest(t, ready)), "synthetic-unit-test-value") {
		t.Fatal("secret plaintext leaked in status metadata")
	}

	other := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	other.Header.Set("Authorization", "Bearer another-caller")
	denied, code := s.callHubToolForRequest(other, "secret_status", map[string]any{"secret_ref": ref})
	if code != http.StatusBadRequest || firstString(denied, "status") != "failed" {
		t.Fatalf("another identity polled caller's secret: status=%d", code)
	}
}

func mustMarshalForSecretTest(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
