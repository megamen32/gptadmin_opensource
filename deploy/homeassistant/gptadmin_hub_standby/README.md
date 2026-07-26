# GPTAdmin Hub Standby Home Assistant add-on

Local HAOS add-on source for a standby GPTAdmin Go hub and its independent
failover runtime. It runs on Home Assistant OS with `host_network: true`, keeps
the standby Hub on `:9001`, and keeps a reclaim-aware proxy on `:9101`.

This directory is safe to commit: it contains no live tokens. The generated `config.yaml` used on HAOS is created by `scripts/deploy_haos_hub_standby.sh` from `/etc/gptadmin/gptadmin.env` on the primary server.

Typical deploy from `roomhacker-server-100`:

```bash
./scripts/deploy_haos_hub_standby.sh --deploy
```

Defaults:

```text
HAOS_HOST=192.168.2.101
HAOS_SSH_PORT=2228
HAOS_SSH_USER=root
HAOS_SSH_KEY=/home/roomhacker/.ssh/id_rsa
HAOS_ADDON_DIR=/addons/gptadmin_hub_standby
```

Validation endpoints after start:

```text
http://192.168.2.101:9001/version
http://192.168.2.101:9001/healthz
```

The add-on owns the fallback-side watchdog and starts one FRP client per
configured endpoint only after the primary public health route fails its
threshold. Existing FRP credentials are passed as a Supervisor password option;
the state bundle remains secret-safe. When the primary returns, signed reclaim
demotes the fallback FRP clients.
