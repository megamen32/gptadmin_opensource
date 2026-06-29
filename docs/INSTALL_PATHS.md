# Install Paths

Where GPT‑Админ lives on each OS, in user-mode and system-mode.

## Modes

The installer auto-detects the mode:

- **user-mode** (default) — installs into the user's home dir, runs as a user
  service. No sudo/Administrator needed.
- **system-mode** — installs system-wide, runs as root/system. Use only when
  you need privileged operations (binding to port 80, managing system services
  for other users, etc.).

## Paths by OS

### Linux

| | user-mode | system-mode |
|---|-----------|-------------|
| Binary | `~/.local/share/gptadmin/` | `/opt/gptadmin/` |
| Config | `~/.config/gptadmin/` | `/etc/gptadmin/` |
| Service | `systemctl --user` | `systemctl` (systemd unit) |
| CLI | `~/.local/bin/gptadmin` | `/usr/local/bin/gptadmin` |

### macOS

| | user-mode | system-mode |
|---|-----------|-------------|
| Binary | `~/.local/share/gptadmin/` | `/opt/gptadmin/` |
| Config | `~/.config/gptadmin/` | `/etc/gptadmin/` |
| Service | LaunchAgents (`~/Library/LaunchAgents/`) | LaunchDaemons (`/Library/LaunchDaemons/`) |
| CLI | `~/.local/bin/gptadmin` | `/usr/local/bin/gptadmin` |

### Windows

| | user-mode | system-mode |
|---|-----------|-------------|
| Binary | `%LOCALAPPDATA%\gptadmin\` | `C:\Program Files\gptadmin\` |
| Config | `%LOCALAPPDATA%\gptadmin\config\` | `C:\ProgramData\gptadmin\` |
| Service | Scheduled Task (on user logon) | Windows Service (Administrator) |
| CLI | `%LOCALAPPDATA%\gptadmin\gptadmin.exe` | `C:\Program Files\gptadmin\gptadmin.exe` |

## Install commands

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

## What the installer does

1. Downloads the CLI (`gptadmin.py`) and packages
2. Runs `gptadmin setup --user` (or `--system`) — an interactive wizard
3. You pick what to install: hub + agent, hub only, or agent only
4. You pick a tunnel: auto-tunnel (FRP/Cloudflare) or your own domain
5. Writes service units and starts them
6. Prints your **Hub URL**, **CTL_TOKEN**, and **SHELLMCP_TOKEN**

## Uninstall

```bash
gptadmin uninstall
```

Removes binaries, configs, and service units. Backups created via
`file_backup` are preserved in `~/.gptadmin/file-backups/` (or
`/var/lib/gptadmin/file-backups/` in system-mode).

## See also

- [Getting Started](./GETTING_STARTED.md)
- [Configuration](./CONFIGURATION.md)
- [Hub](./HUB.md)
