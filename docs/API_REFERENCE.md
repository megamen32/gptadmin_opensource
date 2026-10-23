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

## Current fleet and access endpoints (source contract)

These current Hub routes require an authorized admin request (`CTL_TOKEN`, an
admin session, or an eligible existing MCP bearer, according to the route's
auth middleware). The resource-sample and proxy-route mutation handlers also
require same-origin checks when called with a browser admin session.
The contracts below describe the current source; installation and live runtime
acceptance are separate checks.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/admin/api/overview` | Hub overview, including the current server inventory and recent jobs. |
| `GET` | `/admin/api/clients` | Safe connection/client inventory; use this instead of any endpoint that reveals a saved token value. |
| `GET` | `/admin/api/access-profiles` | List access profiles. |
| `GET` | `/admin/api/access-profiles/{id}` | Get one profile and its `ETag`. |
| `PUT` | `/admin/api/access-profiles/{id}` | Create with `If-Match: *`, or replace with the current `ETag` in `If-Match`; stale versions fail. |
| `POST` | `/admin/api/mcp/issue-token` | Issue an MCP bearer token from `client_id`, optional `profile_id`, `role`, `ttl_days`, and `access_mode`. `access_mode` is `full` or `readonly` (default `full`); `ttl_days: 0` selects the server default. The response includes the secret `access_token` once. |
| `POST` | `/admin/api/servers/resources` | Request a live resource sample from one exact `shell:<id>` target. |
| `GET` | `/admin/api/jobs?server_id=shell:<id>&limit=1` | Fetch newest task history for exactly that shell node; filtering happens before ordering and limit. It works while the node is offline. |
| `GET` / `PUT` | `/admin/api/proxy-route` | Read or save the fixed VUSA-to-node-44 proxy-route profile. |
| `POST` | `/admin/api/proxy-route/{enable,disable,reconcile,probe}` | Operate or probe that fixed route. |

The overview inventory can include the Hub (`hub`), real child MCPs
(`real_mcp`), remote ShellMCP nodes (`virtual_shell`), internal virtual MCPs
(`virtual_mcp`), and paired file/control surfaces (`virtual_file`,
`virtual_control`). Paired surfaces refer back to a backing node; they are not
additional hosts.

The resource request body is `{"server_id":"shell:<exact-id>"}`. A completed
sample returns `sampled_at` (UTC RFC3339), overall `status` (`available`,
`partial`, or `unavailable`), and `cpu`, `memory`, and `root_filesystem`
components with their own status. CPU is a short measured utilization interval,
not load average. Memory includes total, used and available bytes; the root
filesystem includes total, used and free-to-user bytes. A slow request may
return `status: "pending"` and a `job_id`. Offline, unknown, malformed, or
failed request envelopes return `unavailable` with a reason and
`sampled_at: null`. A completed sample records its real sample time even when
all components are unavailable. A component error is not represented as zero;
unsupported or failed individual probes remain unavailable in a `partial`
sample.

The jobs endpoint returns actual recorded task metadata and result/error
preview for that target. It reports history stored by the Hub; reading it does
not claim that the node answered recently or is currently online.

The proxy-route profile is source-constrained to the VUSA consumer, the
node-44 DE HTTP exit, loopback listener `127.0.0.1:24836`, and the existing
`ssh-config:relay02` identity reference. `GET` reports saved profile and runtime
service/listener/probe state separately. The default desired state is disabled;
the API contract does not imply that the route is enabled or live.

See [Configuration → Auth model](./CONFIGURATION.md#auth-model).

---

## Admin API (`/admin/api/*`)

Bearer auth with `CTL_TOKEN`. Legacy admin/web-panel migration detail only.

### `GET /servers`

List registered shellmcp agents.

```json
{
  "servers": [
    { "name": "node-01", "url": "http://203.0.113.10:25901", "alive": true, "last_seen": "2026-06-29T10:00:00Z" }
  ]
}
```

### `POST /exec`

Execute a shell command on a target agent.

```json
{
  "server": "node-01",
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

### `GET /system/info?server=node-01`

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
