# Adapters

The hub exposes **three ways** for an AI to connect. Same hub, same capabilities
— pick the one that matches your AI.

| Adapter | For | How |
|---------|-----|-----|
| [MCP client](#1-mcp-client) | Claude Desktop, Codex, OpenCode | MCP remote SSE at `/mcp` |
| [Browser extension](#2-browser-extension) | DeepSeek, Qwen, Alice, GigaChat, ChatGPT (free) | userscript (Tampermonkey/Firefox) |
| [OpenAI Action](#3-openai-action) | ChatGPT Custom GPT, Open WebUI | REST + OpenAPI, Bearer token |

---

## 1. MCP client

**For:** Claude Desktop, Codex, OpenCode, any MCP-compatible client.

**Protocol:** MCP remote SSE (Streamable HTTP).

**Endpoint:** `https://your-hub.bezrabotnyi.com/mcp`

### Setup

Add the hub as an MCP server in your client config. For Claude Desktop, edit
`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "gptadmin": {
      "type": "http",
      "url": "http://localhost:25900/mcp",
      "headers": {
        "Authorization": "Bearer  YOUR_CTL_TOKEN"
      }
    }
  }
}
```

For Codex / OpenCode, the same config goes in their respective MCP settings.

Restart the client. You should see `gptadmin` tools (shell_exec, file ops,
systemd, etc.) available.

### Notes

- `/mcp` does **not** accept `CTL_TOKEN` directly. It requires an OAuth bearer
  token that the hub signs via `OAUTH_CLIENT_SECRET`. See
  [Configuration → OAuth](./CONFIGURATION.md#oauth).
- For local dev you can use `http://localhost:25900/mcp` without OAuth (the
  hub relaxes auth on localhost).

---

## 2. Browser extension

**For:** DeepSeek, Qwen, Yandex Alice, Sber GigaChat, ChatGPT (free tier) —
any free web chat.

**Protocol:** userscript (runs in the browser via Tampermonkey/Firefox).

**Install:** https://became.bezrabotnyi.com/mcp-bridge.user.js

### How it works

The userscript adds two buttons to the web chat UI:
- **MCP All** (`Alt+M`) — inserts a compact description of all your MCP agents
  and their tools into the chat input. Also copies the prompt to clipboard.
- **MCP** — opens a panel to pick a specific agent with detailed tool docs.

When the AI responds with a ` ```mcp ` code block containing a JSON command,
the script automatically:
1. Highlights the block
2. Sends the call to your hub
3. Inserts the result back into the chat

### Setup per platform

| Platform | Manager | Steps |
|----------|---------|-------|
| macOS / Windows / Linux | Chrome + [Tampermonkey](https://www.tampermonkey.net/) | Install Tampermonkey from Chrome Web Store, then click the install link. |
| iPhone | Safari + [Userscripts](https://apps.apple.com/app/userscripts/id1463298887) | Install Userscripts app, enable in Safari → Extensions, then install. |
| Android | Firefox + Tampermonkey | Install Firefox from Google Play, add Tampermonkey, then install. |

### Configuration

Press `Alt+K` (or the key icon, bottom-right) and enter:
- **Bridge URL** — your hub URL (`https://your-hub.bezrabotnyi.com`)
- **Bridge Key** — your `CTL_TOKEN`

### Supported sites

| Site | Status |
|------|--------|
| chatgpt.com | Full support |
| chat.deepseek.com | Full support |
| chat.qwen.ai | Full support |
| ya.ru / chat.yandex.ru | Full support |

> If auto-insert doesn't work (rare, on some sites), the prompt is always in
> your clipboard — just `Ctrl+V` / `Cmd+V`.

---

## 3. OpenAI Action

**For:** ChatGPT Custom GPT, Open WebUI.

**Protocol:** REST + OpenAPI schema, Bearer auth.

**Endpoint:** `https://your-hub.bezrabotnyi.com/admin/api/*`

### Setup (ChatGPT Custom GPT)

1. Open https://chatgpt.com/gpts/editor
2. Create or edit a GPT → Configure → Actions → Create new action
3. Import OpenAPI by URL: `https://became.bezrabotnyi.com/api.json`
4. In the `servers` block, replace the `url` with your Hub URL
5. Authentication → API key → Bearer → paste your `CTL_TOKEN`
6. Save. You can now ask ChatGPT to run server commands — no Codex limits.

### Setup (Open WebUI)

Add the hub as a tool/function endpoint in Open WebUI settings:
- URL: `https://your-hub.bezrabotnyi.com/admin/api`
- OpenAPI schema: import from `https://became.bezrabotnyi.com/api.json`
- Auth: Bearer `CTL_TOKEN`

### Why "no Codex limits"

Custom GPT Actions don't have per-hour tool-call quotas like Codex. As long as
your hub is up, ChatGPT can call it as much as needed.

---

## Which adapter should I use?

- Using **Claude Desktop / Codex / OpenCode** natively? → **MCP client**
- Want to use **free web chats** (Qwen, Alice, GigaChat)? → **Browser extension**
- On **ChatGPT with Plus** and want a Custom GPT? → **OpenAI Action**

All three give you the same capabilities — the hub doesn't care which adapter
the AI used. See [Architecture](./ARCHITECTURE.md) for why.
