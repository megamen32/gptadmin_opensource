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
