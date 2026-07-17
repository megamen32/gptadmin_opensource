# Getting Started

Install GPT‑Админ, connect your AI, run your first command — in 5 minutes.

## 1. Install the hub

On the machine that will run the hub (your PC, a VPS, or a server):

```bash
# Linux / macOS — auto-detects user/system mode
curl -s https://became.bezrabotnyi.com/install.sh | bash
```

```powershell
# Windows (PowerShell, no Administrator needed)
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

The installer creates the Hub, starts a Tunnel when needed, and prints one
**Hub URL**. Keep that URL: it is the normal connection point for GPTAdmin.

> No domain needed: choose the auto-tunnel option (FRP or Cloudflare) and you
> get a public URL. See [Tunnels](./TUNNELS_DOCS.md).

## 2. Install an agent on a target machine

On each server you want to manage:

```bash
curl -s https://became.bezrabotnyi.com/install.sh | bash
```

Pick "agent only" when prompted. The agent registers with your hub automatically.

## 3. Connect your AI

The installer and every `gptadmin update` automatically register the Hub as an
MCP server in locally installed **Codex**, **Claude Code**, **OpenCode**, and
**VS Code**. No URL, transport mode, or bearer token needs to be copied into
those clients. The registration uses the Hub's public URL, even when the local
agent uses a loopback service URL.

To register a client installed after setup, run:

```bash
gptadmin connect-mcp
```

For other clients, pick an adapter (you can use all of them with the same Hub):

- **Claude Desktop / other MCP clients** → [MCP client setup](./ADAPTERS.md#1-mcp-client)
- **DeepSeek / Qwen / Alice / GigaChat** (free web chats) → [Browser extension](./ADAPTERS.md#2-browser-extension)
- **ChatGPT Custom GPT / Open WebUI** → [OpenAI Action](./ADAPTERS.md#3-openai-action)

## 4. Run your first command

Ask your AI in plain language:

- «покажи статус nginx на server-01»
- «поставь docker на vps-prod»
- «почему openchamber отдаёт 503? посмотри логи»
- «запусти codex чтобы пофиксить баг в этом репо»

The AI calls the hub, the hub routes to the agent, the agent runs the command
and returns real output. The AI reads it and reports back.

## Show connection URLs

After setup, print the current public hub URL, tunnel mode, MCP endpoints and Custom GPT Action schemas:

```bash
sudo gptadmin urls
```

Useful variants:

```bash
sudo gptadmin urls --all   # include every registered MCP server
sudo gptadmin urls --json  # machine-readable output
```

## Next steps

- [Architecture](./ARCHITECTURE.md) — understand how it fits together
- [Configuration](./CONFIGURATION.md) — tune env vars, auth, OAuth
- [Security](./SECURITY_DOCS.md) — production hardening
- [Web panel](./HUB.md#web-panel-admin) — manage from the browser

## Troubleshooting

**The agent doesn't show up in `/admin`**
- Check `HUB_URL` is set and reachable from the agent
- Check `SHELLMCP_TOKEN` matches what the hub expects
- Look at the agent logs: `journalctl --user -u shellmcp -n 50`

**`/mcp` returns 401**
- You're using `CTL_TOKEN` directly. `/mcp` needs an OAuth bearer. See
  [Configuration → OAuth](./CONFIGURATION.md#oauth).

**Browser extension buttons don't appear**
- Refresh the page
- Make sure Tampermonkey/Userscripts has the script enabled
- On some sites, auto-insert fails — the prompt is in your clipboard, paste manually

**Custom GPT action test fails**
- Verify the Hub URL in `servers.url` matches your hub
- Verify the Bearer token is your `CTL_TOKEN`, not `SHELLMCP_TOKEN`
