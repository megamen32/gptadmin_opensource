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
	if !bytes.Contains(w.Body.Bytes(), []byte("list_mcp_servers")) {
		t.Fatalf("tools/list missing expected server tool: %s", w.Body.String())
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

	register := []byte(`{"agent_id":"shell:haos","name":"Shell: haos","kind":"virtual_shell","transport":"long_poll","capabilities":["shell"],"meta":{"base_url":"http://203.0.113.10:25900","args":["--header","Authorization: Bearer should-not-leak"],"api_key":"should-not-leak-key"}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/register", bytes.NewReader(register))
	req.Header.Set("Authorization", "Bearer relay")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}

	payload := []byte(`{"enabled":true,"fail_count_base":4,"nodes":[{"server_id":"shell:haos","rank":1,"enabled":true,"hub_url":"http://203.0.113.10:9001"},{"server_id":"shell:server-01","rank":2,"enabled":true}]}`)
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
		t.Fatalf("state secrets status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "frp-secret") {
		t.Fatalf("state with secrets missing FRP token: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "should-not-leak") {
		t.Fatalf("state with secrets leaked agent meta token material: %s", w.Body.String())
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
	s := New(Config{CtlToken: "ctl", ArtifactDir: artifactDir, DefaultTimeout: 1, PollMaxTimeout: 1})
	h := s.Handler()

	for _, tc := range []struct {
		method string
		path   string
		want   int
		needle string
	}{
		{http.MethodGet, "/actions/openapi.yaml", http.StatusOK, "operationId: listMcpServers"},
		{http.MethodGet, "/servers", http.StatusOK, "servers"},
		{http.MethodGet, "/tasks/demo", http.StatusOK, "tasks"},
		{http.MethodGet, "/artifacts/shellmcp.json", http.StatusOK, "sha256"},
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
	for _, want := range []string{"openapi: 3.1.0", "version: \"1.0.0\"", "additionalProperties: true", "cmd:", "query:", "cwd:", "arguments:", "common tool fields can also be sent as top-level fields", "args:", "Short alias for arguments."} {
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
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewReader([]byte(`{"target":"shell:admin-server-100","tool_name":"shell_exec","cmd":"pwd"}`)))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("callMcpTool shell_exec status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"server_id":"shell:admin-server-100"`) {
		t.Fatalf("callMcpTool shell_exec bad response: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "missing cmd") {
		t.Fatalf("callMcpTool did not forward top-level cmd: %s", w.Body.String())
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

	req = httptest.NewRequest(http.MethodGet, "/mcp-relay/list_mcp_servers", nil)
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
	if !strings.Contains(w.Body.String(), "list_mcp_servers") {
		t.Fatalf("hub server tools/list missing list_mcp_servers: %s", w.Body.String())
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

	token, err := s.signJWT(map[string]any{"sub": "admin", "aud": "https://hub.example", "resource": "https://hub.example", "scope": "gptadmin.read gptadmin.exec", "client_id": "test"})
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
		if tool["name"] == "render_gptadmin_dashboard" {
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
	if len(tools) != 5 {
		t.Fatalf("got %d Apps SDK tools, want 5", len(tools))
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
	for _, want := range []string{"GPTAdmin MCP", "ui/notifications/tool-result", "tools/call", "list_mcp_servers"} {
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
