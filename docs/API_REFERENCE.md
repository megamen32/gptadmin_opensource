# API Reference

## Operational probes

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | none | Liveness only; returns `ok`. |
| `GET` | `/version` | none | Build version and commit identity. |
| `GET` | `/metrics` | none | Bounded aggregate Hub counts; never includes credentials, arguments or file contents. |
| `GET` | `/admin/api/connection-debug?limit=200&server_id=...` | ctl bearer | Secret-safe operator snapshot of Hub, published child MCP connections, heartbeat age, topology metadata, jobs and recent trace-linked audit events. |

REST + MCP endpoints exposed by the hub.

## Request correlation

The Hub accepts an optional W3C `traceparent` request header. It returns a
validated child `traceparent` and the bounded `X-Request-ID` response header;
queued relay and ShellMCP jobs carry the same correlation fields through poll
and result delivery. Invalid trace headers are discarded and replaced. Trace
metadata never contains command arguments, credentials or file contents.

An operator may opt in to OTLP/HTTP log export with
`GPTADMIN_OTLP_ENDPOINT`. External collectors must use HTTPS; plain HTTP is
accepted only for loopback development collectors. The exporter uses a
bounded asynchronous queue and exports allowlisted event fields such as
policy decision, tool, result reference and trace IDs. It never exports raw
arguments, commands, credentials, URLs or file contents, and collector
delivery failures do not fail the originating Hub request.

## Remote secret ingress

Full-access MCP clients can call `secret_request` and `secret_status`. The
first returns only `request_id`, `input_url`, `secret_ref`, `env_name`, `file`
and expiry metadata. The operator submits the value once to
`POST /secret-input/{token}`; neither MCP nor the response body accepts or
returns the plaintext. A later `shell_exec` may pass
`secret_env: {"ENV_NAME": "secret_ref"}`. Hub job responses and logs redact
the resolved value, and readonly profiles cannot access these operations.

## Auth quick reference

| Endpoint | Auth |
|----------|------|
| `GET /admin` | Basic (`CTL_TOKEN`) |
| `GET /admin/api/*` | Bearer `CTL_TOKEN` |
| `POST /mcp` | OAuth bearer |
| `POST /heartbeat` | Bearer `SHELLMCP_TOKEN` |
| `GET /servers` | Bearer `CTL_TOKEN` |
| `GET /actions/openapi.yaml` | none |
| `GET /server/{slug}/actions/openapi.yaml` | none |
| `GET /api.json` | none (legacy alias; not the Custom GPT path) |
| `GET /openapi.yaml` | none (legacy alias; not the Custom GPT path) |
| `POST /oauth/authorize` | `ADMIN_PASSWORD` form |
| `POST /oauth/token` | client credentials |

Custom GPT imports use `/actions/openapi.yaml` or `/server/{slug}/actions/openapi.yaml`;
relay calls go through `/mcp-relay/*`. `CTL_TOKEN` is legacy/admin only.

