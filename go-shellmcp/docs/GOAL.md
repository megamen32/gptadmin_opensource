# ShellMCP MCP host goal

Build Go ShellMCP as a standalone, resource-bounded MCP host that is convenient
for an AI to configure on Linux and macOS without requiring GPTAdmin Hub.

The host must:

- install and manage local stdio MCP servers and remote Streamable HTTP or SSE
  MCP servers through a small, coherent MCP tool surface;
- preserve useful behavior from the retired Python implementation without
  preserving its old APIs when a simpler contract is better;
- expose its management and child-server tools through standard MCP so a user
  can configure ShellMCP directly in Codex or another MCP client;
- keep Hub polling as an optional outbound transport, not a prerequisite for
  standalone operation;
- run on macOS and Linux variants, including constrained Home Assistant hosts;
- rotate every file it owns so total disposable data stays below
  `min(500 MiB, 5% of filesystem capacity)`.

Completion requires runtime tests for local and remote child MCPs, standalone
standard MCP transport, Hub polling compatibility, persistent management,
resource limits, and macOS/Linux build or runtime coverage.
