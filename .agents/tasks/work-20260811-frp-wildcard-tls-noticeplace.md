# FRP wildcard TLS and NoticePlace monitoring

Status: work — awaiting exact deployment and producer-credential authorization

## User request

Issue a replacement trusted TLS certificate for the FRP wildcard serving
`*.t.became.bezrabotnyi.com`, ensure an assigned FRP hostname receives its
own matching certificate and routes to the registered Hub, then configure
NoticePlace to monitor this public TLS/business path.

## Objective and business canary

For the registered `your-subdomain.t.became.bezrabotnyi.com` endpoint, a strict
public HTTPS request to `/healthz` must both validate the certificate hostname
and return the registered Hub's `200 {"ok":true}`. A NoticePlace monitor must
detect loss of that exact public TLS/health path.

## Scope and exclusions

Owned: FRP ingress diagnosis, certificate renewal/reload if technically
required, hostname-to-proxy routing, and a narrow NoticePlace monitor/check for
the public endpoint. Excluded: Hub code changes, client tokens, VPN, replacing
FRP with Cloudflare, generic fleet-health rollout, Telegram delivery, and
unrelated repository changes.

## Initial control limit

- Minimum / maximum active minutes: 15 / 40 (immutable initial range)
- Started at: 2026-08-11T21:49:01+03:00
- Lifecycle provenance: copied by Lead from the committed todo snapshot before
  monitoring implementation; initial scope unchanged
- Last task-file mtime observed: 2026-08-11T22:03:00+03:00

## Runtime identity

- Harness: Codex desktop
- PID: 1894973
- Agent session: unknown (harness did not expose a stable session id)
- PID status: alive during research
- Last PID signal: subagent research completed at 2026-08-11T22:01:00+03:00
- Last task-file transition: todo -> work at 2026-08-11T22:03:00+03:00

## Read-only research evidence

- Public DNS resolves the assigned endpoint to `203.0.113.10`.
- Strict public canary: `https://your-subdomain.t.became.bezrabotnyi.com/healthz`
  returns HTTP/2 200 and `{"ok":true}`.
- Its current Let’s Encrypt certificate has SAN
  `your-subdomain.t.became.bezrabotnyi.com`, issuer `YE1`, and expiry
  `2026-11-06`; no certificate issuance is justified now.
- Nginx terminates the wildcard TLS in
  `/etc/nginx/sites-enabled/t.became.bezrabotnyi.com` and proxies it to FRPS
  on `127.0.0.1:8079`. `/etc/frp/frps.toml` configures
  `subdomainHost="t.became.bezrabotnyi.com"` and the same HTTP vhost port.
  `frps.service` and `gptadmin-tunnel-frpc.service` are active.
- Certbot owns renewal through `snap.certbot.renew.timer` and
  `/etc/letsencrypt/renewal/gptadmin-canonical-http.conf`.
- NoticePlace has no discovered generic URL-monitor CRUD endpoint. Its existing
  event path is authenticated `POST /v1/events`; current local health probes do
  not validate an arbitrary public TLS endpoint.

## Proposed bounded implementation

Install a dedicated timer/service which uses strict HTTPS hostname validation,
requires exactly HTTP 200 plus `{"ok":true}`, and emits a deduplicated event to
NoticePlace only on state transition. The producer must obtain its token through
an approved opaque secret/configuration path; no secret is recorded in this
task file.

## Authorization gate

Installing/enabling the systemd monitor is a deployment and begins an outbound
event path. It requires exact human approval and an attested producer-secret
handoff. No service reload/restart, certificate issue, DNS mutation, or event
delivery has occurred.

## Corrected objective and execution (2026-08-11)

The user clarified that the required fix is not a certificate for one existing
client ID: every current and future assigned `u-<id>` hostname must pass TLS.

- DNS returns three ingress addresses: `203.0.113.10`, `185.240.120.152`, and
  `212.192.31.128`.
- Before the fix, the latter two served a valid Let’s Encrypt wildcard
  `*.t.became.bezrabotnyi.com`; server-100 (`203.0.113.10`) instead served
  a leaf certificate for only `your-subdomain`. A random future ID therefore failed
  hostname validation only when DNS selected server-100.
- The valid existing wildcard from the two matching ingress peers was securely
  synchronized to root-owned `/etc/nginx/ssl/gptadmin-frp-wildcard/` on
  server-100. The key was never written to command output; its public key was
  verified against the certificate.
- Only `/etc/nginx/sites-enabled/t.became.bezrabotnyi.com` was changed, with
  its pre-change copy preserved under `/etc/nginx/rollback-receipts/`. Nginx
  configuration validation passed, then Nginx was reloaded.
- After reload, a strict SNI handshake for a never-issued random subdomain
  received the wildcard certificate from all three public addresses. Its HTTP
  response is the expected FRP 404 because no proxy is registered for that ID.
- The registered live client `your-subdomain` now has strict TLS plus `/healthz`
  HTTP 200 on all three ingress addresses.

## Remaining durability note

The wildcard certificate currently expires on 2026-08-28. The two peer
ingresses contain the same certificate, but no local ACME renewal contract was
found on any inspected host. Renewal and automatic cross-ingress certificate
sync need a separately authorized DNS-01/credential-owner decision; the
immediate unique-client TLS incident is resolved, but this is not durable
renewal proof. NoticePlace monitoring remains pending its precise delivery
policy because the existing `health` producer can trigger broader automation.

## Fresh client registration canary (2026-08-11)

The first TLS-only result did not prove new-client registration, so an isolated
real FRPC canary was run. A temporary unique subdomain was generated, three
temporary `frpc` processes cloned the canonical primary/server01/server01 client
configurations, and each proxied only the existing local `127.0.0.1:9001`
health endpoint. No user client configuration was changed.

- The canary initially exposed FRPS 404 from remote edges because their
  temporary proxy names accidentally collided with the existing client's proxy
  names; this was a test setup error, not a TLS result. All temporary processes
  and files were cleaned before retry.
- The corrected canary used distinct proxy names per edge and the new ID
  `u-e2e20260811d`.
- Strict HTTPS requests with DNS bypass (`--resolve`) returned HTTP 200 and
  `ssl_verify_result=0` for all three public ingress addresses:
  `203.0.113.10`, `185.240.120.152`, and `212.192.31.128`.
- The temporary FRPC processes and their root-only `/run` configuration/log
  directory exited and were removed at the end of the canary.

Result: `DELIVERY P0 CONFIRMED` for newly registered unique client IDs across
the current three-edge FRP ingress and strict wildcard TLS path.
