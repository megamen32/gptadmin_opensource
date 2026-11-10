package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const bootstrapLHCFixture = "# Last Human Commit\n\nRead this complete fixture, including its final rule.\n## Worktree and registration\nUse the native worktree workflow.\nFINAL_LHC_RULE_7f3c\n"

func bootstrapRequest(t *testing.T, s *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func bootstrapTestServer(t *testing.T) (*Server, Config) {
	t.Helper()
	lhcPath := filepath.Join(t.TempDir(), "1.17.1")
	if err := os.Mkdir(lhcPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lhcPath, "AGENTS.md"), []byte(bootstrapLHCFixture), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{CtlToken: "ctl", ConfigDir: t.TempDir(), OAuthClientSecret: "test-secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", RegistryStateFile: filepath.Join(t.TempDir(), "registry.json"), StartupInstructions: "Current profile instructions.", BootstrapLHCPath: lhcPath}
	s := New(cfg)
	r := httptest.NewRequest(http.MethodPut, "/admin/api/access-profiles/sandbox", strings.NewReader(`{"id":"sandbox","name":"Sandbox","access_mode":"full","approval_mode":"unrestricted","allowed_targets":["hub"],"allowed_tools":["discover","search","schema"]}`))
	r.Header.Set("Authorization", "Bearer ctl")
	r.Header.Set("If-Match", "*")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	return s, cfg
}

func bootstrapCreate(t *testing.T, s *Server) (map[string]any, string, string) {
	t.Helper()
	w := bootstrapRequest(t, s, "POST", "/admin/api/bootstrap-links", "ctl", map[string]any{"label": "Z.ai sandbox", "profile_id": "sandbox"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimPrefix(u.Path, "/bootstrap/")) < 60 {
		t.Fatal("capability URL is too short")
	}
	id := result["link"].(map[string]any)["id"].(string)
	return result, u.Path, id
}

func TestExternalBootstrapNeverRequiresMissingEntityFinder(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	created, path, _ := bootstrapCreate(t, s)
	prompt, _ := created["prompt"].(string)
	doc := bootstrapRequest(t, s, "GET", path, "", nil).Body.String()
	if strings.Contains(prompt, "Entity Finder") || strings.Contains(doc, "Entity Finder") {
		t.Fatal("bootstrap still instructs users to find an unavailable Entity Finder")
	}
	for _, text := range []string{"discover", "search", "Agent Herder", "Если доступен"} {
		if !strings.Contains(doc, text) && !strings.Contains(prompt, text) {
			t.Fatalf("bootstrap is missing working discovery/fallback instruction %q", text)
		}
	}
}

func TestExternalBootstrapLifecycle(t *testing.T) {
	s, cfg := bootstrapTestServer(t)
	created, path, id := bootstrapCreate(t, s)
	link := created["link"].(map[string]any)
	if int64(link["expires_at"].(float64))-int64(link["created_at"].(float64)) != 7*86400 {
		t.Fatalf("default TTL: %v", link)
	}
	for _, suffix := range []string{"", "/instructions.md", "/config.json"} {
		w := bootstrapRequest(t, s, "GET", path+suffix, "", nil)
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", suffix, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("cache headers: %v", w.Header())
		}
	}
	doc := bootstrapRequest(t, s, "GET", path, "", nil).Body.String()
	if !strings.Contains(doc, bootstrapLHCFixture) {
		t.Fatal("full native LHC text is missing")
	}
	for _, text := range []string{"Current profile instructions.", "discover", "find_paths", "Last Human Commit", "worktree", "gptadmin.py"} {
		if !strings.Contains(doc, text) {
			t.Fatalf("missing instruction %q", text)
		}
	}
	configResponse := bootstrapRequest(t, s, "GET", path+"/config.json", "", nil)
	var config map[string]any
	if err := json.Unmarshal(configResponse.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	token := config["access_token"].(string)
	if config["protocol_version"] != "2026-07-28" || config["expires_at"] != link["expires_at"] {
		t.Fatalf("config: %v", config)
	}
	list := bootstrapRequest(t, s, "GET", "/admin/api/bootstrap-links", "ctl", nil)
	for _, secret := range []string{token, created["url"].(string), "bootstrap_digest", "token_digest"} {
		if strings.Contains(list.Body.String(), secret) {
			t.Fatal("inventory exposes capability material")
		}
	}
	head := bootstrapRequest(t, s, "HEAD", path+"/config.json", "", nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(configResponse.Body.Len()) {
		t.Fatalf("HEAD: %d %d %v", head.Code, head.Body.Len(), head.Header())
	}
	s.mu.Lock()
	record := s.managedMCP[id]
	s.mu.Unlock()
	serialized, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{record.BootstrapDigest, record.TokenValue, "bootstrap_digest"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatal("managed-token inventory exposes private bootstrap state")
		}
	}
	ordinary := bootstrapRequest(t, s, "POST", "/admin/api/bootstrap-links", token, map[string]any{"profile_id": "sandbox"})
	if ordinary.Code != http.StatusForbidden {
		t.Fatalf("client escalated to issuer: %d", ordinary.Code)
	}
	// Persisted grant and native bearer remain one record after restart.
	restarted := New(cfg)
	if w := bootstrapRequest(t, restarted, "GET", path+"/config.json", "", nil); w.Code != 200 {
		t.Fatalf("restart: %d", w.Code)
	}
	valid := bootstrapRequest(t, restarted, "POST", "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if valid.Code != 200 {
		t.Fatalf("downloaded credential: %d %s", valid.Code, valid.Body.String())
	}
	if strings.Contains(valid.Body.String(), `"name":"exec"`) {
		t.Fatal("profile restrictions lost")
	}
	revoke := bootstrapRequest(t, restarted, "DELETE", "/admin/api/bootstrap-links/"+id, "ctl", nil)
	if revoke.Code != 200 {
		t.Fatalf("revoke: %d %s", revoke.Code, revoke.Body.String())
	}
	if w := bootstrapRequest(t, s, "GET", path, "", nil); w.Code != http.StatusGone {
		t.Fatalf("cross-instance revoke: %d", w.Code)
	}
	denied := bootstrapRequest(t, s, "POST", "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	if denied.Code == 200 {
		t.Fatalf("revoked credential accepted: %s", denied.Body.String())
	}
}

func TestExternalBootstrapExpiryAndInvalidRequests(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	_, path, id := bootstrapCreate(t, s)
	for _, body := range []map[string]any{{"profile_id": "sandbox", "ttl_days": -1}, {"profile_id": "sandbox", "ttl_days": 3651}, {"profile_id": "missing"}, {"label": "unbound"}} {
		w := bootstrapRequest(t, s, "POST", "/admin/api/bootstrap-links", "ctl", body)
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("invalid request: %v -> %d", body, w.Code)
		}
	}
	w := bootstrapRequest(t, s, "GET", path+"x/config.json", "", nil)
	if w.Code != 404 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("wrong secret: %d %v", w.Code, w.Header())
	}
	s.mu.Lock()
	record := s.managedMCP[id]
	record.ExpiresAt = s.now().Unix() - 1
	s.managedMCP[id] = record
	err := s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if w := bootstrapRequest(t, s, "GET", path+"/config.json", "", nil); w.Code != 410 {
		t.Fatalf("expired: %d", w.Code)
	}
	denied := bootstrapRequest(t, s, "POST", "/mcp", record.TokenValue, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	if denied.Code == 200 {
		t.Fatal("expired downloaded credential accepted")
	}
	if w := bootstrapRequest(t, s, "POST", path, "", nil); w.Code != 405 {
		t.Fatalf("public mutation: %d", w.Code)
	}
	if w := bootstrapRequest(t, s, "GET", "/admin/api/bootstrap-links", "", nil); w.Code == 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("unauthenticated inventory: %d", w.Code)
	}
}

