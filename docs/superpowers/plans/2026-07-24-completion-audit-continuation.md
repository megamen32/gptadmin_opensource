# GPTAdmin Completion Audit Continuation Implementation Plan

> **For agentic workers:** Execute the tasks inline on the existing integration branch; do not create a parallel worktree or branch.

**Goal:** Finish and verify the remaining GPTAdmin platform gates while preserving one linear, merge-ready vertex.

**Architecture:** Treat `docs/PROJECT_PLAN.md` as the requirement inventory and `tests/fixtures/completion-matrix.json` as the executable evidence index. Each missing behavior gets a failing black-box or runtime regression first, then the smallest implementation in the existing Hub/CLI/ShellMCP boundaries; integration, deploy and native-platform evidence remain distinct from local proof.

**Tech Stack:** Go Hub/ShellMCP/ProxyRelay, Python CLI/tests, vanilla production admin UI, OpenAPI YAML, Docker acceptance fixtures, GitHub Actions.

## Global Constraints

- Keep all implementation on `codex/haos-addon-public`; preserve the unrelated untracked remote-secret plan.
- Use product vocabulary Hub, MCP clients and Tunnel; do not expose internal credential names in normal user surfaces.
- New behavior uses TDD and must be covered by focused and matrix tests.
- Never persist or return raw request arguments, tokens, passwords, workspace contents or telemetry payloads.
- Do not claim native CI, deploy, public publication or clean-host restore from local synthetic tests.

### Task 1: Close the in-progress activation telemetry slice

**Files:** `go-hub/internal/hub/telemetry.go`, `go-hub/internal/hub/telemetry_test.go`, `go-hub/internal/hub/server.go`, `tests/fixtures/completion-matrix.json`, `docs/PROJECT_PLAN.md`, `docs/WORKLOG.md`.

- [x] Write and run the failing opt-in/persistence regression.
- [x] Implement local-only bounded counters and restrictive persistence.
- [x] Run Hub full/race/vet, Python suite, completion matrix and inspect diff.
- [x] Close the active worklog entry and commit the linear slice.

### Task 2: Audit and close remaining Stage 1/2 client and MFA gaps

**Files:** `go-hub/internal/hub/connection_page.go`, `go-hub/internal/hub/security_settings.go`, `public/openapi.yaml`, `docs/AUTH_SIMPLIFICATION.md`, `docs/PROJECT_PLAN.md`.

- [ ] Verify the canonical connection manifest against each Codex, Claude-compatible and ChatGPT-style golden path; keep local auto-configuration evidence separate. Generic manifest-driven `demo` proof is complete, but real client applications remain open.
- [x] Add test-backed MFA recovery and sensitive-setting re-authentication without exposing secrets; passkeys/WebAuthn and OIDC/external verification remain separate open gates.
- [x] Add focused black-box tests and update plan status only from evidence.

### Task 3: Re-run the complete platform acceptance ladder

**Files:** `tests/fixtures/completion-matrix.json`, `tests/test_completion_matrix.py`, `tests/e2e/`, `docs/WORKLOG.md`.

- [x] Run proxy, endpoint, webhook, MCP forwarding, file sharing, profile and policy rows.
- [x] Run Go/Python race, vet, Darwin cross-build, Docker installer and failover E2E where available; the serial failover capture returned `rc=0` with exactly one all-scenarios pass line.
- [x] Record skipped native/deployment lanes with exact proof gaps rather than substituting local builds.

### Task 4: Final completion audit and handoff

- [x] Compare every requested surface and every non-Complete `PROJECT_PLAN` row with an authoritative test/runtime artifact; local proxy/endpoints/hooks/MCP/file-sharing/profile/policy rows and the new S0.3/S3.2 contracts are green, while real client/Tunnel, physical fallback, external MFA, CI publication and other roadmap lanes remain explicitly open.
- [x] Fix actionable bugs found during the audit before handoff; the only remaining open records are the unrelated HAOS Supervisor job and the elevated Windows legacy-task cleanup, both requiring their owning external session.
- [x] Commit all intended changes on the single branch; leave only the known unrelated untracked plan.

### Task 5: Add a verified configuration backup/restore contract

**Files:** `cli.py`, `tests/test_backup_restore.py`, `docs/PROJECT_PLAN.md`, `docs/WORKLOG.md`.

- [x] Write a failing test that creates a temporary config directory with an env file and JSON state, runs backup creation, verifies the archive manifest and rejects a path-traversal archive member.
- [x] Implement `gptadmin backup create <archive>` and `gptadmin backup verify <archive>` using a deterministic manifest with relative paths, byte sizes and SHA-256 digests; preserve restrictive modes and never print file contents.
- [x] Implement `gptadmin backup restore <archive> <target>` with an explicit target directory, traversal/symlink rejection, atomic extraction and post-restore digest verification.
- [x] Run focused Python tests, full Python suite and a clean temporary restore drill; document that live service restart and root ownership remain deployment-specific evidence.
