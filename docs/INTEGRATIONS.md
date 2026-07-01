# Integrations

Four ways to connect an AI client to your GPTAdmin hub.

| # | Adapter | Best for | Auth |
|---|---------|----------|------|
| 1 | [OpenAI Action](#1-openai-action-custom-gpt) | ChatGPT (Plus/Team/Desktop) Custom GPTs | Bearer `CTL_TOKEN` or OAuth |
| 2 | [MCP remote](#2-mcp-remote-streamable-http) | Claude Desktop / Codex / OpenCode / Mavis | Bearer JWT (OAuth) |
| 3 | [OAuth handshake](#3-oauth-handshake) | the auth flow that feeds #1 and #2 | PKCE S256 |
| 4 | [Browser extension](#4-browser-extension) | DeepSeek / Qwen / Alice / any web chat | `Bridge Key` = `CTL_TOKEN` |

All four reach the same hub and the same tools. See [ADAPTERS.md](./ADAPTERS.md) (older three-way overview) and [GPTADMIN_INSTRUCTIONS.md](./GPTADMIN_INSTRUCTIONS.md) (read-only reference for AI agents).

---

## 1. OpenAI Action (Custom GPT)

**When to use.** ChatGPT-family clients only: `chat.openai.com`, ChatGPT Desktop, Plus/Team. Any tool that imports an OpenAPI 3.x schema. Right pick when you want a Custom GPT that calls your hub without Codex-style per-hour tool-call quotas.

**Protocol.** REST + OpenAPI 3.1, Bearer auth, over the `/mcp-relay/*` family (`list_mcp_agents`, `list_mcp_tools`, `call_mcp_tool`, `get_mcp_job`, `resources/list`, `resources/read`).

**Schema URL.** `https://<your-hub>/actions/openapi.yaml` — the canonical, live-served spec. The repo also ships `public/openapi.json` (synonym of the same spec) so you can `curl` it locally.

### How to connect

1. Open `https://chatgpt.com/gpts/editor` → **Create** or edit a GPT.
2. **Configure → Actions → Create new action.**
3. **Import OpenAPI by URL** → `https://<your-hub>/actions/openapi.yaml`.
4. **Authentication → API key → Bearer** → paste `CTL_TOKEN` (from `config/gptadmin.env` on the hub host).
5. **Save.** The Custom GPT now exposes every operation as a tool.

### Example

```bash
curl -sS -X POST https://<your-hub>/mcp-relay/list_mcp_agents \
  -H "Authorization: Bearer $CTL_TOKEN" \
  -H "Content-Type: application/json" -d '{}'
```

```text
POST /mcp-relay/call_mcp_tool
{
  "agent_id": "shell:admin-server-100",
  "tool_name": "shell_exec",
  "arguments": { "cmd": "uptime" }
}
```

> **Bearer vs OAuth.** Today the hub accepts Bearer `CTL_TOKEN` on `/mcp-relay/*` for fast setup. For production — per-client scopes, rotation, audit, revocation — switch the auth block to OAuth ([§3](#3-oauth-handshake)). Same endpoints, stronger auth.

### Troubleshooting

- **"Action not found"** — schema URL isn't reachable from ChatGPT's side. The hub must be on public HTTPS (Cloudflare Tunnel, public domain, or a `become.bezrabotnyi.com`-style mirror); `http://localhost` won't work.
- **401 on every call** — wrong `CTL_TOKEN`, or token contains stray whitespace / newlines from copy-paste.
- **Schema imports, tools don't show** — the GPT editor caches schemas aggressively. Re-import.
- **Detail reference** — see `docs/CHATGPT_ACTION.md` (legacy) and `public/openapi.json` for the full operation list.

---

## 2. MCP remote (Streamable HTTP)

**When to use.** Any MCP-capable client — Claude Desktop, Codex, OpenCode, Mavis, Cherry Studio, modern AI IDEs/CLIs. The mainline adapter for 2026-era AI tooling.

**Protocol.** MCP over Streamable HTTP, JSON-RPC 2.0.

**Endpoint.** `POST https://<your-hub>/mcp` (also `GET` for `initialize` discovery).

**Auth.** Bearer JWT, HS256-signed by the hub using `OAUTH_CLIENT_SECRET`, 12 h expiry, `iss = PUBLIC_ORIGIN`, `aud = MCP_RESOURCE`. Get one via [§3](#3-oauth-handshake).

> `/mcp` only accepts OAuth-issued JWTs; `CTL_TOKEN` is for the REST/admin API. Local exception: `http://localhost:<port>/mcp` on the hub host itself, where the hub relaxes auth (handy for `claude_desktop_config.json` dev).

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

Restart Claude Desktop. The `gptadmin` server shows up with `list_mcp_agents`, `list_mcp_tools`, `call_mcp_tool`, `get_mcp_job`, `resources/list`, `resources/read`.

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
  "authorization_endpoint": "https://<your-hub>/authorize",
  "token_endpoint": "https://<your-hub>/token",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "client_id_metadata_document_supported": true,
  "registration_endpoint": "https://<your-hub>/register",
  "scopes_supported": ["gptadmin.read", "gptadmin.exec"]
}
```

Clients that support [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) / [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728) fetch this, register at `/register`, run PKCE `authorize → callback → token`, and present the hub's own consent page.

### Troubleshooting

- **401 on every request** — JWT expired (12 h TTL) or signed against a different `OAUTH_CLIENT_SECRET`. Re-run the OAuth flow.
- **"Transport not supported"** — client is stdio-only. Wrap with `mcp-remote` (`npx -y mcp-remote https://<your-hub>/mcp`) or pick another adapter.
- **Stream stalls mid-call** — corporate proxy buffers SSE / chunked responses. Force polling mode on the client or use a non-buffering tunnel.

---

## 3. OAuth handshake

**When to use.** Whenever you (or an MCP client) need a Bearer JWT for `/mcp` (adapter #2), or want to switch the OpenAI Action auth block from `CTL_TOKEN` to OAuth (adapter #1). The handshake is **not** a client-side adapter — it's the flow that **feeds** the other two.

**Grant type.** `authorization_code` with PKCE. **`S256` only** — plain verifiers are rejected.

**Scopes.**

- `gptadmin.read` — list servers / tools, read resources, read jobs.
- `gptadmin.exec` — call tools (`call_mcp_tool`), enqueue jobs.

The hub's `/authorize` page lists the requested scopes; the user types the admin password to consent.

### Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/.well-known/oauth-authorization-server` | `GET` | RFC 8414 issuer metadata. |
| `/.well-known/oauth-protected-resource` | `GET` | RFC 9728 resource metadata. |
| `/register` | `POST` | Dynamic Client Registration — returns `client_id = "chatgpt-dynamic"`. |
| `/authorize` | `GET` | Renders the consent page (open in browser). |
| `/authorize` | `POST` | Submits the consent form (`password` = admin password). |
| `/token` | `POST` | Exchanges `code` + `code_verifier` for a JWT `access_token`. |

### Flow

1. Client generates `code_verifier` (random 43–128 chars) and
   `code_challenge = BASE64URL(SHA256(verifier))`.
2. Client `POST /register` with `redirect_uris` (e.g.
   `https://chatgpt.com/connector/oauth/...` or
   `http://127.0.0.1:<port>/callback` for local CLI clients) → receives `client_id`.
3. Browser opens `GET /authorize?response_type=code&client_id=...&redirect_uri=...&code_challenge=...&code_challenge_method=S256&resource=<hub>&scope=gptadmin.read+gptadmin.exec`.
4. User reviews scopes → types admin password → submits.
5. Hub 302s to `redirect_uri?code=...&state=...`.
6. Client `POST /token` with `code`, `code_verifier`, `redirect_uri`, `client_id` → `access_token` (JWT) → store in MCP config.
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

> **Redirect URI allow-list.** `/authorize` accepts only `https://chatgpt.com/.../connector/oauth/...` and `*.chatgpt.com` by default. For other clients, edit `_is_chatgpt_redirect` in `gptadmin_hub.py` (around line 5500) — no per-client allow-list endpoint today.

### Troubleshooting

- **`invalid_request: invalid redirect_uri`** — not on the allow-list. Use the canonical `https://chatgpt.com/connector/oauth/...` or relax the allow-list on the hub.
- **`invalid_grant` at `/token`** — `code_verifier` doesn't match `code_challenge`, or the 5-minute code window elapsed. Re-run `/authorize`.
- **"expired" on every call** — JWT TTL is 12 h. Most MCP clients re-trigger the flow silently.
- **Revoke everything** — admin dashboard at `https://<your-hub>/admin` → **Security → Revoke all** rotates `OAUTH_CLIENT_SECRET` and kills every live JWT.

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
   - **Bridge Key** — your `CTL_TOKEN` (same one as §1).

### How it works

Two buttons added to the web-chat UI:

- **MCP All** (`Alt+M`) — inserts a compact description of every agent and its tools into the chat input, and copies the same prompt to clipboard.
- **MCP** — opens a panel to pick a specific agent with detailed tool docs.

When the AI responds with a ` ```mcp ` fenced JSON block, the script highlights it, POSTs the call to `<Bridge URL>/mcp-relay/call_mcp_tool`, and replaces the block with the hub's response.

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
- **401 from the bridge** — wrong `CTL_TOKEN`, or the hub is on localhost without a tunnel (the hub only relaxes auth on `127.0.0.1`).
- **No auto-insert** — the AI emitted the code without the ` ```mcp ` fence. Re-prompt it: *"respond with the call inside a fenced block tagged `mcp`."* Fallback: paste from clipboard.
- **`GM_xmlhttpRequest` blocked** — Tampermonkey script settings: set **Run at** `document-idle`, ensure `@grant GM_xmlhttpRequest` is in the metadata block.

---

## Cross-adapter troubleshooting

- **Where is `CTL_TOKEN`?** On the hub host: `grep ^CTL_TOKEN config/gptadmin.env`. Rotate by editing the file and `systemctl restart gptadmin-hub`.
- **Hub isn't reachable from ChatGPT / Claude / my client** — must be public HTTPS. Localhost and LAN IPs work for manual testing but not for ChatGPT Actions or remote MCP clients. Use a Cloudflare Tunnel (see [TUNNELS.md](./TUNNELS.md)) or a reverse proxy with a real domain.
- **MCP connects but every tool returns "unauthorized"** — open `https://<your-hub>/.well-known/oauth-authorization-server` in a browser; if it 404s the OAuth routes aren't enabled in your hub build. Re-check `apps/chatgpt-admin-app/` is deployed (or that `gptadmin_hub.py` has the `oauth_*` handlers).
- **Custom GPT doesn't see the action** — verify the schema URL is public: `curl -I https://<your-hub>/actions/openapi.yaml` from outside your network. If 4xx/5xx, the tunnel / DNS isn't pointing at the hub.
- **Browser extension doesn't inject** — userscript manager permissions: Tampermonkey Dashboard → "Allow user scripts" must be on; iOS Safari → Settings → Safari → Extensions → Userscripts → Allow; Android Firefox → add-on enabled for the current site.
- **OAuth consent page 500s** — `PUBLIC_ORIGIN` in `config/gptadmin.env` doesn't match the URL the client is calling. Set it to the **exact** origin (scheme + host + port) the client uses.
- **Quick pick by client.** ChatGPT (Plus/Team/Custom GPT) → [§1](#1-openai-action-custom-gpt). Claude Desktop / Codex / OpenCode / Mavis → [§2](#2-mcp-remote-streamable-http). Free web chat (DeepSeek / Qwen / Alice / ChatGPT free) → [§4](#4-browser-extension). Still stuck → [FAQ](./FAQ.md), [SECURITY_DOCS.md](./SECURITY_DOCS.md), or `https://<your-hub>/admin` per-section help panels.
