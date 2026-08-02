# Go-only MCP Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Go ShellMCP the only production owner of configured MCP child sessions and prove Linux, macOS, Windows and Android runtime health.

**Architecture:** `mcp.json` remains the user-owned MCP definition source. The CLI projects those definitions directly into `mcp-supervisor.json`; Go ShellMCP starts and speaks MCP to the real child process in-process. Legacy per-agent Python relay services are retired during migration and are never generated for a Go-supervised install.

**Tech Stack:** Python CLI and pytest, Go ShellMCP, systemd, launchd, Windows Task Scheduler, Android/ADB.

## Global Constraints

- Use TDD for every behavior change and record the RED failure before implementation.
- Do not persist or print Hub URLs, bearer tokens, child MCP headers or machine-specific identifiers in tracked files.
- Queue/long-poll is the default; heartbeat is opt-in and queue mode binds no inbound port.
- Preserve actual child MCP commands, including Python MCP applications; only the Python transport relay is forbidden.
- Keep `public/admin/` as production until the separate React parity gate.

---

### Task 1: Direct Go supervisor projection

**Files:**
- Modify: `tests/test_mcp_relay_setup.py`
- Modify: `cli.py`

**Interfaces:**
- Consumes: Claude-compatible `mcpServers` entries from `_mcp_config()`.
- Produces: `_mcp_sync_go_supervisor_config(cfg)` entries accepted by `supervisor.LoadAgents`: `ref`, `name`, `command`, `args`, `env`, `cwd`, `transport`, `url`, `headers`, `enabled`.

- [ ] Add a regression test whose source MCP command is `npx -y example-mcp` and assert the generated supervisor command is `npx`, not `python` or `generic_stdio_mcp_relay.py`.
- [ ] Run `python3 -m pytest tests/test_mcp_relay_setup.py -k go_supervisor -q`; expect the assertion to fail on the Python relay command.
- [ ] Project the original child definition directly, preserving non-secret fields and environment references.
- [ ] Re-run the focused test and the complete `tests/test_mcp_relay_setup.py` file; expect PASS.

### Task 2: Retire duplicate legacy relay services

**Files:**
- Modify: `tests/test_mcp_relay_setup.py`
- Modify: `cli.py`

**Interfaces:**
- Consumes: generated agent-config paths and `_mcp_manager_cmd('uninstall', ...)` only for one-time legacy cleanup.
- Produces: a Go-supervised `gptadmin mcp install` with no enabled `gptadmin-mcp-*` service and a restarted ShellMCP service.

- [ ] Add a failing test asserting supervised install uninstalls each legacy service before restarting ShellMCP.
- [ ] Run the focused pytest and record the missing uninstall calls.
- [ ] Add `_mcp_retire_legacy_relay_services(cfg, names)` and call it only when `_mcp_go_supervisor_enabled()` is true.
- [ ] Restart the canonical ShellMCP service after the aggregate registry is replaced.
- [ ] Re-run focused and full Python tests.

### Task 3: Remove Python ShellMCP launch surfaces

**Files:**
- Modify: `tests/test_plist_macos.py`
- Create or modify: `tests/test_no_legacy_shellmcp.py`
- Modify: `cli.py`
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`
- Delete: `cli/gptadmin.py`

**Interfaces:**
- Consumes: installed native `BIN_DIR/shellmcp` plus the canonical env file.
- Produces: macOS wrapper that only sources env and `exec`s Go; documentation with Go-only commands; one canonical source CLI (`cli.py`, copied into release artifacts by `tools/build.sh`).

- [ ] Add RED assertions forbidding `_mac_python` fallback for ShellMCP and `python client/shellmcp.py` in active docs.
- [ ] Run the focused tests and confirm both stale paths fail.
- [ ] Remove the ShellMCP Python branch while retaining the minimal env-sourcing wrapper.
- [ ] Replace Python development examples with `go run ./go-shellmcp/cmd/shellmcp-go` or the built Go binary.
- [ ] Remove the stale tracked duplicate CLI; keep build-time generation into `cli/gptadmin.py` inside release artifacts.
- [ ] Run focused docs/installer tests and `git diff --check`.

### Task 4: Live migration and platform acceptance

**Files:**
- Modify: `docs/WORKLOG.md`

**Interfaces:**
- Consumes: the tested CLI and rebuilt Go binaries.
- Produces: one active Go ShellMCP on each reachable platform and Hub list/call evidence.

- [ ] On server-100, regenerate the direct registry, retire every `gptadmin-mcp-*` Python relay unit, restart `shellmcp.service`, and prove nested MCP list/call through Hub.
- [ ] On Mac, deploy the Go-only CLI/binary, kickstart launchd, require no new `401`, no listener and an online Hub identity.
- [ ] On Android, repair the live ADB bridge target, start the installed Go arm64 binary, and require online Hub registration with no Python process.
- [ ] On each reachable Windows machine, deploy or repair the Go Windows task and require online Hub registration; if a host is unreachable, report that external blocker separately from installer/CI proof.
- [ ] Run Go suites, Python non-e2e, Windows/Android installer tests, cross-build and secret scan; replace the active worklog entry with factual delivery evidence.

