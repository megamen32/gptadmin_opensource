# API 参考

## 操作探针

|方法|路径|授权 |目的|
| ---| ---| ---| ---|
| `GET` | `/healthz` |无 |仅活性；返回 `ok`。 |
| `GET` | `/version` |无 |构建版本并提交身份。 |
| `GET` | `/metrics` |无 |有界聚合集线器计数；从不包含凭据、参数或文件内容。 |

集线器公开的 REST + MCP 端点。

## 请求关联

集线器接受可选的 W3C `traceparent` 请求标头。它返回一个
已验证的子 `traceparent` 和有界的 `X-Request-ID` 响应标头；
排队中继和 ShellMCP 作业通过轮询携带相同的相关字段
和结果交付。无效的跟踪标头将被丢弃并替换。踪迹
元数据从不包含命令参数、凭据或文件内容。

操作员可以选择使用 OTLP/HTTP 日志导出
`GPTADMIN_OTLP_ENDPOINT`。外部收集器必须使用HTTPS；普通 HTTP 是
仅接受环回开发收集器。出口商使用
有界异步队列并导出列入白名单的事件字段，例如
策略决策、工具、结果参考和跟踪 ID。它从不出口原料
参数、命令、凭据、URL 或文件内容以及收集器
传送失败不会导致原始集线器请求失败。

## 远程秘密入口

完全访问 MCP 客户端可以调用 `secret_request` 和 `secret_status`。的
首先仅返回 `request_id`、`input_url`、`secret_ref`、`env_name`、`file`
和过期元数据。操作员将值提交一次
`POST /secret-input/{token}`; MCP 和响应机构均不接受或
返回明文。稍后的 `shell_exec` 可能会通过
`secret_env: {"ENV_NAME": "secret_ref"}`。中心作业响应和日志编辑
解析值和只读配置文件无法访问这些操作。

## 授权快速参考

|端点 |授权 |
|----------|------|
| `GET /admin` |基本 (`CTL_TOKEN`) |
| `GET /admin/api/*` |持有者 `CTL_TOKEN` |
| `POST /mcp` | OAuth 承载 |
| `POST /heartbeat` |持有者 `SHELLMCP_TOKEN` |
| `GET /servers` |持有者 `CTL_TOKEN` |
| `GET /actions/openapi.yaml` |无 |
| `GET /server/{slug}/actions/openapi.yaml` |无 |
| `GET /api.json` |无（旧别名；不是自定义 GPT 路径）|
| `GET /openapi.yaml` |无（旧别名；不是自定义 GPT 路径）|
| `POST /oauth/authorize` | `ADMIN_PASSWORD` 表单 |
| `POST /oauth/token` |客户凭证|

自定义 GPT 导入使用 `/actions/openapi.yaml` 或 `/server/{slug}/actions/openapi.yaml`；
中继调用通过 `/mcp-relay/*`。 `CTL_TOKEN` 仅限旧版/管理员。

