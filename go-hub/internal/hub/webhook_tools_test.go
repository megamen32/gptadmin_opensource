package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestWebhookHubToolsProvideSecretSafeCRUDAndJobParity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "webhooks.json")
	s := New(Config{CtlToken: "ctl", WebhookConfigFile: configPath})
	route := map[string]any{
		"id":                "repair-mac",
		"hmac_secret":       "write-only-secret",
		"signature_version": "v2",
		"action": map[string]any{
			"kind": "shell", "target": "shell:mac", "command": "fixed-helper repair_mac",
		},
	}

	created, status := s.callWebhookHubTool("webhook_route_create", map[string]any{"route": route})
	if status != http.StatusCreated || created["id"] != "repair-mac" {
		t.Fatalf("create status=%d response=%v", status, created)
	}
	if encoded, _ := json.Marshal(created); string(encoded) == "" || containsSecret(string(encoded), "write-only-secret") {
		t.Fatalf("create response exposed route secret: %s", encoded)
	}

	listed, status := s.callWebhookHubTool("webhook_routes_list", nil)
	if status != http.StatusOK || len(anySlice(listed["routes"])) != 1 {
		t.Fatalf("list status=%d response=%v", status, listed)
	}

	replacement := cloneMap(route)
	replacement["action"] = map[string]any{"kind": "shell", "target": "shell:mac-2", "command": "fixed-helper repair_mac"}
	replaced, status := s.callWebhookHubTool("webhook_route_replace", map[string]any{"id": "repair-mac", "route": replacement})
	if status != http.StatusOK || replaced["target"] != "shell:mac-2" {
		t.Fatalf("replace status=%d response=%v", status, replaced)
	}

	s.mu.Lock()
	s.webhookJobs["job-1"] = &webhookJob{ID: "job-1", RouteID: "repair-mac", Status: "completed"}
	s.mu.Unlock()
	job, status := s.callWebhookHubTool("webhook_job_get", map[string]any{"id": "job-1"})
	if status != http.StatusOK || job["job_id"] != "job-1" || job["status"] != "completed" {
		t.Fatalf("job status=%d response=%v", status, job)
	}

	if _, status := s.callWebhookHubTool("webhook_route_delete", map[string]any{"id": "repair-mac"}); status != http.StatusBadRequest {
		t.Fatalf("delete without confirmation status=%d, want 400", status)
	}
	deleted, status := s.callWebhookHubTool("webhook_route_delete", map[string]any{"id": "repair-mac", "confirm": true})
	if status != http.StatusOK || deleted["deleted"] != true {
		t.Fatalf("delete status=%d response=%v", status, deleted)
	}
}

