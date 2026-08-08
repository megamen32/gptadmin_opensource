# 安全

GPT‑Админ 允许 AI 代理访问您的服务器。安全是重中之重。

## 认证模型（总结）

GPT‑Админ 具有三种身份验证机制 - 请参阅[配置 → 身份验证模型](./CONFIGURATION.md#auth-model)
详情：

1. **`CTL_TOKEN`**（Bearer，仅限旧版迁移）- 现有管理 API + Web 面板安装；请勿将其用于新的自定义 GPT 设置。
2. **OAuth 承载** — `/mcp` 端点（对于 MCP 客户端）和自定义 GPT 中继架构流
3. **`ADMIN_PASSWORD`** — OAuth 流程中的 `/oauth/authorize` 表单

加上 `SHELLMCP_TOKEN` 用于代理 → 中心注册。

## 最小权限

- **默认用户模式** — 代理以安装用户而非 root 身份运行。
  仅当您需要特权操作时，系统模式 (sudo) 是可选的。
- **命令允许列表** — 限制代理将执行的命令
  （在 `~/.config/gptadmin/allowlist.txt` 中配置）。
- **IP 白名单** — 限制哪些 IP 可以到达代理。

## 秘密处理

- 秘密**隐藏在日志中** - 令牌、密码、API 密钥均经过编辑
  在记录之前。
- **“仅限本地”模式** — 对于包含敏感数据的命令，代理可以是
  配置为不将输出返回到集线器（本地运行，仅报告状态）。
- **托管备份** — 在编辑文件之前，`file_backup` 创建备份
  带TTL。关键文件（nginx、systemd、网络）获得更长的 TTL
  默认。

### 管理硕士

锁定管理接受注册的 WebAuthn/密钥凭证或
TOTP 后备。 WebAuthn 仪式公开于
`/admin/api/security/mfa/webauthn/register/{begin,finish}` 和
`/admin/api/security/mfa/webauthn/login/{begin,finish}`； the Hub 公开商店
`webauthn_state.json` 中模式为 `0600` 的凭证记录并使用
验证登录后的短暂 HttpOnly 证明 cookie。 OIDC 身份识别
代理集成仍然是特定于部署的，并不隐含在本地
密钥注册。

### 远程秘密入口

远程 MCP 客户端使用 `secret_request` 创建一次性浏览器条目
flow 和 `secret_status` 仅读取元数据。绝对不能发送值
MCP JSON、日志、审计记录、作业检查或公共响应。一个托管的
`shell_exec` 可能会收到不透明的 `secret_env` 映射；集线器解决它
服务器端并在每个响应边界再次编辑该值。

安全默认值是：

|变量|默认|合同|
|----------|---------|----------|
| `GPTADMIN_SECRET_STORE_DIR` | `$GPTADMIN_CONFIG_DIR/secrets` |目录模式 `0700`;仅包含加密记录 |
| `GPTADMIN_SECRET_STORE_KEY_FILE` | `$GPTADMIN_CONFIG_DIR/secret-store.key` | AES-256 密钥模式 `0600`;丢失/无效密钥无法关闭 |
| `GPTADMIN_SECRET_INGRESS_STATE_FILE` | `$GPTADMIN_CONFIG_DIR/secrets/requests.json` |请求元数据模式`0600`；仅令牌哈希 |
| `GPTADMIN_SECRET_INGRESS_TTL` | `900` 秒 |限制在 60–3600 秒；请求是一次性的 |

使用现有的受保护的密钥和加密存储一起备份
备份程序。如果密钥丢失或无效，请从受保护的密钥中恢复它
备份或重新创建请求； GPTAdmin 永远不会退回到纯文本文件
或环境变量。仅在计划迁移时轮换密钥
在删除旧密钥之前重新加密记录。

### OTLP 遥测导出

OTLP 导出可通过 `GPTADMIN_OTLP_ENDPOINT` 选择加入。外部端点
必须使用 HTTPS；仅允许环回开发收集器使用 HTTP。的
Hub 仅导出列入许可名单的结构化事件信封和有界的
相关字段。参数、命令、凭据、URL、文件内容和
原始有效负载被排除在外。队列是有界的，导出错误是
原始控制平面请求的故障开放。

## 批准模式

对于关键操作（删除文件、更改网络配置），集线器
支持**批准模式**：人工智能提出行动，中心要求
执行前人工确认。在 `/admin` → 安全性中启用每个代理。

## 代币轮换

```bash
# Legacy `CTL_TOKEN` rotation (migration installs only)
openssl rand -hex 32

# Update the hub env, restart
sudo systemctl restart gptadmin_hub  # or: systemctl --user restart gptadmin_hub

# Update each agent's HUB_URL/TOKEN if you changed SHELLMCP_TOKEN
# Update Custom GPT / MCP client configs with the new OAuth bearer or scoped managed MCP token; do not onboard new clients with CTL_TOKEN
```如果令牌泄漏，请立即轮换。回购协议的历史清理
 是一项一次性措施——
旋转以确保安全。


## MCP 服务器的网关模式

当 GPTAdmin 用作安全代理/中继时，外部客户端应连接到 GPTAdmin，而不是直接连接到专用 stdio 或仅限 LAN 的 MCP 服务器。当客户端仅需要一项功能时，首选每服务器 URL：

```text
/server/{slug}/mcp
/server/{slug}/actions/openapi.yaml
```

这使上游 MCP 服务器保持私有，而 GPTAdmin 应用 HTTPS、承载/OAuth 身份验证、审核日志记录、路由和队列处理。仅对需要跨服务器中继/管理功能的可信客户端使用完整的 `/server/hub/mcp` 表面。

## 生产强化清单

- [ ] 新的自定义 GPT 设置使用 OAuth 承载或范围内托管的 MCP 令牌；没有创建新的 `CTL_TOKEN`
- [ ] `OAUTH_CLIENT_SECRET` 已设置（对于 `/mcp`）
- [ ] `ADMIN_PASSWORD` 很强
- [ ] Hub 位于 HTTPS 后面（通过 Cloudflare/FRP 隧道或 nginx + Certbot）
- [ ] 代理 IP 允许列表已设置（只有集线器可以到达代理）
- [ ] 防火墙：仅集线器端口（25900）是公共的；代理端口 (25901) 是内部端口
- [ ] 为关键操作启用批准模式
- [ ] 日志轮换（`logrotate` 或 `journalctl --vacuum-time`）
- [ ] 备份已配置（`file_backup` TTL）

## 报告漏洞

请参阅 [SECURITY.md](../SECURITY.md)（存储库根目录）。简短版本：

- **不要打开公共 GitHub 问题。**
- 通过电报报告：[@careviolan](https://t.me/careviolan)
- 48 小时内确认，关键问题在 30 天内修复目标。

## 审核日志

每个执行的命令都会记录：时间戳、代理、命令、调用者
（哪个AI/适配器），退出代码。可在 `/admin` → 日志中查看。导出到
`/admin/api/logs/export` 用于 SIEM 摄取。

## 另请参阅

- [Configuration](./CONFIGURATION.md) — 如何设置身份验证变量
- [Hub](./HUB.md) — 端点
- [SECURITY.md](../SECURITY.md) — 负责任的披露
