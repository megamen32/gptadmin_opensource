package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const virtualMCPStateFilename = "virtual_mcps_state.json"

var virtualMCPDefinitions = map[string]Agent{
	"network-proxy": {
		AgentID: "network-proxy", Name: "Network Proxy", Kind: "virtual_mcp", Transport: "internal", Status: "online",
		Capabilities: []string{"tools/list", "tools/call"}, Meta: map[string]any{"optional": true, "category": "network"},
	},
	"webhooks": {
		AgentID: "webhooks", Name: "Webhooks", Kind: "virtual_mcp", Transport: "internal", Status: "online",
		Capabilities: []string{"tools/list", "tools/call"}, Meta: map[string]any{"optional": true, "category": "automation"},
	},
}

type virtualMCPState struct {
	Enabled map[string]bool `json:"enabled"`
}

func (s *Server) virtualMCPStatePath() string {
	if s.cfg.VirtualMCPStateFile != "" {
		return s.cfg.VirtualMCPStateFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, virtualMCPStateFilename)
}

func (s *Server) loadVirtualMCPState() error {
	path := s.virtualMCPStatePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state virtualMCPState
	if err := json.Unmarshal(b, &state); err != nil {
		return fmt.Errorf("decode virtual MCP state: %w", err)
	}
	for id, enabled := range state.Enabled {
		if _, ok := virtualMCPDefinitions[id]; !ok && enabled {
			return fmt.Errorf("unknown virtual MCP %q", id)
		}
		s.virtualMCP[id] = enabled
	}
	return nil
}

func (s *Server) saveVirtualMCPStateLocked() error {
	path := s.virtualMCPStatePath()
	if path == "" {
		return nil
	}
	state := virtualMCPState{Enabled: map[string]bool{}}
	for id := range virtualMCPDefinitions {
		state.Enabled[id] = s.virtualMCP[id]
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".virtual-mcps-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *Server) virtualAgentsLocked() []Agent {
	ids := make([]string, 0, len(virtualMCPDefinitions))
	for id := range virtualMCPDefinitions {
		if s.virtualMCP[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	agents := make([]Agent, 0, len(ids))
	for _, id := range ids {
		a := virtualMCPDefinitions[id]
		a.LastSeen = nowFloat()
		a.Meta = cloneMap(a.Meta)
		agents = append(agents, a)
	}
	return agents
}

func isVirtualMCPAgent(agent Agent) bool {
	_, ok := virtualMCPDefinitions[agent.AgentID]
	return ok
}

func virtualMCPToolID(name string) string {
	for id, agent := range virtualMCPDefinitions {
		for _, tool := range virtualMCPTools(agent) {
			if firstString(tool, "name") == name {
				return id
			}
		}
	}
	return ""
}

func virtualMCPTools(agent Agent) []map[string]any {
	switch agent.AgentID {
	case "network-proxy":
		return networkProxyHubTools()
	case "webhooks":
		return webhookHubTools()
	default:
		return []map[string]any{}
	}
}

func (s *Server) virtualMCPToolsForRequest(r *http.Request, agent Agent) []map[string]any {
	tools := virtualMCPTools(agent)
	filtered := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		name := firstString(tool, "name")
		if authorizeFacadeCall(r, name, nil) == nil {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (s *Server) callVirtualMCPTool(r *http.Request, agent Agent, name string, args map[string]any) (any, any) {
	if err := authorizeToolCall(r, agent.AgentID, name); err != nil {
		return nil, map[string]any{"code": -32003, "message": err.Error()}
	}
	result, status := s.callVirtualMCP(agent, AccessProfileIDFromRequest(r), name, args)
	if status >= http.StatusBadRequest {
		return nil, map[string]any{"code": -32000, "message": "virtual MCP tool failed", "data": result}
	}
	return mcpToolResult(result), nil
}

func (s *Server) callVirtualMCP(agent Agent, profileID, name string, args map[string]any) (map[string]any, int) {
	switch agent.AgentID {
	case "network-proxy":
		return s.callNetworkProxyTool(profileID, name, args)
	case "webhooks":
		return s.callWebhookHubTool(name, args)
	default:
		return map[string]any{"error": "unknown virtual MCP"}, http.StatusNotFound
	}
}

func (s *Server) adminVirtualMCPEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/virtual-mcps")
	id := strings.Trim(strings.TrimSpace(path), "/")
	if id == "" && r.Method == http.MethodGet {
		s.mu.Lock()
		items := make([]map[string]any, 0, len(virtualMCPDefinitions))
		for _, definition := range virtualMCPDefinitions {
			items = append(items, map[string]any{"id": definition.AgentID, "name": definition.Name, "enabled": s.virtualMCP[definition.AgentID], "mcp_path": "/server/" + definition.AgentID + "/mcp", "actions_path": "/server/" + definition.AgentID + "/actions/openapi.yaml"})
		}
		s.mu.Unlock()
		sort.Slice(items, func(i, j int) bool { return items[i]["id"].(string) < items[j]["id"].(string) })
		writeJSON(w, http.StatusOK, map[string]any{"virtual_mcps": items})
		return
	}
	if id == "" || r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "use GET /admin/api/virtual-mcps or PUT /admin/api/virtual-mcps/{id}"})
		return
	}
	if _, ok := virtualMCPDefinitions[id]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown virtual MCP", "id": id})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	s.mu.Lock()
	previous := s.virtualMCP[id]
	s.virtualMCP[id] = req.Enabled
	if err := s.saveVirtualMCPStateLocked(); err != nil {
		s.virtualMCP[id] = previous
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "enabled": req.Enabled})
}