func TestExternalBootstrapRequiresCurrentFullWorkflow(t *testing.T) {
	s, cfg := bootstrapTestServer(t)
	_, path, _ := bootstrapCreate(t, s)
	updated := bootstrapLHCFixture + "\nA newly installed rule must be visible immediately.\n"
	if err := os.WriteFile(filepath.Join(cfg.BootstrapLHCPath, "AGENTS.md"), []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	if w := bootstrapRequest(t, s, "GET", path, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), updated) {
		t.Fatalf("stale native LHC: %d %s", w.Code, w.Body.String())
	}
	if err := os.Remove(filepath.Join(cfg.BootstrapLHCPath, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, suffix := range []string{"", "/config.json", "/client.py"} {
			w := bootstrapRequest(t, s, method, path+suffix, "", nil)
			if w.Code != 503 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("%s %s released access without LHC: %d", method, suffix, w.Code)
			}
		}
	}
	s.mu.Lock()
	before := len(s.managedMCP)
	s.mu.Unlock()
	w := bootstrapRequest(t, s, "POST", "/admin/api/bootstrap-links", "ctl", map[string]any{"profile_id": "sandbox"})
	if w.Code != 503 {
		t.Fatalf("issued incomplete instructions: %d", w.Code)
	}
	s.mu.Lock()
	after := len(s.managedMCP)
	s.mu.Unlock()
	if before != after {
		t.Fatal("failed issue minted a credential")
	}
}

