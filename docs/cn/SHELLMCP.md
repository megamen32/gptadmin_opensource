# ShellMCP

ShellMCP 是在每台目标计算机上运行的代理。它注册到
hub，在本地执行命令，并返回真实的输出。

## 它的作用

- **通过托管设备连接向集线器注册**
- **执行** shell 命令、文件操作、systemd 操作
- **返回**真实的标准输出/标准错误（集线器截断长输出以保存令牌）
- **默认在用户模式下运行**（无 sudo），需要时在系统模式下运行
- **适用于** Linux、macOS、Windows

## 实施

|实施 |状态 |地点 |何时使用 |
|------|--------|----------|------------|
|去 (`go-shellmcp/`) | **主要（仅）** | `go-shellmcp/` |新部署 - 更快、单一二进制文件 |

> **Примечание.** 旧版 Python 实现 (`client/shellmcp*.py`) удалены
> из дерева исходников。请参阅 Go-бинарь `shellmcp-go`。

## 在目标机器上安装

```bash
# Linux / macOS (installs the Go binary in user-mode by default)
curl -s https://raw.githubusercontent.com/megamen32/gptadmin_opensource/main/deploy/install.sh | bash
```

安装程序：
- 自动检测模式：无 sudo → 用户模式 (`~/.local/share/gptadmin`)，
  使用 sudo → 系统模式 (`/opt/gptadmin`)
- 注册用户服务（Linux 上为 `systemctl --user`，macOS 上为 `LaunchAgents`）
  - 打印 Hub URL 和代理名称；连接凭据由以下人员管理
    安装人员和服务

## 手动运行

对于仅手动代理设置，请运行 `gptadmin setup --no-hub --shellmcp` 并
出现提示时完成集线器连接页面。不要复制服务
凭证进入终端或聊天。

## 环境变量

|瓦尔 |必填|默认|目的|
|-----|----------|---------|---------|
| `HUB_URL` |是的 | — |用于注册的集线器 URL |
| `SHELLMCP_NAME` |没有|主机名 |中心显示的代理名称 |
| `SHELLMCP_LISTEN` |没有| 25901 | 25901本地监听端口|
| `EXEC_TIMEOUT` |没有| 120 | 120最大命令执行时间（秒）|
| `LOG_LIMIT_B` |没有| 65536 |在将完整流假脱机到磁盘之前此 ShellMCP 代理返回的最大内联 stdout/stderr 尾部（字节） |

对于同机安装的 Hub + ShellMCP，installer 会把内部 `HUB_URL` 规范化为 `http://127.0.0.1:<HUB_PORT>`，并把 `QUEUE_URL` 设为对应的本地 `/queue`。因此即使公共入口发生 failover，同机 polling 仍固定连接 primary Hub。`HUB_PUBLIC_URL`、`PUBLIC_ORIGIN` 和 `MCP_RESOURCE` 继续表示外部 identity。仅安装 ShellMCP 时会保留远程 `HUB_URL`。

`LOG_LIMIT_B` 是每个 ShellMCP 代理。它控制本地 `/exec` 结果尾部，并且不会替换集线器/客户端响应预算；中心仍可能对 ChatGPT Actions、Claude 或其他 MCP 客户端应用不同的响应预算。

## 暴露的操作

当代理版本支持配对文件协议时，一次 ShellMCP 安装会由 Hub 暴露为两个逻辑 MCP target：

- `shell:<host>` — 命令执行和子 MCP 管理（`shell_exec`, `mcp_manage`, `mcp_tools`, `mcp_call`）。
- `file:<host>` — 文件操作（`system_inspect`, `file_editor`, `file_checkpoint`，以及兼容旧客户端的 `file_backup`）。

`file_editor` 支持 `view/create/str_replace/batch_edit/delete`，原子修改文本并返回新的 `N:hhhh` line-id。`file_checkpoint` 使用 CAS 保存显式恢复点；`restore` 会先自动创建 safety checkpoint。系统安装中，文件 target 可通过特权 runtime 操作 root-owned 文件，而普通 `shell_exec` 仍默认以 `SHELLMCP_DEFAULT_USER` 运行。

GrepMesh 默认作为 companion capability 安装；使用 `--no-grepmesh` 可退出内置配置。在 systemd 主机上，内置 companion 使用独立的 `gptadmin-grepmesh-mcp.service`；运营者自己的 `grepmesh-mcp.service` 不会被 GPTAdmin 覆盖或管理。

## 安全

- 代理仅接受其托管设备连接
- 在 system-install 中，transport 可以以 root 运行，从而为 `file:<host>` 提供特权文件边界；普通 `shell_exec` 仍以 `SHELLMCP_DEFAULT_USER` 执行，除非显式请求 root
- 可以配置IP白名单和命令白名单
- 秘密被隐藏在日志中

请参阅[安全性](./SECURITY_DOCS.md)。

## 另请参阅

- [Hub](./HUB.md) — 代理对话的内容
- [安装路径](./INSTALL_PATHS.md) — 它在每个操作系统上的位置
- [Configuration](./CONFIGURATION.md) — 完整的环境变量参考
