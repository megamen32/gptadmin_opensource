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
	taskStatuses := map[string]int{"working": 0, "input_required": 0, "completed": 0, "failed": 0, "cancelled": 0}
	for _, job := range s.relayJobs {
		if job != nil {
			taskStatuses[mcpTaskStatus(job.Status)]++
		}
	}
	for _, job := range s.shellJobs {
		if job != nil {
			taskStatuses[mcpTaskStatus(job.Status)]++
		}
	}
	agentStatuses := map[string]int{}
	for _, agent := range s.agents {
		if agent == nil {
			continue
		}
		status := agent.Status
		if status == "" {
			status = "unknown"
		}
		agentStatuses[status]++
	}
	payload := map[string]any{
		"build_version":      BuildVersion,
		"agents":             len(s.agents),
		"relay_jobs":         len(s.relayJobs),
		"relay_queue_jobs":   relayQueueJobs,
		"shell_jobs":         len(s.shellJobs),
		"shell_queue_jobs":   shellQueueJobs,
		"task_statuses":      taskStatuses,
		"agent_statuses":     agentStatuses,
		"audit_events":       len(s.audit),
		"telemetry_enabled":  telemetry.Enabled,
		"telemetry_counters": telemetry.Counters,
		"security_preset":    s.security.Preset,
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, payload)
}
