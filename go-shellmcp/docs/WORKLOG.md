# ShellMCP MCP host worklog

- 2026-07-12: Goal and implementation plan recorded. Python parity audit found
  lifecycle/config fields, persistent enable/disable, service metadata, log
  paths, and default-enabled semantics missing or incomplete in the Go port.
- 2026-07-12: Chosen direction: standard MCP is the primary interface; Hub
  polling remains an optional outbound transport. One `mcp_manage` tool owns
  configuration/lifecycle; child discovery and calls use separate tools.
- 2026-07-12: TDD fixed omitted `enabled` to mean true for native and legacy
  configs. Added one model for stdio, Streamable HTTP and SSE definitions.
- 2026-07-12: Added atomic persistent registry mutations and wired the first
  complete `mcp_manage` contract: list, upsert, remove, enable, disable,
  restart, status and config. Removed advertised placeholder actions.

- 2026-07-12: Added runtime-tested child MCP client sessions for stdio and remote
  Streamable HTTP/SSE responses. Public `mcp_tools` and `mcp_call` now perform
  real child `tools/list` and `tools/call` without Hub; headers support `${ENV}`.
- 2026-07-12: Full Go suite passes and ShellMCP cross-builds for Darwin amd64
  and arm64 after child MCP integration.
- 2026-07-12: Added a shared disposable-data budget of `min(500 MiB, 5% of
  filesystem capacity)`. Spill and outbox files are pruned oldest-first while
  active results are protected; non-spilled stdout/stderr capture files are
  removed immediately instead of accumulating.
- 2026-07-12: Audit logging now rotates in place at the filesystem-derived
  bound, including when the audit path is outside the main spool root. Full Go
  tests and Darwin amd64/arm64 cross-builds pass after storage controls.
- 2026-07-12: Stdio child MCP sessions are now persistent and serialized per
  configured ref. Repeated discovery/calls reuse one process; disable, remove
  and upsert close stale sessions explicitly.
- 2026-07-12: Hub long-poll shell jobs now carry generic `tool_name` and
  `arguments` while preserving legacy `cmd` jobs. Polling ShellMCP executes
  public MCP tools such as `mcp_tools` and `mcp_call` and posts normal durable
  results through the existing outbox path.
- 2026-07-12: Added signal-aware graceful shutdown. Polling mode now exits on
  context cancellation, HTTP mode performs bounded shutdown, and persistent
  child MCP sessions are closed with the server.
- 2026-07-12: Stale outbox results rejected by Hub with HTTP 404 are removed
  instead of retried forever. Production restart cleared five stale entries.
- 2026-07-12: Deployed updated Go Hub and ShellMCP on roomhacker-server-100.
  Production long-poll smoke test passed end-to-end: `mcp_manage` persisted a
  stdio child, `mcp_tools` discovered `echo`, and `mcp_call` returned
  `echo:production-ok`; the temporary child and definition were then removed.
- 2026-07-12: Completion audit added regression coverage for polling shutdown
  on context cancellation and terminal deletion of Hub-404 stale outbox
  entries. Added standalone installation/configuration documentation for local
  stdio and remote Streamable HTTP/SSE child MCPs.
