# One public origin, independent ingress paths

Keep one HTTPS origin in `PUBLIC_ORIGIN`, for example `https://mesh.example.net`. Clients keep using `https://mesh.example.net/mcp`. Provide two independently reachable paths to a working GPTAdmin Node with the appropriate authorization. Two DNS records pointing through the same router, reverse proxy or connector still share that failure point.

| Topology | What an operator provisions |
| --- | --- |
| FRP or reverse proxies with SpaceWeb, Cloudflare DNS-only, or another authoritative DNS service | Each public relay has working TLS for the same hostname, routes to a surviving Node, and has its IPv4 address added to the same pool. Publish both addresses before a failure. |
| Named Cloudflare Tunnel | Map the public hostname to one named tunnel once. Run replicas of that tunnel on two independent hosts. For a local Node on each host the service URL can be the same loopback address on both. Each replica must still reach its Node when the other host disappears. Cloudflare routes new connections to a surviving replica. |
| Provider with health-based routing | Configure the provider to select reachable origins behind the same hostname. The existing PowerDNS backend supports this with a Lua record. |

An established connection through a lost host can break. New connections must be able to reach the surviving path. A static DNS pool leaves the choice of the next address to the client. A managed ingress can do that selection itself. A random hostname from a Quick Tunnel does not implement a stable public origin. The mesh's existing authorization and task ownership still apply.

## IPv4 DNS adapter v1

Implement an executable in any language. The Hub launches it once per operation, without arguments or a shell. Read one JSON object from stdin, write one JSON object to stdout and exit. No SDK, daemon, registration endpoint or rebuild is needed to add providers to a Node that supports `GPTADMIN_FRP_DNS_PROVIDER=exec`.

| Request | Successful response | Meaning |
| --- | --- | --- |
| `{"action":"append_a","name":"mesh.example.net","value":"192.0.2.42"}` | `{"ok":true,"value":"192.0.2.42"}` | Ensure this one approved IPv4 address is present at the exact configured name. Already present is success. |
| `{"action":"read_a_pool","name":"mesh.example.net"}` | `{"ok":true,"values":["192.0.2.41","192.0.2.42"]}` | Read all A addresses from the provider for that name. An empty pool returns `[]`. This operation has no side effects. |

The IPs above are documentation examples. Supply routable addresses belonging to your relays for a real publication. The caller accepts successful JSON with exit code zero. On failure return `{"ok":false,"error":"write_unresolved"}` or exit nonzero. Never include credentials or raw provider responses in errors.

- Bind the helper to the locally configured exact hostname below the zone. This version supports subdomains and excludes wildcards and the zone apex. Only append the approved address. Preserve existing entries, other simultaneous additions and unrelated records. A whole-set read/replace without atomic concurrency control can discard the other node's address.
- If the provider accepted a write but the response was lost, read the exact record. Report success when it is present. Otherwise report the outcome as unresolved. A second blind write can duplicate the first one.
- No DNS write belongs in normal command execution, heartbeat handling or Raft leader election. New FRP relay enrollment retains its existing owner approval. A repeated join with a consumed grant asks the helper only to read the pool.
- Provider acceptance and reachability are different observations. The executable reports membership in the DNS pool. The client exercise below tests whether the resulting public route works.

## Configure a new installation

Put the executable at an absolute path owned by the operator and executable by the Node service. Group- or world-writable executables are rejected. Configure the same exact hostname and provider on each eligible enrollment writer. The program and its credentials are local to that host. For example, install the Cloudflare reference implementation from the checkout:

```sh
sudo install -m 0755 scripts/gptadmin_dns_cloudflare.py /usr/local/lib/gptadmin/gptadmin_dns_cloudflare.py
```

Set the following in the Node service environment:

```ini
GPTADMIN_FRP_DNS_PROVIDER=exec
GPTADMIN_FRP_DNS_HELPER=/usr/local/lib/gptadmin/gptadmin_dns_cloudflare.py
GPTADMIN_FRP_DNS_ZONE=example.net
GPTADMIN_FRP_DNS_NAME=mesh.example.net
GPTADMIN_FRP_DNS_CREDENTIALS_FILE=/etc/gptadmin/cloudflare-dns.json
```

The helper receives `PATH`, the zone, the name and the optional local credential file path. For Cloudflare the file is JSON with fields `zone_id` and `api_token`. Use a token scoped to DNS editing on this zone and restrict file access to the service. The sample uses DNS-only A records and refuses mixed proxied A/AAAA or an alias at that name.

SpaceWeb can use the same configuration with `scripts/gptadmin_dns_sweb.py` and its existing credentials file. It requires the single-attempt-capable `swebmimic` transport. The existing built-in `spaceweb` mode and previously approved `frp_spaceweb_pool` keep their provider binding. For a fresh installation with a custom DNS helper use the local configuration above and the ordinary relay grant option `--frp-relay-ipv4`. The operator may also invoke a locally provisioned helper directly with the same JSON.

A named Cloudflare Tunnel with independent replicas needs only the public origin and the local connectors. It does not need this IPv4 publisher. [Cloudflare replica setup](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/tunnel-availability/) and [DNS-only round robin](https://developers.cloudflare.com/dns/manage-dns-records/how-to/round-robin-dns/).

## A task for an adapter author or AI

Open the [GPTAdmin DNS adapter skill](../plugins/gptadmin/skills/gptadmin-dns-adapter/SKILL.md) in your assistant, or select `gptadmin-dns-adapter` in the GPTAdmin plugin. Give it your DNS provider and test hostname.

> Configure GPTAdmin for my DNS provider. Use our two test ingresses and show that my MCP client can reconnect at the same URL after the first ingress stops.

For acceptance use a disposable hostname with two independent entries and the intended client. Open a **new** connection through the unchanged HTTPS URL and run a harmless authenticated command. Disable only the first test ingress, then repeat that command on an available node using ordinary DNS, TLS verification and the same identity. A pinned request can diagnose one path but cannot demonstrate automatic client failover. Reconnect after restoring the entry. For managed tunnels, stop one test connector instead of withdrawing a public DNS record.
