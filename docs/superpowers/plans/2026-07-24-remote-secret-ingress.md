# Remote Secret Ingress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an authorized remote MCP agent create a short-lived one-time secret-entry link, let the operator enter the value in a browser, and make the resulting secret usable by Hub-managed agents without returning the plaintext to the model or storing it in Git.

**Architecture:** Add a secret-ingress subsystem to the existing Go Hub. The MCP tool creates a request bound to the caller identity, stores only a hash of the browser token, and returns a public one-time URL plus opaque `secret_ref`, file path, and environment-variable name. The browser POST consumes the token and stores the value encrypted at rest under the Hub config directory. Secret references are resolved server-side only when constructing an approved agent job; MCP responses, audit records, status pages, and job results never include the value.

**Tech Stack:** Go standard library (`crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/sha256`, `encoding/json`, `net/http`), existing GPTAdmin Hub MCP JSON-RPC, existing admin/public static serving, Go `httptest`, and the repository's existing release/deploy path.

## Global Constraints

- Never accept a secret value through an MCP JSON-RPC request; values enter only through the browser form or a local operator CLI used by the Hub host.
- Never return secret plaintext in MCP JSON, HTTP responses, audit records, logs, job inspection, or error messages.
- Browser ingress tokens are random, stored only as hashes, single-use, caller-bound, and expire after 15 minutes by default; configurable bounds are 60–3600 seconds.
- Secret records and the encryption key are created with mode `0600`; parent secret directories are mode `0700`.
- A missing or invalid encryption key fails closed; the Hub must not silently fall back to plaintext files or environment variables.
- Existing MCP auth, access profiles, and the `2.1` router exception remain unchanged; only the public Hub origin is used for generated links.
- Preserve unrelated dirty changes in `/home/roomhacker/gptadmin` (`docs/BUGS.md` and `docs/WORKLOG.md` are already modified).

---

### Task 1: Define the encrypted secret-store and one-time request contract

**Files:**
- Create: `go-hub/internal/hub/secret_store_test.go`
- Create: `go-hub/internal/hub/secret_store.go`

**Interfaces:**
- Produces `SecretStore`, `SecretIngressRequest`, `SecretReference`, and typed errors used by the HTTP and MCP layers.
- `NewSecretStore(configDir, storeDir, keyFile string, now func() time.Time) (*SecretStore, error)` loads or creates the 32-byte AES-GCM key with mode `0600`.
- `CreateRequest(ownerFingerprint, label, envName string, ttl time.Duration) (SecretIngressRequest, string, error)` returns metadata and the raw browser token; only the caller keeps the raw token.
- `ConsumeRequest(rawToken, value string) (SecretReference, error)` atomically consumes a valid request and writes encrypted content.
- `Status(ref, ownerFingerprint string) (SecretReference, error)` checks caller ownership and readiness without returning the value.
- `Resolve(ref, ownerFingerprint string) (string, error)` is internal-only and is used immediately before creating an authorized agent job.

- [ ] **Step 1: Write the failing tests**

