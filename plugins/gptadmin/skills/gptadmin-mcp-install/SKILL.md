---
name: gptadmin-mcp-install
description: Discover machines, run commands, and install or call child MCP servers through the configured GPTAdmin Hub with schema-first, profile-scoped execution.
---

# Work through the GPTAdmin Hub

## Precondition — check this before anything else

Confirm the Hub MCP tools are actually available in this session.

- **Present** — continue.
- **Absent** — stop. Say "the GPTAdmin Hub is not connected", and send the user
  to `gptadmin-connect`.

Never fall back to a token in a local config file, a direct script, SSH, or a
different MCP server. That bypasses Hub policy and makes an authorization
problem invisible. Report the missing connection instead.

Everything below goes through the one Hub URL from `servers.mcp.json`. If that
is still the placeholder, see `gptadmin-connect` first — this skill assumes a
real Hub is configured and authorized.

## Discovery

Call the Hub's discovery tool and read the response. It lists every machine and
child MCP with a status:

- `online` — usable
- `stale` — has not checked in recently; calls may hang, prefer another target
- `failed` — will not work as-is

Report the real numbers: how many targets, how many online. Do not summarise
"connected".

Always choose an explicit target id. Never guess `target: default` and never
infer a target from a display label.

## Schema first, then act

1. `schema` with the chosen target to list its tools.
2. Then the operation.

Never call a tool you have not read the schema for. On a ShellMCP host the tools
are typically `shell_exec`, `mcp_manage`, `mcp_tools`, `mcp_call` — but read
them, do not assume.

## Running a command

```
execute
  target: <shell target id>
  tool:   shell_exec
  args:   {"cmd": "<command>"}
```

- Show the user the exact command before anything destructive or
  configuration-changing, and wait for explicit approval.
- Use root only when root is genuinely required.
- Long jobs: pass `background: true`, keep the returned job id, poll it.
- Retries: reuse the same `idempotency_key`.

## Child MCPs

To add one: `mcp_manage` with the documented action on that target, going
through the Hub's approval flow. Never pass secrets as command arguments or
environment values.

To use one: `mcp_manage` status → `mcp_tools` for a real `tools/list` → then
`mcp_call`. Do not skip to calling a tool that was never listed.

## Policy

The active profile may restrict targets, tools, access mode, approvals, write
budgets, and virtual MCPs. If discovery or schema omits a target or tool, it is
unavailable — say so. Do not work around policy with a direct URL, a raw
credential, an admin endpoint, or a guessed tool name.

## Report results honestly

These are different states and must be reported separately:

OAuth authenticated → target discovered → target online → schema read →
operation accepted.

A successful connection to the Hub proves only the first. Name the target and
show the actual output, not just "done".
