package hub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalQuorumExecutorForwardRequiresItsOwnCredential(t *testing.T) {
	s := New(Config{})
	defer s.Close()
	s.quorumPrivateTLS = &quorumPrivateTLS{}
	identity := map[string]string{"server_id": "id", "public_key": "key", "fingerprint": "fp"}
	token := strings.Repeat("a", 32)
	if err := s.RegisterLocalExecutor("haos", identity, token); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, method, body, auth, want string }{
		{"/queue/haos?timeout=0", http.MethodGet, "", token, "shell:haos"},
		{"/queue/haos/result", http.MethodPost, "{}", token, "shell:haos"},
		{"/queue/haos/acl-check", http.MethodPost, "{}", token, "shell:haos"},
		{"/heartbeat", http.MethodPost, `{"name":"haos"}`, token, "shell:haos"},
		{"/queue/other", http.MethodGet, "", token, ""},
		{"/queue/haos", http.MethodGet, "", strings.Repeat("b", 32), ""},
		{"/queue/haos/result", http.MethodGet, "", token, ""},
		{"/mcp-relay/call", http.MethodPost, "{}", token, ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		got, err := s.localQuorumExecutorForForward(r)
		if err != nil || got != tc.want {
			t.Fatalf("%s %s: got %q, err %v, want %q", tc.method, tc.path, got, err, tc.want)
		}
		if tc.body != "" {
			data, err := io.ReadAll(r.Body)
			if err != nil || string(data) != tc.body {
				t.Fatalf("request body changed for %s", tc.path)
			}
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/heartbeat", strings.NewReader(strings.Repeat("x", quorumHeartbeatMaxBytes+1)))
	if _, err := s.localQuorumExecutorForForward(r); err == nil {
		t.Fatal("oversized heartbeat accepted")
	}
}
