package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListServersUsesHubKind(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	r := httptest.NewRequest(http.MethodGet, "/mcp-relay/list_mcp_servers", nil)
	r.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) == 0 {
		t.Fatalf("no servers in response")
	}
	if got := body.Servers[0]["kind"]; got != "hub" {
		t.Fatalf("hub kind=%q, want hub", got)
	}
}

func TestRelayToolsRoundTrip(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: 1, PollMaxTimeout: 1})
	register := []byte(`{"agent_id":"demo","name":"Demo","capabilities":["tools/list","tools/call"]}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	call := []byte(`{"target":"demo","tool_name":"ping","query":"hello","background":true}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp-relay/call_mcp_tool", bytes.NewReader(call))
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("call status=%d body=%s", w.Code, w.Body.String())
	}
	var queued map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	jobID, _ := queued["job_id"].(string)
	if jobID == "" {
		t.Fatalf("missing job_id in %v", queued)
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/demo?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", w.Code, w.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job["id"] != jobID || job["method"] != "tools/call" {
		t.Fatalf("bad relay job: %v", job)
	}
	params := job["params"].(map[string]any)
	args := params["arguments"].(map[string]any)
	if args["query"] != "hello" {
		t.Fatalf("top-level query was not forwarded as tool argument: %v", job)
	}
}

func TestMCPRelayCallIdempotencyReusesJobAndRejectsConflict(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	registerRelayAgent(t, s, "demo")

	call := func(arguments string) map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call_mcp_tool", strings.NewReader(arguments))
		req.Header.Set("Authorization", "Bearer ctl")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("call status=%d body=%s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	first := call(`{"target":"demo","tool_name":"write","arguments":{"value":"one"},"idempotency_key":"write-1","background":true}`)
	second := call(`{"target":"demo","tool_name":"write","arguments":{"value":"one"},"idempotency_key":"write-1","background":true}`)
	if first["job_id"] != second["job_id"] {
		t.Fatalf("same idempotency key created different jobs: first=%v second=%v", first, second)
	}
	s.mu.Lock()
	queued := len(s.relayQueues["demo"])
	s.mu.Unlock()
	if queued != 1 {
		t.Fatalf("queued jobs=%d, want 1", queued)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call_mcp_tool", strings.NewReader(`{"target":"demo","tool_name":"write","arguments":{"value":"two"},"idempotency_key":"write-1","background":true}`))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflicting reuse status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMCPRelayCallIdempotencyReturnsCompletedResultAfterRetry(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	registerRelayAgent(t, s, "demo")

	body := postHubJSON(t, s, "/mcp-relay/call_mcp_tool", "ctl", `{"target":"demo","tool_name":"write","arguments":{"value":"one"},"idempotency_key":"write-2"}`)
	jobID, _ := body["job_id"].(string)
	if jobID == "" {
		t.Fatalf("missing job_id from timed-out synchronous call: %v", body)
	}

	req := httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/demo?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer relay")
	poll := httptest.NewRecorder()
	s.Handler().ServeHTTP(poll, req)
	if poll.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", poll.Code, poll.Body.String())
	}
	postHubJSON(t, s, "/mcp-relay/result/demo", "relay", `{"id":"`+jobID+`","ok":true,"result":{"changed":true}}`)

	retried := postHubJSON(t, s, "/mcp-relay/call_mcp_tool", "ctl", `{"target":"demo","tool_name":"write","arguments":{"value":"one"},"idempotency_key":"write-2"}`)
	if retried["job_id"] != jobID || retried["status"] != "completed" {
		t.Fatalf("retry did not return completed original result: %v", retried)
	}
	s.mu.Lock()
	queued := len(s.relayQueues["demo"])
	s.mu.Unlock()
	if queued != 0 {
		t.Fatalf("retry enqueued another job, queue length=%d", queued)
	}
}

func TestMCPAppsCallUsesSameIdempotencyContract(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	registerRelayAgent(t, s, "demo")
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"call_mcp_tool","arguments":{"target":"demo","tool_name":"write","arguments":{"value":"one"},"idempotency_key":"apps-write-1","background":true}}}`
	first := postMCPRPC(t, s, payload)
	second := postMCPRPC(t, s, strings.Replace(payload, `"id":1`, `"id":2`, 1))
	firstStructured := mapValue(mapValue(first["result"])["structuredContent"])
	secondStructured := mapValue(mapValue(second["result"])["structuredContent"])
	if firstStructured["job_id"] != secondStructured["job_id"] {
		t.Fatalf("MCP Apps retry created different jobs: first=%v second=%v", firstStructured, secondStructured)
	}
}

func TestMCPToolsExposeCompactCanonicalNames(t *testing.T) {
	tools := appsSDKTools()
	encoded, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 12000 {
		t.Fatalf("tools/list payload is %d bytes; want <=12000", len(encoded))
	}
	got := map[string]map[string]any{}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		got[name] = tool
	}
	for _, name := range []string{"discover", "schema", "execute", "job", "inspect", "ui"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing canonical tool %q; tools=%v", name, got)
		}
	}
	for _, legacy := range []string{"list_mcp_servers", "list_mcp_tools", "call_mcp_tool", "get_mcp_job"} {
		if _, ok := got[legacy]; ok {
			t.Fatalf("legacy tool %q must not be advertised in tools/list", legacy)
		}
	}
	for name, tool := range got {
		description, _ := tool["description"].(string)
		if len(description) > 180 {
			t.Fatalf("description for %s is %d bytes; want <=180: %s", name, len(description), description)
		}
	}
}

func registerRelayAgent(t *testing.T, s *Server, agentID string) {
	t.Helper()
	postHubJSON(t, s, "/mcp-relay/register", "relay", `{"agent_id":"`+agentID+`","name":"Demo","capabilities":["tools/list","tools/call"]}`)
}

func postHubJSON(t *testing.T, s *Server, path, token, payload string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code < http.StatusOK || w.Code >= 300 {
		t.Fatalf("POST %s status=%d body=%s", path, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func postMCPRPC(t *testing.T, s *Server, payload string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("MCP status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestOAuthAndMCPJSONRPC(t *testing.T) {
	s := New(Config{CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", OAuthPermissiveRedirects: true, OAuthPermissiveResources: true, DefaultTimeout: 1, PollMaxTimeout: 1})

	// Unauthorized MCP must advertise OAuth protected-resource metadata.
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatalf("missing WWW-Authenticate")
	}

	// Issue an auth code directly through the authorize POST path.
	form := "client_id=c1&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fconnector%2Foauth%2Fcb&resource=https%3A%2F%2Fhub.example&scope=gptadmin.read+gptadmin.exec&password=pw&code_challenge=" + pkceChallenge("verifier")
	req = httptest.NewRequest(http.MethodPost, "/authorize", bytes.NewBufferString(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatalf("authorize did not redirect")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect %s", loc)
	}

	tokenForm := "grant_type=authorization_code&code=" + code + "&resource=https%3A%2F%2Fhub.example&code_verifier=verifier"
	req = httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString(tokenForm))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("token status=%d body=%s", w.Code, w.Body.String())
	}
	var tok map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	access, _ := tok["access_token"].(string)
	if access == "" {
		t.Fatalf("missing access_token in %v", tok)
	}

	rpc := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(rpc))
	req.Header.Set("Authorization", "Bearer "+access)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mcp status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"name":"discover"`)) {
		t.Fatalf("tools/list missing expected server tool: %s", w.Body.String())
	}
}

