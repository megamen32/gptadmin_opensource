# Retire Legacy CLI Token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `CTL_TOKEN`/legacy CLI bearer from normal GPTAdmin operation and enforce a fixed migration deadline of 2026-07-27 UTC.

**Architecture:** Keep the existing OAuth JWT and AdminPassword session paths as the supported auth mechanisms. During the one-week compatibility window, accept the legacy bearer only with explicit deprecation/sunset metadata; after the deadline, reject it in both admin and MCP auth paths. CLI and product surfaces stop presenting or rotating the legacy bearer immediately.

**Tech Stack:** Go Hub, Python CLI, vanilla admin UI, pytest, Go tests, Markdown docs.

## Global Constraints

- The legacy cutoff is `2026-07-27T00:00:00Z`.
- No raw secrets may be printed, logged or written to the worklog.
- TDD is mandatory: each behavior starts with a failing regression test.
- Existing OAuth/admin-session and ShellMCP transport credentials remain in scope.

### Task 1: Hub legacy-auth cutoff

**Files:**
- Modify: `go-hub/internal/hub/server.go`
- Test: `go-hub/internal/hub/server_test.go`

- [ ] Add tests proving the legacy bearer is accepted with a `Sunset` header before the deadline and rejected after it for both `/admin/api/overview` and `/mcp`.
- [ ] Run the focused tests and verify they fail because the Hub currently accepts the bearer without a cutoff.
- [ ] Add a fixed UTC cutoff helper and apply it to `requireCtl`, `mcpAuth`, and the legacy admin-password fallback; preserve OAuth/admin-session behavior.
- [ ] Run focused Go tests, then the complete Hub suite.

### Task 2: CLI migration boundary

**Files:**
- Modify: `cli.py`
- Test: `tests/test_cli_token_deprecation.py`

- [ ] Add tests proving `doctor`, `tokens`, and `rotate hub` do not print or rotate `CTL_TOKEN`, and that migration status reports the exact deadline without secret material.
- [ ] Run the focused tests red.
- [ ] Remove legacy token display/rotation from normal commands and add a non-secret migration notice pointing to OAuth/AdminPassword.
- [ ] Run the focused Python tests and the full Python suite.

### Task 3: Installer/admin/docs contract

**Files:**
- Modify: `public/admin/index.html`, `public/admin_dashboard.html`, `public/admin/app.js`, `docs/AUTH_SIMPLIFICATION.md`, `docs/CONFIGURATION.md`, `README.md`
- Test: `tests/test_admin_ui.py`, `tests/test_cli_token_deprecation.py`

- [ ] Add contract assertions that normal UI/docs no longer instruct users to copy or rotate the legacy bearer and that the fixed deadline is visible.
- [ ] Run them red.
- [ ] Replace legacy copy with AdminPassword/OAuth connection guidance while keeping advanced migration diagnostics explicit.
- [ ] Run UI/docs tests and `git diff --check`.

### Task 4: Verification and delivery

- [ ] Run `python3 -m pytest tests/ --ignore=tests/e2e`.
- [ ] Run `cd go-hub && go test ./...` and `cd go-shellmcp && go test ./...`.
- [ ] Update this worklog entry with factual evidence, commit, CI and one next action.
