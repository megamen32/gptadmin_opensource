---
name: gptadmin-mcp-install
description: Discover, install, and use child MCP servers through the selected GPTAdmin target with schema-first and profile-scoped execution.
---

# Manage child MCPs through GPTAdmin

GPTAdmin is the front door for child MCP servers. Use the Hub's profile and
target policy; never connect directly to an unapproved child endpoint when the
task is meant to go through GPTAdmin.

## Canonical workflow

1. Read the active GPTAdmin profile or startup instructions and state which
   profile is being used. If no profile is available, ask before selecting a
   write-capable target.
2. Call the Hub's discovery tool and choose an explicit target id. Never guess
   `target="default"` and never infer a target from a display label alone.
3. Read the selected target's schema before calling a tool. For a ShellMCP
   target, inspect `mcp_manage` first.
4. To install a child MCP, use `mcp_manage` with the documented `upsert` action
   and the approved child definition. Use the Hub's approval flow for writes;
   do not pass secrets through command arguments or `--env` values.
5. Verify the child with `mcp_manage` `status`, then use `mcp_tools` to read its
   real `tools/list` response. Only then call a child tool through `mcp_call`.
6. For long-running calls, preserve the returned job id and poll it through the
   Hub's job operation. Report the actual result and target, not just an HTTP
   acceptance.

## Profiles and permissions

A profile can restrict targets, tools, access mode, approvals, write budgets,
and virtual MCPs. If discovery or schema omits a target/tool, treat it as
unavailable. Do not bypass the profile with a direct URL, legacy bearer, admin
endpoint, or guessed tool name.

## Failure handling

Separate these states: OAuth authenticated, target discovered, child process
healthy, child `tools/list` successful, and business operation accepted. A
successful parent MCP connection proves none of the later states by itself.