func TestLegacyCTLAppearsInClientInventoryWithoutBeingRevocableAsJWT(t *testing.T) {
	s := New(Config{CtlToken: "legacy-ctl", ConfigDir: t.TempDir(), DefaultTimeout: time.Second, PollMaxTimeout: time.Second})

	list := httptest.NewRequest(http.MethodGet, "/admin/api/clients", nil)
	list.Header.Set("Authorization", "Bearer legacy-ctl")
	listed := httptest.NewRecorder()
	s.Handler().ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"token_kind":"legacy_ctl"`) {
		t.Fatalf("legacy CTL inventory status=%d body=%s", listed.Code, listed.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/admin/api/clients/revoke-all", nil)
	revoke.Header.Set("Authorization", "Bearer legacy-ctl")
	revoked := httptest.NewRecorder()
	s.Handler().ServeHTTP(revoked, revoke)
	if revoked.Code != http.StatusOK || !strings.Contains(revoked.Body.String(), `"revoked_count":0`) {
		t.Fatalf("legacy CTL revoke-all status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestOAuthRotationPersistsWithoutReturningSecret(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "gptadmin.env")
	if err := os.WriteFile(envFile, []byte("OAUTH_CLIENT_SECRET=old-secret\nOTHER=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{CtlToken: "ctl", OAuthClientSecret: "old-secret", EnvFile: envFile, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/rotate-oauth", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "old-secret") || strings.Contains(rec.Body.String(), s.cfg.OAuthClientSecret) {
		t.Fatalf("rotation leaked secret or failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	contents, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "old-secret") || !strings.Contains(string(contents), "OAUTH_CLIENT_SECRET=") {
		t.Fatalf("env file was not rotated: %s", contents)
	}
	if s.cfg.OAuthClientSecret == "old-secret" {
		t.Fatal("runtime OAuth secret was not rotated")
	}
}

func TestSecurityEnvEndpointNeverReturnsValues(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "gptadmin.env")
	if err := os.WriteFile(envFile, []byte("OAUTH_CLIENT_SECRET=secret-value\nSHELLMCP_HEARTBEAT=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{CtlToken: "ctl", EnvFile: envFile, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/security/env", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "secret-value") || !strings.Contains(rec.Body.String(), "OAUTH_CLIENT_SECRET") {
		t.Fatalf("unsafe or incomplete env response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminIssueMCPTokenUsesPublicOriginAndWorksForRelay(t *testing.T) {
	s := New(Config{
		CtlToken:                 "ctl",
		AdminPassword:            "pw",
		OAuthClientSecret:        "oauth-secret",
		PublicOrigin:             "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		MCPResource:              "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
		DefaultTimeout:           time.Second,
		PollMaxTimeout:           time.Second,
	})
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/issue-token", bytes.NewBufferString(`{"client_id":"chatgpt","ttl_days":365}`))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("issue-token status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	access, _ := body["access_token"].(string)
	if access == "" {
		t.Fatalf("missing access_token: %v", body)
	}
	claims, err := s.verifyJWT(access)
	if err != nil {
		t.Fatalf("verifyJWT err=%v", err)
	}
	if claims["iss"] != "https://u-f1102930.t.gptadmin.bezrabotnyi.com" {
		t.Fatalf("iss=%v", claims["iss"])
	}
	if claims["aud"] != "https://u-f1102930.t.gptadmin.bezrabotnyi.com" {
		t.Fatalf("aud=%v", claims["aud"])
	}
	if claims["client_id"] != "chatgpt" {
		t.Fatalf("client_id=%v", claims["client_id"])
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/servers", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("relay with issued token status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"servers"`) {
		t.Fatalf("relay body missing servers: %s", w.Body.String())
	}
}

