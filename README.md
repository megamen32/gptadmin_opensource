# GPT‑Админ

**One MCP hub — any AI controls any infrastructure.**

GPT‑Админ is a self-hosted MCP hub that sits between your AI assistant and your
servers. Plug servers and any MCP tools into the hub, then connect your AI via
one of three adapters. Manage everything from server admin to running subagents
— from ChatGPT, Claude, Codex, DeepSeek, Qwen, even Yandex Alice or Sber
GigaChat.

> 🌐 Website & live docs: **https://gptadmin.bezrabotnyi.com**
> 📦 Install: `curl -s https://became.bezrabotnyi.com/install.sh | bash`

---

## Why

Most "AI server admin" tools are either cloud-only, tied to one AI, or require
a paid API. GPT‑Админ is different:

- **Self-hosted** — your servers, your hub, your tokens. Nothing leaves your
  infra.
- **Any AI** — ChatGPT, Claude, Codex, OpenCode, DeepSeek, Qwen, Alice,
  GigaChat. Even free web chats work via a browser extension.
- **MCP-native** — the hub speaks MCP remote SSE, so any MCP client connects.
  Plug in chrome-devtools, openmemory, or any other MCP too.
- **Real execution** — not "here's the command, copy it". The agent reads
  state, runs commands, validates, and reports actual output.

## How it works

```
   MCP tools plug IN            AIs connect OUT (3 adapters)
  ┌─────────────────┐          ┌──────────────────────┐
  │ shellmcp        │          │ Claude · Codex       │ (MCP client)
  │ chrome-devtools │  ──►  ┌──┴──────────────┐       │
  │ openmemory      │       │   GPT‑Админ     │  ──►  │ DeepSeek · Qwen  │ (browser ext)
  │ any MCP         │  ◄──  │   MCP hub       │       │ Alice · GigaChat │
  └─────────────────┘       └──┬──────────────┘  ──►  │ ChatGPT · OpenUI │ (OpenAI Action)
                              │                │       └──────────────────────┘
                              ▼
                        your servers
                     (Linux · macOS · Windows)
```

The hub is one process. Tools plug into it. AIs connect to it via one of three
adapters. Capabilities (admin, code, logs, search, subagents) come from the hub
— independent of which AI you use.

## Quickstart

```bash
# 1. Install (Linux / macOS — auto-detects user/system mode)
curl -s https://became.bezrabotnyi.com/install.sh | bash

# Windows (PowerShell, no Administrator needed)
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

The installer prints your **Hub URL** and **CTL_TOKEN** — keep them.

```bash
# 2. Connect your AI — pick one adapter:
#    - OpenAI Action (ChatGPT Custom GPT / Open WebUI) → see docs
#    - MCP remote SSE (Claude / Codex / OpenCode)      → see docs
#    - Browser extension (free web AIs)                → see docs

# 3. Use it:
#    "поставь nginx", "почини сайт", "покажи логи", "запусти codex для фикса бага"
```

Full per-adapter instructions: **https://gptadmin.bezrabotnyi.com/#/docs**

## What you can do through the hub

| Capability | Example |
|------------|---------|
| **Server administration** | restart systemd services, manage firewall/nginx/fail2ban/sshd, install packages |
| **Write & run code** | edit files, run type-check/lint/build, launch subagents ("run codex to fix this bug") |
| **Fix & clean PRs** | find fork in memory, keep one feature commit, force-push via SSH |
| **Check logs** | parse journalctl/nginx/postgres, find anomalies, suggest & apply fixes |
| **Web search** | via chrome-devtools MCP — agent opens pages, reads docs, searches |
| **Diagnose incidents** | find downed service, read logs, understand cause (ECONNREFUSED, 503, OOM), fix it |

## Three adapters

| Adapter | For | How |
|---------|-----|-----|
| **OpenAI Action** | ChatGPT, Open WebUI | Create a Custom GPT / add OpenAPI endpoint, Bearer token. No Codex limits. |
| **MCP remote SSE** | Claude Desktop, Codex, OpenCode | Hub is an MCP server (Streamable HTTP). Add to `claude_desktop_config.json`. |
| **Browser extension** | DeepSeek, Qwen, Alice, GigaChat, ChatGPT (free) | Userscript (Tampermonkey/Firefox) adds MCP buttons to web chat UIs. No paid API. |

All three connect to the **same hub**. Same capabilities. Pick what fits your AI.

## Install paths

| OS | user-mode (default) | system-mode (`sudo`) |
|----|---------------------|----------------------|
| Linux / macOS | `~/.local/share/gptadmin` | `/opt/gptadmin` |
| Windows | `%LOCALAPPDATA%\gptadmin` | `C:\Program Files\gptadmin` |

The installer auto-detects the mode: no `sudo` → user-mode (home dir, user
service); `sudo` → system-mode. No domain needed — auto-tunnel via FRP or
Cloudflare gives a public URL.

## Project layout

```
hub_proxy.py            # the MCP hub — proxies commands to agents
hub_watchdog.py         # keeps the hub alive
server_for_installer.py # serves install scripts + OpenAPI
gptadmin_security.py    # auth, OAuth, token validation
cli.py                  # `gptadmin` CLI (setup, tunnel, status, logs)
telegram_logs_bot.py    # optional Telegram alerts
go-shellmcp/            # primary shell agent (Go) — runs on target machines
client/                 # legacy Python shell agent (compat)
public/                 # OpenAPI schema, install scripts, mcp-bridge.user.js
deploy/                 # install scripts, systemd, nginx configs
tests/                  # test_hub.py, test_rootd.py
docs/                   # OPEN_CORE_PLAN.md
```

> Structure is being cleaned up — see `docs/OPEN_CORE_PLAN.md` for the target
> layout and the open-core launch plan.

## Running from source

```bash
python3 -m venv .venv && source .venv/bin/activate
pip install fastapi uvicorn requests

# hub (terminal 1)
CTL_TOKEN=your-token python hub_proxy.py

# agent on a target machine (terminal 2)
SHELLMCP_TOKEN=agent-token HUB_URL=http://127.0.0.1:25900 python client/shellmcp.py

# smoke test (terminal 3)
python tests/test_hub.py
```

## Contributing

PRs welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for dev setup, code style,
and the PR process. For security issues see [SECURITY.md](SECURITY.md).

## License

**GNU AGPL-3.0** — see [LICENSE](LICENSE).

The hub, shellmcp, all three adapters, and the basic web panel are **free
forever**. Future paid offerings (hosted cloud, enterprise SSO/audit/RBAC,
advanced panel, support) will live in a separate repo — the core stays open.

## Links

- 🌐 Website & docs: https://gptadmin.bezrabotnyi.com
- 💬 Telegram: [@careviolan](https://t.me/careviolan)
- 📦 Other projects: https://bezrabotnyi.com
