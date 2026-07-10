# GPTAdmin ShellMCP Home Assistant add-on

Local HAOS add-on source for a GPTAdmin ShellMCP server/agent. It runs with `host_network: true`, listens on `:25900`, registers as `haos`, and exposes direct MCP endpoints.

This directory is safe to commit: it contains no live tokens. The generated `config.yaml` used on HAOS is created by `scripts/deploy_haos_shellmcp.sh` from `/etc/gptadmin/gptadmin.env` on the primary server.

Typical deploy from `roomhacker-server-100`:

```bash
./scripts/deploy_haos_shellmcp.sh --deploy
```

Defaults:

```text
HAOS_HOST=192.168.2.101
HAOS_SSH_PORT=2228
HAOS_SSH_USER=root
HAOS_SSH_KEY=/home/roomhacker/.ssh/id_rsa
HAOS_ADDON_DIR=/addons/gptadmin_shellmcp
```

Validation endpoints after start:

```text
http://192.168.2.101:25900/version
http://192.168.2.101:25900/mcp
```
