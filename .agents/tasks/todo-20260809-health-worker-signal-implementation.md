# Worker lane: health signal producer

Role: Worker
Status: todo
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

## Worker evidence log

Implementation slice completed:

- Added a new namespace package at `automation/`.
- Added `automation/health_incident_monitor/health_monitor.py` with:
  - `HealthEvent`, `HealthCollectionResult`, `VerificationResult`
  - `FixtureRunner`, `FixtureVerificationRunner`
  - `collect_health_signals(...)`
  - `verify_original_signal(...)`
- Added fixture-first test coverage in
  `automation/health-incident-monitor/test_health_monitor.py`.
- Added fixture data in `automation/health-incident-monitor/fixtures/degraded.json`.

Behavior proved by focused pytest run:

- one degraded fixture produced one deterministic event per degraded signal
  type (`cpu`, `service`, `log`)
- repeated observations preserved each event's `dedup_key`
- the event summary stays redacted and does not emit raw log payload text
- verification is tied to the same source identity and cannot be fabricated for
  a different source

Commands run:

- `pytest -q /home/admin/gptadmin/automation/health-incident-monitor/test_health_monitor.py`

Result:

- `3 passed in 0.03s`

Remaining assumptions / limits:

- The implementation is fixture-first and deterministic, but it is still a
  local producer only; no live systemd, disk, or log collection was wired in.
- I did not touch any runtime, recovery, nginx, or deployment files.