```go
func TestSecretStoreCreatesSingleUseRequestAndEncryptedRecord(t *testing.T) {
	store := newTestSecretStore(t)
	request, rawToken, err := store.CreateRequest("owner-a", "OpenAI key", "OPENAI_API_KEY", time.Minute)
	if err != nil { t.Fatal(err) }
	ref, err := store.ConsumeRequest(rawToken, "value-that-must-not-be-printed")
	if err != nil { t.Fatal(err) }
	if ref.EnvName != "OPENAI_API_KEY" || ref.Status != "ready" { t.Fatalf("unexpected ref: %#v", ref) }
	if _, err := store.ConsumeRequest(rawToken, "second-use"); !errors.Is(err, ErrSecretRequestConsumed) { t.Fatalf("second use error = %v", err) }
	got, err := store.Resolve(ref.Ref, "owner-a")
	if err != nil || got != "value-that-must-not-be-printed" { t.Fatalf("resolved value = %q, err = %v", got, err) }
	for _, path := range store.filesForTest() {
		if strings.Contains(string(mustReadFile(t, path)), "value-that-must-not-be-printed") { t.Fatalf("plaintext leaked to %s", path) }
	}
}

func TestSecretStoreRejectsExpiredAndWrongOwnerRequests(t *testing.T) {
	clock := newTestClock(time.Unix(100, 0))
	store := newTestSecretStoreWithClock(t, clock)
	request, token, err := store.CreateRequest("owner-a", "key", "KEY", time.Minute)
	if err != nil { t.Fatal(err) }
	if _, err := store.Status(request.Ref, "owner-b"); !errors.Is(err, ErrSecretNotFound) { t.Fatalf("wrong owner status error = %v", err) }
	clock.Advance(61 * time.Second)
	if _, err := store.ConsumeRequest(token, "expired"); !errors.Is(err, ErrSecretRequestExpired) { t.Fatalf("expired request error = %v", err) }
}
```

- [ ] **Step 2: Run the focused tests to verify RED**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestSecretStore' -count=1`

Expected: FAIL because `NewSecretStore`, `CreateRequest`, and the typed errors do not exist.

- [ ] **Step 3: Implement the minimal store**

Implement AES-256-GCM records under `<storeDir>/<ref-id>.json`, a state file for pending requests, hash browser tokens with SHA-256, and persist all state with a temporary file plus rename. Keep the plaintext value only in memory during `ConsumeRequest` and `Resolve`; zero the mutable byte slice after encryption/decryption where practical. Reject empty labels, invalid environment names, TTL values outside 60–3600 seconds, expired requests, consumed requests, and owner mismatches.

- [ ] **Step 4: Run the focused tests to verify GREEN**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestSecretStore' -count=1`

Expected: PASS with no secret value in test output.

- [ ] **Step 5: Commit**

```bash
git add go-hub/internal/hub/secret_store.go go-hub/internal/hub/secret_store_test.go
git commit -m "feat: add encrypted one-time secret store"
```

### Task 2: Add browser secret ingress and Hub configuration

**Files:**
- Modify: `go-hub/internal/hub/server.go:70-107, 274-334, 515-591`
- Create: `go-hub/internal/hub/secret_ingress.go`
- Create: `go-hub/internal/hub/secret_ingress_test.go`
- Create: `public/secret-input/index.html`

**Interfaces:**
- `Config` gains `SecretStoreDir`, `SecretStoreKeyFile`, `SecretIngressStateFile`, and `SecretIngressTTL` values loaded from `GPTADMIN_SECRET_*` variables.
- `Handler` registers `GET /secret-input/{token}` and `POST /secret-input/{token}` outside `requireCtl`; the page exposes only a one-time form and the POST returns a generic completion message.
- `POST /secret-input/{token}` calls `ConsumeRequest` and never renders the submitted value, reference value, or filesystem contents.
- Generated links use `PublicOrigin`, falling back to the request scheme and host only when `PublicOrigin` is unset.

- [ ] **Step 1: Write the failing tests**

```go
func TestSecretIngressPageConsumesTokenWithoutReturningSecret(t *testing.T) {
	s := newSecretTestServer(t)
	request, token, err := s.secretStore.CreateRequest("owner-a", "GitHub token", "GITHUB_TOKEN", time.Minute)
	if err != nil { t.Fatal(err) }
	get := httptest.NewRequest(http.MethodGet, "/secret-input/"+token, nil)
	get.Header.Set("Host", "hub.example")
	getRec := httptest.NewRecorder(); s.Handler().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), "name=\"value\"") { t.Fatalf("GET status/body = %d/%s", getRec.Code, getRec.Body.String()) }
	post := httptest.NewRequest(http.MethodPost, "/secret-input/"+token, strings.NewReader("value=browser-secret"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder(); s.Handler().ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK || strings.Contains(postRec.Body.String(), "browser-secret") { t.Fatalf("POST leaked or failed: %d/%s", postRec.Code, postRec.Body.String()) }
	if _, err := s.secretStore.ConsumeRequest(token, "reuse"); !errors.Is(err, ErrSecretRequestConsumed) { t.Fatalf("reuse error = %v", err) }
	_ = request
}
```

