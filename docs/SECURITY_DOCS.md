# Security

GPT‑Админ gives AI agents access to your servers. Security is the top priority.

## Auth model (summary)

GPT‑Админ has three auth mechanisms — see [Configuration → Auth model](./CONFIGURATION.md#auth-model)
for details:

1. **`CTL_TOKEN`** (Bearer) — admin API + web panel
2. **OAuth bearer** — `/mcp` endpoint (for MCP clients)
3. **`ADMIN_PASSWORD`** — the `/authorize` form inside OAuth flow

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

## Approve mode

For critical operations (deleting files, changing network config), the hub
supports an **approve mode**: the AI proposes the action, the hub asks for
human confirmation before executing. Enable per-agent in `/admin` → Security.

## Token rotation

```bash
# Generate a new CTL_TOKEN
openssl rand -hex 32

# Update the hub env, restart
sudo systemctl restart gptadmin_hub  # or: systemctl --user restart gptadmin_hub

# Update each agent's HUB_URL/TOKEN if you changed SHELLMCP_TOKEN
# Update Custom GPT / MCP client configs with the new CTL_TOKEN
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

- [ ] `CTL_TOKEN` is a strong random value (`openssl rand -hex 32`)
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
