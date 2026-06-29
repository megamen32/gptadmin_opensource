# GPT‑Админ — Documentation

Welcome to the GPT‑Админ docs. GPT‑Админ is a self-hosted MCP hub: plug your
servers and any MCP tools into it, then connect any AI via one of three
adapters.

**Website:** https://gptadmin.bezrabotnyi.com
**Install:** `curl -s https://became.bezrabotnyi.com/install.sh | bash`

## Table of contents

| Page | What's inside |
|------|---------------|
| [Architecture](./ARCHITECTURE.md) | How the hub, shellmcp, and 3 adapters fit together |
| [Getting Started](./GETTING_STARTED.md) | Install + first command in 5 minutes |
| [Adapters](./ADAPTERS.md) | The 3 ways to connect your AI (MCP / extension / Custom GPT) |
| [Hub](./HUB.md) | gptadmin_hub: config, env vars, endpoints, web panel |
| [ShellMCP](./SHELLMCP.md) | The agent that runs on target machines |
| [Install Paths](./INSTALL_PATHS.md) | Where GPT‑Админ lives on Linux/macOS/Windows |
| [Configuration](./CONFIGURATION.md) | Full env-var reference, auth model, OAuth |
| [API Reference](./API_REFERENCE.md) | REST + MCP endpoints |
| [Security](./SECURITY.md) | Auth, tokens, OAuth, responsible disclosure |
| [Tunnels](./TUNNELS.md) | FRP and Cloudflare tunnels to expose the hub |
| [Roadmap](./ROADMAP.md) | What's built, what's coming, open-core split |
| [FAQ](./FAQ.md) | Common questions |
| [Open-Core Plan](./OPEN_CORE_PLAN.md) | Internal launch plan (living doc) |

## Quick links

- **New here?** Start with [Getting Started](./GETTING_STARTED.md).
- **Want to understand the design?** Read [Architecture](./ARCHITECTURE.md).
- **Connecting a specific AI?** Jump to [Adapters](./ADAPTERS.md).
- **Going to production?** See [Security](./SECURITY.md) and [Tunnels](./TUNNELS.md).
