# Network Tunnel protocol contract

> **Status:** The v1 contract is now backed by a runnable vertical slice: an
> isolated WSS relay, an edge offer runner, and a loopback HTTP CONNECT/SOCKS5
> TCP connector. Hub-issued dynamic grants and the AI-facing MCP surface remain
> the next integration stage; the static ticket flow below is for controlled
> bring-up and blackbox testing only.

## Runnable vertical slice

The data plane is separate from ShellMCP queues and heartbeat traffic. Build the
relay and edge binaries from their respective modules:

```bash
tools/build.sh network-tunnel
# build/gptadmin-network-tunnel.tar.gz
```

The archive contains Linux relay/ticket/connector/agent binaries and Android
arm64 connector/agent binaries under `network-tunnel/linux_amd64/` and
`network-tunnel/android_arm64/`, plus Windows amd64 `.exe` binaries under
`network-tunnel/windows_amd64/`.

Create one random relay key with mode `0600`, run the relay on a private
address. When the Hub is the issuer, point both components at that same file
using `GPTADMIN_NETWORK_PROXY_RELAY_KEY_FILE` for the Hub and
`NETWORK_TUNNEL_RELAY_KEY_FILE` for the relay. The client ticket goes to
`network-tunnel-proxy`; the agent ticket is embedded as `relay_ticket` in the
signed offer consumed by `network-tunnel-agent`. Each ticket is one-use and
each TCP connection needs a fresh pair of tickets.

Set `GPTADMIN_NETWORK_PROXY_RELAY_REVOKE_URL` on the Hub to the relay base URL
to deliver signed capability revocations to `/v1/control/revoke`; this control
request carries metadata only and never shares the ShellMCP control token.

For a controlled bring-up, the local issuer is `go-proxyrelay/cmd/networkticket`:

```bash
cd go-proxyrelay
go build -o ../trash/generated/network-tunnel-ticket ./cmd/networkticket
./../trash/generated/network-tunnel-ticket -key-file relay.key -role client \
  -stream-id demo-1 -target 192.168.2.50:80 -output client.ticket
./../trash/generated/network-tunnel-ticket -key-file relay.key -role agent \
  -stream-id demo-1 -target 192.168.2.50:80 -output agent.ticket
```

The issuer is deliberately a bring-up tool, not a replacement for Hub policy
or approval. It must run only on the operator's private admin host.

The connector binds loopback by default on `127.0.0.1:3126`; it accepts only
the exact target bound into its ticket. Use HTTP `CONNECT` or SOCKS5 TCP
`CONNECT`. UDP, transparent proxying, and public listener binds remain outside
v1. The edge runner supports the controlled `file` bring-up mode plus signed
`pull` and `webhook` offer sources; both delivered modes converge on the same
`OfferConsumer` and data-plane activation path. The blackbox coverage is:

```bash
cd go-proxyrelay && go test ./blackbox -count=1
cd ../go-shellmcp && go test ./blackbox -count=1
```

On Android, pass `-dns-server 1.1.1.1:53` when the carrier's libc resolver is
an unavailable localhost stub. The edge log should show the cellular interface
address during a live acceptance; target policy still resolves and validates
every returned address before dialing.

## Purpose and boundary

The Network Tunnel is a future, capability-scoped TCP path for an approved MCP
client or local program to reach a target through a selected agent. It is not a
general-purpose proxy and it is not part of normal ShellMCP command execution.
The product-facing names remain **Hub**, **MCP clients**, and **Tunnel**.

Version 1 has one deliberate scope: TCP carried by HTTP `CONNECT` or SOCKS5
`CONNECT`. Its fixed protocol feature declaration is:

```text
tcp=true
udp=false
socks5_connect=true
socks5_udp_associate=false
tun=false
```

UDP, SOCKS5 `UDP ASSOCIATE`, TUN devices, transparent routing, packet replay,
and stream resumption are unsupported. A later datagram or TUN design must use
a new capability and protocol version; it must not widen this contract.

The Network Tunnel is separate from all existing ShellMCP control paths:

| Existing path | Contractual use | Network Tunnel rule |
| --- | --- | --- |
| `/queue/{server}` and `queueLoop()` | Command delivery | Never carries offers, tickets, stream frames, or proxy payloads. |
| `shell_exec` | Explicit ShellMCP command execution | Never creates, authorizes, or transports a Tunnel stream. |
| Durable command outbox | Retryable command results | Never stores stream data, initial bytes, tickets, or offer state. |
| ShellMCP heartbeat | Agent liveness and registration | Is not a proxy heartbeat and does not keep a stream or capability alive. |
| Existing webhook mode | ShellMCP command notification | Is not a stream transport and cannot be reused as one. |

The Hub is a control-plane issuer and audit point only. A future isolated relay
owns the paired WebSocket data sockets; the Hub has no data-stream file
descriptors, does not proxy payload bytes, and cannot become a data-plane
fallback.

## Capability contract

The Hub may create a capability only after the selected MCP client is
authorized by its admin profile and an explicit approval policy permits it. A
capability is an immutable, versioned policy record with these fields:

| Field | Meaning and validation |
| --- | --- |
| `capability_id` | Opaque unique identifier. It is never a credential. |
| `agent_id` | The one registered agent allowed to perform the destination dial. |
| `mode` | Exactly `lan` or `internet_egress`; the modes cannot be combined. |
| `target_cidrs` | Canonical CIDRs permitted for `lan`. Empty for `internet_egress`; it must not mean all addresses. |
| `target_ports` | Explicit TCP ports or finite port ranges. No implicit all-ports value exists. |
| `protocols` | The fixed v1 feature declaration above. A value outside it is rejected. |
| `lease` | Issued time, expiry, and finite maximum duration. Expiry ends the capability. |
| `limits` | Finite `max_streams`, `max_frame_bytes`, `max_pending_frames`, `max_stream_lifetime_seconds`, `idle_timeout_seconds`, `max_bytes`, and optional finite bandwidth limit. There is no unlimited default. |

