# Roadmap

What's built, what's coming, and the open-core split.

## MCP and context sources — owner priorities, 2026-11-08

The optional schema-resource server implementation is present; release and
client acceptance remain separate checkpoints in the existing GPTAdmin fullcycle.
Remote Node SSH/Telnet remains paused. Continue in this order:

1. **Optional active MCP schema resources — implemented in source.** In the same resource picker as
   startup instructions and servers, offer a compact active-server/tool index,
   individual server schemas, and an explicitly selected full active-schema
   snapshot. Include input/output schemas, descriptions and tool annotations;
   use stable server identity, not mutable hostnames. Filter both listing and
   reading by the caller's current profile/ACL. Advertise metadata in
   `resources/list`; fetch content on `resources/read`, with bounded cached
   discovery, freshness/version/size and explicit partial/unavailable status.
   Never fan out unboundedly or silently truncate a supposedly complete schema.
   **Acceptance:** the real client picker can attach a selected schema; listing
   resources does not fetch every child schema or inject their contents.
   An unauthorized resource cannot be listed or read after access revocation.
   **Current boundary:** full schemas, bounded discovery/cache, ACL filtering and
   explicit completeness are implemented. Native catalog/read and installed Node
   checks are tracked separately from phone-picker attachment; the latter remains
   open until observed. See [resource contract and limits](MCP_SCHEMA_RESOURCES.md).
2. **Child context-source federation and selection.** Relay authorized child
   `resources/list`, `resources/templates/list` and `resources/read` with stable,
   collision-free resource URIs. Forward subscriptions/change notifications only
   when negotiated and supported. Add source selection to profiles and connection
   creation, with per-link overrides restricted to the profile's permissions.
   Preserve provenance and distinguish external resource text from trusted
   policy. Client-side attachment remains client-controlled; a server preference
   cannot force a client to add text to its model context.
   **Acceptance:** two children with identical original URIs remain distinct;
   selected sources survive reconnect/update and access revocation takes effect.
3. **Broader modern MCP support (owner shorthand: “MCP 2.0”).** Maintain an
   implemented/missing matrix against dated protocol versions and negotiated
   extensions, starting with 2026-07-28. Cover resource/template discovery,
   pagination, notifications, caching, structured tool results/output schemas,
   task lifecycle/cancellation/expiry/restart and modern/legacy transport
   compatibility. Audit current implementations before adding duplicates.
   **Acceptance:** missing behavior gets RED → fix → GREEN and a real supported
   client check; document unsupported combinations without claiming blanket
   “2.0 compliance”. Measure initial context size and on-demand schema cost.

MCP resources are **application-driven**: the host may use explicit selection,
heuristics or model selection. Publishing a resource does not itself put its
contents in every prompt; once attached, its contents can consume context tokens.
A resource containing a tool schema is documentation, not tool registration or
an execution grant. Keep GPTAdmin's compact execution facade.

Specification references checked 2026-11-08:
[Resources](https://modelcontextprotocol.io/specification/2026-07-28/server/resources),
[Tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools).

## Status today

### ✅ Built and working

- **Hub** (`go-hub/`) — MCP remote SSE, admin API, OAuth, web panel
- **ShellMCP** (Go + Python) — agent for Linux/macOS/Windows
- **Three adapters**:
  - MCP client (Claude Desktop, Codex, OpenCode)
  - Browser extension (userscript for free web AIs)
  - OpenAI Action (Custom GPT, Open WebUI)
- **CLI** (`gptadmin`) — setup, tunnel, status, logs, config
- **Tunnels** — FRP + Cloudflare auto-tunnel
- **Web panel** (`/admin`) — queue, agent health, logs
- **OAuth** for OpenAI SDK
- **Install scripts** for Linux/macOS/Windows (user + system mode)
- **Background tasks** with polling
- **Output truncation** (saves tokens)
- **Managed file backups** with TTLs

### 🚧 Coming soon

- **Advanced web panel** — teams, RBAC, alerting, audit log export
- **Hosted cloud** — don't want to self-host? We'll host it for you
- **Enterprise SSO** — SAML, OIDC, SCIM provisioning
- **MCP marketplace** — browse and install MCPs (openmemory, chrome-devtools,
  custom) from the panel
- **More adapters** — Slack, Discord, Telegram bots as first-class adapters

## Open-core model

GPT‑Админ is **open-core** under AGPL-3.0.

### Free forever (this repo)

- The hub, shellmcp, all three adapters
- Basic web panel (queue, health, logs)
- All CLI commands
- All tunnels
- Community support (GitHub issues, Discussions)

### Will be paid (separate repo / cloud)

- Hosted cloud (managed hub, no self-hosting)
- Enterprise SSO (SAML/OIDC) + SCIM
- Advanced RBAC (roles, per-agent permissions)
- Extended audit log + SIEM export
- SLA + priority support
- Advanced panel (team dashboards, alerting, analytics)

The core stays open. Paid features are additive — never paywalling existing
functionality.

## Versioning

We follow [SemVer](https://semver.org/). See [CHANGELOG.md](../CHANGELOG.md)
for release history.

- `0.x` — pre-1.0, breaking changes possible between minor versions
- `1.0` — first stable release (after web panel ships)
- `1.x+` — backward-compatible additions

## Contributing

PRs welcome — see [CONTRIBUTING.md](../CONTRIBUTING.md). Areas that need love:

- More test coverage (tunnels, MCP-SSE, CLI)
- Documentation improvements
- New MCP integrations (write an MCP that wraps your favorite tool)
- Packaging (Homebrew formula, AUR package, Scoop manifest)

## See also

- [Architecture](./ARCHITECTURE.md) — how it's built
- [CHANGELOG.md](../CHANGELOG.md) — what changed
