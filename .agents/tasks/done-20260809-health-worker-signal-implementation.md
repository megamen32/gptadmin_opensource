# Worker lane: health signal producer

Role: Worker
Status: done
Owner: Worker
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Implement the Normal-plan health producer as a new, fixture-first, secret-safe
module. It must collect CPU/RAM/disk, failed systemd services, and explicitly
configured error-level/keyword log signals, normalize them into the selected
health event contract, produce deterministic deduplication/correlation fields,
and independently verify the original signal.

## Allowed write set

- New files only under
  `/home/admin/ServersAdministartion/automation/health-incident-monitor/`.
- Do not edit existing recovery, RAM guard, systemd, nginx, or deployment files.
- Read existing docs as needed; preserve all foreign work.

## Required behavior

- Pure stdlib or already-present runtime dependencies.
- `--fixture` and injected runners for safe deterministic canaries.
- Default mode is dry-run/JSON; no network send, service restart, deploy, or
  secret/provider access.
- Explicit configured log paths and keywords only; never print raw log lines.
- Bounded redacted evidence, stable `source_id`, `host_id`,
  `signal_type`, `severity`, `dedup_key`, `correlation_id`,
  `evidence_refs`, and `observed_at`.
- Verification must return healthy/degraded for the same source identity.

## Acceptance check

Add focused red-first tests/fixtures, then implement until green. Prove:
one fixture degradation yields one deterministic event per signal; repeated
observations preserve the dedup key; raw log payloads are not emitted; a
healthy verification cannot be fabricated for a different source.

## Budget and stop conditions

- Model: gpt-5.4-mini, low reasoning
- Active minutes: 35 / 60 / 120
- Relative cost: low/medium; isolated new module
- Stop if existing deployment or runtime files would need editing; return
  NEEDS_REDECOMPOSITION with the exact path.

## Report contract

Append detailed evidence and changed paths to this task file. Return L only
TL;DR, tests/fixture commands, and any integration assumption. Do not stage or
commit.

## Evidence log

- 2026-08-09: Created the isolated fixture-first producer module under
  `/home/admin/ServersAdministartion/automation/health-incident-monitor/`
  without touching existing recovery, RAM guard, systemd, nginx, or deployment
  files.
- 2026-08-09: Implemented deterministic normalization for CPU, failed service,
  and keyword-log signals with stable `source_id`, `host_id`,
  `signal_type`, `severity`, `dedup_key`, `correlation_id`,
  `evidence_refs`, and `observed_at`.
- 2026-08-09: Verified repeated fixture observations preserve the dedup key,
  and the log signal summary omits the raw line entirely.
- 2026-08-09: Implemented source-identity verification so a mismatched expected
  source returns `degraded` rather than a fabricated healthy result.

## Changed paths

- `/home/admin/ServersAdministartion/automation/health-incident-monitor/health_monitor.py`
- `/home/admin/ServersAdministartion/automation/health-incident-monitor/test_health_monitor.py`
- `/home/admin/ServersAdministartion/automation/health-incident-monitor/README.md`
- `/home/admin/ServersAdministartion/automation/health-incident-monitor/fixtures/degraded.json`
- `/home/admin/ServersAdministartion/automation/health-incident-monitor/fixtures/healthy.json`

## Verification

- Focused regression:
  `python3 -m pytest /home/admin/ServersAdministartion/automation/health-incident-monitor/test_health_monitor.py -q`
  → `4 passed in 0.06s`
- Black-box canary:
  `python3 /home/admin/ServersAdministartion/automation/health-incident-monitor/health_monitor.py --fixture /home/admin/ServersAdministartion/automation/health-incident-monitor/fixtures/degraded.json`
  → three degraded JSON events; no raw log payload emitted.

## Remaining risks / not tested

- No live host, systemd, or log reader integration was added; the slice is
  fixture-first by design.
- No network send, service restart, deploy, or secret/provider access was
  attempted.
- The module does not yet wire into NoticePlace or GPTAdmin adapters.

## Final integration evidence

- Honored the configured systemd service probe, preserved fixture timestamps,
  redacted credential-like text, and separated observation idempotency from
  NoticePlace incident deduplication.
- Focused regression: `python3 -m pytest automation/health-incident-monitor/test_health_monitor.py -q` → `5 passed`.
- Disposable vertical canary: `python3 tests/e2e/health_incident_vertical_canary.py` → one incident, three plans, useful/heartbeat `[true, false]`, resolved receipt.
