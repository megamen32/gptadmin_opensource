# 安装路径

GPT‑Админ 存在于每个操作系统的用户模式和系统模式中。

## 模式

安装程序自动检测模式：

- **用户模式**（默认）- 安装到用户的主目录中，以用户身份运行
  服务。无需 sudo/管理员。
- **系统模式** — 在系统范围内安装，以 root/系统身份运行。仅在以下情况下使用
  您需要特权操作（绑定到端口80、管理系统服务
  对于其他用户等）。

## 按操作系统的路径

### Linux

| |用户模式 ​​|系统模式 |
|---|-----------|-------------|
|二进制 | `~/.local/share/gptadmin/` | `/opt/gptadmin/` |
|配置 | `~/.config/gptadmin/` | `/etc/gptadmin/` |
|服务 | `systemctl --user` | `systemctl`（系统单元）|
|命令行| `~/.local/bin/gptadmin` | `/usr/local/bin/gptadmin` |

### macOS

| |用户模式 ​​|系统模式 |
|---|-----------|-------------|
|二进制 | `~/.local/share/gptadmin/` | `/opt/gptadmin/` |
|配置 | `~/.config/gptadmin/` | `/etc/gptadmin/` |
|服务 |启动代理 (`~/Library/LaunchAgents/`) | LaunchDaemons (`/Library/LaunchDaemons/`) |
|命令行| `~/.local/bin/gptadmin` | `/usr/local/bin/gptadmin` |

### 窗口

| |用户模式 ​​|系统模式 |
|---|-----------|-------------|
|二进制 | `%LOCALAPPDATA%\gptadmin\` | `C:\Program Files\gptadmin\` |
|配置 | `%LOCALAPPDATA%\gptadmin\config\` | `C:\ProgramData\gptadmin\` |
|服务 |计划任务（用户登录时）| Windows 服务（管理员）|
|命令行| `%LOCALAPPDATA%\gptadmin\gptadmin.exe` | `C:\Program Files\gptadmin\gptadmin.exe` |

## 安装命令

```bash
# Linux / macOS — user-mode (default)
curl -s https://became.bezrabotnyi.com/install.sh | bash

# Linux / macOS — system-mode (when you need root)
curl -s https://became.bezrabotnyi.com/install.sh | sudo bash
```

```powershell
# Windows — user-mode (no Administrator)
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

## 安装程序的作用

1. 下载 CLI (`gptadmin.py`) 和软件包
2. 运行 `gptadmin setup --user` （或 `--system`） — 交互式向导
3. 您选择要安装的内容：集线器 + 代理、仅集线器或仅代理
4. 您选择隧道：自动隧道 (FRP/Cloudflare) 或您自己的域
5. 编写服务单元并启动它们
6. 打印您的 **Hub URL** 和 `/connect` 加入 URL。内部代理
   凭据存储在服务器端，并且永远不会打印以进行复制/粘贴。

## 卸载

```bash
gptadmin uninstall
```

删除二进制文件、配置和服务单元。通过创建的备份
`file_backup` 保留在 `~/.gptadmin/file-backups/` 中（或
`/var/lib/gptadmin/file-backups/` 在系统模式下）。

## 另请参阅

- [入门](./GETTING_STARTED.md)
- [配置](./CONFIGURATION.md)
- [集线器](./HUB.md)
