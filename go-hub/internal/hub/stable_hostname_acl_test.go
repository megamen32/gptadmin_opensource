package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStableHostnameAliasPreservesConsumerAccess(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "ctl"})
	defer s.Close()
	const oldID = "shell:mac-before"
	const newID = "shell:mac-after"
	identity := map[string]any{"server_id": "persistent-node-id", "fingerprint": "key-A", "hostname": "mac-before", "approved": true}
	s.agents[oldID] = &Agent{AgentID: oldID, Kind: "shell", Status: "online", Meta: identity}
	alias := s.shortTarget(oldID)
	for id, target := range map[string]string{"hostname-profile": oldID, "other-profile": "shell:other"} {
		// Existing installations contain this legacy name-only persisted shape.
		_, err := s.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
			profiles[id] = AccessProfile{ID: id, Version: 1, AccessMode: "full", AllowedTargets: []string{target}, AllowedTools: []string{"schema", "shell_exec", "mcp_manage"}}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// Upgrade uses the ordinary CAS save while the original identity is known.
		response := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/access-profiles/"+id, map[string]any{"name": id, "access_mode": "full", "allowed_targets": []string{target}, "allowed_tools": []string{"schema", "shell_exec", "mcp_manage"}, "tool_rules": []any{map[string]any{"target": target, "tool": "mcp_manage", "actions": []string{"list"}, "read_only": true}, map[string]any{"target": target, "tool": "shell_exec"}}}, map[string]string{"If-Match": `"1"`})
		if response.Code != http.StatusOK {
			t.Fatalf("save profile: %d %s", response.Code, response.Body.String())
		}
	}
	read := func(rqLabel string, expectAllowed bool) {
		t.Helper()
		profileID := "hostname-profile"
		if !expectAllowed {
			profileID = "other-profile"
		}
		r := s.applyAccessProfileContext(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"profile_id": profileID})
		args := map[string]any{"target": alias, "tool": "shell_exec"}
		err := s.authorizePublicFacade(r, "schema", args)
		if !expectAllowed {
			if err == nil {
				t.Fatalf("%s: denied profile gained access", rqLabel)
			}
			return
		}
		if err != nil {
			t.Errorf("%s: stable target lost access: %v", rqLabel, err)
			return
		}
		out := mapValue(s.appsSDKCallForRequest(r, "schema", args))
		if firstString(out, "status") != "completed" {
			t.Fatalf("%s: schema consumer failed: %v", rqLabel, out)
		}
	}
	read("before rename", true)
	read("before rename denied", false)
	delete(s.agents, oldID)
	renamed := cloneMap(identity)
	renamed["hostname"] = "mac-after"
	s.agents[newID] = &Agent{AgentID: newID, Kind: "shell", Status: "online", Meta: renamed}
	if got := s.shortTarget(newID); got != alias {
		t.Fatalf("stable target changed: %s -> %s", alias, got)
	}
	read("after rename", true)
	read("after rename denied", false)
	// A new device taking the old name must not inherit the original grant.
	s.agents[oldID] = &Agent{AgentID: oldID, Kind: "shell", Status: "online", Meta: map[string]any{"server_id": "replacement-id", "fingerprint": "key-B", "approved": true}}
	r := s.applyAccessProfileContext(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"profile_id": "hostname-profile"})
	if profileAllowsTarget(r, oldID) {
		t.Fatal("replacement device inherited old name grant")
	}
	// Reload persisted profiles rather than relying on the request's memory.
	if err := s.saveRegistryStateLocked(); err != nil {
		t.Fatal(err)
	}
	cfg := s.cfg
	_ = s.Close()
	s = New(cfg)
	defer s.Close()
	read("after restart", true)
	r = s.applyAccessProfileContext(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"profile_id": "hostname-profile"})
	if err := authorizeToolAction(r, newID, "mcp_manage", map[string]any{"action": "list"}); err != nil {
		t.Fatalf("allowed action lost: %v", err)
	}
	if err := authorizeToolAction(r, newID, "mcp_manage", map[string]any{"action": "remove"}); err == nil {
		t.Fatal("rename discarded action restriction")
	}
	s.agents[newID].Meta["fingerprint"] = "replacement-key"
	r = s.applyAccessProfileContext(httptest.NewRequest("POST", "/mcp", nil), map[string]any{"profile_id": "hostname-profile"})
	if profileAllowsTarget(r, newID) {
		t.Fatal("replacement key inherited identity grant")
	}
}