func TestBootstrapWorkflowUsesConfiguredActivePackage(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"1.17.1", "9.99.99"} {
		path := filepath.Join(root, version)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "AGENTS.md"), []byte("full "+version), 0600); err != nil {
			t.Fatal(err)
		}
	}
	active := filepath.Join(root, "active")
	if err := os.Symlink(filepath.Join(root, "1.17.1"), active); err != nil {
		t.Fatal(err)
	}
	workflow, err := loadBootstrapWorkflow(active)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte("full 1.17.1"))
	if workflow.meta["version"] != "1.17.1" || workflow.meta["sha256"] != hex.EncodeToString(wantHash[:]) || !strings.Contains(workflow.text, "full 1.17.1") || strings.Contains(workflow.text, "9.99.99") {
		t.Fatalf("wrong active package: %#v", workflow)
	}
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "9.99.99"), active); err != nil {
		t.Fatal(err)
	}
	workflow, err = loadBootstrapWorkflow(active)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.meta["version"] != "9.99.99" {
		t.Fatalf("active package change not observed: %#v", workflow.meta)
	}
	for _, path := range []string{"", "relative/path", filepath.Join(root, "missing")} {
		if _, err := loadBootstrapWorkflow(path); err == nil {
			t.Fatalf("accepted incomplete LHC source %q", path)
		}
	}
	invalid := filepath.Join(root, "invalid.md")
	for _, data := range [][]byte{nil, {0xff}, []byte(strings.Repeat("x", 256*1024+1))} {
		if err := os.WriteFile(invalid, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadBootstrapWorkflow(invalid); err == nil {
			t.Fatalf("accepted invalid LHC (%d bytes)", len(data))
		}
	}
}

func TestExternalBootstrapFollowsNewestInstalledLHC(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	root := t.TempDir()
	install := func(version string) {
		t.Helper()
		path := filepath.Join(root, version)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "AGENTS.md"), []byte("# Last Human Commit installed "+version), 0600); err != nil {
			t.Fatal(err)
		}
	}
	install("1.17.2")
	s.cfg.BootstrapLHCPath = root
	_, path, _ := bootstrapCreate(t, s)
	check := func(version string) {
		t.Helper()
		w := bootstrapRequest(t, s, "GET", path, "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "installed "+version) {
			t.Fatalf("same bootstrap link did not load LHC %s: %d %s", version, w.Code, w.Body.String())
		}
		w = bootstrapRequest(t, s, "GET", path+"/manifest.json", "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), version) {
			t.Fatalf("manifest did not match LHC %s: %d %s", version, w.Code, w.Body.String())
		}
	}
	check("1.17.2")
	install("1.18.1")
	check("1.18.1")
	if err := os.RemoveAll(filepath.Join(root, "1.18.1")); err != nil {
		t.Fatal(err)
	}
	check("1.17.2")
}

