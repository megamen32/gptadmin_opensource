package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractMCPManageAgentsFromStructuredResult(t *testing.T) {
	raw := map[string]any{
		"server_id": "control:node-a",
		"status":    "completed",
		"response": map[string]any{
			"structuredContent": map[string]any{
				"result": map[string]any{
					"structuredContent": map[string]any{
						"agents": []any{map[string]any{"ref": "filesystem", "enabled": true}},
					},
				},
			},
		},
	}
	rows, err := extractMCPManageAgents(raw)
	if err != nil || len(rows) != 1 || firstString(rows[0], "ref") != "filesystem" {
		t.Fatalf("extractMCPManageAgents() = %#v, %v", rows, err)
	}
}

func TestExtractMCPManageAgentsFromJSONTextAndConfirmedEmpty(t *testing.T) {
	textResult := map[string]any{"response": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": `{"agents":[{"ref":"remote","enabled":false}]}`},
	}}}
	rows, err := extractMCPManageAgents(textResult)
	if err != nil || len(rows) != 1 || firstString(rows[0], "ref") != "remote" {
		t.Fatalf("JSON text extract = %#v, %v", rows, err)
	}

	empty, err := extractMCPManageAgents(map[string]any{"structuredContent": map[string]any{"agents": []any{}}})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("confirmed empty list = %#v, %v", empty, err)
	}
}

func TestExtractMCPManageAgentsFailsClosedOnMissingOrMalformedList(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"missing":   {"status": "completed", "response": map[string]any{"content": []any{map[string]any{"type": "text", "text": "MCP supervisors"}}}},
		"malformed": {"structuredContent": map[string]any{"agents": "not-an-array"}},
		"error":     {"isError": true, "content": []any{map[string]any{"type": "text", "text": "node offline"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := extractMCPManageAgents(raw); err == nil {
				t.Fatalf("expected %s result to be unavailable", name)
			}
		})
	}
}

func TestNormalizeNodeMCPInventorySeparatesDesiredAndObservedStatus(t *testing.T) {
	rows, err := normalizeNodeMCPInventory("shell:node-a", []map[string]any{
		{"ref": "lazy", "name": "Lazy child", "enabled": true, "health": map[string]any{"protocol": map[string]any{"state": "unknown"}}},
		{"ref": "off", "enabled": false},
		{"ref": "bad", "enabled": true, "health": map[string]any{"process": map[string]any{"state": "exited"}}},
		{"ref": "running", "enabled": true, "health": map[string]any{"process": map[string]any{"state": "running"}}},
	})
	if err != nil || len(rows) != 4 {
		t.Fatalf("normalizeNodeMCPInventory() = %#v, %v", rows, err)
	}
	byRef := map[string]string{}
	for _, row := range rows {
		byRef[firstString(row, "ref")] = firstString(row, "status")
	}
	if byRef["lazy"] != "configured" || byRef["off"] != "disabled" || byRef["bad"] != "failed" || byRef["running"] != "online" {
		t.Fatalf("node status projection lost desired/observed distinction: %#v", byRef)
	}
}

