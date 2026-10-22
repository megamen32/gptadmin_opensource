package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// accessToolRulesRequest attaches a profile snapshot to a request exactly the
// way JWT authentication does, so the policy functions see the same state as
// a real caller.
func accessToolRulesRequest(t *testing.T, profile AccessProfile) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	return requestWithAccessProfile(r, profile)
}

// accessToolRulesProfile is the base profile used by the rule tests: one
// file target, one mixed tool, one facade set.
func accessToolRulesProfile() AccessProfile {
	return AccessProfile{
		ID:             "rules",
		Version:        1,
		AccessMode:     accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: []string{"file:host", "hub"},
		AllowedTools:   []string{"file_editor", "system_inspect"},
		ToolRules: []AccessToolRule{
			{Target: "file:host", Tool: "file_editor", Actions: []string{"view"}},
		},
	}
}

// ---------------------------------------------------------------------------
// Action level
// ---------------------------------------------------------------------------

// A rule that names actions allows exactly those actions. A missing action, a
// wrong type and a wrong value are all refused: an action-restricted rule
// never falls back to "allow".
func TestAccessToolRuleActionsAreEnforcedAtCallTime(t *testing.T) {
	r := accessToolRulesRequest(t, accessToolRulesProfile())
	if err := authorizeToolAction(r, "file:host", "file_editor", map[string]any{"action": "view"}); err != nil {
		t.Fatalf("declared action view denied: %v", err)
	}
	refused := map[string]map[string]any{
		"unlisted action": {"action": "create"},
		"other write":     {"action": "delete"},
		"missing action":  {},
		"non-string":      {"action": 42},
		"empty string":    {"action": ""},
		"nil args":        nil,
	}
	for name, args := range refused {
		if err := authorizeToolAction(r, "file:host", "file_editor", args); err == nil {
			t.Errorf("%s: action-restricted rule allowed %v", name, args)
		}
	}
}

