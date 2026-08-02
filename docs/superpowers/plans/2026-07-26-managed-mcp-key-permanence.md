# Managed MCP Key Permanence Implementation Plan

> **Superseded on 2026-07-27:** do not implement the no-expiry policy below.
> The active requirement is a five-year lifetime for newly issued managed MCP
> keys and OAuth refresh credentials, while already-issued JWT strings retain
> their signed lifetime and are never silently replaced. OAuth reconnect is
> implemented separately by candidate `b54cbab`; deploy it only after the
> credential acceptance matrix defined in `docs/LIVE_ACCEPTANCE.md` passes.

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make GPTAdmin-issued MCP client keys persist until an operator explicitly revokes or rotates them.

**Architecture:** New managed MCP keys are opaque high-entropy bearer values. The Hub persists only a SHA-256 digest plus authorization metadata and accepts a key only while that record is unrevoked. Existing client JWTs and legacy bearer values are imported by exact digest from the protected environment file, so their current strings survive the old expiry/deadline behavior; short-lived OAuth browser sessions remain separate.

**Tech Stack:** Go Hub, Python installer/CLI, JSON state, pytest, Go test.

## Global Constraints

- Never print bearer keys, signing secrets, or persisted credential values.
- Ordinary setup, update and client auto-configuration must not revoke, replace, or shorten a pre-existing client key.
- Explicit `/admin/api/clients/{id}` delete, `/admin/api/mcp/tokens/{id}/rotate`, and an explicitly invoked signing-key rotation may invalidate affected credentials.
- Preserve unrelated dirty files in the GPTAdmin checkout.

### Task 1: Permanent managed-token contract

**Files:**
- Modify: `go-hub/internal/hub/server.go`
- Modify: `go-hub/internal/hub/server_test.go`

**Interfaces:**
- Consumes: `issueManagedMCPTokenWithMode(clientID, ttlDays, origin, resource, accessMode, profileID)`.
- Produces: an opaque `gptk_<id>_<secret>` bearer, a SHA-256 digest-only persisted record, and verification that rejects it only after explicit record revocation.

- [ ] **Step 1: Write the failing Go regression**

```go
func TestManagedMCPTokenWithoutExpirySurvivesTimeButNotExplicitRevoke(t *testing.T) {
    now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
    s := New(Config{OAuthClientSecret: "oauth-secret", PublicOrigin: "https://hub.example", MCPResource: "https://hub.example", Now: func() time.Time { return now }})
    token, record, err := s.issueManagedMCPTokenWithMode("codex", 0, "https://hub.example", "https://hub.example", accessModeFull, "")
    if err != nil { t.Fatal(err) }
    now = now.AddDate(20, 0, 0)
    if _, err := s.verifyJWTForRequest(httptest.NewRequest(http.MethodPost, "/mcp", nil), token); err != nil { t.Fatalf("permanent token rejected: %v", err) }
    s.managedMCP[record.ID] = managedMCPToken{ID: record.ID, RevokedAt: now.Unix()}
    if _, err := s.verifyJWTForRequest(httptest.NewRequest(http.MethodPost, "/mcp", nil), token); err == nil { t.Fatal("explicitly revoked token was accepted") }
}
```

- [ ] **Step 2: Run the regression and verify it fails**

Run: `cd go-hub && go test ./internal/hub -run TestManagedMCPTokenWithoutExpirySurvivesTimeButNotExplicitRevoke -count=1`

Expected: FAIL because the current issuer always writes a JWT with `exp` and verifier requires it.

- [ ] **Step 3: Implement the smallest permanent-token branch**

Add `TokenDigest` and `TokenKind` to `managedMCPToken`. Make `issueManagedMCPTokenWithMode` generate an opaque bearer, persist only `sha256(token)`, and return no automatic expiry for `ttlDays == 0`. Add exact opaque-bearer verification that reconstructs claims from an unrevoked stored record. Keep JWT expiry checks for interactive OAuth/session JWTs.

- [ ] **Step 4: Run focused Go verification**

Run: `cd go-hub && go test ./internal/hub -run 'TestManagedMCPTokenWithoutExpirySurvivesTimeButNotExplicitRevoke|TestAdminManagedMCPTokenCanBeListedAndRotated' -count=1`

Expected: PASS.

### Task 2: Installer and client defaults

**Files:**
- Modify: `cli.py`
- Modify: `tests/test_ai_mcp_clients.py`
- Modify: `tests/test_cli_token_deprecation.py`

**Interfaces:**
- Consumes: `make_mcp_bearer_token(env, client_id, ttl_days, access_mode)` and `configure_ai_mcp_clients(env, rotate, clients)`.
- Produces: client keys issued by the Hub without an automatic expiry; an opt-in `--ttl-days` compatibility mode remains only for explicitly requested temporary credentials.

- [ ] **Step 1: Write failing Python regressions**

```python
def test_default_client_token_is_issued_by_the_hub() -> None:
    assert cli.issue_mcp_bearer(_client_env(), "codex")[0].startswith("gptk_")

def test_cli_does_not_advertise_a_legacy_key_deadline() -> None:
    assert "deadline" not in cli.cmd_tokens.__doc__.lower()
```

- [ ] **Step 2: Run the focused Python regressions and verify failure**

Run: `python3 -m pytest tests/test_ai_mcp_clients.py tests/test_cli_token_deprecation.py -q`

Expected: FAIL because client JWTs currently contain `exp` and product copy advertises automatic legacy-key expiry.

- [ ] **Step 3: Implement the defaults**

Make `ttl_days=0` the default in CLI/client issuance and obtain durable client keys from the local Hub issuance endpoint instead of signing them in the CLI. Import saved existing client bearers from the protected environment file by exact digest before enforcing the new durable format. Remove automatic legacy bearer retirement from normal authorization and user-facing command text. Keep explicit `--fresh`, rotate and delete operations as opt-in invalidation mechanisms.

- [ ] **Step 4: Run focused Python verification**

Run: `python3 -m pytest tests/test_ai_mcp_clients.py tests/test_cli_token_deprecation.py -q`

Expected: PASS.

### Task 3: Exit gate

**Files:**
- Modify: `docs/BUGS.md`
- Modify: `docs/WORKLOG.md`

- [ ] Run `cd go-hub && go test ./internal/hub -count=1` and `python3 -m pytest tests/test_ai_mcp_clients.py tests/test_cli_token_deprecation.py -q`.
- [ ] Verify a no-expiry managed key remains valid after a synthetic time jump, then fails only after explicit delete/rotate.
- [ ] Verify a normal installer update keeps its prior `OAUTH_CLIENT_SECRET` and client bearer values unchanged.
- [ ] Replace the active worklog entry and BUG status with exact test results and commit SHA.
