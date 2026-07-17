package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	accessModeFull     = "full"
	accessModeReadonly = "readonly"
)

type authClaimsContextKey struct{}

func requestWithAuthClaims(r *http.Request, claims map[string]any) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authClaimsContextKey{}, claims))
}

func requestAccessMode(r *http.Request) string {
	if r == nil {
		return accessModeFull
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	if len(claims) == 0 {
		return accessModeFull
	}
	if mode, _ := claims["access_mode"].(string); mode == accessModeReadonly {
		return accessModeReadonly
	}
	scopes := strings.Fields(firstString(claims, "scope"))
	if containsString(scopes, "gptadmin.exec") {
		return accessModeFull
	}
	if containsString(scopes, "gptadmin.read") || containsString(scopes, "gptadmin.inspect") {
		return accessModeReadonly
	}
	// A signed but unrecognized scope must never inherit command execution.
	return accessModeReadonly
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func authorizeToolCall(r *http.Request, target, toolName string) error {
	if requestAccessMode(r) != accessModeReadonly {
		return nil
	}
	if target == "hub" {
		switch toolName {
		case "listMcpServers", "list_mcp_servers", "listMcpAgents", "list_mcp_agents", "list_pending_servers", "pending", "hub_status", "status":
			return nil
		}
	}
	if strings.HasPrefix(target, "shell:") && toolName == "system_inspect" {
		return nil
	}
	return errors.New("read-only client cannot call this tool")
}

func authorizeFacadeCall(r *http.Request, name string, args map[string]any) error {
	if requestAccessMode(r) != accessModeReadonly {
		return nil
	}
	switch name {
	case "ui", "render_gptadmin_dashboard", "renderGptadminDashboard", "discover", "list_mcp_servers", "listMcpServers", "list_mcp_agents", "listMcpAgents", "schema", "list_mcp_tools", "listMcpTools", "inspect", "inspect_system", "inspectSystem", "job", "get_mcp_job", "getMcpJob":
		return nil
	case "execute", "call_mcp_tool", "callMcpTool":
		return authorizeToolCall(r, firstString(args, "target", "server_id", "agent_id"), firstString(args, "tool", "tool_name", "name"))
	default:
		return errors.New("read-only client cannot call this tool")
	}
}

func appsSDKToolsForRequest(r *http.Request) []map[string]any {
	tools := appsSDKTools()
	if requestAccessMode(r) != accessModeReadonly {
		return tools
	}
	filtered := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if authorizeFacadeCall(r, firstString(tool, "name"), nil) == nil {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func toolsForRequest(r *http.Request, target string, tools []map[string]any) []map[string]any {
	if requestAccessMode(r) != accessModeReadonly {
		return tools
	}
	filtered := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if authorizeToolCall(r, target, firstString(tool, "name")) == nil {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func mcpClientHTTPPathAllowed(path string) bool {
	return strings.HasPrefix(path, "/mcp-relay/")
}
