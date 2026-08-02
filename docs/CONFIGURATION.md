# Configuration

Full environment-variable reference, auth model, and OAuth setup.

## Hub env vars (`go-hub/`)

### Auth

| Var | Required | Default | Purpose |
|-----|----------|---------|---------|
| `ADMIN_PASSWORD` | **yes** | — | Password for the `/oauth/authorize` HTML form and admin session. |
| `CTL_TOKEN` | existing installations only | — | Deprecated compatibility bearer; do not create or copy it. It remains valid until its owner explicitly rotates or removes it. |
| `OAUTH_CLIENT_SECRET` | for `/mcp` | — | Signs OAuth bearer tokens. Generate with `openssl rand -hex 32`. |
| `PUBLIC_ORIGIN` | recommended | — | Public base URL (e.g. `https://your-hub.bezrabotnyi.com`). Used in OAuth + OpenAPI. |
| `MCP_RESOURCE` | recommended | `$PUBLIC_ORIGIN` | The MCP resource identifier. |
| `GPTADMIN_AUTH_RATE_LIMIT` | optional | `60` per client per minute | Maximum failed admin/control/MCP authentication attempts from one client before a temporary `429` response. Successful authentication does not consume the budget. |

### Network

| Var | Default | Purpose |
|-----|---------|---------|
| `HUB_PORT` | 25900 | Listen port |
| `HUB_HOST` | `127.0.0.1` | Listen host; public deployments must set an explicit boundary host at the Tunnel/HAOS layer. `HUB_BIND` remains an installer compatibility alias. |
| `CORS_ORIGINS` | `*` | Allowed CORS origins (comma-separated) |

### Behavior

| Var | Default | Purpose |
|-----|---------|---------|
| `EXEC_TIMEOUT` | 120 | Max command execution time (seconds) |
| `LOG_LIMIT_B` | 65536 | Per-ShellMCP-agent inline stdout/stderr tail budget. Larger command output is spooled to disk; hub/client response budgets are configured separately. |
| `HEARTBEAT_TIMEOUT` | 60 | Seconds before an agent is marked offline |
| `BACKGROUND_TASK_TTL` | 3600 | How long completed background jobs are kept (seconds) |
| `GPTADMIN_STARTUP_INSTRUCTIONS_FILE` | `$GPTADMIN_CONFIG_DIR/startup_instructions.md` | Optional local Markdown startup instructions for MCP clients. |
| `GPTADMIN_STARTUP_INSTRUCTIONS` | — | Optional environment override for startup instructions; takes precedence over the file. |
| `GPTADMIN_INSTRUCTION_SETS_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/instruction_sets_state.json` | Restrictive state file for named profile instruction sets. |
| `GPTADMIN_WEBHOOK_CONFIG_FILE` | `$GPTADMIN_CONFIG_DIR/webhooks.json` | Operator-owned universal webhook route definitions. |
| `GPTADMIN_WEBHOOK_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/webhook_state.json` | Durable webhook jobs and replay keys; written with mode `0600`. |

### MCP startup instructions

GPTAdmin supplies generic system-administration guidance in the MCP `initialize`
result. To customize it persistently, create
`$GPTADMIN_CONFIG_DIR/startup_instructions.md` (normally
`$GPTADMIN_ROOT/config/startup_instructions.md`). The file must be a regular file
of at most 16 KiB; unreadable, empty, or oversized files safely fall back to the
built-in generic guidance. `GPTADMIN_STARTUP_INSTRUCTIONS` overrides the file
when it is non-empty and at most 16 KiB.

The same content is available to clients that ignore `initialize.instructions`
via MCP `resources/read` at `gptadmin://startup-instructions`. Startup
instructions are operational guidance, **not** a security boundary: configured
permissions and approvals still control access and execution.

Named profile instruction sets are managed through the authenticated Hub
endpoints `GET /admin/api/instruction-sets` and
`GET|PUT|DELETE /admin/api/instruction-sets/{id}`. `PUT` requires `If-Match`
(`*` for create); a set cannot be deleted while an access profile references
it. The selected profile set is returned on the next MCP `initialize` and
startup-resource read without restarting Hub.

Manage the file without exposing its contents accidentally:

```bash
gptadmin instructions path
gptadmin instructions set-file /secure/path/sysadmin_startup.md
gptadmin hub restart
gptadmin instructions show  # explicitly prints the potentially sensitive content
```

