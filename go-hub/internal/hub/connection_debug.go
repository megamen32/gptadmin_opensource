package hub

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// connectionDebug is an operator-only, secret-safe snapshot of the Hub's
// connection graph. It intentionally complements /metrics (aggregates) and
// /mcp-relay/servers (discovery) with the evidence needed to explain a failed
// client connection without requiring access to process memory or logs.
func (s *Server) connectionDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "limit must be between 1 and 500"})
			return
		}
		limit = parsed
	}
	filter := strings.TrimSpace(r.URL.Query().Get("server_id"))
	now := s.now().Unix()

	s.mu.Lock()
	agents := s.publicAgentsLocked(r)
	connections := make([]map[string]any, 0, len(agents))
	statusCounts := map[string]int{}
	kindCounts := map[string]int{}
	transportCounts := map[string]int{}
	for _, agent := range agents {
		if filter != "" && agent.AgentID != filter {
			continue
		}
		item := agentAsServer(agent)
		age := any(nil)
		if agent.LastSeen > 0 {
			age = float64(now) - agent.LastSeen
		}
		item["health"] = map[string]any{
			"status":                agent.Status,
			"last_seen":             agent.LastSeen,
			"last_seen_age_seconds": age,
			"heartbeat_observed":    agent.LastSeen > 0,
		}
		connections = append(connections, item)
		statusCounts[agent.Status]++
		kindCounts[agent.Kind]++
		transportCounts[agent.Transport]++
	}
	sort.Slice(connections, func(i, j int) bool {
		return connections[i]["server_id"].(string) < connections[j]["server_id"].(string)
	})

	jobs := make([]map[string]any, 0, len(s.relayJobs)+len(s.shellJobs))
	jobStatusCounts := map[string]int{}
	for _, job := range s.relayJobs {
		jobs = append(jobs, map[string]any{
			"job_id": job.ID, "server_id": job.AgentID, "kind": "mcp_relay",
			"method": job.Method, "status": job.Status, "created_at": job.CreatedAt,
			"started_at": job.StartedAt, "completed_at": job.DoneAt,
			"trace_id": job.TraceID, "traceparent": job.TraceParent,
		})
		jobStatusCounts[job.Status]++
	}
	for _, job := range s.shellJobs {
		jobs = append(jobs, map[string]any{
			"job_id": job.ID, "server_id": "shell:" + job.Server, "kind": "shell",
			"tool_name": job.ToolName, "status": job.Status, "created_at": job.CreatedAt,
			"started_at": job.StartedAt, "completed_at": job.DoneAt,
			"trace_id": job.TraceID, "traceparent": job.TraceParent,
		})
		jobStatusCounts[job.Status]++
	}
	sort.Slice(jobs, func(i, j int) bool {
		left, _ := jobs[i]["created_at"].(float64)
		right, _ := jobs[j]["created_at"].(float64)
		if left == right {
			return jobs[i]["job_id"].(string) < jobs[j]["job_id"].(string)
		}
		return left > right
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}

	audit := make([]map[string]any, 0, limit)
	for i := len(s.audit) - 1; i >= 0 && len(audit) < limit; i-- {
		event := s.audit[i]
		audit = append(audit, map[string]any{
			"time":   event.Time,
			"name":   event.Name,
			"fields": redactPublicMetadata(event.Fields),
		})
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": s.now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"hub": map[string]any{
			"name": "gptadmin-go-hub", "build_version": BuildVersion, "git_commit": GitCommit,
			"public_origin": s.cfg.PublicOrigin, "mcp_resource": s.resource(r),
			"transport": "stateless_http_jsonrpc", "trace_header": requestTraceHeader,
		},
		"filter": map[string]any{"server_id": filter, "limit": limit},
		"summary": map[string]any{
			"connections": len(connections), "status_counts": statusCounts,
			"kind_counts": kindCounts, "transport_counts": transportCounts,
			"job_status_counts": jobStatusCounts, "audit_events_total": len(s.audit),
		},
		"connections":  connections,
		"jobs":         map[string]any{"items": jobs, "returned": len(jobs)},
		"recent_audit": audit,
	})
}
