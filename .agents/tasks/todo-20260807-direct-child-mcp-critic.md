# Direct child MCP release critic

## Role

Critic. Independent release gate; do not edit source.

## Contract

Assess whether the selected direct child MCP outcome is complete and safe to release. Use the original goal: configured enabled child MCPs appear under Hub `/server/` with direct Bearer MCP endpoints, while process ownership remains parent ShellMCP. Check current diff, tests, and remaining blockers. Return CONTINUE or STOP/ASK_USER with exact evidence.

## Scope

Current worktree diff in `go-hub` and `go-shellmcp`, focused tests, full Go tests, and release canary readiness.

## Stop condition

Return one independent verdict; no implementation.

## Critic evidence 2026-08-07

- Focused Hub tests passed: `TestDiscoveryPublishesShellMCPChildAsDirectServer`, `TestDirectChildMCPToolsListRoutesThroughParentShell`, and `TestDirectChildMCPToolCallRoutesThroughParentShell` (`go test ./internal/hub -run ... -count=1`).
- Full Go tests passed independently in both modules: `go test ./... -count=1` under `go-hub` and under `go-shellmcp`.
- Python service-template contract passed: `python -m pytest -q tests/test_shellmcp_service_templates.py` => `3 passed`.
- Both binaries build successfully when invoked in their owning modules: `go build ./cmd/gptadmin-hub` and `go build ./cmd/shellmcp-go`.
- `git diff --check` is clean for the scoped files.
- Source review confirms child calls route through `parent_server_id` using parent `mcp_tools`/`mcp_call`; no child process launch or second Hub is introduced by the scoped diff.
- Release/business canary is not complete: the recorded catalog task explicitly says no production restart/deploy has been performed, and this critic found no live heartbeat evidence showing BrowserClaw under `/server/`, no authenticated live `initialize`/`tools/list`/safe `tabs` result, and no process-ownership receipt from the Mac mini runtime.

## Independent verdict

`STOP/ASK_USER`

The implementation and automated evidence are release-candidate ready, but the contract's business canary is unproven. The task explicitly forbids production deployment/restart/public release without user request. Ask the user whether to authorize the bounded Mac mini heartbeat/direct-BrowserClaw canary (including restart if required); do not claim release completion before that evidence exists.
