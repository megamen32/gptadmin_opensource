# Network Tunnel threat model

> **Status:** This is the security contract for Network Tunnel v1. The isolated
> relay and edge vertical slice are implemented; profile/MCP integration must
> continue to satisfy these controls.

## Security objective

An approved MCP client may use a short-lived, target-bound TCP capability
through one selected agent without turning that agent, the Hub, or an exposed
connector into a general-purpose proxy. The full protocol is specified in
[`NETWORK_PROXY.md`](./NETWORK_PROXY.md).

The protected assets are agent network reachability, private LAN services,
public egress identity, Hub availability, ShellMCP control availability,
approval policy, grant integrity, and user secrets. TCP payloads and headers
are deliberately outside the audit data set.

## Trust boundaries

| Boundary | Trusted responsibility | Prohibited shortcut |
| --- | --- | --- |
| MCP client to Hub | Request and observe an approved capability. | No raw SOCKS credential, generic connect primitive, or authority to mint grants. |
| Hub control plane | Authorize profiles, issue leases/offers/grants, revoke, and audit metadata. | No data-stream file descriptors, payload relay, or stream resumption. |
| Isolated relay | Validate and consume one-time grants; pair bounded WSS streams. | No ShellMCP control credential and no policy widening. |
| Proxy agent | Resolve DNS, enforce IP/port/interface policy, and dial only after validation. | No implicit access because it runs ShellMCP or has a heartbeat. |
| Local connector | Accept only approved local connections and present a capability. | No public bind and no conversion of a scoped capability into an unrestricted proxy. |

The existing queue, `shell_exec`, durable command outbox, heartbeat, and
existing webhook mode stay outside this boundary. None carries a proxy offer,
ticket, frame, or payload.

## Threats and required controls

| Threat | Required v1 control |
| --- | --- |
| A normal ShellMCP agent becomes a proxy without approval. | Network Tunnel is a separate capability and future agent role. It is disabled without explicit profile policy, approval, lease, and agent selection. `shell_exec`, heartbeat, and agent registration cannot create it. |
| A public unauthenticated proxy is exposed. | Loopback is the only no-auth bind. LAN publication needs explicit credentials, source-CIDR ACL, and expiry; public bind is denied. |
| A client uses an approved LAN path for Internet egress, or vice versa. | `lan` and `internet_egress` are mutually exclusive capability modes. CIDRs, port list, and mode are bound to every offer and grant. |
| DNS rebinding or a mixed answer reaches a private or metadata address. | The agent resolves locally at dial time and validates every chosen address, port, and egress interface. `internet_egress` denies loopback, private, link-local, metadata, multicast, broadcast, unspecified, documentation, benchmarking, and reserved ranges. |
| A weak ticket is replayed, exchanged between roles, or used for another target. | Client and agent receive separate short-lived grants with a unique, atomically consumed `jti`, `role`, `stream_id`, `agent_id`, `target`, capability ID, expiry, and protocol version. |
| Webhook delivery injects a target, ticket, or payload. | Webhook contains only signed `offer_id`, timestamp, and nonce. The agent verifies it and uses only `/proxy-agent/offers` to retrieve an offer; it never uses `/queue/{server}`. |
| A broken connection resumes with stale or duplicated bytes. | One WSS pair represents one TCP stream. Loss, restart, revoke, expiry, invalid frame, or timeout sends `RESET`; reconnection requires a new offer and stream ID. Replay and resumption are forbidden. |
| Slow peers or frame floods exhaust Hub or relay memory. | A separate relay enforces finite frame, pending-frame, stream, byte, lifetime, idle, and bandwidth limits. It resets before unbounded buffering. The Hub never owns data sockets. |
| A revoked capability keeps an existing connection alive. | Revocation and expiry stop new offers and reset all matching active streams. Restart fails closed; persisted policy is not a credential. |
| Logs expose traffic or secrets. | Audit keeps metadata only and excludes payload, headers, credentials, DNS bodies, and initial bytes. |

## Required negative properties

The implementation review must demonstrate all of the following:

1. A stock ShellMCP agent with no Network Tunnel capability cannot listen,
   dial, forward, or accept SOCKS/HTTP proxy traffic solely because it has a
   ShellMCP identity, queue access, or heartbeat.
2. A ShellMCP control credential cannot authenticate a stream. A stream grant
   cannot execute `shell_exec` or access the queue.
3. The Hub process has no WSS or TCP data-stream file descriptor for a Tunnel
   stream. It may retain metadata only; a separate relay process owns the two
   stream sockets.
4. No unapproved public or LAN destination becomes reachable through DNS,
   hostname aliases, IP literals, interface selection, redirect handling, or
   reconnect behavior.
5. No capability, offer, grant, stream, audit record, or persisted policy can
   reconstitute application payload after a crash or rollback.

## Security test gates for later implementation

Before any future service is advertised, focused tests must prove: role and
target mismatch rejection; atomic ticket replay rejection; offer-webhook nonce
and freshness rejection; queue isolation; agent DNS/private-range/interface
denial; public-bind denial; bounded-frame and slow-consumer reset; FIN
half-close; RESET propagation; revoke/expiry/restart closure; and a process
inspection showing the Hub has no data-stream sockets. Integration coverage
must also show that the legacy Hub and ShellMCP command controls remain usable
when the relay is stopped or fails.

This docs-only slice adds none of those runtime controls. It defines their
acceptance criteria so a future implementation cannot claim Network Tunnel
support before proving them.
