package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeModeRoutesNodeBeforeLegacyRegistryAndChecksAccess(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/mcp-relay/call" || r.Header.Get("Authorization") != "Bearer token" || firstString(body, "target") != "shell:nodeB" || firstString(body, "idempotency_key") != "same-key" {
			t.Errorf("invalid forwarded request: %v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"stdout": "ok"}})
	}))
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:nodeB": peer.URL})
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer token")
	b := &codeModeBridge{server: s}
	got, status := b.dispatch(r, "shell:nodeB", "shell_exec", map[string]any{"cmd": "hostname"}, nil, time.Second, "same-key")
	if status != http.StatusOK || firstString(got, "status") != "completed" || calls.Load() != 1 {
		t.Fatalf("route failed: %d %v calls=%d", status, got, calls.Load())
	}
	denied := requestWithAccessProfile(r, AccessProfile{ID: "deny", AccessMode: accessModeFull, AllowedTargets: []string{"shell:other"}, AllowedTools: []string{"shell_exec"}})
	_, status = b.dispatch(denied, "shell:nodeB", "shell_exec", map[string]any{"cmd": "hostname"}, nil, time.Second, "")
	if status != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("denied call reached peer: %d calls=%d", status, calls.Load())
	}
}
