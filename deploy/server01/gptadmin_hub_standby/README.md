# GPTAdmin Hub Standby Home Assistant add-on

Local HAOS add-on source for a standby GPTAdmin Go hub and its independent
failover runtime. It runs on Home Assistant OS with `host_network: true`, keeps
the standby Hub on `:9001`, and keeps a reclaim-aware proxy on `:9101`.

This directory is safe to commit: it contains no live tokens. The generated `config.yaml` used on HAOS is created by `scripts/deploy_haos_hub_standby.sh` from `/etc/gptadmin/gptadmin.env` on the primary server.

Typical deploy from `admin-node-100`:

```bash
./scripts/deploy_haos_hub_standby.sh --deploy
```

Defaults:

```text
HAOS_HOST=203.0.113.10
HAOS_SSH_PORT=2228
HAOS_SSH_USER=root
HAOS_SSH_KEY=/home/admin/.ssh/id_rsa
HAOS_ADDON_DIR=/addons/gptadmin_hub_standby
```

Validation endpoints after start:

```text
http://203.0.113.10:9001/version
http://203.0.113.10:9001/healthz
```

The add-on owns the fallback-side watchdog and starts one FRP client per
configured endpoint only after the primary public health route fails its
threshold. Existing FRP credentials are passed as a Supervisor password option;
the state bundle remains secret-safe. When the primary returns, signed reclaim
demotes the fallback FRP clients.


## Unified HAOS Node cutover

The public app image contains both the compatibility Hub binary and
`gptadmin-node`. Stage the image with `node_mode: legacy`, then stop the separate
`local_gptadmin_shellmcp` add-on, preserve its `/data/identity`, `/data/spool`
and `/data/outbox` into the Hub Standby app's `/data`, and copy the writer-issued
Node identity to `/data/config/membership/node-identity.json`. Keep all three
executor directories and the Node identity at mode 0700/0600 as appropriate.
Set `node_mode: member` after joining the writer. The add-on uses
`failover.primary_public_url` as the enrolled writer's stable direct endpoint;
keep `public_origin` and `mcp_resource` on the canonical MCP URL.

Before selecting member mode, join the name `haos` through the writer with a
single-use grant, `--identity-file`, and `--join-grant-stdin`. Pass the HAOS
LAN origin/IPv4 as `--endpoint http://<HAOS-LAN-IP>:9001` and `--connect-to
<HAOS-LAN-IP>` using the current endpoint from
`megamen32/ServersAdministartion/docs/inventory/devices/haos.md`. The identity
contains a node key and must not be printed, committed or stored in command
history. Verify membership is ready and a real `shell:haos` command works
before disabling the old ShellMCP add-on. The previous Hub Standby image remains
the rollback path.
