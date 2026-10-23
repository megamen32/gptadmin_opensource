package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTaskReceiptAccessBindsACLCallerAndCurrentProfile(t *testing.T) {
	s := &Server{accessProfiles: map[string]AccessProfile{
		"limited": {ID: "limited", Version: 3, AccessMode: accessModeFull},
	}}
	claims := map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a"}
	r := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/job-a", nil), claims)
	r = requestWithAccessProfile(r, s.accessProfiles["limited"])
	caller := taskCallerFromRequest(r)
	job := &shellJob{ID: "job-a", Caller: caller, FilesystemACL: &filesystemACLPolicy{Version: 1, ProfileID: "limited", ProfileVersion: 3}}

	if err := s.authorizeTaskReceipt(r, job.Caller, job.FilesystemACL); err != nil {
		t.Fatalf("own current task receipt denied: %v", err)
	}
	foreign := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/job-a", nil), map[string]any{"client_id": "client-b", "jti": "token-b", "sub": "client-b"})
	foreign = requestWithAccessProfile(foreign, s.accessProfiles["limited"])
	if err := s.authorizeTaskReceipt(foreign, job.Caller, job.FilesystemACL); err == nil {
		t.Fatal("foreign ACL caller read another client's saved task output")
	}
	if err := s.authorizeTaskReceipt(r, nil, job.FilesystemACL); err == nil {
		t.Fatal("ACL caller read an unbound legacy task receipt")
	}
	s.accessProfiles["limited"] = AccessProfile{ID: "limited", Version: 4, AccessMode: accessModeFull}
	if err := s.authorizeTaskReceipt(r, job.Caller, job.FilesystemACL); err == nil {
		t.Fatal("task receipt remained available after its bound profile changed")
	}
}