- [ ] **Step 2: Run the focused tests to verify RED**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestSecretIngress' -count=1`

Expected: FAIL because the route and server-owned secret store are not present.

- [ ] **Step 3: Implement the route and page**

Initialize the store in `New`, fail Hub startup when the key cannot be loaded, register both routes, load the static page from `PublicDir`, set `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, `Content-Security-Policy: default-src 'none'; form-action 'self'; style-src 'unsafe-inline'`, and use `303` after successful POST only if the response does not reveal secret metadata. Add `SameSite=Strict` only if a cookie is needed; the one-time token itself is the credential.

- [ ] **Step 4: Run the focused tests to verify GREEN**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestSecretIngress' -count=1`

Expected: PASS; the response body and logs contain no submitted secret.

- [ ] **Step 5: Commit**

```bash
git add go-hub/internal/hub/server.go go-hub/internal/hub/secret_ingress.go go-hub/internal/hub/secret_ingress_test.go public/secret-input/index.html
git commit -m "feat: add one-time browser secret ingress"
```

### Task 3: Expose MCP request/status tools without exposing values

**Files:**
- Modify: `go-hub/internal/hub/server.go:2247-2260, 4080-4255`
- Modify: `go-hub/internal/hub/server_test.go`

**Interfaces:**
- Advertise `secret_request` and `secret_status` from `hubTools()` and the normal `/mcp` tools list.
- `secret_request` arguments: `{label: string, env_name?: string, ttl_seconds?: integer}`; result: `{status, request_id, input_url, secret_ref, env_name, file}`.
- `secret_status` arguments: `{secret_ref: string}`; result: `{status, secret_ref, env_name, file}`; it never returns the value.
- Caller ownership is derived from the authenticated request's stable identity (JWT subject/client ID when present, otherwise a SHA-256 fingerprint of the bearer credential); unauthenticated calls remain rejected by existing MCP auth.

- [ ] **Step 1: Write the failing tests**

```go
func TestMCPSecretRequestReturnsInputURLAndOpaqueReference(t *testing.T) {
	s := newSecretTestServer(t)
	resp := mcpCall(t, s, "Bearer ctl", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"secret_request","arguments":{"label":"OpenAI","env_name":"OPENAI_API_KEY"}}}`)
	if resp.Code != http.StatusOK { t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String()) }
	body := resp.Body.String()
	for _, want := range []string{"input_url", "secret_ref", "OPENAI_API_KEY"} { if !strings.Contains(body, want) { t.Fatalf("missing %q: %s", want, body) } }
	if strings.Contains(body, "value") || strings.Contains(body, "plaintext") { t.Fatalf("unexpected secret material: %s", body) }
}

