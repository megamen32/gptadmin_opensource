# 路线图

已构建什么、即将发生什么以及开放核心分裂。

## 今天的状态

### ✅ 已建成并正在运行

- **Hub** (`go-hub/`) — MCP 远程 SSE、管理 API、OAuth、Web 面板
- **ShellMCP** (Go + Python) — 适用于 Linux/macOS/Windows 的代理
- **三个适配器**：
  - MCP 客户端（Claude Desktop、Codex、OpenCode）
  - 浏览器扩展（免费网络人工智能的用户脚本）
  - OpenAI Action（自定义 GPT、开放 WebUI）
- **CLI** (`gptadmin`) — 设置、隧道、状态、日志、配置
- **隧道** — FRP + Cloudflare 自动隧道
- **Web 面板** (`/admin`) — 队列、代理运行状况、日志
- OpenAI SDK 的 **OAuth**
- **安装脚本**适用于Linux/macOS/Windows（用户+系统模式）
- **后台任务**带轮询
- **输出截断**（保存令牌）
- **带有 TTL 的托管文件备份**

### 🚧 即将推出

- **高级 Web 面板** — 团队、RBAC、警报、审核日志导出
- **托管云** — 不想自行托管？我们将为您举办
- **企业 SSO** — SAML、OIDC、SCIM 配置
- **MCP 市场** — 浏览并安装 MCP（openmemory、chrome-devtools、
  自定义）从面板
- **更多适配器** — Slack、Discord、Telegram 机器人作为一流适配器

## 开放核心模型

GPT‑Админ 在 AGPL-3.0 下是**开放核心**。

### 永久免费（此存储库）

- 集线器、shellmcp、所有三个适配器
- 基本网页面板（队列、运行状况、日志）
- 所有 CLI 命令
- 所有隧道
- 社区支持（GitHub 问题、讨论）

### 将支付（单独的存储库/云）

- 托管云（托管中心，无自托管）
- 企业单点登录 (SAML/OIDC) + SCIM
- 高级 RBAC（角色、每个代理权限）
- 扩展审核日志+SIEM导出
- SLA + 优先支持
- 高级面板（团队仪表板、警报、分析）

核心保持开放。付费功能是附加的——永远不会存在付费墙
功能。

## 版本控制

我们遵循 [SemVer](https://semver.org/)。请参阅 [CHANGELOG.md](../CHANGELOG.md)
用于发布历史记录。

- `0.x` — 1.0 之前的版本，次要版本之间可能存在重大更改
- `1.0` — 第一个稳定版本（Web 面板发布后）
- `1.x+` — 向后兼容的添加

## 贡献

欢迎 PR — 请参阅 [CONTRIBUTING.md](../CONTRIBUTING.md)。需要关爱的地方：

- 更多的测试覆盖范围（隧道、MCP-SSE、CLI）
- 文档改进
- 新的 MCP 集成（编写一个包含您最喜欢的工具的 MCP）
- 包装（自制配方、AUR 包装、Scoop 清单）

## 另请参阅

- [Architecture](./ARCHITECTURE.md) — 它是如何构建的
- [CHANGELOG.md](../CHANGELOG.md) — 发生了什么变化
