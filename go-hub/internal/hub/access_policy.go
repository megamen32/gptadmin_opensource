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

const (
	approvalModeReadOnly          = "read_only"
	approvalModeAskBeforeWrite    = "ask_before_write"
	approvalModeBoundedAutonomous = "bounded_autonomous"
)

func requestWithAuthClaims(r *http.Request, claims map[string]any) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authClaimsContextKey{}, claims))
}

// requestWithAutomationProfile gives non-interactive ingress an auditable
// policy identity instead of allowing it to execute with a nil request.
func requestWithAutomationProfile(r *http.Request, actor, target, tool, approvalMode string) *http.Request {
	if r == nil {
		r, _ = http.NewRequest(http.MethodPost, "http://automation.invalid/", nil)
	}
	if approvalMode != approvalModeAskBeforeWrite && approvalMode != approvalModeBoundedAutonomous {
		approvalMode = approvalModeAskBeforeWrite
	}
	claims := map[string]any{
		"sub":         actor,
		"client_id":   actor,
		"scope":       "gptadmin.read gptadmin.exec",
		"access_mode": accessModeFull,
	}
	r = requestWithAuthClaims(r, claims)
	return requestWithAccessProfile(r, AccessProfile{
		ID:             "automation:" + actor,
		AccessMode:     accessModeFull,
		ApprovalMode:   approvalMode,
		AllowedTargets: []string{target},
		AllowedTools:   []string{tool},
		Version:        1,
	})
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

func profileAllowsTarget(r *http.Request, target string) bool {
	profile, bound := AccessProfileFromRequest(r)
	if !bound {
		return true
	}
	return containsString(profile.AllowedTargets, target)
}

func profileAllowsTool(r *http.Request, toolName string) bool {
	profile, bound := AccessProfileFromRequest(r)
	if !bound {
		return true
	}
	return containsString(profile.AllowedTools, toolName)
}

func authorizeToolCall(r *http.Request, target, toolName string) error {
	if !profileAllowsTarget(r, target) || !profileAllowsTool(r, toolName) {
		return errors.New("access profile denies this target or tool")
	}
	if requestAccessMode(r) != accessModeReadonly {
		return nil
	}
	if target == "hub" {
		switch toolName {
		case "listMcpServers", "list_mcp_servers", "listMcpAgents", "list_mcp_agents", "list_pending_servers", "pending", "hub_status", "status", "demo", "resource_receipt", webhookRoutesListTool, webhookJobGetTool:
			return nil
		}
	}
	if target == "webhooks" && (toolName == webhookRoutesListTool || toolName == webhookJobGetTool) {
		return nil
	}
	if toolName == "resources/list" || toolName == "resources/read" {
		return nil
	}
	if strings.HasPrefix(target, "shell:") && toolName == "system_inspect" {
		return nil
	}
	return errors.New("read-only client cannot call this tool")
}

func authorizeFacadeCall(r *http.Request, name string, args map[string]any) error {
	if !profileAllowsTool(r, name) {
		return errors.New("access profile denies this facade")
	}
	if requestAccessMode(r) != accessModeReadonly {
		switch name {
		case "schema", "list_mcp_tools", "listMcpTools", "inspect", "inspect_system", "inspectSystem":
			target := firstString(args, "target", "server_id", "agent_id")
			if target != "" && !profileAllowsTarget(r, target) {
				return errors.New("access profile denies this target")
			}
			return nil
		case "execute", "call_mcp_tool", "callMcpTool":
			return authorizeToolCall(r, firstString(args, "target", "server_id", "agent_id"), firstString(args, "tool", "tool_name", "name"))
		default:
			return nil
		}
	}
	switch name {
	case "ui", "render_gptadmin_dashboard", "renderGptadminDashboard", "resource_receipt", "discover", "demo", "list_mcp_servers", "listMcpServers", "list_mcp_agents", "listMcpAgents", "pending", "list_pending_servers", "job", "get_mcp_job", "getMcpJob", webhookRoutesListTool, webhookJobGetTool:
		return nil
	case "schema", "list_mcp_tools", "listMcpTools", "inspect", "inspect_system", "inspectSystem":
		target := firstString(args, "target", "server_id", "agent_id")
		if target != "" && !profileAllowsTarget(r, target) {
			return errors.New("access profile denies this target")
		}
		return nil
	case "execute", "call_mcp_tool", "callMcpTool":
		return authorizeToolCall(r, firstString(args, "target", "server_id", "agent_id"), firstString(args, "tool", "tool_name", "name"))
	default:
		return errors.New("read-only client cannot call this tool")
	}
}

func appsSDKToolsForRequest(r *http.Request) []map[string]any {
	tools := appsSDKTools()
	_, bound := AccessProfileFromRequest(r)
	if !bound && requestAccessMode(r) != accessModeReadonly {
		return tools
	}
	filtered := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		name := firstString(tool, "name")
		allowed := profileAllowsTool(r, name)
		if !bound {
			allowed = authorizeFacadeCall(r, name, nil) == nil
		}
		if allowed {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func toolsForRequest(r *http.Request, target string, tools []map[string]any) []map[string]any {
	_, bound := AccessProfileFromRequest(r)
	if !bound && requestAccessMode(r) != accessModeReadonly {
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
	return strings.HasPrefix(path, "/mcp-relay/") ||
		path == "/webhook-routes" || strings.HasPrefix(path, "/webhook-routes/") ||
		strings.HasPrefix(path, "/admin/api/webhook-jobs/")
}
