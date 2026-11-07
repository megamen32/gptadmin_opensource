package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPinnedToolRuleDoesNotFollowReplacementNameWithWildcardTarget(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	const target = "shell:old-host"
	s.agents[target] = &Agent{AgentID: target, Kind: "shell", Status: "online", Meta: map[string]any{
		"server_id": "original", "fingerprint": "key-original", "approved": true,
	}}
	profile := AccessProfile{
		ID: "wildcard-pinned-rule", AccessMode: "full",
		AllowedTargets: []string{"*"}, AllowedTools: []string{"mcp_manage"},
		ToolRules: []AccessToolRule{{Target: target, Tool: "mcp_manage", Actions: []string{"list"}, ReadOnly: true}},
	}
	profile = s.pinAccessProfileTargets(profile, AccessProfile{})
	if profile.TargetIdentities[target] == "" {
		t.Fatal("fixture did not pin the original target identity")
	}
	request := func() *http.Request {
		return requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), s.resolveAccessProfileTargets(profile))
	}
	args := map[string]any{"action": "list"}
	if err := authorizeToolAction(request(), target, "mcp_manage", args); err != nil {
		t.Fatalf("original identity must be allowed: %v", err)
	}
	s.agents[target] = &Agent{AgentID: target, Kind: "shell", Status: "online", Meta: map[string]any{
		"server_id": "replacement", "fingerprint": "key-replacement", "approved": true,
	}}
	if err := authorizeToolAction(request(), target, "mcp_manage", args); err == nil {
		t.Fatal("replacement identity inherited the pinned tool rule through the old name")
	}
}
