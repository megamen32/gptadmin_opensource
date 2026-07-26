# Network Tunnel Implementation Plan

> **For agentic workers:** Use `subagent-driven-development` when delegation is
> useful. Let delegated work run to its normal handoff; do not add per-phase
> polling or review gates. Run one consolidated review after the runnable
> vertical slice is complete.

**Goal:** Give an explicitly approved ShellMCP agent temporary, audited TCP access to selected networks behind NAT/4G through separate webhook or pull delivery, without using the ShellMCP command channel for proxy bytes.

**Architecture:** The Hub owns policy, approvals, leases and one-time stream grants. A separate `gptadmin-proxy-relay` process carries data between a local connector and a separate `gptadmin-proxy-agent`; the agent resolves and enforces the target policy locally before dialing the LAN device. Webhook and pull differ only in offer delivery: both converge on the same authenticated WSS stream protocol.

**Tech Stack:** Go stdlib, TLS WebSocket with one WSS per TCP stream, existing Go Hub/ShellMCP services, systemd packaging, Python CLI, OpenAPI/admin surfaces after the backend contract is stable.

## Global Constraints

- Existing `/queue/{server}`, `queueLoop()`, `shell_exec`, durable command outbox and ShellMCP heartbeat are control-plane-only and remain untouched by proxy payloads.
- The first protocol version is TCP-only: HTTP CONNECT and SOCKS5 CONNECT; SOCKS5 UDP ASSOCIATE, TUN and transparent routing are explicitly unsupported.
- Raw L4 relay runs in a separate process/service from `gptadmin-hub`.
- `SHELLMCP_TOKEN` authenticates agent control/bootstrap only; stream tickets are separate, one-time, role-bound and target-bound.
- No public or unauthenticated LAN proxy: loopback may use no-auth; LAN publication requires credentials, source-CIDR ACL and an expiry; public bind is denied.
- Agent-side policy runs after local DNS resolution and blocks loopback, metadata, multicast, broadcast and unapproved interfaces by default.
- A broken transport resets active TCP streams. No replay or transparent resume of arbitrary TCP payload is allowed.
- No application heartbeat is introduced. Capability leases, offer claims and active transport state determine proxy status.
- Logs and audit records contain metadata only, never payload, headers, credentials or initial bytes.
- All behavior changes use TDD: failing regression/security test first, then the smallest implementation, then focused and full verification.

---

## Target contract

```text
Capability: disabled -> requested -> approved -> awaiting_agent -> accepting
             -> active -> draining -> revoked | expired | failed

Stream: created -> offered -> claimed -> dialing -> connected
                                      -> rejected
        connected -> client_fin | agent_fin -> closed
        connected -> reset | timeout | revoke -> closed
```

The first stream framing is `gptadmin-proxy/1`: a bounded JSON handshake followed by binary `DATA`, `FIN`, `RESET` and `ERROR` frames. A single WSS carries one TCP connection, with a 32–64 KiB frame limit, write deadlines and bounded queues. There is no chunk replay. Multiplexing and UDP require a later protocol version.

## MCP surface decision

Expose one logical MCP server named `network-access` to the AI, preferably as a
managed child MCP registered through the Hub's existing child-server model.
The MCP is a semantic control facade; it does not expose raw SOCKS credentials,
ShellMCP commands or a generic `connect(host, port)` tool.

Recommended tools:

```text
network_access_plan(agent, purpose, scope, target_cidrs, target_ports, duration)
network_access_enable(plan_id, explicit_confirm)
network_access_status(capability_id)
network_targets_discover(capability_id, bounded_ports)
network_http_request(capability_id, target_id, method, path, body_limit)
network_access_disable(capability_id)
```

`scope` must distinguish `lan` from `internet_egress`; they have different
policies and abuse risks. Enabling access returns the lease, exact CIDR/port
scope and limitations. The AI can then say, in effect: “I have temporary
access to this camera LAN; I will inspect only the approved HTTP/API ports.”

The same data-plane must support two explicit human workflows:

```text
Mac local connector -> Hub -> Android proxy-agent -> 192.168.2.x camera
Mac local connector -> Hub -> Android proxy-agent -> public API over Android 4G
```

For the first route, the Mac connector binds only `127.0.0.1` and uses
`socks5h`/HTTP CONNECT, so camera DNS and TCP dialing happen on the Android
side. The Mac does not need a LAN listener on the Android host and does not
depend on ADB forwarding. For the second route, `internet_egress` explicitly
allows public destinations while denying private, loopback, link-local,
metadata and other reserved ranges; the capability response includes the
observed egress address for API test evidence. `lan` and `internet_egress`
must not silently widen into one unrestricted scope.

The raw local SOCKS/HTTP connector remains a separate CLI for humans and
ordinary programs. If browser interaction is required, add a bounded browser
worker later; do not make the AI configure an arbitrary browser-wide proxy.
For camera video, HTTP configuration and RTSP/media access are separate
capabilities; an HTTP MCP tool must not imply that it can view or relay video.

