package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	adminMCPInventoryHub       = "hub"
	adminMCPInventoryHubCore   = "hub_core"
	adminMCPInventoryVirtual   = "hub_virtual_mcp"
	adminMCPInventoryHost      = "registered_host"
	adminMCPInventoryChild     = "registered_runtime_child"
	adminMCPInventoryNodeChild = "node_child_config"
)

func (s *Server) handleAdminMCPManage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid MCP inventory request"})
		return
	}
	target := strings.TrimSpace(firstString(req, "target"))
	action := strings.TrimSpace(firstString(req, "action"))
	if action == "" {
		action = "list"
	}
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target is required"})
		return
	}
	if target == adminMCPInventoryHub {
		if action != "list" {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"detail": "Hub inventory supports list only"})
			return
		}
		s.mu.Lock()
		servers := s.adminHubMCPInventoryLocked(r)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target, "action": action, "response": map[string]any{"servers": servers, "complete": true}})
		return
	}
	if !strings.HasPrefix(target, "shell:") || strings.TrimSpace(strings.TrimPrefix(target, "shell:")) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target must be hub or an enrolled shell:<host>"})
		return
	}
	if action != "list" && action != "status" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"detail": "This endpoint supports MCP inventory and status reads only"})
		return
	}
	args := map[string]any{"action": action}
	if action == "status" {
		ref := strings.TrimSpace(firstString(req, "ref", "name"))
		if ref == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "ref is required for MCP status"})
			return
		}
		args["ref"] = ref
	}
	controlTarget := "control:" + strings.TrimPrefix(target, "shell:")
	// This is an authenticated operator route. Keep the ordinary target/tool/action
	// authorization and node drain gates; only agent instruction context is omitted.
	timeout := s.cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	result, status := s.executeMCPTool(nonAgentWorkflow(r), controlTarget, "mcp_manage", args, false, timeout, "")
	if status < 200 || status >= 300 {
		writeJSON(w, status, map[string]any{"detail": mcpManageFailureDetail(result)})
		return
	}
	if action == "status" {
		response, err := extractMCPManageStatus(result)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target, "action": action, "response": response})
		return
	}
	items, err := extractMCPManageAgents(result)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	servers, err := normalizeNodeMCPInventory(target, items)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": target, "action": action, "response": map[string]any{"servers": servers, "complete": true, "source": "shellmcp_supervisor"}})
}

// adminHubMCPInventoryLocked projects the Hub's target registry without
// presenting paired control/file routes as separate installed MCP servers.
// Callers hold s.mu. The virtual definition list includes disabled entries;
// child_mcp rows remain the enabled runtime descriptors reported by ShellMCP.
func (s *Server) adminHubMCPInventoryLocked(r *http.Request) []map[string]any {
	rows := make([]map[string]any, 0, len(s.agents)+len(virtualMCPDefinitions)+1)
	for _, agent := range s.publicAgentsLocked(r) {
		if agent.Kind == "virtual_mcp" || agent.Kind == "virtual_control" || agent.Kind == "virtual_file" || truthy(agent.Meta["backing_server_id"]) {
			continue
		}
		row := agentAsServer(agent)
		switch {
		case agent.Kind == "hub":
			row["registry_category"] = adminMCPInventoryHubCore
		case isChildMCPAgent(agent):
			row["registry_category"] = adminMCPInventoryChild
			row["enabled"] = truthy(agent.Meta["enabled"])
		default:
			row["registry_category"] = adminMCPInventoryHost
			row["enabled"] = agent.Status != "disabled"
		}
		rows = append(rows, row)
	}
	for id, definition := range virtualMCPDefinitions {
		definition.Status = "disabled"
		if s.virtualMCP[id] {
			definition.Status = "online"
		}
		row := agentAsServer(definition)
		row["enabled"] = s.virtualMCP[id]
		row["registry_category"] = adminMCPInventoryVirtual
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := firstString(rows[i], "registry_category"), firstString(rows[j], "registry_category")
		if left != right {
			return left < right
		}
		return firstString(rows[i], "server_id") < firstString(rows[j], "server_id")
	})
	return rows
}

