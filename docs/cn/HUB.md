# 中心 (`go-hub/`)

hub 是 GPT‑Админ 的中央进程。它将来自人工智能的命令代理到
shellmcp 代理、处理身份验证并为 Web 面板提供服务。

## 它的作用

1. **注册代理** — shellmcp 代理向 `POST /heartbeat` 发送心跳；
   集线器会跟踪它们，并在心跳停止时将其标记为离线。
2. **路由命令** — 当 AI 调用工具时，集线器会查找目标
   代理并转发命令。
3. **身份验证** — `AdminPassword` 用于人工管理，OAuth 用于
   MCP 客户端和代理的受管设备连接。
4. **截断输出** — 长的 stdout/stderr 被分块以保存标记（AI
   可以根据需要阅读更多内容）。
5. **为面板提供服务** — Web UI 位于 `/admin`（队列、代理运行状况、日志）。
6. **公开 MCP** — MCP 客户端的 `/mcp` 处的 MCP 远程 SSE。
7. **公开中继 OpenAPI** — `/actions/openapi.yaml` 处的规范自定义 GPT 导入；每服务器导入使用 `/server/{slug}/actions/openapi.yaml`。
8. **接受 Webhooks** — 经过身份验证的 `/webhooks/v1/{route}` 入口提供单独的默认关闭 `webhooks` 虚拟 MCP，并可以调度已配置的 MCP、提示或 Shell 操作。

## 远程秘密入口

授权的完全访问 MCP 客户端可以使用标签调用 `secret_request`
和可选的 `env_name`。集线器返回一个不透明的 `secret_ref` 和一个短
曾经住过一次 `input_url`；它从不单独返回令牌或接受
MCP JSON 中的值。在浏览器中打开`input_url`，提交一次值，然后调用
`secret_status` 确认 `ready` 而不接收明文。

要在托管 shell 作业中使用该值，请仅传递引用：

```json
{
  "target": "shell:example",
  "tool": "shell_exec",
  "args": {
    "cmd": "printenv EXAMPLE_TOKEN",
    "secret_env": {"EXAMPLE_TOKEN": "secret-ref-from-secret_request"}
  }
}
```

集线器解析参考服务器端，仅将其注入到批准的
作业，并从 MCP 结果、作业检查、审计记录和日志中对其进行编辑。
只读配置文件无法请求或检查机密。 `file` 元数据是
不透明的集线器管理的存储引用，没有权限读取文件
`system_inspect`。公共路由器保留现有的Hub起源，并且不
暴露内部2.1路由。

## 运行

```bash
python3 cli.py setup --hub --tunnel none --user
```

默认情况下，它侦听 `0.0.0.0:25900`。使用 `--port` 或 `HUB_PORT` 进行更改。

## 关键端点

|端点 |授权 |目的|
|----------|------|---------|
| `GET /admin` |管理会议 |网页面板|
| `GET /admin/api/*` |管理会议 |管理 REST API |
| `POST /mcp` | OAuth 承载 | MCP 远程 SSE（适用于 MCP 客户端）|
| `POST /heartbeat` |受管设备连接 |代理注册|
| `GET /servers` |管理会议 |注册代理列表 |
| `GET /actions/openapi.yaml` |无 |用于自定义 GPT 导入的规范 OpenAPI 架构 |
| `GET /api.json` |无 |中继模式的旧版 JSON 别名 |
| `GET /openapi.yaml` |无 |中继架构的旧版 YAML 别名 |
| `POST /oauth/authorize` | `ADMIN_PASSWORD` 形式 | Canonical OAuth 授权端点 |
| `POST /webhooks/v1/{route}` |路由令牌或 HMAC 签名 |通用事件入口 |
| `GET /webhook-jobs/{job_id}` |相同的路由凭证 |读取 webhook 作业状态/结果 |
| `GET/POST /webhook-routes` |集线器控制授权 |列出或创建路由定义而不返回机密 |
| `PUT/DELETE /webhook-routes/{route}` |集线器控制授权 |替换或删除运营商拥有的路线 |
| `POST /oauth/token` |客户凭证|规范 OAuth 令牌端点 |
| `GET/POST /secret-input/{token}` |一次性浏览器令牌 |输入一次秘密值；回复中绝不会包含它 |

有关完整详细信息，请参阅 [API 参考](./API_REFERENCE.md)。
请参阅 [Webhooks](./WEBHOOKS.md) 了解路由配置和传递语义。

## OAuth 凭证生命周期OAuth 授权代码和刷新交换使用短期访问 JWT 和
不透明的、旋转的刷新凭证。集线器仅保留摘要
刷新凭证；它在集线器重启后仍可存活五年，除非
明确撤销。客户端必须保留每个返回的替换值
刷新。还可以在没有显式 `ttl_days` 的情况下颁发托管 MCP 承载密钥
默认为五年。现有的 JWT 字符串保留其签名生命周期，并且
永远不会被更新默默地取代。

## 网页面板 (`/admin`)

打开安装程序打印的集线器 URL，然后选择 **管理**。使用您的登录
`AdminPassword`。你会看到：

- **队列** — 每个代理的活动任务和已完成任务（状态、时间、结果）
- **代理运行状况** — shellmcp 代理 + 连接的 MCP 列表（openmemory、
  chrome-devtools，...）具有实时在线/离线状态
- **日志** — 命令日志和输出，可从浏览器读取（无 SSH）

## 环境变量

有关完整列表，请参阅 [Configuration](./CONFIGURATION.md)]。要点：

|瓦尔 |必填|默认|目的|
|-----|----------|---------|---------|
| `PUBLIC_ORIGIN` |推荐| — |公共基本 URL（用于 OAuth、OpenAPI）|
| `HUB_PORT` |没有| 25900 | 25900监听端口 |

## 监督

Go hub 由 systemd 使用 `Restart=always` 直接监管。旧版 Python 看门狗单元已被删除。手动重启：

```bash
systemctl restart gptadmin-hub.service
```

## 另请参阅

- [Configuration](./CONFIGURATION.md) — 完整的环境变量参考
- [API 参考](./API_REFERENCE.md) — 端点详细信息
- [Security](./SECURITY_DOCS.md) — 身份验证模型
- [ShellMCP](./SHELLMCP.md) — 代理
