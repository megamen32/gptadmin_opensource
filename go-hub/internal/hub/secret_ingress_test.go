package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFromEnvConfiguresSecretIngress(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_CONFIG_DIR", root)
	t.Setenv("GPTADMIN_SECRET_STORE_DIR", filepath.Join(root, "secret-store"))
	t.Setenv("GPTADMIN_SECRET_STORE_KEY_FILE", filepath.Join(root, "secret-store.key"))
	t.Setenv("GPTADMIN_SECRET_INGRESS_STATE_FILE", filepath.Join(root, "secret-store", "state.json"))
	t.Setenv("GPTADMIN_SECRET_INGRESS_TTL", "600")

	cfg := FromEnv()
	if cfg.SecretStoreDir != filepath.Join(root, "secret-store") || cfg.SecretStoreKeyFile != filepath.Join(root, "secret-store.key") {
		t.Fatalf("secret store config=%q/%q", cfg.SecretStoreDir, cfg.SecretStoreKeyFile)
	}
	if cfg.SecretIngressStateFile != filepath.Join(root, "secret-store", "state.json") || cfg.SecretIngressTTL.Seconds() != 600 {
		t.Fatalf("secret ingress config=%q/%s", cfg.SecretIngressStateFile, cfg.SecretIngressTTL)
	}
}

func newSecretTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	publicDir := filepath.Join(root, "public")
	if err := os.MkdirAll(filepath.Join(publicDir, "secret-input"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "secret-input", "index.html"), []byte(`<form method="post"><input name="value"></form>`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		ConfigDir:      root,
		PublicDir:      publicDir,
		CtlToken:       "ctl",
		ShellToken:     "shell",
		PublicOrigin:   "https://hub.example",
		DefaultTimeout: time.Second,
		PollMaxTimeout: time.Second,
	})
	if s.secretStoreErr != nil {
		t.Fatalf("secret store initialization failed: %v", s.secretStoreErr)
	}
	return s
}

