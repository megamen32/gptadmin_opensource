package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthFailuresAreRateLimitedPerClient(t *testing.T) {
	s := New(Config{CtlToken: "ctl", AdminPassword: "admin", AuthRateLimit: 2})
	for attempt := 1; attempt <= 3; attempt++ {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
		req.RemoteAddr = "198.51.100.20:1234"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		want := http.StatusUnauthorized
		if attempt == 3 {
			want = http.StatusTooManyRequests
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("rate-limited response has no Retry-After header")
			}
		}
		if w.Code != want {
			t.Fatalf("attempt %d status=%d body=%s, want %d", attempt, w.Code, w.Body.String(), want)
		}
	}
}
