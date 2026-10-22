package hub

import (
	"encoding/json"
	"net/http"
	"strings"
)

// controlCatalogToolDefinition is intentionally a read-only entry point. The
// catalog points at the existing, independently authorized writers rather
// than creating a second configuration mutation path.
func controlCatalogToolDefinition() map[string]any {
	return map[string]any{
		"name":        "control_catalog",
		"title":       "Control settings and capabilities",
		"description": "Show the real configuration surfaces, current redacted values, schemas, and existing writers for this control target.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"section": map[string]any{"type": "string", "description": "Optional exact section id."}}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
	}
}

// controlCatalog returns a compact catalog of settings and real control
// mechanisms. It deliberately distinguishes settings with a supported writer
// from configuration that is only available through the installation's
// protected admin/configuration plane.
func (s *Server) controlCatalog(r *http.Request, target, section string) map[string]any {
	section = strings.TrimSpace(section)
	s.mu.Lock()
	settings := redactControlValue(s.hubSettingsLocked())
	schema := hubSettingsSchema()
	shellRuntime := s.shellRuntimeSettingsLocked()
	failover := s.failover
	var descriptors []any
	if strings.HasPrefix(target, "control:") && target != "control:hub" {
		if parent := s.agents["shell:"+strings.TrimPrefix(target, "control:")]; parent != nil {
			for _, raw := range sliceValue(parent.Meta["mcp_agents"]) {
				descriptor := mapValue(raw)
				child := Agent{AgentID: "mcp:" + strings.TrimPrefix(target, "control:") + ":" + firstString(descriptor, "ref"), Meta: descriptor}
				if agentOwnerConnectionAllowed(r, child) {
					descriptors = append(descriptors, redactPublicMetadata(descriptor))
				}
			}
		}
	}
	s.mu.Unlock()

	// Each current value is protected by the same target, tool, access-mode and
	// owner checks as its real reader. A caller who can see the catalog shell
	// does not thereby inherit access to hub-global state.
	canHubSettingsRead := authorizeToolCall(r, "hub", "settings_get") == nil
	canHubSettingsWrite := authorizeToolCall(r, "hub", "settings_set") == nil
	canWebhookRead := authorizeToolCall(r, "hub", webhookRoutesListTool) == nil
	webhookWriters := allowedCatalogTools(r, "hub", webhookRouteCreateTool, webhookRouteReplaceTool, webhookRouteDeleteTool)
	canWebhookWrite := len(webhookWriters) != 0
	canFailoverRead := s.requestHasAdminRole(r) && profileAllowsTarget(r, "hub")
	proxyReaders := allowedCatalogTools(r, "hub", "network_proxy_status", "network_access_status")
	proxyWriters := allowedCatalogTools(r, "hub", "network_proxy_request", "network_proxy_approve", "network_proxy_issue", "network_proxy_revoke", "network_access_enable", "network_access_disable")
	canProxyRead := len(proxyReaders) != 0
	canProxyWrite := len(proxyWriters) != 0
	canNodeRead := target != "control:hub" && profileAllowsTarget(r, target) &&
		(profileAllowsTool(r, "mcp_tools") || profileAllowsTool(r, "mcp_manage"))
	canNodeWrite := target != "control:hub" && profileAllowsTarget(r, target) &&
		profileAllowsTool(r, "mcp_manage") && requestAccessMode(r) != accessModeReadonly
	if !canHubSettingsRead {
		settings = nil
		shellRuntime = nil
	}
	if !canNodeRead {
		descriptors = nil
	}

	defs := append([]hubSettingDefinition(nil), hubSettingRegistry...)
	filteredDefs := make([]hubSettingDefinition, 0, len(defs))
	for _, def := range defs {
		if section == "" || section == "hub-shellmcp" || section == "hub" {
			filteredDefs = append(filteredDefs, def)
		}
	}
	if len(filteredDefs) != len(defs) {
		filteredSchema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
		properties := filteredSchema["properties"].(map[string]any)
		for _, def := range filteredDefs {
			properties[def.Key] = map[string]any{"type": def.Type, "description": def.Description, "default": def.Default}
		}
		schema = filteredSchema
	}

	hubSection := map[string]any{
		"id": "hub-shellmcp", "title": "Hub и ShellMCP runtime",
		"scope":  "Hub registry; runtime values are delivered to enrolled ShellMCP nodes",
		"values": map[string]any{"hub": settings, "shellmcp_runtime": shellRuntime},
		"schema": schema, "definitions": filteredDefs,
		"capabilities": filterCatalogTools(r, "hub", selectedToolDefinitions(hubTools(), "settings_get", "settings_schema", "settings_set")),
		"access":       map[string]any{"read": canHubSettingsRead, "write": canHubSettingsWrite},
		"writable":     canHubSettingsWrite, "writer": catalogWriter(canHubSettingsWrite, "control:hub.settings_set (same typed registry and validation as settings_set)"),
		"read_api":         []string{"control:hub.settings_get", "control:hub.settings_schema"},
		"persistence":      "Hub registry authority/state; settings history is revisioned",
		"restart_required": false,
		"notes":            "mcp_auto_cleanup_enabled controls stale non-shell registry cleanup. ShellMCP self-repair is fleet_auto_update_enabled / fleet_desired_build_version. These do not edit the node's local daemon configuration.",
	}
	if !canHubSettingsRead {
		delete(hubSection, "values")
	}

	var webhookRoutes any
	if canWebhookRead {
		webhookRoutes = s.listWebhookRouteSummaries()
	}
	webhooks := map[string]any{
		"id": "webhooks", "title": "Webhook routes",
		"scope":  "Hub ingress routes and durable webhook jobs",
		"values": map[string]any{"routes": webhookRoutes},
		"schema": webhookRouteInputSchema(), "writable": canWebhookWrite,
		"access":           map[string]any{"read": canWebhookRead, "write": canWebhookWrite},
		"capabilities":     filterCatalogTools(r, "hub", webhookHubTools()),
		"writer":           catalogWriter(canWebhookWrite, webhookWriters),
		"read_api":         []string{"webhook_routes_list", "webhook_job_get"},
		"persistence":      "configured webhook routes file; jobs/delivery state in webhook state store",
		"restart_required": false,
		"notes":            "Route summaries intentionally omit token, HMAC secret, callback credentials, and action arguments. Secrets are write-only; route mutations use the existing validator and persistence path.",
	}
	if !canWebhookRead {
		delete(webhooks, "values")
	}

	proxyTools := networkProxyHubTools()
	proxy := map[string]any{
		"id": "network-proxy", "title": "Network Tunnel capabilities",
		"scope": "Per-caller access profile and explicitly bounded target policy",
		"values": map[string]any{"controller_available": func() any {
			if canProxyRead {
				return s.networkProxy != nil && s.networkProxy.unavailable == nil
			}
			return nil
		}()},
		"schema":       map[string]any{"policy": map[string]any{"type": "object", "properties": map[string]any{"scope": map[string]any{"type": "string", "enum": []string{"host", "service"}}, "agent_id": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []string{"webhook", "pull", "auto"}}, "target_cidrs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "target_ports": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "max_streams": map[string]any{"type": "integer"}, "max_bytes": map[string]any{"type": "integer"}, "lease": map[string]any{"type": "string"}}, "required": []string{"scope", "agent_id", "mode", "target_cidrs", "target_ports", "max_streams", "max_bytes", "lease"}, "additionalProperties": false}},
		"capabilities": filterCatalogTools(r, "hub", proxyTools),
		"writable":     canProxyWrite, "access": map[string]any{"read": canProxyRead, "write": canProxyWrite}, "writer": catalogWriter(canProxyWrite, proxyWriters),
		"read_api": []string{"network_proxy_status"}, "persistence": "capability state file; active grants expire and are not restored after restart",
		"restart_required": false,
		"unavailable":      []string{"global outbound proxy listener/upstream configuration has no MCP writer; it is installation-managed configuration"},
		"notes":            "This surface grants bounded Network Tunnel capabilities. It does not reconfigure an HTTP/SOCKS proxy listener or node egress defaults.",
	}
	if !canProxyRead {
		delete(proxy, "values")
	}

	failoverSection := map[string]any{
		"id": "failover", "title": "Hub failover and mesh",
		"scope": "Hub deployment topology",
		"values": func() any {
			if canFailoverRead {
				return redactControlValue(failover)
			}
			return nil
		}(),
		"schema": failoverConfigSchema(), "writable": false, "access": map[string]any{"read": canFailoverRead, "write": false},
		"capabilities":     []any{map[string]any{"name": "GET /admin/api/failover", "description": "Read current failover config and state; authenticated admin API", "schema": map[string]any{"type": "object"}}, map[string]any{"name": "PUT /admin/api/failover", "description": "Validate, persist, and apply failover configuration; authenticated admin API", "schema": failoverConfigSchema()}},
		"writer":           "authenticated admin API PUT /admin/api/failover (outside MCP; this catalog cannot write it)",
		"read_api":         []string{"authenticated admin API GET /admin/api/failover", "authenticated admin API GET /admin/api/failover/state"},
		"persistence":      "failover_config.json and failover_state.json; config is loaded by Hub",
		"restart_required": true,
		"notes":            "This MCP catalog is read-only for failover. Use the existing admin API; no settings_set alias is provided.",
	}
	if !canFailoverRead {
		delete(failoverSection, "values")
	}

	nodeSection := map[string]any{
		"id": "node-shellmcp", "title": "ShellMCP node daemon and child MCP lifecycle",
		"scope": "Selected control:<host> node; child MCP definitions belong to that node",
		"values": func() any {
			if canNodeRead {
				values := map[string]any{"node_transport": "enrolled ShellMCP control channel", "config_source": "ShellMCP installation configuration and environment", "child_mcp_descriptors": descriptors}
				if canHubSettingsRead {
					values["shellmcp_runtime_settings"] = shellRuntime
				}
				return values
			}
			return nil
		}(),
		"schema":   map[string]any{"child_mcp_lifecycle": selectedToolDefinitions(controlTools("control:catalog"), "mcp_manage", "mcp_tools", "mcp_call"), "runtime_settings": schema},
		"writable": canNodeWrite, "access": map[string]any{"read": canNodeRead, "write": canNodeWrite}, "writer": catalogWriter(canNodeWrite, []string{"control:<host>.mcp_manage action=list|config|status|upsert|remove|enable|disable|restart"}),
		"read_api":         []string{"control:<host>.mcp_manage action=list|config|status", "control:<host>.mcp_tools"},
		"persistence":      "child MCP definitions are persisted by the ShellMCP node",
		"restart_required": "per child MCP operation; restart is an explicit action",
		"notes":            "To disable AgentHerder, use mcp_manage action=disable with its exact ref on control:<host>; do not use shell commands. Node daemon bind/auth/install settings remain installation-managed and are not exposed as invented setters.",
		"unavailable":      []string{"ShellMCP daemon configuration mutation has no control MCP writer"},
	}
	if !canNodeRead {
		delete(nodeSection, "values")
	}

	sections := []map[string]any{hubSection, nodeSection, webhooks, failoverSection, proxy}
	if section != "" {
		for _, item := range sections {
			if item["id"] == section {
				return map[string]any{"section": item, "catalog_version": 1}
			}
		}
		return map[string]any{"error": "unknown control catalog section", "section": section, "available_sections": []string{"hub-shellmcp", "node-shellmcp", "webhooks", "failover", "network-proxy"}}
	}
	return map[string]any{"catalog_version": 1, "summary": "Actual settings and control capabilities. Mutations use the named existing writers and their normal authorization/validation.", "sections": sections}
}

func catalogWriter(authorized bool, writer any) any {
	if !authorized {
		return "unavailable to this caller"
	}
	return writer
}

func allowedCatalogTools(r *http.Request, target string, names ...string) []string {
	allowed := make([]string, 0, len(names))
	for _, name := range names {
		if authorizeToolCall(r, target, name) == nil {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

func filterCatalogTools(r *http.Request, target string, tools []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if authorizeToolCall(r, target, firstString(tool, "name")) == nil {
			result = append(result, tool)
		}
	}
	return result
}

func selectedToolDefinitions(definitions []map[string]any, names ...string) []map[string]any {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	result := make([]map[string]any, 0, len(names))
	for _, definition := range definitions {
		if wanted[firstString(definition, "name")] {
			result = append(result, definition)
		}
	}
	return result
}

func failoverConfigSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"enabled": map[string]any{"type": "boolean"}, "primary_public_url": map[string]any{"type": "string"}, "primary_health_url": map[string]any{"type": "string"},
		"primary_reclaim_url": map[string]any{"type": "string"}, "primary_reclaim_accept_url": map[string]any{"type": "string"}, "state_url": map[string]any{"type": "string"},
		"reclaim_max_age_sec": map[string]any{"type": "integer"}, "check_interval_sec": map[string]any{"type": "integer"}, "public_confirm_timeout_sec": map[string]any{"type": "integer"}, "fail_count_base": map[string]any{"type": "integer"}, "promotion_cooldown_sec": map[string]any{"type": "integer"}, "deterministic_rank_backoff": map[string]any{"type": "boolean"}, "nodes": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}},
	}, "additionalProperties": false}
}

func redactControlValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if isSensitiveControlKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactControlValue(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = redactControlValue(v[i])
		}
		return out
	default:
		// Struct-backed configs are converted through JSON so the same key
		// redaction is applied to typed settings and nested nodes.
		encoded, err := json.Marshal(value)
		if err != nil {
			return "[unavailable]"
		}
		var decoded any
		if json.Unmarshal(encoded, &decoded) != nil {
			return "[unavailable]"
		}
		if m, ok := decoded.(map[string]any); ok {
			return redactControlValue(m)
		}
		return decoded
	}
}

func isSensitiveControlKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, fragment := range []string{"secret", "token", "password", "credential", "private_key", "bearer", "api_key"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
