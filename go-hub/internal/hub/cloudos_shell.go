package hub

import (
	"net/http"
	"strings"
	"time"
)

// cloudOSWaitTimeout keeps the foreground wait long enough to actually
// collect a result: DefaultTimeout is 0 on several node installs, which used
// to return "still running" before the executor ever picked the job up.
func cloudOSWaitTimeout(req map[string]any, def, min time.Duration) time.Duration {
	if t := timeoutFromReq(req, def); t > min {
		return t
	}
	return min
}

// cloudOSShellComputers is a narrow, UI-oriented projection of existing
// ShellMCP targets. CloudOS is only a client of ShellMCP, never a second host
// enrollment protocol.
func (s *Server) cloudOSShellComputers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	s.mu.Lock()
	computers := make([]map[string]any, 0)
	for _, agent := range s.agents {
		if agent == nil || !strings.HasPrefix(agent.AgentID, "shell:") {
			continue
		}
		computers = append(computers, map[string]any{"id": agent.AgentID, "name": strings.TrimPrefix(agent.AgentID, "shell:"), "os": firstString(agent.Meta, "os", "platform"), "capabilities": []string{"terminal", "files"}, "status": agent.Status, "last_seen": agent.LastSeen})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"computers": computers})
}

func (s *Server) cloudOSShellExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target, cmd := firstString(req, "target", "computer"), firstString(req, "cmd", "command")
	if !strings.HasPrefix(target, "shell:") || cmd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target must be a ShellMCP computer and command is required"})
		return
	}
	resp, status := s.executeMCPTool(r, target, "shell_exec", map[string]any{"cmd": cmd, "cwd": req["cwd"], "timeout": req["timeout"]}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 20*time.Second), "")
	writeJSON(w, status, resp)
}

func (s *Server) cloudOSShellInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target, path := firstString(req, "target", "computer"), firstString(req, "path")
	if !strings.HasPrefix(target, "shell:") || path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target must be a ShellMCP computer and path is required"})
		return
	}
	resp, status := s.executeMCPTool(r, target, "system_inspect", map[string]any{"action": "list_directory", "path": path, "max_bytes": 1048576}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 15*time.Second), "")
	writeJSON(w, status, resp)
}

func (s *Server) cloudOSBrowserConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	s.mu.Lock()
	connectors := []map[string]any{}
	for _, parent := range s.agents {
		if parent == nil || !strings.HasPrefix(parent.AgentID, "shell:") {
			continue
		}
		for _, agent := range childMCPAgents(*parent) {
			if !strings.Contains(strings.ToLower(agent.Name), "browser") {
				continue
			}
			connectors = append(connectors, map[string]any{"id": agent.AgentID, "name": agent.Name, "status": agent.Status, "parent": firstString(agent.Meta, "parent_server_id")})
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"connectors": connectors})
}

func (s *Server) cloudOSBrowserTabs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	target := firstString(req, "connector")
	if !strings.HasPrefix(target, "mcp:") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "connector is required"})
		return
	}
	resp, status := s.executeMCPTool(r, target, "tabs", map[string]any{"action": "list"}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 15*time.Second), "")
	writeJSON(w, status, resp)
}
