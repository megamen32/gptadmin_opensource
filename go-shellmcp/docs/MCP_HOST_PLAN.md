# MCP host implementation plan

1. Define one persistent child-server model covering local stdio, Streamable
   HTTP, and SSE transports. Omitted `enabled` means enabled.
2. Expose a compact standard-MCP API: `mcp_manage` for CRUD, enable/disable,
   restart, status and config; discovery and call tools for using child
   MCP servers. Do not expose actions that are placeholders.
3. Implement persistence and lifecycle with atomic config writes. The child MCP
   client owns each active local protocol process; the registry owns desired
   configuration only. Remote servers are configured endpoints and do not bind
   a local port.
4. Implement MCP client sessions and tool discovery/calls for each transport,
   usable through ShellMCP's standalone MCP endpoint and through Hub polling.
5. Apply a shared disk budget of `min(500 MiB, 5% filesystem capacity)` to
   logs, audit, spill, outbox, and child-server output, with bounded per-file
   rotation and cleanup.
6. Verify Linux tests, race-sensitive lifecycle behavior, Darwin cross-build,
   constrained-host behavior, and end-to-end local/remote MCP use.
7. Document installation/configuration and record breaking API changes.

Development is test-driven: add a failing runtime-contract test before each
behavioral implementation, then run the narrow package suite and the full Go
module suite.

## Implemented contract

ShellMCP exposes standard MCP directly and can also receive the same tool calls
through optional Hub long polling. Child definitions use `stdio`,
`streamable-http`, or `sse`. `mcp_manage` persists lifecycle state;
`mcp_tools` and `mcp_call` perform child discovery and invocation. Stdio
sessions are reused until their definition is changed, disabled, removed, or
the ShellMCP process exits.

Disposable spill, outbox, audit and managed-backup data share one
`min(500 MiB, 5% of filesystem capacity)` budget. Child stderr retained in
memory is independently capped at 64 KiB.
