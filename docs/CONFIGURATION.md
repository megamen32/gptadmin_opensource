# Configuration

Full environment-variable reference, auth model, and OAuth setup.

## Hub env vars (`hub_proxy.py`)

### Auth

| Var | Required | Default | Purpose |
|-----|----------|---------|---------|
| `CTL_TOKEN` | **yes** | — | Bearer token for admin API + web panel. Generate with `openssl rand -hex 32`. |
| `ADMIN_PASSWORD` | for OAuth | — | Password for the `/authorize` HTML form (OAuth flow). |
| `OAUTH_CLIENT_SECRET` | for `/mcp` | — | Signs OAuth bearer tokens. Generate with `openssl rand -hex 32`. |
| `PUBLIC_ORIGIN` | recommended | — | Public base URL (e.g. `https://your-hub.bezrabotnyi.com`). Used in OAuth + OpenAPI. |
| `MCP_RESOURCE` | recommended | `$PUBLIC_ORIGIN` | The MCP resource identifier. |

### Network

| Var | Default | Purpose |
|-----|---------|---------|
| `HUB_PORT` | 25900 | Listen port |
| `HUB_HOST` | 0.0.0.0 | Listen host |
| `CORS_ORIGINS` | `*` | Allowed CORS origins (comma-separated) |

### Behavior

| Var | Default | Purpose |
|-----|---------|---------|
| `EXEC_TIMEOUT` | 120 | Max command execution time (seconds) |
| `LOG_LIMIT_B` | 1048576 | Max output size before truncation (bytes) — saves tokens |
| `HEARTBEAT_TIMEOUT` | 60 | Seconds before an agent is marked offline |
| `BACKGROUND_TASK_TTL` | 3600 | How long completed background jobs are kept (seconds) |

## ShellMCP env vars

See [ShellMCP → Environment variables](./SHELLMCP.md#environment-variables).

## Auth model

GPT‑Админ has **three** auth mechanisms — they're different, don't mix them up.

### 1. `CTL_TOKEN` (Bearer)

- Used for: `/admin`, `/admin/api/*`, `/servers`, `/tasks/*`, artifact endpoints
- Header: `Authorization: Bearer <CTL_TOKEN>`
- This is the "admin" token. The web panel and Custom GPT actions use it.

### 2. OAuth bearer (for `/mcp`)

- Used for: `/mcp` (MCP remote SSE)
- `/mcp` does **not** accept `CTL_TOKEN` directly. It requires an OAuth bearer
  token that the hub signs via `OAUTH_CLIENT_SECRET`.
- MCP clients (Claude Desktop, Codex) obtain this token via the OAuth flow.

### 3. `ADMIN_PASSWORD` (form)

- Used for: the HTML form at `/authorize` inside the OAuth flow
- This is what a human types to authorize an OAuth client.

### 4. `SHELLMCP_TOKEN` (agent → hub)

- Used for: `POST /heartbeat` (agent registration)
- Each agent has its own `SHELLMCP_TOKEN` — the hub validates it on heartbeat.

## OAuth

The hub implements OAuth endpoints compatible with the OpenAI SDK OAuth flow.

### Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/oauth/authorize` | GET/POST | Authorization endpoint (shows the `ADMIN_PASSWORD` form) |
| `/oauth/token` | POST | Token endpoint (client credentials / authorization code) |
| `/.well-known/oauth-authorization-server` | GET | OAuth server metadata |

### Setup

1. Set `OAUTH_CLIENT_SECRET` on the hub (generate with `openssl rand -hex 32`)
2. Set `ADMIN_PASSWORD` (this is what users type at the authorize form)
3. Set `PUBLIC_ORIGIN` to your public hub URL
4. MCP clients will discover the OAuth endpoints via `/.well-known/...`

### Where to set the password

In the web panel: `/admin` → **Security** → set `ADMIN_PASSWORD` and generate
`OAUTH_CLIENT_SECRET`. Or set them as env vars when starting the hub.

## Example `.env`

```bash
# Generate strong values:
# CTL_TOKEN=$(openssl rand -hex 32)
# OAUTH_CLIENT_SECRET=$(openssl rand -hex 32)

CTL_TOKEN=generate-a-strong-random-token
ADMIN_PASSWORD=choose-a-strong-password
OAUTH_CLIENT_SECRET=$(openssl rand -hex 32)
PUBLIC_ORIGIN=https://your-hub.example.com
MCP_RESOURCE=https://your-hub.example.com
```

## See also

- [Hub](./HUB.md) — what these vars configure
- [Security](./SECURITY_DOCS.md) — hardening for production
- [API Reference](./API_REFERENCE.md) — which auth each endpoint needs
