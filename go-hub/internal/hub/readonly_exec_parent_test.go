package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func parentReadRequest() *http.Request {
	r := httptest.NewRequest("POST", "/mcp", nil)
	return requestWithAuthClaims(r, map[string]any{"sub": "parent-tests", "client_id": "parent-tests", "scope": "gptadmin.read gptadmin.exec", "access_mode": "full"})
}

func TestReadOnlyParentCeilingIndependentOfProfile(t *testing.T) {
	r := requestWithReadOnlyExec(parentReadRequest())
	for _, tc := range []struct {
		target, tool string
		args         map[string]any
	}{
		{"file:fixture", "file_editor", map[string]any{"action": "create"}},
		{"control:fixture", "mcp_manage", map[string]any{"action": "restart"}},
		{"shell:fixture", "shell_exec", map[string]any{"cmd": "printf harmless"}},
	} {
		if err := authorizeToolAction(r, tc.target, tc.tool, tc.args); err == nil {
			t.Errorf("ro allowed unprofiled mutation %s %s", tc.target, tc.tool)
		}
	}
}

func TestReadOnlyParentRuleRequiresExplicitReadCertification(t *testing.T) {
	p := AccessProfile{ID: "parent", Version: 1, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub", "remote:fixture"}, AllowedTools: []string{"ro", "custom_action"}, ToolRules: []AccessToolRule{{Target: "remote:fixture", Tool: "custom_action", ReadOnly: false}}}
	r := requestWithReadOnlyExec(requestWithAccessProfile(parentReadRequest(), p))
	if readOnlyExecToolAllowed(r, "remote:fixture", "custom_action", map[string]any{}) {
		t.Fatal("write-capable rule became read-only")
	}
	p.ToolRules[0].ReadOnly = true
	r = requestWithReadOnlyExec(requestWithAccessProfile(parentReadRequest(), p))
	if !readOnlyExecToolAllowed(r, "remote:fixture", "custom_action", map[string]any{}) {
		t.Fatal("explicit read rule is not usable")
	}
}

func TestReadOnlyParentFileViewAndRuleActions(t *testing.T) {
	p := AccessProfile{ID: "parent", Version: 1, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub", "file:fixture"}, AllowedTools: []string{"ro", "file_editor"}, ToolRules: []AccessToolRule{{Target: "file:fixture", Tool: "file_editor", Actions: []string{"view"}, ReadOnly: true}}}
	r := requestWithReadOnlyExec(requestWithAccessProfile(parentReadRequest(), p))
	if err := authorizeToolCall(r, "file:fixture", "file_editor"); err != nil {
		t.Fatalf("coarse eligibility hides allowed view: %v", err)
	}
	if err := authorizeToolAction(r, "file:fixture", "file_editor", map[string]any{"action": "view"}); err != nil {
		t.Fatalf("view denied: %v", err)
	}
	for _, args := range []map[string]any{{}, {"action": 42}, {"action": "delete"}} {
		if authorizeToolAction(r, "file:fixture", "file_editor", args) == nil {
			t.Errorf("invalid action accepted: %v", args)
		}
	}
}

func TestReadOnlyParentNoImplicitFacadeGrant(t *testing.T) {
	p := AccessProfile{ID: "no-ro", Version: 1, AccessMode: accessModeReadonly, ApprovalMode: approvalModeReadOnly, AllowedTargets: []string{"hub"}, AllowedTools: []string{"search"}}
	r := requestWithAccessProfile(parentReadRequest(), p)
	if authorizeFacadeCall(r, "ro", nil) == nil {
		t.Fatal("readonly profile gained unassigned ro facade")
	}
}

func TestReadOnlyParentUnknownRemoteResourcesNotImplicitlyTrusted(t *testing.T) {
	r := requestWithReadOnlyExec(parentReadRequest())
	if readOnlyExecToolAllowed(r, "remote:fixture", "resources/read", map[string]any{"uri": "fixture://data"}) {
		t.Fatal("unknown external resource method auto-trusted")
	}
}
