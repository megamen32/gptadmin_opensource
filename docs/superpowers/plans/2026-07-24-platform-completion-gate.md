# GPTAdmin Platform Completion Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven development or execute this plan task-by-task with a review gate after each task.

**Goal:** Turn the currently implemented Hub, ShellMCP, proxy/tunnel, MCP, file-sharing, profile, policy, security, endpoint and webhook surfaces into one verifiable release candidate with automated black-box checks and one mergeable integration commit.

**Architecture:** Preserve the production `public/admin/` surface and existing Go boundaries. Add one language-neutral acceptance harness that starts real Go processes and probes their HTTP/MCP contracts, then close only the implementation gaps exposed by that harness. Keep operator-owned webhook configuration and durable delivery state behind explicit configuration; all security decisions remain fail-closed and profile-scoped.

**Tech Stack:** Go modules (`go-hub`, `go-shellmcp`, `go-proxyrelay`), Python `pytest`, shell release scripts, JSON/YAML contract fixtures.

## Global Constraints

- Treat `/home/roomhacker/gptadmin` as the only source tree; do not merge private workspace content or machine-specific paths into public docs.
- Preserve the existing dirty files and the untracked remote-secret plan unless a task explicitly owns them; do not discard user work.
- Use TDD for behavioral changes: write and run a failing regression before implementation, then focused and full verification.
- Product vocabulary remains Hub, MCP clients and Tunnel; do not expose internal token names in normal user-facing setup text.
- Read-only profiles must not gain shell execution, arbitrary MCP calls, admin API access or unsafe filesystem traversal.
- No new compatibility shim or silent fallback; explicit invalid configuration must fail closed.
- Do not mark a milestone complete from source inspection alone; record exact commands and artifacts in `docs/WORKLOG.md`.

### Task 1: Establish the completion matrix and baseline evidence

**Files:**
- Create: `tests/test_completion_matrix.py`
- Create: `tests/fixtures/completion-matrix.json`
- Modify: `docs/PROJECT_PLAN.md`
- Modify: `docs/WORKLOG.md`

**Interfaces:**
- Consumes: existing Go contract fixtures and process helpers in `tests/conftest.py`, `tests/test_hub_contract.py`, and `tests/test_shellmcp_contract.py`.
- Produces: a machine-readable matrix covering proxy, endpoints, hooks/webhooks, MCP forwarding, file sharing, profiles, policies and security, plus a baseline report that names skipped external-runtime gates.

- [ ] **Step 1: Write the failing matrix test**

  Add one parametrized test per required surface. Each case must declare its command, expected exit code and evidence label; the test fails when a required case is missing or marked `skip` without a reason.

- [ ] **Step 2: Run the matrix to verify the RED state**

  Run `python3 -m pytest tests/test_completion_matrix.py -q`. Expected: FAIL because the fixture and required runtime cases are not yet complete.

- [ ] **Step 3: Add only the matrix data and existing-command adapters**

  Map each case to an existing focused test or black-box command. Do not change production behavior in this task.

- [ ] **Step 4: Run focused and baseline suites**

  Run `python3 -m pytest tests/test_completion_matrix.py -q` and the project’s Go/Python commands. Capture counts and remaining failures in `docs/WORKLOG.md`.

- [ ] **Step 5: Update plan statuses only where evidence exists**

  Keep planned milestones planned; change only statuses directly proven by the matrix.

### Task 2: Complete the real Hub/ShellMCP/proxy/MCP contract gate

**Files:**
- Modify: `tests/test_hub_contract.py`
- Modify: `tests/test_shellmcp_contract.py`
- Modify: `tests/test_tunnels.py`
- Modify: `tests/test_mcp_relay_setup.py`
- Modify: `go-hub/internal/hub/*_test.go` only for missing real-contract regressions
- Modify: `go-shellmcp/blackbox/*_test.go` only for missing real-contract regressions

**Interfaces:**
- Consumes: real binaries built from `go-hub`, `go-shellmcp`, and `go-proxyrelay`.
- Produces: reproducible tests for health/version, auth, global and per-server MCP list/call, action proxy, tunnel reachability, signed relay grants, policy rejection and result propagation.

- [ ] **Step 1: Add one failing black-box regression for each uncovered contract**

  Exercise real HTTP or MCP JSON-RPC behavior, not helper functions. Assertions must include status code, response shape, authorization boundary and upstream/result identity.

- [ ] **Step 2: Run each new test and confirm the expected failure**

  Use the narrowest pytest or Go `-run` selector and record the exact failed contract.

