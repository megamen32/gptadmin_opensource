# FAQ

## Is GPT‑Админ free?

Yes. The core (hub, shellmcp, all three adapters, basic web panel) is free
forever under AGPL-3.0. Future paid offerings (hosted cloud, enterprise SSO,
advanced panel) will be additive — we won't paywall existing functionality.
See [Roadmap](./ROADMAP.md).

## Do I need a paid AI subscription?

No. The browser extension works with **free web chats** — DeepSeek, Qwen,
Yandex Alice, Sber GigaChat, even free-tier ChatGPT. See
[Adapters → Browser extension](./ADAPTERS.md#2-browser-extension).

## Is it safe to give an AI access to my servers?

GPT‑Админ is designed for this. Key safety features:

- **User-mode by default** — no root/sudo needed
- **Command allowlist** — restrict what the AI can run
- **Approve mode** — human confirmation for critical operations
- **Audit log** — every command is logged with caller + result
- **Secrets masked** in logs
- **Managed backups** before file edits

See [Security](./SECURITY_DOCS.md).

## Will the AI run commands without my knowledge?

No. Commands only run when you ask in the chat. For critical operations
(deletions, network changes), approve mode requires human confirmation.

## What's the difference between the three adapters?

Same hub, same capabilities — they're just different ways for the AI to
connect:

- **MCP client** — for Claude/Codex/OpenCode (native MCP support)
- **Browser extension** — for free web chats (no API needed)
- **OpenAI Action** — for ChatGPT Custom GPT / Open WebUI

See [Adapters](./ADAPTERS.md).

## Why is it called CTL_TOKEN?

Historical naming. `CTL_TOKEN` is the admin bearer token for the hub API +
web panel. It's confusing — we may rename it in 1.0 (with a migration path).
See [Configuration → naming](./CONFIGURATION.md).

## `/mcp` returns 401 — why?

`/mcp` doesn't accept `CTL_TOKEN` directly. It needs an OAuth bearer token
signed via `OAUTH_CLIENT_SECRET`. MCP clients handle this automatically via
the OAuth flow. For local dev, the hub relaxes auth on localhost. See
[Configuration → OAuth](./CONFIGURATION.md#oauth).

## Do I need my own domain?

No. The installer offers an auto-tunnel via FRP — you get a public URL on
`frp.bezrabotnyi.com` with no DNS setup. For your own domain, use Cloudflare
Tunnel or nginx + Certbot. See [Tunnels](./TUNNELS_DOCS.md).

## Does it work on Windows?

Yes. The agent runs on Windows (user-mode, no Administrator needed) via a
Scheduled Task. Install with:

```powershell
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

See [Install Paths](./INSTALL_PATHS.md).

## Can I use it without the browser extension / Custom GPT?

Yes — use the **MCP client** adapter with Claude Desktop, Codex, or OpenCode.
These connect natively via MCP remote SSE, no browser needed. See
[Adapters → MCP client](./ADAPTERS.md#1-mcp-client).

## How does output truncation work?

Long stdout/stderr is chunked (default: 1MB). The AI sees the head + tail +
a "read more" pointer. This saves tokens — the AI only reads what it needs
to answer. See [API Reference → Output truncation](./API_REFERENCE.md#output-truncation).

## Can I plug in other MCPs?

Yes! The hub can consume other MCP servers (chrome-devtools for web search,
openmemory for project memory, etc.) and expose them as tools to every
connected AI. See [Architecture](./ARCHITECTURE.md).

## How do I rotate tokens?

See [Security → Token rotation](./SECURITY_DOCS.md#token-rotation). Short version:
generate new values with `openssl rand -hex 32`, update the hub env, restart,
update clients.

## Something broke. Where are the logs?

- Hub: `journalctl -u gptadmin_hub -n 100` (or `--user` for user-mode)
- Agent: `journalctl -u shellmcp -n 100` (or `--user`)
- Or read them in the web panel: `/admin` → Logs

## How do I uninstall?

```bash
gptadmin uninstall
```

Removes binaries, configs, and service units. File backups are preserved.

## Still stuck?

- [Open a GitHub issue](https://github.com/megamen32/gptadmin/issues)
- Telegram: [@careviolan](https://t.me/careviolan)
- Website: https://gptadmin.bezrabotnyi.com
