package hub

import (
	"database/sql"
	"strings"
)

// Explicit registration cleanup preserves both durable and in-flight work.
// A failed task-store read is uncertainty, never evidence of an idle executor.
func (s *Server) shellCleanupBlockerLocked(id string) string {
	if s.localExecutors[id] != nil || (s.nodeConns != nil && s.nodeConns.get(id) != nil) {
		return "live_executor"
	}
	name := strings.TrimPrefix(id, "shell:")
	childPrefix := "mcp:" + id + ":"
	for _, j := range s.shellJobs {
		if j != nil && j.Server == name && !taskTerminal(j.Status) {
			return "unfinished_tasks"
		}
	}
	for _, j := range s.relayJobs {
		if j != nil && (j.AgentID == id || strings.HasPrefix(j.AgentID, childPrefix)) && !taskTerminal(j.Status) {
			return "unfinished_tasks"
		}
	}
	if s.cfg.ConfigDir != "" {
		var pending int
		err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT coalesce(sum(n),0) FROM task_status_counts WHERE n>0 AND status NOT IN ('completed','failed','cancelled') AND (server=? OR server=? OR substr(server,1,?)=?)`, name, id, len(childPrefix), childPrefix).Scan(&pending)
		})
		if err != nil {
			return "task_state_unavailable"
		}
		if pending > 0 {
			return "unfinished_tasks"
		}
	}
	return ""
}
