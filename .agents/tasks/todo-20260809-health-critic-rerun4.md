# Fresh Critic gate 4: final adversarial health contract

Role: Critic
Status: done
Owner: fresh independent Critic gate 4
Parent task: work-20260809-health-monitoring-orchestration.md

Fresh read-only adversarial gate. Do not edit product code, secrets, runtime
state, or external systems; do not send Telegram. Append PASS or RETHINK with
exact reproductions.

Falsify only bounded selected paths: race two independent plan callbacks;
select before attachment; post heartbeat-only over HTTP and inspect status plus
event count; post useful progress and mismatched fingerprint verification;
send 10,000-character Hermes progress and resolution actors plus `token:value`;
issue Hub progress GET; inspect canary provenance and no external send.

## Critic result (bounded final pass, 2026-08-09)

### Verdict

`PASS` for this bounded Critic gate. No `QUESTIONS_FOR_L` remain for the
selected local contracts. This does not upgrade the disposable canary into
proof of real provider/model authorization, Agent-Herder/Hermes runtime
delivery, or external Telegram delivery.

### Decisive evidence

- **Independent plan race:** 20 fresh temporary SQLite databases were checked;
  two independent `NotificationCenter` instances raced distinct callbacks
  (`observe` versus `repair`) after the same three plans were attached. Every
  run produced exactly one accepted callback, one rejected callback, one row in
  `health_plan_selections`, and one `health.plan_selected` event. The rejected
  callback returned `ValidationError: health plan already selected`.
- **Selection ordering:** a selection before plan attachment was rejected with
  `ValidationError: unknown health plan`; no selection was accepted before the
  plans existed.
- **HTTP heartbeat boundary:** the real loopback
  `POST /v1/incidents/{incident_id}/health/progress` with
  `step=heartbeat`, empty `evidence_refs`, empty `progress_fingerprint`, and
  `heartbeat_at=1` returned HTTP `400` with
  `heartbeat-only health progress is not accepted`; the durable
  `health.progress` event count remained `0`. A useful receipt returned HTTP
  `200` with `useful_progress=true`.
- **Fingerprint guard:** useful progress returned HTTP `200`; an independent
  healthy verification with fingerprint `different-fp` was accepted as a
  receipt, but resolution returned HTTP `400` with
  `health verification receipt must match the original source fingerprint and
  remain independent`; the durable `health.resolved` count remained `0`.
- **Hermes boundary:** `record_health_progress` and
  `record_health_resolution` both rejected a 10,000-character `session_id`
  with `session_id must be an opaque bounded identifier`. A progress
  `step=token:value` and resolution `source_id=token:value` were both rejected
  before emission with the bounded-identifier errors.
- **Hub progress:** the focused Go tests passed. The exercised GET
  `/webhook-jobs/{job_id}/progress` returned `405`; useful POST progress was
  `202`, a repeated receipt was non-useful, and a terminal-job update was
  `409`. The read-only job GET returned `200` with stored receipts.
- **Canary provenance:** `tests/e2e/health_incident_vertical_canary.py` clearly
  labels the health producer and NoticePlace HTTP as `real_local`, GPTAdmin,
  OmniRoute, and Agent-Herder as `fake`, Telegram as `fake_no_send`, and
  `external_sends` as `False`. This is honest fixture provenance and confirms
  no external send claim.

### Excluded hypotheses and boundaries

No production restart/deploy, secret inspection/change, runtime mutation,
Telegram send, provider/model authorization check, or external delivery was
performed. Those remain outside this read-only gate and outside its PASS.
