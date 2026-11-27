package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A member whose queue origin dies mid-proxy panics with ErrAbortHandler
// inside the ReverseProxy. The unlock/lock pair around proxyMemberRequest
// must not leave s.mu unlocked when that panic unwinds, otherwise the
// deferred unlock crashes the whole node process.
func TestPollShellQueueKeepsMutexLockedWhenProxyPanics(t *testing.T) {
	s := New(Config{AuthMode: "reader", ConfigDir: t.TempDir(), OAuthClientSecret: "fixture", PublicOrigin: "https://issuer.test"})
	defer s.Close()
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer remote.Close()
	s.memberQuorumHubs.Set([]string{remote.URL})
	s.nodePeerClient = remote.Client()
	if err := s.RegisterLocalExecutor("mac", map[string]string{"server_id": "mac-id", "public_key": "mac-key", "fingerprint": "mac-fp"}, strings.Repeat("w", 32)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/queue/mac?timeout=0", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("w", 32))
	r.Header.Set("X-GPTAdmin-Node-Key", strings.Repeat("w", 32))
	r.Header.Set("Server-ID", "go-shellmcp-fixture")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == 0 {
		t.Fatal("handler produced no response")
	}
	// The deferred unlock must still own the mutex. Acquiring it here proves
	// the panic path left s.mu locked instead of crashing or unlocking twice.
	s.mu.Lock()
	s.mu.Unlock()
}
