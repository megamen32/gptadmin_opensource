# GPTAdmin Hub Standby Home Assistant add-on

Local HAOS add-on source for a standby GPTAdmin Go hub. It is intended to run on Home Assistant OS with `host_network: true` and listen on `:9001`.

This directory is safe to commit: it contains no live tokens. The generated `config.yaml` used on HAOS is created by `scripts/deploy_haos_hub_standby.sh` from `/etc/gptadmin/gptadmin.env` on the primary server.

Typical deploy from `admin-server-100`:

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

Public failover is separate: this add-on provides the LAN standby hub, but does not itself switch FRP/DNS from the primary hub.
