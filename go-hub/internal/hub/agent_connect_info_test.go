package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func connectInfoServer(t *testing.T, shellToken string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GPTADMIN_AUTH_MODE", "legacy")
	t.Setenv("GPTADMIN_CONFIG_DIR", dir)
	cfg := FromEnv()
	cfg.CtlToken = "fixture-owner"
	cfg.OAuthClientSecret = "fixture-signing"
	cfg.PublicOrigin = "https://hub.example"
	cfg.MCPResource = "https://hub.example/mcp"
	cfg.ShellToken = shellToken
	cfg.ExistingMCPBearers = nil
	s := New(cfg)
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterLocalExecutor("fixture-node", map[string]string{"server_id": "fixture-identity", "public_key": "fixture-public", "fingerprint": "fixture-fingerprint"}, strings.Repeat("q", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	return s
}

// The connect-info endpoint hands the operator the exact code a new shell MCP
// agent needs; the shared agent bearer must never leak without admin authority.
func TestAgentConnectInfo(t *testing.T) {
	s := connectInfoServer(t, "agent-shared-secret")

	unauthorized := httptest.NewRequest(http.MethodGet, "/admin/api/agents/connect-info", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, unauthorized)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous connect-info must fail: %d %s", rec.Code, rec.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/api/agents/connect-info", nil)
	request.Header.Set("Authorization", "Bearer fixture-owner")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("ctl connect-info failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("connect-info must be no-store")
	}
	var payload struct {
		HubURL         string `json:"hub_url"`
		McpEndpoint    string `json:"mcp_endpoint"`
		ShellToken     string `json:"shell_token"`
		Transport      string `json:"transport"`
		InstallCommand string `json:"install_command"`
		EnvBlock       string `json:"env_block"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("connect-info malformed: %v", err)
	}
	if payload.HubURL != "https://hub.example" || payload.McpEndpoint != "https://hub.example/mcp" {
		t.Fatalf("connect-info origin wrong: %v", payload)
	}
	if payload.ShellToken != "agent-shared-secret" {
		t.Fatalf("connect-info token wrong: %q", payload.ShellToken)
	}
	for _, required := range []string{"HUB_URL=https://hub.example", "SHELL_TOKEN=agent-shared-secret", "SHELL_NAME=", "SHELLMCP_TRANSPORT=polling"} {
		if !strings.Contains(payload.EnvBlock, required) {
			t.Fatalf("env block missing %q: %q", required, payload.EnvBlock)
		}
	}
	if payload.InstallCommand == "" || payload.Transport != "polling" {
		t.Fatalf("connect-info incomplete: %v", payload)
	}
}

// Hubs that authenticate agents per executor have no shared token to reveal;
// the endpoint must report the gap instead of an unusable snippet.
func TestAgentConnectInfoWithoutSharedToken(t *testing.T) {
	s := connectInfoServer(t, "")
	request := httptest.NewRequest(http.MethodGet, "/admin/api/agents/connect-info", nil)
	request.Header.Set("Authorization", "Bearer fixture-owner")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request)
	if rec.Code != http.StatusConflict {
		t.Fatalf("empty shell token must report a conflict: %d %s", rec.Code, rec.Body.String())
	}
}
