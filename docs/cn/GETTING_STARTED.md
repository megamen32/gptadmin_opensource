# 开始使用

安装 GPT‑Админ，连接您的 AI，运行您的第一个命令 - 只需几分钟。

## 1. 安装集线器

在将运行集线器的计算机（您的 PC、VPS 或服务器）上：

```bash
# Linux / macOS — auto-detects user/system mode
curl -s https://became.bezrabotnyi.com/install.sh | bash
```

```powershell
# Windows (PowerShell, no Administrator needed)
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

安装程序创建集线器，在需要时启动隧道，并打印一个
**中心网址**。保留该 URL：这是您连接的地方。

> 无需域名：选择自动隧道选项（FRP 或 Cloudflare），然后您
> 获取公共 URL。请参阅[隧道](./TUNNELS_DOCS.md)。

## 2. 在目标机器上安装代理

在您要管理的每台服务器上：

```bash
curl -s https://became.bezrabotnyi.com/install.sh | bash
```

出现提示时选择“仅限代理”。代理会自动向您的集线器注册。

## 3. 连接您的人工智能

如果客户端已经使用 MCP，请注册一次 Hub：

```bash
gptadmin connect-mcp
```

对于其他客户端，请选择一个适配器：

- **Claude Desktop/其他 MCP 客户端** → [MCP 客户端设置](./ADAPTERS.md#1-mcp-client)
- **DeepSeek / Qwen / Alice / GigaChat**（免费网络聊天）→ [浏览器扩展](./ADAPTERS.md#2-browser-extension)
- **ChatGPT 自定义 GPT / 打开 WebUI** → [OpenAI Action](./ADAPTERS.md#3-openai-action)

## 4. 运行你的第一个命令

用简单的语言询问你的人工智能：

- «покажи статус nginx на server-01»
- «поставь docker на vps-prod»
- «почему openchamber отдаёт 503？ посмотри логи»
- «запусти Codex чтобы пофиксить баг в этом репо»

AI呼叫hub，hub路由到agent，agent运行命令
并返回实际输出。人工智能会读取它并返回报告。

如果您要连接自定义 GPT，请从以下位置导入生成的架构 URL：
[Adapters](./ADAPTERS.md#3-openai-action) 中的操作部分，然后选择
那里有承载者或 OAuth。切勿将内部服务机密粘贴到 GPT 中。

## 显示连接 URL

设置后，打印当前公共集线器 URL、隧道模式、MCP 端点和自定义 GPT 操作架构：

```bash
sudo gptadmin urls
```

有用的变体：

```bash
sudo gptadmin urls --all   # include every registered MCP server
sudo gptadmin urls --json  # machine-readable output
```

## 后续步骤

- [架构](./ARCHITECTURE.md) — 了解它如何组合在一起
- [Configuration](./CONFIGURATION.md) — 调整环境变量、身份验证、OAuth
- [Security](./SECURITY_DOCS.md) — 生产强化
- [Web 面板](./HUB.md#web-panel-admin) — 从浏览器管理

可选附加功能：

- [MCP 代理中继](./MCP_PROXY_RELAY.md)
- [Webhooks](./WEBHOOKS.md)

## 故障排除

**代理未出现在 `/admin`**
- 检查 `HUB_URL` 是否已设置并且可以从代理访问
- 从集线器连接页面重新运行代理连接步骤
- 查看代理日志：`journalctl --user -u shellmcp -n 50`

**`/mcp` 返回 401**
- 从 Hub URL 完成 OAuth 连接； `/mcp` 接受作用域 MCP
  连接，而不是复制的服务凭据。请参阅[配置 → OAuth](./CONFIGURATION.md#oauth)。
- 如果此连接早于集线器刷新支持，请重新连接一次以获得
  `offline_access` 刷新凭据。现有会话无法接收
  追溯性地。

**浏览器扩展按钮不出现**
- 刷新页面
- 确保 Tampermonkey/Userscripts 已启用脚本
- 在某些网站上，自动插入失败 - 提示位于剪贴板中，请手动粘贴

**自定义 GPT 操作测试失败**
- 验证 `servers.url` 中的中心 URL 与您的中心匹配
- 重新打开Hub连接页面并完成OAuth授权
  自定义 GPT 客户端