func TestOAuthWebhookActionsHonorScopesAndAccessProfiles(t *testing.T) {
	s := New(Config{
		CtlToken: "ctl", OAuthClientSecret: "oauth-secret",
		PublicOrigin: "https://hub.example", MCPResource: "https://hub.example",
		WebhookConfigFile: filepath.Join(t.TempDir(), "webhooks.json"),
	})
	s.mu.Lock()
	s.accessProfiles["webhook-reader"] = AccessProfile{
		ID: "webhook-reader", AccessMode: accessModeReadonly, ApprovalMode: approvalModeReadOnly,
		AllowedTargets: []string{"hub"}, AllowedTools: []string{webhookRoutesListTool, webhookJobGetTool}, Version: 1,
	}
	s.accessProfiles["webhook-writer"] = AccessProfile{
		ID: "webhook-writer", AccessMode: accessModeFull, ApprovalMode: approvalModeBoundedAutonomous,
		AllowedTargets: []string{"hub"}, AllowedTools: []string{webhookRouteCreateTool}, Version: 1,
	}
	s.accessProfiles["webhook-denied"] = AccessProfile{
		ID: "webhook-denied", AccessMode: accessModeFull, ApprovalMode: approvalModeBoundedAutonomous,
		AllowedTargets: []string{"hub"}, AllowedTools: []string{webhookRoutesListTool}, Version: 1,
	}
	s.mu.Unlock()

	token := func(profileID, scope string) string {
		t.Helper()
		value, err := s.signJWT(map[string]any{
			"sub": profileID, "client_id": profileID, "profile_id": profileID, "scope": scope,
			"aud": "https://hub.example", "resource": "https://hub.example",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}

	reader := token("webhook-reader", "gptadmin.read")
	if response := call(http.MethodGet, "/webhook-routes", reader, ""); response.Code != http.StatusOK {
		t.Fatalf("OAuth reader list status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodGet, "/admin/api/webhook-jobs/missing", reader, ""); response.Code != http.StatusNotFound {
		t.Fatalf("OAuth reader job status=%d body=%s", response.Code, response.Body.String())
	}
	routeBody := `{"id":"oauth-route","token":"write-only","action":{"kind":"shell","target":"shell:test","command":"fixed-helper"}}`
	if response := call(http.MethodPost, "/webhook-routes", reader, routeBody); response.Code != http.StatusForbidden {
		t.Fatalf("OAuth reader write status=%d body=%s", response.Code, response.Body.String())
	}
	denied := token("webhook-denied", "gptadmin.read gptadmin.exec")
	if response := call(http.MethodPost, "/webhook-routes", denied, routeBody); response.Code != http.StatusForbidden {
		t.Fatalf("profile-denied OAuth write status=%d body=%s", response.Code, response.Body.String())
	}
	writer := token("webhook-writer", "gptadmin.read gptadmin.exec")
	if response := call(http.MethodPost, "/webhook-routes", writer, routeBody); response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "write-only") {
		t.Fatalf("OAuth writer create status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebhookMCPWritesHonorApprovalAndBoundedAutonomousGates(t *testing.T) {
	newServer := func(profile AccessProfile) (*Server, string) {
		t.Helper()
		s := New(Config{
			CtlToken: "ctl", OAuthClientSecret: "oauth-secret",
			PublicOrigin: "https://hub.example", MCPResource: "https://hub.example",
			WebhookConfigFile: filepath.Join(t.TempDir(), profile.ID+".json"),
		})
		s.mu.Lock()
		s.virtualMCP["webhooks"] = true
		s.accessProfiles[profile.ID] = profile
		s.mu.Unlock()
		token, err := s.signJWT(map[string]any{
			"sub": profile.ID, "client_id": profile.ID, "profile_id": profile.ID,
			"scope": "gptadmin.read gptadmin.exec", "aud": "https://hub.example", "resource": "https://hub.example",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s, token
	}
	call := func(s *Server, token string, arguments map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": webhookRouteCreateTool, "arguments": arguments},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/server/webhooks/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	route := func(id string) map[string]any {
		return map[string]any{"id": id, "token": "write-only", "action": map[string]any{"kind": "shell", "target": "shell:test", "command": "fixed-helper"}}
	}

	askProfile := AccessProfile{
		ID: "webhook-ask", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite,
		AllowedTargets: []string{"webhooks"}, AllowedTools: []string{webhookRouteCreateTool}, Version: 1,
	}
	askServer, askToken := newServer(askProfile)
	pending := call(askServer, askToken, map[string]any{"route": route("ask-route")})
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), `"code":-32004`) {
		t.Fatalf("MCP write bypassed ask-before-write: status=%d body=%s", pending.Code, pending.Body.String())
	}
	if len(askServer.listWebhookRouteSummaries()) != 0 {
		t.Fatal("ask-before-write created a route before approval")
	}
	var pendingBody map[string]any
	if err := json.Unmarshal(pending.Body.Bytes(), &pendingBody); err != nil {
		t.Fatal(err)
	}
	errorBody := mapValue(pendingBody["error"])
	approvalID := firstString(mapValue(errorBody["data"]), "approval_id")
	if approvalID == "" || strings.Contains(pending.Body.String(), "write-only") {
		t.Fatalf("approval response invalid or exposed a secret: %s", pending.Body.String())
	}
	approve := httptest.NewRequest(http.MethodPost, "/admin/api/approvals/"+approvalID, strings.NewReader(`{"action":"approve"}`))
	approve.Header.Set("Authorization", "Bearer ctl")
	approved := httptest.NewRecorder()
	askServer.Handler().ServeHTTP(approved, approve)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", approved.Code, approved.Body.String())
	}
	if response := call(askServer, askToken, map[string]any{"route": route("ask-route"), "approval_id": approvalID}); response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"code":-32004`) {
		t.Fatalf("approved MCP write did not execute: status=%d body=%s", response.Code, response.Body.String())
	}

	boundedProfile := AccessProfile{
		ID: "webhook-bounded", AccessMode: accessModeFull, ApprovalMode: approvalModeBoundedAutonomous,
		AllowedTargets: []string{"webhooks"}, AllowedTools: []string{webhookRouteCreateTool}, Version: 1,
	}
	boundedServer, boundedToken := newServer(boundedProfile)
	for i := 0; i < autonomousCallLimit; i++ {
		response := call(boundedServer, boundedToken, map[string]any{"route": route("bounded-" + string(rune('a'+i)))})
		if strings.Contains(response.Body.String(), `"code":-32005`) {
			t.Fatalf("bounded budget exhausted early at %d: %s", i, response.Body.String())
		}
	}
	overBudget := call(boundedServer, boundedToken, map[string]any{"route": route("bounded-over")})
	if overBudget.Code != http.StatusOK || !strings.Contains(overBudget.Body.String(), `"code":-32005`) {
		t.Fatalf("MCP write bypassed bounded budget: status=%d body=%s", overBudget.Code, overBudget.Body.String())
	}
	for _, summary := range boundedServer.listWebhookRouteSummaries() {
		if summary.ID == "bounded-over" {
			t.Fatal("over-budget MCP write created a route")
		}
	}
}

func TestWebhookParityToolsAreVirtualAndReadPolicyIsNarrow(t *testing.T) {
	for _, name := range []string{"webhook_routes_list", "webhook_route_create", "webhook_route_replace", "webhook_route_delete", "webhook_job_get"} {
		if !toolListContains(virtualMCPTools(virtualMCPDefinitions["webhooks"]), name) {
			t.Fatalf("webhooks virtual MCP does not advertise %q", name)
		}
		if toolListContains(hubTools(), name) || toolListContains(appsSDKTools(), name) {
			t.Fatalf("default hub surface leaked optional webhook tool %q", name)
		}
	}
	encodedSchema, _ := json.Marshal(webhookRouteInputSchema())
	for _, required := range []string{`"signature_version"`, `"max_skew_seconds"`, `"approval_mode"`, `"arguments"`, `"callback"`, `"writeOnly":true`} {
		if !containsSecret(string(encodedSchema), required) {
			t.Fatalf("MCP route schema is missing UI field %s: %s", required, encodedSchema)
		}
	}
	request := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{
		"scope": "gptadmin.read", "access_mode": accessModeReadonly,
	})
	for _, name := range []string{"webhook_routes_list", "webhook_job_get"} {
		if err := authorizeFacadeCall(request, name, nil); err != nil {
			t.Fatalf("read-only parity tool %q denied: %v", name, err)
		}
	}
	for _, name := range []string{"webhook_route_create", "webhook_route_replace", "webhook_route_delete"} {
		if err := authorizeFacadeCall(request, name, nil); err == nil {
			t.Fatalf("write parity tool %q allowed to read-only client", name)
		}
	}
}

func TestAdminWebhookJobEndpointUsesOperatorAuthentication(t *testing.T) {
	s := New(Config{CtlToken: "ctl"})
	s.mu.Lock()
	s.webhookJobs["job-admin"] = &webhookJob{ID: "job-admin", RouteID: "route", Status: "failed", Error: "safe failure"}
	s.mu.Unlock()

	request := httptest.NewRequest(http.MethodGet, "/admin/api/webhook-jobs/job-admin", nil)
	request.Header.Set("Authorization", "Bearer ctl")
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusOK || !containsSecret(record.Body.String(), `"job_id":"job-admin"`) {
		t.Fatalf("admin job status=%d body=%s", record.Code, record.Body.String())
	}
}

func TestActionsOpenAPIDefaultExcludesWebhookManagement(t *testing.T) {
	s := New(Config{PublicOrigin: "https://hub.example"})
	request := httptest.NewRequest(http.MethodGet, "/actions/openapi.yaml", nil)
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("openapi status=%d body=%s", record.Code, record.Body.String())
	}
	for _, operationID := range []string{"listWebhookRoutes", "createWebhookRoute", "replaceWebhookRoute", "deleteWebhookRoute", "getAdminWebhookJob"} {
		if containsSecret(record.Body.String(), "operationId: "+operationID) {
			t.Fatalf("default OpenAPI leaked %s", operationID)
		}
	}
	if strings.Contains(record.Body.String(), "X-GPTAdmin-Approval-ID") || strings.Contains(record.Body.String(), "webhookToken:") {
		t.Fatalf("default OpenAPI contains Custom GPT-incompatible webhook security or approval header")
	}
	if strings.Count(record.Body.String(), "scheme: bearer") != 1 {
		t.Fatalf("default OpenAPI must contain exactly one bearer security scheme")
	}
	var document any
	if err := yaml.Unmarshal(record.Body.Bytes(), &document); err != nil {
		t.Fatalf("OpenAPI is not valid YAML: %v", err)
	}
}

func TestWebhookRouteLifecycleIsCallableThroughMCP(t *testing.T) {
	s := New(Config{CtlToken: "ctl", WebhookConfigFile: filepath.Join(t.TempDir(), "webhooks.json")})
	s.mu.Lock()
	s.virtualMCP["webhooks"] = true
	s.mu.Unlock()
	created := postMCPRPCPath(t, s, "/server/webhooks/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"webhook_route_create","arguments":{"route":{"id":"mcp-route","token":"mcp-write-only","action":{"kind":"mcp","target":"hub","tool":"status"}}}}}`)
	if containsSecret(string(mustJSON(t, created)), "mcp-write-only") {
		t.Fatalf("MCP create response exposed a route secret: %v", created)
	}
	s.mu.Lock()
	_, exists := s.webhookRoutes["mcp-route"]
	s.mu.Unlock()
	if !exists {
		t.Fatal("MCP create did not persist the route")
	}
	listed := postMCPRPCPath(t, s, "/server/webhooks/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"webhook_routes_list","arguments":{}}}`)
	if !containsSecret(string(mustJSON(t, listed)), "mcp-route") || containsSecret(string(mustJSON(t, listed)), "mcp-write-only") {
		t.Fatalf("MCP list is incomplete or exposed a secret: %v", listed)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func containsSecret(value, needle string) bool {
	return len(needle) > 0 && len(value) >= len(needle) && stringContains(value, needle)
}

func stringContains(value, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	if items != nil {
		return items
	}
	if typed, ok := value.([]webhookRouteSummary); ok {
		items = make([]any, len(typed))
		for index := range typed {
			items[index] = typed[index]
		}
	}
	return items
}

func toolListContains(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		if tool["name"] == name {
			return true
		}
	}
	return false
}
