# GrepMesh recovery topology reconnaissance

Role: Explorer

## Objective

Read-only discover the real server-100/server-88 deployment and transport prerequisites for the GrepMesh vertical slice: OS, SSH/service capabilities, management/VPN addresses, available runtime control seams, existing users/ports, and safe install paths. Do not mutate either host.

## Owned scope

- Read-only local and SSH probes for server-100 and server-88 using configured aliases only.
- Inspect existing service conventions and non-secret network/service metadata.
- Do not read secrets, env values, private keys, or credentials; do not deploy/restart.

## Acceptance/report

Append a target matrix with exact host/OS/addresses, required install/restart actions, known blockers, and a safe two-node deployment preview. Stop at `NEEDS_REDECOMPOSITION` if auth, ACL, firewall, or peer identity must be invented.

## Budget

Explorer, low effort, 20 / 40 / 80 active minutes, relative cost low.

## Explorer evidence (2026-08-11, read-only)

### Target matrix

| Target | SSH alias / address | OS and interfaces | Existing control seam | GrepMesh state | Relevant users/ports |
|---|---|---|---|---|---|
| server-100 | `admin-server-100` -> `203.0.113.10:22104`, user `admin` | Ubuntu kernel `6.8.0-136-generic`, x86_64; `enp28s0f2np2 UP 203.0.113.10/24 203.0.113.10/24`; other physical NICs down | `gptadmin-hub.service=active`, `gptadmin-tunnel-frpc.service=active`; canonical binary path `/opt/gptadmin/bin`; systemd available | No `grepmesh-mcp.service` listed/probed; no `:9419` listener | `admin` has `sudo NOPASSWD: ALL`; `:9001` listens on `203.0.113.10` and `127.0.0.1`; `/opt/gptadmin` and `/usr/local/bin` exist |
| server-88 | `admin-server-88` -> `203.0.113.10:22`, user `admin` | Ubuntu kernel `6.8.0-136-generic`, x86_64; `enp6s0 UP 203.0.113.10/24 203.0.113.10/24` | `shellmcp.service=active`, `User=root`, `ExecStart=/opt/gptadmin/bin/rootd-go`; systemd available | `grepmesh-mcp.service=inactive` (unit is not installed/listed); no `:9419` listener | `admin-search` does not resolve via `getent passwd`; `/opt/gptadmin` and `/usr/local/bin` exist |

### Transport and topology findings

- SSH aliases are configured in `/home/admin/.ssh/config:24-31`; no alternate management/VPN hop is required for either target.
- Cross-host TCP probes from 100 -> `203.0.113.10:9419` and 88 -> `203.0.113.10:9419` both returned connection refused. This is consistent with no service/listener, not proof of a firewall allow rule.
- `server-88.internal`, `server-100.internal`, and `gptadmin.internal` do not resolve locally or from either target in the read-only `getent hosts` probe. The example peer and GPTAdmin URLs therefore cannot be used as-is.
- No firewall tool reported state in the bounded probe (`ufw`/`firewall-cmd` unavailable or no output). Firewall/ACL policy remains unknown and must not be invented.
- `grepmesh/grepmesh-mcp.service:8-18` requires a dedicated `admin-search` identity, `/usr/local/bin/grepmesh-mcp`, `/etc/grepmesh-mcp/config.json`, and writable `/var/lib/grepmesh-mcp`; the dedicated identity is absent on server-88 and was not proven on server-100.
- `grepmesh/config.example.json:2-4,11-16,27-30` binds each node to its LAN address but advertises unresolved `https://server-88.internal:9419/mcp` and calls unresolved `https://gptadmin.internal/mcp-relay/grepmesh`; it also requires `GPTADMIN_GREPMESH_TOKEN`.
- `grepmesh/README.md:38-54` explicitly leaves production enrollment, credentials, mTLS/firewall policy, ACLs, and service restart as deployment gates.

### Safe two-node deployment preview (no mutation performed)

1. Human/Lead must choose and authorize concrete routable peer addresses (LAN IPs or an existing authenticated transport) and the GPTAdmin topology endpoint; current `.internal` names are not usable.
2. Preflight both hosts for a dedicated least-privilege search user, read-only roots, `/var/lib/grepmesh-mcp`, and the exact binary/config hashes; create/install only after authorization.
3. Configure server-100 as `host_id=server-100`, bind `203.0.113.10:9419`; configure server-88 as `host_id=server-88`, bind `203.0.113.10:9419`; use exact peer URLs and an approved auth/mTLS/ACL policy.
4. Add the supplied systemd unit on each host, enable/start only after a restart gate, then verify listeners, local MCP, peer fan-out, cached topology, and GPTAdmin read-only projection through the real consumer path.

### Result

`NEEDS_REDECOMPOSITION`: the recovery slice cannot safely proceed to deployment because peer identity/addressing, GPTAdmin endpoint, token ingress, dedicated service identity, and firewall/ACL policy are not established. No auth, ACL, firewall, service, or host state was changed.

Highest-value next probe: Lead/user should provide the approved transport and identity contract (exact peer URLs/IPs, GPTAdmin topology endpoint, token/mTLS mechanism, and firewall scope), then reassign a bounded preflight/implementation task.
