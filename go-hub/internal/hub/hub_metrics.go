package hub

import "net/http"

// hubMetrics exposes only bounded aggregate state. It is intentionally
// payload-free so liveness/operations probes cannot become a data exfiltration
// path for credentials, arguments or file contents.
func (s *Server) hubMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	telemetry := s.telemetrySnapshot()
	s.mu.Lock()
	relayQueueJobs := 0
	for _, queue := range s.relayQueues {
		relayQueueJobs += len(queue)
	}
	shellQueueJobs := 0
	for _, queue := range s.shellQueues {
		shellQueueJobs += len(queue)
	}
	payload := map[string]any{
		"build_version":      BuildVersion,
		"agents":             len(s.agents),
		"relay_jobs":         len(s.relayJobs),
		"relay_queue_jobs":   relayQueueJobs,
		"shell_jobs":         len(s.shellJobs),
		"shell_queue_jobs":   shellQueueJobs,
		"audit_events":       len(s.audit),
		"telemetry_enabled":  telemetry.Enabled,
		"telemetry_counters": telemetry.Counters,
		"security_preset":    s.security.Preset,
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, payload)
}
