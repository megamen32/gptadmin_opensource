package hub

import (
	"bytes"
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

type blockedAuthResponse struct {
	http.ResponseWriter
	entered chan struct{}
	release chan struct{}
}

func (w *blockedAuthResponse) Write(body []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(body), nil
}

// Local lock-boundary regression, not a network integration claim. The real
// idle relay poll/admission regression is in tests/e2e/node/auth_run.py.
func TestAuthSnapshotResponseDoesNotHoldLocks(t *testing.T) {
	s := authContinuityNode(t, "writer", "")
	w := &blockedAuthResponse{ResponseWriter: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("POST", "/admin/api/auth-snapshot/export", nil)
	r.Header.Set("Authorization", "Bearer fixture-owner")
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(w, r); close(done) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not reach response write")
	}
	if !s.authMu.TryLock() {
		t.Fatal("export holds auth gate while response write is blocked")
	}
	s.authMu.Unlock()
	if !s.mu.TryLock() {
		t.Fatal("export holds hub mutex while response write is blocked")
	}
	s.mu.Unlock()
}

func authContinuityNode(t *testing.T, mode, source string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GPTADMIN_AUTH_MODE", mode)
	t.Setenv("GPTADMIN_AUTH_SOURCE_ID", source)
	t.Setenv("GPTADMIN_CONFIG_DIR", dir)
	cfg := FromEnv()
	cfg.CtlToken = "fixture-owner"
	cfg.OAuthClientSecret = "fixture-signing"
	cfg.PublicOrigin = "https://hub.example"
	cfg.MCPResource = "https://hub.example/mcp"
	cfg.ExistingMCPBearers = nil
	s := New(cfg)
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterLocalExecutor(mode, map[string]string{"server_id": mode + "-identity", "public_key": mode + "-public", "fingerprint": mode + "-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	return s
}

func authContinuityHTTP(s *Server, method, path, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAuthContinuitySnapshotManagedRevocation(t *testing.T) {
	a := authContinuityNode(t, "writer", "")
	b := authContinuityNode(t, "reader", "writer-identity")
	token, record, err := a.issueManagedMCPToken("ordinary", 7, a.cfg.PublicOrigin, a.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	export := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if export.Code != 200 {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	if apply := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", export.Body.Bytes()); apply.Code != 200 {
		t.Fatalf("apply: %d %s", apply.Code, apply.Body.String())
	}
	if got := authContinuityHTTP(b, "GET", "/mcp", token, nil); got.Code != 200 {
		t.Fatalf("replicated ordinary token rejected: %d %s", got.Code, got.Body.String())
	}
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/export", token, nil); got.Code < 400 {
		t.Fatal("ordinary client can export auth")
	}
	if got := authContinuityHTTP(b, "POST", "/register", "", []byte(`{"redirect_uris":["http://127.0.0.1/callback"]}`)); got.Code < 400 {
		t.Fatal("reader registered a client")
	}
	if _, _, err := b.issueManagedMCPToken("forbidden", 7, b.cfg.PublicOrigin, b.cfg.MCPResource); err == nil {
		t.Fatal("common reader mutation was permitted")
	}
	if got := authContinuityHTTP(a, "DELETE", "/admin/api/clients/"+record.ID, "fixture-owner", nil); got.Code != 200 {
		t.Fatal(got.Code)
	}
	next := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if next.Code != 200 {
		t.Fatal(next.Body.String())
	}
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", next.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := authContinuityHTTP(b, "GET", "/mcp", token, nil); got.Code != 401 {
		t.Fatalf("revoked token: %d", got.Code)
	}
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", export.Body.Bytes()); got.Code < 400 {
		t.Fatal("stale snapshot resurrected revoked token")
	}
	var foreign map[string]any
	if err := json.Unmarshal(next.Body.Bytes(), &foreign); err != nil {
		t.Fatal(err)
	}
	foreign["writer_id"] = "foreign"
	foreign["generation"] = float64(1000)
	raw, _ := json.Marshal(foreign)
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", raw); got.Code < 400 {
		t.Fatal("foreign writer accepted")
	}
}

func TestAuthContinuityBudgetAndManifestFailClosed(t *testing.T) {
	a := authContinuityNode(t, "writer", "")
	b := authContinuityNode(t, "reader", "writer-identity")
	token, _, err := a.issueManagedMCPToken("ordinary", 7, a.cfg.PublicOrigin, a.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	export := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if export.Code != 200 {
		t.Fatal(export.Body.String())
	}
	before := b.authState
	large := bytes.Repeat([]byte(" "), b.cfg.AuthSnapshotBudget+1)
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", large); got.Code != 413 || b.authState != before {
		t.Fatal("oversize apply changed state")
	}
	var missing map[string]json.RawMessage
	_ = json.Unmarshal(export.Body.Bytes(), &missing)
	delete(missing, "profiles")
	raw, _ := json.Marshal(missing)
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", raw); got.Code != 400 || b.authState != before {
		t.Fatal("missing optional manifest accepted")
	}
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", export.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if err := os.Remove(b.managedMCPStatePath()); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(b, "GET", "/mcp", token, nil); got.Code != 503 {
		t.Fatalf("missing required store allowed admission: %d", got.Code)
	}
	before = a.authState
	a.cfg.AuthSnapshotBudget = 128
	if got := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil); got.Code < 400 || a.authState != before {
		t.Fatal("oversize export changed state")
	}
	if _, ok := a.verifyManagedMCPToken(token); !ok {
		t.Fatal("oversize export truncated credential")
	}
}

