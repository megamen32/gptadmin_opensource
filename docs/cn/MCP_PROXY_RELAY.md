# GPTAdmin 作为安全 MCP 代理/中继

GPTAdmin 可以通过两个公共的、经过身份验证的兼容层公开每个注册的 MCP 服务器：

1. **MCP 兼容端点**，适用于 MCP 客户端，例如 Claude Desktop、Codex、OpenCode、类似 Cursor 的工具或任何可以通过 HTTP 进行 MCP 通信的客户端。
2. ChatGPT 自定义 GPT 和其他 OpenAPI 操作客户端的 **OpenAPI 操作端点**。

这使您可以将真正的 MCP 服务器保留在私有计算机上、NAT 后面、stdio 后面或内部隧道后面，同时为外部 AI 客户端提供一个 HTTPS 入口点，包括 GPTAdmin 身份验证、审核日志记录、路由、队列和输出处理。

`network-proxy` 和 `webhooks` 是单独的虚拟 MCP 功能。它们默认处于关闭状态，并且仅在操作员启用它们后才会出现。

## 为什么使用GPTAdmin作为前门

- 一个公共 HTTPS 端点，而不是暴露许多 MCP 服务器。
- 网关处的承载/OAuth 保护。
- 每个服务器稳定的 URL 和 slugs。
- 可与 stdio MCP、远程 MCP、shell 连接器和内部 GPTAdmin 集线器工具配合使用。
- OpenAPI 模式是从上游 MCP 服务器 `tools/list` 响应生成的，因此操作模式遵循真实的工具集。
- 呼叫仅代理至选定的 MCP 服务器；自定义 GPT 只能看到 OpenMemory、只能看到 FileShare 或任何其他单个服务器，而无法看到完整的 GPTAdmin 中继。

## 可选的虚拟 MCP 功能

|能力|默认|给予|启用 |检查 |使用|
|------------|---------|--------|--------|--------|-----|
| `network-proxy` |关闭 |有界网络隧道工具：`network_proxy_request`、`network_proxy_approve`、`network_proxy_issue`、`network_proxy_open`、`network_proxy_status`、`network_proxy_revoke` | `curl -X PUT -H "Authorization: Bearer <admin-credential>" -H 'Content-Type: application/json' https://<your-hub>/admin/api/virtual-mcps/network-proxy -d '{"enabled":true}'` | `curl -H "Authorization: Bearer <admin-credential>" https://<your-hub>/admin/api/virtual-mcps` | `https://<your-hub>/server/network-proxy/mcp` · `https://<your-hub>/server/network-proxy/actions/openapi.yaml` |
| `webhooks` |关闭 |无秘密 Webhook 路由 CRUD 和作业查找：`webhook_routes_list`、`webhook_route_create`、`webhook_route_replace`、`webhook_route_delete`、`webhook_job_get` | `curl -X PUT -H "Authorization: Bearer <admin-credential>" -H 'Content-Type: application/json' https://<your-hub>/admin/api/virtual-mcps/webhooks -d '{"enabled":true}'` | `curl -H "Authorization: Bearer <admin-credential>" https://<your-hub>/admin/api/virtual-mcps` | `https://<your-hub>/server/webhooks/mcp` · `https://<your-hub>/server/webhooks/actions/openapi.yaml` |

## 网址布局

假设您的中心发布于：

```text
https://hub.example.com
```

每个注册的 MCP 服务器都会获得一个 slug，在 `/admin` 和 `meta.public_mcp_slug` 下的 `GET /mcp-relay/servers` 中可见。

|目的|网址 |
|---------|-----|
| MCP 兼容端点 | `https://hub.example.com/server/{slug}/mcp` |
|服务器卡/发现| `https://hub.example.com/server/{slug}/card` |
|健康 | `https://hub.example.com/server/{slug}/health` |
| OpenAPI 操作架构 | `https://hub.example.com/server/{slug}/actions/openapi.yaml` |
| OpenAPI 操作架构、JSON | `https://hub.example.com/server/{slug}/actions/openapi.json` |
| OpenAPI Action工具调用| `POST https://hub.example.com/server/{slug}/actions/tools/{tool_name}` |

