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

	call := []byte(`{"target":"demo","tool_name":"ping","arguments":{"x":1},"background":true}`)
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

	register := []byte(`{"agent_id":"shell:haos","name":"Shell: haos","kind":"virtual_shell","transport":"long_poll","capabilities":["shell"],"meta":{"base_url":"http://203.0.113.10:25900"}}`)
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
	if strings.Contains(w.Body.String(), "frp-secret") {
		t.Fatalf("state without secrets leaked FRP token: %s", w.Body.String())
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
