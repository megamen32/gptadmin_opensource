# 适配器

该集线器公开了人工智能连接的**三种方式**。相同的中心，相同的功能
— 选择与您的人工智能相匹配的一个。

|适配器|对于 |如何|
|---------|-----|-----|
| [MCP 客户端](#1-mcp-client) |克劳德桌面、Codex、OpenCode | MCP 远程 SSE 位于 `/mcp` |
| [浏览器扩展](#2-browser-extension) | DeepSeek、Qwen、Alice、GigaChat、ChatGPT（免费）|用户脚本（Tampermonkey/Firefox）|
| [OpenAI 操作](#3-openai-action) | ChatGPT 自定义GPT，打开WebUI |生成 OpenAPI 架构；选择 Bearer 或 OAuth |

---

## 1.MCP客户端

**适用于：** Claude Desktop、Codex、OpenCode、任何 MCP 兼容客户端。

**协议：** MCP 远程 SSE（流式 HTTP）。

**端点：** `https://your-hub.bezrabotnyi.com/mcp`（从 `/connect` 发现它）

### 设置

将集线器添加为客户端配置中的 MCP 服务器。对于 Claude Desktop，编辑
`claude_desktop_config.json`：

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

对于 Codex / OpenCode，相同的配置位于各自的 MCP 设置中。

重新启动客户端。您应该看到 `gptadmin` 工具（shell_exec、文件操作、
systemd 等）可用。

### 注释

- `/mcp` 使用 OAuth 授权码 + PKCE。从 `/connect` 或
  `/.well-known/oauth-authorization-server` 元数据；内部承载价值
  永远不会复制到客户端配置中。参见
  [配置 → OAuth](./CONFIGURATION.md#oauth)。
- 对于本地开发，请针对环回集线器使用相同的 OAuth 流程。

### 授权持久性

完成授权的MCP客户端必须保留足够的客户端管理
会话状态刷新或恢复该授权而无需询问
操作员重复设置。特别是连接器重新启动、客户端重新启动、
或正常的令牌刷新路径不得留下其他配置的 GPTADMIN
连接无法调用 `discover`。

如果 Codex 报告 `reauthentication_required` 或缺少刷新状态，请使用
客户端的重新连接控制一次以恢复访问；不要粘贴内部
将凭证集线器发送到客户端。将消息视为连接器生命周期
缺陷直到识别出所属层。在部署相关修复之前，
验证新的授权、定义的重新启动/刷新场景以及真实的
无害的 `discover -> schema -> execute` 交互。所需的证据是
在 [集成控制合同](./INTEGRATION_CONTROL_CONTRACT.md#authorization-durability) 中指定。

---

## 2. 浏览器扩展

**适用于：** DeepSeek、Qwen、Yandex Alice、Sber GigaChat、ChatGPT（免费套餐）—
任何免费的网络聊天。

**协议：** 用户脚本（通过 Tampermonkey/Firefox 在浏览器中运行）。

**安装：** https://became.bezrabotnyi.com/mcp-bridge.user.js

### 它是如何工作的

用户脚本向网络聊天 UI 添加两个按钮：
- **MCP 全部** (`Alt+M`) — 插入所有 MCP 代理的紧凑描述
  并将他们的工具放入聊天输入中。还将提示复制到剪贴板。
- **MCP** — 打开一个面板来选择具有详细工具文档的特定代理。

当 AI 使用包含 JSON 命令的 ` ```mcp ` 代码块进行响应时，
自动执行脚本：
1.高亮区块
2. 将呼叫发送至您的中心
3. 将结果插入回聊天中

### 每个平台的设置

|平台|经理 |步骤|
|----------|---------|--------|
| macOS / Windows / Linux | Chrome + [Tampermonkey](https://www.tampermonkey.net/) |从 Chrome 网上应用店安装 Tampermonkey，然后单击安装链接。 |
| iPhone | Safari + [用户脚本](https://apps.apple.com/app/userscripts/id1463298887) |安装 Userscripts 应用程序，在 Safari → 扩展中启用，然后安装。 |
|安卓 |火狐 + 篡改猴 |从 Google Play 安装 Firefox，添加 Tampermonkey，然后安装。 |

### 配置

按 `Alt+K` （或右下角的钥匙图标）并输入：
- **桥 URL** — 您的中心 URL (`https://your-hub.bezrabotnyi.com`)
- 完成一次性 `/connect` 配对/OAuth 流程；不要粘贴内部
  浏览器扩展中的集线器或代理凭据。

### 支持的网站|网站 |状态 |
|------|--------|
|聊天gpt.com |全力支持 |
|聊天.deepseek.com |全力支持 |
|聊天.qwen.ai |全力支持 |
| ya.ru / chat.yandex.ru |全力支持 |

> 如果自动插入不起作用（在某些网站上很少见），则提示始终处于
> 您的剪贴板 — 只是 `Ctrl+V` / `Cmd+V`。

---

## 3. OpenAI 行动

**对于：** ChatGPT 自定义 GPT，打开 WebUI。

**协议：** REST + 生成的 OpenAPI 架构。自定义 GPT 操作使用
您导入的架构 URL；本机 MCP 客户端使用 `/mcp` 代替。

**架构 URL：**

- 中心范围：`https://your-hub.example/actions/openapi.yaml`
- 单服务器：`https://your-hub.example/server/{slug}/actions/openapi.yaml`

中心从当前 MCP 工具列表生成这些模式。导入网址
你需要；不要手动编辑自定义 GPT 的架构。

### 设置（ChatGPT 自定义 GPT）

1.打开https://chatgpt.com/gpts/editor
2. 创建或编辑 GPT → 配置 → 操作 → 创建新操作
3. 通过 URL 导入 OpenAPI：`https://your-hub.example/actions/openapi.yaml`。
   这是生成的中心范围架构。对于单个服务器，导入
   改为 `/server/{slug}/actions/openapi.yaml` 。
4. 选择身份验证路径：

   - **Bearer** — 选择 **API key** → **Bearer**，然后仅粘贴范围内的
     Hub发行的代币值。
   - **OAuth** — 选择 **OAuth**，然后使用来自的集线器授权/令牌流程
     中心 URL。

当 GPT 只需要一个作用域令牌时，承载是最短路径。 OAuth 是
当您希望 GPT 完成 Hub 登录流程时，这是更好的选择。

5. 保存并使用每个操作**测试**控件。第一个通话打开
   ChatGPT的一次性外呼确认；批准它，然后确认
   生成的模式适用于无害的操作。

### 自定义 GPT 的 OAuth

自定义 GPT OAuth 使用授权代码流程。在中选择 **OAuth**
操作身份验证对话框并配置公共集线器：

- 授权URL：`https://your-hub.example/oauth/authorize`；
- 令牌 URL：`https://your-hub.example/oauth/token`；
- 客户端 ID：此 GPT 的稳定标签（例如 `chatgpt-custom-gpt`）；
- 范围：`gptadmin.read`（仅在需要时添加 `gptadmin.exec`）。

ChatGPT 提供其回调。某些自定义 GPT 编辑器版本不发送
PKCE 参数，因此集线器接受可互操作的授权代码
仅适用于确切的 `https://chat.openai.com/aip/g-.../oauth/callback` 的变体
回调配置文件。所有其他 OAuth 客户端都必须使用 PKCE S256。
在 Hub 授权页面上，登录并完成重定向，然后运行
再次进行无害的测试动作。 Hub 拒绝其发行者、受众、
资源、签名或密钥 ID 与其规范公共来源不匹配。

### 在不暴露凭证的情况下诊断 401

在运行 Hub 的同一安装上运行以下命令。它打印
仅配置路径、标准化发行者/受众/资源、签名密钥
指纹和独特的本地判决；它从不打印提供的 JWT 或
签名秘密。

```bash
gptadmin auth-diagnose --token '<paste-token-here>'
```

对于适用于公共 URL 但不适用于环回（或相反）的令牌，
检查 Hub 的来源、资源和签名设置均来自
相同的配置文件，然后重新启动该集线器并颁发新令牌。不
将自定义 GPT 指向 `127.0.0.1`。

### 设置（打开 WebUI）

在 Open WebUI 设置中将集线器添加为工具/功能端点：
- URL：`https://your-hub.example/mcp-relay`
- OpenAPI 架构：导入 `https://your-hub.example/actions/openapi.yaml`
- Auth：使用 scoped Bearer token，或者在客户端支持动态注册时使用来自 Hub
  元数据的 OAuth 授权码 + PKCE。

### 为什么“没有法典限制”

自定义 GPT 操作没有像 Codex 那样的每小时工具调用配额。只要
您的集线器已启动，ChatGPT 可以根据需要调用它。

---

## 我应该使用哪个适配器？

- 本地使用**Claude Desktop / Codex / OpenCode**？ → **MCP 客户端**
- 想要使用**免费网络聊天**（Qwen、Alice、GigaChat）？ → **浏览器扩展**
- 使用 **ChatGPT with Plus** 并想要自定义 GPT？ → **OpenAI 行动**

所有这三个都为您提供相同的功能 - 集线器并不关心哪个适配器
使用的人工智能。请参阅 [Architecture](./ARCHITECTURE.md) 了解原因。
