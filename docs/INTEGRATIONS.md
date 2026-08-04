# Integrations

Four ways to connect an AI client to your GPTAdmin hub.

| # | Adapter | Best for | Auth |
|---|---------|----------|------|
| 1 | [OpenAI Action](#1-openai-action-custom-gpt) | ChatGPT (Plus/Team/Desktop) Custom GPTs | OAuth connection |
| 2 | [MCP remote](#2-mcp-remote-streamable-http) | Claude Desktop / Codex / OpenCode / Mavis | Bearer JWT (OAuth) |
| 3 | [OAuth handshake](#3-oauth-handshake) | the auth flow that feeds #1 and #2 | PKCE S256 |
| 4 | [Browser extension](#4-browser-extension) | DeepSeek / Qwen / Alice / any web chat | Hub connection page |

All four reach the same hub and the same tools. See [ADAPTERS.md](./ADAPTERS.md) (older three-way overview) and [GPTADMIN_INSTRUCTIONS.md]() (read-only reference for AI agents).

---

## 1. OpenAI Action (Custom GPT)

**When to use.** ChatGPT-family clients only: `chat.openai.com`, ChatGPT Desktop, Plus/Team. Any tool that imports an OpenAPI 3.x schema. Right pick when you want a Custom GPT that calls your hub without Codex-style per-hour tool-call quotas.

**Protocol.** REST + OpenAPI 3.1, Bearer auth. The compact control flow is `discover → schema → execute`; `job` polls background work. Legacy long names remain accepted but are not advertised.

**Schema URL.** `https://<your-hub>/actions/openapi.yaml` — the canonical, live-served spec. The repo also ships `public/openapi.json` (synonym of the same spec) so you can `curl` it locally.

### How to connect

1. Open `https://chatgpt.com/gpts/editor` → **Create** or edit a GPT.
2. **Configure → Actions → Create new action.**
3. **Import OpenAPI by URL** → `https://<your-hub>/actions/openapi.yaml`.
4. **Authentication** → choose OAuth and complete the authorization from the Hub connection page. OAuth Authorization Code + PKCE is the recommended path.
5. **Save.** The Custom GPT now exposes the relay workflow as actions.

### Bearer fallback and token diagnostics

For a named, non-interactive Custom GPT connection, the Admin Hub can issue a
managed `gptk_…` Bearer token. Configure the Action with that token and use the
public HTTPS schema URL; never use `localhost` in ChatGPT. The CLI fallback
`gptadmin issue-token` is also compatible with the Hub: it binds both JWT
`aud` and `resource` to the public MCP resource.

`PUBLIC_ORIGIN` is the issuer/public origin and `MCP_RESOURCE` is the exact
expected OAuth audience and protected resource. Both must equal the public
HTTPS origin with no trailing slash. The installer keeps signing material inside
the Hub configuration; clients never copy or configure an internal signing key.

The default `/actions/openapi.yaml` intentionally contains one Bearer security
scheme and only `discover → schema → execute → job`. This avoids Custom GPT
import warnings from webhook ingress and approval-only headers.

### Optional virtual MCP capabilities

`network-proxy` and `webhooks` are disabled by default. An administrator can
list or set them through the authenticated Hub API:

```bash
curl -H "Authorization: Bearer <admin-credential>" https://<your-hub>/admin/api/virtual-mcps
curl -X PUT -H "Authorization: Bearer <admin-credential>" -H 'Content-Type: application/json' \
  https://<your-hub>/admin/api/virtual-mcps/webhooks -d '{"enabled":true}'
```

Once enabled, they appear in `discover` and have ordinary isolated surfaces:
`/server/network-proxy/mcp`, `/server/network-proxy/actions/openapi.yaml`,
`/server/webhooks/mcp`, and `/server/webhooks/actions/openapi.yaml`. Bind an
access profile to the virtual server and its individual tools before giving it
to a client.

### Example

```bash
curl -sS -X GET https://<your-hub>/mcp-relay/servers \
  -H "Authorization: Bearer <scoped-connection>" \
  -H "Content-Type: application/json" -d '{}'
```

```text
POST /mcp-relay/call
{
  "target": "shell:roomhacker-server-100",
  "tool": "shell_exec",
  "args": { "cmd": "uptime" }
}
```

> **Connection choice.** OAuth is the supported client connection. It provides
> per-client scopes, rotation, audit and revocation without copying service
> credentials into a client configuration.

### Troubleshooting

- **"Action not found"** — schema URL isn't reachable from ChatGPT's side. The hub must be on public HTTPS (Cloudflare Tunnel, public domain, or a `become.bezrabotnyi.com`-style mirror); `http://localhost` won't work.
- **401 on every call** — reopen the Hub connection page and complete OAuth
  again; an expired or mismatched scoped connection must be renewed.
- **Schema imports, tools don't show** — the GPT editor caches schemas aggressively. Re-import.
- **Detail reference** — see `docs/CHATGPT_ACTION.md` (legacy) and `public/openapi.json` for the full operation list.

---

## 2. MCP remote (Streamable HTTP)

**When to use.** Any MCP-capable client — Claude Desktop, Codex, OpenCode, Mavis, Cherry Studio, modern AI IDEs/CLIs. The mainline adapter for 2026-era AI tooling.

**Protocol.** MCP over Streamable HTTP, JSON-RPC 2.0.

**Endpoint.** `POST https://<your-hub>/mcp` (also `GET` for `initialize` discovery).

**Auth.** A short-lived scoped OAuth connection issued by the Hub, with the
Hub's public origin and MCP resource bound into the authorization flow. Get
one via [§3](#3-oauth-handshake).

> `/mcp` accepts OAuth-issued MCP connections. Local development may relax auth
> on `http://localhost:<port>/mcp` on the Hub host itself.

### How to connect

#### Claude Desktop — `claude_desktop_config.json`

```json
{
  "mcpServers": {
    "gptadmin": {
      "type": "http",
      "url": "https://<your-hub>/mcp",
      "headers": {
        "Authorization": "Bearer  <paste JWT here>"
      }
    }
  }
}
```

Restart Claude Desktop. The `gptadmin` server exposes `discover`, `schema`, `execute`, `job`, `inspect`, and `ui`.

#### Mavis

```bash
mavis mcp add gptadmin '{"url":"https://<your-hub>/mcp"}'
mavis mcp auth login gptadmin     # opens browser → OAuth flow → writes JWT
```

#### Codex / OpenCode / others

Same shape: HTTP-type MCP server pointing at `https://<your-hub>/mcp` with `Authorization: Bearer <JWT>`.

### OAuth discovery

Modern MCP clients auto-discover the auth server:

```bash
curl -sS https://<your-hub>/.well-known/oauth-authorization-server
```

```json
{
  "issuer": "https://<your-hub>",
  "authorization_endpoint": "https://<your-hub>/oauth/authorize",
  "token_endpoint": "https://<your-hub>/oauth/token",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "client_id_metadata_document_supported": true,
  "registration_endpoint": "https://<your-hub>/register",
  "scopes_supported": ["gptadmin.read", "gptadmin.exec", "offline_access"]
}
```

Clients that support [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) / [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728) fetch this, register at `/register`, run PKCE `authorize → callback → token`, and present the hub's own consent page.

ChatGPT requires `offline_access` and the `refresh_token` grant to maintain an
OAuth connection after the original access token expires. GPTAdmin rotates a
digest-only refresh credential on each use; the client receives the replacement
while the Hub persists only its digest, so authorization survives a Hub restart.
Connections created before this capability was deployed must be authorized once
again: a server cannot add a refresh credential to an already-issued session.

### Troubleshooting

- **401 on every request** — the scoped connection expired or was issued for a
  different Hub. Re-run the OAuth flow.
- **"Transport not supported"** — client is stdio-only. Wrap with `mcp-remote` (`npx -y mcp-remote https://<your-hub>/mcp`) or pick another adapter.
- **Stream stalls mid-call** — corporate proxy buffers SSE / chunked responses. Force polling mode on the client or use a non-buffering tunnel.

---

## 3. OAuth handshake

**When to use.** Whenever you or an MCP client need a scoped connection for
`/mcp` (adapter #2), or want to authorize an OpenAI Action (adapter #1). The
handshake is **not** a client-side adapter — it feeds the other two.

**Grant type.** `authorization_code` with PKCE. **`S256` only** — plain verifiers are rejected.

**Scopes.**

- `gptadmin.read` — list servers / tools, read resources, read jobs.
- `gptadmin.exec` — execute tools (`execute`), enqueue jobs.

The hub's `/oauth/authorize` page lists the requested scopes; the user types the admin password to consent.

### Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/.well-known/oauth-authorization-server` | `GET` | RFC 8414 issuer metadata. |
| `/.well-known/oauth-protected-resource` | `GET` | RFC 9728 resource metadata. |
| `/register` | `POST` | Dynamic Client Registration — returns `client_id = "chatgpt-dynamic"`. |
| `/oauth/authorize` | `GET` | Renders the consent page (open in browser). |
| `/oauth/authorize` | `POST` | Submits the consent form (`password` = admin password). |
| `/oauth/token` | `POST` | Exchanges `code` + `code_verifier` for a JWT `access_token`. |

### Flow

1. Client generates `code_verifier` (random 43–128 chars) and
   `code_challenge = BASE64URL(SHA256(verifier))`.
2. Client `POST /register` with `redirect_uris` (e.g.
   `https://chatgpt.com/connector/oauth/...` or
   `http://127.0.0.1:<port>/callback` for local CLI clients) → receives `client_id`.
3. Browser opens `GET /oauth/authorize?response_type=code&client_id=...&redirect_uri=...&code_challenge=...&code_challenge_method=S256&resource=<hub>&scope=gptadmin.read+gptadmin.exec`.
4. User reviews scopes → types admin password → submits.
5. Hub 302s to `redirect_uri?code=...&state=...`.
6. Client `POST /oauth/token` with `code`, `code_verifier`, `redirect_uri`, `client_id` → `access_token` (JWT) → store in MCP config.
7. Every `/mcp` call: `Authorization: Bearer <access_token>`.

### JWT shape

```json
{
  "sub": "<user-entered name, optional>",
  "client_id": "chatgpt-dynamic",
  "scope": "gptadmin.read gptadmin.exec",
  "iss": "<PUBLIC_ORIGIN>",
  "aud": "<MCP_RESOURCE>",
  "iat": 1719820000,
  "exp": 1719863200
}
```

> **Redirect URI allow-list.** `/oauth/authorize` accepts only `https://chatgpt.com/.../connector/oauth/...` and `*.chatgpt.com` by default. For other clients, configure the Go hub OAuth redirect allow-list.

### Troubleshooting

- **`invalid_request: invalid redirect_uri`** — not on the allow-list. Use the canonical `https://chatgpt.com/connector/oauth/...` or relax the allow-list on the hub.
- **`invalid_grant` at `/oauth/token`** — `code_verifier` doesn't match `code_challenge`, the client/redirect binding does not match, or the 5-minute code window elapsed. Re-run `/oauth/authorize`.
- **"expired" on every call** — JWT TTL is 12 h. Most MCP clients re-trigger the flow silently.
- **Revoke everything** — admin dashboard at `https://<your-hub>/admin` →
  **Security → Revoke all** invalidates every live client connection.

---

## 4. Browser extension

**When to use.** Free web-chat AIs that don't speak MCP natively — DeepSeek, Qwen, Tongyi, Yandex Alice, ChatGPT (free tier). The extension turns "any web chat" into a gptadmin client: intercepts ` ```mcp ` code blocks the AI emits, POSTs them to your hub, pastes the result back.

**Artifact.** `apps/chatgpt-admin-app/` — a Tampermonkey / Userscripts userscript; the published build is mirrored at `public/mcp-bridge.user.js`.

### How to connect

1. **Install a userscript manager:**
   - Desktop Chrome / Edge / Brave → [Tampermonkey](https://www.tampermonkey.net/).
   - iPhone / iPad → Safari + [Userscripts](https://apps.apple.com/app/userscripts/id1463298887) app; enable under Safari → Extensions.
   - Android → Firefox from Google Play + Tampermonkey from [tampermonkey.net](https://www.tampermonkey.net/).
2. **Install the script** — open `https://<your-hub>/mcp-bridge.user.js` (or load the file from `apps/chatgpt-admin-app/`). Tampermonkey picks up the `@userscript` metadata block → **Install**.
3. **Configure:** press <kbd>Alt</kbd>+<kbd>K</kbd> (or the key icon, bottom-right):
   - **Bridge URL** — `https://<your-hub>` (no trailing slash).
   - **Connection** — choose the Hub URL and complete pairing from the Hub
     connection page.

### How it works

Two buttons added to the web-chat UI:

- **MCP All** (`Alt+M`) — inserts a compact description of every agent and its tools into the chat input, and copies the same prompt to clipboard.
- **MCP** — opens a panel to pick a specific agent with detailed tool docs.

When the AI responds with a ` ```mcp ` fenced JSON block, the script highlights it, POSTs the call to `<Bridge URL>/mcp-relay/call`, and replaces the block with the hub's response.

> If auto-insert fails on a site with a custom editor, the prompt is always on the clipboard — <kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>V</kbd>.

### Supported sites (from `@match` directives)

| Site | Status |
|------|--------|
| `chatgpt.com` | Full support |
| `chat.deepseek.com` | Full support |
| `tongyi.aliyun.com` | Full support |
| `qwenlm.github.io`, `chat.qwenlm.ai`, `chat.qwen.ai` | Full support |
| `ya.ru`, `yandex.ru`, `alice.yandex.ru`, `chat.yandex.ru` | Full support |

To add a new site, append a `@match` line to `apps/chatgpt-admin-app/public/userscript-header` (or the published `mcp-bridge.user.js`) and reinstall.

### Troubleshooting

- **Buttons don't appear** — userscript manager not enabled for the site, or the script crashed (Tampermonkey dashboard → script → Errors).
- **401 from the bridge** — complete pairing again, or verify that the Hub URL
  is publicly reachable through its Tunnel.
- **No auto-insert** — the AI emitted the code without the ` ```mcp ` fence. Re-prompt it: *"respond with the call inside a fenced block tagged `mcp`."* Fallback: paste from clipboard.
- **`GM_xmlhttpRequest` blocked** — Tampermonkey script settings: set **Run at** `document-idle`, ensure `@grant GM_xmlhttpRequest` is in the metadata block.

---

## Cross-adapter troubleshooting

- **Where are credentials?** They are managed by the Hub and its connection
  page. Do not extract service credentials from the Hub host; revoke and
  recreate a named client connection from **Security** when needed.
- **Hub isn't reachable from ChatGPT / Claude / my client** — must be public HTTPS. Localhost and LAN IPs work for manual testing but not for ChatGPT Actions or remote MCP clients. Use a Cloudflare Tunnel (see [TUNNELS.md](./TUNNELS_DOCS.md)) or a reverse proxy with a real domain.
- **MCP connects but every tool returns "unauthorized"** — open `https://<your-hub>/.well-known/oauth-authorization-server` in a browser; if it 404s the OAuth routes aren't enabled in your hub build. Re-check `apps/chatgpt-admin-app/` is deployed (or that the Go hub OAuth handlers are enabled).
- **Custom GPT doesn't see the action** — verify the schema URL is public: `curl -I https://<your-hub>/actions/openapi.yaml` from outside your network. If 4xx/5xx, the tunnel / DNS isn't pointing at the hub.
- **Browser extension doesn't inject** — userscript manager permissions: Tampermonkey Dashboard → "Allow user scripts" must be on; iOS Safari → Settings → Safari → Extensions → Userscripts → Allow; Android Firefox → add-on enabled for the current site.
- **OAuth consent page 500s** — `PUBLIC_ORIGIN` in `config/gptadmin.env` doesn't match the URL the client is calling. Set it to the **exact** origin (scheme + host + port) the client uses.
- **Quick pick by client.** ChatGPT (Plus/Team/Custom GPT) → [§1](#1-openai-action-custom-gpt). Claude Desktop / Codex / OpenCode / Mavis → [§2](#2-mcp-remote-streamable-http). Free web chat (DeepSeek / Qwen / Alice / ChatGPT free) → [§4](#4-browser-extension). Still stuck → [FAQ](./FAQ.md), [SECURITY_DOCS.md](./SECURITY_DOCS.md), or `https://<your-hub>/admin` per-section help panels.


## Secure MCP proxy/relay

For a single-purpose integration, expose one registered MCP server instead of the whole GPTAdmin relay. Every server has:

```text
/server/{slug}/mcp
/server/{slug}/actions/openapi.yaml
/server/{slug}/actions/tools/{tool_name}
```

Use `/server/openmemory/actions/openapi.yaml` for a Custom GPT that should only access OpenMemory. Use `/server/openmemory/mcp` for MCP-compatible clients. The OpenAPI schema is generated from the selected server's `tools/list`, so it stays aligned with the real MCP tools.

See [MCP Proxy Relay](./MCP_PROXY_RELAY.md).
