package hub

import (
	"net/http"
	"testing"
)

func selfIdentityProbeProfile() AccessProfile {
	return AccessProfile{ID: "self-probe", Version: 1, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub"}, AllowedTools: []string{"access_clients"}}
}
func TestROSelfIdentityAdmission(t *testing.T) {
	p := selfIdentityProbeProfile()
	r := requestWithReadOnlyExec(accessToolRulesRequest(t, p))
	if err := authorizeToolAction(r, "hub", "access_clients", map[string]any{"action": "whoami"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"list", "token", "issue", "bind", "unbind", "revoke", "set_role", "", "WHOAMI", " whoami "} {
		t.Run("deny_"+action, func(t *testing.T) {
			if err := authorizeToolAction(r, "hub", "access_clients", map[string]any{"action": action}); err == nil {
				t.Fatal("non-self operation admitted")
			}
		})
	}
	for name, args := range map[string]map[string]any{"missing": nil, "wrong_type": {"action": true}, "other_client": {"action": "whoami", "id": "other"}, "other_profile": {"action": "whoami", "profile_id": "other"}} {
		t.Run(name, func(t *testing.T) {
			if err := authorizeToolAction(r, "hub", "access_clients", args); err == nil {
				t.Fatal("ambiguous/targeted request admitted")
			}
		})
	}
	t.Run("no_other_target", func(t *testing.T) {
		if err := authorizeToolAction(r, "control:hub", "access_clients", map[string]any{"action": "whoami"}); err == nil {
			t.Fatal("other target admitted")
		}
	})
	t.Run("no_tool_grant", func(t *testing.T) {
		q := p
		q.AllowedTools = []string{"search"}
		if err := authorizeToolAction(requestWithReadOnlyExec(accessToolRulesRequest(t, q)), "hub", "access_clients", map[string]any{"action": "whoami"}); err == nil {
			t.Fatal("tool ACL bypassed")
		}
	})
	t.Run("no_target_grant", func(t *testing.T) {
		q := p
		q.AllowedTargets = []string{"file:elsewhere"}
		if err := authorizeToolAction(requestWithReadOnlyExec(accessToolRulesRequest(t, q)), "hub", "access_clients", map[string]any{"action": "whoami"}); err == nil {
			t.Fatal("target ACL bypassed")
		}
	})
}
func TestROSelfIdentityProfileMetadata(t *testing.T) {
	s, token, original := configuredDurableFixture(t)
	base, _, ok := configuredBearerRequest(s, token)
	if !ok {
		t.Fatal("fixture login failed")
	}
	for _, id := range []string{"own-profile-A", "own-profile-B", ""} {
		t.Run("own_"+id, func(t *testing.T) {
			r := base
			if id != "" {
				p := selfIdentityProbeProfile()
				p.ID = id
				p.Name = "must-not-disclose"
				p.AllowedTargets = []string{"hub", "secret-target"}
				r = requestWithAccessProfile(r, p)
			}
			got, status := s.callAccessClientTool(r, "access_clients", map[string]any{"action": "whoami"})
			if status != 200 || got["client_id"] != "zcode" || got["token_id"] != original.ID || got["profile_id"] != id {
				t.Fatalf("wrong own identity: %#v", got)
			}
			if len(got) != 5 {
				t.Fatalf("unexpected metadata keys: %#v", got)
			}
			for _, key := range []string{"profile", "allowed_targets", "allowed_tools", "tool_rules", "read_roots", "write_roots", "token", "bearer", "name"} {
				if _, found := got[key]; found {
					t.Fatalf("unapproved metadata %s", key)
				}
			}
		})
	}
	if _, status := s.callAccessClientTool(nil, "access_clients", map[string]any{"action": "whoami"}); status != 401 {
		t.Fatal("nil request accepted")
	}
}

func TestROSelfIdentitySandbox(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	good, status := s.callCodeModeToolMode(r, map[string]any{"code": "return await mcp.call('hub','access_clients',{action:'whoami'});"}, true)
	if status != http.StatusOK || firstString(good, "status") != "completed" {
		t.Fatalf("self identity read failed: %#v", good)
	}
	got := mapValue(good["result"])
	if got["client_id"] != "code-mode-client" || got["profile_id"] != "" {
		t.Fatalf("unexpected self identity: %#v", got)
	}
	for _, code := range []string{
		"return await mcp.call('hub','access_clients',{action:'list'});",
		"return await mcp.call('hub','access_clients',{action:'token',id:'other'});",
		"return await mcp.call('hub','access_clients',{action:'whoami',id:'other'});",
		"try { await mcp.call('hub','access_clients',{action:'bind',id:'other',profile_id:'other'}); } catch(e) {} return await mcp.call('hub','access_clients',{action:'whoami'});",
	} {
		bad, st := s.callCodeModeToolMode(r, map[string]any{"code": code}, true)
		if st == http.StatusOK || firstString(bad, "status") == "completed" {
			t.Fatalf("forbidden action or sticky failure escaped: %#v", bad)
		}
	}
}
