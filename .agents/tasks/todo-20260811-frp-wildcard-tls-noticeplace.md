# FRP wildcard TLS and NoticePlace monitoring

Status: todo

## User request

Issue a replacement trusted TLS certificate for the FRP wildcard serving
`*.t.became.bezrabotnyi.com`, ensure an assigned FRP hostname receives its
own matching certificate and routes to the registered Hub, then configure
NoticePlace to monitor this public TLS/business path.

## Objective and business canary

For a newly assigned or existing registered `u-<id>.t.became.bezrabotnyi.com`
endpoint, a strict public HTTPS request to `/healthz` must both validate the
certificate hostname and return the registered Hub's `200 {"ok":true}`. A
NoticePlace monitor must detect loss of that exact public TLS/health path.

## Scope and exclusions

Owned: FRP ingress diagnosis, certificate renewal/reload if technically
required, hostname-to-proxy routing, and a narrow NoticePlace monitor/check for
the public endpoint. Excluded: Hub code changes, client tokens, VPN, replacing
FRP with Cloudflare, generic fleet-health rollout, Telegram delivery, and
unrelated repository changes.

## Initial control limit

- Minimum / maximum active minutes: 15 / 40 (immutable initial range)
- Started at: 2026-08-11T21:49:01+03:00
- Lifecycle provenance: created by Lead from the explicit user request; no
  prior task lineage
- Last task-file mtime observed: 2026-08-11T21:49:01+03:00

## Runtime identity

- Harness: Codex desktop
- PID: 1894973
- Agent session: unknown (harness did not expose a stable session id)
- PID status: alive at task creation
- Last PID signal: shell parent PID reported 1894973 at 2026-08-11T21:49:01+03:00
- Last task-file transition: created todo at 2026-08-11T21:49:01+03:00

## Research contract

Perform read-only tracing from the public FRP hostname through DNS/TLS listener,
certificate store/renewal mechanism, FRPS vhost routing, and current
NoticePlace monitoring capabilities. Do not issue certificates, reload/restart
services, alter DNS, or create monitoring events. Return the exact safe
execution slice and commands, including the current assigned hostname if it can
be identified without exposing secrets.

Acceptance: evidence identifies the live certificate owner/reload path and a
concrete monitor creation/update route, or names the missing access/input.

## Stop conditions

Stop and report before any restart, deployment, DNS change, certificate
issuance, credential use, or outbound NoticePlace/Telegram notification not
strictly needed for the approved objective.
