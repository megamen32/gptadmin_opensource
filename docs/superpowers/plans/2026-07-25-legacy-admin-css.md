# Legacy Admin CSS Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use test-driven-development to implement this plan task-by-task.

**Goal:** Make the packaged `/admin/legacy/` console load its CSS and JavaScript after the React admin cutover.

**Architecture:** Keep `public/admin/` as the source payload that the release builder copies to `public/admin-legacy/`. Use document-relative static asset URLs so the same payload works at both `/admin/` and `/admin/legacy/`; keep API URLs absolute because they remain Hub routes. Prove the contract in the existing Python release tests and the Go static handler test.

**Tech Stack:** Go `net/http`, Python `pytest`, vanilla HTML/CSS/JavaScript, shell release packaging.

## Global Constraints

- Preserve `public/admin/` as the production legacy source until the explicit React parity gate.
- Do not expose or change credentials, tokens, private URLs, or deployment configuration.
- Use TDD: record a failing regression before changing the asset references.
- Preserve unrelated dirty files, including `.vscode/settings.json` and the existing remote-secret-ingress plan.

---

### Task 1: Lock the legacy asset URL contract

**Files:**
- Modify: `tests/test_admin_ui_release_contract.py`
- Modify: `go-hub/internal/hub/server_test.go`

- [x] **Step 1: Add a Python regression requiring document-relative `style.css` and `app.js` references in the legacy source.**
- [x] **Step 2: Add Go coverage that authenticated `/admin/legacy/style.css` is served as CSS from the packaged legacy directory.**
- [x] **Step 3: Run the focused tests and confirm RED because the source currently points at `/admin/style.css` and `/admin/app.js`.**

### Task 2: Correct source asset references

**Files:**
- Modify: `public/admin/index.html`

- [x] **Step 1: Change only the stylesheet and script URLs to `style.css` and `app.js`; leave `/admin/api/...` URLs unchanged.**
- [x] **Step 2: Run focused tests and confirm GREEN.**

### Task 3: Verify release and repository acceptance

**Files:**
- Modify: `docs/BUGS.md`
- Modify: `docs/WORKLOG.md`

- [x] **Step 1: Run the focused Python and Go tests, then `git diff --check`.**
- [x] **Step 2: Run the relevant full Python admin/UI contract tests and Go Hub package tests.**
- [x] **Step 3: Record immutable local RED/GREEN evidence, changed paths, deployment state, and one next action in the append-only bug/work logs.**
