package hub

import (
	"errors"
	"net/http"
	"strings"
)

func connectionOwnerRPCError() map[string]any {
	return map[string]any{"code": -32003, "message": "personal MCP requires its exact owner profile and managed HTTP connection"}
}

func agentOwnerConnectionAllowed(r *http.Request, agent Agent) bool {
	if !agentOwnerProfileAllowed(r, agent) {
		return false
	}
	if firstString(agent.Meta, "owner_profile_id") == "" {
		return true
	}
	if r == nil {
		return false
	}
	scope, exists := r.Context().Value(connectionContextKey{}).(connectionScope)
	return exists && scope.Target == agent.AgentID && scope.Key != "" && scope.Instance != ""
}

func agentOwnerProfileAllowed(r *http.Request, agent Agent) bool {
	owner := firstString(agent.Meta, "owner_profile_id")
	if owner == "" {
		return true
	}
	profile, verified := AccessProfileFromRequest(r)
	if !verified || profile.ID != owner || connectionPrincipal(r) == "" {
		return false
	}
	if enabled, specified := agent.Meta["enabled"]; specified && !truthyAny(enabled) {
		return false
	}
	return true
}

// The parent ShellMCP facade is a separate route to the same child. Resolve
// its authoritative descriptor before a caller can create any execution job.
func (s *Server) authorizeMCPConnectionOwner(r *http.Request, target, tool string, args map[string]any) error {
	// Definition maintenance is an explicit administrative operation, never
	// authority to execute a personal user's tools with a different profile.
	if tool == "mcp_manage" && r != nil {
		legacyOwner := s.cfg.CtlToken != "" && s.legacyCtlTokenAllowed() &&
			strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == s.cfg.CtlToken
		if legacyOwner || s.requestHasAdminRole(r) {
			return nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(target, "shell:") {
		switch tool {
		case "mcp_tools", "mcp_call", "mcp_request", "mcp_connection_close", "mcp_manage":
		default:
			return nil
		}
		ref := firstString(args, "ref")
		if tool == "mcp_manage" && firstString(args, "action") == "upsert" {
			ref = firstString(mapValue(args["config"]), "ref")
		}
		parent := s.agents[target]
		if parent == nil || ref == "" {
			return nil
		}
		for _, raw := range sliceValue(parent.Meta["mcp_agents"]) {
			descriptor := mapValue(raw)
			if firstString(descriptor, "ref") == ref && firstString(descriptor, "owner_profile_id") != "" {
				child := Agent{AgentID: "mcp:" + target + ":" + ref, Meta: descriptor}
				if !agentOwnerConnectionAllowed(r, child) {
					return errors.New("personal MCP requires its exact owner profile")
				}
			}
		}
		return nil
	}
	child, exists := s.exposedAgentByIDLocked(target)
	if exists && !agentOwnerConnectionAllowed(r, child) {
		return errors.New("personal MCP requires its exact owner profile")
	}
	return nil
}
