# 架构

GPT‑Админ 是 **一个 MCP 集线器** 和 **三个适配器**。工具插入轮毂；
AI 连接它的 OUT。

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

## 组件

### 1. 中心 (`go-hub/`)

中央进程。它：
- 接受来自 shellmcp 代理的心跳（注册它们，跟踪活跃度）
- 在 `/mcp` 处为 MCP 客户端公开 MCP 远程 SSE（Claude、Codex、OpenCode）
- 在 `/admin/api/*` 公开 REST 管理 API（由 Web 面板使用）
- 在 `/mcp-relay/*` 公开中继 API，在 `/actions/openapi.yaml` 公开规范的自定义 GPT 架构
- 将来自 AI 的命令代理到正确的 shellmcp 代理
- 处理 OAuth（用于 OpenAI SDK OAuth 流程）和范围内客户端连接的承载身份验证； `CTL_TOKEN` 仅适用于旧版迁移
- 为 `/admin` 处的 Web 面板提供服务

### 2.ShellMCP (`go-shellmcp/`, `client/`)

在每台目标计算机上运行的代理。它：
- 通过心跳向集线器注册 (`POST /heartbeat`)
- 在本地执行 shell 命令、文件操作、systemd 操作
- 将真实的 stdout/stderr 返回到集线器，集线器将其返回到 AI
- 适用于 Linux、macOS、Windows
- 默认情况下在用户模式（无 sudo）下运行，需要时在系统模式下运行

Go 实现 (`go-shellmcp/`) 是主要实现。 Python 客户端
保留 (`client/`) 是为了兼容性。

### 3. 三个适配器

该集线器公开了人工智能连接的三种方式。相同的中心，相同的功能 -
选择适合您的人工智能的。

|适配器|协议|对于 |端点 |
|--------|----------|-----|---------|
| **MCP 客户端** | MCP 远程 SSE |克劳德桌面、Codex、OpenCode | `/mcp` |
| **浏览器扩展** |用户脚本（Tampermonkey/Firefox）| DeepSeek、Qwen、Alice、GigaChat、ChatGPT（免费）|注入 Web UI |
| **OpenAI 行动** |休息 + OpenAPI | ChatGPT 自定义GPT，打开WebUI | `/actions/openapi.yaml` 或 `/server/{slug}/actions/openapi.yaml` |

请参阅 [Adapters](./ADAPTERS.md) 了解每个适配器的设置。

## 数据流

当你询问 AI“在 server-01 上重新启动 nginx”时：

1. **AI**决定调用一个工具（MCP工具/OpenAI Action/注入的mcp块）
2. **适配器** 将呼叫路由到集线器（`POST /mcp` 或 `/mcp-relay/call`）
3. **Hub**查找`server-01`，找到其shellmcp代理，转发命令
4. **shellmcp** 在 `server-01` 上运行 `systemctl restart nginx`，捕获输出
5. **shellmcp** 将 stdout/stderr 返回到集线器
6. **Hub** 截断长输出（节省令牌），返回到适配器
7. **Adapter**返回给AI，AI读取结果并返回报告

## 为什么这样设计

- **中心辐射型**：管理身份验证、日志记录、截断、审计的一个地方。
  添加新的AI=添加适配器，而不是重写代理。
- **MCP-native**：集线器使用 MCP，因此任何 MCP 客户端都可以工作。并且集线器可以
  本身会消耗其他 MCP（chrome-devtools、openmemory）——它们成为工具
  适用于每个连接的人工智能。
- **自定义 GPT 的中继优先**：导入架构是 `/actions/openapi.yaml` （或每个服务器），而中继调用则通过 `/mcp-relay/*`。
- **自托管**：您的服务器、您的代币、您的数据。没有什么可以离开你的
  下文。
- **任何人工智能，甚至是免费的**：浏览器扩展意味着您不需要付费
  API。免费网络聊天（Qwen、Alice、GigaChat）成为 GPT‑Админ 客户端。

## 另请参阅

- [Hub](./HUB.md) — 集线器内部结构、配置、端点
- [ShellMCP](./SHELLMCP.md) — 代理内部结构
- [Adapters](./ADAPTERS.md) — 如何连接每个 AI
