package hub

import (
	"net"
	"net/http"
	"strings"
	"time"
)

const authRateWindowSize = time.Minute

// allowAuthFailure applies one shared per-client window to failed auth
// attempts. Successful authenticated requests never consume this budget.
func (s *Server) allowAuthFailure(r *http.Request) bool {
	limit := s.cfg.AuthRateLimit
	if limit <= 0 {
		return true
	}
	key := authRateClientKey(r)
	now := s.now()
	s.authRateMu.Lock()
	defer s.authRateMu.Unlock()
	for candidate, window := range s.authRate {
		if now.Sub(window.Started) >= authRateWindowSize {
			delete(s.authRate, candidate)
		}
	}
	window := s.authRate[key]
	if window.Started.IsZero() || now.Sub(window.Started) >= authRateWindowSize {
		window = authRateWindow{Started: now}
	}
	if window.Count >= limit {
		s.authRate[key] = window
		return false
	}
	window.Count++
	s.authRate[key] = window
	return true
}

func authRateClientKey(r *http.Request) string {
	if r == nil || strings.TrimSpace(r.RemoteAddr) == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (s *Server) writeAuthRateLimited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	s.authAudit("auth_rate_limited", r, map[string]any{"reason": "authentication failure rate exceeded"})
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"detail": "too many authentication failures; retry later"})
}

func (s *Server) authFailureRateLimited(w http.ResponseWriter, r *http.Request) bool {
	if s.allowAuthFailure(r) {
		return false
	}
	s.writeAuthRateLimited(w, r)
	return true
}
