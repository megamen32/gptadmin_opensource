# Fix GPTADMIN MCP registration

## Original request

fix gptadmin это самое важное

## Objective

Make GPTADMIN MCP registration match the native ShellMCP supervisor architecture and prevent accidental overwrites or secret persistence during Memos-style MCP registration.

## Business canary

A registration dry-run writes only the aggregate supervisor descriptor, preserves existing entries unless explicitly forced, and never serializes credential values into GPTADMIN config or generated status output.

## Confirmed scope

- `mcp-add`
- `cli.py` MCP add/config path
- `MCP_SERVERS.md`
- focused MCP registration tests

## Explicit exclusions

- No remote changes on `roomhacker-server-100`.
- No Hermes config or credential changes.
- No service restart, deployment, or production registration.

## Estimate

- Initial active-minute estimate: 35 minutes.

## Red evidence

- Added TDD checks initially failed because `mcp-add` passed implicit `--force` and docs described per-MCP systemd units.

## Implementation

- Removed the wrapper's implicit overwrite flag.
- Made the helper explicitly run `mcp install NAME` and `mcp status NAME` after registration.
- Updated usage and `MCP_SERVERS.md` to describe the aggregate native ShellMCP supervisor.
- Documented the secret-safe launcher rule instead of persisting secrets through `--env`.
- Added regression tests for non-destructive helper behavior and architecture documentation.

## Green evidence

- `uv run pytest tests/test_mcp_relay_setup.py -q` -> `17 passed`.
- `uv run pytest tests/test_mcp_*.py tests/test_shellmcp_contract.py -q` -> `31 passed`.
- `bash -n mcp-add` passed.
- `git diff --check` passed.

## Follow-up skill

- Added `skills/gptadmin-mcp-testing/SKILL.md` with the inspect, TDD, secret-safety, topology, client-routing, and fail-closed acceptance workflow.
- Remote execution was blocked because the GPTADMIN connector was not exposed in this session; no server mutation was claimed.
