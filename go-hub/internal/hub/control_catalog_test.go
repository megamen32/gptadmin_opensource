package hub

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControlCatalogDescribesActualWritersAndCurrentValues(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:9001", ConfigDir: t.TempDir()})
	t.Cleanup(func() { s.Close() })
	s.mu.Lock()
	s.settings.Values["fleet_auto_update_enabled"] = false
	s.settings.Values["shell_queue_retry_seconds"] = 17
	s.failover = FailoverConfig{Enabled: true, CheckIntervalSec: 9, Nodes: []FailoverNode{{ServerID: "node-44", Enabled: true}}}
	s.webhookRoutes["route-a"] = WebhookRoute{ID: "route-a", Token: "DO_NOT_LEAK_TOKEN", Action: WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"}}
	s.agents["shell:catalog-host"] = &Agent{AgentID: "shell:catalog-host", Meta: map[string]any{"mcp_agents": []any{map[string]any{"ref": "agentherder", "enabled": true}}}}
	s.mu.Unlock()

	catalog := s.controlCatalog(httptest.NewRequest("POST", "/mcp", nil), "control:catalog-host", "")
	sections, ok := catalog["sections"].([]map[string]any)
	if !ok || len(sections) < 5 {
		t.Fatalf("catalog sections missing: %#v", catalog)
	}
	byID := map[string]map[string]any{}
	for _, item := range sections {
		byID[firstString(item, "id")] = item
	}
	for _, id := range []string{"hub-shellmcp", "node-shellmcp", "webhooks", "failover", "network-proxy"} {
		if byID[id] == nil {
			t.Errorf("missing section %q", id)
		}
	}
	hub := byID["hub-shellmcp"]
	values := mapValue(hub["values"])
	hubValues := mapValue(values["hub"])
	if hubValues["fleet_auto_update_enabled"] != false {
		t.Fatalf("current AgentHerder/ShellMCP policy missing: %#v", hubValues)
	}
	if firstString(hub, "writer") == "" || !strings.Contains(firstString(hub, "writer"), "settings_set") {
		t.Fatalf("real Hub settings writer not linked: %#v", hub)
	}
	if !strings.Contains(string(mustJSON(t, byID["node-shellmcp"]["writer"])), "mcp_manage") || !strings.Contains(firstString(byID["node-shellmcp"], "notes"), "AgentHerder") {
		t.Fatalf("child lifecycle not discoverable: %#v", byID["node-shellmcp"])
	}
	if !strings.Contains(firstString(byID["failover"], "writer"), "/admin/api/failover") {
		t.Fatalf("failover writer inaccurately represented: %#v", byID["failover"])
	}
	webhookJSON := string(mustJSON(t, byID["webhooks"]))
	if strings.Contains(webhookJSON, "DO_NOT_LEAK_TOKEN") {
		t.Fatalf("webhook secret escaped catalog: %s", webhookJSON)
	}
	if !strings.Contains(webhookJSON, "webhook_route_create") {
		t.Fatalf("webhook MCP writer missing: %s", webhookJSON)
	}
	if !strings.Contains(firstString(byID["network-proxy"], "notes"), "does not reconfigure") {
		t.Fatalf("proxy configuration boundary missing: %#v", byID["network-proxy"])
	}
}

func TestControlCatalogSectionAndToolAreReadOnlyAndRedactSecrets(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	t.Cleanup(func() { s.Close() })
	tool := controlCatalogToolDefinition()
	if firstString(tool, "name") != "control_catalog" || mapValue(tool["annotations"])["readOnlyHint"] != true {
		t.Fatalf("bad tool definition: %#v", tool)
	}
	got := s.controlCatalog(httptest.NewRequest("POST", "/mcp", nil), "control:catalog-host", "failover")
	if firstString(mapValue(got["section"]), "id") != "failover" {
		t.Fatalf("section selection failed: %#v", got)
	}
	bad := s.controlCatalog(httptest.NewRequest("POST", "/mcp", nil), "control:catalog-host", "not-real")
	if firstString(bad, "error") == "" {
		t.Fatalf("unknown section accepted: %#v", bad)
	}
	redacted := redactControlValue(map[string]any{"token": "secret-value", "child": []any{map[string]any{"private_key": "private-value", "count": 2}}})
	encoded := string(mustJSON(t, redacted))
	for _, secret := range []string{"secret-value", "private-value"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("secret %q escaped redaction: %s", secret, encoded)
		}
	}
	if !strings.Contains(encoded, "[REDACTED]") || !strings.Contains(encoded, `"count":2`) {
		t.Fatalf("redaction lost useful data: %s", encoded)
	}
}

func TestControlCatalogRestrictedProfileDoesNotExposeHubOrOtherOwnerValues(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	t.Cleanup(func() { s.Close() })
	s.mu.Lock()
	s.webhookRoutes["private-route"] = WebhookRoute{ID: "private-route", Token: "PRIVATE_ROUTE_TOKEN"}
	s.failover = FailoverConfig{Enabled: true, Nodes: []FailoverNode{{ServerID: "global-node", Enabled: true}}}
	s.agents["shell:node-a"] = &Agent{AgentID: "shell:node-a", Meta: map[string]any{"mcp_agents": []any{
		map[string]any{"ref": "owned", "enabled": true},
		map[string]any{"ref": "another-owner", "owner_profile_id": "profile-b", "enabled": true},
	}}}
	s.mu.Unlock()
	profile := AccessProfile{ID: "profile-a", Version: 2, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted,
		AllowedTargets: []string{"control:node-a"}, AllowedTools: []string{"control_catalog", "mcp_tools"}}
	r := requestWithAccessProfile(httptest.NewRequest("POST", "/mcp", nil), profile)
	catalog := s.controlCatalog(r, "control:node-a", "")
	sections, ok := catalog["sections"].([]map[string]any)
	if !ok {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	byID := map[string]map[string]any{}
	for _, section := range sections {
		byID[firstString(section, "id")] = section
	}
	for _, id := range []string{"hub-shellmcp", "webhooks", "failover", "network-proxy"} {
		section := byID[id]
		if section == nil {
			t.Fatalf("missing metadata section %q", id)
		}
		if _, exists := section["values"]; exists {
			t.Errorf("restricted caller saw %s current values: %#v", id, section["values"])
		}
		access := mapValue(section["access"])
		if access["read"] != false {
			t.Errorf("restricted caller marked %s readable: %#v", id, access)
		}
	}
	for _, id := range []string{"hub-shellmcp", "webhooks", "failover"} {
		if byID[id]["writable"] != false {
			t.Errorf("restricted caller marked %s writable: %#v", id, byID[id])
		}
	}
	nodeValues := mapValue(byID["node-shellmcp"]["values"])
	descriptors := sliceValue(nodeValues["child_mcp_descriptors"])
	if len(descriptors) != 1 || firstString(mapValue(descriptors[0]), "ref") != "owned" {
		t.Fatalf("other owner's child descriptor escaped catalog: %#v", descriptors)
	}
	encoded := string(mustJSON(t, catalog))
	for _, forbidden := range []string{"PRIVATE_ROUTE_TOKEN", "private-route", "global-node", "another-owner"} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("restricted catalog contains %q: %s", forbidden, encoded)
		}
	}
}
