# GPTAdmin Go hub rewrite

This worktree starts a Go implementation of the GPTAdmin hub while keeping the existing Python hub untouched.

Implemented compatibility surface:

- `GET /version`, `GET /healthz`
- `POST /heartbeat`
- `GET /queue/{server}`, `POST /queue/{server}/result` for polling shell agents
- `POST /mcp-relay/register`
- `GET /mcp-relay/poll/{agent_id}` returning `{id, method, params}` for existing stdio relay agents
- `POST /mcp-relay/result/{agent_id}`
- authenticated `list_mcp_agents`, `list_mcp_tools`, `call_mcp_tool`, `get_mcp_job`
- minimal `/admin/api/overview`, `/admin/api/jobs`, `/admin/api/audit`, `/admin/api/clients`
- static `/admin/` serving from `public/admin`

Important semantic fix: the internal hub reports `kind: "hub"`, not `virtual_hub`. The dashboard also maps legacy `virtual_hub` to `hub` if an older backend still returns it.

Build/test:

```bash
cd go-hub
go test ./...
go build -o bin/gptadmin-hub ./cmd/gptadmin-hub
```

Runtime env keeps current names where possible: `CTL_TOKEN`, `MCP_RELAY_AGENT_TOKEN`, `MCP_RELAY_DEFAULT_TIMEOUT`, `MCP_RELAY_POLL_MAX_TIMEOUT`, `PUBLIC_ORIGIN`, plus optional `GPTADMIN_ROOT`, `GPTADMIN_CONFIG_DIR`, `GPTADMIN_PUBLIC_DIR`, `GPTADMIN_HUB_PORT`.


## 2026-07-04 parity pass 2

Added the next compatibility layer on top of the relay core:

- OAuth metadata and authorization-code flow: `/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`, `/register`, `/authorize`, `/token`.
- Apps SDK / MCP JSON-RPC endpoint: `/mcp` with `initialize`, `tools/list`, `tools/call`, `resources/list`, `resources/read`.
- Prompt bridge compatibility: `/mcp-prompt/prompt` and `/mcp-prompt/call`.
- Admin compatibility: dashboard-shaped `/admin/api/overview`, `/admin/api/jobs`, `/admin/api/audit`, `/admin/api/clients`, `/admin/api/mcp/resources/list`, `/admin/api/mcp/resources/read`, plus placeholder-safe `/admin/api/mcp/manage` and client revoke/delete endpoints.
- Installer/actions compatibility: `/actions/openapi.yaml`, `/artifacts/shellmcp.json`, `/artifacts/shellmcp.tar.gz`, `/servers`, `/bulk/exec`, `/tasks/*`.

The Go hub still intentionally keeps several legacy subsystems minimal/in-memory until final production cutover: OAuth client persistence, rich audit/client history, websocket shell transport, and full mutating MCP manager parity. Production service remains the Python hub until an explicit switch.

## 2026-07-04 per-agent MCP facade

Added default public MCP facades for every registered/public agent. The hub still exposes the aggregate endpoint at `/mcp`, but each agent can now be used as a drop-in MCP server endpoint:

- `/agent/{slug}`
- `/agent/{slug}/mcp`
- `/agent/{slug}/card`
- `/agent/{slug}/health`

Examples:

- `/agent/hub/mcp` — aggregate GPTAdmin hub MCP tools, same as `/mcp`.
- `/agent/openmemory/mcp` — pinned MCP facade for the `OpenMemory` agent.
- `/agent/fileshare/mcp` — pinned MCP facade for the `FileShare` agent.
- `/agent/shell-admin-server-100/mcp` — pinned MCP facade for the shell agent.

`list_mcp_agents` and `/mcp-relay/list_mcp_agents` now include default exposure metadata in each agent card: `public_mcp_slug`, `public_mcp_path`, `public_mcp_endpoint`, `exposed_by_default`, and `public_mcp_auth`. The facade accepts the same Bearer/OAuth authentication as `/mcp` for now. Future admin UI work should turn these defaults into explicit expose aliases with per-alias security policy: Bearer on/off, OAuth on/off, tool allowlist/denylist, and eventually per-client policy.

The public endpoint is pinned to exactly one upstream agent. A client connected to `/agent/openmemory/mcp` sees an ordinary MCP server and cannot choose another `target`; the hub routes `tools/list`, `tools/call`, `resources/list`, `resources/read`, `prompts/list`, and `prompts/get` to the resolved internal agent through whatever transport backs it (`stdio`, `mcp-tunnel`, shell connector, or another relay adapter).