func TestMCPSecretStatusDoesNotReturnPlaintext(t *testing.T) {
	s := newSecretTestServer(t)
	request, rawToken, err := s.secretStore.CreateRequest("owner-a", "Test", "TEST_SECRET", time.Minute)
	if err != nil { t.Fatal(err) }
	post := httptest.NewRequest(http.MethodPost, "/secret-input/"+rawToken, strings.NewReader("value=must-not-return"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder(); s.Handler().ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK { t.Fatalf("consume status=%d body=%s", postRec.Code, postRec.Body.String()) }
	resp := mcpCall(t, s, "Bearer ctl", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"secret_status","arguments":{"secret_ref":"`+request.Ref+`"}}}`)
	if resp.Code != http.StatusOK { t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String()) }
	if strings.Contains(resp.Body.String(), "must-not-return") { t.Fatalf("MCP status leaked the secret: %s", resp.Body.String()) }
	for _, want := range []string{"ready", "TEST_SECRET", "secret_ref", "file"} { if !strings.Contains(resp.Body.String(), want) { t.Fatalf("missing %q: %s", want, resp.Body.String()) } }
}
```

- [ ] **Step 2: Run the focused tests to verify RED**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestMCPSecret' -count=1`

Expected: FAIL because the tools are not advertised or dispatched.

- [ ] **Step 3: Implement the two MCP operations**

Add exact JSON schemas with `additionalProperties: false`, use `PublicOrigin` to form the link, and return opaque metadata only. Reject a missing label, invalid env name, expired request, foreign reference, and any attempt to pass a value argument. Add access-profile classification so secret request/status are treated as write/credential operations and are unavailable to readonly profiles.

- [ ] **Step 4: Run the focused tests to verify GREEN**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestMCPSecret' -count=1`

Expected: PASS; `tools/list`, `secret_request`, and `secret_status` work through the same remote MCP endpoint.

- [ ] **Step 5: Commit**

```bash
git add go-hub/internal/hub/server.go go-hub/internal/hub/server_test.go
git commit -m "feat: expose secret ingress through Hub MCP"
```

### Task 4: Resolve references server-side for agent jobs

**Files:**
- Modify: `go-hub/internal/hub/server.go:2015-2245, 3930-3965`
- Modify: `go-hub/internal/hub/server_test.go`

**Interfaces:**
- `shell_exec` accepts optional `secret_env: {ENV_NAME: SECRET_REF}`; the Hub resolves each reference for the authenticated caller and injects the resulting values into the queued job's environment.
- The MCP response returns only job metadata; `GET /mcp-relay/job/{id}` redacts secret environment entries and command output using the resolved values.
- Unknown, expired, foreign, or revoked refs fail before the job is queued.

- [ ] **Step 1: Write the failing tests**

```go
func TestShellExecResolvesSecretReferenceWithoutLeakingValue(t *testing.T) {
	s := newSecretTestServer(t)
	ref := createReadySecretForTest(t, s, "owner-a", "TEST_SECRET", "do-not-return")
	resp := callHubShellExec(t, s, map[string]any{"target":"shell:test", "cmd":"printenv TEST_SECRET", "secret_env":map[string]any{"TEST_SECRET":ref.Ref}})
	if strings.Contains(resp.Body.String(), "do-not-return") { t.Fatalf("secret leaked: %s", resp.Body.String()) }
	job := queuedShellJobForTest(t, s)
	if job.Env["TEST_SECRET"] != "do-not-return" { t.Fatalf("secret was not injected into the job") }
}
```

- [ ] **Step 2: Run the focused tests to verify RED**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestShellExecResolvesSecret' -count=1`

Expected: FAIL because `secret_env` is currently ignored or rejected.

- [ ] **Step 3: Implement server-side resolution and redaction**

Resolve references before `callShellTool`/relay enqueue, keep plaintext only in the in-memory job payload required by the target agent, add a redaction list to the job record, and apply it to synchronous responses, background job results, audit payloads, and error strings. Do not persist resolved values in JSON state files.

- [ ] **Step 4: Run focused and package tests**

Run: `cd /home/roomhacker/gptadmin/go-hub && go test ./internal/hub -run 'TestShellExecResolvesSecret|TestAuditRedacts' -count=1 && go test ./internal/hub -count=1`

Expected: PASS; existing relay, auth, and redaction tests remain green.

- [ ] **Step 5: Commit**

```bash
git add go-hub/internal/hub/server.go go-hub/internal/hub/server_test.go
git commit -m "feat: inject secret references into agent jobs"
```

### Task 5: Document configuration, client workflow, and deployment verification

**Files:**
- Modify: `docs/HUB.md`
- Modify: `docs/SECURITY_DOCS.md`
- Modify: `docs/API_REFERENCE.md`
- Modify: `tests/test_no_secrets.py` only if the new generated/static assets require a narrowly scoped rule
- Create: `tests/test_secret_ingress_contract.py` for static route/config documentation contracts

**Interfaces:**
- Document `GPTADMIN_SECRET_STORE_DIR`, `GPTADMIN_SECRET_STORE_KEY_FILE`, `GPTADMIN_SECRET_INGRESS_STATE_FILE`, and `GPTADMIN_SECRET_INGRESS_TTL` with secure defaults and rotation/recovery behavior.
- Document the exact flow: call `secret_request`, open `input_url`, submit once, poll `secret_status`, then pass `secret_env` to `shell_exec`.
- Document that the public router forwards the existing Hub origin, while 2.1 remains excluded from the remote MCP route.

- [ ] **Step 1: Write the failing documentation/contract tests**

```python
def test_secret_ingress_docs_define_no_plaintext_mcp_flow():
    text = (ROOT / "docs" / "HUB.md").read_text(encoding="utf-8")
    assert "secret_request" in text
    assert "secret_env" in text
    assert "never" in text.lower() and "plaintext" in text.lower()
```

- [ ] **Step 2: Run the focused test to verify RED**

Run: `pytest -q tests/test_secret_ingress_contract.py`

Expected: FAIL because the workflow is not documented.

- [ ] **Step 3: Add the documentation and contract test**

Include examples with fake names only, never real tokens, and explain that file paths are references for Hub-managed execution rather than permission to read the file through `system_inspect`.

- [ ] **Step 4: Run the full focused verification**

Run:

```bash
cd /home/roomhacker/gptadmin/go-hub
go test ./internal/hub -count=1
go build ./cmd/gptadmin-hub
cd /home/roomhacker/gptadmin
pytest -q tests/test_secret_ingress_contract.py tests/test_no_secrets.py
git diff --check
```

Expected: all tests pass, the Hub binary builds, and no secret scan or whitespace check reports a failure.

- [ ] **Step 5: Commit**

```bash
git add docs/HUB.md docs/SECURITY_DOCS.md docs/API_REFERENCE.md tests/test_secret_ingress_contract.py
git commit -m "docs: describe remote secret ingress workflow"
```

### Task 6: Live acceptance on server 88

**Files:**
- No source changes expected; use the repository's existing Hub release/deploy workflow and immutable build/version identity.

- [ ] **Step 1: Build the exact release artifact from the tested commit**

Run the existing GPTAdmin release workflow for the current commit, record the commit SHA and artifact checksum, and do not copy a working tree directly over the server.

- [ ] **Step 2: Deploy through the existing server-88 service path**

Update the Hub on server 88 using its existing service/deploy mechanism, preserving its current `CTL_TOKEN`, `MCP_RELAY_AGENT_TOKEN`, `ADMIN_PASSWORD`, OAuth secrets, and router configuration. Only add the four `GPTADMIN_SECRET_*` settings if the deployment path does not already supply secure defaults.

- [ ] **Step 3: Verify the public path end to end**

From an authorized MCP client, call `secret_request`; open the exact returned URL; submit a disposable test value; poll `secret_status`; run a disposable `shell_exec` with `secret_env`; verify the target receives the value; verify MCP result, job inspection, audit output, and logs do not contain it; verify a second browser submission returns consumed/expired; verify 2.1 remains excluded from remote MCP routing.

- [ ] **Step 4: Record evidence**

Record the exact commit/artifact identity, public Hub URL, service state, health response, focused test result, and redaction probe path in the project worklog without recording the test value or any live credentials.

---

## Self-review

- One-time browser entry, TTL, ownership, encryption at rest, MCP metadata, server-side injection, redaction, documentation, and live server-88 verification each have an explicit task.
- No task introduces a plaintext fallback, generic secret reader, or router-wide exception.
- The `secret_request`/`secret_status` names and `secret_env` argument are consistent across store, MCP, tests, docs, and live acceptance.
