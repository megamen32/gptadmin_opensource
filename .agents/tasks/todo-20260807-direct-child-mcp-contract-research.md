# Direct child MCP contract research

## Role

Explorer. Read-only; do not edit production source.

## Goal

Determine the smallest existing Go Hub/ShellMCP contract changes needed to publish configured child MCPs as direct `/server/{slug}/mcp` endpoints while routing through the parent ShellMCP.

## Owned paths

Read-only: `go-hub/internal/hub`, `go-shellmcp/internal/server`, and their tests.

## Acceptance check

Return exact symbols/files, identity and slug collision risks, and a bounded test plan. Stop after evidence; do not propose unrelated architecture.

## Estimates

10 / 15 / 25 active minutes.

## Explorer evidence (2026-08-07)

Finding: the current direct endpoint contract is Hub-owned and already routes
configured ShellMCP hosts, but it has no representation of ShellMCP child
MCPs. `go-hub/internal/hub/server.go:853-908` registers `/server/` to
`serverMCPEndpoint`; `:5367-5380` parses only one slug and tail; and
`:5333-5352` resolves against `hub`, virtual agents, and `s.agents` only.
`Agent` at `:217-226` has no parent/child/ref fields. Therefore the smallest
contract extension must add a child identity mapping (parent ShellMCP
`shell:<id>` + child `ref`) to Hub's exposed-agent resolution and dispatch,
while keeping the public URL `/server/{slug}/mcp`.

Current parent routing is already reusable: `:5730-5735` initializes
`shell:*` locally, `:5791-5803` advertises `shellTools`, and `:5821-5835`
routes tool calls through `callShellToolWithTraceParent`. This means child
publication can route through the parent ShellMCP's existing `mcp_tools` /
`mcp_call` tools, rather than adding a new transport in Hub.

ShellMCP owns child configuration and sessions. `go-shellmcp/internal/server/server.go:263-271`
loads `supervisor.Agent` definitions from `MCPConfig`; `:300-304` validates
cached sessions by exact `reflect.DeepEqual` against the manager definition.
`go-shellmcp/internal/server/mcp.go:543-574` resolves exact child `ref`, then
uses `childMCP.ListTools` / `CallTool`. The HTTP handler at
`go-shellmcp/internal/server/server.go:380-396` exposes only `/mcp` and the
legacy `/capabilities/mcp/{ref}` compatibility API; it does not expose
`/server/{slug}/mcp` itself.

Identity and collision risks: child refs are exact, trimmed strings and are
the supervisor primary key (`go-shellmcp/internal/supervisor/supervisor.go:35-64`,
`:263-290`), while Hub slugs are lossy lowercase alphanumeric/dash forms
(`go-hub/internal/hub/server.go:5308-5331`). Hub resolution also matches
case-insensitively, normalized, and compact aliases (`:5339-5348`), so
`foo-bar` vs `foobar`, name vs ID, or a child slug colliding with an existing
Hub/virtual/host slug can resolve ambiguously. Do not silently first-match;
publication needs deterministic collision rejection or a namespace/prefix,
and the parent host must be part of the internal identity even if the public
slug is short.

Bounded test plan: (1) Hub unit tests for child descriptor resolution and
collision rejection across existing host, virtual, and child aliases;
(2) Hub endpoint test for GET card and POST initialize/tools/list/tools/call
at `/server/{slug}/mcp`, asserting calls reach the selected parent and exact
child ref; (3) negative tests for unknown slug, disabled/missing parent,
missing child, and ambiguous normalized slug; (4) ShellMCP integration test
with a fake child MCP proving `mcp_tools` and `mcp_call` are the parent-side
contract, while preserving existing `/mcp` and `/capabilities/mcp/{ref}`;
(5) auth/access-policy test proving the direct endpoint cannot bypass the
parent `shell:<id>` authorization and trace propagation.

Checked: only the assigned Hub and ShellMCP server paths plus their tests and
the directly referenced supervisor/mcpclient definitions. Excluded deployment,
config manifests outside those paths, and unrelated architecture.
