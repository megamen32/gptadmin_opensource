package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	accessModeFull     = "full"
	accessModeReadonly = "readonly"
)

type authClaimsContextKey struct{}
type readOnlyExecContextKey struct{}

func requestWithReadOnlyExec(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), readOnlyExecContextKey{}, true))
}
func readOnlyExecRequest(r *http.Request) bool {
	return r != nil && r.Context().Value(readOnlyExecContextKey{}) == true
}

const (
	approvalModeUnrestricted      = "unrestricted"
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
	if profile, ok := AccessProfileFromRequest(r); ok && (profile.AccessMode == accessModeReadonly || profile.ApprovalMode == approvalModeReadOnly) {
		return accessModeReadonly
	}
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
	// ``gptadmin.write`` is the deployed OAuth compatibility spelling for the
	// execute capability. Keep accepting the canonical ``gptadmin.exec`` too;
	// read-only clients must not inherit either write-capable scope.
	if containsString(scopes, "gptadmin.exec") || containsString(scopes, "gptadmin.write") {
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

func requestScopes(r *http.Request) []string {
	if r == nil {
		return nil
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	return strings.Fields(firstString(claims, "scope"))
}

func hubGranularScopeAllows(r *http.Request, toolName string) bool {
	scopes := requestScopes(r)
	has := func(scope string) bool { return containsString(scopes, scope) }
	switch toolName {
	case "settings_schema", "settings_get", "settings_history":
		return has("gptadmin.settings.read") || has("gptadmin.settings.write")
	case "settings_set", "settings_rollback":
		return has("gptadmin.settings.write")
	case "stale_cleanup_preview", "agent_policy_get", "agent_tombstones":
		return has("gptadmin.registry.read") || has("gptadmin.registry.manage")
	case "stale_cleanup_run", "agent_policy_set", "approve_pending_server":
		return has("gptadmin.registry.manage")
	case "handover_status":
		return has("gptadmin.update")
	case "handover_start":
		return has("gptadmin.update")
	}
	return false
}

func profileAllowsTarget(r *http.Request, target string) bool {
	profile, bound := AccessProfileFromRequest(r)
	if !bound {
		return true
	}
	return containsString(profile.AllowedTargets, target) || containsString(profile.AllowedTargets, "*")
}

func profileAllowsTool(r *http.Request, toolName string) bool {
	profile, bound := AccessProfileFromRequest(r)
	if !bound {
		return true
	}
	return containsString(profile.AllowedTools, toolName) || containsString(profile.AllowedTools, "*")
}

func authorizeToolCall(r *http.Request, target, toolName string) error {
	if !profileAllowsTarget(r, target) || !profileAllowsTool(r, toolName) {
		return errors.New("access profile denies this target or tool")
	}
	if err := authorizeToolRulePresence(r, target, toolName); err != nil {
		return err
	}
	if target == "hub" && hubGranularScopeAllows(r, toolName) {
		return nil
	}
	if toolName == "search" || toolName == "control_catalog" {
		return nil
	}
	if requestAccessMode(r) != accessModeReadonly {
		return nil
	}
	if target == "hub" {
		switch toolName {
		case "expand", "listMcpServers", "list_mcp_servers", "listMcpAgents", "list_mcp_agents", "list_pending_servers", "pending", "hub_status", "status", "demo", "resource_receipt", "settings_schema", "settings_get", "settings_history", "stale_cleanup_preview", "agent_policy_get", "agent_tombstones", "handover_status", "diagnose", webhookRoutesListTool, webhookJobGetTool:
			return nil
		}
	}
	if target == "control:hub" && isReadOnlyTool("hub", toolName) {
		return nil
	}
	if target == "webhooks" && (toolName == webhookRoutesListTool || toolName == webhookJobGetTool) {
		return nil
	}
	if toolName == "resources/list" || toolName == "resources/read" {
		return nil
	}
	if strings.HasPrefix(target, "file:") && toolName == "system_inspect" {
		return nil
	}
	return errors.New("read-only client cannot call this tool")
}

// ---------------------------------------------------------------------------
// Fine-grained tool rules
// ---------------------------------------------------------------------------

// facadeToolNames are the hub routing facades. A profile that declares
// tool_rules keeps these facades under their own facade permission, so a
// fine-grained profile stays usable. Every business call that is not named by
// a rule is denied.
var facadeToolNames = map[string]bool{
	"ro": true, "exec": true, "search": true, "schema": true, "discover": true,
	"job": true, "expand": true,
	// execute is a routing facade: it forwards to one business target/tool
	// pair, and that pair is checked on its own.
	"execute": true, "call_mcp_tool": true, "callMcpTool": true,
}

// isFacadeTool reports whether target/tool is a routing facade that is not
// covered by the business tool rules.
func isFacadeTool(target, tool string) bool {
	if target != "hub" && target != "control:hub" {
		return false
	}
	return facadeToolNames[tool]
}

// profileToolRule returns the rule for one canonical target/tool pair. An
// exact pair wins over a wildcard pair.
func profileToolRule(r *http.Request, target, tool string) (AccessToolRule, bool) {
	p, bound := AccessProfileFromRequest(r)
	if !bound || len(p.ToolRules) == 0 {
		return AccessToolRule{}, false
	}
	for _, rule := range p.ToolRules {
		if rule.Target == target && rule.Tool == tool {
			return rule, true
		}
	}
	for _, rule := range p.ToolRules {
		if (rule.Target == target || rule.Target == "*") && (rule.Tool == tool || rule.Tool == "*") {
			return rule, true
		}
	}
	return AccessToolRule{}, false
}

// authorizeToolRulePresence enforces the "rules only narrow" contract: rules
// never grant a target or a tool that allowed_targets/allowed_tools do not
// already allow, and when a profile declares tool_rules every business call
// must be named by a rule.
func authorizeToolRulePresence(r *http.Request, target, tool string) error {
	p, bound := AccessProfileFromRequest(r)
	if !bound || len(p.ToolRules) == 0 {
		return nil
	}
	if isFacadeTool(target, tool) {
		return nil
	}
	if _, found := profileToolRule(r, target, tool); !found {
		return errors.New("access profile has no rule for this target/tool")
	}
	return nil
}

// authorizeToolRuleAction applies the action level of a tool rule. A missing
// action, a non-string action and an unmatched action are all refused: an
// action-restricted rule never falls back to "allow".
func authorizeToolRuleAction(r *http.Request, target, tool string, args map[string]any) error {
	p, bound := AccessProfileFromRequest(r)
	if !bound || len(p.ToolRules) == 0 {
		return nil
	}
	if isFacadeTool(target, tool) {
		return nil
	}
	rule, found := profileToolRule(r, target, tool)
	if !found {
		return errors.New("access profile has no rule for this target/tool")
	}
	if len(rule.Actions) > 0 {
		action, ok := args["action"].(string)
		if !ok || action == "" {
			return errors.New("access profile requires an explicit action for this tool")
		}
		if !containsString(rule.Actions, action) {
			return errors.New("access profile denies this action")
		}
	}
	if rule.ReadOnly && knownWriteOperation(target, tool, args) {
		return errors.New("read-only tool rule denies this action")
	}
	return nil
}

// isCodeModeContainer reports whether a facade only forwards to nested
// operations. A container has no side effect of its own, so it must not
// consume a write approval or the bounded write budget: every nested
// operation is checked on its own target, tool and action.
func isCodeModeContainer(target, tool string) bool {
	return target == "hub" && (tool == codeModeTool || tool == "ro")
}

// authorizeToolAction is the single args-aware authorization gate. Every
// execution path funnels through it, so a profile rule, a read-only ceiling
// and an access mode are enforced identically for the JS bridge, the direct
// execute facade, the per-agent MCP endpoint and the relay.
func authorizeToolAction(r *http.Request, target, tool string, args map[string]any) error {
	if target == "" || tool == "" {
		return errors.New("an explicit target and tool are required")
	}
	if err := authorizeToolCall(r, target, tool); err != nil {
		return err
	}
	if err := authorizeToolRuleAction(r, target, tool, args); err != nil {
		return err
	}
	if readOnlyExecRequest(r) && !readOnlyExecToolAllowed(r, target, tool, args) {
		return errors.New("read-only execution refuses this target/tool/action")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Read and write classification
// ---------------------------------------------------------------------------

// readActionTools maps a mixed tool to the actions that provably do not
// mutate state. The lists are verified against the shipped implementations:
//   file_editor      view reads; create/str_replace/batch_edit/delete write
//   file_checkpoint  list/diff read; create/restore/delete/cleanup/gc write
//   file_backup      list reads; backup/restore/cleanup write
//   mcp_manage       list/status/config read; upsert/remove/enable/disable/restart write
//   system_inspect   read_file/list_directory read only
var readActionTools = map[string]map[string]bool{
	"file_editor":     {"view": true},
	"file_checkpoint": {"list": true, "diff": true},
	"file_backup":     {"list": true},
	"mcp_manage":      {"list": true, "status": true, "config": true},
	"system_inspect":  {"read_file": true, "list_directory": true},
}

// writeOnlyTools never read: they run a command, forward a raw MCP call or
// change a registration.
var writeOnlyTools = map[string]bool{
	"shell_exec": true, "mcp_call": true, "mcp_request": true,
	"mcp_connection_close": true, "fleetExec": true, "fleet_exec": true,
	"code_exec": true,
}

// knownWriteOperation reports whether the operation is a known state change.
// It is independent of any trusted rule: a rule may narrow a known write
// operation down to one of its read actions, but it can never turn a known
// write into a read.
func knownWriteOperation(target, tool string, args map[string]any) bool {
	if writeOnlyTools[tool] {
		return true
	}
	if reads, ok := readActionTools[tool]; ok {
		action := firstString(args, "action")
		return action == "" || !reads[action]
	}
	if target == "hub" && (tool == codeModeTool || tool == "ro") {
		return true
	}
	return false
}

// ruleWriteRefusal explains why a rule may not certify an operation as
// read-only. It returns an empty string when the rule is acceptable.
//
// The check is deliberately rule-level: the classification tables are keyed by
// tool, so a rule can be judged before any call arguments exist.
//   - a tool from writeOnlyTools never reads;
//   - the hub script containers (exec/ro) forward arbitrary work;
//   - a mixed tool may only be certified read-only for its known read
//     actions, so naming any other action is refused.
//
// A mixed tool with read_only and no actions at all is accepted: such a rule
// names no write action, and the call-time gate already refuses a missing,
// unknown or write action for it (knownWriteOperation reports a missing
// action as a write, because an action that cannot be proven read is not a
// read). Refusing the rule here would forbid the only safe spelling of it, so
// the restriction stays where the arguments are known.
func ruleWriteRefusal(rule AccessToolRule) string {
	if !rule.ReadOnly {
		return ""
	}
	if writeOnlyTools[rule.Tool] {
		return "the tool is a known write-only operation"
	}
	if rule.Target == "hub" && (rule.Tool == codeModeTool || rule.Tool == "ro") {
		return "the tool forwards arbitrary work"
	}
	if reads, ok := readActionTools[rule.Tool]; ok {
		for _, action := range rule.Actions {
			if !reads[action] {
				return fmt.Sprintf("action %q is not a known read action of %q", action, rule.Tool)
			}
		}
	}
	return ""
}

// ruleDeclaresKnownWrite reports whether a rule certifies a known builtin
// write operation as read-only. It is the rule-level counterpart of
// knownWriteOperation and reads the same tables, so the tables stay the
// single source of truth.
func ruleDeclaresKnownWrite(rule AccessToolRule) bool {
	return ruleWriteRefusal(rule) != ""
}

// operationIsWrite reports whether the operation consumes write budget and
// requires write approval. An operation that cannot be proven read-only is a
// write: an external MCP tool is not read-only by default.
func operationIsWrite(target, tool string, args map[string]any) bool {
	if knownWriteOperation(target, tool, args) {
		return true
	}
	if _, ok := readActionTools[tool]; ok {
		return false
	}
	if isReadOnlyTool(target, tool) {
		return false
	}
	switch {
	case target == "hub", target == "control:hub":
		return true
	case strings.HasPrefix(target, "control:"):
		return tool != "control_catalog" && tool != "mcp_tools"
	case strings.HasPrefix(target, "shell:"):
		return tool != "system_inspect"
	}
	// An external tool is not provably read-only.
	return true
}

// profileRuleAllowsRead reports whether the profile itself proves the
// operation is a read: a trusted read-only rule that names the exact
// target/tool pair and, for a mixed tool, a listed read action.
func profileRuleAllowsRead(r *http.Request, target, tool string, args map[string]any) bool {
	rule, ok := profileToolRule(r, target, tool)
	if !ok || !rule.ReadOnly {
		return false
	}
	if len(rule.Actions) > 0 {
		action, valid := args["action"].(string)
		if !valid || !containsString(rule.Actions, action) {
			return false
		}
	}
	return !knownWriteOperation(target, tool, args)
}

// ---------------------------------------------------------------------------
// Read-only execution ceiling
// ---------------------------------------------------------------------------

// readOnlyHubToolAllowed lists the hub facades a read-only script may use.
// The hub read set is the existing read-only facade list, minus the two
// script containers: exec and ro are refused inside a read-only script so one
// script cannot multiply its own budget or nest a second executor.
func readOnlyHubToolAllowed(tool string) bool {
	if tool == codeModeTool || tool == "ro" {
		return false
	}
	return isReadOnlyTool("hub", tool)
}

// readOnlyFileToolAllowed lists the file tools a read-only script may use.
func readOnlyFileToolAllowed(tool string, args map[string]any) bool {
	switch tool {
	case "file_editor":
		return firstString(args, "action") == "view"
	case "file_checkpoint":
		action := firstString(args, "action")
		return action == "list" || action == "diff"
	case "file_backup":
		return firstString(args, "action") == "list"
	case "system_inspect":
		return true
	}
	return false
}

// readOnlyControlToolAllowed lists the control tools a read-only script may
// use. mcp_manage keeps only the actions that really read the registration:
// list, status and config.
func readOnlyControlToolAllowed(tool string, args map[string]any) bool {
	switch tool {
	case "control_catalog", "mcp_tools":
		return true
	case "mcp_manage":
		action := firstString(args, "action")
		return action == "list" || action == "status" || action == "config"
	}
	return false
}

// readOnlyExecToolAllowed is the read-only execution ceiling. It is an
// explicit allowlist evaluated per action: a target/tool/action that is not
// provably read-only is refused before the operation reaches the executor.
func readOnlyExecToolAllowed(r *http.Request, target, tool string, args map[string]any) bool {
	switch {
	case target == "hub":
		return readOnlyHubToolAllowed(tool)
	case target == "control:hub":
		return tool == "control_catalog" || readOnlyHubToolAllowed(tool)
	case strings.HasPrefix(target, "file:"):
		return readOnlyFileToolAllowed(tool, args)
	case strings.HasPrefix(target, "control:"):
		return readOnlyControlToolAllowed(tool, args)
	case strings.HasPrefix(target, "shell:"):
		// A shell host exposes shell_exec (write) and system_inspect (read).
		return tool == "system_inspect"
	}
	// Any other target is an external MCP server. External servers are not
	// read-only by default: an administrator must name the exact target/tool
	// pair in a trusted rule before a read-only script may use it.
	rule, found := profileToolRule(r, target, tool)
	if !found || !rule.ReadOnly {
		return false
	}
	if len(rule.Actions) > 0 {
		action, ok := args["action"].(string)
		if !ok || !containsString(rule.Actions, action) {
			return false
		}
	}
	return !knownWriteOperation(target, tool, args)
}

func authorizeFacadeCall(r *http.Request, name string, args map[string]any) error {
	// These public names are retired. Let stale clients receive the explicit
	// migration response instead of a misleading profile-denied response.
	if name == "code_exec" {
		return nil
	}
	if name == "execute" || name == "call_mcp_tool" || name == "callMcpTool" {
		return authorizeExecuteFacade(r, args)
	}
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
		case "fleetExec", "fleet_exec":
			// Child target/tool authorization is applied independently by fleetExecForRequest.
			return nil
		default:
			return nil
		}
	}
	switch name {
	case "ui", "render_gptadmin_dashboard", "renderGptadminDashboard", "resource_receipt", "expand", "discover", "search", "demo", "list_mcp_servers", "listMcpServers", "list_mcp_agents", "listMcpAgents", "pending", "list_pending_servers", "job", "get_mcp_job", "getMcpJob", webhookRoutesListTool, webhookJobGetTool:
		return nil
	case "schema", "list_mcp_tools", "listMcpTools", "inspect", "inspect_system", "inspectSystem":
		target := firstString(args, "target", "server_id", "agent_id")
		if target != "" && !profileAllowsTarget(r, target) {
			return errors.New("access profile denies this target")
		}
		return nil
	case "exec":
		return authorizeToolCall(r, "hub", codeModeTool)
	case "ro":
		if !profileAllowsTool(r, "ro") {
			return errors.New("access profile denies this facade")
		}
		return nil
	default:
		return errors.New("read-only client cannot call this tool")
	}
}

// authorizeExecuteFacade checks the direct execute facade. A code-only call is
// the old transport spelling of exec; a call with routing arguments is a
// business call and is checked against that exact target/tool pair with its
// own arguments. A mixed call is refused before anything runs, so a caller
// can never be unsure which path executed.
func authorizeExecuteFacade(r *http.Request, args map[string]any) error {
	allowed := false
	for _, alias := range []string{"execute", "call_mcp_tool", "callMcpTool"} {
		if profileAllowsTool(r, alias) {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("access profile denies this facade")
	}
	code := firstString(args, "code")
	target := firstString(args, "target", "server_id", "agent_id")
	tool := firstString(args, "tool", "tool_name", "name")
	if code != "" {
		if target != "" || tool != "" {
			return errors.New("execute accepts either code or target/tool, not both")
		}
		return authorizeToolAction(r, "hub", codeModeTool, nil)
	}
	if args == nil {
		// A catalog listing has no arguments to check yet.
		return nil
	}
	if target == "" || tool == "" {
		return errors.New("execute requires an explicit target and tool")
	}
	callArgs := mapValue(args["arguments"])
	if len(callArgs) == 0 {
		callArgs = mapValue(args["args"])
	}
	if len(callArgs) == 0 && firstString(args, "args_json") != "" {
		if parsed, err := toolArgsFromJSON(firstString(args, "args_json")); err == nil {
			callArgs = parsed
		}
	}
	return authorizeToolAction(r, target, tool, callArgs)
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
		name := firstString(tool, "name")
		if authorizeToolCall(r, target, name) != nil {
			continue
		}
		// A mixed tool stays visible even when the listing has no action yet:
		// the restriction is published with the tool and enforced strictly at
		// call time.
		if rule, ok := profileToolRule(r, target, name); ok && len(rule.Actions) > 0 {
			entry := cloneMap(tool)
			entry["allowed_actions"] = append([]string(nil), rule.Actions...)
			filtered = append(filtered, entry)
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func mcpClientHTTPPathAllowed(path string) bool {
	return strings.HasPrefix(path, "/mcp-relay/") ||
		path == "/webhook-routes" || strings.HasPrefix(path, "/webhook-routes/") ||
		strings.HasPrefix(path, "/admin/api/webhook-jobs/")
}
