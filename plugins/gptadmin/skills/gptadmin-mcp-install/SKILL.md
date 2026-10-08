---
name: gptadmin-mcp-install
description: Discover targets, install child MCP servers, and run commands through a selected GPTAdmin target with schema-first and profile-scoped execution.
---

# Manage targets and child MCPs through GPTAdmin

GPTAdmin is the front door for ShellMCP targets and child MCP servers. Use the
Hub's profile and target policy. Do not connect directly to an unapproved child
endpoint when the task is meant to go through GPTAdmin.

The packaged parent endpoint is `https://mcp.bezrabotnyi.com/mcp`. Keep the
parent connection on that canonical OAuth origin.

## Canonical workflow

1. Call `discover` and read the response. Choose an explicit `server_id`.
   Never guess `target="default"` and never infer a target from a display
   label alone.
2. Prefer `status=online` targets. `stale` means the relay has not checked in
   and the call may hang; `failed` means it will not work.
3. Read the target schema with `schema {"target": "<server_id>"}` before
   calling anything. For a ShellMCP target the tools are typically
   `shell_exec`, `mcp_manage`, `mcp_tools`, `mcp_call`.
4. Run work with `execute {"target","tool","args"}`. Pass `idempotency_key`
   when retrying. Use `background=true` for long operations and poll the
   returned `job_id` with the `job` tool.
5. To install a child MCP, use `mcp_manage` with the documented `upsert` action
   on the target. Use the Hub's approval flow for writes. Never pass secrets
   through command arguments or environment values.
6. Verify with `mcp_manage` `status`, read the real `tools/list` through
   `mcp_tools`, and only then call a child tool through `mcp_call`.

## Running a shell command

`execute` on a ShellMCP target:

```json
{"target": "shell:roomhacker-server-100", "tool": "shell_exec",
 "args": {"cmd": "uptime"}}
```

Show the user the exact command before running anything destructive, and get
explicit confirmation. `run_as_user=root` is for intentional root work only.

## Profiles and permissions

A profile can restrict targets, tools, access mode, approvals, write budgets,
and virtual MCPs. If discovery or schema omits a target or tool, treat it as
unavailable. Do not bypass the profile with a direct URL, a legacy bearer, an
admin endpoint, or a guessed tool name.

## Failure handling

Separate these states: OAuth authenticated, target discovered, target online,
tool schema read, and business operation accepted. A successful parent MCP
connection proves none of the later states by itself. Report the actual result
and the target it came from, not just an HTTP acceptance.