The MCP facade calls Hub proxy-control APIs over a local authenticated boundary
and never calls `/queue/{server}`. The Hub owns approval, leases, ACLs and
revocation; the MCP process does not mint unrestricted credentials.

### Task 1: Threat model and protocol contract

**Files:**

- Create: `docs/NETWORK_PROXY.md`
- Create: `docs/NETWORK_PROXY_THREAT_MODEL.md`
- Modify: `docs/ADMIN_PROFILES.md`
- Modify: `public/openapi.yaml`

**Deliverable:** Document capability fields (`agent_id`, `mode`, `target_cidrs`, `target_ports`, `protocols`, `lease`, `limits`), state machines, grant claims (`capability_id`, `stream_id`, `agent_id`, `target`, `role`, `exp`, `jti`, protocol version), and the following transport semantics:

- webhook sends only a signed `offer_id`, timestamp and nonce; the agent opens the outbound data session;
- pull uses a dedicated `/proxy-agent/offers` long-poll and never `/queue/{server}`;
- a client or agent reconnect can obtain new offers, but cannot resume an old TCP stream;
- `tcp=true`, `udp=false`, `socks5_connect=true`, `socks5_udp_associate=false`, `tun=false`;
- restart fails closed for active capabilities; persisted policy is not a live credential.

**Gate:** Threat-model review proves that a normal ShellMCP agent cannot silently become a general-purpose public proxy and that the Hub has no data-stream file descriptors.

### Task 2: Hub proxy controller

**Files:**

- Create: `go-hub/internal/hub/network_proxy.go`
- Test: `go-hub/internal/hub/network_proxy_test.go`
- Modify: `go-hub/internal/hub/server.go` near `Server`, `New()` and route registration
- Modify: Hub tool/schema registration near `callHubTool()` and relevant OpenAPI entries

**Interfaces:**

```go
type NetworkProxyPolicy struct {
    AgentID string
    Mode string // webhook, pull, auto
    TargetCIDRs []string
    TargetPorts []int
    MaxStreams int
    MaxBytes int64
    Lease time.Duration
}

type ProxyStreamGrant struct {
    CapabilityID string
    StreamID string
    AgentID string
    Target string
    Role string // client or agent
    Token string
    ExpiresAt time.Time
}
```

Implement controller operations for request, approve, open, status and revoke under a namespace such as `/proxy-control/v1/`. Opening a stream atomically consumes a grant; revocation transitions to draining and signals the relay to close active pairs. Keep these operations independent of `Server.relayJobs`, shell jobs and `touchShellPollLocked()`.

**TDD steps:**

- Add tests that deny an unauthorized profile, an unapproved agent, an expired capability, an unapproved CIDR/port, a wrong role and a reused `jti`.
- Run `cd go-hub && go test ./internal/hub -run 'Proxy|Network' -count=1` and verify the new tests fail before implementation.
- Implement policy, lease, atomic claim and revocation with opaque serialized state.
- Add tests proving revoke is idempotent, restart does not resurrect active capabilities, and tokens/targets are absent from audit payloads.
- Run `cd go-hub && go test ./...`.

**Gate:** Hub can manage a capability while the relay is stopped; existing queue, MCP relay and ShellMCP tests remain green.

### Task 3: Isolated proxy relay

**Files:**

- Create module: `go-proxyrelay/go.mod`
- Create: `go-proxyrelay/cmd/proxyrelay/main.go`
- Create: `go-proxyrelay/internal/ticket/`
- Create: `go-proxyrelay/internal/relay/`
- Test: relay unit and integration tests beside those packages
- Modify: `tools/build.sh`
- Create: `deploy/systemd/gptadmin-proxy-relay.service`
- Modify: `cli.py` only for installation, update and rollback of this separate binary

Implement a relay that validates the role-bound grant, pairs exactly one client WSS with one agent WSS, and never accepts a Shell token. Use bounded frame/write queues, dial and idle deadlines, maximum stream lifetime, byte/bandwidth limits, per-agent/profile concurrency limits and systemd resource limits. On relay crash, Hub and ShellMCP must remain usable.

**TDD steps:**

- Add failing tests for pairing, grant replay, wrong capability/role, invalid protocol version, `FIN` half-close, `RESET` propagation and revoke closure.
- Add failing slow-consumer, max-frame, max-stream, max-byte and timeout tests; assert bounded memory/queue behavior.
- Implement one-WSS-per-TCP-stream relay and metadata-only audit hooks.
- Run `cd go-proxyrelay && go test ./...` and an integration test with a local TCP echo target.
- Run the relay under a dedicated service account with explicit `LimitNOFILE`, memory and process limits; verify stopping it does not stop Hub.

**Gate:** A relay failure, slow client or stream flood cannot consume the Hub process or ShellMCP control channel.

