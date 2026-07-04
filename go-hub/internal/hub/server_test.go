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
)

func TestListAgentsUsesHubKind(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	r := httptest.NewRequest(http.MethodGet, "/mcp-relay/list_mcp_agents", nil)
	r.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) == 0 {
		t.Fatalf("no agents in response")
	}
	if got := body.Agents[0].Kind; got != "hub" {
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
	if !bytes.Contains(w.Body.Bytes(), []byte("list_mcp_agents")) {
		t.Fatalf("tools/list missing expected tool: %s", w.Body.String())
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
	if _, ok := body["agent_counts"].(map[string]any); !ok {
		t.Fatalf("overview.agent_counts has bad shape: %T", body["agent_counts"])
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
		{http.MethodGet, "/actions/openapi.yaml", http.StatusOK, "operationId: listMcpAgents"},
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
