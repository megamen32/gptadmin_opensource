package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/gofrs/flock"
)

func sharedAccessPair(t *testing.T) (*Server, *Server) {
	t.Helper()
	cfg := Config{ConfigDir: t.TempDir(), CtlToken: "fixture-owner", PublicOrigin: "https://hub.example", OAuthClientSecret: "fixture-signing"}
	a, b := New(cfg), New(cfg)
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
func sharedAccessCall(t *testing.T, s *Server, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestSharedAccessIssueRevokeAcrossLiveHubs(t *testing.T) {
	a, b := sharedAccessPair(t)
	token, record, err := a.issueManagedMCPToken("first", 7, a.cfg.PublicOrigin, a.cfg.PublicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.verifyManagedMCPToken(token); !ok {
		t.Fatal("live standby rejects newly issued token")
	}
	if w := sharedAccessCall(t, b, "fixture-owner", http.MethodDelete, "/admin/api/clients/"+record.ID, nil); w.Code != 200 {
		t.Fatalf("standby revoke status=%d", w.Code)
	}
	if _, ok := a.verifyManagedMCPToken(token); ok {
		t.Fatal("primary still accepts revoked token")
	}
}
func TestSharedAccessConcurrentIssuancePreservesAllRecords(t *testing.T) {
	a, b := sharedAccessPair(t)
	const count = 12
	tokens := make(chan string, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			token, _, err := s.issueManagedMCPToken("parallel", 7, s.cfg.PublicOrigin, s.cfg.PublicOrigin)
			if err != nil {
				errs <- err
				return
			}
			tokens <- token
		}(i)
	}
	wg.Wait()
	close(tokens)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	restored := New(a.cfg)
	defer restored.Close()
	for token := range tokens {
		if _, ok := restored.verifyManagedMCPToken(token); !ok {
			t.Fatal("parallel issuer overwrote another token")
		}
	}
}
func TestSharedAccessBindingAndProfileEditAreImmediate(t *testing.T) {
	a, b := sharedAccessPair(t)
	token, record, err := a.issueManagedMCPToken("operator", 7, a.cfg.PublicOrigin, a.cfg.PublicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	profile, status := accessToolTest(t, b, "fixture-owner", "access_profiles", map[string]any{"action": "create", "id": "policy", "profile": map[string]any{"access_mode": "full", "approval_mode": "unrestricted", "allowed_targets": []string{"*"}, "allowed_tools": []string{"*"}}})
	if status != 200 {
		t.Fatalf("profile status=%d", status)
	}
	_, status = accessToolTest(t, b, "fixture-owner", "access_clients", map[string]any{"action": "bind", "id": record.ID, "profile_id": "policy"})
	if status != 200 {
		t.Fatalf("bind status=%d", status)
	}
	_, status = accessToolTest(t, b, "fixture-owner", "access_profiles", map[string]any{"action": "update", "id": "policy", "etag": profile["etag"], "profile": map[string]any{"access_mode": "readonly", "allowed_targets": []string{"*"}, "allowed_tools": []string{"*"}}})
	if status != 200 {
		t.Fatalf("profile update status=%d", status)
	}
	claims, ok := a.verifyManagedMCPToken(token)
	if !ok {
		t.Fatal("token no longer valid")
	}
	r := requestWithAuthClaims(httptest.NewRequest("GET", "/mcp", nil), claims)
	r = a.applyAccessProfileContext(r, claims)
	if requestAccessMode(r) != "readonly" {
		t.Fatal("primary uses stale full profile after binding/edit on standby")
	}
}
func TestSharedAccessDelegatedAdminAndTokenReadAfterRestart(t *testing.T) {
	a, b := sharedAccessPair(t)
	issued, status := accessToolTest(t, a, "fixture-owner", "access_clients", map[string]any{"action": "issue", "client_id": "delegated-ai", "role": "admin", "access_mode": "full", "ttl_days": 7})
	if status != 200 {
		t.Fatalf("admin issue status=%d", status)
	}
	token, id := firstString(issued, "access_token"), firstString(issued, "token_id")
	if _, status = accessToolTest(t, b, token, "access_profiles", map[string]any{"action": "list"}); status != 200 {
		t.Fatalf("delegated admin cannot manage through MCP: %d", status)
	}
	restored := New(a.cfg)
	defer restored.Close()
	value, status := accessToolTest(t, restored, token, "access_clients", map[string]any{"action": "token", "id": id})
	if status != 200 || value["access_token"] != token {
		t.Fatalf("owner token read failed after restart: status=%d", status)
	}
	inventory, status := accessToolTest(t, b, token, "access_clients", map[string]any{"action": "list"})
	data, _ := json.Marshal(inventory)
	if status != 200 || bytes.Contains(data, []byte(token)) {
		t.Fatal("inventory unavailable or leaks token values")
	}
	ordinary, _, err := a.issueManagedMCPToken("ordinary", 7, a.cfg.PublicOrigin, a.cfg.PublicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if _, status = accessToolTest(t, b, ordinary, "access_clients", map[string]any{"action": "token", "id": id}); status != 403 {
		t.Fatalf("ordinary operator can reveal credentials: %d", status)
	}
}
func TestSharedAccessRefreshTokenCannotActAsBearer(t *testing.T) {
	a, b := sharedAccessPair(t)
	token, _, err := a.issueOAuthRefreshToken("client", a.cfg.PublicOrigin, "gptadmin.read gptadmin.exec")
	if err != nil {
		t.Fatal(err)
	}
	if w := sharedAccessCall(t, b, token, "GET", "/mcp-relay/servers", nil); w.Code < 400 {
		t.Fatal("refresh credential accepted as execution bearer")
	}
}
func TestSharedAccessFailedTokenWriteDoesNotPublish(t *testing.T) {
	a, _ := sharedAccessPair(t)
	path := a.managedMCPStatePath()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.issueManagedMCPToken("failure", 7, a.cfg.PublicOrigin, a.cfg.PublicOrigin)
	if err == nil {
		t.Fatal("expected write failure")
	}
	if len(a.managedMCP) != 0 {
		t.Fatal("failed token creation remained in authoritative memory")
	}
	if token != "" {
		t.Fatal("unsaved token returned as issued")
	}
}

func TestSharedAccessStaleBindingCannotUndoRevocation(t *testing.T) {
	a, b := sharedAccessPair(t)
	token, record, err := a.issueManagedMCPToken("stale", 7, a.cfg.PublicOrigin, a.cfg.PublicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	if err = b.refreshManagedMCPStateLocked(); err != nil {
		t.Fatal(err)
	}
	b.mu.Unlock()
	if w := sharedAccessCall(t, a, "fixture-owner", "DELETE", "/admin/api/clients/"+record.ID, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	b.mu.Lock()
	stale := b.managedMCP[record.ID]
	stale.ProfileID = "stale-binding"
	b.managedMCP[record.ID] = stale
	err = b.saveManagedMCPStateLocked()
	b.mu.Unlock()
	if err == nil {
		t.Fatal("stale binding silently undid revocation")
	}
	if _, ok := b.verifyManagedMCPToken(token); ok {
		t.Fatal("revoked token resurrected")
	}
}
func TestSharedAccessRoleDowngradeTakesEffectWithoutRestart(t *testing.T) {
	a, b := sharedAccessPair(t)
	issued, status := accessToolTest(t, a, "fixture-owner", "access_clients", map[string]any{"action": "issue", "client_id": "admin", "role": "admin", "access_mode": "full"})
	if status != 200 {
		t.Fatal(status)
	}
	token, id := firstString(issued, "access_token"), firstString(issued, "token_id")
	if _, status = accessToolTest(t, b, token, "access_clients", map[string]any{"action": "list"}); status != 200 {
		t.Fatal(status)
	}
	if _, status = accessToolTest(t, a, "fixture-owner", "access_clients", map[string]any{"action": "set_role", "id": id, "role": "client"}); status != 200 {
		t.Fatal(status)
	}
	if _, status = accessToolTest(t, b, token, "access_clients", map[string]any{"action": "list"}); status != 403 {
		t.Fatalf("revoked admin authority cached: %d", status)
	}
}
func TestSharedAccessRotationIsAtomicAndPreservesNonExpiringRole(t *testing.T) {
	a, b := sharedAccessPair(t)
	token, record, err := a.issueManagedMCPTokenWithMode("rotate", 0, a.cfg.PublicOrigin, a.cfg.PublicOrigin, "full", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	// Hold the shared state lock so the save path hits "access state busy".
	// A directory in place of the lock file only blocks gofrs on Linux:
	// darwin opens directories O_RDONLY and flocks them without error.
	stateLock := flock.New(a.managedMCPStatePath() + ".lock")
	held, err := stateLock.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("shared state lock already held")
	}
	response := sharedAccessCall(t, b, "fixture-owner", "POST", "/admin/api/mcp/tokens/"+record.ID+"/rotate", nil)
	if response.Code < 400 {
		t.Fatal("failed rotation was acknowledged")
	}
	if _, ok := a.verifyManagedMCPToken(token); !ok {
		t.Fatal("failed rotation destroyed original token")
	}
	if len(a.managedMCP) != 1 {
		t.Fatal("failed rotation published a second token")
	}
	if err = stateLock.Close(); err != nil {
		t.Fatal(err)
	}
	response = sharedAccessCall(t, b, "fixture-owner", "POST", "/admin/api/mcp/tokens/"+record.ID+"/rotate", nil)
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	var out map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.verifyManagedMCPToken(token); ok {
		t.Fatal("old token still accepted after successful rotation")
	}
	next := firstString(out, "access_token")
	claims, ok := a.verifyManagedMCPToken(next)
	if !ok {
		t.Fatal("replacement rejected")
	}
	if intFromAny(claims["exp"]) != 0 {
		t.Fatal("rotation added an expiry to a non-expiring token")
	}
	if _, status := accessToolTest(t, a, next, "access_profiles", map[string]any{"action": "list"}); status != 200 {
		t.Fatalf("rotation lost admin role: %d", status)
	}
}
func TestSharedAccessExpiredRefreshCannotAuthenticateAsBearerWithRelaxedChecks(t *testing.T) {
	a, _ := sharedAccessPair(t)
	a.cfg.DebugLowSecurity = true
	a.cfg.RelaxAuthChecks = true
	token, record, err := a.issueOAuthRefreshToken("refresh", a.cfg.PublicOrigin, "gptadmin.read gptadmin.exec")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.existingMCPBearerClaims(token); ok {
		t.Fatal("OAuth refresh secret accepted as configured execution bearer")
	}
	record.RevokedAt = 1
	a.mu.Lock()
	a.managedMCP[record.ID] = record
	err = a.saveManagedMCPStateLocked()
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.oauthRefreshTokenRecord(token, "refresh", a.cfg.PublicOrigin); ok {
		t.Fatal("explicitly revoked refresh accepted")
	}
}

func TestSharedAccessNativeMCPAdministrator(t *testing.T) {
	a, b := sharedAccessPair(t)
	token, _, err := a.issueManagedMCPTokenWithMode("native-admin", 7, a.cfg.PublicOrigin, a.cfg.PublicOrigin, "full", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	w := sharedAccessCall(t, b, token, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "execute", "arguments": map[string]any{"target": "hub", "tool": "access_profiles", "arguments": map[string]any{"action": "list"}}}})
	if w.Code != 200 {
		t.Fatalf("native MCP status=%d", w.Code)
	}
	var response map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["error"] != nil || !bytes.Contains(w.Body.Bytes(), []byte("profiles")) {
		t.Fatal("native MCP administration did not reach profile handler")
	}
}
