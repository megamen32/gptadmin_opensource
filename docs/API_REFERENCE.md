# API Reference

REST + MCP endpoints exposed by the hub.

## Auth quick reference

| Endpoint | Auth |
|----------|------|
| `GET /admin` | Basic (`CTL_TOKEN`) |
| `GET /admin/api/*` | Bearer `CTL_TOKEN` |
| `POST /mcp` | OAuth bearer |
| `POST /heartbeat` | Bearer `SHELLMCP_TOKEN` |
| `GET /servers` | Bearer `CTL_TOKEN` |
| `GET /api.json` | none |
| `GET /openapi.yaml` | none |
| `POST /authorize` | `ADMIN_PASSWORD` form |
| `POST /oauth/token` | client credentials |

See [Configuration → Auth model](./CONFIGURATION.md#auth-model).

---

## Admin API (`/admin/api/*`)

Bearer auth with `CTL_TOKEN`. Used by the web panel and Custom GPT actions.

### `GET /servers`

List registered shellmcp agents.

```json
{
  "servers": [
    { "name": "server-01", "url": "http://203.0.113.10:25901", "alive": true, "last_seen": "2026-06-29T10:00:00Z" }
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

Full schema: import `https://became.bezrabotnyi.com/api.json` into your client.

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

- `GET /api.json` — JSON schema (for Custom GPT / Open WebUI import)
- `GET /openapi.yaml` — YAML schema

These are public (no auth) so Custom GPT can import by URL.

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