func TestAdminHubMCPInventorySeparatesRegistryCategories(t *testing.T) {
	s := &Server{
		agents: map[string]*Agent{
			"shell:node-a": {
				AgentID: "shell:node-a", Name: "Node A", Kind: "virtual_shell", Status: "online",
				Meta: map[string]any{"mcp_agents": []any{
					map[string]any{"ref": "active", "name": "Active child", "enabled": true, "transport": "stdio"},
					map[string]any{"ref": "disabled", "name": "Disabled child", "enabled": false},
				}},
			},
			"file:node-a":    {AgentID: "file:node-a", Kind: "virtual_file", Meta: map[string]any{"backing_server_id": "shell:node-a"}},
			"control:node-a": {AgentID: "control:node-a", Kind: "virtual_control", Meta: map[string]any{"backing_server_id": "shell:node-a"}},
		},
		virtualMCP: map[string]bool{"network-proxy": true},
	}
	rows := s.adminHubMCPInventoryLocked(nil)
	seen := map[string]string{}
	for _, row := range rows {
		seen[firstString(row, "server_id")] = firstString(row, "registry_category")
	}
	if seen["hub"] != adminMCPInventoryHubCore || seen["network-proxy"] != adminMCPInventoryVirtual || seen["webhooks"] != adminMCPInventoryVirtual || seen["shell:node-a"] != adminMCPInventoryHost || seen["mcp:shell:node-a:active"] != adminMCPInventoryChild {
		t.Fatalf("missing or miscategorized Hub inventory rows: %#v", seen)
	}
	if _, exists := seen["control:node-a"]; exists {
		t.Fatalf("paired control target appeared as an installed server: %#v", seen)
	}
	if _, exists := seen["file:node-a"]; exists {
		t.Fatalf("paired file target appeared as an installed server: %#v", seen)
	}
	if len(rows) == 0 || strings.TrimSpace(firstString(rows[0], "registry_category")) == "" {
		t.Fatalf("inventory did not include category labels: %#v", rows)
	}
}

func TestAdminMCPManageRouterReturnsHubRegistryUnderAdminSession(t *testing.T) {
	adminPassword := "router-test-admin-password"
	s := New(Config{ConfigDir: t.TempDir(), AdminPassword: adminPassword})
	s.mu.Lock()
	s.virtualMCP["network-proxy"] = true
	s.virtualMCP["webhooks"] = false
	s.agents["shell:fixture-node"] = &Agent{
		AgentID: "shell:fixture-node", Name: "Fixture node", Kind: "virtual_shell", Status: "online",
		Meta: map[string]any{"mcp_agents": []any{map[string]any{
			"ref": "filesystem", "name": "Filesystem", "enabled": true, "transport": "stdio",
		}}},
	}
	s.mu.Unlock()
	handler := s.Handler()

	body := `{"target":"hub","action":"list"}`
	unauthenticated := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/manage", strings.NewReader(body))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}

	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("password="+adminPassword+"&next=%2Fadmin%2F"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusFound {
		t.Fatalf("admin login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var adminCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == adminSessionCookieName {
			adminCookie = cookie
			break
		}
	}
	if adminCookie == nil || adminCookie.Value == "" {
		t.Fatal("admin login did not issue a session cookie")
	}

	request := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/manage", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(adminCookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated Hub list status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		OK       bool   `json:"ok"`
		Target   string `json:"target"`
		Response struct {
			Complete bool             `json:"complete"`
			Servers  []map[string]any `json:"servers"`
		} `json:"response"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode Hub list response: %v; body=%s", err, response.Body.String())
	}
	if !result.OK || result.Target != "hub" || !result.Response.Complete {
		t.Fatalf("Hub list envelope = %#v", result)
	}
	byID := make(map[string]map[string]any, len(result.Response.Servers))
	for _, server := range result.Response.Servers {
		id, _ := server["server_id"].(string)
		byID[id] = server
	}
	for id, category := range map[string]string{
		"hub":                               adminMCPInventoryHubCore,
		"network-proxy":                     adminMCPInventoryVirtual,
		"webhooks":                          adminMCPInventoryVirtual,
		"shell:fixture-node":                adminMCPInventoryHost,
		"mcp:shell:fixture-node:filesystem": adminMCPInventoryChild,
	} {
		server := byID[id]
		if server == nil || server["registry_category"] != category {
			t.Errorf("registry row %q = %#v, want category %q; full registry=%#v", id, server, category, byID)
		}
	}
	if _, present := byID["control:fixture-node"]; present {
		t.Fatalf("paired control surface was listed as an installed MCP: %#v", byID["control:fixture-node"])
	}
	if _, present := byID["file:fixture-node"]; present {
		t.Fatalf("paired file surface was listed as an installed MCP: %#v", byID["file:fixture-node"])
	}
}
