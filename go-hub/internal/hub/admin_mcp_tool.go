package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// adminMCPTool is a same-origin, authenticated adapter for the dashboard.
// It invokes the same native tools/list and tools/call implementation as
// /server/<agent>/mcp; it never forwards browser cookies to a bearer endpoint.
// Task results are read separately via /admin/api/tasks/{taskId}.
func (s *Server) adminMCPTool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if cookieAdminSessionPresent(r, s) && !sameOriginAdminMutation(r, adminRequestOrigin(r)) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "same-origin admin request required"})
		return
	}
	var req struct {
		Target    string         `json:"target"`
		Method    string         `json:"method"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Context   string         `json:"context"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid MCP tool request"})
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "unexpected extra JSON"})
		return
	}
	if req.Method != "tools/list" && req.Method != "tools/call" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "method must be tools/list or tools/call"})
		return
	}
	if req.Target == "" || strings.TrimSpace(req.Target) != req.Target {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "exact target is required"})
		return
	}
	if req.Method == "tools/call" && (req.Name == "" || strings.TrimSpace(req.Name) != req.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "exact tool name is required"})
		return
	}
	if len(req.Context) > 16*1024 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "instruction context too long"})
		return
	}
	target, status, detail := s.selectMCPRelayTarget(req.Target)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	if !profileAllowsTarget(r, target) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "access profile denies this target"})
		return
	}
	s.mu.Lock()
	agent, ok := s.exposedAgentByIDLocked(target)
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "target is not exposed as a native MCP server"})
		return
	}
	if !agentOwnerProfileAllowed(r, agent) || !agentOwnerConnectionAllowed(r, agent) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "MCP target is not accessible"})
		return
	}
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion": mcpProtocolVersion,
		"io.modelcontextprotocol/clientInfo":      map[string]any{"name": "gptadmin-admin-tools", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{
			"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}},
		},
	}
	if req.Context != "" {
		meta[instructionContextMeta] = req.Context
	}
	params := map[string]any{"_meta": meta}
	if req.Method == "tools/call" {
		if req.Arguments == nil {
			req.Arguments = map[string]any{}
		}
		params["name"] = req.Name
		params["arguments"] = req.Arguments
	}
	result, rpcErr, _ := s.agentMCPJSONRPC(r, agent, map[string]any{"method": req.Method, "params": params})
	if rpcErr != nil {
		errBody := mapValue(rpcErr)
		status := http.StatusBadRequest
		if intFromAny(errBody["code"]) == -32003 {
			status = http.StatusForbidden
		} else if intFromAny(errBody["code"]) == -32004 {
			status = http.StatusPreconditionRequired
		} else if intFromAny(errBody["code"]) <= -32000 && intFromAny(errBody["code"]) > -32600 {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]any{"detail": firstString(errBody, "message"), "error": errBody})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}
