# GPTAdmin — MiniMax Code plugin

Connects MiniMax Code to the OAuth-protected GPTAdmin MCP hub: one remote entry
point for administering servers, running commands through ShellMCP targets, and
installing child MCP servers.

## What it contains

- `servers.mcp.json` — the remote GPTAdmin hub over `streamable-http`
- `skills/gptadmin-connect` — OAuth connection and reconnection
- `skills/gptadmin-mcp-install` — target discovery, schema-first execution,
  child-MCP installation
- `skills/gptadmin-workflow` — profile routing, memory workflow, ShellMCP
  enrollment on a new host

## Install

Point your client at the public repository and install the `gptadmin` plugin
directory, or copy this directory into `~/.minimax/plugins/gptadmin/`.

The hub URL lives in exactly one place, `servers.mcp.json`. To use a different
Hub, change only that `url`.

## Authorization

The hub is OAuth-protected. On first use the client follows OAuth discovery and
opens the browser consent page; the admin password is entered there and nowhere
else. Never put the password, an authorization code, an access or refresh
token, or a signing secret into chat, `.mcp.json`, a skill, or a task file.

Requested scopes: `gptadmin.read gptadmin.exec offline_access`.

## Hub and ShellMCP are different things

- **Hub** — the single OAuth front door, policy and approval point. You need
  exactly one reachable over HTTPS.
- **ShellMCP** — the per-host agent that exposes shell and child-MCP tools to
  the Hub. You need one on every host you want to control.

A host only becomes usable after its ShellMCP is enrolled in `discover` and
approved through `approve_pending_server`.

## Issuer stability

If the hub advertises an OAuth issuer on a `t.gptadmin` FRP tunnel host, saved
tokens break whenever that tunnel restarts or its URL changes. The stable
public hostname is `https://mcp.bezrabotnyi.com`. The hub should advertise that
hostname as its issuer via `HUB_PUBLIC_URL` / `PublicOrigin`; the tunnel is
transport only.

## Verify before trusting it

After authorizing, call `tools/list`, then `discover`, then one read-only
command on an `online` target. OAuth success is not business acceptance.

## License

AGPL-3.0