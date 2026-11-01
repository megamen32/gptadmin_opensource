---
name: gptadmin-mcp-install
description: Discover, install, and use child MCP servers through the selected GPTAdmin target with schema-first and profile-scoped execution.
---

# Manage child MCPs through GPTAdmin

GPTAdmin is the front door for child MCP servers. Use the Hub's profile and
target policy; never connect directly to an unapproved child endpoint when the
task is meant to go through GPTAdmin.

The packaged parent endpoint is `https://mcp.bezrabotnyi.com/mcp`. For this
managed installation, keep the parent connection on that canonical OAuth
origin; an `u-f….t.became.bezrabotnyi.com` FRP address is legacy/fallback
transport only. A separately configured Hub uses its own stable HTTPS origin.

## Canonical workflow

1. Call `search` with a specific query and bounded filters, then read the
   returned profile instructions and choose an explicit target from the result.
   Never guess `target="default"` or infer a target from a display label alone.
2. Use its argument schema and ready-to-run JavaScript call (or scoped handle)
   with `exec({code})`. For child MCP lifecycle work, search for the
   supported management capability and inspect its exact schema in the result.
3. Use the existing approval flow for writes. Never pass secrets through
   command arguments or child environment values.
4. Verify changes using the real child service and its actual `tools/list`
   response, then call child tools through `exec` under the caller's
   profile and authorization.
5. For long-running calls, preserve the returned job id and inspect it through
   `job`; use `expand` for needed receipt detail. Report the actual result and
   target, not just an HTTP acceptance.

`discover` and `schema` remain available as read-only compatibility tools.
Direct public `execute(target, tool, args)` is retired; do not call it or provide
it as a fallback.

## Profiles and permissions

A profile can restrict targets, tools, access mode, approvals, write budgets,
and virtual MCPs. If search or compatibility discovery/schema omits a target/tool, treat it as
unavailable. Do not bypass the profile with a direct URL, legacy bearer, admin
endpoint, or guessed tool name.

## Failure handling

Separate these states: OAuth authenticated, target discovered, child process
healthy, child `tools/list` successful, and business operation accepted. A
successful parent MCP connection proves none of the later states by itself.
