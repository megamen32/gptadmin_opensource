# MCP relay arbitrary-arguments delivery plan

## Goal

Ensure every GPTAdmin entry point for `callMcpTool` forwards a selected MCP
tool's complete argument object unchanged.  This restores support for tools
such as OpenMemory's `openmemory_store_project`, whose schema uses fields other
than the legacy `cmd`, `query`, and `cwd` shortcuts.

## Non-goals

- Do not change the selected target, tool name, timeout, or background-job
  controls.
- Do not add per-tool allowlists or OpenMemory-specific behavior.
- Do not alter the MCP transport's JSON-RPC envelope.

## Required behavior

1. The REST relay accepts `arguments`, `args`, or arbitrary non-control
   top-level fields and sends the resulting object as `tools/call.arguments`.
2. The MCP Apps/JIT path (`call_mcp_tool` / `callMcpTool`) follows the same
   precedence and forwarding rule as the REST relay.
3. Explicit `arguments` takes precedence over `args`, which takes precedence
   over arbitrary top-level fields.
4. Control fields are never forwarded as MCP tool arguments.
5. A regression test uses OpenMemory-shaped fields (`content`, `project_id`,
   `tags`, `metadata`, and `type`) and verifies the queued relay request.

## Delivery tasks

1. Add the failing Go regression test for the MCP Apps/JIT route.
2. Share the existing control-field filtering helper with that route; remove
   the divergent nested-arguments-only behavior.
3. Run focused and full Go hub tests, then the project test suite.
4. Audit and repair failing GitHub Actions CI/CD configuration based on the
   current workflow failures.
5. Audit Windows CI and validate the Go ShellMCP's Windows build/runtime
   contracts with platform-appropriate tests.
6. Record verification results and breaking-change status in this document.

## Worklog

- 2026-07-14: Specification recorded before implementation.  Initial code
  inspection found that the REST handler already supports generic top-level
  fields, while the MCP Apps/JIT `appsSDKCall` route reads only `arguments`.
  The latter is the likely loss point described by the incident.
- 2026-07-14: Added a failing OpenMemory-shaped MCP Apps/JIT test, then made
  that path follow the REST relay precedence (`arguments`, `args`, then
  non-control top-level fields). The generic Apps tool schema now permits the
  latter form.
- 2026-07-14: Windows audit found that the installer wrote deprecated Python
  environment names, causing the Go service to use webhook mode instead of
  polling. Added installer and Go configuration-contract regression tests,
  corrected the emitted names, and added a Windows CI job for compilation plus
  the real polling/no-listener contract. Full Windows process execution tests
  remain a follow-up because several legacy tests currently embed POSIX shell
  commands.
- 2026-07-14: Website-submodule CI failure is external configuration: the
  required `GPTADMIN_BOT_PAT` secret is absent. The workflow now fails before
  checkout with precise required permissions; repository administration must
  add the secret to make scheduled runs succeed.
