package hub

import (
	"net/http"
	"strings"
)

// adminTaskReceipt exposes an existing MCP Tasks status through the standard
// authenticated admin session. Browser clients must not receive a bearer token
// in JavaScript just to inspect an existing task. This HTTP adapter does not
// submit or replay any execution; it reads the canonical MCP task ledger.
func (s *Server) adminTaskReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	taskID := strings.TrimPrefix(r.URL.Path, "/admin/api/tasks/")
	if taskID == "" || len(taskID) > 256 || strings.ContainsAny(taskID, "/\\") ||
		strings.TrimSpace(taskID) != taskID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid task id"})
		return
	}
	result, rpcErr := s.mcpTaskGetForRequest(r, taskID, "")
	if rpcErr != nil {
		// Never distinguish an inaccessible receipt from an absent one.
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "task unavailable"})
		return
	}
	task := mapValue(result)
	if firstString(task, "taskId") != taskID {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "task identity mismatch"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}