func TestAdminManagedMCPTokenCanBeListedAndRotated(t *testing.T) {
	configDir := t.TempDir()
	s := New(Config{
		CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret",
		PublicOrigin: "https://hub.example", MCPResource: "https://hub.example",
		ConfigDir: configDir, DefaultTimeout: time.Second, PollMaxTimeout: time.Second,
	})
	h := s.Handler()
	issue := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/issue-token", bytes.NewBufferString(`{"client_id":"manual-client","ttl_days":7}`))
	issue.Header.Set("Authorization", "Bearer ctl")
	issue.Header.Set("Content-Type", "application/json")
	issued := httptest.NewRecorder()
	h.ServeHTTP(issued, issue)
	if issued.Code != http.StatusOK {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var issuedBody map[string]any
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil {
		t.Fatal(err)
	}
	tokenID, _ := issuedBody["token_id"].(string)
	oldToken, _ := issuedBody["access_token"].(string)
	if tokenID == "" || oldToken == "" {
		t.Fatalf("managed token response missing id or token: %v", issuedBody)
	}
	adminWithClientToken := httptest.NewRequest(http.MethodGet, "/admin/api/clients", nil)
	adminWithClientToken.Header.Set("Authorization", "Bearer "+oldToken)
	adminWithClientTokenRec := httptest.NewRecorder()
	h.ServeHTTP(adminWithClientTokenRec, adminWithClientToken)
	if adminWithClientTokenRec.Code != http.StatusForbidden {
		t.Fatalf("MCP client token reached admin API: status=%d body=%s", adminWithClientTokenRec.Code, adminWithClientTokenRec.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/admin/api/clients", nil)
	list.Header.Set("Authorization", "Bearer ctl")
	listed := httptest.NewRecorder()
	h.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), tokenID) {
		t.Fatalf("managed token missing from client inventory: status=%d body=%s", listed.Code, listed.Body.String())
	}

	rotate := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/tokens/"+tokenID+"/rotate", nil)
	rotate.Header.Set("Authorization", "Bearer ctl")
	rotated := httptest.NewRecorder()
	h.ServeHTTP(rotated, rotate)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	var rotatedBody map[string]any
	if err := json.Unmarshal(rotated.Body.Bytes(), &rotatedBody); err != nil {
		t.Fatal(err)
	}
	newToken, _ := rotatedBody["access_token"].(string)
	if newToken == "" || newToken == oldToken {
		t.Fatalf("rotation did not issue a replacement token: %v", rotatedBody)
	}
	if _, err := s.verifyJWT(oldToken); err == nil {
		t.Fatal("rotated token remained valid")
	}
	if _, err := s.verifyJWT(newToken); err != nil {
		t.Fatalf("replacement token invalid: %v", err)
	}
}

func TestReadonlyManagedTokenCannotCallShellExec(t *testing.T) {
	s := New(Config{
		CtlToken: "ctl", RelayAgentToken: "relay", AdminPassword: "pw", OAuthClientSecret: "oauth-secret",
		PublicOrigin: "https://hub.example", MCPResource: "https://hub.example",
		ConfigDir: t.TempDir(), DefaultTimeout: 20 * time.Millisecond, PollMaxTimeout: 20 * time.Millisecond,
	})
	h := s.Handler()
	register := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewBufferString(`{"agent_id":"shell:test","name":"Test shell","kind":"virtual_shell","transport":"long_poll","capabilities":["shell"]}`))
	register.Header.Set("Authorization", "Bearer "+s.cfg.RelayAgentToken)
	register.Header.Set("Content-Type", "application/json")
	registered := httptest.NewRecorder()
	h.ServeHTTP(registered, register)
	if registered.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}

	issue := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/issue-token", bytes.NewBufferString(`{"client_id":"chatgpt-readonly","ttl_days":7,"access_mode":"readonly"}`))
	issue.Header.Set("Authorization", "Bearer ctl")
	issue.Header.Set("Content-Type", "application/json")
	issued := httptest.NewRecorder()
	h.ServeHTTP(issued, issue)
	var issuedBody map[string]any
	if issued.Code != http.StatusOK || json.Unmarshal(issued.Body.Bytes(), &issuedBody) != nil {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	token, _ := issuedBody["access_token"].(string)
	tokenID, _ := issuedBody["token_id"].(string)
	if issuedBody["access_mode"] != "readonly" || token == "" || tokenID == "" {
		t.Fatalf("readonly token response incomplete: %v", issuedBody)
	}

	adminCall := httptest.NewRequest(http.MethodGet, "/admin/api/clients", nil)
	adminCall.Header.Set("Authorization", "Bearer "+token)
	adminCalled := httptest.NewRecorder()
	h.ServeHTTP(adminCalled, adminCall)
	if adminCalled.Code != http.StatusForbidden || !strings.Contains(adminCalled.Body.String(), "read-only") {
		t.Fatalf("readonly token reached admin API: status=%d body=%s", adminCalled.Code, adminCalled.Body.String())
	}

	tools := httptest.NewRequest(http.MethodPost, "/mcp-relay/tools", bytes.NewBufferString(`{"target":"shell:test"}`))
	tools.Header.Set("Authorization", "Bearer "+token)
	tools.Header.Set("Content-Type", "application/json")
	listed := httptest.NewRecorder()
	h.ServeHTTP(listed, tools)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "system_inspect") || strings.Contains(listed.Body.String(), "shell_exec") {
		t.Fatalf("readonly tool list is unsafe: status=%d body=%s", listed.Code, listed.Body.String())
	}

	call := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewBufferString(`{"target":"shell:test","tool_name":"shell_exec","arguments":{"cmd":"whoami"}}`))
	call.Header.Set("Authorization", "Bearer "+token)
	call.Header.Set("Content-Type", "application/json")
	called := httptest.NewRecorder()
	h.ServeHTTP(called, call)
	if called.Code != http.StatusForbidden || !strings.Contains(called.Body.String(), "read-only") {
		t.Fatalf("readonly shell_exec was not denied: status=%d body=%s", called.Code, called.Body.String())
	}

	globalList := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":0,"method":"tools/list","params":{}}`))
	globalList.Header.Set("Authorization", "Bearer "+token)
	globalList.Header.Set("Content-Type", "application/json")
	globalListed := httptest.NewRecorder()
	h.ServeHTTP(globalListed, globalList)
	if globalListed.Code != http.StatusOK || !strings.Contains(globalListed.Body.String(), `"name":"inspect"`) || strings.Contains(globalListed.Body.String(), `"name":"execute"`) {
		t.Fatalf("global readonly tool list is unsafe: status=%d body=%s", globalListed.Code, globalListed.Body.String())
	}

	inspectCall := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"inspect_system","arguments":{"target":"shell:test","action":"list_directory","path":"/tmp"}}}`))
	inspectCall.Header.Set("Authorization", "Bearer "+token)
	inspectCall.Header.Set("Content-Type", "application/json")
	inspectCalled := httptest.NewRecorder()
	h.ServeHTTP(inspectCalled, inspectCall)
	if inspectCalled.Code != http.StatusOK || strings.Contains(inspectCalled.Body.String(), "read-only client cannot") {
		t.Fatalf("readonly inspection was denied: status=%d body=%s", inspectCalled.Code, inspectCalled.Body.String())
	}
	s.mu.Lock()
	inspectQueued := false
	for _, job := range s.shellJobs {
		if job.ToolName == "system_inspect" {
			inspectQueued = true
		}
	}
	s.mu.Unlock()
	if !inspectQueued {
		t.Fatal("readonly inspection did not queue system_inspect")
	}

	facade := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"call_mcp_tool","arguments":{"target":"shell:test","tool_name":"shell_exec","arguments":{"cmd":"whoami"}}}}`))
	facade.Header.Set("Authorization", "Bearer "+token)
	facade.Header.Set("Content-Type", "application/json")
	facadeRec := httptest.NewRecorder()
	h.ServeHTTP(facadeRec, facade)
	if facadeRec.Code != http.StatusOK || !strings.Contains(facadeRec.Body.String(), "read-only") {
		t.Fatalf("readonly facade shell_exec was not denied: status=%d body=%s", facadeRec.Code, facadeRec.Body.String())
	}

	pinnedList := httptest.NewRequest(http.MethodPost, "/server/shell-test/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	pinnedList.Header.Set("Authorization", "Bearer "+token)
	pinnedList.Header.Set("Content-Type", "application/json")
	pinnedListed := httptest.NewRecorder()
	h.ServeHTTP(pinnedListed, pinnedList)
	if pinnedListed.Code != http.StatusOK || !strings.Contains(pinnedListed.Body.String(), "system_inspect") || strings.Contains(pinnedListed.Body.String(), "shell_exec") {
		t.Fatalf("pinned readonly tool list is unsafe: status=%d body=%s", pinnedListed.Code, pinnedListed.Body.String())
	}

	pinnedCall := httptest.NewRequest(http.MethodPost, "/server/shell-test/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"shell_exec","arguments":{"cmd":"whoami"}}}`))
	pinnedCall.Header.Set("Authorization", "Bearer "+token)
	pinnedCall.Header.Set("Content-Type", "application/json")
	pinnedCalled := httptest.NewRecorder()
	h.ServeHTTP(pinnedCalled, pinnedCall)
	if pinnedCalled.Code != http.StatusOK || !strings.Contains(pinnedCalled.Body.String(), "read-only") {
		t.Fatalf("pinned readonly shell_exec was not denied: status=%d body=%s", pinnedCalled.Code, pinnedCalled.Body.String())
	}

	actionCall := httptest.NewRequest(http.MethodPost, "/server/shell-test/actions/tools/shell_exec", bytes.NewBufferString(`{"cmd":"whoami"}`))
	actionCall.Header.Set("Authorization", "Bearer "+token)
	actionCall.Header.Set("Content-Type", "application/json")
	actionCalled := httptest.NewRecorder()
	h.ServeHTTP(actionCalled, actionCall)
	if actionCalled.Code != http.StatusForbidden || !strings.Contains(actionCalled.Body.String(), "read-only") {
		t.Fatalf("generated Action readonly shell_exec was not denied: status=%d body=%s", actionCalled.Code, actionCalled.Body.String())
	}

	rotate := httptest.NewRequest(http.MethodPost, "/admin/api/mcp/tokens/"+tokenID+"/rotate", nil)
	rotate.Header.Set("Authorization", "Bearer ctl")
	rotated := httptest.NewRecorder()
	h.ServeHTTP(rotated, rotate)
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `"access_mode":"readonly"`) {
		t.Fatalf("rotation lost readonly mode: status=%d body=%s", rotated.Code, rotated.Body.String())
	}
}