// A read_only rule on a mixed tool without an action list is accepted at write
// time (it names no write action) and still only admits the known read
// actions at call time.
func TestAccessToolRuleReadOnlyWithoutActionsAllowsOnlyKnownReads(t *testing.T) {
	profile := accessToolRulesProfile()
	profile.ToolRules = []AccessToolRule{{Target: "file:host", Tool: "file_editor", ReadOnly: true}}
	if _, err := normalizeAccessProfile(profile); err != nil {
		t.Fatalf("read_only rule without actions rejected at write time: %v", err)
	}
	r := accessToolRulesRequest(t, profile)
	if err := authorizeToolAction(r, "file:host", "file_editor", map[string]any{"action": "view"}); err != nil {
		t.Fatalf("read_only view denied: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"create":        {"action": "create"},
		"missing":       {},
		"non-string":    {"action": 42},
		"empty string":  {"action": ""},
		"unknown write": {"action": "str_replace"},
	} {
		if err := authorizeToolAction(r, "file:host", "file_editor", args); err == nil {
			t.Errorf("%s: read_only rule allowed %v", name, args)
		}
	}
}

// ---------------------------------------------------------------------------
// Write time validation
// ---------------------------------------------------------------------------

// A known builtin write can never be certified read-only.
func TestAccessToolRuleReadOnlyOnWriteOnlyToolIsRejected(t *testing.T) {
	for _, tool := range []string{"shell_exec", "mcp_call", "mcp_request", "mcp_connection_close", "fleetExec", "fleet_exec", "code_exec"} {
		_, err := normalizeAccessToolRules([]AccessToolRule{{Target: "shell:host", Tool: tool, ReadOnly: true}})
		if err == nil {
			t.Errorf("read_only rule accepted for write-only tool %q", tool)
		} else if !strings.Contains(err.Error(), "read_only") {
			t.Errorf("tool %q: error %v does not explain the read_only refusal", tool, err)
		}
	}
}

// The hub script containers forward arbitrary work and cannot be certified
// read-only either.
func TestAccessToolRuleReadOnlyOnScriptContainerIsRejected(t *testing.T) {
	for _, tool := range []string{codeModeTool, "ro"} {
		if _, err := normalizeAccessToolRules([]AccessToolRule{{Target: "hub", Tool: tool, ReadOnly: true}}); err == nil {
			t.Errorf("read_only rule accepted for script container %q", tool)
		}
	}
}

// A read_only rule may only name known read actions of a mixed tool.
func TestAccessToolRuleReadOnlyActionListMustOnlyNameReadActions(t *testing.T) {
	accepted := []AccessToolRule{
		{Target: "control:host", Tool: "mcp_manage", Actions: []string{"list", "status", "config"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_editor", Actions: []string{"view"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_checkpoint", Actions: []string{"list", "diff"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_backup", Actions: []string{"list"}, ReadOnly: true},
		{Target: "file:host", Tool: "system_inspect", Actions: []string{"read_file", "list_directory"}, ReadOnly: true},
	}
	for _, rule := range accepted {
		if _, err := normalizeAccessToolRules([]AccessToolRule{rule}); err != nil {
			t.Errorf("read_only rule for %q/%q rejected: %v", rule.Target, rule.Tool, err)
		}
	}
	refused := []AccessToolRule{
		{Target: "control:host", Tool: "mcp_manage", Actions: []string{"restart"}, ReadOnly: true},
		{Target: "control:host", Tool: "mcp_manage", Actions: []string{"list", "restart"}, ReadOnly: true},
		{Target: "control:host", Tool: "mcp_manage", Actions: []string{"upsert"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_editor", Actions: []string{"view", "create"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_checkpoint", Actions: []string{"restore"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_backup", Actions: []string{"backup"}, ReadOnly: true},
		{Target: "file:host", Tool: "file_editor", Actions: []string{"not_an_action"}, ReadOnly: true},
	}
	for _, rule := range refused {
		if _, err := normalizeAccessToolRules([]AccessToolRule{rule}); err == nil {
			t.Errorf("read_only rule for %q/%q with write action %v accepted", rule.Target, rule.Tool, rule.Actions)
		}
	}
}

// A write_only tool is never a known write for a rule that does not claim
// read_only: the classification must not fire on ordinary rules.
func TestAccessToolRuleWriteOnlyToolIsAllowedWithoutReadOnly(t *testing.T) {
	if _, err := normalizeAccessToolRules([]AccessToolRule{{Target: "shell:host", Tool: "shell_exec"}}); err != nil {
		t.Fatalf("ordinary rule for a write-only tool rejected: %v", err)
	}
	if ruleDeclaresKnownWrite(AccessToolRule{Target: "shell:host", Tool: "shell_exec"}) {
		t.Fatal("rule without read_only declared a known write")
	}
	if !ruleDeclaresKnownWrite(AccessToolRule{Target: "shell:host", Tool: "shell_exec", ReadOnly: true}) {
		t.Fatal("rule with read_only on a write-only tool did not declare a known write")
	}
}

// Malformed rules are refused before they can be stored.
func TestAccessToolRuleValidationRejectsMalformedRules(t *testing.T) {
	long := strings.Repeat("x", accessProfileMaxStringBytes+1)
	cases := []struct {
		name string
		rule AccessToolRule
	}{
		{"empty target", AccessToolRule{Target: "", Tool: "file_editor"}},
		{"whitespace target", AccessToolRule{Target: "   ", Tool: "file_editor"}},
		{"empty tool", AccessToolRule{Target: "file:host", Tool: ""}},
		{"whitespace tool", AccessToolRule{Target: "file:host", Tool: "\t"}},
		{"wildcard target", AccessToolRule{Target: "*", Tool: "file_editor", ReadOnly: true}},
		{"wildcard tool", AccessToolRule{Target: "file:host", Tool: "*", ReadOnly: true}},
		{"empty action", AccessToolRule{Target: "file:host", Tool: "file_editor", Actions: []string{""}}},
		{"whitespace action", AccessToolRule{Target: "file:host", Tool: "file_editor", Actions: []string{"  "}}},
		{"long target", AccessToolRule{Target: long, Tool: "file_editor"}},
		{"long tool", AccessToolRule{Target: "file:host", Tool: long}},
		{"long action", AccessToolRule{Target: "file:host", Tool: "file_editor", Actions: []string{long}}},
	}
	for _, tc := range cases {
		if _, err := normalizeAccessToolRules([]AccessToolRule{tc.rule}); err == nil {
			t.Errorf("%s: malformed rule accepted", tc.name)
		}
	}
	if _, err := normalizeAccessToolRules([]AccessToolRule{
		{Target: "file:host", Tool: "file_editor"},
		{Target: "file:host", Tool: "file_editor"},
	}); err == nil {
		t.Fatal("duplicate target/tool pair accepted")
	}
}

// ---------------------------------------------------------------------------
// Rules only narrow
// ---------------------------------------------------------------------------

// A rule never grants a target or a tool that allowed_targets/allowed_tools
// do not already allow.
func TestAccessToolRulesNeverWidenAllowedTargetsOrTools(t *testing.T) {
	profile := accessToolRulesProfile()
	profile.AllowedTargets = []string{"file:host"}
	profile.AllowedTools = []string{"file_editor"}
	profile.ToolRules = []AccessToolRule{
		{Target: "file:host", Tool: "file_editor", Actions: []string{"view"}},
		{Target: "file:other", Tool: "file_editor", Actions: []string{"view"}},
		{Target: "file:host", Tool: "system_inspect", Actions: []string{"read_file"}},
	}
	r := accessToolRulesRequest(t, profile)
	if err := authorizeToolAction(r, "file:host", "file_editor", map[string]any{"action": "view"}); err != nil {
		t.Fatalf("rule covered call denied: %v", err)
	}
	if err := authorizeToolAction(r, "file:other", "file_editor", map[string]any{"action": "view"}); err == nil {
		t.Fatal("rule widened allowed_targets")
	}
	if err := authorizeToolAction(r, "file:host", "system_inspect", map[string]any{"action": "read_file"}); err == nil {
		t.Fatal("rule widened allowed_tools")
	}
}

// ---------------------------------------------------------------------------
// Facades stay usable
// ---------------------------------------------------------------------------

// A profile with rules denies every business call that is not named, but the
// sanctioned facades keep their own facade permission.
func TestAccessProfileToolRulesDenyUnnamedBusinessCallsButKeepFacades(t *testing.T) {
	profile := accessToolRulesProfile()
	profile.AllowedTools = []string{
		"ro", "exec", "search", "schema", "discover", "job", "expand", "execute",
		"file_editor", "system_inspect", "hub_status", "access_profiles",
	}
	profile.ToolRules = []AccessToolRule{
		{Target: "file:host", Tool: "file_editor", Actions: []string{"view"}},
		{Target: "hub", Tool: "hub_status"},
	}
	r := accessToolRulesRequest(t, profile)

	// The sanctioned facades of the short public catalog stay reachable.
	for _, facade := range []string{"ro", "exec", "search", "schema", "discover", "job", "expand"} {
		if err := authorizeFacadeCall(r, facade, nil); err != nil {
			t.Errorf("facade %q denied for a profile with rules: %v", facade, err)
		}
	}
	// The direct execute facade is retired: a profile that names it still
	// cannot route a business call through it.
	if err := authorizeFacadeCall(r, "execute", nil); err == nil || !strings.Contains(err.Error(), "no longer public") {
		t.Fatalf("execute routing facade is still usable: %v", err)
	}
	// The business target/tool behind the exec facade still needs a rule.
	if err := authorizeToolAction(r, "file:host", "system_inspect", map[string]any{}); err == nil {
		t.Fatal("exec reached a business call without a rule")
	}
	// A rule covered business call is reachable through the exec facade.
	if err := authorizeToolAction(r, "file:host", "file_editor", map[string]any{"action": "view"}); err != nil {
		t.Fatalf("exec refused a rule covered business call: %v", err)
	}
	// A business call without a rule is denied on the direct path too.
	if err := authorizeToolAction(r, "file:host", "system_inspect", map[string]any{}); err == nil {
		t.Fatal("business call without a rule was allowed")
	}

	// The exec facade applies the same rule to the business call it forwards
	// to: an unnamed pair is refused, a named pair runs.
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl"})
	defer s.Close()
	denied, status := s.callCodeModeTool(r, map[string]any{"code": "return await mcp.call('hub','access_profiles',{action:'list'});"})
	if status == http.StatusOK || firstString(mapValue(denied["error"]), "code") != "forbidden" ||
		!strings.Contains(firstString(mapValue(denied["error"]), "message"), "no rule") {
		t.Fatalf("exec reached an unnamed business call: %d %v", status, denied)
	}
	allowed, status := s.callCodeModeTool(r, map[string]any{"code": "return await mcp.call('hub','hub_status',{});"})
	if status != http.StatusOK || firstString(allowed, "status") != "completed" {
		t.Fatalf("exec refused a rule covered business call: %d %v", status, allowed)
	}
}

// A profile without tool_rules keeps the previous behaviour: the profile
// target/tool lists are the whole authorization.
func TestAccessProfileWithoutToolRulesKeepsPreviousBehaviour(t *testing.T) {
	profile := AccessProfile{
		ID:             "plain",
		Version:        1,
		AccessMode:     accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: []string{"file:host"},
		AllowedTools:   []string{"file_editor"},
	}
	r := accessToolRulesRequest(t, profile)
	if err := authorizeToolAction(r, "file:host", "file_editor", map[string]any{"action": "create"}); err != nil {
		t.Fatalf("profile without rules changed the previous behaviour: %v", err)
	}
	if err := authorizeToolAction(r, "file:other", "file_editor", map[string]any{"action": "view"}); err == nil {
		t.Fatal("profile without rules allowed a target outside allowed_targets")
	}
	if err := authorizeToolAction(r, "file:host", "system_inspect", map[string]any{}); err == nil {
		t.Fatal("profile without rules allowed a tool outside allowed_tools")
	}
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

// A mixed tool stays visible in a listing and the action restriction is
// published with it, so the caller knows which actions it may use.
func TestToolsForRequestPublishesAllowedActions(t *testing.T) {
	profile := accessToolRulesProfile()
	profile.AllowedTargets = []string{"file:host"}
	profile.AllowedTools = []string{"file_editor", "system_inspect"}
	r := accessToolRulesRequest(t, profile)

	tools := toolsForRequest(r, "file:host", []map[string]any{
		{"name": "file_editor"},
		{"name": "system_inspect"},
	})
	if len(tools) != 1 || firstString(tools[0], "name") != "file_editor" {
		t.Fatalf("listing did not keep the mixed tool visible: %v", tools)
	}
	actions, _ := tools[0]["allowed_actions"].([]string)
	if len(actions) != 1 || actions[0] != "view" {
		t.Fatalf("listing did not publish allowed_actions: %v", tools[0])
	}
}

// ---------------------------------------------------------------------------
// Read-only ceiling
// ---------------------------------------------------------------------------

// An external MCP target is not read-only by default: it needs an explicit
// trusted rule, and even then a known write operation stays refused.
func TestReadOnlyExecExternalTargetNeedsTrustedRule(t *testing.T) {
	profile := accessToolRulesProfile()
	profile.AllowedTargets = []string{"remote:ext"}
	profile.AllowedTools = []string{"custom_action", "shell_exec"}
	r := requestWithReadOnlyExec(accessToolRulesRequest(t, profile))

	if readOnlyExecToolAllowed(r, "remote:ext", "custom_action", map[string]any{}) {
		t.Fatal("external target was trusted without a rule")
	}
	trusted := profile
	trusted.ToolRules = []AccessToolRule{{Target: "remote:ext", Tool: "custom_action", ReadOnly: true}}
	trustedRequest := requestWithReadOnlyExec(accessToolRulesRequest(t, trusted))
	if !readOnlyExecToolAllowed(trustedRequest, "remote:ext", "custom_action", map[string]any{}) {
		t.Fatal("explicit trusted read rule was not usable")
	}
	writeRule := profile
	writeRule.ToolRules = []AccessToolRule{{Target: "remote:ext", Tool: "shell_exec", ReadOnly: true}}
	writeRequest := requestWithReadOnlyExec(accessToolRulesRequest(t, writeRule))
	if readOnlyExecToolAllowed(writeRequest, "remote:ext", "shell_exec", map[string]any{}) {
		t.Fatal("rule naming a known write operation was trusted as read-only")
	}
}

// The read-only hub allowlist keeps the read facades and refuses the script
// containers and every write facade.
func TestReadOnlyHubAllowlistKeepsReadFacadesAndRefusesContainers(t *testing.T) {
	for _, tool := range []string{"settings_schema", "search", "schema", "discover", "job", "expand"} {
		if !readOnlyHubToolAllowed(tool) {
			t.Errorf("read facade %q is not allowed in a read-only script", tool)
		}
	}
	for _, tool := range []string{codeModeTool, "ro", "settings_set", "settings_rollback", "approve_pending_server", "stale_cleanup_run", "agent_policy_set", "handover_start"} {
		if readOnlyHubToolAllowed(tool) {
			t.Errorf("write facade %q is allowed in a read-only script", tool)
		}
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// A profile with rules persists them, deep-copies them and bumps the version
// on every accepted update. A refused update leaves the stored version alone.
func TestAccessProfileToolRulesPersistDeepCopyAndVersionBump(t *testing.T) {
	cfg := Config{
		CtlToken:          "ctl",
		ConfigDir:         t.TempDir(),
		OAuthClientSecret: "oauth-secret",
		PublicOrigin:      "https://hub.example",
		MCPResource:       "https://hub.example",
	}
	s := New(cfg)
	path := "/admin/api/access-profiles/rules"

	create := map[string]any{
		"id":              "rules",
		"name":            "Rules profile",
		"access_mode":     "full",
		"approval_mode":   "ask_before_write",
		"allowed_targets": []string{"file:host"},
		"allowed_tools":   []string{"file_editor"},
		"tool_rules": []any{map[string]any{
			"target": "file:host", "tool": "file_editor", "actions": []string{"view"}, "read_only": true,
		}},
	}
	created := accessProfileTestRequest(t, s, http.MethodPut, path, create, map[string]string{"If-Match": "*"})
	if created.Code != http.StatusOK || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var stored AccessProfile
	if err := json.Unmarshal(created.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.ToolRules) != 1 || stored.ToolRules[0].Target != "file:host" || stored.ToolRules[0].Tool != "file_editor" {
		t.Fatalf("tool rules were not persisted: %+v", stored)
	}
	if len(stored.ToolRules[0].Actions) != 1 || stored.ToolRules[0].Actions[0] != "view" || !stored.ToolRules[0].ReadOnly {
		t.Fatalf("tool rule detail was not persisted: %+v", stored.ToolRules[0])
	}
	if stored.Version != 1 {
		t.Fatalf("created version = %d, want 1", stored.Version)
	}

	// The state file holds the rules.
	statePath := s.accessProfilesStatePath()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(raw), `"tool_rules"`) || !strings.Contains(string(raw), `"read_only": true`) {
		t.Fatalf("state file does not carry the rules: %s", raw)
	}

	// A refused update must not change the stored version.
	invalid := map[string]any{
		"id":              "rules",
		"access_mode":     "full",
		"allowed_targets": []string{"file:host"},
		"allowed_tools":   []string{"file_editor"},
		"tool_rules": []any{map[string]any{
			"target": "shell:host", "tool": "shell_exec", "read_only": true,
		}},
	}
	rejected := accessProfileTestRequest(t, s, http.MethodPut, path, invalid, map[string]string{"If-Match": `"1"`})
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("invalid rule update status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	stale := accessProfileTestRequest(t, s, http.MethodPut, path, create, map[string]string{"If-Match": `"0"`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	afterFailures := accessProfileTestRequest(t, s, http.MethodGet, path, nil, nil)
	if afterFailures.Code != http.StatusOK || afterFailures.Header().Get("ETag") != `"1"` {
		t.Fatalf("failed update changed the stored version: status=%d etag=%q", afterFailures.Code, afterFailures.Header().Get("ETag"))
	}

	// An accepted update bumps the version and the ETag matches the stored one.
	update := map[string]any{
		"id":              "rules",
		"access_mode":     "full",
		"allowed_targets": []string{"file:host"},
		"allowed_tools":   []string{"file_editor"},
		"tool_rules": []any{map[string]any{
			"target": "file:host", "tool": "file_editor", "actions": []string{"view", "create"},
		}},
	}
	updated := accessProfileTestRequest(t, s, http.MethodPut, path, update, map[string]string{"If-Match": `"1"`})
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("update status=%d etag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}
	reloaded := accessProfileTestRequest(t, s, http.MethodGet, path, nil, nil)
	if reloaded.Code != http.StatusOK || reloaded.Header().Get("ETag") != `"2"` {
		t.Fatalf("stored version was not bumped: status=%d etag=%q", reloaded.Code, reloaded.Header().Get("ETag"))
	}
}

// The profile a caller receives is a copy: mutating it must not change the
// policy the next call sees.
func TestAccessProfileFromRequestReturnsIndependentCopy(t *testing.T) {
	profile := accessToolRulesProfile()
	r := accessToolRulesRequest(t, profile)
	attached, ok := AccessProfileFromRequest(r)
	if !ok {
		t.Fatal("profile not attached to the request")
	}
	if len(attached.ToolRules) != 1 || len(attached.ToolRules[0].Actions) != 1 {
		t.Fatalf("attached profile lost its rules: %+v", attached)
	}
	attached.ToolRules[0].Actions[0] = "delete"
	attached.ToolRules[0].Target = "file:other"
	again, _ := AccessProfileFromRequest(r)
	if again.ToolRules[0].Actions[0] != "view" || again.ToolRules[0].Target != "file:host" {
		t.Fatalf("caller mutated the request profile: %+v", again.ToolRules[0])
	}
	// The attached profile is what the policy sees.
	if err := authorizeToolAction(r, "file:host", "file_editor", map[string]any{"action": "view"}); err != nil {
		t.Fatalf("attached profile lost its rule: %v", err)
	}
}

// The stored state is validated on load: an invalid rule or a version below
// one is refused instead of being silently used.
func TestAccessProfilesStateRejectsInvalidStoredProfile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/access_profiles_state.json"
	write := func(state map[string]any) {
		t.Helper()
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(map[string]any{"profiles": map[string]any{
		"bad-version": map[string]any{"id": "bad-version", "access_mode": "full", "version": 0},
	}})
	if _, err := readAccessProfilesState(path); err == nil {
		t.Fatal("stored profile with version 0 was accepted")
	}

	write(map[string]any{"profiles": map[string]any{
		"bad-rule": map[string]any{
			"id": "bad-rule", "access_mode": "full", "version": 1,
			"tool_rules": []any{map[string]any{"target": "shell:host", "tool": "shell_exec", "read_only": true}},
		},
	}})
	if _, err := readAccessProfilesState(path); err == nil {
		t.Fatal("stored profile with an invalid rule was accepted")
	}

	write(map[string]any{"profiles": map[string]any{
		"good": map[string]any{
			"id": "good", "access_mode": "full", "version": 1,
			"tool_rules": []any{map[string]any{"target": "file:host", "tool": "file_editor", "actions": []string{"view"}, "read_only": true}},
		},
	}})
	profiles, err := readAccessProfilesState(path)
	if err != nil {
		t.Fatalf("valid stored profile refused: %v", err)
	}
	if len(profiles) != 1 || len(profiles["good"].ToolRules) != 1 {
		t.Fatalf("valid stored profile was not loaded: %+v", profiles)
	}
}
