# Worker lane: NoticePlace health incident workflow

Role: Worker
Status: done
Owner: Worker
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Implement the Normal-plan NoticePlace side of the immutable acceptance contract:
health event intake, exactly-three plan attachment, signed Telegram plan
selection, useful-progress receipts, independent source verification, and
resolution only after verification. Reuse the existing SQLite/events/audit and
delivery seams without a new schema migration.

## Allowed write set

- `/home/admin/agents-projects/noticeplace/notification_center/core.py`
- `/home/admin/agents-projects/noticeplace/notification_center/http_api.py`
- `/home/admin/agents-projects/noticeplace/notification_center/telegram_interactions.py`
- `/home/admin/agents-projects/noticeplace/notification_center/gptadmin_agent.py`
- New `/home/admin/agents-projects/noticeplace/notification_center/health_workflow.py`
- New or focused tests under `/home/admin/agents-projects/noticeplace/tests/`
- Do not edit any other paths, runtime config, secrets, service state, or
  production delivery.

## Required behavior

- Preserve existing notify.event.v1 compatibility and idempotency.
- Store health updates in existing event/audit tables, linked to the original
  incident; do not create a second incident for plan/progress/verification
  updates.
- Validate exactly three bounded plans with stable plan IDs.
- Telegram callbacks must be signed and allowlisted; selection is durable and
  idempotent, and cannot select a fourth/unknown plan.
- Useful progress means a step/evidence/fingerprint change; heartbeat alone is
  not useful progress.
- Resolution must reject until the original source has a matching independent
  healthy verification receipt; terminal agent status is insufficient.
- Keep trace/correlation fields opaque and bounded; never persist raw secrets or
  unbounded log payloads.

## Acceptance check

Write focused red tests first, then prove green tests for event/update
idempotency, three-plan validation, Telegram plan selection, useful-progress
classification, verification-gated resolution, and bounded receipts. Preserve
all existing tests.

## Budget and stop conditions

- Model: gpt-5.4-mini, medium reasoning
- Active minutes: 75 / 150 / 300
- Relative cost: medium/high; core state-machine integration
- Stop if a schema migration, external send, or unrelated owner path is
  required; return NEEDS_REDECOMPOSITION with the exact decision.

## Report contract

Append detailed evidence and changed paths to this task file. Return L only
TL;DR, focused/full test commands, and unresolved integration assumptions.
Do not stage or commit.

## Worker evidence log

- 2026-08-09: Added focused health-workflow regression coverage in
  `/home/admin/agents-projects/noticeplace/tests/test_health_workflow.py`
  for intake plan attachment, signed Telegram plan callbacks, useful-progress
  receipts, verification-gated resolution, and bounded receipts.
- Confirmed by direct snippet that `TelegramHealthPlanCodec` callbacks decode
  and `TelegramInteractionPoller` can record a health plan selection when the
  core path is reached.
- Ran: `python -m pytest /home/admin/agents-projects/noticeplace/tests/test_health_workflow.py -q`
  from `/home/admin/agents-projects/noticeplace`.
- Current blocker: `notification_center/core.py`,
  `notification_center/http_api.py`, and `notification_center/telegram_interactions.py`
  are being modified concurrently in the shared worktree and keep refreshing
  inside the 5-minute hands-off window, so I did not patch them further.
- Remaining failing points observed in tests:
  - `TelegramSender` still emits health reply_markup with the wrong codec path,
    so callback data is not health-signed.
  - `record_health_progress` is classifying duplicate progress as useful
    because the latest-progress lookup and event-type naming are still out of
    sync in the active core revision.
  - Intake still needs the final wiring to guarantee exactly-three plans are
    attached automatically on health intake.

## Final integration evidence

- Reconciled the write-set changes: health intake no longer attaches generic
  plans before diagnosis; exactly three plans are attached through the
  post-diagnosis workflow and scheduled for one durable plan delivery.
- Added loopback HTTP workflow routes for health signals, plans, progress,
  verification, and resolution. Resolution stores elapsed milliseconds and
  bounded trace references and checks the original source identity.
- Added bounded health metadata to the GPTAdmin agent-job payload and an
  explicit `health` Telegram topic mode that must be enabled by operator
  configuration.
- Focused verification: `python3 -m pytest tests/test_health_workflow.py
  tests/test_http_api.py tests/test_gptadmin_agent.py -q` → `34 passed`.
- Full NoticePlace suite: `135 passed, 1 failed`; the one failure is the
  pre-existing/unselected Telegram policy regression recorded in
  `todo-20260809-noticeplace-telegram-controls-regression.md`.