func TestBootstrapLatestSourceFailsClosedOnIncompleteUpdate(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "1.18.1")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "AGENTS.md"), []byte("valid version"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "1.19.0"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBootstrapWorkflow(root); err == nil {
		t.Fatal("published older LHC while newest version was incomplete")
	}
}

func TestBootstrapConfiguredVersionPathSurvivesPrune(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"1.17.2", "1.18.1", "1.18.10"} {
		path := filepath.Join(root, version)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "AGENTS.md"), []byte("installed "+version), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldPath := filepath.Join(root, "1.17.2")
	for _, action := range []string{"before-prune", "after-prune"} {
		workflow, err := loadBootstrapWorkflow(oldPath)
		if err != nil {
			t.Fatal(action, err)
		}
		if workflow.meta["version"] != "1.18.10" {
			t.Fatalf("%s: got %v", action, workflow.meta["version"])
		}
		if action == "before-prune" {
			if err := os.RemoveAll(oldPath); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func bootstrapPutProfile(t *testing.T, s *Server, profile AccessProfile) {
	t.Helper()
	path := "/admin/api/access-profiles/" + profile.ID
	current := bootstrapRequest(t, s, "GET", path, "ctl", nil)
	match := "*"
	if current.Code == 200 {
		match = current.Header().Get("ETag")
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", path, strings.NewReader(string(data)))
	r.Header.Set("Authorization", "Bearer ctl")
	r.Header.Set("If-Match", match)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("profile %s: %d %s", profile.ID, w.Code, w.Body.String())
	}
}

func bootstrapRoleToken(t *testing.T, s *Server, profileID, role string) string {
	t.Helper()
	_, record, err := newManagedMCPToken("bootstrap-test-"+newID(), 7, "https://hub.example", "https://hub.example", "full", profileID, role, s.now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.managedMCP[record.ID] = record
	err = s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return record.TokenValue
}

func TestExternalBootstrapAdminRoleAndActions(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	profile := AccessProfile{ID: "issuer", Name: "Issuer", AccessMode: "full", ApprovalMode: "unrestricted", AllowedTargets: []string{"hub"}, AllowedTools: []string{"access_clients"}, ToolRules: []AccessToolRule{{Target: "hub", Tool: "access_clients", Actions: []string{"list"}}}}
	bootstrapPutProfile(t, s, profile)
	admin := bootstrapRoleToken(t, s, profile.ID, "admin")
	client := bootstrapRoleToken(t, s, profile.ID, "client")
	_, path, id := bootstrapCreate(t, s)
	for _, check := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/admin/api/bootstrap-links", admin, 200},
		{"POST", "/admin/api/bootstrap-links", admin, 403},
		{"DELETE", "/admin/api/bootstrap-links/" + id, admin, 403},
		{"GET", "/admin/api/bootstrap-links", client, 403},
		{"POST", "/admin/api/bootstrap-links", client, 403},
		{"DELETE", "/admin/api/bootstrap-links/" + id, client, 403},
	} {
		w := bootstrapRequest(t, s, check.method, check.path, check.token, map[string]any{"profile_id": "sandbox"})
		if w.Code != check.status {
			t.Fatalf("%s %s expected %d got %d: %s", check.method, check.path, check.status, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("authorization response can be cached: %v", w.Header())
		}
	}
	if w := bootstrapRequest(t, s, "GET", path, "", nil); w.Code != 200 {
		t.Fatalf("denied revoke still mutated link: %d", w.Code)
	}
	profile.ToolRules[0].Actions = []string{"list", "issue", "revoke"}
	bootstrapPutProfile(t, s, profile)
	if w := bootstrapRequest(t, s, "POST", "/admin/api/bootstrap-links", admin, map[string]any{"profile_id": "sandbox"}); w.Code != 201 {
		t.Fatalf("updated current profile did not grant issue: %d %s", w.Code, w.Body.String())
	}
	if w := bootstrapRequest(t, s, "DELETE", "/admin/api/bootstrap-links/"+id, admin, nil); w.Code != 200 {
		t.Fatalf("updated current profile did not grant revoke: %d %s", w.Code, w.Body.String())
	}
}

func TestBootstrapRequestAuditRedactsCapability(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	_, path, _ := bootstrapCreate(t, s)
	r := httptest.NewRequest("GET", path+"/config.json", nil)
	r.Header.Set("Referer", "https://hub.example"+path)
	fields := s.requestForAudit(r)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), strings.TrimPrefix(path, "/bootstrap/")) {
		t.Fatal("request audit retains bootstrap capability")
	}
	if bootstrapAuditPath(path+"/config.json") != "/bootstrap/[redacted]" || bootstrapAuditPath("/mcp") != "/mcp" {
		t.Fatal("incorrect ingress path redaction")
	}
	if bootstrapAuditReferer("https://hub.example/bootstrap%2Fsecret/config.json") != "[redacted bootstrap URL]" {
		t.Fatal("encoded bootstrap referer was not redacted")
	}
}

func TestExternalBootstrapUsesCurrentGrantProfile(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	_, path, _ := bootstrapCreate(t, s)
	config := bootstrapRequest(t, s, "GET", path+"/config.json", "", nil)
	var cfg map[string]any
	if err := json.Unmarshal(config.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	token := cfg["access_token"].(string)
	profile := AccessProfile{ID: "sandbox", Name: "Narrowed sandbox", AccessMode: "full", ApprovalMode: "unrestricted", AllowedTargets: []string{"hub"}, AllowedTools: []string{"search"}}
	bootstrapPutProfile(t, s, profile)
	response := bootstrapRequest(t, s, "POST", "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if response.Code != 200 {
		t.Fatalf("current profile authentication: %d %s", response.Code, response.Body.String())
	}
	var rpc struct {
		Result struct{ Tools []struct{ Name string } }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	searchPresent := false
	for _, tool := range rpc.Result.Tools {
		if tool.Name == "search" {
			searchPresent = true
		}
		if tool.Name == "schema" || tool.Name == "exec" {
			t.Fatalf("downloaded credential retained removed tool %s", tool.Name)
		}
	}
	if !searchPresent {
		t.Fatal("current profile tools/list did not expose its allowed search tool")
	}
	if w := bootstrapRequest(t, s, "GET", path, "", nil); w.Code != 200 {
		t.Fatalf("valid narrowed profile: %d", w.Code)
	}
	// A removed profile on shared storage must not fall back to unbound access.
	profiles, err := s.accessProfilesSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	delete(profiles, "sandbox")
	data, err := json.Marshal(accessProfilesState{Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.accessProfilesStatePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if w := bootstrapRequest(t, s, "GET", path+"/config.json", "", nil); w.Code != 410 {
		t.Fatalf("missing profile did not disable link: %d", w.Code)
	}
	denied := bootstrapRequest(t, s, "POST", "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	if denied.Code == 200 {
		t.Fatal("missing profile accepted the downloaded credential")
	}
}

func TestBootstrapExpirySurvivesRelaxedBearerPolicy(t *testing.T) {
	s, _ := bootstrapTestServer(t)
	_, _, id := bootstrapCreate(t, s)
	s.cfg.RelaxAuthChecks = true
	if s.effectiveBearerSecurityProfile().EnforceTokenLifecycle {
		t.Fatal("test fixture did not disable the general lifecycle check")
	}
	s.mu.Lock()
	record := s.managedMCP[id]
	record.ExpiresAt = s.now().Unix() - 1
	s.managedMCP[id] = record
	err := s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.verifyManagedMCPToken(record.TokenValue); ok {
		t.Fatal("relaxed bearer policy extended the temporary bootstrap grant")
	}
}
