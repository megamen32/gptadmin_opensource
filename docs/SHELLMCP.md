# ShellMCP

ShellMCP is the agent that runs on each target machine. It registers with the
hub, executes commands locally, and returns real output.

## What it does

- **Registers** with the hub via `POST /heartbeat` (sends URL + token + hostname)
- **Executes** shell commands, file operations, systemd actions
- **Returns** real stdout/stderr (the hub truncates long output to save tokens)
- **Runs** in user-mode by default (no sudo), system-mode when needed
- **Works** on Linux, macOS, Windows

## Implementations

| Impl | Status | Location | When to use |
|------|--------|----------|-------------|
| Go (`go-shellmcp/`) | **Primary** | `go-shellmcp/` | New deployments — faster, single binary |
| Python (`client/shellmcp.py`) | Legacy | `client/` | Compatibility with older setups |
| Python pure (`client/shellmcp_pure.py`) | Minimal | `client/` | No external deps, any Unix |

## Install on a target machine

```bash
# Linux / macOS (installs the Go binary in user-mode by default)
curl -s https://became.bezrabotnyi.com/install.sh | bash
```

The installer:
- Auto-detects mode: no sudo → user-mode (`~/.local/share/gptadmin`),
  with sudo → system-mode (`/opt/gptadmin`)
- Registers a user service (`systemctl --user` on Linux, `LaunchAgents` on macOS)
- Prints the agent URL + `SHELLMCP_TOKEN`

## Running manually

```bash
# Register with a hub
SHELLMCP_TOKEN=agent-secret \
HUB_URL=http://your-hub:25900 \
python client/shellmcp.py
```

Or the Go binary:

```bash
SHELLMCP_TOKEN=agent-secret \
HUB_URL=http://your-hub:25900 \
./go-shellmcp/shellmcp
```

## Environment variables

| Var | Required | Default | Purpose |
|-----|----------|---------|---------|
| `SHELLMCP_TOKEN` | yes | — | Bearer token (must match what the hub expects) |
| `HUB_URL` | yes | — | Hub URL to register with |
| `SHELLMCP_NAME` | no | hostname | Agent name shown in the hub |
| `SHELLMCP_LISTEN` | no | 25901 | Local listen port |
| `EXEC_TIMEOUT` | no | 120 | Max command execution time (seconds) |
| `LOG_LIMIT_B` | no | 1048576 | Max output size before truncation (bytes) |

## Operations exposed

The hub proxies these to the agent. Available to all 3 adapters:

| Operation | Example |
|-----------|---------|
| `shell_exec` | run a shell command, return stdout/stderr |
| `file_read` | read a file |
| `file_write` | write a file (with backup) |
| `file_backup` | create a managed backup before edits |
| `systemd_*` | status / start / stop / restart / enable units |
| `system_info` | CPU, RAM, disk, uptime |
| `system_health` | quick health check |
| `venv_*` | manage Python virtualenvs |
| `dir` | list directory |

See [API Reference](./API_REFERENCE.md) for the exact schema.

## Security

- The agent only accepts requests bearing its `SHELLMCP_TOKEN`
- By default runs as the installing user (not root) — system-mode with sudo
  is opt-in
- IP allowlist and command allowlist can be configured
- Secrets are masked in logs

See [Security](./SECURITY_DOCS.md).

## See also

- [Hub](./HUB.md) — what the agent talks to
- [Install Paths](./INSTALL_PATHS.md) — where it lives on each OS
- [Configuration](./CONFIGURATION.md) — full env-var reference
