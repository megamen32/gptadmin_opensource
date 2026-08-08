# 配置

完整的环境变量参考、身份验证模型和 OAuth 设置。

## 集线器环境变量 (`go-hub/`)

### 授权

|瓦尔 |必填|默认|目的|
|-----|----------|---------|---------|
| `ADMIN_PASSWORD` | **是** | — | `/oauth/authorize` HTML 表单和管理会话的密码。 |
| `CTL_TOKEN` |仅限现有安装| — |已弃用的兼容性承载；不要创建或复制它。它一直有效，直到其所有者明确轮换或删除它为止。 |
| `OAUTH_CLIENT_SECRET` |对于 `/mcp` | — |签署 OAuth 不记名令牌。使用 `openssl rand -hex 32` 生成。 |
| `PUBLIC_ORIGIN` |推荐| — |公共基本 URL（例如 `https://your-hub.bezrabotnyi.com`）。用于 OAuth + OpenAPI。 |
| `MCP_RESOURCE` |推荐| `$PUBLIC_ORIGIN` | MCP 资源标识符。 |
| `GPTADMIN_AUTH_RATE_LIMIT` |可选 | `60` 每个客户端每分钟 |在临时 `429` 响应之前，来自一个客户端的最大失败管理/控制/MCP 身份验证尝试次数。成功的身份验证不会消耗预算。 |

### 网络

|瓦尔 |默认|目的|
|-----|---------|---------|
| `HUB_PORT` | 25900 | 25900监听端口 |
| `HUB_HOST` | `127.0.0.1` |听主持人；公共部署必须在 Tunnel/HAOS 层设置显式边界主机。 `HUB_BIND` 仍然是安装程序兼容性别名。 |
| `CORS_ORIGINS` | `*` |允许的 CORS 来源（逗号分隔）|

### 行为

|瓦尔 |默认|目的|
|-----|---------|---------|
| `EXEC_TIMEOUT` | 120 | 120最大命令执行时间（秒）|
| `LOG_LIMIT_B` | 65536 |每个 ShellMCP 代理内联 stdout/stderr 尾部预算。较大的命令输出被假脱机到磁盘；中心/客户端响应预算是单独配置的。 |
| `HEARTBEAT_TIMEOUT` | 60|座席被标记为离线前的秒数 |
| `BACKGROUND_TASK_TTL` | 3600 | 3600已完成的后台作业保留多长时间（秒） |
| `GPTADMIN_STARTUP_INSTRUCTIONS_FILE` | `$GPTADMIN_CONFIG_DIR/startup_instructions.md` | MCP 客户端的可选本地 Markdown 启动说明。 |
| `GPTADMIN_STARTUP_INSTRUCTIONS` | — |启动指令的可选环境覆盖；优先于文件。 |
| `GPTADMIN_INSTRUCTION_SETS_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/instruction_sets_state.json` |命名配置文件指令集的限制性状态文件。 |
| `GPTADMIN_VIRTUAL_MCP_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/virtual_mcps_state.json` | `network-proxy` 和 `webhooks` 保持开/关状态；如果文件不存在，则两者均保持关闭状态。 |
| `GPTADMIN_WEBHOOK_CONFIG_FILE` | `$GPTADMIN_CONFIG_DIR/webhooks.json` |运营商拥有的通用 Webhook 路由定义。 |
| `GPTADMIN_WEBHOOK_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/webhook_state.json` |持久的 webhook 作业和重播密钥；使用模式 `0600` 编写。 |

### MCP启动说明

GPTAdmin 在 MCP `initialize` 中提供通用系统管理指南
结果。要持久地自定义它，请创建
`$GPTADMIN_CONFIG_DIR/startup_instructions.md`（通常
`$GPTADMIN_ROOT/config/startup_instructions.md`)。该文件必须是常规文件
最多 16 KiB；不可读、空或过大的文件可以安全地回退到
内置通用指导。 `GPTADMIN_STARTUP_INSTRUCTIONS` 覆盖该文件
当它非空且最多 16 KiB 时。

忽略 `initialize.instructions` 的客户端也可以使用相同的内容
通过 MCP `resources/read` 位于 `gptadmin://startup-instructions`。启动
说明是操作指导，**不是**安全边界：已配置
权限和批准仍然控制访问和执行。

命名配置文件指令集通过经过身份验证的集线器进行管理
端点 `GET /admin/api/instruction-sets` 和
`GET|PUT|DELETE /admin/api/instruction-sets/{id}`。 `PUT` 需要 `If-Match`
（`*` 用于创建）；当访问配置文件引用时，无法删除集合
它。选定的配置文件集将在下一个 MCP `initialize` 上返回，并且
无需重新启动集线器即可读取启动资源。管理文件而不意外暴露其内容：

```bash
gptadmin instructions path
gptadmin instructions set-file /secure/path/sysadmin_startup.md
gptadmin hub restart
gptadmin instructions show  # explicitly prints the potentially sensitive content
```

`set-file` 接受最大 16 KiB 的 UTF-8 文件，使用模式 `0600` 原子安装，
并仅打印目标路径以及重新启动提示。 CLI 使用
选定的安装范围：`~/.config/gptadmin` for `--user` 安装并
`/etc/gptadmin` 用于 `--system` 安装，除非 `GPTADMIN_CONFIG_DIR` 覆盖它。

## ShellMCP 环境变量

