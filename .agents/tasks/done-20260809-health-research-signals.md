# Research lane: host, service, and log signals

Role: Explorer
Status: complete
Owner: Explorer
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Establish what already exists for observing host CPU/RAM/disk and other health
signals, failed services, and error-level/keyword logs, and identify the real
consumer/event path available to GPTAdmin. This is read-only research only.

## Allowed paths

- `/home/admin/ServersAdministartion`
- Existing inventory/docs under that root that directly describe host/service/
  log health and event delivery.

## Excluded paths and actions

- Do not edit source, configs, task files, generated files, or service state.
- Do not restart, deploy, send messages, change secrets/providers, or inspect
  secret values.
- Do not infer a live capability from a name, unit file, or historical note.

## Acceptance check

Return a compact evidence table with exact file/line or endpoint references for
each signal family, cadence/trigger, transport, current consumer, and missing
piece needed to create one deduplicated incident. Mark every unproven link as a
gap. Include the shortest safe synthetic-canary entry point if one is already
documented.

## Budget and stop conditions

- Model: gpt-5.6-luna explorer, low reasoning
- Active minutes: 25 / 45 / 90
- Relative cost: low; read-only search and bounded file inspection
- Stop when the acceptance table and gaps are proven or when the next fact
  requires a consequential action.
- Return `NEEDS_REDECOMPOSITION` if the scope expands beyond signal sources.

## Report contract

Append detailed evidence and result to this file. Return L only a TL;DR with
status, top proven links, top gaps, and exact paths/lines.

## Explorer evidence (2026-08-09)

### Evidence table

| Signal family | Cadence / trigger | Transport | Current consumer | Provenance / gap |
|---|---|---|---|---|
| Host CPU/RAM/disk metrics | No documented active sampler/cadence in the allowed inventory. Netdata is the named host-monitoring backend, but its unit is inactive and port `19999` had no listener. | Intended route → `localhost:19999`; no live consumer link to GPTAdmin proven. | None proven. | `docs/inventory/ports.md:83`; `docs/inventory/services/netdata.md:7-12`; `docs/inventory/operational-exceptions.md:10`. Gap: no active source, threshold, event schema, or GPTAdmin delivery. |
| Failed services / host ingress state | Recovery watchdog state changes (`ha`, `100`, `failed`); exact timer cadence is not stated. `recovery-status` is a manual read-only snapshot. | Controller watchdog; optional Telegram through local proxy. | Telegram notification path only; GPTAdmin consumer not documented. | `infra/recovery.md:62-79`. Gap: no service-wide `systemctl --failed` producer or normalized event consumer. |
| Public endpoint availability | VPN2 oneshot timer hourly; aggregate DOWN/recovery after two consecutive results; stale alert after 2h15m telemetry absence. | Restricted SSH list pull from server-100 nginx state; JSONL under `/var/lib/external-site-monitor`; aggregate Telegram alerts. | Telegram only; GPTAdmin/notification-center link not proven. | `infra/recovery.md:269-283`. Gap: no documented webhook/MCP/API handoff or deduplication contract. |
| Error-level / keyword logs | No cadence/trigger or central collector documented. Fail2Ban jails watch nginx access/error logs and kernel log for port scans, but this is security enforcement, not a GPTAdmin incident feed. | Local log files / Fail2Ban jails. | Fail2Ban action; GPTAdmin consumer not proven. | `infra/security.md:3-13`. Gap: no error keyword extraction, cursor, event schema, or delivery path. |
| RAM/log pressure guard | Boot after 30s, then every 1 minute; trims >128 MiB files and reacts at ≥75% tmpfs usage. | systemd timer + local Bash; local HTTP probe `127.0.0.1:3129/`; may restart VPN service. | VPN Panel/systemd only; no alert delivery documented. | `automation/ram-log-guard/ram-log-guard.timer:4-8`; `automation/ram-log-guard/README.md:3-7`; `automation/ram-log-guard/ram_log_guard.sh:23-47`. Gap: restart is a side effect, no incident publication/dedup. |

### Real consumer/event-path findings

The inventory confirms a notification-center runtime on server-100 at
`127.0.0.1:8091` plus admin at `127.0.0.1:8092`, but does not document any
producer integration from host/service/log monitors into it
(`docs/inventory/sites/notify.md:7-16`). GPTAdmin is separately documented at
hub/web `127.0.0.1:22554/22555` and an MCP public route, with no health-event
consumer contract in this inventory (`docs/inventory/services/gptadmin.md:7-16`).
Therefore the shortest *documented* path is existing Telegram notification for
the recovery and external-site monitors; a GPTAdmin incident path remains
unproven.

### Synthetic canary

No safe synthetic incident/log canary entry point is documented in the allowed
inventory. `recovery-status` is explicitly a read-only status command
(`infra/recovery.md:68-79`), but forcing HA/100 would change routing and is not
an allowed canary. The monitor's HTTP probes are implementation details, not a
documented synthetic event injector.

### Result

Status: complete for bounded research. Proven sources are recovery watchdog,
hourly external-site monitor, one-minute RAM-log guard, Fail2Ban log watchers,
and the currently inactive Netdata package. The key missing piece is a live,
owned normalized event path into GPTAdmin (or notification center), including
host metric producer, log cursor/keyword policy, severity, deduplication key,
and consumer acknowledgement. No service state, secrets, configs, or task
files outside this report were changed.