See [Configuration → Auth model](./CONFIGURATION.md#auth-model).

---

## Admin API (`/admin/api/*`)

Bearer auth with `CTL_TOKEN`. Legacy admin/web-panel migration detail only.

### `GET /servers`

List registered shellmcp agents.

```json
{
  "servers": [
    { "name": "server-01", "url": "http://10.0.0.5:25901", "alive": true, "last_seen": "2026-06-29T10:00:00Z" }
  ]
}
```

### `POST /exec`

Execute a shell command on a target agent.

```json
{
  "server": "server-01",
  "cmd": "systemctl status nginx"
}
```

Response (truncated to save tokens if long):

```json
{
  "stdout": "● nginx.service - The nginx HTTP server...",
  "stderr": "",
  "exit_code": 0,
  "truncated": false
}
```

### `GET /tasks/{task_id}`

Get the status of a background task.

### `POST /file/backup`

Create a managed backup of a file before editing.

### `GET /system/info?server=server-01`

CPU, RAM, disk, uptime for a target agent.

Legacy import schema: `https://became.bezrabotnyi.com/api.json` or
`https://became.bezrabotnyi.com/openapi.yaml`. Custom GPT import uses
`/actions/openapi.yaml` or `/server/{slug}/actions/openapi.yaml`, not these
legacy aliases.

---

## MCP endpoint (`/mcp`)

OAuth bearer auth. MCP remote SSE (Streamable HTTP).

MCP clients (Claude Desktop, Codex, OpenCode) connect here. The hub exposes
the shellmcp tools as MCP tools:

- `shell_exec` — run a shell command
- `file_read` — read a file
- `file_write` — write a file (with backup)
- `file_backup` — create a managed backup
- `systemd_status` / `systemd_start` / `systemd_stop` / `systemd_restart`
- `system_info` — CPU/RAM/disk/uptime
- `system_health` — quick health check
- `dir` — list a directory

See the [Adapters → MCP client](./ADAPTERS.md#1-mcp-client) setup.

---

## Agent endpoints (shellmcp)

These are called by the hub, not directly by AIs. Bearer `SHELLMCP_TOKEN`.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/exec` | POST | Run a shell command |
| `/file` | GET/POST | Read/write a file |
| `/dir` | GET | List a directory |
| `/systemd/{action}` | POST | status/start/stop/restart/enable |
| `/system/info` | GET | CPU/RAM/disk/uptime |
| `/system/health` | GET | Health check |
| `/heartbeat` | POST | Register with the hub (called by agent → hub) |

---

## OAuth endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/oauth/authorize` | GET/POST | Authorization endpoint |
| `/oauth/token` | POST | Token endpoint |
| `/.well-known/oauth-authorization-server` | GET | OAuth server metadata |

See [Configuration → OAuth](./CONFIGURATION.md#oauth).

---

## OpenAPI schema

- Canonical Custom GPT import: `GET /actions/openapi.yaml`
- Per-server import: `GET /server/{slug}/actions/openapi.yaml`
- Legacy aliases: `GET /api.json` and `GET /openapi.yaml`

The canonical import URLs are public (no auth). The legacy aliases stay public
for migration, but they are not the Custom GPT path.

## Background tasks

Long-running commands return a `task_id` instead of blocking:

```json
{ "task_id": "abc123", "status": "running" }
```

Poll with `GET /tasks/abc123` until `status: completed`. The AI does this
automatically.

## Output truncation

Long stdout/stderr is chunked. The response includes:

```json
{
  "stdout": "...first 1MB...",
  "truncated": true,
  "spilled_path": "/tmp/spilled.stdout",
  "preview_head": "...",
  "preview_tail": "..."
}
```

The AI can read more on demand via a follow-up call. This saves tokens — the
AI only reads what it needs to answer.


## Per-server MCP and OpenAPI Action proxy

GPTAdmin exposes each registered MCP server through authenticated per-server routes. Replace `{slug}` with `meta.public_mcp_slug` from `GET /mcp-relay/servers`.

| Method | Path | Purpose |
|--------|------|---------|
| `GET` / `POST` | `/server/{slug}/mcp` | MCP-compatible endpoint for one server |
| `GET` | `/server/{slug}/card` | Server discovery card |
| `GET` | `/server/{slug}/health` | Server health |
| `GET` | `/server/{slug}/actions/openapi.yaml` | Generated OpenAPI schema for Custom GPT Actions |
| `GET` | `/server/{slug}/actions/openapi.json` | Same schema as JSON |
| `POST` | `/server/{slug}/actions/tools/{tool_name}` | Proxy an OpenAPI Action call to one MCP tool |

The Action schema is generated from the selected MCP server's `tools/list`. Each operation request body is the MCP tool `inputSchema`. The Action call response wraps the upstream MCP result:

## Optional virtual MCP management

`network-proxy` and `webhooks` are off by default. The default `/actions/openapi.yaml` stays relay-only and excludes both.

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/admin/api/virtual-mcps` | Check both states in one call. |
| `PUT` | `/admin/api/virtual-mcps/{id}` | Persist `{"enabled": true|false}` for `network-proxy` or `webhooks`. |

`network-proxy` gives bounded Network Tunnel tools: `network_proxy_request`, `network_proxy_approve`, `network_proxy_issue`, `network_proxy_open`, `network_proxy_status`, `network_proxy_revoke`.

`webhooks` gives secret-free webhook route CRUD and job lookup: `webhook_routes_list`, `webhook_route_create`, `webhook_route_replace`, `webhook_route_delete`, `webhook_job_get`.

Enablement check: `GET /mcp-relay/servers` shows only enabled virtual MCPs.

Use after enablement: `/server/{slug}/mcp` and `/server/{slug}/actions/openapi.yaml`.

```json
{
  "server_id": "OpenMemory",
  "tool_name": "openmemory_query",
  "status": "completed",
  "response": {"content": []}
}
```

See [MCP Proxy Relay](./MCP_PROXY_RELAY.md) for examples.