func TestMCPRelayJobRejectsForeignAndLegacyHistoryBeforeAck(t *testing.T) {
	profile := AccessProfile{ID: "limited", Version: 3, AccessMode: accessModeFull}
	s := &Server{
		accessProfiles: map[string]AccessProfile{"limited": profile},
		shellJobs: map[string]*shellJob{
			"foreign": {ID: "foreign", Server: "fixture", Status: "completed", Result: map[string]any{"sentinel": "outside-root-secret"}},
			"legacy":  {ID: "legacy", Server: "fixture", Status: "completed", Result: map[string]any{"sentinel": "legacy-secret"}},
		},
		relayJobs: map[string]*relayJob{
			"relay-foreign": {ID: "relay-foreign", AgentID: "child:fixture", Status: "completed", Result: map[string]any{"sentinel": "relay-secret"}},
		},
	}
	ownerRequest := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/owned", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a"})
	ownerRequest = requestWithAccessProfile(ownerRequest, profile)
	owner := taskCallerFromRequest(ownerRequest)
	s.shellJobs["owned"] = &shellJob{ID: "owned", Server: "fixture", Status: "completed", Caller: owner, FilesystemACL: &filesystemACLPolicy{Version: 1, ProfileID: "limited", ProfileVersion: 3}, Result: map[string]any{"sentinel": "own-safe-result"}}

	foreignRequest := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/foreign?ack=true", nil), map[string]any{"client_id": "client-b", "jti": "token-b", "sub": "client-b"})
	foreignRequest = requestWithAccessProfile(foreignRequest, profile)
	foreignResponse := httptest.NewRecorder()
	s.mcpRelayJob(foreignResponse, foreignRequest)
	if foreignResponse.Code != http.StatusNotFound || s.shellJobs["foreign"] == nil || strings.Contains(foreignResponse.Body.String(), "outside-root-secret") {
		t.Fatalf("foreign history read/ack was not denied safely: code=%d body=%s present=%v", foreignResponse.Code, foreignResponse.Body.String(), s.shellJobs["foreign"] != nil)
	}
	foreignRelayRequest := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/get_mcp_job/relay-foreign?ack=true", nil), map[string]any{"client_id": "client-b", "jti": "token-b", "sub": "client-b"})
	foreignRelayRequest = requestWithAccessProfile(foreignRelayRequest, profile)
	foreignRelayResponse := httptest.NewRecorder()
	s.mcpRelayJob(foreignRelayResponse, foreignRelayRequest)
	if foreignRelayResponse.Code != http.StatusNotFound || s.relayJobs["relay-foreign"] == nil || strings.Contains(foreignRelayResponse.Body.String(), "relay-secret") {
		t.Fatalf("foreign relay history read/ack was not denied safely: code=%d body=%s present=%v", foreignRelayResponse.Code, foreignRelayResponse.Body.String(), s.relayJobs["relay-foreign"] != nil)
	}
	legacyRequest := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/legacy", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a"})
	legacyRequest = requestWithAccessProfile(legacyRequest, profile)
	legacyResponse := httptest.NewRecorder()
	s.mcpRelayJob(legacyResponse, legacyRequest)
	if legacyResponse.Code != http.StatusNotFound || strings.Contains(legacyResponse.Body.String(), "legacy-secret") {
		t.Fatalf("ACL caller read unbound legacy result: code=%d body=%s", legacyResponse.Code, legacyResponse.Body.String())
	}
	ownedRequest := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/owned", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a"})
	ownedRequest = requestWithAccessProfile(ownedRequest, profile)
	ownedResponse := httptest.NewRecorder()
	s.mcpRelayJob(ownedResponse, ownedRequest)
	if ownedResponse.Code != http.StatusOK || !strings.Contains(ownedResponse.Body.String(), "own-safe-result") {
		t.Fatalf("owner could not read current allowed result: code=%d body=%s", ownedResponse.Code, ownedResponse.Body.String())
	}
	ackRequest := requestWithAuthClaims(httptest.NewRequest(http.MethodGet, "/mcp-relay/job/owned?ack=true", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a"})
	ackRequest = requestWithAccessProfile(ackRequest, profile)
	ackResponse := httptest.NewRecorder()
	s.mcpRelayJob(ackResponse, ackRequest)
	if ackResponse.Code != http.StatusOK || s.shellJobs["owned"] != nil {
		t.Fatalf("owner could not acknowledge own result: code=%d body=%s present=%v", ackResponse.Code, ackResponse.Body.String(), s.shellJobs["owned"] != nil)
	}
}

func TestMCPRelayJobAliasesAuthorizePersistedCallerThroughRouter(t *testing.T) {
	s := New(Config{CtlToken: "ctl", OAuthClientSecret: "receipt-test-secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example"})
	defer s.Close()
	roots := []string{t.TempDir()}
	profile := AccessProfile{ID: "limited", Version: 1, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub", "shell:fixture"}, AllowedTools: []string{"ro", "search", "safe_child_tool"}, ReadRoots: &roots, WriteRoots: &roots}
	s.accessProfiles[profile.ID] = profile
	makeToken := func(clientID, tokenID string) string {
		t.Helper()
		token, err := s.signJWT(map[string]any{
			"sub": clientID, "client_id": clientID, "jti": tokenID, "profile_id": profile.ID,
			"scope": "gptadmin.read gptadmin.exec", "aud": "https://hub.example", "resource": "https://hub.example",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	ownerToken := makeToken("receipt-owner", "receipt-owner-token")
	foreignToken := makeToken("receipt-foreign", "receipt-foreign-token")
	owner := &taskCaller{ClientID: "receipt-owner", TokenID: "receipt-owner-token", Subject: "receipt-owner", ProfileID: profile.ID, ProfileVersion: 1}
	s.relayJobs["relay-history"] = &relayJob{ID: "relay-history", AgentID: "fixture-agent", Method: "tools/call", CreatedAt: nowFloat(), DoneAt: nowFloat(), Status: "completed", Caller: owner, Result: map[string]any{"sentinel": "relay-history-private"}}

	route := s.Handler()
	call := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		route.ServeHTTP(w, req)
		return w
	}
	for _, prefix := range []string{"/mcp-relay/job/", "/mcp-relay/get_mcp_job/"} {
		for _, ack := range []bool{false, true} {
			path := prefix + "relay-history"
			if ack {
				path += "?ack=true"
			}
			response := call(foreignToken, path)
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "relay-history-private") || s.relayJobs["relay-history"] == nil {
				t.Fatalf("foreign route %s exposed/consumed relay receipt: status=%d body=%s present=%v", path, response.Code, response.Body.String(), s.relayJobs["relay-history"] != nil)
			}
		}
	}
	if response := call(ownerToken, "/mcp-relay/job/relay-history"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "relay-history-private") {
		t.Fatalf("owner could not read relay receipt: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(ownerToken, "/mcp-relay/get_mcp_job/relay-history?ack=true"); response.Code != http.StatusOK || s.relayJobs["relay-history"] != nil {
		t.Fatalf("owner could not acknowledge relay receipt: status=%d body=%s present=%v", response.Code, response.Body.String(), s.relayJobs["relay-history"] != nil)
	}

	parent := Agent{AgentID: "shell:fixture", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "fixture-child", "enabled": true, "transport": "streamable-http"}}}}
	s.agents[parent.AgentID] = &parent
	child := childMCPAgents(parent)[0]
	profile.AllowedTargets = append(profile.AllowedTargets, child.AgentID)
	profile.Version++
	s.accessProfiles[profile.ID] = profile
	ownerClaims, err := s.verifyBearerJWTFromRequest(authenticatedReceiptRequest(ownerToken))
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := requestWithAuthClaims(authenticatedReceiptRequest(ownerToken), ownerClaims)
	ownerRequest = s.applyAccessProfileContext(ownerRequest, ownerClaims)
	queued := s.childSchemaForRequest(ownerRequest, child)
	childTaskID := firstString(queued, "job_id")
	if childTaskID == "" {
		t.Fatalf("child schema fixture did not produce a receipt: %v", queued)
	}
	s.mu.Lock()
	shell := s.shellJobs[childTaskID]
	relay := s.relayJobs[childTaskID]
	shell.Status, shell.StartedAt, shell.DoneAt = "completed", nowFloat(), nowFloat()
	shell.Result = map[string]any{"tools": []any{map[string]any{"name": "safe_child_tool"}}}
	relay.Status, relay.StartedAt, relay.DoneAt = "completed", shell.StartedAt, shell.DoneAt
	s.mu.Unlock()
	if response := call(ownerToken, "/mcp-relay/job/"+childTaskID); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "safe_child_tool") {
		t.Fatalf("owner could not read child-schema receipt: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(foreignToken, "/mcp-relay/get_mcp_job/"+childTaskID+"?ack=true"); response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "safe_child_tool") || s.relayJobs[childTaskID] == nil {
		t.Fatalf("foreign caller read/consumed child-schema receipt: status=%d body=%s present=%v", response.Code, response.Body.String(), s.relayJobs[childTaskID] != nil)
	}
	profile.Version++
	s.accessProfiles[profile.ID] = profile
	if response := call(ownerToken, "/mcp-relay/job/"+childTaskID); response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "safe_child_tool") {
		t.Fatalf("stale profile read child-schema receipt: status=%d body=%s", response.Code, response.Body.String())
	}
}

func authenticatedReceiptRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/mcp-relay/job/receipt", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestMCPTaskAndResourceReadsRequireBoundCaller(t *testing.T) {
	profile := AccessProfile{ID: "limited", Version: 7, AccessMode: accessModeFull}
	s := &Server{
		accessProfiles: map[string]AccessProfile{"limited": profile},
		shellJobs: map[string]*shellJob{
			"task": {ID: "task", Server: "fixture", ToolName: "system_inspect", Status: "completed", Caller: &taskCaller{ClientID: "client-a", TokenID: "token-a", Subject: "client-a", ProfileID: "limited", ProfileVersion: 7}, FilesystemACL: &filesystemACLPolicy{Version: 1, ProfileID: "limited", ProfileVersion: 7}, Result: map[string]any{"sentinel": "task-result-secret", "stdout_path": "/outside/root/stdout"}},
		},
		relayJobs: map[string]*relayJob{},
	}
	foreign := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "client-b", "jti": "token-b", "sub": "client-b"})
	foreign = requestWithAccessProfile(foreign, profile)
	if result, rpcErr := s.mcpTaskGetForRequest(foreign, "task", ""); rpcErr == nil || result != nil {
		t.Fatalf("foreign tasks/get exposed output: result=%v err=%v", result, rpcErr)
	}
	if result := s.appsSDKCallForRequest(foreign, "get_mcp_job", map[string]any{"id": "task"}); strings.Contains(string(mustJSON(t, result)), "task-result-secret") {
		t.Fatalf("legacy hub job alias exposed foreign output: %v", result)
	}
	resource := s.taskOutputResourceRead(foreign, "gptadmin://task/task/stdout", "")
	if strings.Contains(string(mustJSON(t, resource)), "outside/root") || strings.Contains(string(mustJSON(t, resource)), "task-result-secret") {
		t.Fatalf("foreign task resource read exposed saved output metadata: %v", resource)
	}
	owner := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a"})
	owner = requestWithAccessProfile(owner, profile)
	if result, rpcErr := s.mcpTaskGetForRequest(owner, "task", ""); rpcErr != nil || !strings.Contains(string(mustJSON(t, result)), "task-result-secret") {
		t.Fatalf("owner could not read its current MCP task result: result=%v err=%v", result, rpcErr)
	}
}

