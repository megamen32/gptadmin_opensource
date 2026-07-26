package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/audit"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// supervisorActionReq is the JSON shape accepted on /capabilities/mcp/:
//   - "action" + "ref" both in body, OR
//   - "ref" in the URL path (the trailing path segment) and "action" in body/query.
type supervisorActionReq struct {
	Action string `json:"action"`
	Ref    string `json:"ref"`
}

// supervisorHandler implements the deprecated POST /capabilities/mcp/ API.
// It intentionally delegates to childMCP so this compatibility path cannot
// create a second detached process outside the MCP protocol session owner.
func (s *Server) supervisorHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	var req supervisorActionReq
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}

	// Fall back to the URL path: the trailing segment after /capabilities/mcp/
	// is treated as the agent ref.
	if req.Ref == "" {
		req.Ref = strings.TrimPrefix(r.URL.Path, "/capabilities/mcp/")
		req.Ref = strings.Trim(req.Ref, "/")
	}
	// And the query string, mirroring the Python shellmcp behavior.
	if req.Action == "" {
		req.Action = r.URL.Query().Get("action")
	}
	if req.Ref == "" {
		req.Ref = r.URL.Query().Get("ref")
	}

	if req.Action == "" || req.Ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing action or ref"})
		return
	}

	mgr := s.supervisor
	if mgr == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "supervisor not initialized"})
		return
	}

	s.auditLog.Event(audit.ServiceAction, map[string]any{
		"service": "supervisor",
		"action":  req.Action,
		"ref":     req.Ref,
	})

	agent, err := mgr.Agent(req.Ref)
	if err != nil {
		if errors.Is(err, supervisor.ErrUnknownRef) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error(), "ref": req.Ref})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "ref": req.Ref, "action": req.Action})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status := s.childMCP.Status(req.Ref)
	switch req.Action {
	case "start":
		err = s.childMCP.Start(ctx, agent)
	case "stop":
		err = s.childMCP.Close(req.Ref)
	case "restart":
		err = s.childMCP.Restart(ctx, agent)
	case "status":
		status = s.childMCP.Status(req.Ref)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action: " + req.Action})
		return
	}
	if err != nil {
		// Distinguish "unknown ref" so clients can recover.
		if errors.Is(err, supervisor.ErrUnknownRef) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error(), "ref": req.Ref})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "ref": req.Ref, "action": req.Action})
		return
	}

	resp := map[string]any{
		"ok":     true,
		"action": req.Action,
		"ref":    req.Ref,
	}
	if req.Action == "status" {
		resp["running"] = status.Running
		resp["pid"] = status.PID
		resp["started_at"] = status.StartedAt
		resp["exited_at"] = status.ExitedAt
		resp["exit_code"] = status.ExitCode
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpAgentsForCapabilities summarizes the supervisor's agent registry in
// the shape clients expect on /capabilities.
func (s *Server) mcpAgentsForCapabilities() []map[string]any {
	if s.supervisor == nil {
		return []map[string]any{}
	}
	agents := s.supervisor.Agents()
	out := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		out = append(out, map[string]any{
			"ref":       a.Ref,
			"name":      a.Name,
			"transport": a.Transport,
			"command":   a.Command,
			"args":      a.Args,
			"url":       a.URL,
			"enabled":   a.Enabled,
		})
	}
	return out
}
