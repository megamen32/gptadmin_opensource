# Adapters

The hub exposes **three ways** for an AI to connect. Same hub, same capabilities
— pick the one that matches your AI.

| Adapter | For | How |
|---------|-----|-----|
| [MCP client](#1-mcp-client) | Claude Desktop, Codex, OpenCode | MCP remote SSE at `/mcp` |
| [Browser extension](#2-browser-extension) | DeepSeek, Z.ai, Qwen, Alice, ChatGPT (free) | userscript (Tampermonkey/Firefox) |
| [OpenAI Action](#3-openai-action) | ChatGPT Custom GPT, Open WebUI | generated OpenAPI schema; choose Bearer or OAuth |

---

## 1. MCP client

**For:** Claude Desktop, Codex, OpenCode, any MCP-compatible client.

**Protocol:** MCP remote SSE (Streamable HTTP).

**Endpoint:** `https://your-hub.example/mcp` (discover it from `/connect`). The
managed GPTAdmin plugin in this repository defaults to
`https://mcp.bezrabotnyi.com/mcp`. A separate installation must use its own
explicitly selected, stable public origin.

### Setup

Add the hub as an MCP server in your client config. For Claude Desktop, edit
`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "gptadmin": {
      "type": "http",
      "url": "https://your-hub.example/mcp"
    }
  }
}
```

For Codex / OpenCode, the same config goes in their respective MCP settings.

Restart the client. You should see `gptadmin` tools (shell_exec, file ops,
systemd, etc.) available.

### Notes

- `/mcp` uses OAuth Authorization Code + PKCE. Start at `/connect` or the
  `/.well-known/oauth-authorization-server` metadata; internal bearer values
  are never copied into client configuration. See
  [Configuration → OAuth](./CONFIGURATION.md#oauth).
- For local development, use the same OAuth flow against the loopback Hub.

### Authorization durability

An MCP client that has completed authorization must retain enough client-managed
session state to refresh or restore that authorization without asking the
operator to repeat setup. In particular, a connector restart, client restart,
or normal token-refresh path must not leave an otherwise configured GPTADMIN
connection unable to call `discover`.

If Codex reports `reauthentication_required` or a missing refresh state, use
the client’s reconnect control once to recover access; do not paste internal
Hub credentials into the client. If OAuth metadata or the frozen ChatGPT app
configuration changed, recreate the app/connection: refreshing Actions does
not necessarily refresh saved OAuth metadata. Request
`gptadmin.read gptadmin.exec offline_access` when reconnecting. Treat the
message as a connector-lifecycle defect until the owning layer is identified.
Before deploying a related fix,
verify a fresh authorization, the defined restart/refresh scenario, and a real
harmless `discover -> schema -> execute` interaction. The required evidence is
specified in [Integration Control Contract](./INTEGRATION_CONTROL_CONTRACT.md#authorization-durability).

---

## 2. Browser extension

**For:** DeepSeek, Z.ai, Qwen, Yandex Alice, ChatGPT (free tier) —
any free web chat.

**Protocol:** userscript (runs in the browser via Tampermonkey/Firefox).

**Install:** https://became.bezrabotnyi.com/mcp-bridge.user.js

### How it works

The userscript adds two buttons to the web chat UI:
- **MCP All** (`Alt+M`) — inserts your Hub profile instructions and available
  target IDs. It does not wait for every target's tool schema.
- **MCP Tools** — opens a panel to pick one target and load its exact tool docs.

In Chrome 138+, enable Tampermonkey's **Allow User Scripts** permission. On
first use, **MCP All** opens the Hub's OAuth authorization page; after approval
it loads that user's Hub instructions and target IDs into the chat. It is setup,
not itself a tool call.

When the AI responds with a ` ```mcp ` code block containing a JSON command,
the script automatically:
1. Highlights the block
2. Sends the call to your hub
3. Inserts the result back into the chat

For multiple independent commands, use one `hub.exec` Code Mode block with
`await Promise.all([...])`; use sequential `await` only for calls that depend
on earlier results. Separate `mcp` blocks require separate clicks.

### Setup per platform

| Platform | Manager | Steps |
|----------|---------|-------|
| macOS / Windows / Linux | Chrome + [Tampermonkey](https://www.tampermonkey.net/) | Install Tampermonkey from Chrome Web Store, then click the install link. |
| iPhone | Safari + [Userscripts](https://apps.apple.com/app/userscripts/id1463298887) | Install Userscripts app, enable in Safari → Extensions, then install. |
| Android | Firefox + Tampermonkey | Install Firefox from Google Play, add Tampermonkey, then install. |

### Configuration

Press `Alt+K` (or the key icon, bottom-right) and check:
- **Bridge URL** — your configured Hub URL (`https://your-hub.example`); the
  managed deployment uses `https://mcp.bezrabotnyi.com`
- Choose **Connect with OAuth** and authorize in the Hub popup; do not paste an
  internal Hub or agent credential into chat.

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

**Protocol:** REST + generated OpenAPI schema. Custom GPT Actions use the
schema URL you import; native MCP clients use `/mcp` instead.

**Schema URLs:**

- Hub-wide: `https://your-hub.example/actions/openapi.yaml`
- Single-server: `https://your-hub.example/server/{slug}/actions/openapi.yaml`

The hub generates these schemas from the current MCP tool list. Import the URL
you need; do not hand-edit a schema for Custom GPT.

### Setup (ChatGPT Custom GPT)

1. Open https://chatgpt.com/gpts/editor
2. Create or edit a GPT → Configure → Actions → Create new action
3. Import OpenAPI by URL: `https://your-hub.example/actions/openapi.yaml`.
   This is the generated hub-wide schema. For a single server, import
   `/server/{slug}/actions/openapi.yaml` instead.
4. Pick the auth path:

   - **Bearer** — choose **API key** → **Bearer**, then paste only a scoped
     token value issued by the Hub.
   - **OAuth** — choose **OAuth**, then use the Hub authorize/token flow from
     the Hub URL.

Bearer is the shortest path when the GPT only needs a scoped token. OAuth is
the better choice when you want the GPT to go through the Hub login flow.

5. Save and use the per-operation **Test** control. The first call opens
   ChatGPT's one-time outbound-call confirmation; approve it, then confirm the
   generated schema works with a harmless action.

### OAuth for a Custom GPT

Custom GPT OAuth uses the Authorization Code flow. Choose **OAuth** in the
Action authentication dialog and configure the public Hub:

- authorization URL: `https://your-hub.example/oauth/authorize`;
- token URL: `https://your-hub.example/oauth/token`;
- client ID: a stable label for this GPT (for example `chatgpt-custom-gpt`);
- scopes: `gptadmin.read gptadmin.exec offline_access` (omit `gptadmin.exec`
  only for a deliberately read-only connection).

ChatGPT supplies its callback. Some Custom GPT editor versions do not send
PKCE parameters, so the Hub accepts that interoperable Authorization Code
variant only for the exact `https://chat.openai.com/aip/g-.../oauth/callback`
callback profile. Every other OAuth client must use PKCE S256.
On the Hub authorization page, sign in and complete the redirect, then run a
harmless test action again. If OAuth metadata or the frozen ChatGPT app
changes, recreate the app and authorize it again; refreshing Actions alone may
not update metadata. The Hub rejects a token whose issuer, audience,
resource, signature or key id does not match its canonical public origin.

### Diagnose a 401 without exposing a credential

Run the command below on the same installation that runs the Hub. It prints
only config paths, normalized issuer/audience/resource, the signing-key
fingerprint and a distinct local verdict; it never prints the supplied JWT or
signing secret.

```bash
gptadmin auth-diagnose --token '<paste-token-here>'
```

For a token that works on the public URL but not loopback (or the inverse),
check that the Hub's origin, resource and signing settings all come from the
same configuration file, then restart that Hub and issue a new token. Do not
point a Custom GPT at `127.0.0.1`.

### Setup (Open WebUI)

Add the hub as a tool/function endpoint in Open WebUI settings:
- URL: `https://your-hub.example/mcp-relay`
- OpenAPI schema: import from `https://your-hub.example/actions/openapi.yaml`
- Auth: a scoped Bearer token, or OAuth Authorization Code + PKCE from the Hub
  metadata where the client supports dynamic registration.

### Why "no Codex limits"

Custom GPT Actions don't have per-hour tool-call quotas like Codex. As long as
your hub is up, ChatGPT can call it as much as needed.

---

## Which adapter should I use?

- Using **Claude Desktop / Codex / OpenCode** natively? → **MCP client**
- Want to use **free web chats** (DeepSeek, Z.ai, Qwen, Alice)? → **Browser extension**
- On **ChatGPT with Plus** and want a Custom GPT? → **OpenAI Action**

All three give you the same capabilities — the hub doesn't care which adapter
the AI used. See [Architecture](./ARCHITECTURE.md) for why.
