# Security

GPT‑Админ gives AI agents access to your servers. Security is the top priority.

## Auth model (summary)

GPT‑Админ has three auth mechanisms — see [Configuration → Auth model](./CONFIGURATION.md#auth-model)
for details:

1. **`CTL_TOKEN`** (Bearer, legacy migration only) — existing admin API + web panel installs; do not use it for new Custom GPT setup.
2. **OAuth bearer** — `/mcp` endpoint (for MCP clients) and the Custom GPT relay schema flow
3. **`ADMIN_PASSWORD`** — the `/oauth/authorize` form inside OAuth flow

Plus `SHELLMCP_TOKEN` for agent → hub registration.

## Least privilege

- **User-mode by default** — the agent runs as the installing user, not root.
  System-mode (sudo) is opt-in, only when you need privileged operations.
- **Command allowlist** — restrict which commands the agent will execute
  (configure in `~/.config/gptadmin/allowlist.txt`).
- **IP allowlist** — restrict which IPs can reach the agent.

## Secrets handling

- Secrets are **masked in logs** — tokens, passwords, API keys are redacted
  before logging.
- **"Local-only" mode** — for commands with sensitive data, the agent can be
  configured to not return output to the hub (run locally, report only status).
- **Managed backups** — before editing files, `file_backup` creates a backup
  with a TTL. Critical files (nginx, systemd, networking) get longer TTLs by
  default.

### Admin MFA

Locked-down administration accepts an enrolled WebAuthn/passkey credential or
the TOTP fallback. The WebAuthn ceremonies are exposed at
`/admin/api/security/mfa/webauthn/register/{begin,finish}` and
`/admin/api/security/mfa/webauthn/login/{begin,finish}`; the Hub stores public
credential records in `webauthn_state.json` with mode `0600` and uses a
short-lived HttpOnly proof cookie after a verified login. OIDC identity-aware
proxy integration remains deployment-specific and is not implied by local
passkey enrollment.

### Remote secret ingress

Remote MCP clients use `secret_request` to create a one-time browser entry
flow and `secret_status` to read metadata only. Values must never be sent in
MCP JSON, logs, audit records, job inspection or public responses. A managed
`shell_exec` may receive an opaque `secret_env` mapping; the Hub resolves it
server-side and redacts the value again at every response boundary.

The secure defaults are:

| Variable | Default | Contract |
|----------|---------|----------|
| `GPTADMIN_SECRET_STORE_DIR` | `$GPTADMIN_CONFIG_DIR/secrets` | Directory mode `0700`; contains encrypted records only |
| `GPTADMIN_SECRET_STORE_KEY_FILE` | `$GPTADMIN_CONFIG_DIR/secret-store.key` | AES-256 key mode `0600`; missing/invalid key fails closed |
| `GPTADMIN_SECRET_INGRESS_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/secrets/requests.json` | Request metadata mode `0600`; token hashes only |
| `GPTADMIN_SECRET_INGRESS_TTL` | `900` seconds | Bounded to 60–3600 seconds; requests are single-use |

Back up the key and encrypted store together using the existing protected
backup procedure. If the key is lost or invalid, restore it from a protected
backup or recreate the request; GPTAdmin never falls back to plaintext files
or environment variables. Rotate the key only with a planned migration that
re-encrypts records before removing the old key.

### OTLP telemetry export

OTLP export is opt-in through `GPTADMIN_OTLP_ENDPOINT`. External endpoints
must use HTTPS; HTTP is allowed only for loopback development collectors. The
Hub exports only an allowlisted structured event envelope and bounded
correlation fields. Arguments, commands, credentials, URLs, file contents and
raw payloads are excluded. The queue is bounded and export errors are
fail-open for the originating control-plane request.

## Approve mode

For critical operations (deleting files, changing network config), the hub
supports an **approve mode**: the AI proposes the action, the hub asks for
human confirmation before executing. Enable per-agent in `/admin` → Security.

## Token rotation

```bash
# Legacy `CTL_TOKEN` rotation (migration installs only)
openssl rand -hex 32

# Update the hub env, restart
sudo systemctl restart gptadmin_hub  # or: systemctl --user restart gptadmin_hub

# Update each agent's HUB_URL/TOKEN if you changed SHELLMCP_TOKEN
# Update Custom GPT / MCP client configs with the new OAuth bearer or scoped managed MCP token; do not onboard new clients with CTL_TOKEN
```

Rotate immediately if a token leaks. The repo's history-scrubbing
 is a one-time measure —
rotate to be safe.


## Gateway mode for MCP servers

When GPTAdmin is used as a secure proxy/relay, external clients should connect to GPTAdmin, not directly to private stdio or LAN-only MCP servers. Prefer per-server URLs when the client only needs one capability:

```text
/server/{slug}/mcp
/server/{slug}/actions/openapi.yaml
```

This keeps the upstream MCP server private while GPTAdmin applies HTTPS, bearer/OAuth auth, audit logging, routing and queue handling. Use the full `/server/hub/mcp` surface only for trusted clients that need cross-server relay/admin capabilities.

## Production hardening checklist

- [ ] New Custom GPT setup uses OAuth bearer or a scoped managed MCP token; no new `CTL_TOKEN` is created
- [ ] `OAUTH_CLIENT_SECRET` is set (for `/mcp`)
- [ ] `ADMIN_PASSWORD` is strong
- [ ] Hub is behind HTTPS (via Cloudflare/FRP tunnel or nginx + Certbot)
- [ ] Agent IP allowlist is set (only the hub can reach agents)
- [ ] Firewall: only hub port (25900) is public; agent port (25901) is internal
- [ ] Approve mode enabled for critical operations
- [ ] Logs are rotated (`logrotate` or `journalctl --vacuum-time`)
- [ ] Backups are configured (`file_backup` TTLs)

## Reporting a vulnerability

See [SECURITY.md](../SECURITY.md) (repo root). Short version:

- **Do NOT open a public GitHub issue.**
- Report via Telegram: [@careviolan](https://t.me/careviolan)
- Acknowledgement within 48h, fix target 30 days for critical issues.

## Audit log

Every command executed is logged with: timestamp, agent, command, caller
(which AI / adapter), exit code. Viewable in `/admin` → Logs. Export to
`/admin/api/logs/export` for SIEM ingestion.

## See also

- [Configuration](./CONFIGURATION.md) — how to set the auth vars
- [Hub](./HUB.md) — endpoints
- [SECURITY.md](../SECURITY.md) — responsible disclosure
