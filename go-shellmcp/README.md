# go-shellmcp

Primary GPTAdmin ShellMCP runtime and standalone MCP host.

The old Python runtime is deprecated. New deployments use the Go
`shellmcp` binary installed by GPTAdmin.

Implemented:

- `/version`
- `/system/info`
- `/system/health`
- `/exec`
- `/exec/live` NDJSON streaming
- background jobs via `{"background": true}` + `GET /jobs/<job_id>`
- stdout/stderr spooled to disk with bounded tail in RAM
- timeout + process-group kill on Linux/macOS
- token auth compatibility bootstrap
- optional signed long-poll queue runner
- durable queue result outbox under `SHELL_OUTBOX_DIR`
- `SHELL_MODE=long_poll|webhook` transport selection
- optional signed heartbeat to GPTAdmin hub
- `/file?path=...` for authenticated spool file retrieval

Compatibility notes:

- Python `client/shellmcp.py` / `client/shellmcp.py` is deprecated.
- Keep the `shellmcp.service` service name for compatibility; current production overrides it to execute the Go binary.

Run locally:

```bash
cd go-shellmcp
SHELL_PORT=25990 SHELL_TOKEN=test go run ./cmd/shellmcp-go
curl -H 'Authorization: Bearer test' http://127.0.0.1:25990/system/health
curl -H 'Authorization: Bearer test' -H 'Content-Type: application/json' \
  -d '{"cmd":"printf hello"}' http://127.0.0.1:25990/exec

curl -H 'Authorization: Bearer test' -H 'Content-Type: application/json' \
  -d '{"cmd":"echo live"}' http://127.0.0.1:25990/exec/live
```

## Tests

```bash
go test ./...
./scripts/cross-build.sh
go test -tags=stress ./internal/server -run TestStressExecHTTP -count=1
REQUESTS=120 WORKERS=20 ./scripts/stress-local.sh
```

`cross-build.sh` verifies Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 builds.
`stress-local.sh` starts a local shellmcp-go process, runs concurrent `/exec` requests, checks background jobs, and verifies large stdout spill files.

## Default execution user

When `SHELL_DEFAULT_USER` is set and a request does not explicitly set `run_as_user`/`user`, shellmcp-go runs commands that do not mention `sudo` as that user:

```bash
SHELL_DEFAULT_USER=roomhacker
SHELL_DEFAULT_HOME=/home/roomhacker
SHELL_DEFAULT_CWD=/home/roomhacker
```

Commands containing a `sudo` token stay in the service/root context, so privileged operations can still be requested explicitly with `sudo ...`.

When ShellMCP itself runs as root, `SHELL_DEFAULT_USER` (or `SHELLMCP_DEFAULT_USER`) is required for ordinary commands. Without it, the command is rejected rather than silently running as root. Use `run_as_user: "root"` or an explicit `sudo ...` command only for intentional privileged operations.

## Output size and client budgets

`LOG_LIMIT_B` is a per-agent ShellMCP setting. It controls how much stdout/stderr tail is returned inline from `/exec`; full larger output is still written to the spool file and returned through `stdout_path`/`stderr_path`. The default is `65536` bytes.

This is separate from GPTAdmin hub client budgets. The hub can apply different response budgets for ChatGPT Actions, Claude, or other MCP clients, so increasing one ShellMCP agent's `LOG_LIMIT_B` does not globally change every client or every agent.


## Standalone MCP host

ShellMCP is itself a standard MCP server. Hub polling is optional: Codex,
Claude, or another MCP client can launch the binary directly over stdio.

Example client configuration:

```json
{
  "mcpServers": {
    "shellmcp": {
      "command": "/opt/gptadmin/bin/shellmcp",
      "args": ["--mcp-stdio"],
      "env": {
        "SHELLMCP_MCP_CONFIG": "/etc/gptadmin/shellmcp-mcp.json",
        "SHELLMCP_SPOOL_DIR": "/var/lib/gptadmin/shellmcp-spool"
      }
    }
  }
}
```

For a user-scope install, the binary is normally under
`~/.local/share/gptadmin/bin/shellmcp`. On macOS and Linux, run the normal
GPTAdmin setup/update flow first; it installs the native Go binary and applies
the platform service definition. Direct MCP clients do not need Hub, an
inbound port, heartbeat, or a service: launch the binary with `--mcp-stdio` as
shown above.

The child-MCP tools are:

- `mcp_manage`: list, upsert, remove, enable, disable, restart, status, config;
- `mcp_tools`: perform `tools/list` on one enabled child MCP;
- `mcp_call`: perform `tools/call` on one enabled child MCP.

An omitted `enabled` field means `true`. Registry writes are atomic and use
mode `0600`.

### Local stdio MCP

Install the package or executable with `shell_exec`, then persist its runtime
configuration with `mcp_manage`. Keeping installation and configuration as two
explicit operations lets the AI use the host package manager appropriate for
Linux, macOS, Home Assistant, npm, pipx, uv, or a standalone binary without
hard-coding an installer into ShellMCP.

Example `mcp_manage` arguments:

```json
{
  "action": "upsert",
  "config": {
    "ref": "filesystem",
    "transport": "stdio",
    "command": "/usr/local/bin/filesystem-mcp",
    "args": ["/srv/shared"],
    "env": {
      "API_TOKEN": "${FILESYSTEM_MCP_TOKEN}"
    },
    "enabled": true
  }
}
```

Stdio sessions are persistent and serialized per `ref`. Updating, disabling,
removing, or restarting a definition closes the actual protocol session.
Enable only changes persistent desired state; the process starts lazily on the
first discovery or call.

### Remote Streamable HTTP MCP

```json
{
  "action": "upsert",
  "config": {
    "ref": "remote-docs",
    "transport": "streamable-http",
    "url": "https://mcp.example.net/mcp",
    "headers": {
      "Authorization": "Bearer ${REMOTE_MCP_TOKEN}"
    },
    "enabled": true
  }
}
```

Use `"transport": "sse"` for legacy SSE endpoints. Header and environment
values are expanded from the ShellMCP process environment at request time, so
secrets do not need to be written into the registry.

Streamable HTTP sessions reuse the negotiated protocol version and session ID,
recover once from a server-side session reset, and send DELETE when closed.
Legacy SSE uses its long-lived GET event stream and the endpoint it advertises
for POST requests.

### Resource bounds

ShellMCP-owned spool, outbox, audit, and managed-backup files share one
oldest-first limit of `min(500 MiB, 5% of filesystem capacity)`. Exact-file
roots prevent ShellMCP from pruning unrelated sibling logs. Child stderr kept
in memory is capped at 64 KiB.

### Hub polling

When `SHELLMCP_QUEUE=1`, ShellMCP opens no inbound listener. It polls Hub and
accepts the same `shell_exec`, `mcp_manage`, `mcp_tools`, and `mcp_call`
operations through the outbound queue. Results use the durable outbox; Hub 404
responses for expired jobs are treated as terminal and removed.

Heartbeat is disabled by default and is independent of long-poll activity.
Enable it explicitly only when the extra diagnostic signal is useful.

Important variables:

```text
SHELLMCP_MCP_CONFIG=/etc/gptadmin/shellmcp-mcp.json
SHELLMCP_SPOOL_DIR=/var/lib/gptadmin/shellmcp-spool
SHELLMCP_OUTBOX_DIR=/var/lib/gptadmin/shellmcp-spool/outbox
SHELLMCP_AUDIT_LOG=/var/log/gptadmin/shellmcp-audit.jsonl
SHELLMCP_QUEUE=1
HUB_URL=https://gptadmin.example.net
```
