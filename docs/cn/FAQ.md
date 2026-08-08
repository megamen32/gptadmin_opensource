# 常见问题解答

## GPT‑Админ 是免费的吗？

是的。核心（集线器、shellmcp、所有三个适配器、基本 Web 面板）是免费的
永远在 AGPL-3.0 下。未来的付费产品（托管云、企业 SSO、
高级面板）将是附加的——我们不会为现有功能付费。
请参阅[路线图](./ROADMAP.md)。

## 我需要付费订阅 AI 吗？

不需要。浏览器扩展程序可与**免费网络聊天**配合使用 - DeepSeek、Qwen、
Yandex Alice、Sber GigaChat，甚至免费版 ChatGPT。参见
[适配器→浏览器扩展](./ADAPTERS.md#2-browser-extension)。

## 允许 AI 访问我的服务器安全吗？

GPT‑Админ 就是为此而设计的。主要安全特性：

- **默认用户模式** — 不需要 root/sudo
- **命令白名单** — 限制 AI 可以运行的内容
- **批准模式** — 关键操作的人工确认
- **审核日志** — 每个命令都记录有调用者 + 结果
- **日志中隐藏的秘密**
- 文件编辑前的**托管备份**

请参阅[安全](./SECURITY_DOCS.md)。

## AI 会在我不知情的情况下运行命令吗？

不会。命令仅在您在聊天中询问时运行。对于关键操作
（删除、网络变更），批准模式需要人工确认。

## 这三个适配器有什么区别？

相同的中心，相同的功能——它们只是人工智能的不同方式
连接：

- **MCP 客户端** — 用于 Claude/Codex/OpenCode（原生 MCP 支持）
- **浏览器扩展** — 用于免费网络聊天（无需 API）
- **OpenAI 操作** — 用于 ChatGPT 自定义 GPT / 开放 WebUI

请参阅[适配器](./ADAPTERS.md)。

## 如何处理凭证？

您只记得 `AdminPassword`。 Hub 创建短暂的作用域
MCP 客户端的 OAuth 连接并管理代理设备凭据
服务配置。正常设置不会要求您复制内部密钥。

## `/mcp` 返回 401 — 为什么？

`/mcp` 需要限定范围的 OAuth 连接。重新打开Hub连接页面并
完成 OAuth 流程； MCP 客户端自动处理连接。对于
本地开发，Hub 可能会放宽本地主机上的身份验证。参见[配置→
OAuth](./CONFIGURATION.md#oauth)。

## 我需要自己的域名吗？

不会。安装程序通过 FRP 提供自动隧道 — 您可以在
`frp.bezrabotnyi.com` 没有 DNS 设置。对于您自己的域，请使用 Cloudflare
隧道或 nginx + Certbot。请参阅[隧道](./TUNNELS_DOCS.md)。

## 它可以在 Windows 上运行吗？

是的。该代理通过以下方式在 Windows 上运行（用户模式，无需管理员）
预定任务。安装：

```powershell
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

请参阅[安装路径](./INSTALL_PATHS.md)。

## 我可以在没有浏览器扩展/自定义 GPT 的情况下使用它吗？

是 — 将 **MCP 客户端** 适配器与 Claude Desktop、Codex 或 OpenCode 结合使用。
这些通过 MCP 远程 SSE 进行本地连接，无需浏览器。参见
[适配器 → MCP 客户端](./ADAPTERS.md#1-mcp-client)。

## 输出截断是如何工作的？

长的 stdout/stderr 被分块（默认值：1MB）。 AI看到头+尾+
“阅读更多”指针。这可以节省代币——人工智能只读取它需要的内容
来回答。请参阅 [API 参考 → 输出截断](./API_REFERENCE.md#output-truncation)。

## 我可以插入其他 MCP 吗？

是的！该集线器可以使用其他 MCP 服务器（用于网络搜索的 chrome-devtools，
openmemory 用于项目内存等）并将它们作为工具公开给每个人
连接人工智能。请参阅[架构](./ARCHITECTURE.md)。

## 如何轮换代币？

请参阅[安全→令牌轮换](./SECURITY_DOCS.md#token-rotation)。简短版本：
使用 `openssl rand -hex 32` 生成新值，更新集线器环境，重新启动，
更新客户端。

## 有东西坏了。日志在哪里？

- 集线器：`journalctl -u gptadmin_hub -n 100`（或 `--user` 对于用户模式）
- 代理：`journalctl -u shellmcp -n 100`（或 `--user`）
- 或在网络面板中阅读它们：`/admin` → 日志

## 如何卸载？

```bash
gptadmin uninstall
```

删除二进制文件、配置和服务单元。文件备份被保留。

## 还是卡住了吗？

- [打开 GitHub 问题](https://github.com/megamen32/gptadmin/issues)
- 电报：[@careviolan](https://t.me/careviolan)
- 网站：https://gptadmin.bezrabotnyi.com
