package hub

import (
	"net"
	"net/http"
)

const localIngressReadinessPath = "/_gptadmin/ingress-readyz"

// A connector checks its own origin. Command and control-plane admission
// remain the responsibility of their respective handlers.
func (s *Server) withLocalIngressReadiness(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != localIngressReadinessPath {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() ||
			r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" ||
			r.Header.Get("Cf-Connecting-Ip") != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		s.mu.Lock()
		ready := !s.taskRuntimeClosed && (!s.authContinuityEnabled() ||
			s.authInitialized && !s.authState.Pending && s.authState.Generation != 0)
		s.mu.Unlock()
		if ready && s.quorumNode != nil {
			ready = s.taskQuorumReplicaReady() == nil
		}
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"service": "gptadmin", "ready": ready})
	})
}