旧版 `/agent/{slug}/...` 路由保留为兼容性别名，但新客户端应使用 `/server/{slug}/...`。

## 示例：仅向自定义 GPT 公开 OpenMemory

在 GPT 编辑器中使用此架构 URL 操作导入：

```text
https://hub.example.com/server/openmemory/actions/openapi.yaml
```

将身份验证配置为 API 密钥/不记名令牌，并提供您的集线器接受的 GPTAdmin 令牌。

生成的架构将包含 OpenMemory 工具，例如：

```text
openmemory_query
openmemory_store_project
openmemory_store
openmemory_list
```

除非所选服务器是内部 `hub` 服务器，否则它将不包括 GPTAdmin 中继工具，例如 `call_mcp_tool`。

直接的 Action 调用如下所示：

```bash
curl -fsS \
  -H 'Authorization: Bearer <GPTADMIN_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"query":"deployment notes","project_id":"gptadmin","k":3}' \
  https://hub.example.com/server/openmemory/actions/tools/openmemory_query
```

响应形状：

```json
{
  "server_id": "OpenMemory",
  "tool_name": "openmemory_query",
  "status": "completed",
  "response": {
    "content": [
      {"type": "text", "text": "..."}
    ]
  }
}
```

## 示例：连接 MCP 兼容客户端

当客户端已经使用 MCP 时，使用每服务器 MCP URL：

```text
https://hub.example.com/server/openmemory/mcp
```

此端点接受标准 MCP JSON-RPC 方法，例如：

```text
initialize
tools/list
tools/call
resources/list
resources/read
prompts/list
prompts/get
```

对于完整的 GPTAdmin 中心界面，请使用：

```text
https://hub.example.com/server/hub/mcp
```

对于单个上游服务器，使用其 slug：

```text
https://hub.example.com/server/fileshare/mcp
https://hub.example.com/server/chromedevtools-roomhacker-server-100/mcp
https://hub.example.com/server/openmemory/mcp
```

## 模式是如何生成的

当客户要求：

```text
GET /server/{slug}/actions/openapi.yaml
```GPTAdmin 将 `{slug}` 解析为一个已注册的 MCP 服务器，调用 `tools/list`，并将每个 MCP 工具描述符转换为 OpenAPI `POST /server/{slug}/actions/tools/{tool_name}` 操作。 MCP `inputSchema` 成为 OpenAPI 请求正文架构。

这意味着：

- 添加新的 MCP 工具会自动更新 OpenAPI Action 架构；
- 删除工具会将其从生成的模式中删除；
- 每台服务器的自定义 GPT 保持小而集中；
- 用户无需手动维护大型 OpenAPI 文件。
- 启用的虚拟 MCP 拥有自己的每服务器架构；默认集线器 `/actions/openapi.yaml` 保持仅中继状态，并省略 `network-proxy` 和 `webhooks`。

## 安全说明

- 不要将原始 stdio MCP 服务器直接暴露到互联网上；将 GPTAdmin 放在前面。
- 对公共中心使用 HTTPS。
- 如果与自定义 GPT 或 MCP 客户端共享，请使用强承载/OAuth 凭据并轮换它们。
- 当 GPT 仅需要一种功能时，首选针对自定义 GPT 的每服务器 OpenAPI 架构。
- 默认集线器 `/actions/openapi.yaml` 用于仅中继自定义 GPT 导入；它不包括可选的虚拟 MCP。
- 仅当客户端确实需要完整的中继/管理界面时，才使用 `/server/hub/mcp` 或 GPTAdmin Apps SDK。

## 另请参阅

- [API 参考](./API_REFERENCE.md)
- [集成](./INTEGRATIONS.md)
- [安全](./SECURITY_DOCS.md)
- [集线器](./HUB.md)