func TestAuthContinuityInterruptedApplyAndSymlinkRecovery(t *testing.T) {
	a := authContinuityNode(t, "writer", "")
	b := authContinuityNode(t, "reader", "writer-identity")
	export := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", export.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	next := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	dir := b.authSlotDir(1 - b.authState.Slot)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "mcp_tokens_state.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", next.Body.Bytes()); got.Code < 400 {
		t.Fatal("symlink apply succeeded")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "unchanged" {
		t.Fatal("apply followed symlink")
	}
	if got := authContinuityHTTP(b, "GET", "/mcp", "fixture-owner", nil); got.Code != 503 {
		t.Fatal("incomplete apply admitted a request")
	}
	cfg := b.cfg
	b.Close()
	restarted := New(cfg)
	defer restarted.Close()
	if err := restarted.RegisterLocalExecutor("reader", map[string]string{"server_id": "reader-identity", "public_key": "reader-public", "fingerprint": "reader-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(restarted, "GET", "/mcp", "fixture-owner", nil); got.Code != 503 {
		t.Fatal("restart fell back to older permissive generation")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(restarted, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", next.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := authContinuityHTTP(restarted, "GET", "/mcp", "fixture-owner", nil); got.Code != 200 {
		t.Fatalf("recovered generation unavailable: %d %s", got.Code, got.Body.String())
	}
}

func TestAuthContinuityReaderCannotMutateProfilesThroughMCP(t *testing.T) {
	a := authContinuityNode(t, "writer", "")
	b := authContinuityNode(t, "reader", "writer-identity")
	_, err := a.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
		profiles["policy"] = AccessProfile{ID: "policy", Name: "policy", AccessMode: "full", ApprovalMode: "unrestricted", AllowedTargets: []string{"*"}, AllowedTools: []string{"*"}, Version: 1}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	export := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if export.Code != 200 {
		t.Fatal(export.Body.String())
	}
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", export.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	before, _ := os.ReadFile(b.accessProfilesStatePath())
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute","arguments":{"target":"hub","tool":"access_profiles","args":{"action":"create","id":"forbidden","profile":{"access_mode":"full","allowed_targets":["*"],"allowed_tools":["*"]}}}}}`)
	got := authContinuityHTTP(b, "POST", "/mcp", "fixture-owner", body)
	after, _ := os.ReadFile(b.accessProfilesStatePath())
	if !bytes.Equal(before, after) || !strings.Contains(got.Body.String(), "reader") {
		t.Fatalf("MCP reader mutation not rejected: %d %s", got.Code, got.Body.String())
	}
}

func TestAuthContinuityBootstrapRejectsMalformedLegacyStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp_tokens_state.json"), []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{ConfigDir: dir, AuthMode: "writer", CtlToken: "owner", OAuthClientSecret: "signer"})
	defer s.Close()
	if err := s.RegisterLocalExecutor("writer", map[string]string{"server_id": "writer-id", "public_key": "public", "fingerprint": "fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err == nil {
		t.Fatal("malformed existing authority silently replaced with empty snapshot")
	}
}

func TestAuthContinuityBasicRefreshAndOfflineAccess(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), OAuthClientSecret: "test-signing", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example/mcp"})
	defer s.Close()
	scope := "gptadmin.read gptadmin.exec offline_access"
	token, _, err := s.issueOAuthRefreshToken("native-client", s.cfg.MCPResource, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"other-client", ""} {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "resource": {s.cfg.MCPResource}}
		if client != "" {
			form.Set("client_id", client)
		}
		r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetBasicAuth("native-client", "native-secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if client != "" {
			if w.Code != http.StatusBadRequest {
				t.Fatalf("conflicting Basic/form client accepted: %d", w.Code)
			}
		} else if w.Code != http.StatusOK {
			t.Fatalf("Basic-only refresh rejected: %d %s", w.Code, w.Body.String())
		}
	}
	access, err := s.issueOAuthAccessToken("native-client", s.cfg.MCPResource, scope)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("issued offline_access JWT cannot authenticate MCP: %d %s", w.Code, w.Body.String())
	}
}
