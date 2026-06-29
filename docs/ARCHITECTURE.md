# Architecture

GPT‑Админ is **one MCP hub** with **three adapters**. Tools plug INTO the hub;
AIs connect OUT of it.

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

## Components

### 1. The Hub (`hub_proxy.py`)

The central process. It:
- Accepts heartbeats from shellmcp agents (registers them, tracks liveness)
- Exposes MCP remote SSE at `/mcp` for MCP clients (Claude, Codex, OpenCode)
- Exposes a REST admin API at `/admin/api/*` (used by the web panel + Custom GPTs)
- Proxies commands from AIs to the right shellmcp agent
- Handles OAuth (for OpenAI SDK OAuth flow) and Bearer auth (CTL_TOKEN)
- Serves the web panel at `/admin`

### 2. ShellMCP (`go-shellmcp/`, `client/`)

The agent that runs on each target machine. It:
- Registers with the hub via heartbeat (`POST /heartbeat`)
- Executes shell commands, file ops, systemd actions locally
- Returns real stdout/stderr to the hub, which returns it to the AI
- Works on Linux, macOS, Windows
- Runs in user-mode (no sudo) by default, system-mode when needed

The Go implementation (`go-shellmcp/`) is the primary one. The Python client
(`client/`) is kept for compatibility.

### 3. Three Adapters

The hub exposes three ways for an AI to connect. Same hub, same capabilities —
pick what fits your AI.

| Adapter | Protocol | For | Endpoint |
|---------|----------|-----|----------|
| **MCP client** | MCP remote SSE | Claude Desktop, Codex, OpenCode | `/mcp` |
| **Browser extension** | userscript (Tampermonkey/Firefox) | DeepSeek, Qwen, Alice, GigaChat, ChatGPT (free) | injects into web UI |
| **OpenAI Action** | REST + OpenAPI | ChatGPT Custom GPT, Open WebUI | `/admin/api/*` |

See [Adapters](./ADAPTERS.md) for per-adapter setup.

## Data flow

When you ask the AI "restart nginx on server-01":

1. **AI** decides to call a tool (MCP tool / OpenAI Action / injected mcp block)
2. **Adapter** routes the call to the hub (`POST /mcp` or `/admin/api/exec`)
3. **Hub** looks up `server-01`, finds its shellmcp agent, forwards the command
4. **shellmcp** on `server-01` runs `systemctl restart nginx`, captures output
5. **shellmcp** returns stdout/stderr to the hub
6. **Hub** truncates long output (saves tokens), returns to the adapter
7. **Adapter** returns to the AI, which reads the result and reports back

## Why this design

- **Hub-and-spoke**: one place to manage auth, logging, truncation, audit.
  Adding a new AI = adding an adapter, not rewriting the agent.
- **MCP-native**: the hub speaks MCP, so any MCP client works. And the hub can
  itself consume other MCPs (chrome-devtools, openmemory) — they become tools
  available to every connected AI.
- **Self-hosted**: your servers, your tokens, your data. Nothing leaves your
  infra.
- **Any AI, even free ones**: the browser extension means you don't need a paid
  API. Free web chats (Qwen, Alice, GigaChat) become GPT‑Админ clients.

## See also

- [Hub](./HUB.md) — hub internals, config, endpoints
- [ShellMCP](./SHELLMCP.md) — agent internals
- [Adapters](./ADAPTERS.md) — how to connect each AI
