# ShellMCP

ShellMCP is the agent that runs on each target machine. It registers with the
hub, executes commands locally, and returns real output.

## What it does

- **Registers** with the Hub via a managed device connection
- **Executes** shell commands, file operations, systemd actions
- **Returns** real stdout/stderr (the hub truncates long output to save tokens)
- **Runs** in user-mode by default (no sudo), system-mode when needed
- **Works** on Linux, macOS, Windows

## Implementations

| Impl | Status | Location | When to use |
|------|--------|----------|-------------|
| Go (`go-shellmcp/`) | **Primary (only)** | `go-shellmcp/` | New deployments — faster, single binary |

> **Примечание.** Legacy Python implementations (`client/shellmcp*.py`) удалены
> из дерева исходников. Все инсталляции теперь используют Go-бинарь `shellmcp-go`.

## Install on a target machine

```bash
# Linux / macOS (installs the Go binary in user-mode by default)
curl -s https://raw.githubusercontent.com/megamen32/gptadmin_opensource/main/deploy/install.sh | bash
```

The installer:
- Auto-detects mode: no sudo → user-mode (`~/.local/share/gptadmin`),
  with sudo → system-mode (`/opt/gptadmin`)
- Registers a user service (`systemctl --user` on Linux, `LaunchAgents` on macOS)
  - Prints the Hub URL and agent name; connection credentials stay managed by
    the installer and service

## Running manually

For a manual agent-only setup, run `gptadmin setup --no-hub --shellmcp` and
complete the Hub connection page when prompted. Do not copy a service
credential into a terminal or chat.

## Environment variables

| Var | Required | Default | Purpose |
|-----|----------|---------|---------|
| `HUB_URL` | yes | — | Hub URL to register with |
| `SHELLMCP_NAME` | no | hostname | Agent name shown in the hub |
| `SHELLMCP_LISTEN` | no | 25901 | Local listen port |
| `EXEC_TIMEOUT` | no | 120 | Max command execution time (seconds) |
| `LOG_LIMIT_B` | no | 65536 | Max inline stdout/stderr tail returned by this ShellMCP agent before the full stream is spooled to disk (bytes) |

For a bundled Hub + ShellMCP installation, the installer normalizes the internal `HUB_URL` to `http://127.0.0.1:<HUB_PORT>` and `QUEUE_URL` to the matching local `/queue`. This keeps same-host polling pinned to the primary Hub even while public ingress fails over. `HUB_PUBLIC_URL`, `PUBLIC_ORIGIN`, and `MCP_RESOURCE` remain the external identity. Agent-only installs preserve their remote `HUB_URL`.

`LOG_LIMIT_B` is per ShellMCP agent. It controls the local `/exec` result tail and does not replace hub/client response budgets; the hub may still apply different response budgets for ChatGPT Actions, Claude, or other MCP clients.

## Operations exposed

One installed ShellMCP transport is projected by the Hub as two logical MCP targets when the agent build supports the paired-file contract:

- `shell:<host>` — command execution and child-MCP management (`shell_exec`, `mcp_manage`, `mcp_tools`, `mcp_call`).
- `file:<host>` — typed filesystem operations (`system_inspect`, `file_editor`, `file_checkpoint`, legacy `file_backup`).

`file_editor` performs atomic text edits and returns fresh `N:hhhh` line IDs/context on successful edits and recoverable stale/no-match failures. `file_checkpoint` stores explicit durable restore points in a content-addressed store; restore first creates a safety checkpoint of the live state. A checkpoint is a meaningful rollback boundary, not an automatic pre-edit backup.

On a system installation the ShellMCP runtime may run privileged so `file:<host>` can edit root-owned configuration while preserving owner/group/mode. Ordinary `shell_exec` still defaults to the configured `SHELLMCP_DEFAULT_USER`; root shell execution remains explicit.

GrepMesh is provisioned as a companion capability by default when the package contains a native GrepMesh binary. Use `--no-grepmesh` (or `GPTADMIN_SETUP_GREPMESH=0` for unattended bootstrap) to opt out. Existing operator-managed GrepMesh definitions are preserved. On systemd hosts the bundled companion uses its own `gptadmin-grepmesh-mcp.service`; an operator-owned `grepmesh-mcp.service` is not overwritten or managed.

## Security

- The agent accepts only its managed device connection
- User installs run as the installing user. System installs may run the transport as root to provide the paired privileged file boundary; `shell_exec` still drops to `SHELLMCP_DEFAULT_USER` unless root is explicitly requested
- IP allowlist and command allowlist can be configured
- Secrets are masked in logs

See [Security](./SECURITY_DOCS.md).

## See also

- [Hub](./HUB.md) — what the agent talks to
- [Install Paths](./INSTALL_PATHS.md) — where it lives on each OS
- [Configuration](./CONFIGURATION.md) — full env-var reference