`set-file` accepts UTF-8 files up to 16 KiB, installs atomically with mode `0600`,
and prints only the destination path plus the restart hint. The CLI uses the
selected installation scope: `~/.config/gptadmin` for `--user` installs and
`/etc/gptadmin` for `--system` installs, unless `GPTADMIN_CONFIG_DIR` overrides it.

## ShellMCP env vars

See [ShellMCP → Environment variables](./SHELLMCP.md#environment-variables).

## Auth model

GPT‑Админ has **three** auth mechanisms — they're different, don't mix them up.

### 1. Legacy `CTL_TOKEN` (temporary compatibility only)

- Used for: `/admin`, `/admin/api/*`, `/servers`, `/tasks/*`, artifact endpoints
- Header: `Authorization: Bearer <CTL_TOKEN>` (accepted only for an existing
  compatibility credential until its owner explicitly rotates or removes it)
- This is the "admin" token. The web panel and Custom GPT actions use it.

### 2. OAuth bearer (for `/mcp`)

- Used for: `/mcp` (MCP remote SSE)
- `/mcp` normally uses an OAuth bearer token that the hub signs via
  `OAUTH_CLIENT_SECRET`. The deprecated existing compatibility bearer remains
  accepted only until its owner explicitly rotates or removes it.
- MCP clients (Claude Desktop, Codex) obtain this token via the OAuth flow.

### 3. `ADMIN_PASSWORD` (form)

- Used for: the HTML form at `/oauth/authorize` inside the OAuth flow
- This is what a human types to authorize an OAuth client.

### 4. `SHELLMCP_TOKEN` (agent → hub)

- Used for: `POST /heartbeat` (agent registration)
- Each agent has its own `SHELLMCP_TOKEN` — the hub validates it on heartbeat.
- It is also the credential for authenticated queue polling. A client that
  cannot authenticate to the Hub must not be auto-approved: queued work can
  contain sensitive command arguments and results, so an impersonating client
  must be rejected before it receives tasks.
- In-place updates preserve this credential and the service always uses the
  canonical `gptadmin.env`. Explicit token rotation is a separate operation;
  installing a new binary or restarting a service must not rotate it.
- A new device identity is reported as `awaiting_approval`, not `offline`.
  Review it with the Hub MCP `pending` tool and approve exactly one returned
  `server_id` with `approve_pending_server`; approval is not repeated for each
  poll or after a normal binary update.

## Legacy bearer migration

`CTL_TOKEN` is a deprecated compatibility credential, not a supported setup
path. New setup and updates never create or print it. An existing credential
remains valid until its owner explicitly rotates or removes it; normal clients
should use the AdminPassword OAuth authorization flow or a scoped MCP JWT.
ShellMCP agent credentials are separate and are not affected by this rule.

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

### Connection lifecycle

The Hub issues the OAuth protocol responses, while a given MCP client or its
connector may own persisted browser/session and refresh state. An authorized
client must remain able to refresh or restore its session across its supported
restart boundary. Do not work around a client-side refresh failure by exposing
or copying internal credentials. Changes to this boundary require the
authorization-durability regression and live-client acceptance defined in
[Integration Control Contract](./INTEGRATION_CONTROL_CONTRACT.md#authorization-durability).
OAuth access JWTs remain short lived; the Hub-issued opaque refresh credential
has a five-calendar-year lifetime and rotates on use. New managed MCP bearers
without an explicit `ttl_days` default to five years. Existing signed JWTs
retain their original expiry and are not silently replaced by setup or update.

### Where to set the password

In the web panel: `/admin` → **Security** → set `ADMIN_PASSWORD` and generate
`OAUTH_CLIENT_SECRET`. Or set them as env vars when starting the hub.

## Example `.env`

```bash
# Generate strong values:
# Do not generate CTL_TOKEN on new installations; configure AdminPassword/OAuth instead.
# OAUTH_CLIENT_SECRET=$(openssl rand -hex 32)

ADMIN_PASSWORD=choose-a-password
OAUTH_CLIENT_SECRET=internal-signing-secret
ADMIN_PASSWORD=choose-a-strong-password
OAUTH_CLIENT_SECRET=$(openssl rand -hex 32)
PUBLIC_ORIGIN=https://your-hub.example.com
MCP_RESOURCE=https://your-hub.example.com
```

## See also

- [Hub](./HUB.md) — what these vars configure
- [Security](./SECURITY_DOCS.md) — hardening for production
- [API Reference](./API_REFERENCE.md) — which auth each endpoint needs
