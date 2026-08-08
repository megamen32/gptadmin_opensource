---
name: gptadmin-mcp-testing
description: Verify a Memos-style MCP integration through GPTADMIN without duplicating the backend, leaking credentials, or confusing the native ShellMCP supervisor with legacy per-agent services.
---

# GPTADMIN MCP Testing

Use this workflow when an MCP is visible in one client, especially Hermes, but
is expected to be centrally available through GPTADMIN.

## Preconditions

- The real GPTADMIN connector must expose `listMcpAgents`, `callMcpTool`, and
  the target `shell:*` execution surface. A local checkout or GitHub Actions
  result is not server proof.
- Identify the target from live inventory; for this project the expected target
  is `shell:roomhacker-server-100`.
- Treat Memos as an existing backend. Do not create a second database or copy
  credentials/data.

## Inspect

1. Snapshot the target configuration and service state before mutation.
2. Locate the existing Hermes Memos adapter, its actual interpreter/venv,
   command, working directory, environment-file path, transport, and process
   owner. Redact values, tokens, cookies, and authorization headers.
3. Check whether Hermes currently owns the only Memos MCP process and whether
   GPTADMIN already has a Memos entry. Record process fingerprints, not just
   filenames.
4. Inspect GPTADMIN's native registry:
   - `/etc/gptadmin/mcp.json`
   - `/etc/gptadmin/mcp-supervisor.json`
   - `/etc/gptadmin/gptadmin.env`
   - `systemctl status shellmcp.service`

Do not assume `gptadmin-mcp-Memos.service` exists. Native GPTADMIN uses the
aggregate ShellMCP supervisor; a per-MCP legacy unit is evidence to investigate,
not the expected architecture.

## TDD gate

Run the repository tests before touching the target. Add a failing test for each
new contract, then implement and rerun it:

- registration refuses overwrite unless `--force` is explicit;
- supervisor config contains the Memos launcher/ref but no credential value;
- a fake stdio Memos server answers `tools/list` and one read-only search;
- one logical Memos process is not registered simultaneously by Hermes and
  ShellMCP;
- a client using `/server/hub/mcp` does not receive a second direct Memos entry.

## Secret-safe deployment

- Back up every target config with a timestamp before mutation.
- Put launcher code under `/opt/gptadmin/mcp-servers/memos/` when the original
  `/root/hermes-agent` path is not appropriate for the supervisor user.
- Keep credentials in a root-readable `EnvironmentFile` with mode `0600`.
- The launcher may read that file and `exec` the existing adapter; never put
  secret values in `mcp.json`, `mcp-supervisor.json`, client configs, command
  arguments, or status output.
- Use GPTADMIN's native `mcp add` plus `mcp install`; do not create a parallel
  standalone relay unit.

## Smoke canary

Run in this order and preserve raw receipts without secrets:

1. `systemctl is-active shellmcp.service`.
2. GPTADMIN `listMcpAgents`; confirm exactly one Memos ref and its status.
3. `tools/list` through the Memos route or full hub route.
4. One bounded, read-only Memos search using a fixed canary query.
5. Recheck Hermes and GPTADMIN process inventories; assert no duplicate Memos
   process and no duplicate write path.
6. Check permissions on configs/env files and scan journal/status receipts for
   token-shaped or credential values.
7. Verify OpenCode, Codex, Claude Code, Hermes, and Matrix agents by their
   effective configured route. If a client already uses the full hub, do not
   add a direct Memos MCP.

## Acceptance

The change is complete only when the requested business route is live, the
read-only canary succeeds through that route, the process topology has one
owner, and secret-safety checks pass. A green unit test, service status, or
`tools/list` alone is not sufficient proof.

## Failure handling

If the GPTADMIN connector is unavailable, stop at local TDD and document the
blocker. Do not claim server mutation, endpoint availability, client
visibility, or cleanup. Resume from the inspect stage when the connector is
actually exposed.