请参阅[配置 → 身份验证模型](./CONFIGURATION.md#auth-model)。

---

## 管理 API (`/admin/api/*`)

使用 `CTL_TOKEN` 进行不记名身份验证。仅旧版管理/Web 面板迁移详细信息。

### `GET /servers`

列出已注册的 shellmcp 代理。

```json
{
  "servers": [
    { "name": "server-01", "url": "http://10.0.0.5:25901", "alive": true, "last_seen": "2026-06-29T10:00:00Z" }
  ]
}
```

### `POST /exec`

在目标代理上执行 shell 命令。

```json
{
  "server": "server-01",
  "cmd": "systemctl status nginx"
}
```

响应（如果太长则被截断以保存标记）：

```json
{
  "stdout": "● nginx.service - The nginx HTTP server...",
  "stderr": "",
  "exit_code": 0,
  "truncated": false
}
```

### `GET /tasks/{task_id}`

获取后台任务的状态。

### `POST /file/backup`

在编辑之前创建文件的托管备份。

### `GET /system/info?server=server-01`

目标代理的 CPU、RAM、磁盘、正常运行时间。

旧版导入架构：`https://became.bezrabotnyi.com/api.json` 或
`https://became.bezrabotnyi.com/openapi.yaml`。自定义 GPT 导入用途
`/actions/openapi.yaml` 或 `/server/{slug}/actions/openapi.yaml`，不是这些
遗留别名。

---

## MCP 端点 (`/mcp`)

OAuth 承载身份验证。 MCP 远程 SSE（流式 HTTP）。

MCP 客户端（Claude Desktop、Codex、OpenCode）连接至此处。集线器暴露
shellmcp 工具作为 MCP 工具：- `shell_exec` — 运行 shell 命令
- `file_read` — 读取文件
- `file_write` — 写入文件（带备份）
- `file_backup` — 创建托管备份
- `systemd_status` / `systemd_start` / `systemd_stop` / `systemd_restart`
- `system_info` — CPU/RAM/磁盘/正常运行时间
- `system_health` — 快速健康检查
- `dir` — 列出目录

请参阅[适配器 → MCP 客户端](./ADAPTERS.md#1-mcp-client) 设置。

---

## 代理端点 (shellmcp)

这些由集线器调用，而不是直接由人工智能调用。持有者 `SHELLMCP_TOKEN`。

|端点 |方法|目的|
|----------|--------|---------|
| `/exec` |发布 |运行 shell 命令 |
| `/file` |获取/发布 |读/写文件 |
| `/dir` |获取 |列出目录 |
| `/systemd/{action}` |发布 |状态/启动/停止/重新启动/启用 |
| `/system/info` |获取 | CPU/RAM/磁盘/正常运行时间 |
| `/system/health` |获取 |健康检查|
| `/heartbeat` |发布 |向集线器注册（由代理→集线器调用） |

---

## OAuth 端点

|端点 |方法|目的|
|----------|--------|---------|
| `/oauth/authorize` |获取/发布 |授权端点 |
| `/oauth/token` |发布 |令牌端点 |
| `/.well-known/oauth-authorization-server` |获取 | OAuth 服务器元数据 |

请参阅[配置 → OAuth](./CONFIGURATION.md#oauth)。

---

## OpenAPI 架构

- 规范自定义 GPT 导入：`GET /actions/openapi.yaml`
- 每服务器导入：`GET /server/{slug}/actions/openapi.yaml`
- 旧别名：`GET /api.json` 和 `GET /openapi.yaml`

规范导入 URL 是公开的（无需身份验证）。旧别名保持公开
用于迁移，但它们不是自定义 GPT 路径。

## 后台任务

长时间运行的命令返回 `task_id` 而不是阻塞：

```json
{ "task_id": "abc123", "status": "running" }
```

使用 `GET /tasks/abc123` 进行轮询，直到 `status: completed`。 AI 这样做
自动。

## 输出截断

长的 stdout/stderr 被分块。响应内容包括：

```json
{
  "stdout": "...first 1MB...",
  "truncated": true,
  "spilled_path": "/tmp/spilled.stdout",
  "preview_head": "...",
  "preview_tail": "..."
}
```

人工智能可以通过后续通话按需阅读更多内容。这节省了代币——
人工智能只读取它需要回答的内容。


## 每服务器 MCP 和 OpenAPI Action 代理

GPTAdmin 通过经过身份验证的每服务器路由公开每个注册的 MCP 服务器。将 `{slug}` 替换为 `GET /mcp-relay/servers` 中的 `meta.public_mcp_slug`。

|方法|路径|目的|
|--------|------|---------|
| `GET` / `POST` | `/server/{slug}/mcp` |一台服务器的 MCP 兼容端点 |
| `GET` | `/server/{slug}/card` |服务器发现卡 |
| `GET` | `/server/{slug}/health` |服务器健康状况 |
| `GET` | `/server/{slug}/actions/openapi.yaml` |为自定义 GPT 操作生成 OpenAPI 架构 |
| `GET` | `/server/{slug}/actions/openapi.json` |与 JSON 相同的架构 |
| `POST` | `/server/{slug}/actions/tools/{tool_name}` |将 OpenAPI 操作调用代理到一个 MCP 工具 |

操作架构是从所选 MCP 服务器的 `tools/list` 生成的。每个操作请求主体是MCP工具`inputSchema`。 Action 调用响应包装上游 MCP 结果：

## 可选的虚拟 MCP 管理

`network-proxy` 和 `webhooks` 默认情况下处于关闭状态。默认 `/actions/openapi.yaml` 保持仅中继状态并排除两者。

|方法|路径|目的|
|--------|------|---------|
| `GET` | `/admin/api/virtual-mcps` |在一次调用中检查两种状态。 |
| `PUT` | `/admin/api/virtual-mcps/{id}` |保留 `{"enabled": true|false}` 为 `network-proxy` 或 `webhooks`。 |

`network-proxy` 提供有界网络隧道工具：`network_proxy_request`、`network_proxy_approve`、`network_proxy_issue`、`network_proxy_open`、`network_proxy_status`、 `network_proxy_revoke`。`webhooks` 提供无秘密的 Webhook 路由 CRUD 和作业查找：`webhook_routes_list`、`webhook_route_create`、`webhook_route_replace`、`webhook_route_delete`、`webhook_job_get`。

启用检查：`GET /mcp-relay/servers` 仅显示已启用的虚拟 MCP。

启用后使用：`/server/{slug}/mcp` 和 `/server/{slug}/actions/openapi.yaml`。

```json
{
  "server_id": "OpenMemory",
  "tool_name": "openmemory_query",
  "status": "completed",
  "response": {"content": []}
}
```

有关示例，请参阅 [MCP 代理中继](./MCP_PROXY_RELAY.md)。
