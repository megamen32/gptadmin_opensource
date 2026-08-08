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
curl -s https://became.bezrabotnyi.com/install.sh | bash
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

`LOG_LIMIT_B` 是每个 ShellMCP 代理。它控制本地 `/exec` 结果尾部，并且不会替换集线器/客户端响应预算；中心仍可能对 ChatGPT Actions、Claude 或其他 MCP 客户端应用不同的响应预算。

## 暴露的操作

集线器将这些代理给代理。适用于所有 3 个适配器：

|运营|示例|
|------------|---------|
| `shell_exec` |运行 shell 命令，返回 stdout/stderr |
| `file_read` |读取文件 |
| `file_write` |写一个文件（带备份）|
| `file_backup` |编辑前创建托管备份 |
| `systemd_*` |状态/启动/停止/重新启动/启用单元|
| `system_info` | CPU、RAM、磁盘、正常运行时间 |
| `system_health` |快速健康检查|
| `venv_*` |管理 Python virtualenvs |
| `dir` |列表目录|

请参阅 [API 参考](./API_REFERENCE.md) 了解确切的架构。

## 安全

- 代理仅接受其托管设备连接
- 默认情况下以安装用户（而不是 root）身份运行 — 使用 sudo 的系统模式
  是选择加入
- 可以配置IP白名单和命令白名单
- 秘密被隐藏在日志中

请参阅[安全性](./SECURITY_DOCS.md)。

## 另请参阅

- [Hub](./HUB.md) — 代理对话的内容
- [安装路径](./INSTALL_PATHS.md) — 它在每个操作系统上的位置
- [Configuration](./CONFIGURATION.md) — 完整的环境变量参考
