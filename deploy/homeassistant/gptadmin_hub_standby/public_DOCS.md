# GPTAdmin Hub Standby

This app keeps a second GPTAdmin Hub on Home Assistant OS and promotes its
Tunnel only after the configured primary health endpoint fails the threshold.
When the primary returns, the signed reclaim path stops the fallback Tunnel.

## Setup

Configure the app before starting it:

- `public_origin`: public URL served through the Tunnel;
- `admin_password`: the only user-owned Hub password;
- `failover_frp_token`: the existing Tunnel credential;
- `failover.primary_health_url`: primary Hub `/healthz` URL;
- `failover.primary_public_url`: public Hub URL;
- `failover.endpoints`: FRP endpoint host/port list;
- `failover.subdomain` and `failover.domain`: the public Tunnel route.

The app generates internal Hub credentials and stores them in its persistent
`/data` volume. They are not shown as Supervisor options and are not committed
to the repository.

The first production acceptance must stop only the primary Hub, verify public
`/healthz` and `/version`, then start the primary and verify automatic reclaim.
