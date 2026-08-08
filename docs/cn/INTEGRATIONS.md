# 集成

将 AI 客户端连接到 GPTAdmin 中心的四种方法。

| ＃|适配器|最适合 |授权 |
|---|---------|----------|--------|
| 1 | [OpenAI 操作](#1-openai-action-custom-gpt) | ChatGPT（Plus/团队/桌面）自定义 GPT | OAuth 连接 |
| 2 | [MCP 远程](#2-mcp-remote-streamable-http) |克劳德桌面 / Codex / OpenCode / Mavis |不记名 JWT (OAuth) |
| 3 | [OAuth 握手](#3-oauth-handshake) |提供 #1 和 #2 的身份验证流程 | PKCE S256 |
| 4 | [浏览器扩展](#4-browser-extension) | DeepSeek / Qwen / Alice / 任何网络聊天 |集线器连接页面|

所有四个都到达相同的中心和相同的工具。请参阅 [ADAPTERS.md](./ADAPTERS.md)（旧版三向概述）和 [GPTADMIN_INSTRUCTIONS.md]()（AI 代理的只读参考）。

---

## 1.OpenAI 操作（自定义 GPT）

**何时使用。** 仅 ChatGPT 系列客户端：`chat.openai.com`、ChatGPT 桌面版、Plus/Team。导入 OpenAPI 3.x 架构的任何工具。当您想要一个自定义 GPT 来调用您的中心而无需 Codex 风格的每小时工具调用配额时，请正确选择。

**协议。** REST + OpenAPI 3.1，承载身份验证。中继流程为`/mcp-relay/servers → /mcp-relay/tools → /mcp-relay/call`； `job` 轮询后台工作。旧的长名称仍然被接受，但不会做广告。

**架构 URL。** `https://<your-hub>/actions/openapi.yaml` — 自定义 GPT 的规范、实时服务规范。每服务器导入使用 `https://<your-hub>/server/{slug}/actions/openapi.yaml`；该存储库还附带 `public/openapi.json` （同一规范的同义词），因此您可以在本地 `curl` 。

### 如何连接

1. 打开 `https://chatgpt.com/gpts/editor` → **创建**或编辑 GPT。
2. **配置 → 操作 → 创建新操作。**
3. **通过 URL 导入 OpenAPI** → `https://<your-hub>/actions/openapi.yaml`。
4. **身份验证** → 选择OAuth并从Hub连接页面完成授权。推荐路径为OAuth授权码+PKCE。
5. **保存。** 自定义 GPT 现在将中继工作流程公开为操作。

### 承载回退和令牌诊断

对于命名的非交互式自定义 GPT 连接，管理中心可以发出
托管 `gptk_…` 不记名令牌。使用该令牌配置操作并使用
公共 HTTPS 架构 URL；切勿在 ChatGPT 中使用 `localhost`。 CLI 后备
`gptadmin issue-token` 也与 Hub 兼容：它绑定了 JWT
`aud` 和 `resource` 到公共 MCP 资源。

`PUBLIC_ORIGIN` 是发行者/公共来源，`MCP_RESOURCE` 是确切的
预期的 OAuth 受众和受保护的资源。两者必须与公众平等
HTTPS 来源，不带尾部斜杠。安装人员将签名材料保存在里面
集线器配置；客户端永远不会复制或配置内部签名密钥。

默认的 `/actions/openapi.yaml` 有意包含一个 Bearer 安全性
方案且仅 `discover → schema → execute → job`。该中继流是
规范的自定义 GPT 导入； Webhook 入口和仅批准标头保留
超出默认架构。

### 可选的虚拟 MCP 功能

默认情况下禁用 `network-proxy` 和 `webhooks`。管理员可以
通过经过身份验证的 Hub API 列出或设置它们：

```bash
curl -H "Authorization: Bearer <admin-credential>" https://<your-hub>/admin/api/virtual-mcps
curl -X PUT -H "Authorization: Bearer <admin-credential>" -H 'Content-Type: application/json' \
  https://<your-hub>/admin/api/virtual-mcps/webhooks -d '{"enabled":true}'
```

启用后，它们出现在 `discover` 中并具有普通的隔离表面：
`/server/network-proxy/mcp`、`/server/network-proxy/actions/openapi.yaml`、
`/server/webhooks/mcp` 和 `/server/webhooks/actions/openapi.yaml`。绑定一个
在提供之前访问虚拟服务器及其各个工具的配置文件
给客户。

### 示例

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

> **连接选择。** OAuth 是支持的客户端连接。它提供
> 每个客户的范围、轮换、审核和撤销，无需复制服务
> 将凭据添加到客户端配置中。

### 故障排除- **“未找到操作”** — 无法从 ChatGPT 端访问架构 URL。中心必须位于公共 HTTPS（Cloudflare Tunnel、公共域或 `become.bezrabotnyi.com` 样式镜像）上； `http://localhost` 不起作用。
- **每次调用都会出现 401** — 重新打开 Hub 连接页面并完成 OAuth
  再次；必须更新过期或不匹配范围的连接。
- **架构导入，工具不显示** - GPT 编辑器积极缓存架构。重新导入。
- **详细参考** — 请参阅 `docs/CHATGPT_ACTION.md`（旧版）和 `public/openapi.json` 了解完整操作列表。

---

## 2.MCP 远程（流式 HTTP）

**何时使用。** 任何支持 MCP 的客户端 — Claude Desktop、Codex、OpenCode、Mavis、Cherry Studio、现代 AI IDE/CLI。 2026 时代 AI 工具的主线适配器。

**协议。** MCP over Streamable HTTP、JSON-RPC 2.0。

**端点。** `POST https://<your-hub>/mcp`（也用于 `initialize` 发现的 `GET`）。

**Auth.** 由 Hub 发出的短暂范围的 OAuth 连接，带有
Hub 的公共来源和 MCP 资源绑定到授权流中。获取
一个通过 [§3](#3-oauth-handshake).

> `/mcp` 接受 OAuth 颁发的 MCP 连接。地方发展或可放宽授权
> 在 Hub 主机本身上的 `http://localhost:<port>/mcp` 上。

### 如何连接

#### 克劳德桌面 — `claude_desktop_config.json`

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

重新启动克劳德桌面。 `gptadmin` 服务器公开 `discover`、`schema`、`execute`、`job`、`inspect` 和`ui`。

#### 梅维斯

```bash
mavis mcp add gptadmin '{"url":"https://<your-hub>/mcp"}'
mavis mcp auth login gptadmin     # opens browser → OAuth flow → writes JWT
```

#### Codex / OpenCode / 其他

形状相同：HTTP 类型 MCP 服务器指向 `https://<your-hub>/mcp` 和 `Authorization: Bearer <JWT>`。

### OAuth 发现

现代 MCP 客户端自动发现身份验证服务器：

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

支持 [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) / [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)] 的客户端获取此内容，在 `/register` 注册，运行 PKCE `authorize → callback → token`，并显示中心自己的同意页面。

ChatGPT 需要 `offline_access` 和 `refresh_token` 授予来维持
原始访问令牌过期后的 OAuth 连接。 GPTAdmin 轮换
每次使用时仅摘要刷新凭证；客户收到替换品
而集线器仅保留其摘要，因此授权在集线器重新启动后仍然有效。
部署此功能之前创建的连接必须经过一次授权
再次强调：服务器无法将刷新凭证添加到已发出的会话中。

### 故障排除

- **每个请求均出现 401** — 范围连接已过期或针对某个请求发出
  不同的枢纽。重新运行 OAuth 流程。
- **“不支持传输”** — 客户端仅支持 stdio。使用 `mcp-remote` (`npx -y mcp-remote https://<your-hub>/mcp`) 换行或选择其他适配器。
- **流在通话中停止** — 公司代理缓冲 SSE/分块响应。在客户端上强制轮询模式或使用非缓冲隧道。

---

## 3.OAuth 握手

**何时使用。** 每当您或 MCP 客户端需要范围连接时
`/mcp`（适配器 #2），或者想要授权 OpenAI 操作（适配器 #1）。的
握手不是客户端适配器——它为另外两个适配器提供支持。

**授予类型。** `authorization_code` 与 PKCE。 **仅限`S256`** — 普通验证程序被拒绝。

**范围。**

- `gptadmin.read` — 列出服务器/工具、读取资源、读取作业。
- `gptadmin.exec` — 执行工具 (`execute`)，将作业排队。

中心的 `/oauth/authorize` 页面列出了请求的范围；用户输入管理员密码以表示同意。

### 端点|端点 |方法|目的|
|----------|--------|---------|
| `/.well-known/oauth-authorization-server` | `GET` | RFC 8414 发行者元数据。 |
| `/.well-known/oauth-protected-resource` | `GET` | RFC 9728 资源元数据。 |
| `/register` | `POST` |动态客户端注册 — 返回 `client_id = "chatgpt-dynamic"`。 |
| `/oauth/authorize` | `GET` |呈现同意页面（在浏览器中打开）。 |
| `/oauth/authorize` | `POST` |提交同意书（`password` = 管理员密码）。 |
| `/oauth/token` | `POST` |将 `code` + `code_verifier` 交换为 JWT `access_token`。 |

### 流量

1. 客户端生成 `code_verifier` （随机 43–128 个字符）并且
   `code_challenge = BASE64URL(SHA256(verifier))`。
2. 客户端 `POST /register` 与 `redirect_uris` （例如
   `https://chatgpt.com/connector/oauth/...` 或
   `http://127.0.0.1:<port>/callback` 对于本地 CLI 客户端） → 接收 `client_id`。
3. 浏览器打开`GET /oauth/authorize?response_type=code&client_id=...&redirect_uri=...&code_challenge=...&code_challenge_method=S256&resource=<hub>&scope=gptadmin.read+gptadmin.exec`。
4. 用户查看范围 → 输入管理员密码 → 提交。
5.集线器 302 至 `redirect_uri?code=...&state=...`。
6. 客户端 `POST /oauth/token` 与 `code`、`code_verifier`、`redirect_uri`、`client_id` → `access_token` (JWT) → 存储在 MCP 中配置。
7. 每次 `/mcp` 调用：`Authorization: Bearer <access_token>`。

### JWT 形状

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

> **重定向 URI 允许列表。** `/oauth/authorize` 默认情况下仅接受 `https://chatgpt.com/.../connector/oauth/...` 和 `*.chatgpt.com`。对于其他客户端，配置 Go hub OAuth 重定向允许列表。

### 故障排除

- **`invalid_request: invalid redirect_uri`** — 不在允许列表中。使用规范的 `https://chatgpt.com/connector/oauth/...` 或放宽集线器上的允许列表。
- **`invalid_grant` at `/oauth/token`** — `code_verifier` 与 `code_challenge` 不匹配，客户端/重定向绑定不匹配，或者 5 分钟代码窗口已过。重新运行 `/oauth/authorize`。
- **每次调用都会“过期”** — JWT TTL 为 12 小时。大多数 MCP 客户端都会以静默方式重新触发流程。
- **撤销所有内容** — `https://<your-hub>/admin` 的管理仪表板 →
  **安全→全部撤销**使每个实时客户端连接失效。

---

## 4. 浏览器扩展

**何时使用。** 原生不支持 MCP 的免费网络聊天 AI — DeepSeek、Qwen、Tongyi、Yandex Alice、ChatGPT（免费套餐）。该扩展将“任何网络聊天”转换为 gptadmin 客户端：拦截 AI 发出的 ` ```mcp ` 代码块，将它们发布到您的集线器，将结果粘贴回来。

**Artifact.** `apps/chatgpt-admin-app/` — Tampermonkey / Userscripts 用户脚本；已发布的版本镜像在 `public/mcp-bridge.user.js` 处。

### 如何连接

1. **安装用户脚本管理器：**
   - 桌面 Chrome / Edge / Brave → [Tampermonkey](https://www.tampermonkey.net/)。
   - iPhone / iPad → Safari + [Userscripts](https://apps.apple.com/app/userscripts/id1463298887) 应用程序；在 Safari → 扩展下启用。
   - Android → 来自 Google Play 的 Firefox + 来自 [tampermonkey.net](https://www.tampermonkey.net/) 的 Tampermonkey。
2. **安装脚本** — 打开 `https://<your-hub>/mcp-bridge.user.js` （或从 `apps/chatgpt-admin-app/` 加载文件）。 Tampermonkey 选择 `@userscript` 元数据块 → **安装**。
3. **配置：** 按 <kbd>Alt</kbd>+<kbd>K</kbd> （或右下角的钥匙图标）：
   - **桥 URL** — `https://<your-hub>`（无尾部斜杠）。
   - **连接** — 选择集线器 URL 并从集线器完成配对
     连接页面。

### 它是如何工作的

网络聊天 UI 中添加了两个按钮：

- **MCP All** (`Alt+M`) — 将每个代理及其工具的紧凑描述插入聊天输入，并将相同的提示复制到剪贴板。
- **MCP** — 打开一个面板来选择具有详细工具文档的特定代理。当 AI 使用 ` ```mcp ` 防护 JSON 块进行响应时，脚本会突出显示该块，将调用 POST 到 `<Bridge URL>/mcp-relay/call`，并用集线器的响应替换该块。

> 如果在使用自定义编辑器的网站上自动插入失败，则提示始终显示在剪贴板上 - <kbd>Ctrl</kbd>/<kbd>⌘</kbd>+<kbd>V</kbd>。

### 支持的站点（来自 `@match` 指令）

|网站 |状态 |
|------|--------|
| `chatgpt.com` |全力支持 |
| `chat.deepseek.com` |全力支持 |
| `tongyi.aliyun.com` |全力支持 |
| `qwenlm.github.io`、`chat.qwenlm.ai`、`chat.qwen.ai` |全力支持 |
| `ya.ru`、`yandex.ru`、`alice.yandex.ru`、`chat.yandex.ru` |全力支持 |

要添加新站点，请将 `@match` 行附加到 `apps/chatgpt-admin-app/public/userscript-header`（或已发布的 `mcp-bridge.user.js`）并重新安装。

### 故障排除

- **按钮不出现** — 站点未启用用户脚本管理器，或者脚本崩溃（Tampermonkey 仪表板 → 脚本 → 错误）。
- **来自网桥的 401** — 再次完成配对，或验证集线器 URL
  可以通过其隧道公开访问。
- **无自动插入** — AI 发出的代码没有 ` ```mcp ` 栅栏。重新提示它：*“在标记为 `mcp` 的隔离块内响应调用。”* 后备：从剪贴板粘贴。
- **`GM_xmlhttpRequest` 被阻止** — Tampermonkey 脚本设置：设置 **运行于** `document-idle`，确保 `@grant GM_xmlhttpRequest` 位于元数据块中。

---

## 跨适配器故障排除

- **凭证在哪里？** 它们由集线器及其连接管理
  页。不要从 Hub 主机中提取服务凭证；撤销和
  需要时从 **Security** 重新创建命名客户端连接。
- **无法从 ChatGPT / Claude / 我的客户端访问集线器** — 必须是公共 HTTPS。本地主机和 LAN IP 适用于手动测试，但不适用于 ChatGPT 操作或远程 MCP 客户端。使用 Cloudflare 隧道（请参阅 [TUNNELS.md](./TUNNELS_DOCS.md)）或具有真实域的反向代理。
- **MCP 连接，但每个工具都返回“未经授权”** — 在浏览器中打开 `https://<your-hub>/.well-known/oauth-authorization-server`；如果出现 404 错误，则表示您的集线器构建中未启用 OAuth 路由。重新检查 `apps/chatgpt-admin-app/` 是否已部署（或者 Go hub OAuth 处理程序是否已启用）。
- **自定义 GPT 看不到该操作** — 验证架构 URL 是否公开：来自网络外部的 `curl -I https://<your-hub>/actions/openapi.yaml`。如果是 4xx/5xx，则隧道/DNS 未指向集线器。
- **浏览器扩展不会注入** — 用户脚本管理器权限：Tampermonkey 仪表板 → 必须打开“允许用户脚本”； iOS Safari → 设置 → Safari → 扩展 → 用户脚本 → 允许； Android Firefox → 为当前站点启用了附加组件。
- **OAuth 同意页面 500s** — `config/gptadmin.env` 中的 `PUBLIC_ORIGIN` 与客户端调用的 URL 不匹配。将其设置为客户端使用的**准确**源（方案+主机+端口）。
- **由客户快速选择。** ChatGPT（Plus/团队/自定义 GPT）→ [§1](#1-openai-action-custom-gpt)。 Claude Desktop / Codex / OpenCode / Mavis → [§2](#2-mcp-remote-streamable-http)。免费网络聊天（DeepSeek / Qwen / Alice / ChatGPT 免费）→ [§4](#4-browser-extension)。仍然卡住 → [FAQ](./FAQ.md)、[SECURITY_DOCS.md](./SECURITY_DOCS.md) 或 `https://<your-hub>/admin` 每个部分的帮助面板。


## 安全 MCP 代理/中继

对于单一用途集成，请公开一台已注册的 MCP 服务器而不是整个 GPTAdmin 中继。每个服务器都有：

```text
/server/{slug}/mcp
/server/{slug}/actions/openapi.yaml
/server/{slug}/actions/tools/{tool_name}
```

对于仅应访问 OpenMemory 的自定义 GPT，请使用 `/server/openmemory/actions/openapi.yaml`。对于与 MCP 兼容的客户端，请使用 `/server/openmemory/mcp`。 OpenAPI 架构是从所选服务器的 `tools/list` 生成的，因此它与真实的 MCP 工具保持一致。

请参阅 [MCP 代理中继](./MCP_PROXY_RELAY.md)。