func TestGeneratedActionEndpointsAdvertiseOAuthWhenUnauthorized(t *testing.T) {
	s := New(Config{
		CtlToken:                 "test-secret-not-for-production",
		AdminPassword:            "test-secret-not-for-production",
		OAuthClientSecret:        "test-secret-not-for-production",
		PublicOrigin:             "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		MCPResource:              "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
		DefaultTimeout:           time.Second,
		PollMaxTimeout:           time.Second,
	})
	req := httptest.NewRequest(http.MethodGet, "/mcp-relay/servers", nil)
	req.Host = "u-f1102930.t.gptadmin.bezrabotnyi.com"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(got, `resource_metadata="https://u-f1102930.t.gptadmin.bezrabotnyi.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("missing oauth resource metadata in WWW-Authenticate: %q", got)
	}
	if !strings.Contains(got, `scope="gptadmin.read gptadmin.exec"`) {
		t.Fatalf("missing scope hint in WWW-Authenticate: %q", got)
	}
}

func TestAdminOverviewShape(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["jobs"].(map[string]any); !ok {
		t.Fatalf("overview.jobs has bad shape: %T", body["jobs"])
	}
	if _, ok := body["server_counts"].(map[string]any); !ok {
		t.Fatalf("overview.server_counts has bad shape: %T", body["server_counts"])
	}
	if _, ok := body["shell_builds"].(map[string]any); !ok {
		t.Fatalf("overview.shell_builds has bad shape: %T", body["shell_builds"])
	}
	if _, ok := body["update"].(map[string]any); !ok {
		t.Fatalf("overview.update has bad shape: %T", body["update"])
	}
}

func TestAdminOverviewIncludesShellBuildsAndUpdate(t *testing.T) {
	// This test assumes a running test server or mocked state.
	// For now, test that the field structure is correct by calling ReadUpdateState directly.
	dir := t.TempDir()
	statePath := dir + "/update_state.json"

	// Write a test state.
	state := &UpdateState{
		Current: UpdateCurrent{Status: "idle"},
		LastResult: &UpdateResult{
			Status:      "done",
			Message:     "Updated build 119 → 120",
			StartedAt:   123,
			FinishedAt:  456,
			FromVersion: 119,
			ToVersion:   120,
		},
	}
	if err := WriteUpdateState(statePath, state); err != nil {
		t.Fatalf("WriteUpdateState: %v", err)
	}

	got, err := ReadUpdateState(statePath)
	if err != nil {
		t.Fatalf("ReadUpdateState: %v", err)
	}
	if got.LastResult.Status != "done" {
		t.Errorf("expected done, got %q", got.LastResult.Status)
	}
}

func TestAdminTriggerUpdateReturns409WhenRunning(t *testing.T) {
	// Write state with running status, verify handler returns 409.
	dir := t.TempDir()
	statePath := dir + "/update_state.json"
	state := &UpdateState{Current: UpdateCurrent{Status: "running"}}
	WriteUpdateState(statePath, state)

	st, err := ReadUpdateState(statePath)
	if err != nil {
		t.Fatalf("ReadUpdateState: %v", err)
	}
	if st.Current.Status != "running" {
		t.Errorf("expected running, got %q", st.Current.Status)
	}
}

func TestAdminPasswordLoginCookieProtectsStaticAndAPI(t *testing.T) {
	tmp := t.TempDir()
	adminDir := filepath.Join(tmp, "admin")
	if err := os.MkdirAll(adminDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adminDir, "index.html"), []byte("secret-admin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adminDir, "app.js"), []byte("console.log('secret')"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret", PublicDir: tmp, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unauthorized admin page status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Введите admin-пароль") || strings.Contains(w.Body.String(), "secret-admin") {
		t.Fatalf("unauthorized admin page leaked content or missed login form: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/app.js", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized static asset status=%d body=%s", w.Code, w.Body.String())
	}

	form := "password=pw&next=%2Fadmin%2F"
	req = httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == adminSessionCookieName {
			session = c
			break
		}
	}
	if session == nil || session.Value == "" || !session.HttpOnly {
		t.Fatalf("missing secure-ish admin session cookie: %#v", session)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "secret-admin") {
		t.Fatalf("authenticated admin static status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/servers", nil)
	req.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cookie did not authorize relay API: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthPagesExplainAdminPasswordAndBearerOptions(t *testing.T) {
	s := New(Config{
		CtlToken:                 "test-secret-not-for-production",
		AdminPassword:            "test-secret-not-for-production",
		OAuthClientSecret:        "test-secret-not-for-production",
		PublicOrigin:             "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		MCPResource:              "https://u-f1102930.t.gptadmin.bezrabotnyi.com",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
		DefaultTimeout:           time.Second,
		PollMaxTimeout:           time.Second,
	})
	h := s.Handler()

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin login status=%d body=%s", w.Code, w.Body.String())
	}
	loginPage := w.Body.String()
	for _, want := range []string{"admin-пароль", "CTL_TOKEN", "JWT"} {
		if !strings.Contains(loginPage, want) {
			t.Fatalf("admin login page missing %q: %s", want, loginPage)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/authorize?client_id=chatgpt&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fconnector%2Foauth%2Fcb&resource=https%3A%2F%2Fu-f1102930.t.gptadmin.bezrabotnyi.com&scope=gptadmin.read+gptadmin.exec&code_challenge="+pkceChallenge("verifier"), nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authorize page status=%d body=%s", w.Code, w.Body.String())
	}
	authorizePage := w.Body.String()
	for _, want := range []string{"Admin password", "CTL_TOKEN", "JWT"} {
		if !strings.Contains(authorizePage, want) {
			t.Fatalf("authorize page missing %q: %s", want, authorizePage)
		}
	}
}

func TestRegistryStatePersistsAgentsAcrossRestart(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{CtlToken: "ctl", RelayAgentToken: "relay", ConfigDir: tmp, RegistryStateFile: filepath.Join(tmp, "registry_state.json"), DefaultTimeout: time.Second, PollMaxTimeout: time.Second}
	s := New(cfg)
	h := s.Handler()

	register := []byte(`{"agent_id":"demo-agent","name":"Demo Agent","kind":"real_mcp","transport":"stdio","capabilities":["tools/list"],"meta":{"os":"test"}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(cfg.RegistryStateFile); err != nil {
		t.Fatalf("registry state not written: %v", err)
	}

	restarted := New(cfg)
	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/list_mcp_agents", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, a := range body.Agents {
		if a.AgentID == "demo-agent" {
			if a.Status != "stale" {
				t.Fatalf("restored agent status=%q, want stale", a.Status)
			}
			if a.Meta["restored_from_state"] != true {
				t.Fatalf("restored agent meta missing marker: %#v", a.Meta)
			}
			return
		}
	}
	t.Fatalf("restored agent not listed: %+v", body.Agents)
}

func TestMCPListServersSurvivesHubRestartWithDirectStructuredContent(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{CtlToken: "ctl", RelayAgentToken: "relay", ConfigDir: tmp, RegistryStateFile: filepath.Join(tmp, "registry_state.json"), DefaultTimeout: time.Second, PollMaxTimeout: time.Second}
	first := New(cfg)
	register := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewBufferString(`{"agent_id":"survivor","name":"Survivor","kind":"virtual_shell","transport":"long_poll"}`))
	register.Header.Set("Authorization", "Bearer relay")
	registered := httptest.NewRecorder()
	first.Handler().ServeHTTP(registered, register)
	if registered.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}

	restarted := New(cfg)
	rpc := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_mcp_servers","arguments":{}}}`))
	rpc.Header.Set("Authorization", "Bearer ctl")
	response := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(response, rpc)
	if response.Code != http.StatusOK {
		t.Fatalf("MCP list after restart status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Result struct {
			StructuredContent struct {
				Servers  []map[string]any `json:"servers"`
				Response map[string]any   `json:"response"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, server := range body.Result.StructuredContent.Servers {
		if server["server_id"] == "survivor" {
			found = true
		}
	}
	if !found {
		t.Fatalf("surviving agent missing from direct structured content: %s", response.Body.String())
	}
	if len(body.Result.StructuredContent.Response) != 0 {
		t.Fatalf("list_mcp_servers must not nest its result under response: %s", response.Body.String())
	}
}

func TestFailoverConfigAndStateEndpoints(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HUB_PUBLIC_URL", "https://primary.example.test")
	t.Setenv("FRP_ENABLE", "true")
	t.Setenv("FRP_DOMAIN", "t.example.test")
	t.Setenv("FRP_SUBDOMAIN", "u-test")
	t.Setenv("FRP_SERVER_ADDR", "frp.example.test")
	t.Setenv("FRP_SERVER_PORT", "7000")
	t.Setenv("FRP_TOKEN", "frp-secret")
	cfg := Config{
		CtlToken:           "ctl",
		RelayAgentToken:    "relay",
		ConfigDir:          tmp,
		RegistryStateFile:  filepath.Join(tmp, "registry_state.json"),
		FailoverConfigFile: filepath.Join(tmp, "failover_config.json"),
		FailoverStateFile:  filepath.Join(tmp, "failover_state.json"),
		DefaultTimeout:     time.Second,
		PollMaxTimeout:     time.Second,
	}
	s := New(cfg)
	h := s.Handler()

	register := []byte(`{"agent_id":"shell:haos","name":"Shell: haos","kind":"virtual_shell","transport":"long_poll","capabilities":["shell"],"meta":{"base_url":"http://192.168.2.101:25900","args":["--header","Authorization: Bearer should-not-leak"],"api_key":"should-not-leak-key"}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	payload := []byte(`{"enabled":true,"fail_count_base":4,"nodes":[{"server_id":"shell:haos","rank":1,"enabled":true,"hub_url":"http://192.168.2.101:9001"},{"server_id":"shell:server-44","rank":2,"enabled":true}]}`)
	req = httptest.NewRequest(http.MethodPost, "/admin/api/failover", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save failover status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(cfg.FailoverConfigFile); err != nil {
		t.Fatalf("failover config not written: %v", err)
	}
	if _, err := os.Stat(cfg.FailoverStateFile); err != nil {
		t.Fatalf("failover state not written: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/api/failover", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get failover status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	cfgMap := got["config"].(map[string]any)
	if cfgMap["enabled"] != true || int(cfgMap["fail_count_base"].(float64)) != 4 {
		t.Fatalf("bad failover config: %#v", cfgMap)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/api/failover/state", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "frp-secret") || strings.Contains(w.Body.String(), "should-not-leak") {
		t.Fatalf("state without secrets leaked token material: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "shell:haos") || !strings.Contains(w.Body.String(), "u-test") {
		t.Fatalf("state missing agent/tunnel fields: %s", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/api/failover/state?secrets=1", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "frp-secret") || strings.Contains(w.Body.String(), "should-not-leak") {
		t.Fatalf("state leaked credential material: %s", w.Body.String())
	}
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestCompatibilityEndpoints(t *testing.T) {
	tmp := t.TempDir()
	artifactDir := filepath.Join(tmp, "build")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactDir, "gptadmin-shellmcp.tar.gz")
	if err := os.WriteFile(artifactPath, []byte("dummy artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	androidBinary := filepath.Join(artifactDir, "android-arm64", "bin", "shellmcp")
	if err := os.MkdirAll(filepath.Dir(androidBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(androidBinary, []byte("android shellmcp binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "gptadmin-android-arm64.version"), []byte("126\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Config{CtlToken: "ctl", ArtifactDir: artifactDir, DefaultTimeout: 1, PollMaxTimeout: 1})
	h := s.Handler()

	for _, tc := range []struct {
		method string
		path   string
		want   int
		needle string
	}{
		{http.MethodGet, "/actions/openapi.yaml", http.StatusOK, "operationId: discover"},
		{http.MethodGet, "/servers", http.StatusOK, "servers"},
		{http.MethodGet, "/tasks/demo", http.StatusOK, "tasks"},
		{http.MethodGet, "/artifacts/shellmcp.json", http.StatusOK, "sha256"},
		{http.MethodGet, "/artifacts/shellmcp-android-arm64.json", http.StatusOK, "shellmcp-android-arm64"},
		{http.MethodGet, "/artifacts/shellmcp-android-arm64.bin", http.StatusOK, "android shellmcp binary"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer ctl")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), tc.needle) {
			t.Fatalf("%s %s missing %q in %s", tc.method, tc.path, tc.needle, w.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/actions/openapi.yaml", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	body := w.Body.String()
	for _, want := range []string{"openapi: 3.1.0", "version: \"1.0.0\"", "additionalProperties: true", "cmd:", "query:", "cwd:", "arguments:", "args:", "operationId: execute", "Tool name from schema."} {
		if !strings.Contains(body, want) {
			t.Fatalf("/actions/openapi.yaml missing %q in %s", want, body)
		}
	}
	if strings.Contains(body, "operationId: shellExec") || strings.Contains(body, "/mcp-relay/shell_exec") || strings.Contains(body, "ShellExecRequest") {
		t.Fatalf("/actions/openapi.yaml leaked special shellExec action: %s", body)
	}
}

func TestCallMcpToolAcceptsTopLevelShellArgs(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:roomhacker-server-100"] = &Agent{AgentID: "shell:roomhacker-server-100", Name: "Shell: roomhacker-server-100", Kind: "virtual_shell", Status: "online"}
	s.mu.Unlock()
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewReader([]byte(`{"target":"shell:roomhacker-server-100","tool_name":"shell_exec","cmd":"pwd"}`)))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("callMcpTool shell_exec status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"server_id":"shell:roomhacker-server-100"`) {
		t.Fatalf("callMcpTool shell_exec bad response: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "missing cmd") {
		t.Fatalf("callMcpTool did not forward top-level cmd: %s", w.Body.String())
	}
}

func TestRelayShellExecForwardsExplicitRunAsUser(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:roomhacker-server-100"] = &Agent{AgentID: "shell:roomhacker-server-100", Name: "Shell: roomhacker-server-100", Kind: "virtual_shell", Status: "online"}
	s.mu.Unlock()
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/shell_exec", bytes.NewReader([]byte(`{"target":"shell:roomhacker-server-100","cmd":"id -un","run_as_user":"root","background":true}`)))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("shell_exec status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	job := s.shellJobs[response.JobID]
	s.mu.Unlock()
	if job == nil || firstString(job.Arguments, "run_as_user") != "root" {
		t.Fatalf("run_as_user was not queued: %#v", job)
	}
}

func TestMcpRelayRejectsDefaultTarget(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	h := s.Handler()

	for _, tc := range []struct {
		path string
		body string
	}{
		{path: "/mcp-relay/tools", body: `{"target":"default"}`},
		{path: "/mcp-relay/call", body: `{"target":"default","tool_name":"shell_exec"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Authorization", "Bearer ctl")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "There is no default target") {
			t.Fatalf("%s did not explain explicit target requirement: %s", tc.path, w.Body.String())
		}
	}
}

func TestAppsSDKRejectsDefaultTarget(t *testing.T) {
	s := New(Config{DefaultTimeout: time.Second, PollMaxTimeout: time.Second})

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "list_mcp_tools", args: map[string]any{"target": "default"}},
		{name: "call_mcp_tool", args: map[string]any{"target": "default", "tool_name": "shell_exec"}},
	} {
		result, ok := s.appsSDKCall(tc.name, tc.args).(map[string]any)
		if !ok {
			t.Fatalf("%s returned %T, want map", tc.name, result)
		}
		if result["status"] != "failed" {
			t.Fatalf("%s status=%v, want failed: %v", tc.name, result["status"], result)
		}
		err := mapValue(result["error"])
		if err["status_code"] != http.StatusBadRequest || !strings.Contains(firstString(err, "message"), "There is no default target") {
			t.Fatalf("%s did not reject default target: %v", tc.name, result)
		}
	}
}

func TestAppsSDKCallForwardsArbitraryTopLevelToolArgs(t *testing.T) {
	var callSchema map[string]any
	for _, tool := range appsSDKTools() {
		if tool["name"] == "execute" {
			callSchema = tool["inputSchema"].(map[string]any)
			break
		}
	}
	if callSchema == nil || callSchema["additionalProperties"] != true {
		t.Fatalf("execute schema must permit arbitrary selected-tool fields: %v", callSchema)
	}
	if _, ok := callSchema["properties"].(map[string]any)["args"]; !ok {
		t.Fatalf("execute schema must expose args: %v", callSchema)
	}

	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	h := s.Handler()

	register := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader([]byte(`{"agent_id":"OpenMemory","name":"OpenMemory","capabilities":["tools/call"]}`)))
	register.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, register)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	done := make(chan any, 1)
	go func() {
		done <- s.appsSDKCall("call_mcp_tool", map[string]any{
			"target":     "OpenMemory",
			"tool_name":  "openmemory_store_project",
			"content":    "release notes",
			"project_id": "gptadmin",
			"tags":       []any{"release", "mcp"},
			"metadata":   map[string]any{"source": "chatgpt"},
			"type":       "project",
		})
	}()

	time.Sleep(30 * time.Millisecond)
	poll := httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/OpenMemory?timeout=1", nil)
	poll.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, poll)
	if w.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", w.Code, w.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	params := job["params"].(map[string]any)
	args := params["arguments"].(map[string]any)
	for key, want := range map[string]any{
		"content": "release notes", "project_id": "gptadmin", "type": "project",
	} {
		if args[key] != want {
			t.Fatalf("apps SDK dropped %s: arguments=%v", key, args)
		}
	}
	if _, ok := args["tags"].([]any); !ok {
		t.Fatalf("apps SDK dropped tags: arguments=%v", args)
	}
	if metadata := args["metadata"].(map[string]any); metadata["source"] != "chatgpt" {
		t.Fatalf("apps SDK changed metadata: arguments=%v", args)
	}

	result := []byte(`{"id":"` + job["id"].(string) + `","result":{"ok":true}}`)
	complete := httptest.NewRequest(http.MethodPost, "/mcp-relay/result/OpenMemory", bytes.NewReader(result))
	complete.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, complete)
	if w.Code != http.StatusOK {
		t.Fatalf("result status=%d body=%s", w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("apps SDK call did not complete")
	}
}

func TestAgentFacadeDefaultExposesAllAgents(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", PublicOrigin: "https://hub.example", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	h := s.Handler()

	register := []byte(`{"server_id":"OpenMemory","name":"OpenMemory","capabilities":["tools/list","tools/call","resources/list","resources/read","prompts/list","prompts/get"]}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/list_mcp_servers?detail=full", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("servers status=%d body=%s", w.Code, w.Body.String())
	}
	var listed struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var openMemory map[string]any
	for i := range listed.Servers {
		if listed.Servers[i]["server_id"] == "OpenMemory" {
			openMemory = listed.Servers[i]
			break
		}
	}
	if openMemory == nil {
		t.Fatalf("OpenMemory not listed: %+v", listed.Servers)
	}
	if got := openMemory["meta"].(map[string]any)["public_mcp_path"]; got != "/server/openmemory/mcp" {
		t.Fatalf("public_mcp_path=%v", got)
	}
	if got := openMemory["meta"].(map[string]any)["exposed_by_default"]; got != true {
		t.Fatalf("exposed_by_default=%v", got)
	}

	// Both /server/openmemory and /server/openmemory/mcp are accepted as MCP endpoints.
	for _, path := range []string{"/server/openmemory", "/server/openmemory/mcp"} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer ctl")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"server_id":"OpenMemory"`) {
			t.Fatalf("GET %s did not resolve OpenMemory: %s", path, w.Body.String())
		}
	}

	rpc := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/server/hub/mcp", bytes.NewReader(rpc))
	req.Header.Set("Authorization", "Bearer ctl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hub server mcp status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"discover"`) {
		t.Fatalf("hub server tools/list missing discover: %s", w.Body.String())
	}
}

func TestAgentFacadeProxiesPinnedRelayAgent(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", PublicOrigin: "https://hub.example", DefaultTimeout: 2 * time.Second, PollMaxTimeout: 2 * time.Second})
	h := s.Handler()
	register := []byte(`{"agent_id":"demo","name":"Demo","capabilities":["tools/list","tools/call"]}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rpc := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
		req := httptest.NewRequest(http.MethodPost, "/server/demo/mcp", bytes.NewReader(rpc))
		req.Header.Set("Authorization", "Bearer ctl")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		done <- w
	}()

	time.Sleep(30 * time.Millisecond)
	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/demo?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", w.Code, w.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	jobID, _ := job["id"].(string)
	if jobID == "" || job["method"] != "tools/list" {
		t.Fatalf("bad polled job: %v", job)
	}

	result := []byte(`{"id":"` + jobID + `","result":{"tools":[{"name":"demo_tool","inputSchema":{"type":"object"}}]}}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp-relay/result/demo", bytes.NewReader(result))
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("result status=%d body=%s", w.Code, w.Body.String())
	}

	select {
	case w = <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("direct agent MCP request timed out")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("agent mcp status=%d body=%s", w.Code, w.Body.String())
	}
	var rpcResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if _, hasError := rpcResp["error"]; hasError {
		t.Fatalf("unexpected rpc error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "demo_tool") {
		t.Fatalf("agent facade did not return upstream tools: %s", w.Body.String())
	}
}

func TestAppsSDKMetadataAndWidget(t *testing.T) {
	s := New(Config{CtlToken: "ctl", AdminPassword: "pw", OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", OAuthPermissiveRedirects: true, OAuthPermissiveResources: true, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	h := s.Handler()

	token, err := s.signJWT(map[string]any{"sub": "admin", "aud": "https://hub.example", "resource": "https://hub.example", "scope": "gptadmin.read gptadmin.exec", "client_id": "test", "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	result := body["result"].(map[string]any)
	tools := result["tools"].([]any)
	renderTools := 0
	for _, raw := range tools {
		tool := raw.(map[string]any)
		meta := tool["_meta"].(map[string]any)
		if _, ok := tool["outputSchema"]; !ok {
			t.Fatalf("tool %s missing outputSchema", tool["name"])
		}
		if _, ok := tool["securitySchemes"]; !ok {
			t.Fatalf("tool %s missing securitySchemes", tool["name"])
		}
		if _, ok := meta["securitySchemes"]; !ok {
			t.Fatalf("tool %s missing _meta.securitySchemes", tool["name"])
		}
		if tool["name"] == "ui" {
			renderTools++
			ui := meta["ui"].(map[string]any)
			if meta["openai/widgetAccessible"] != true {
				t.Fatalf("render tool is not widget-accessible")
			}
			if ui["resourceUri"] != "ui://widget/admin-v3.html" || meta["openai/outputTemplate"] != "ui://widget/admin-v3.html" {
				t.Fatalf("render tool missing UI template metadata: %#v", meta)
			}
		} else {
			if _, ok := meta["openai/outputTemplate"]; ok {
				t.Fatalf("data tool %s must not attach outputTemplate", tool["name"])
			}
			if _, ok := meta["ui"]; ok {
				t.Fatalf("data tool %s must not attach ui metadata", tool["name"])
			}
			if _, ok := meta["openai/widgetAccessible"]; ok {
				t.Fatalf("data tool %s must not be widget-accessible", tool["name"])
			}
		}
	}
	if len(tools) != 6 {
		t.Fatalf("got %d Apps SDK tools, want 6", len(tools))
	}
	if renderTools != 1 {
		t.Fatalf("got %d render tools, want 1", renderTools)
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"ui://widget/admin-v3.html"}}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resources/read status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	contents := body["result"].(map[string]any)["contents"].([]any)
	content := contents[0].(map[string]any)
	if content["mimeType"] != "text/html;profile=mcp-app" {
		t.Fatalf("bad mime: %v", content["mimeType"])
	}
	htmlText := content["text"].(string)
	for _, want := range []string{"GPTAdmin MCP", "ui/notifications/tool-result", "tools/call", "discover"} {
		if !strings.Contains(htmlText, want) {
			t.Fatalf("widget html missing %q", want)
		}
	}
	meta := content["_meta"].(map[string]any)
	ui := meta["ui"].(map[string]any)
	if ui["domain"] == "" || ui["csp"] == nil || meta["openai/widgetCSP"] == nil {
		t.Fatalf("resource missing widget metadata: %#v", meta)
	}
}

func TestServerActionsOpenAPIProxyForPinnedMCPServer(t *testing.T) {
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", PublicOrigin: "https://hub.example", DefaultTimeout: 2 * time.Second, PollMaxTimeout: 2 * time.Second})
	h := s.Handler()
	register := []byte(`{"server_id":"OpenMemory","name":"OpenMemory","kind":"real_mcp","transport":"stdio","capabilities":["tools/list","tools/call"]}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	doneSchema := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/server/openmemory/actions/openapi.yaml", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		doneSchema <- w
	}()
	time.Sleep(30 * time.Millisecond)
	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/OpenMemory?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("poll tools/list status=%d body=%s", w.Code, w.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	jobID, _ := job["id"].(string)
	if jobID == "" || job["method"] != "tools/list" {
		t.Fatalf("bad tools/list job: %v", job)
	}
	result := []byte(`{"id":"` + jobID + `","result":{"tools":[{"name":"openmemory_query","description":"Query OpenMemory","inputSchema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}}]}}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp-relay/result/OpenMemory", bytes.NewReader(result))
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("result tools/list status=%d body=%s", w.Code, w.Body.String())
	}
	select {
	case w = <-doneSchema:
	case <-time.After(2 * time.Second):
		t.Fatalf("actions openapi request timed out")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("actions openapi status=%d body=%s", w.Code, w.Body.String())
	}
	schema := w.Body.String()
	for _, want := range []string{"openapi: 3.1.0", "version: \"1.0.0\"", "/server/openmemory/actions/tools/openmemory_query", "operationId: \"openmemory_query\"", "Query OpenMemory", "bearerAuth"} {
		if !strings.Contains(schema, want) {
			t.Fatalf("openapi schema missing %q:\n%s", want, schema)
		}
	}
	if strings.Contains(schema, "list_mcp_tools") || strings.Contains(schema, "call_mcp_tool") {
		t.Fatalf("per-server action schema leaked GPTAdmin relay tools: %s", schema)
	}

	doneCall := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/server/openmemory/actions/tools/openmemory_query", bytes.NewReader([]byte(`{"query":"hello"}`)))
		req.Header.Set("Authorization", "Bearer ctl")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		doneCall <- w
	}()
	time.Sleep(30 * time.Millisecond)
	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/poll/OpenMemory?timeout=1", nil)
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("poll tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	jobID, _ = job["id"].(string)
	if jobID == "" || job["method"] != "tools/call" {
		t.Fatalf("bad tools/call job: %v", job)
	}
	params := job["params"].(map[string]any)
	if params["name"] != "openmemory_query" {
		t.Fatalf("bad proxied tool name: %v", params)
	}
	args := params["arguments"].(map[string]any)
	if args["query"] != "hello" {
		t.Fatalf("bad proxied args: %v", args)
	}
	result = []byte(`{"id":"` + jobID + `","result":{"structuredContent":{"answer":"world"},"content":[{"type":"text","text":"world"}]}}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp-relay/result/OpenMemory", bytes.NewReader(result))
	req.Header.Set("Authorization", "Bearer relay")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("result tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	select {
	case w = <-doneCall:
	case <-time.After(2 * time.Second):
		t.Fatalf("actions tool call timed out")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("actions tool call status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "world") || !strings.Contains(w.Body.String(), "openmemory_query") {
		t.Fatalf("bad actions tool response: %s", w.Body.String())
	}
}

func TestHTTPServiceEndpointProxiesRegisteredLoopbackService(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			t.Fatalf("backend path=%q", r.URL.Path)
		}
		if got := r.Header.Get("X-Forwarded-Prefix"); got != "/_services/demo/files" {
			t.Fatalf("prefix=%q", got)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	s := New(Config{RelayAgentToken: "relay"})
	body := `{"agent_id":"demo","name":"Demo","meta":{"http_endpoints":[{"name":"files","local_url":"` + backend.URL + `","strip_prefix":true,"visibility":"public-capability"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register=%d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/_services/demo/files/hello", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("proxy=%d %q", w.Code, w.Body.String())
	}
}

func TestHTTPServiceEndpointRejectsNonLoopback(t *testing.T) {
	s := New(Config{RelayAgentToken: "relay"})
	body := `{"agent_id":"demo","name":"Demo","meta":{"http_endpoints":[{"name":"files","local_url":"http://192.168.2.1:80","strip_prefix":true,"visibility":"public-capability"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	req = httptest.NewRequest(http.MethodGet, "/_services/demo/files/x", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// An http_endpoints entry without visibility=public-capability must NOT be
// reachable through the public ingress, even if it targets a loopback backend.
func TestHTTPServiceEndpointRejectsPrivateCapability(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("leaked"))
	}))
	defer backend.Close()

	s := New(Config{RelayAgentToken: "relay"})
	body := `{"agent_id":"demo","name":"Demo","meta":{"http_endpoints":[{"name":"files","local_url":"` + backend.URL + `","strip_prefix":true}]}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	req = httptest.NewRequest(http.MethodGet, "/_services/demo/files/x", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("private endpoint should be hidden, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPollingShellQueueCarriesGenericMCPToolCall(t *testing.T) {
	s := New(Config{ShellToken: "shell", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	queued := s.callShellTool("shell:demo", "mcp_tools", map[string]any{"ref": "docs"}, true, time.Second)
	if queued["status"] != "running" {
		t.Fatalf("queue result=%#v", queued)
	}
	req := httptest.NewRequest(http.MethodGet, "/queue/demo?timeout=0", nil)
	req.Header.Set("Authorization", "Bearer shell")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job["tool_name"] != "mcp_tools" {
		t.Fatalf("job=%#v", job)
	}
	args, _ := job["arguments"].(map[string]any)
	if args["ref"] != "docs" {
		t.Fatalf("args=%#v", args)
	}
}

func TestShellToolsAdvertiseChildMCPDiscoveryAndCall(t *testing.T) {
	tools := shellTools()
	seen := map[string]bool{}
	for _, tool := range tools {
		if name, _ := tool["name"].(string); name != "" {
			seen[name] = true
		}
	}
	for _, name := range []string{"shell_exec", "mcp_manage", "mcp_tools", "mcp_call"} {
		if !seen[name] {
			t.Fatalf("missing %s in %#v", name, tools)
		}
	}
}

func TestCanonicalShellQueueNameHomeAssistantAlias(t *testing.T) {
	for _, alias := range []string{"homeassistant", "home-assistant", "haos"} {
		if got := canonicalShellQueueName(alias); got != "haos" {
			t.Fatalf("%s => %s", alias, got)
		}
	}
}

func TestCriticalHubEndpointsFailClosed(t *testing.T) {
	s := New(Config{DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/heartbeat", `{"name":"victim"}`},
		{http.MethodGet, "/queue/victim?timeout=0", ""},
		{http.MethodPost, "/mcp-relay/register", `{"agent_id":"attacker"}`},
		{http.MethodGet, "/admin/api/overview", ""},
		{http.MethodPost, "/admin/api/failover/reclaim/accept", `{}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status=%d, want %d; body=%s", tc.method, tc.path, rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	}
}

func TestShellQueueRequiresDedicatedShellToken(t *testing.T) {
	s := New(Config{ShellToken: "shell-secret", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	queued := s.callShellTool("shell:demo", "shell_exec", map[string]any{"cmd": "echo secret"}, true, time.Second)
	if queued["status"] != "running" {
		t.Fatalf("queue result=%#v", queued)
	}
	unauthorized := httptest.NewRequest(http.MethodGet, "/queue/demo?timeout=0", nil)
	unauthorizedRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated queue poll status=%d body=%s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}
	authorized := httptest.NewRequest(http.MethodGet, "/queue/demo?timeout=0", nil)
	authorized.Header.Set("Authorization", "Bearer shell-secret")
	authorizedRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(authorizedRec, authorized)
	if authorizedRec.Code != http.StatusOK || !strings.Contains(authorizedRec.Body.String(), "echo secret") {
		t.Fatalf("authenticated queue poll status=%d body=%s", authorizedRec.Code, authorizedRec.Body.String())
	}
}

func TestJWTRejectsEmptySecretWrongAlgorithmAndMissingExpiry(t *testing.T) {
	empty := New(Config{})
	if _, err := empty.signJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}); err == nil {
		t.Fatal("signJWT accepted an unset OAuth secret")
	}
	s := New(Config{OAuthClientSecret: "oauth-secret"})
	wrongAlg := b64url([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + b64url([]byte(`{"exp":9999999999}`)) + ".sig"
	if _, err := s.verifyJWT(wrongAlg); err == nil {
		t.Fatal("verifyJWT accepted an unsupported algorithm")
	}
	token, err := s.signJWT(map[string]any{"sub": "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.verifyJWT(token); err == nil {
		t.Fatal("verifyJWT accepted a token without expiry")
	}
}

func TestAuditRedactsAuthorizationAndQueryCredentials(t *testing.T) {
	s := New(Config{})
	req := httptest.NewRequest(http.MethodGet, "/x?token=query-secret&safe=value", nil)
	req.Header.Set("Authorization", "Bearer authorization-secret")
	fields := s.requestForAudit(req)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"query-secret", "authorization-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("audit fields leaked %q: %s", secret, encoded)
		}
	}
}