请参阅 [ShellMCP → 环境变量](./SHELLMCP.md#environment-variables)。

## 授权模型

GPT‑Админ 有 **三种** 身份验证机制 - 它们是不同的，不要混淆。

### 1. 旧版 `CTL_TOKEN`（仅临时兼容）

- 用于：`/admin`、`/admin/api/*`、`/servers`、`/tasks/*`、工件端点
- 标头：`Authorization: Bearer <CTL_TOKEN>`（仅适用于现有的
  兼容性凭证，直到其所有者明确轮换或删除它）
- 这是“管理员”令牌。 Web 面板和自定义 GPT 操作使用它。

### 2. OAuth 承载（对于 `/mcp`）

- 用于：`/mcp`（MCP 远程 SSE）
- `/mcp` 通常使用集线器通过其签名的 OAuth 不记名令牌
  `OAUTH_CLIENT_SECRET`。已弃用的现有兼容性承载仍然存在
  仅在其所有者明确轮换或删除它之前接受。
- MCP 客户端（Claude Desktop、Codex）通过 OAuth 流获取此令牌。

### 3. `ADMIN_PASSWORD`（形式）

- 用于：OAuth 流程内 `/oauth/authorize` 处的 HTML 表单
- 这是人类授权 OAuth 客户端时键入的内容。

### 4. `SHELLMCP_TOKEN`（代理 → 中心）

- 用于：`POST /heartbeat`（代理注册）
- 每个代理都有自己的 `SHELLMCP_TOKEN` — 集线器根据心跳对其进行验证。
- 它也是经过身份验证的队列轮询的凭证。一位客户表示
  无法对集线器进行身份验证，不得自动批准：排队的工作可以
  包含敏感的命令参数和结果，因此冒充客户端
  在接收任务之前必须被拒绝。
- 就地更新保留此凭据，并且服务始终使用
  规范的 `gptadmin.env`。显式令牌轮换是一个单独的操作；
  安装新的二进制文件或重新启动服务不得轮换它。
- 新设备标识报告为 `awaiting_approval`，而不是 `offline`。
  使用 Hub MCP `pending` 工具对其进行审核并批准返回的确切一个
  `server_id` 和 `approve_pending_server`；每项均不重复审批
  轮询或在正常的二进制更新之后。

## 传统承载迁移

`CTL_TOKEN` 是已弃用的兼容性凭据，不是受支持的设置
路径。新的设置和更新永远不会创建或打印它。现有凭证
保持有效，直到其所有者明确轮换或删除它；普通客户
应使用 AdminPassword OAuth 授权流程或范围内的 MCP JWT。
ShellMCP 代理凭据是独立的，不受此规则的影响。

## OAuth

该中心实现与 OpenAI SDK OAuth 流程兼容的 OAuth 端点。

### 端点

|端点 |方法|目的|
|----------|--------|---------|
| `/oauth/authorize` |获取/发布 |授权端点（显示 `ADMIN_PASSWORD` 表单）|
| `/oauth/token` |发布 |令牌端点（客户端凭据/授权代码）|
| `/.well-known/oauth-authorization-server` |获取 | OAuth 服务器元数据 |

### 设置

1. 在集线器上设置 `OAUTH_CLIENT_SECRET`（使用 `openssl rand -hex 32` 生成）
2. 设置 `ADMIN_PASSWORD` （这是用户在授权表单中键入的内容）
3. 将 `PUBLIC_ORIGIN` 设置为您的公共中心 URL
4. MCP 客户端将通过 `/.well-known/...` 发现 OAuth 端点

### 连接生命周期Hub 发出 OAuth 协议响应，而给定的 MCP 客户端或其
连接器可以拥有持久的浏览器/会话和刷新状态。经授权的
客户端必须能够在其支持的范围内刷新或恢复其会话
重新启动边界。不要通过暴露来解决客户端刷新失败
或复制内部凭据。改变这个边界需要
授权持久性回归和实时客户端接受度定义在
[集成控制合同](./INTEGRATION_CONTROL_CONTRACT.md#authorization-durability)。
OAuth 访问 JWT 的寿命仍然短暂； Hub颁发的不透明刷新凭证
使用寿命为五个日历年，并轮换使用。新管理的 MCP 承载者
如果没有明确的 `ttl_days` 默认为五年。现有已签名的 JWT
保留其原始有效期，并且不会被安装或更新悄悄替换。

### 在哪里设置密码

在网页面板中： `/admin` → **Security** → 设置 `ADMIN_PASSWORD` 并生成
`OAUTH_CLIENT_SECRET`。或者在启动集线器时将它们设置为环境变量。

## 示例 `.env`

```bash
# Generate strong values:
# Do not generate CTL_TOKEN on new installations; configure AdminPassword/OAuth instead.
# OAUTH_CLIENT_SECRET=$(openssl rand -hex 32)

ADMIN_PASSWORD=choose-a-password
OAUTH_CLIENT_SECRET=internal-signing-secret
ADMIN_PASSWORD=choose-a-strong-password
OAUTH_CLIENT_SECRET=$(openssl rand -hex 32)
PUBLIC_ORIGIN=https://your-hub.example.com
MCP_RESOURCE=https://your-hub.example.com
```

## 另请参阅

- [Hub](./HUB.md) — 这些变量配置什么
- [Security](./SECURITY_DOCS.md) — 生产强化
- [API 参考](./API_REFERENCE.md) — 每个端点需要哪个身份验证
