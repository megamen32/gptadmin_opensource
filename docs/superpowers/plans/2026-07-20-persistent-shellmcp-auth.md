# Persistent ShellMCP Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure ShellMCP keeps the Hub-issued credential across upgrades and cannot be silently redirected to a stale legacy credential source.

**Architecture:** The canonical service reads `/etc/gptadmin/gptadmin.env`. In-place updates preserve the canonical auth material, then remove legacy Go-primary systemd drop-ins that override the service environment. No new credential is generated during update, and explicit token rotation remains the only rotation path.

**Tech Stack:** Python installer/CLI, systemd unit templates, pytest.

## Global Constraints

- Never print or commit tokens, private URLs, customer data, or raw logs.
- Use TDD: add a failing regression test before changing installer behavior.
- Preserve unrelated systemd drop-ins and user configuration.
- Keep Go ShellMCP as the only production runtime.

---

### Task 1: Cover legacy environment override cleanup

**Files:**
- Modify: `tests/test_update_semantics.py`
- Modify: `cli.py:629-635`

**Interfaces:**
- Consumes: `_cleanup_obsolete_runtime_files()` and its `SYSTEMD_DIR`/`SYSTEMD_SHELLMCP` paths.
- Produces: cleanup of every legacy `*go-primary*.conf` drop-in while preserving unrelated drop-ins.

- [ ] **Step 1: Write the failing test**

Add a second legacy override beside the existing one and assert both are removed while `80-spool-readable.conf` remains.

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `python3 -m pytest tests/test_update_semantics.py::test_cleanup_removes_obsolete_shellmcp_primary_override -q`

Expected: FAIL because `95-go-primary.conf` remains.

- [ ] **Step 3: Implement the minimal cleanup**

Replace the single fixed-path unlink with a glob restricted to `*go-primary*.conf` inside the ShellMCP drop-in directory.

- [ ] **Step 4: Run focused and adjacent tests**

Run: `python3 -m pytest tests/test_update_semantics.py tests/test_shellmcp_service_templates.py -q`

Expected: PASS.

- [ ] **Step 5: Run the project verification**

Run: `python3 -m pytest tests/ --ignore=tests/e2e`

Expected: all non-e2e tests pass with no auth secrets in output.

### Task 2: Record the security contract and live recovery

**Files:**
- Modify: `docs/CONFIGURATION.md` (auth persistence/update note)
- Modify: `docs/WORKLOG.md` (append active then completed factual entry)

**Interfaces:**
- Consumes: canonical `gptadmin.env` auth contract and the cleanup behavior from Task 1.
- Produces: operator-facing explanation that updates preserve credentials and that explicit rotation is separate.

- [ ] **Step 1: Add the contract documentation**

Document that an authenticated ShellMCP client may receive queued sensitive work, so an unknown client must not be auto-approved; queue polling is authenticated by the persistent credential; upgrades must not rotate it.

- [ ] **Step 2: Verify documentation and tests**

Run: `python3 -m pytest tests/test_update_semantics.py -q`.

- [ ] **Step 3: Apply the live repair only after code verification**

On `server-88`, remove the stale legacy Go-primary override, restart `shellmcp.service`, and verify Hub status transitions to `online` and a harmless `tools/list` call succeeds. Do not print secrets.

### Task 3: Make standalone installers idempotent

**Files:**
- Modify: `deploy/install_shellmcp.sh`
- Modify: `deploy/install_android.sh`
- Test: `tests/test_install_scripts.py`

**Interfaces:**
- Consumes: existing `SHELLMCP_TOKEN`/Hub URL from the configured state file or environment.
- Produces: repeatable installs that reuse the credential and device name instead of generating a new one on every run.

- [ ] **Step 1: Write the failing installer contract tests**

Assert that the Android installer reads `ENV_FILE` before generating values and that standalone Linux persists a token file without printing the raw token.

- [ ] **Step 2: Run focused tests to verify they fail**

Run: `python3 -m pytest tests/test_install_scripts.py::test_android_installer_reuses_existing_shellmcp_credentials tests/test_install_scripts.py::test_standalone_shellmcp_installer_persists_credentials_without_printing_them -q`

Expected: both tests fail against the unconditional-generation implementation.

- [ ] **Step 3: Implement idempotent state loading and persistence**

Load existing Android `HUB_URL`, `SHELLMCP_TOKEN`, and `SHELLMCP_NAME`; make the standalone installer use a mode-600 token file and redact status output.

- [ ] **Step 4: Run focused and full tests**

Run: `python3 -m pytest tests/test_install_scripts.py tests/test_update_semantics.py -q` and then the full non-e2e suite.

Expected: all pass.