func TestMCPClientHTTPPathAllowlistExcludesExecutorAndAdminRoutes(t *testing.T) {
	for _, path := range []string{
		"/mcp-relay/register", "/mcp-relay/poll/node", "/mcp-relay/result/node",
		"/mcp-relay/call", "/mcp-relay/shell_exec",
		"/admin/api/mcp/issue-token", "/admin/api/client-roles/client-a", "/webhook-routes/route",
	} {
		if mcpClientHTTPPathAllowed(path, http.MethodGet) {
			t.Errorf("managed MCP client unexpectedly allowed on %s", path)
		}
	}
	for _, path := range []string{"/mcp-relay/job/job-a", "/mcp-relay/get_mcp_job/job-a", "/mcp-relay/servers", "/mcp-relay/list_mcp_servers", "/mcp-relay/agents", "/webhook-routes", "/admin/api/webhook-jobs/job-a"} {
		if !mcpClientHTTPPathAllowed(path, http.MethodGet) {
			t.Errorf("user receipt/read route unexpectedly denied at path allowlist: %s", path)
		}
	}
	for _, path := range []string{"/mcp-relay/call", "/mcp-relay/call_mcp_tool", "/mcp-relay/shell_exec", "/mcp-relay/tools", "/mcp-relay/list_mcp_tools", "/mcp-relay/expand"} {
		if !mcpClientHTTPPathAllowed(path, http.MethodPost) {
			t.Errorf("protected consumer POST unexpectedly denied at path allowlist: %s", path)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/webhook-routes"}, {http.MethodPut, "/webhook-routes/route-a"}, {http.MethodDelete, "/webhook-routes/route-a"},
	} {
		if !mcpClientHTTPPathAllowed(tc.path, tc.method) {
			t.Errorf("protected webhook consumer method unexpectedly denied: %s %s", tc.method, tc.path)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/mcp-relay/job/job-a"}, {http.MethodGet, "/webhook-routes/route-a"},
		{http.MethodPost, "/mcp-relay/register"}, {http.MethodGet, "/mcp-relay/poll/node"},
		{http.MethodPost, "/mcp-relay/result/node"}, {http.MethodGet, "/mcp-relay/grepmesh"}, {http.MethodGet, "/admin/api/mcp/tokens"},
	} {
		if mcpClientHTTPPathAllowed(tc.path, tc.method) {
			t.Errorf("managed client unexpectedly allowed %s %s", tc.method, tc.path)
		}
	}
	s := &Server{}
	client := map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a", "scope": "gptadmin.read gptadmin.exec"}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/admin/api/client-roles/client-a"},
		{http.MethodPost, "/admin/api/mcp/issue-token"},
		{http.MethodGet, "/admin/api/mcp/tokens"},
	} {
		r := requestWithAuthClaims(httptest.NewRequest(tc.method, tc.path, nil), client)
		if s.clientHTTPPathAllowed(r) {
			t.Errorf("managed client path gate unexpectedly allowed %s %s", tc.method, tc.path)
		}
	}
}

func TestFilesystemScopedProfileCannotReadUnboundWebhookHistory(t *testing.T) {
	roots := []string{"/tmp/allowed"}
	profile := AccessProfile{ID: "limited", Version: 1, ReadRoots: &roots, AllowedTargets: []string{"hub"}, AllowedTools: []string{webhookJobGetTool}}
	r := requestWithAccessProfile(httptest.NewRequest(http.MethodPost, "/mcp", nil), profile)
	if err := authorizeToolCall(r, "hub", webhookJobGetTool); err == nil {
		t.Fatal("filesystem-scoped profile can read an unbound webhook job result")
	}
	if err := authorizeFacadeCall(r, webhookJobGetTool, map[string]any{"id": "unbound"}); err == nil {
		t.Fatal("filesystem-scoped HTTP facade can read an unbound webhook job result")
	}
}