func createReadySecretForTest(t *testing.T, s *Server, envName, value string) SecretReference {
	t.Helper()
	request, token, err := s.secretStore.CreateRequest(secretOwnerFingerprintValue("bearer:ctl"), "test", envName, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.secretStore.ConsumeRequest(token, value)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Ref != request.Ref {
		t.Fatalf("reference changed during consume: %q/%q", request.Ref, ref.Ref)
	}
	return ref
}

func TestSecretIngressPageConsumesTokenWithoutReturningSecret(t *testing.T) {
	s := newSecretTestServer(t)
	request, token, err := s.secretStore.CreateRequest("owner-a", "GitHub token", "GITHUB_TOKEN", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, "/secret-input/"+token, nil)
	get.Header.Set("Host", "hub.example")
	getRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `name="value"`) {
		t.Fatalf("GET status/body = %d/%s", getRec.Code, getRec.Body.String())
	}
	if getRec.Header().Get("Cache-Control") != "no-store" || getRec.Header().Get("Referrer-Policy") != "no-referrer" || getRec.Header().Get("Content-Security-Policy") != "default-src 'none'; form-action 'self'; style-src 'unsafe-inline'" {
		t.Fatalf("unsafe secret-input headers: %v", getRec.Header())
	}
	post := httptest.NewRequest(http.MethodPost, "/secret-input/"+token, strings.NewReader("value=browser-secret"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK || strings.Contains(postRec.Body.String(), "browser-secret") || strings.Contains(postRec.Body.String(), request.Ref) {
		t.Fatalf("POST leaked or failed: %d/%s", postRec.Code, postRec.Body.String())
	}
	if _, err := s.secretStore.ConsumeRequest(token, "reuse"); !errors.Is(err, ErrSecretRequestConsumed) {
		t.Fatalf("reuse error = %v", err)
	}
}

func callSecretMCP(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestMCPSecretRequestReturnsInputURLAndOpaqueReference(t *testing.T) {
	s := newSecretTestServer(t)
	resp := callSecretMCP(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret_request","arguments":{"label":"OpenAI","env_name":"OPENAI_API_KEY"}}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, want := range []string{"input_url", "secret_ref", "OPENAI_API_KEY"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "plaintext") || strings.Contains(body, "browser-secret") {
		t.Fatalf("unexpected secret material: %s", body)
	}
}

func TestMCPSecretStatusDoesNotReturnPlaintext(t *testing.T) {
	s := newSecretTestServer(t)
	owner := secretOwnerFingerprintValue("bearer:ctl")
	request, rawToken, err := s.secretStore.CreateRequest(owner, "Test", "TEST_SECRET", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, "/secret-input/"+rawToken, strings.NewReader("value=must-not-return"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK {
		t.Fatalf("consume status=%d body=%s", postRec.Code, postRec.Body.String())
	}
	resp := callSecretMCP(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"secret_status","arguments":{"secret_ref":"`+request.Ref+`"}}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "must-not-return") {
		t.Fatalf("MCP status leaked the secret: %s", resp.Body.String())
	}
	for _, want := range []string{"ready", "TEST_SECRET", "secret_ref", "file"} {
		if !strings.Contains(resp.Body.String(), want) {
			t.Fatalf("missing %q: %s", want, resp.Body.String())
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestShellExecResolvesSecretReferenceWithoutLeakingValue(t *testing.T) {
	s := newSecretTestServer(t)
	s.mu.Lock()
	s.agents["shell:test"] = &Agent{AgentID: "shell:test", Status: "online"}
	s.mu.Unlock()
	ref := createReadySecretForTest(t, s, "TEST_SECRET", "do-not-return")
	request := httptest.NewRequest(http.MethodPost, "/mcp-relay/shell_exec", strings.NewReader(`{"target":"shell:test","cmd":"printenv TEST_SECRET","secret_env":{"TEST_SECRET":"`+ref.Ref+`"},"background":true}`))
	request.Header.Set("Authorization", "Bearer ctl")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "do-not-return") {
		t.Fatalf("secret leaked or request failed: %d/%s", response.Code, response.Body.String())
	}
	var queued map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	jobID, _ := queued["job_id"].(string)
	if jobID == "" {
		t.Fatalf("missing queued job: %v", queued)
	}
	s.mu.Lock()
	job := s.shellJobs[jobID]
	if job == nil || job.Env["TEST_SECRET"] != "do-not-return" {
		s.mu.Unlock()
		t.Fatalf("secret was not injected into queued job: %#v", job)
	}
	job.Status = "completed"
	job.Result = map[string]any{"stdout": "do-not-return"}
	s.mu.Unlock()

	inspect := httptest.NewRequest(http.MethodGet, "/mcp-relay/job/"+jobID, nil)
	inspect.Header.Set("Authorization", "Bearer ctl")
	inspectResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(inspectResponse, inspect)
	if inspectResponse.Code != http.StatusOK || strings.Contains(inspectResponse.Body.String(), "do-not-return") {
		t.Fatalf("job inspection leaked secret or failed: %d/%s", inspectResponse.Code, inspectResponse.Body.String())
	}
}

func TestReadonlyMCPCannotRequestOrInspectSecrets(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	r = requestWithAuthClaims(r, map[string]any{"scope": "gptadmin.read", "access_mode": accessModeReadonly})
	for _, name := range []string{"secret_request", "secret_status"} {
		if err := authorizeFacadeCall(r, name, nil); err == nil {
			t.Fatalf("readonly request unexpectedly authorized %s", name)
		}
	}
	for _, tool := range appsSDKToolsForRequest(r) {
		if firstString(tool, "name") == "secret_request" || firstString(tool, "name") == "secret_status" {
			t.Fatalf("readonly tools exposed secret operation: %v", tool)
		}
	}
}

func TestSecretIngressDoesNotAcceptValueFromURLQuery(t *testing.T) {
	s := newSecretTestServer(t)
	request, token, err := s.secretStore.CreateRequest("owner-a", "Query test", "QUERY_SECRET", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(http.MethodPost, "/secret-input/"+token+"?value=query-secret", strings.NewReader(""))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, post)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "query-secret") {
		t.Fatalf("query value was accepted or leaked: %d/%s", rec.Code, rec.Body.String())
	}
	status, err := s.secretStore.Status(request.Ref, "owner-a")
	if err != nil || status.Status != "pending" {
		t.Fatalf("query submission changed request state: %#v err=%v", status, err)
	}
}