`lan` permits only the configured CIDRs, ports, and approved local interfaces.
It never implies Internet egress. `internet_egress` permits only public
Internet destinations after resolution; it never permits a private LAN merely
because the caller supplied a hostname. A capability request, an approved
capability, and a stream audit record keep the mode distinct.

The agent enforces policy at dial time, after its own DNS resolution. It must
re-check every resolved address and reject a request if the selected address is
outside the approved `lan` CIDRs or public-Internet policy. It rejects loopback,
unspecified, link-local, private, multicast, broadcast, documentation,
benchmarking, reserved, and cloud-metadata addresses by default for
`internet_egress`, including `169.254.169.254`. It also rejects a route through
an unapproved interface. DNS names are input, not authorization: an allowed
name cannot bypass IP, port, or interface policy through rebinding or a mixed
DNS answer.

### Capability states

```text
draft -> pending_approval -> active -> revoking -> revoked
                  |              |             \
                  v              v              -> expired
                denied         expired
```

- Only `active` can issue a new offer. `denied`, `expired`, and `revoked` never
  issue one.
- Revocation stops new offers immediately and makes the relay reset every
  active stream for the capability.
- Expiry has the same new-offer and active-stream effect as revocation.
- A Hub, relay, or agent restart fails closed: active capabilities require a
  fresh authorization path before any new stream. Persisted policy may aid
  review, but is never a live credential or an implicit restore of a lease.

## Offer, ticket, and data transport

Offers are control-plane records, not transport connections. A webhook may
notify an agent only with a signed `offer_id`, timestamp, and nonce. It carries
no destination, ticket, stream frame, or payload. The agent verifies freshness
and nonce uniqueness, then opens an outbound control/data session; no inbound
agent listener is required.

An agent pulls offers only through the dedicated long-poll route
`/proxy-agent/offers`. This route is reserved by the contract for the future
Network Tunnel and is never `/queue/{server}`. A webhook notification may wake
the agent to pull; it cannot substitute for the pull response or deliver a
data stream.

For each approved TCP connection, the relay pairs exactly one client WSS with
exactly one agent WSS. The pair is one `stream_id`; another TCP connection gets
another pair. The relay applies the capability's finite frame, queue, byte,
idle, lifetime, and concurrency limits before buffering. It rejects an
oversized frame or queue overflow with `RESET`; it never changes the limit by
accepting more data.

Stream frames are directional and bounded:

| Frame | Meaning |
| --- | --- |
| `DATA` | Bounded bytes in one direction. |
| `FIN` | Sender has no more bytes in that direction; the opposite direction may drain. |
| `RESET` | Immediate failure; both directions close and buffered data is discarded. |

### Stream states

```text
requested -> offered -> pairing -> open -> fin_sent -> closed
                         |             \-> fin_received -> closed
                         v
                       reset
```

The agent may open only after it has accepted the offer and validated the
target policy. A dial failure, lease expiry, revocation, timeout, invalid
frame, broken WSS, or restart transitions the stream to `reset` and then
`closed`. A reconnect may obtain a newly authorized offer and a new
`stream_id`; it cannot resume, replay, retransmit, or splice an old TCP
stream. TCP application data is never persisted for recovery.

### One-time grant claims

The Hub issues separate short-lived grants for the client and agent side of a
single stream. A grant contains at least:

```text
protocol_version, capability_id, stream_id, agent_id, target, role, exp, jti
```

`protocol_version` is `1`. `role` is exactly `client` or `agent`; the relay
rejects the wrong role. `target` canonically binds the requested authority and
TCP port to the stream; the agent separately binds the actual dial to its
post-resolution policy check. `exp` is a short finite expiry. `jti` is unique
and atomically consumed once by the relay for its role, stream, agent, target,
and version. A consumed, expired, revoked, mismatched, or replayed grant is
rejected. ShellMCP control or bootstrap credentials cannot substitute for a
stream grant.

## Publication and local connectors

The Network Tunnel does not create a public proxy. A future local SOCKS/HTTP
connector may bind loopback without proxy authentication because it is local
to the host. Any LAN publication requires explicit proxy credentials, a
source-CIDR allowlist, and an expiry. Public binds are denied. A connector is
limited by the capability it presents; it cannot convert a `lan` capability
into `internet_egress` or vice versa.

MCP clients receive semantic, bounded Network Tunnel operations in a later
slice, not raw proxy credentials, ShellMCP commands, or a generic
`connect(host, port)` primitive. The absence of an explicitly allowed Network
Tunnel capability means no Tunnel access.

## Audit, failure, and rollback

Audit records are metadata only: capability, agent, mode, requested authority
and port, resolved address classification, interface decision, timestamps,
lease outcome, stream lifecycle, byte counters, limit or reset reason, and
grant identifier hashes. They never contain payload bytes, HTTP headers,
credentials, DNS response bodies, SOCKS request bytes, or an initial-byte
sample.

The kill path is capability revoke: the Hub stops offers, the relay rejects
new pairing and sends `RESET` to matching streams, and the agent closes the
matching local dial. If any component cannot confirm the policy, grant, or
lease, it fails closed and resets the stream. Rollback disables Network Tunnel
issuance and offer retrieval, resets active Tunnel streams, and leaves the
Hub, MCP clients, ShellMCP command path, command outbox, and heartbeat
operational. No rollback restores a prior stream or turns persisted policy into
a credential.
