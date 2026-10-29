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

For planned primary maintenance, set `failover.maintenance_until` to a finite
ISO-8601 timestamp with a timezone, for example `2026-11-01T12:30:00+03:00`,
and restart the app to apply the options. Until that time the watchdog clears
its failure count and skips health checks and promotion. Signed primary reclaim
still runs first; an already active fallback Tunnel is not stopped by maintenance
alone. At expiry normal health checks resume with a fresh failure count. Leave
the option empty to disable the window. Invalid values and timestamps without a
timezone are rejected; there is no indefinite maintenance flag.

The app generates internal Hub credentials and stores them in its persistent
`/data` volume. They are not shown as Supervisor options and are not committed
to the repository.

The first production acceptance must stop only the primary Hub, verify public
`/healthz` and `/version`, then start the primary and verify automatic reclaim.


## Unified Node mode

The app supports `node_mode: legacy` (the default during staged updates) and
`node_mode: member`. Member mode runs the Hub API and the local shell executor
in one `gptadmin-node` process. It requires a writer-enrolled identity at
`/data/config/membership/node-identity.json`, plus the existing executor
identity, spool and outbox migrated into `/data/identity`, `/data/spool` and
`/data/outbox`. The add-on refuses member startup without the enrolled identity
and pinned writer information.

The member uses `failover.primary_public_url` as its enrolled writer's stable
direct node endpoint. Keep `public_origin` and `mcp_resource` on the canonical
client-facing URL; the writer seed is separate so member heartbeats cannot loop
through a failover edge that points back to this HAOS node. The existing failover proxy continues
to serve the canonical OAuth and MCP hostname through local port 9001.

## Optional quorum membership

`quorum_enabled` defaults to false. A provisioned voter may enable it only after
its authenticated cluster and private peer routes are ready. Stage the complete
node-specific quorum configuration at `/data/config/quorum/quorum.json` as a
regular file readable only by its owner, with its TLS keys, CA and encrypted
state key at the configured private paths. The app validates its unique HTTPS
peer origin and passes the configuration to the Node; quorum itself validates
the voters, certificates and storage. This does not create a cluster or allow
an independent writer: a majority and the authoritative baseline remain
required. Do not enable it against an unfenced legacy writer.

For a quorum that must remain usable when its public edge is down, configure
`internal_hub_url` on every voter as a unique private-IP HTTPS origin. The Node
opens that voter’s private listener itself, checks a client certificate against
the configured cluster CA and the enrolled peer names, and verifies the leader
certificate against its enrolled `server_name`. Certificates must chain to the
configured CA, match their local enrolled name, and permit both TLS server and
client authentication. Do not mix HTTP and HTTPS private voter routes. Allow the
selected private ports between voters, and test election, authentication and
client operations during an actual controlled primary outage before relying on
the cluster.
