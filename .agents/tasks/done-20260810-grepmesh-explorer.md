# GrepMesh MCP GPTAdmin seam reconnaissance

Role: Explorer
Status: todo
Parent task: /home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-mcp.md

## Original request

Implement a future GrepMesh MCP project that registers with GPTAdmin Discover
but serves local and federated file search directly from each host.

## Bounded objective

Map the existing GPTAdmin repository seams relevant to GrepMesh: discovery
registration/readback, managed MCP metadata, install/upgrade/health control,
test harnesses, and the safest isolated location for a new Rust project.
Determine whether any existing component already owns the requested behavior.

## Allowed scope and paths

- Read-only inspection of `/home/admin/gptadmin` source, docs, tests,
  manifests, and task history.
- Do not edit source, docs, ROADMAP, production state, or any existing dirty
  file. Append the final report only to this task file.
- Do not use or alter secrets, ACLs, mTLS, firewall, permissions, or deploy/
  restart actions.

## Acceptance proof for this lane

Return a compact file/line map with confirmed owners, proposed disjoint new
files, relevant existing tests, and explicit blockers. Distinguish code facts
from recommendations and stop after the bounded report.

## Expected report

Append detailed evidence and finish with a short TL;DR for L:
recommended seams, no-go overlaps, and the smallest next implementation step.

## Explorer evidence (2026-08-10)

### Confirmed ownership and seams

- No `grepmesh`/GrepMesh source, manifest, or existing project was found. Existing project roots are `go-hub/`, `go-shellmcp/`, `go-proxyrelay/`, and `android-cellular-proxy/`; there is no `Cargo.toml`.
- GPTAdmin's MCP source of truth is `/etc/gptadmin/mcp.json` (`cli.py:132-138`). `cli.py:2439-2463` projects enabled entries into the single aggregate `/etc/gptadmin/mcp-supervisor.json`; disabled entries are excluded. The generated record supports command/args/env/cwd/user and optional URL/headers/transport.
- `cli.py:2732-2797` is the managed-MCP add seam: local executable or URL, optional stable `agent_id`, source config plus supervisor projection. Curated capability installs require explicit `--accept-capability` (`:2735-2741`).
- `cli.py:2832-2852` is the install/control seam: set `SHELLMCP_MCP_CONFIG`, regenerate the aggregate registry, retire legacy per-MCP services, and restart the one ShellMCP service. Do not add a GrepMesh-specific unit or parallel supervisor.
- `go-shellmcp/internal/supervisor/supervisor.go:32-65` defines the child contract; validation at `:276-292` requires command for stdio or URL for streamable-http/SSE, and `Manager.Start` owns child launch at `:352-385`. This is the natural local-host lifecycle seam.

### Discover registration/readback

- Hub routes are in `go-hub/internal/hub/server.go:872-890`: `/mcp-relay/register`, poll/result, `/mcp-relay/servers`, tools, calls, and jobs. Discovery is control-plane only; approved design says search data must not pass through Hub.
- `mcpRelayRegister` (`go-hub/internal/hub/server.go:2409-2531`) accepts `agent_id`/`name`, `kind`, `transport`, capabilities and safe `meta`. Existing credentials register online; enrollment without approval becomes `awaiting_approval` and requires signed proof before a relay credential is issued.
- Discovery readback is `go-hub/internal/hub/server.go:3627-3640`; target selection (`:2939-2960`) rejects empty/`default`. GrepMesh should be a direct host agent with stable ID and metadata, not a Hub search proxy.
- Reusable tests: `go-hub/internal/hub/server_test.go:42-79` (child discovery and disabled omission), `:81-203` (tool list/call), `:395-467` (discover -> schema -> execute), and `:232-281` (register/poll/call). New acceptance must additionally hit each host's local MCP endpoint and federation behavior.

### Health, install, and tests

- Hub health is `/healthz` (`go-hub/internal/hub/server.go:861-863`); ShellMCP exposes local capability control under `/capabilities/mcp/` (`go-shellmcp/internal/server/server.go:411`). GrepMesh can own read-only `search_status`, but must not redefine Hub health or add a second lifecycle owner.
- `go-shellmcp/scripts/smoke_mcp_host.py:44-85` is the closest black-box harness: temporary aggregate config, upsert stdio child, list tools, call tool. It does not prove Hub registration or five-host federation.
- `docs/LIVE_ACCEPTANCE.md:1-39` covers Hub/OAuth/MCP discovery and harmless demo proof only. GrepMesh needs a separate harness for local `127.0.0.1:9419/mcp`, exact tools/results, partial federation, and cached topology when Hub is unavailable.

### Recommended location and blockers

- Recommended disjoint root: `grepmesh/` at repository top level, with its own `Cargo.toml`, `src/`, contract tests, and acceptance scripts. Integrate only through MCP JSON-RPC, `mcp.json` projection, and the Hub registration protocol; do not modify `go-hub`/`go-shellmcp` in the first slice.
- Smallest next implementation: Rust stdio/HTTP MCP binary with four local tools and deterministic fixture-backed tests; then local HTTP listener/direct federation; only afterward separately gate installer/enrollment.
- Blockers requiring L/user approval: five-host enrollment identity/credential, metadata for advertising direct `127.0.0.1:9419/mcp`, federation auth/allowlist, index/watcher format, and Rust artifact/update ownership. Do not infer these by mutating auth, ACL, firewall, or production state.

## TL;DR for L

Use `cli.py` MCP projection and the aggregate ShellMCP supervisor for local lifecycle; use Hub `POST /mcp-relay/register` plus canonical `discover` readback for visibility. Do not add a Hub search proxy, per-MCP service, or first-slice changes inside `go-hub`/`go-shellmcp`. Create isolated `grepmesh/`, prove a local black-box MCP canary, then separately gate enrollment/installer and five-host federation. No existing component owns GrepMesh behavior.
