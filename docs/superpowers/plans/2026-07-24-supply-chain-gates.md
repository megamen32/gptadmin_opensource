# Supply-Chain Gates Implementation Plan

> **For agentic workers:** Execute this plan inline on the existing integration branch. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make release installation fail closed without artifact metadata, publish build provenance in CI, and document an actionable vulnerability-response policy.

**Architecture:** Keep the existing SHA-256 release manifest and SPDX SBOM as the local source of truth. The CLI will reject downloaded artifacts when a manifest is unavailable or lacks digest/size metadata, except for the already explicit diagnostic bypass. GitHub Actions will attest the verified release inputs and run dependency vulnerability checks before publication.

**Tech Stack:** Python CLI/tests, GitHub Actions, `actions/attest-build-provenance`, `govulncheck`, npm audit, Markdown policy.

## Global Constraints

- Work on the current linear branch; do not create a worktree or branch.
- Never print artifact contents, secrets or customer data.
- Preserve the unrelated untracked `docs/superpowers/plans/2026-07-24-remote-secret-ingress.md`.
- Keep the explicit `GPTADMIN_UPDATE_SKIP_MANIFEST` escape documented as a diagnostic-only bypass.
- Behavior changes require a failing regression before implementation.

### Task 1: Fail closed on missing release metadata

**Files:**
- Modify: `cli.py`
- Test: `tests/test_update_semantics.py`

- [x] Add a regression asserting `_verify_downloaded_artifact(..., require_metadata=True)` rejects empty metadata.
- [x] Run the focused test and observe the missing contract failure.
- [x] Add the optional strict flag and use it for normal update downloads; preserve the explicit diagnostic bypass.
- [x] Run focused update tests and the full Python suite.

### Task 2: Add CI provenance and vulnerability gates

**Files:**
- Modify: `.github/workflows/build-and-sync.yml`
- Modify: `tests/test_release_workflow_contract.py`

- [x] Add RED assertions for `actions/attest-build-provenance`, `govulncheck`, and npm audit before publication.
- [x] Run the workflow contract test to verify it fails before the workflow change.
- [x] Add job permissions and steps after manifest/SBOM verification and before public publication.
- [x] Run the focused workflow contract test.

### Task 3: Document response and verify the contract

**Files:**
- Create: `docs/SUPPLY_CHAIN.md`
- Create: `tests/test_supply_chain_policy.py`
- Modify: `tests/fixtures/completion-matrix.json`
- Modify: `docs/WORKLOG.md`

- [x] Add policy thresholds, owner actions, artifact verification commands and the diagnostic bypass warning.
- [x] Add tests for required policy sections and completion-matrix coverage.
- [x] Run focused/full acceptance and inspect the diff.
- [x] Commit one linear slice.