- [ ] **Step 3: Implement the smallest production fix**

  Preserve explicit target allowlists, signed grant validation, nonce/replay handling, redaction and bounded timeouts.

- [ ] **Step 4: Run focused black-box tests, then all Go tests**

  Run `cd go-hub && go test -race ./...`, `cd go-shellmcp && go test -race ./...`, `cd go-proxyrelay && go test ./...`, and the selected Python contract tests.

### Task 3: Finish security/profile/file-sharing acceptance

**Files:**
- Modify: `go-hub/internal/hub/access_profiles_test.go`
- Modify: `go-hub/internal/hub/access_policy.go` only if a failing runtime test proves a gap
- Modify: `go-hub/internal/hub/access_profiles.go` only if a failing runtime test proves a gap
- Modify: `go-shellmcp/internal/inspect/inspect_test.go`
- Modify: `go-shellmcp/internal/shell/fsmeta_test.go`
- Modify: `tests/test_readonly_mode.py`
- Modify: `tests/test_no_secrets.py`
- Modify: `docs/ADMIN_PROFILES.md` and `docs/READONLY_MODE.md` only to document verified behavior

**Interfaces:**
- Consumes: profile persistence, live refresh, effective tool filtering, capability policy, read-only inspection and fs metadata/backup APIs.
- Produces: black-box proof that forbidden calls fail, allowed calls carry policy decisions into audit events, symlinks/credential paths are rejected, file sharing is bounded and profile state survives restart.

- [ ] **Step 1: Add failing regressions for forbidden tool, forbidden path, symlink escape, secret redaction and restart persistence**
- [ ] **Step 2: Run focused tests and confirm each fails for the intended reason**
- [ ] **Step 3: Implement fail-closed fixes without adding fallback paths**
- [ ] **Step 4: Run focused Go/Python tests and inspect audit evidence for policy fields**

### Task 4: Complete webhook/event gateway delivery state and endpoint coverage

**Files:**
- Modify: `go-hub/internal/hub/webhook_gateway.go`
- Modify: `go-hub/internal/hub/webhook_gateway_test.go`
- Modify: `go-hub/internal/hub/server.go`
- Create or modify: `go-hub/internal/hub/webhook_store.go`
- Create or modify: `docs/WEBHOOKS.md`
- Modify: `tests/test_hub_contract.py`

**Interfaces:**
- Consumes: existing route token/HMAC authentication, JSON template rendering, MCP/prompt/Shell dispatch and callback behavior.
- Produces: route-scoped CRUD, durable job/delivery state, replay-safe idempotency, authenticated status lookup, bounded callback retries and restart recovery.

- [ ] **Step 1: Add failing tests for restart recovery, route CRUD authorization, duplicate delivery and callback retry bounds**
- [ ] **Step 2: Run focused Go tests and confirm RED**
- [ ] **Step 3: Implement durable state with atomic writes and restrictive file permissions**
- [ ] **Step 4: Run `go test -race ./...` and real HTTP endpoint probes**
- [ ] **Step 5: Document operator configuration without secrets or private URLs**

### Task 5: Release gate, clean integration vertex and handoff

**Files:**
- Modify: `tools/build.sh` or `.github/workflows/build-and-sync.yml` only if release evidence is missing
- Modify: `tests/test_installer_manifest.py`, `tests/test_public_mirror.py`, or related contract tests only for proven gaps
- Modify: `docs/PROJECT_PLAN.md`
- Modify: `docs/WORKLOG.md`

**Interfaces:**
- Consumes: all focused and full test evidence plus clean-build artifacts.
- Produces: one intentional commit on the current feature branch, a clean-tree/clean-clone verification record, and a single compare target ready to merge into `main`.

- [ ] **Step 1: Run all focused gates and fix any regressions test-first**
- [ ] **Step 2: Run the complete non-e2e Python suite and every Go module suite**
- [ ] **Step 3: Run release/build and cross-compilation checks**
- [ ] **Step 4: Verify public/private boundary and absence of secrets in tracked tree/history**
- [ ] **Step 5: Update the worklog with exact source paths, commit and one next action**
- [ ] **Step 6: Create one final integration commit only after all gates pass**

## Self-review

- The matrix covers every capability named by the request and separates implementation evidence from unavailable external-host evidence.
- Every behavior-changing task starts with a failing regression and ends with focused plus full verification.
- Existing uncommitted webhook and documentation changes remain in scope and are not silently discarded.
- The final output is one branch tip/commit suitable for merging into `main`; no unrelated branch merge is performed.
