# Hub (`go-hub/`)

The hub is the central process of GPT‑Админ. It proxies commands from AIs to
shellmcp agents, handles auth, and serves the web panel.

## What it does

1. **Registers agents** — shellmcp agents send heartbeats to `POST /heartbeat`;
   the hub tracks them and marks offline if heartbeat stops.
2. **Routes commands** — when an AI calls a tool, the hub looks up the target
   agent and forwards the command.
3. **Authenticates** — `AdminPassword` for human administration, OAuth for
   MCP clients, and managed device connections for agents.
4. **Truncates output** — long stdout/stderr is chunked to save tokens (the AI
   can read more on demand).
5. **Serves the panel** — web UI at `/admin` (queue, agent health, logs).
6. **Exposes MCP** — MCP remote SSE at `/mcp` for MCP clients.
7. **Exposes relay OpenAPI** — canonical Custom GPT import at `/actions/openapi.yaml`; per-server imports use `/server/{slug}/actions/openapi.yaml`.
8. **Accepts webhooks** — authenticated `/webhooks/v1/{route}` ingress feeds the separate default-off `webhooks` virtual MCP and can dispatch a configured MCP, prompt, or Shell action.

## Remote secret ingress

An authorized full-access MCP client can call `secret_request` with a label
and optional `env_name`. The Hub returns an opaque `secret_ref` and a short-
lived one-time `input_url`; it never returns the token separately or accepts
the value in MCP JSON. Open `input_url` in a browser, submit the value once, then call
`secret_status` to confirm `ready` without receiving plaintext.

To use the value in a managed shell job, pass only the reference:

```json
{
  "target": "shell:example",
  "tool": "shell_exec",
  "args": {
    "cmd": "printenv EXAMPLE_TOKEN",
    "secret_env": {"EXAMPLE_TOKEN": "secret-ref-from-secret_request"}
  }
}
```

The Hub resolves the reference server-side, injects it only into the approved
job, and redacts it from MCP results, job inspection, audit records and logs.
Readonly profiles cannot request or inspect secrets. The `file` metadata is an
opaque Hub-managed storage reference, not permission to read the file through
`system_inspect`. The public router keeps the existing Hub origin and does not
expose the internal 2.1 route.

## Running

```bash
python3 cli.py setup --hub --tunnel none --user
```

By default it listens on `0.0.0.0:25900`. Change with `--port` or `HUB_PORT`.

## Key endpoints

| Endpoint | Auth | Purpose |
|----------|------|---------|
| `GET /admin` | Admin session | Web panel |
| `GET /admin/api/*` | Admin session | Admin REST API |
| `POST /mcp` | OAuth bearer | MCP remote SSE (for MCP clients) |
| `POST /heartbeat` | Managed device connection | Agent registration |
| `GET /servers` | Admin session | List registered agents |
| `GET /actions/openapi.yaml` | none | Canonical OpenAPI schema for Custom GPT import |
| `GET /api.json` | none | Legacy JSON alias for the relay schema |
| `GET /openapi.yaml` | none | Legacy YAML alias for the relay schema |
| `POST /oauth/authorize` | `ADMIN_PASSWORD` form | Canonical OAuth authorize endpoint |
| `POST /webhooks/v1/{route}` | Route token or HMAC signature | Universal event ingress |
| `GET /webhook-jobs/{job_id}` | Same route credential | Read webhook job status/result |
| `GET/POST /webhook-routes` | Hub control auth | List or create route definitions without returning secrets |
| `PUT/DELETE /webhook-routes/{route}` | Hub control auth | Replace or remove an operator-owned route |
| `POST /oauth/token` | client credentials | Canonical OAuth token endpoint |
| `GET/POST /secret-input/{token}` | One-time browser token | Enter a secret value once; responses never include it |

See [API Reference](./API_REFERENCE.md) for full details.
See [Webhooks](./WEBHOOKS.md) for route configuration and delivery semantics.

## OAuth credential lifetime

OAuth authorization-code and refresh exchanges use short-lived access JWTs and
an opaque, rotating refresh credential. The Hub persists only the digest of the
refresh credential; it survives a Hub restart for five calendar years unless
explicitly revoked. Clients must persist the replacement value returned by each
refresh. Managed MCP bearer keys issued without an explicit `ttl_days` also
default to five years. Existing JWT strings retain their signed lifetime and
are never silently replaced by an update.

## Web panel (`/admin`)

Open the Hub URL printed by setup and choose **Admin**. Sign in with your
`AdminPassword`. You'll see:

- **Queue** — active and completed tasks per agent (status, time, result)
- **Agent health** — list of shellmcp agents + connected MCPs (openmemory,
  chrome-devtools, …) with live online/offline status
- **Logs** — command journal and outputs, readable from the browser (no SSH)

## Environment variables

See [Configuration](./CONFIGURATION.md) for the full list. The essentials:

| Var | Required | Default | Purpose |
|-----|----------|---------|---------|
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