func normalizeNodeMCPInventory(target string, items []map[string]any) ([]map[string]any, error) {
	servers := make([]map[string]any, 0, len(items))
	for _, item := range items {
		ref := strings.TrimSpace(firstString(item, "ref", "server_id"))
		if ref == "" {
			return nil, errors.New("ShellMCP returned a child entry without a ref; inventory is unavailable")
		}
		name := firstString(item, "name")
		if name == "" {
			name = ref
		}
		enabled := truthyAny(item["enabled"])
		status := nodeMCPConfiguredStatus(item, enabled)
		row := map[string]any{
			"server_id":         ref,
			"ref":               ref,
			"name":              name,
			"kind":              "child_mcp_config",
			"registry_category": adminMCPInventoryNodeChild,
			"parent_server_id":  target,
			"enabled":           enabled,
			"status":            status,
		}
		for _, key := range []string{"transport", "command", "args", "url", "health"} {
			if value, ok := item[key]; ok {
				row[key] = value
			}
		}
		servers = append(servers, row)
	}
	sort.Slice(servers, func(i, j int) bool {
		return firstString(servers[i], "server_id") < firstString(servers[j], "server_id")
	})
	return servers, nil
}

func nodeMCPConfiguredStatus(item map[string]any, enabled bool) string {
	if !enabled {
		return "disabled"
	}
	health := mapValue(item["health"])
	protocol := mapValue(health["protocol"])
	process := mapValue(health["process"])
	if firstString(protocol, "state") == "failed" || firstString(process, "state") == "exited" {
		return "failed"
	}
	if firstString(protocol, "state") == "ready" || firstString(process, "state") == "running" {
		return "online"
	}
	// Enabled is desired configuration, not proof that a lazy child process is running.
	return "configured"
}

func extractMCPManageAgents(raw map[string]any) ([]map[string]any, error) {
	value, err := findMCPManagePayload(raw, "agents", 0)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("ShellMCP response did not contain an MCP supervisor list")
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("ShellMCP supervisor list has an invalid shape")
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := mapValue(item)
		if len(row) == 0 {
			return nil, errors.New("ShellMCP supervisor list contains a non-object entry")
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func extractMCPManageStatus(raw map[string]any) (map[string]any, error) {
	value, err := findMCPManagePayload(raw, "agent", 0)
	if err != nil {
		return nil, err
	}
	status, statusErr := findMCPManagePayload(raw, "status", 0)
	if statusErr != nil {
		return nil, statusErr
	}
	if value == nil || status == nil {
		return nil, errors.New("ShellMCP response did not contain MCP status")
	}
	return map[string]any{"agent": value, "status": status}, nil
}

func findMCPManagePayload(value any, key string, depth int) (any, error) {
	if depth > 10 {
		return nil, errors.New("ShellMCP response nesting exceeded the supported limit")
	}
	object := mapValue(value)
	if len(object) == 0 {
		return nil, nil
	}
	if truthyAny(object["isError"]) || firstString(object, "status") == "failed" {
		return nil, errors.New(mcpManageFailureDetail(object))
	}
	if candidate, exists := object[key]; exists {
		if key != "status" {
			return candidate, nil
		}
		if _, isEnvelopeStatus := candidate.(string); !isEnvelopeStatus {
			return candidate, nil
		}
	}
	for _, nestedKey := range []string{"response", "structuredContent", "result", "data"} {
		if nested, ok := object[nestedKey]; ok {
			found, err := findMCPManagePayload(nested, key, depth+1)
			if err != nil || found != nil {
				return found, err
			}
		}
	}
	for _, content := range sliceValue(object["content"]) {
		entry := mapValue(content)
		text, _ := entry["text"].(string)
		text = strings.TrimSpace(text)
		if text == "" || !json.Valid([]byte(text)) {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			continue
		}
		found, err := findMCPManagePayload(decoded, key, depth+1)
		if err != nil || found != nil {
			return found, err
		}
	}
	return nil, nil
}

func mcpManageFailureDetail(raw map[string]any) string {
	if detail := firstString(raw, "detail", "error"); detail != "" {
		return detail
	}
	if errorValue := mapValue(raw["error"]); len(errorValue) > 0 {
		if detail := firstString(errorValue, "message", "detail", "code"); detail != "" {
			return detail
		}
	}
	for _, content := range sliceValue(raw["content"]) {
		if detail := firstString(mapValue(content), "text"); detail != "" {
			return detail
		}
	}
	return fmt.Sprintf("MCP inventory request failed (status %s)", firstString(raw, "status"))
}
