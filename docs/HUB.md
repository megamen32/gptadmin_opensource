# Hub (`go-hub/`)

The hub is the central process of GPT‑Админ. It proxies commands from AIs to
shellmcp agents, handles auth, and serves the web panel.

## What it does

1. **Registers agents** — shellmcp agents send heartbeats to `POST /heartbeat`;
   the hub tracks them and marks offline if heartbeat stops.
2. **Routes commands** — when an AI calls a tool, the hub looks up the target
   agent and forwards the command.
3. **Authenticates** — Bearer (`CTL_TOKEN`) for admin API, OAuth bearer for
   `/mcp`, `ADMIN_PASSWORD` for the OAuth authorize form.
4. **Truncates output** — long stdout/stderr is chunked to save tokens (the AI
   can read more on demand).
5. **Serves the panel** — web UI at `/admin` (queue, agent health, logs).
6. **Exposes MCP** — MCP remote SSE at `/mcp` for MCP clients.
7. **Exposes OpenAPI** — `/api.json` and `/openapi.yaml` for Custom GPT import.

## Running

```bash
CTL_TOKEN=your-token go run ./go-hub/cmd/gptadmin-hub
```

By default it listens on `0.0.0.0:25900`. Change with `--port` or `HUB_PORT`.

## Key endpoints

| Endpoint | Auth | Purpose |
|----------|------|---------|
| `GET /admin` | `CTL_TOKEN` (basic) | Web panel |
| `GET /admin/api/*` | Bearer `CTL_TOKEN` | Admin REST API |
| `POST /mcp` | OAuth bearer | MCP remote SSE (for MCP clients) |
| `POST /heartbeat` | Bearer `SHELLMCP_TOKEN` | Agent registration |
| `GET /servers` | Bearer `CTL_TOKEN` | List registered agents |
| `GET /api.json` | none | OpenAPI schema (for Custom GPT import) |
| `GET /openapi.yaml` | none | OpenAPI YAML |
| `POST /authorize` | `ADMIN_PASSWORD` form | OAuth authorize endpoint |
| `POST /oauth/token` | client credentials | OAuth token endpoint |

See [API Reference](./API_REFERENCE.md) for full details.

## Web panel (`/admin`)

Open `https://your-hub.bezrabotnyi.com/admin` in a browser, auth with
`CTL_TOKEN`. You'll see:

- **Queue** — active and completed tasks per agent (status, time, result)
- **Agent health** — list of shellmcp agents + connected MCPs (openmemory,
  chrome-devtools, …) with live online/offline status
- **Logs** — command journal and outputs, readable from the browser (no SSH)

## Environment variables

See [Configuration](./CONFIGURATION.md) for the full list. The essentials:

| Var | Required | Default | Purpose |
|-----|----------|---------|---------|
| `CTL_TOKEN` | yes | — | Bearer token for admin API + panel |
| `ADMIN_PASSWORD` | for OAuth | — | Password for the `/authorize` form |
| `OAUTH_CLIENT_SECRET` | for `/mcp` | — | Signs OAuth bearer tokens |
| `PUBLIC_ORIGIN` | recommended | — | Public base URL (for OAuth, OpenAPI) |
| `HUB_PORT` | no | 25900 | Listen port |

## Supervision

The Go hub is supervised directly by systemd with `Restart=always`. Legacy Python watchdog units were removed. Restart manually:

```bash
systemctl restart gptadmin-hub.service
```

## See also

- [Configuration](./CONFIGURATION.md) — full env-var reference
- [API Reference](./API_REFERENCE.md) — endpoint details
- [Security](./SECURITY_DOCS.md) — auth model
- [ShellMCP](./SHELLMCP.md) — the agent