### Task 4: Edge proxy agent

**Files:**

- Create: `go-shellmcp/internal/networkproxy/policy.go`
- Create: `go-shellmcp/internal/networkproxy/offer_client.go`
- Create: `go-shellmcp/internal/networkproxy/dialer.go`
- Create: `go-shellmcp/internal/networkproxy/agent.go`
- Test: corresponding package tests
- Create: `go-shellmcp/cmd/gptadmin-proxy-agent/main.go`
- Modify: platform packaging/install paths without modifying `queueLoop()` or `heartbeatLoop()` contracts

The agent receives offers via the dedicated pull long-poll or signed webhook activation, claims the one-time grant, resolves target names locally, validates every resolved IPv4/IPv6 address against policy, pins the approved address for the dial and connects outbound to the relay. It must not register another ShellMCP queue or require ShellMCP heartbeat.

**TDD steps:**

- Test that webhook and pull deliver the same claim behavior, including nonce replay rejection.
- Test DNS rebinding, loopback, metadata, multicast, broadcast, IPv4/IPv6 CIDR and port/interface policy.
- Test lease expiry, revoke, wrong agent identity and relay outage.
- Test that command execution remains available with proxy data-plane disabled or failed.
- Run `cd go-shellmcp && go test ./...`.

**Gate:** With heartbeat disabled, an agent can expose only explicitly approved TCP targets in both activation modes.

### Task 5: Connector and Android value proof

**Files:**

- Refactor: `go-shellmcp/internal/proxy/proxy.go` as protocol ingress only
- Extend tests: `go-shellmcp/internal/proxy/proxy_test.go`
- Create: `go-shellmcp/cmd/gptadmin-proxy/main.go`
- Modify: `deploy/android-4g-lan-proxy.sh` only for the local proof path and explicit authenticated-LAN mode
- Modify: `cli.py` with `gptadmin proxy open|status|close`

The first useful milestone is pull-mode Android plus a remote local connector bound to `127.0.0.1:3126`, SOCKS5 CONNECT and HTTP CONNECT, one WSS per TCP connection, a 15-minute capability lease, an allowlist such as `192.168.2.0/24` with selected ports, and no public/LAN listener. The current ADB proof remains a TCP-only local validation and is not treated as the remote protocol.

**TDD steps:**

- Test SOCKS5 CONNECT and HTTP CONNECT against a local echo target.
- Test loopback no-auth, authenticated LAN publication, source-CIDR denial and public-bind denial.
- Test remote DNS semantics (`socks5h` behavior), dynamic local port selection from 3126 downward and credential absence from argv/logs.
- Test capability revoke blocks new connections and closes an active stream.
- Run Android reproducible build and the external-network acceptance matrix.

**Acceptance:** From an external network, the Mac connector reaches an approved Android-LAN target such as a camera and produces Android 4G egress distinct from the direct host route for a public API test; a private target is denied in `internet_egress`, a public target is denied in `lan`, revoke closes the active stream, UDP is reported unsupported, and ShellMCP queue latency remains within the agreed baseline.

### Task 6: Admin and AI surfaces

**Files:**

- Modify: `public/openapi.yaml`
- Modify: `public/admin/` current production UI
- Modify: `admin-ui/` only after the explicit parity gate
- Modify: typed Hub tool registration and access-profile policy

Generic programs use the local SOCKS/HTTP connector. Custom GPT/browser flows do not receive raw SOCKS credentials; add a bounded typed operation such as `network_http_request` over the same capability with method, target, response-size, timeout and redaction limits. Keep LAN target policy and approvals visible in admin status.

**Gate:** The AI can read one permitted router status endpoint, while arbitrary LAN targets, unrestricted methods, large bodies and credentials remain denied/redacted.

### Task 7: UDP/TUN, separate project

Do not add `UDP ASSOCIATE` to protocol version 1. ADB forwarding is TCP-only, and Android shell UID cannot be assumed to create a TUN. A future datagram project must first select Android `VPNService`/root/Termux privileges, a QUIC or other datagram-capable relay, NAT mapping limits and a dedicated test matrix. It must use a new capability and protocol version rather than silently changing TCP semantics.

## Rollback

Disable proxy capability creation, revoke active capabilities, stop `gptadmin-proxy-relay`, and remove proxy routes while leaving `gptadmin-hub`, ShellMCP queues and MCP relay running. Do not roll back by changing ShellMCP heartbeat or command-queue behavior.

## Final self-review

- User value is covered by the Android+LAN TCP milestone.
- Webhook and pull are both covered without putting payloads in webhook requests or `/queue`.
- Security coverage includes public-proxy abuse, DNS rebinding, token replay, revocation and resource exhaustion.
- UDP is explicitly marked unsupported until Android privilege and datagram transport are proven.
- The plan does not require heartbeat and does not promise TCP stream resume.
