# Final review brief: health delivery single-slot

Status: work
Role: fresh independent Reviewer/Critic, read-only
Parent task: `/home/admin/gptadmin/.agents/tasks/work-20260810-health-runtime-continuation.md`

## Business contract

Health degradation must reach GPTAdmin diagnosis, exactly three validated plans,
one user-facing Telegram Health card with three signed plan buttons, explicit
user selection, Hermes remediation, useful progress, independent verification,
and resolved receipt with elapsed time and trace IDs. No Telegram/Hermes action
is authorized by this review brief; only source review is requested.

## Review scope

Review only the current uncommitted NoticePlace diff in:

- `/home/admin/agents-projects/noticeplace/notification_center/core.py`
- `/home/admin/agents-projects/noticeplace/notification_center/http_api.py`
- `/home/admin/agents-projects/noticeplace/tests/test_delivery_worker.py`

Ignore the older unrelated `package.json` diff and all untracked files.

## Current evidence

- The live canary exposed a real P0: a Telegram initial Health card sent before
  plans and a claimed initial could race with `health.plans`, producing doubles.
- Health mode is disabled again; no new Telegram/Hermes egress is allowed while
  reviewing. The live queue is quarantined conceptually and must be reconciled
  after deploy before re-enable.
- Red then green coverage exists for: no initial health slot, plan gate,
  queued legacy replacement, claimed legacy interleaving, persisted synthetic
  marker mismatch, and custom Telegram consumer plan gate.
- Current scoped suite: `95 passed in 42.33s`; py_compile and diff-check passed.

## Revision 2 addendum

- The review findings about sent legacy rows and custom consumers are now
  covered by red-then-green tests. Plan attachment coalesces every active
  `telegram.main`/`telegram.consumer:*` initial or health-plan row into one
  canonical slot; an already sent row never creates another row. Custom
  consumer policy stages are materialized again, preserving non-Telegram
  Matrix/phone escalation.
- Persisted original-event synthetic detection is used even when the plan
  callback supplies a missing or mismatched correlation ID.
- Current scoped suite including `tests/test_consumer_policy.py`: `105 passed
  in 45.50s`. Re-review the actual current diff, not the earlier revision.

## Revision 3 addendum

- If any active Telegram row is already `sent`, the plan-attachment path now
  cancels queued duplicate rows and schedules nothing new. This is covered by
  a red-then-green regression with a pre-existing queued `health.plans` row.
- Coalescing now searches every `telegram.%` channel, and the health gate also
  covers generic `telegram.message/call` channels. Custom policy stages are
  still materialized, including Matrix/phone escalation.
- Current scoped suite is `106 passed in 47.09s`; re-review the actual current
  working diff, including these sent-row and generic-channel changes.

## Questions that must be answered

1. Is there a linearizable invariant preventing a claimed legacy initial from
   being superseded into a duplicate? Specifically inspect the branch where
   `health.plans_attached` sees `status=claimed`, worker claim/send, and the
   branch where it sees `status=queued`.
2. Do all Telegram channels, including `telegram.consumer:*`, enforce the
   exactly-three-plans and persisted-synthetic guards?
3. Does a new health incident create exactly one sendable Telegram delivery,
   only after plans attach, without breaking diagnosis/remediation deliveries?
4. Identify any P0/P1 issue, missing regression, or deployment-risk assumption.

## Required report

Return exactly one of `PASS` or `CHANGES_REQUIRED`, with concise evidence,
specific file/line findings, and explicit statement that no production or
external state was changed.

## Critic receipt (2026-08-11 01:06 MSK)

- Verdict: `CHANGES_REQUIRED`.
- Scope audited: only the current uncommitted NoticePlace diff in
  `notification_center/core.py`, `notification_center/http_api.py`, and
  `tests/test_delivery_worker.py`. The unrelated `package.json` diff and
  untracked files were excluded and left untouched.
- Source evidence: `pytest -q tests/test_delivery_worker.py` passed `22` tests;
  the broader selected delivery/core/health/HTTP/Telegram/admin/consumer slice
  completed with its existing green output. Selected `git diff --check` and
  `py_compile` were clean. No production or external state was changed.

- P1 — sent legacy initial can still create a second Health card:
  `notification_center/core.py:2151-2170` only cancels an initial row when its
  status is `queued`, and only suppresses the new `health.plans` row when the
  initial status is `claimed`. If the initial row is already `sent` (the live
  incident class that motivated this review), the code schedules a new
  `telegram.main:health.plans` delivery. A deterministic local no-network
  reproduction attached one plan bundle while a legacy initial was claimed,
  delivered that claimed row once, attached the same bundle under a second
  idempotency key, and then delivered the new row: fake Telegram observed
  `sent_count=2` with both stable keys ending in `:initial` and `:health.plans`,
  and both rows were `sent`. The current claimed-race test at
  `tests/test_delivery_worker.py:132-166` does not cover the post-send/duplicate
  plan-attachment state. The state machine needs a durable single-card rule
  for sent/legacy rows (plus an explicit migration outcome for an already sent
  card), with a red regression for this reproduction.

- P1 — legacy custom Telegram rows can produce a second card:
  `notification_center/core.py:2152-2170` reconciles only
  `incident_id:telegram.main:initial`; it does not cancel or coalesce an
  existing `telegram.consumer:*:initial` row. The worker gate in
  `notification_center/http_api.py:407-439` correctly applies the synthetic
  and exact-three-plan checks to that channel, but therefore sends the valid
  custom row as well as the newly scheduled main `health.plans` row. A local
  no-network reproduction with one legacy custom row and three persisted plans
  observed two sends, channels `telegram.consumer:custom` and `telegram.main`,
  with both rows `sent`. The added test at
  `tests/test_delivery_worker.py:192-218` proves only that the custom row waits
  before plans; it does not prove single-slot behavior after plans attach. The
  queue migration must enumerate and explicitly reconcile all Telegram health
  channels, and a red regression must cover custom-row plus main-plan
  coalescing.

- Q1/Q2/Q3 result: the plan gate and persisted-source synthetic guard are
  present for both Telegram channel families, and a new ordinary health event
  starts with no Telegram initial and schedules one main `health.plans` row
  after a valid attachment. Those local invariants do not cover pre-existing
  sent/custom rows, so they do not establish the business single-card
  invariant or a linearizable migration guarantee for the real queue. The
  claimed branch is serialized with the plan-attachment transaction, but
  `delivery_is_claimed()` at `notification_center/core.py:857-861` is only a
  status read before the external send; it is not a durable send reservation.
  Any claimed-row cancellation path must remain explicitly excluded or be
  covered by a compare-and-set/send-ownership test.

- Minimum proof to proceed: add red-then-green tests for (1) a sent legacy
  initial followed by a plan attachment/retry and (2) a legacy
  `telegram.consumer:*` initial alongside the generated main plan delivery;
  implement and review the corresponding single-slot/migration reconciliation;
  then run the required read-only queue audit proving no old initial/custom or
  synthetic row can drain beside the one canary. This review authorizes no
  Telegram send, Hermes egress, deployment, restart, or other external action.

## Reviewer evidence — 2026-08-11

Scope reviewed: only the selected uncommitted diff in NoticePlace `notification_center/core.py`, `notification_center/http_api.py`, and `tests/test_delivery_worker.py`. The unrelated `package.json` diff and all untracked files were not inspected for acceptance.

Checks run:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_consumer_policy.py`: 50 passed.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- `git diff --check` for the selected paths: passed.
- Temporary-database reproductions showed an already `sent` legacy main delivery leaves a queued `health.plans` delivery, and a queued `telegram.consumer:*:initial` remains alongside a scheduled `telegram.main:health.plans` delivery.

Findings:

1. P0 — `notification_center/core.py:2151-2170` does not protect the exact-one-card invariant for a legacy initial already in `sent` state. `initial_claimed` is true only for `claimed`; therefore a pre-deploy initial that already sent without plans causes a second `telegram.main:health.plans` delivery. This matches the live P0 described in the brief and requires explicit durable reconciliation/message identity before Health mode is re-enabled.

2. P0 — `notification_center/core.py:2152-2170` reconciles only `telegram.main:initial`. A queued or claimed `telegram.consumer:*:initial` is neither cancelled nor selected as the canonical plan delivery; the code schedules `telegram.main:health.plans` as well. Once plans exist, `notification_center/http_api.py:407-437` correctly gates the custom channel, so both deliveries are sendable and can produce two Health cards. The new custom-channel test only proves the guard and misses this replacement/routing race.

3. P1 — `notification_center/core.py:727-733` returns before materializing every custom health consumer policy, including its Telegram target and Matrix/phone escalation. Plans later route to `telegram.main` at `core.py:2169-2170`, losing the consumer target. Diagnosis/remediation agent deliveries remain separately scheduled by `core.py:651-653` and plan selection, but custom consumer delivery/escalation is regressed.

Question answers: the default `queued` versus `claimed` branch is serialized by the SQLite transaction: queued is cancelled and replaced; claimed is not superseded, so the specific observed race is covered. However, the check at `core.py:857-861` is only a pre-send read and is not an atomic claim-to-external-send operation; the existing at-least-once lease remains an unverified duplicate assumption. Both default and `telegram.consumer:*` worker paths enforce the exact-three-plan and persisted-original synthetic guards at `http_api.py:407-437`, but the scheduling/reconciliation path does not cover all channels.

Result: `CHANGES_REQUIRED`.

## Reviewer evidence — 2026-08-11 current independent pass

Scope reviewed: only the current uncommitted diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated `package.json`
diff and all untracked files were left untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `121 passed in 54.77s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- No-network temporary-database reproductions showed two compliant sent
  Telegram rows remain sent and `center.health()` still returns `status=ok`;
  a generic health policy chain `telegram -> telegram -> matrix` emitted only
  the first Telegram row and never materialized the Matrix successor.

Decisive findings:

1. P0 — multiple already-sent Health cards still pass the readiness surface.
   `core.py:2225-2244` audits `len(sent_rows) > 1` and cancels only
   non-sent rows; it deliberately leaves every sent row in `sent`. The
   selected source has no message-level edit/replacement or re-enable gate,
   and `core.py:2107` computes readiness without the duplicate-sent audit.
   A temporary incident with `telegram.main` and
   `telegram.consumer:custom`, both carrying the exact three plan IDs and
   signed-button receipt metadata, remained two `sent` rows after valid plan
   attachment while `health()` returned `ok`. This violates the exact-one
   user-facing card contract. Add a red-then-green readiness/reconciliation
   regression and require an explicit message-level repair/audit outcome
   before Health can be re-enabled.

2. P1 — suppressing a Health Telegram repeat can drop a downstream
   non-Telegram successor. In `core.py:920-935`, when a repeatable Telegram
   step is suppressed, the code inspects only its immediate successor and
   schedules it only when that successor is non-Telegram. For a valid generic
   policy chain `telegram root -> telegram successor -> matrix/phone`, the
   immediate Telegram successor is skipped and the Matrix/phone stage is
   never traversed or materialized. The existing regression covers only a
   direct Telegram-to-Matrix successor at
   `tests/test_delivery_worker.py:664-716`. Traverse the suppressed Telegram
   chain to the first configured non-Telegram stage (with a regression for a
   nested chain), or explicitly constrain the supported policy shape.

Unverified deployment assumption: the durable `sending` reservation and
lease-generation CAS prevent the covered same-process stale worker duplicate,
but a process crash after Bot API acceptance can leave `sending` stranded;
the current readiness gate correctly degrades for that state, while no queue
or Bot API audit was authorized or performed in this review.

Result: `CHANGES_REQUIRED`.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; runtime checks used repository tests, fake
adapters, and temporary databases only.

## Critic receipt — 2026-08-11 02:44 MSK

Verdict: `CHANGES_REQUIRED`.

Scope reviewed: only the current uncommitted diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated
`package.json` diff and all untracked files were excluded and untouched.

Fresh checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `121 passed in 54.72s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- Temporary-database/fake-adapter reproductions were read-only with respect to production and external systems.

Decisive findings:

1. P0 — multiple already-sent Health cards still yield a ready state. The
   `len(sent_rows) > 1` branch at `core.py:2225-2244` emits
   `health.multiple_sent_delivery_reconciliation_required` but retains every
   sent row. `health()` at `core.py:2107-2117` only blocks on `sending` and
   `uncertain`, so a temporary database containing two sent Telegram rows with
   exact three-plan/signed-button receipts returned `status=ok` while both rows
   remained `sent`. A route using this readiness result can re-enable with two
   user-visible Health cards, violating the exact-one-card contract. The added
   regression checks the audit marker but not readiness or a message-level
   reconciliation outcome.

2. P0 — a noncompliant pre-deploy sent card is retained without a user-facing
   migration outcome. `core.py:2305-2324` records
   `health.legacy_card_migration_required`, cancels other active rows, and
   deliberately leaves the old `sent` row as the only card. The selected
   `TelegramSender` path at `http_api.py:218-292` only creates a new
   `sendMessage`; it has no edit/replacement operation for the retained
   message. A temporary database with a sent legacy row lacking
   `health_plan_ids`/button metadata returned `status=ok` and no plan delivery,
   leaving no selectable three-plan workflow. This requires either a durable
   message migration/edit or an enforced queue/message audit gate before Health
   re-enable; the current source and tests provide neither.

3. P1 — a post-send completion failure can requeue the same external delivery.
   `complete_delivery()` updates the row before scheduling a policy successor
   at `core.py:909-953`; if successor persistence raises, the transaction
   rolls back to `sending`. The outer `DeliveryWorker` handler at
   `http_api.py:580-588` then calls `complete_delivery(..., "retry")`, and
   `core.py:900-912` accepts that outcome for `status='sending'`. A no-network
   reproduction with a successful fake Telegram send and an injected Matrix
   successor persistence failure observed `sends=2` after reclaim/retry and
   the delivery returned to `queued` on both attempts. Existing post-send
   coverage patches `_after_telegram_delivery` after the row is already
   `sent`; it does not cover this transactional rollback path.

Question answers: the exact-three-plan gate, persisted synthetic guard,
generic/custom Telegram channel gate, same-process lease-generation check,
uncertain-send blocker, in-flight no-cancel behavior, signed-codec fail-closed
path, generic Telegram repeat suppression, and Matrix/phone successor
preservation are covered and behaved as expected. They do not repair the
three blockers above. `QUESTIONS_FOR_L`: none; each blocker is reproducible
from the selected source and temporary databases.


- Route A: add red-then-green regressions for multiple sent rows plus
  readiness, noncompliant sent-card migration, and successor-persistence
  failure; make reconciliation state block re-enable and make any post-send
  completion failure terminal/uncertain rather than retryable.
- Route B: keep Health disabled and require a read-only queue plus Bot API
  message audit that proves exactly one compliant card per incident, while
  adding an external durable send/reconciliation gate for the successor-failure
  boundary before claiming the one-card contract.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; all runtime checks used repository code,
temporary databases, and fake adapters only.

## Reviewer evidence — 2026-08-11 current-snapshot final pass

Scope reviewed: only the selected uncommitted NoticePlace diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated
`package.json` diff and all untracked files were left untouched. The selected
files were stable during this pass and remained read-only.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `121 passed in 50.94s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- A fresh temporary-database/no-network reproduction created one compliant
  `telegram.main` sent row and one compliant
  `telegram.consumer:custom` sent row. After valid plan attachment, both
  remained `sent`, while `center.health()` returned `status=ok`,
  `sending_deliveries=0`, and `uncertain_deliveries=0`.
- A single sent legacy row without receipt/button metadata still remained the
  sole sent row after plan attachment; the code emitted only
  `health.legacy_card_migration_required` and created no replacement/edit.

Decisive findings:

1. P0 — multiple already-sent Health cards are audited but not quarantined
   from readiness or repaired. `core.py:2225-2244` records
   `health.multiple_sent_delivery_reconciliation_required` and cancels only
   non-sent rows, leaving every sent row active. `core.py:2091-2117` computes
   readiness only from storage, dispatcher heartbeat, `sending`, and
   `uncertain`; it does not account for multiple sent Health rows. The fresh
   reproduction therefore left two user-visible canonical candidates while
   the public health surface reported `ok`. The existing regression at
   `tests/test_delivery_worker.py:537-575` asserts the audit marker, not the
   exact-one-card/readiness invariant. The smallest safe fix is a durable
   duplicate-state gate/quarantine that prevents Health re-enable until
   exactly one compliant card is reconciled, or a message-level migration
   outcome that removes the duplicate.

2. P0 — a pre-deploy sent legacy card without the three signed buttons has no
   user-facing migration outcome. `core.py:2305-2324` retains the old sent row
   and records the migration-required audit, while the selected sender code at
   `http_api.py:245-289` only creates a new `sendMessage` and has no edit or
   replacement operation for that existing message. `core.py:2091-2117` still
   reports readiness `ok` once no send is in flight. Re-enable therefore can
   leave the only retained Health card without selectable plans and stall the
   workflow. Before re-enable, require either durable message migration/edit
   support or a read-only queue plus Bot API audit proving every retained sent
   card is already compliant; that audit was not performed by this review.

Question answers:

- Q1: the lone queued-versus-claimed path is serialized and exact lease
  generation checks prevent the covered stale same-process worker. Mixed
  already-sent rows still do not produce one durable canonical outcome.
- Q2: the persisted synthetic-source guard and exact-three-plan gate cover
  `telegram.main`, custom, and generic `telegram.*` worker paths. The
  coalescer does not make multiple sent history or an old noncompliant sent
  message safe for re-enable.
- Q3: a fresh built-in Health event creates no initial Telegram row and a
  valid plan attachment releases one canonical slot when no legacy sent or
  in-flight reconciliation state exists. The two findings prevent acceptance
  of the full single-card/user-selection contract.
- Q4: the two P0 findings are release blockers. The source-level reservation
  and `sending`/`uncertain` readiness behavior remain an unverified
  deployment assumption until the required read-only queue/message audit and
  reconciliation are completed.

Result: `CHANGES_REQUIRED`.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; runtime checks used repository code and
temporary databases only.

Minimum proof to proceed, with two viable routes:


No Telegram send, Hermes egress, deployment, restart, queue mutation, or
other production/external action was authorized or performed by this review.

## Reviewer evidence — 2026-08-11 current final re-review

Scope reviewed: only the current uncommitted NoticePlace diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated
`package.json` diff and all untracked files were left untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `125 passed in 57.20s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- The selected paths remained modified but stable; no source file was changed during this review.
- Temporary-database/fake-adapter reproductions used no network or production state.

Decisive findings:

1. P0 — readiness can report `ok` while a sent Health card still lacks the
   exact attached plan-set proof and its in-place migration is only queued.
   `core.py:2130-2154` checks only `message_id`, `health_button_count`, and
   `health_signed_callback_count`; it does not require `health_plan_ids` to
   equal the latest three attached plan IDs. `core.py:2360-2376` correctly
   detects the mismatch and schedules `telegram.edit`, but that queued edit is
   not a reconciliation blocker. A temporary DB with one sent row containing
   `message_id` and three-button counts but no `health_plan_ids`, followed by a
   valid three-plan attachment, produced a sent legacy row plus queued edit and
   `center.health()` returned `status=ok` with
   `reconciliation_required=0`. Re-enable can therefore occur before the old
   card is proven or migrated to the exact three signed choices. The smallest
   fix is to make readiness require exact persisted plan IDs (and no pending
   migration) per Health incident, with a red regression; the current
   compliant-only test at `tests/test_delivery_worker.py:253-287` does not
   cover this mismatch.

2. P1 — a post-send successor-persistence failure can requeue the same
   external Telegram delivery. `core.py:911-914` changes `sending` to
   `queued` for retry, while `core.py:915-955` persists policy successors in
   the same transaction as the `sent` transition. If successor persistence
   raises, that transaction rolls back to `sending`; the outer handler at
   `http_api.py:680-688` then invokes the retry path, which is explicitly
   accepted for `sending` at `core.py:900-912`. A temporary generic Health
   policy with a Telegram root and Matrix successor, an injected
   `_schedule_delivery` failure, and a fake Telegram adapter observed one
   send, `queued` attempt 1, then two sends after the queued retry. The
   existing regression at `tests/test_delivery_worker.py:509-525` only raises
   from `_after_telegram_delivery` after the row is already `sent`, so it does
   not cover transactional rollback after the external send. The smallest
   fix is to make post-send completion failures terminal/uncertain without
   retrying the external delivery, or persist successor work outside the
   send-finalization rollback boundary, plus a red regression.

3. P1 — generic Health repeat suppression drops a configured non-Telegram
   successor when an intermediate Telegram stage is present. In
   `core.py:924-937`, the code inspects only the immediate successor while
   suppressing a Telegram repeat; if that successor is also Telegram, it
   schedules nothing and never traverses to a later Matrix/phone stage. A
   temporary policy `telegram(root, max_repeats=2) -> telegram(successor) ->
   matrix` sent the root once and materialized no Matrix row. The regression
   at `tests/test_delivery_worker.py:720-772` covers only a direct
   Telegram-to-Matrix successor. Traverse to the first configured
   non-Telegram stage with cycle protection, or explicitly reject/constrain
   nested Telegram policy chains.

Question answers:

- Q1: the claimed-versus-plan-attachment path is serialized by the shared
  center lock, and exact `claimed_at`/`attempt` reservation prevents the
  covered stale generation from sending. It is not sufficient for the
  post-send completion failure above.
- Q2: the worker branch at `http_api.py:547-590` covers `telegram.main`,
  `telegram.consumer:*`, and generic `telegram.*` sends with persisted
  synthetic and exact-three-plan guards; `telegram.edit` separately validates
  the signed plan keyboard. Readiness still fails to verify the exact plan IDs.
- Q3: a fresh built-in Health event creates no Telegram initial and a valid
  plan attachment releases one canonical slot in the covered queue states;
  the readiness bypass and post-send retry path prevent acceptance of the
  full exactly-one-card contract. Health phone pre-call context is suppressed
  at `http_api.py:483-487`.
- Q4: findings 1-2 are release blockers; finding 3 is a scoped escalation
  regression. `QUESTIONS_FOR_L`: none.

Result: `CHANGES_REQUIRED`.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed. All runtime checks used repository tests,
temporary SQLite databases, and fake adapters only.

## Critic receipt — 2026-08-11 current independent final pass

Verdict: `CHANGES_REQUIRED`.

Scope reviewed: only the current uncommitted diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated
`package.json` diff and all untracked files were excluded and untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `125 passed in 53.02s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- No-network temporary SQLite/fake-adapter reproductions were read-only with
  respect to production and external systems.

Decisive findings:

1. P0 — readiness accepts a noncompliant sent Health card and a pending
   migration. `core.py:2146-2152` checks only `message_id`,
   `health_button_count`, and `health_signed_callback_count`; it never
   requires `health_plan_ids` to exist, contain exactly three IDs, or match the
   attached plan set. A temporary incident with a sent card carrying
   `health_plan_ids=["old-a", "old-b", "old-c"]` and three signed-button
   counters, followed by valid new plan attachment, left the source row
   `sent` plus `telegram.edit` `queued` while `center.health()` returned
   `status=ok`, `reconciliation_required=0`. The same false-ready result
   occurs when `health_plan_ids` is absent. This can permit Health re-enable
   while the only user-facing card has the wrong or no selectable plan set.
   Add a red-then-green readiness regression requiring exact plan-ID proof and
   blocking until the edit/migration is complete.

2. P0 — a post-send successor persistence failure still requeues an external
   Health send. `core.py:914-955` changes the row to `sent` and then persists a
   policy successor in the same transaction. If successor persistence raises,
   the transaction rolls the row back to `sending`; the outer handler at
   `http_api.py:680-689` then accepts `retry` for that sending generation and
   changes it to `queued`. A temporary generic Health policy
   `telegram.message -> matrix.call` with valid plans, a fake Telegram send,
   and an injected successor persistence failure observed the first send leave
   the row `queued`; reclaiming it and running the worker again produced
   `sends=2`. The existing post-send regression patches
   `_after_telegram_delivery` after the row is already terminal and does not
   exercise this rollback boundary. Make the post-send outcome terminal or
   uncertain without retrying, and add the injected successor-failure
   red-then-green regression.

3. P1 — nested Telegram repeat suppression drops a configured non-Telegram
   successor. In `core.py:924-937`, when a Health Telegram repeat would be
   skipped, only the immediate successor is inspected; if it is another
   Telegram stage, traversal stops. A supported policy chain
   `telegram.message(root, max_repeats=2) -> telegram.message(mid) ->
   matrix.call` sent the root once and left no Matrix row. The current test at
   `tests/test_delivery_worker.py:720-772` covers only a direct
   Telegram-to-Matrix successor. Traverse to the first configured
   non-Telegram stage, or explicitly reject/constrain nested Telegram chains
   before claiming Matrix/phone escalation preservation.

4. P1 — repeated plan attachment can cancel the only queued legacy edit and
   fail to recreate it. In `core.py:2360-2376`, a noncompliant sent row with a
   message identity schedules `telegram.edit`; a second distinct
   `health.plans_attached` event sees the queued edit as an active duplicate,
   cancels it, and calls `_schedule_delivery` again. Because
   `_schedule_delivery` at `core.py:718-724` returns the existing delivery ID
   without reopening cancelled rows, the migration remains `cancelled` and no
   edit can drain. A temporary SQLite reproduction produced exactly
   `telegram.main=sent, telegram.edit=cancelled` after the second attachment.
   Make attachment reconciliation idempotently revive/reuse a cancelled
   migration only when safe, or test and gate repeated attachment events.

Question answers: the current covered claimed/queued coalescing, generic
Telegram plan gate, persisted synthetic guard, stale lease CAS, in-flight
no-cancel path, and phone pre-call suppression behaved as intended. They do
not establish the readiness, post-send transaction, nested successor, or
repeat-migration invariants above. `QUESTIONS_FOR_L`: none; every blocker is
reproducible from the selected source with temporary state.

Minimum proof to proceed, with two viable routes:

- Route A: add the four red-then-green regressions, fail readiness on exact
  plan-ID/receipt mismatch and pending migration, make post-send successor
  failure terminal/uncertain, and traverse or constrain nested successors;
  then rerun the selected suite and the quarantined queue/message audit.
- Route B: leave Health disabled and require a read-only queue plus Bot API
  audit proving exactly one compliant message per incident, while separately
  gating generic policy shapes and the successor-persistence failure domain
  before any re-enable.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; all behavioral checks used temporary SQLite
databases and fake adapters only.

No production, Telegram, Hermes, database, queue, or other external state was changed; all runtime checks used the repository and temporary test databases only.

## Reviewer evidence — 2026-08-11 Revision 2 final review

Scope reviewed: only the selected uncommitted NoticePlace diff in
`notification_center/core.py`, `notification_center/http_api.py`, and
`tests/test_delivery_worker.py`. The unrelated `package.json` diff and all
untracked files were left untouched.

Checks run:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_consumer_policy.py`: `52 passed in 17.98s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- Read-only temporary-database/no-network reproductions covered legacy queue
  states and lease ownership; no production, Telegram, Hermes, database, or
  queue state was changed.

Findings:

1. P0 — `notification_center/core.py:2158-2183` still permits two active
   canonical candidates in the migration state the brief requires us to
   reconcile. When a legacy `telegram.main:initial` is `claimed` and an old
   `telegram.main:health.plans` row is already `queued`, `plan_rows` selects
   the queued plan row, but the claimed initial is deliberately not cancelled.
   Both rows then pass `notification_center/http_api.py:407-439` and send. A
   deterministic local reproduction observed two fake Telegram sends and both
   rows reached `sent`. The claimed/queued regression test at
   `tests/test_delivery_worker.py:132-166` has no pre-existing plan row and
   therefore does not cover this state.

2. P1 — `notification_center/core.py:858-862` and
   `notification_center/http_api.py:409-439` do not establish claim ownership
   across the external send. `delivery_is_claimed()` checks only `status`, not
   the worker's `attempt`/`claimed_at` generation. In a no-network reentrant
   reproduction, a first worker entered `send`, a second worker reclaimed the
   expired lease, and both workers passed the status check and sent the same
   Health card. This leaves the documented at-least-once lease as an
   unverified duplicate path for a contract that requires one card; add a
   compare-and-set/send-ownership regression or an explicit durable migration
   and idempotent-send boundary.

3. P1 — not every supported Telegram channel receives the Health guards.
   `notification_center/http_api.py:407-438` handles only `telegram.main` and
   `telegram.consumer:*`, while `notification_center/core.py:738-746` can
   materialize a supported generic `telegram.message` or `telegram.call`
   consumer root. A local custom generic Telegram policy produced a queued
   `telegram.message` row plus a separate `telegram.main:health.plans` row;
   neither the coalescer at `core.py:2154-2183` nor the worker gate covers the
   generic row. Add a regression for every supported Telegram channel or
   explicitly constrain the health policy surface.

4. P1 — the sent-legacy regression at
   `tests/test_delivery_worker.py:221-246` proves only that no second durable
   row is scheduled. The selected diff has no Telegram edit/update operation;
   if the pre-deploy `sent` card was emitted before plans, treating that row
   as canonical leaves the user-facing card without the required three signed
   plan buttons. The deployment must provide a durable migration outcome or a
   read-only queue/message audit proving such rows are already compliant
   before Health mode is re-enabled.

Question answers: the ordinary lone `queued` versus `claimed` path is
serialized, and the persisted-source synthetic guard plus exact-three-plan
gate work for `telegram.main` and `telegram.consumer:*`. They do not establish
the required single-slot invariant for legacy mixed rows, stale lease owners,
generic Telegram channels, or already-sent cards. Diagnosis/remediation rows
remain independently scheduled, but this review does not authorize their
external execution.

Result: `CHANGES_REQUIRED`.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed.

## Reviewer handoff — 2026-08-11 final current-snapshot verdict

The detailed evidence for this independent pass is recorded in the earlier
`Reviewer evidence — 2026-08-11 current final re-review` block. Final verdict:
`CHANGES_REQUIRED`.

Fresh source-level blockers remain: `health()` omits exact `health_plan_ids`
and pending `telegram.edit` from its reconciliation gate; successor
persistence can roll a successfully sent Health delivery back to `queued` and
allow a duplicate external send; and nested Telegram policy stages can hide a
later Matrix/phone successor. The selected suite was green (`125 passed`),
but it does not cover these three boundaries. No production, Telegram,
Hermes, database, queue, deployment, restart, or other external state was
changed.

## Critic receipt — 2026-08-11 01:18 MSK

Verdict: `CHANGES_REQUIRED`.

Re-reviewed the actual current uncommitted diff only in `notification_center/core.py`, `notification_center/http_api.py`, and `tests/test_delivery_worker.py`. The unrelated `package.json` diff and untracked files were excluded.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_consumer_policy.py`: `52 passed in 18.35s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- `git diff --check` for the selected paths: passed.
- Temporary-database, no-network reproductions exercised sent-main plus queued-custom and claimed-main plus claimed-custom interleavings.

Decisive finding:

1. P0 — `notification_center/core.py:2158-2179` still fails the exact-one-card invariant when a legacy `sent` Telegram row coexists with another active Telegram initial/plan row. The code builds `non_sent`, preferentially selects a non-sent custom/plan row as `canonical`, and only cancels queued duplicates (`core.py:2160-2169`). A sent main row therefore remains, while the queued custom row is released and both can be user-visible. In the temporary DB reproduction, attaching three valid plans left `telegram.main`=`sent` and `telegram.consumer:custom`=`queued`; the worker then sent the custom row, yielding two cards including the already-sent main card. With both rows claimed before attachment, both remained claimed and both fake sends completed. This is the live P0 class and is not covered by the individual sent-only or custom-only regressions at `tests/test_delivery_worker.py:221-272`.

Question answers:

- For one claimed row and no other active Telegram row, the SQLite transaction does avoid scheduling a second plan row; the claimed row remains the canonical slot. This is not a linearizable external-send reservation: `core.py:858-862` is a status read, while lease reclaim can allow two workers to pass the read and send the same delivery. The at-least-once lease is therefore not proof of the business exact-one-card claim.
- The worker's shared Telegram branch at `notification_center/http_api.py:407-437` applies both the persisted synthetic guard and exact-three-plan validation to `telegram.main` and `telegram.consumer:*`. The custom policy materialization path is present again: `core.py:727-768` returns `None` only for built-in health and still materializes custom non-Telegram stages. These parts are not the blocking defect.
- A fresh built-in health event starts without a Telegram initial and plan attachment creates/reuses one slot, but the multi-row migration case above remains unresolved before re-enable.

Minimum proof to proceed: add a red-then-green regression for an already-sent Telegram row coexisting with a queued/claimed custom or plan row; make reconciliation select one durable canonical outcome and prevent every other active row from sending; then run a read-only queue audit proving no legacy initial/custom/synthetic row can drain beside the canary. If exact-one external send remains an at-least-once limitation, record and gate that limitation explicitly instead of claiming the business invariant.

No production, Telegram, Hermes, database, queue, deployment, restart, or other external state was changed; all runtime checks used repository code and temporary test databases only.

## Critic receipt — 2026-08-11 01:27 MSK

Verdict: `CHANGES_REQUIRED`.

Re-reviewed the actual current uncommitted diff only in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`notification_center/http_api.py`, and `tests/test_delivery_worker.py`.
The unrelated `package.json` diff and untracked files were excluded.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_consumer_policy.py`: `53 passed in 19.02s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- Fresh temporary-database/no-network reproductions confirmed the mixed-row
  duplicate paths below.

Decisive findings:

1. P0 — a claimed legacy initial can still be superseded into a second card.
   At `core.py:2158-2196`, when a pre-existing
   `telegram.main:initial` is `claimed` and an old
   `telegram.main:health.plans` row is `queued`, `plan_rows` makes the queued
   plan row canonical, but the cancellation loop only cancels `queued` rows
   (`core.py:2179-2181`). The claimed initial remains active. After attaching
   three valid plans, a local worker reproduction sent both rows; both fake
   Telegram sends completed. The current claimed-race regression has no
   pre-existing plan row and does not cover this state.

2. P0 — a sent legacy row can still coexist with a claimed custom Telegram
   row. The `sent` branch at `core.py:2160-2173` cancels queued duplicates but
   deliberately leaves claimed rows untouched. A fresh local reproduction
   marked `telegram.main:initial` sent, claimed
   `telegram.consumer:custom`, attached valid plans, and then delivered the
   still-claimed custom row; it produced the already-sent main card plus a
   second custom card. The sent-only and custom-only tests do not cover this
   mixed claimed state.

3. P0 — generic Telegram consumer roots bypass coalescing. Generic policy
   scheduling at `core.py:738-746` uses a delivery key ending in
   `:step:<step_id>:repeat:1`, while the health coalescer query at
   `core.py:2154-2156` only matches `:initial` and `:health.plans`. A local
   custom health policy with root `telegram.message` therefore retained its
   queued generic row and scheduled a separate `telegram.main:health.plans`
   row; the worker sent both after plans attached. The worker gate now covers
   `telegram.*` at `http_api.py:407-439`, but that guard cannot prevent this
   duplicate scheduling.

4. P1 — claim ownership is not linearizable across the external send.
   `core.py:858-862` checks only `status = 'claimed'`; it does not compare the
   worker's `attempt`/`claimed_at` generation. `claim_due_deliveries()` can
   reclaim an expired lease while an older worker is between the check and
   `self._telegram.send()` at `http_api.py:439`. The documented at-least-once
   lease is therefore not proof of the business exact-one-card invariant.

5. P1 — an already-sent pre-deploy card may be accepted as canonical without
   the required three signed plan buttons. The current sent-legacy regression
   only verifies that no new durable plan row is scheduled; the selected diff
   contains no Telegram edit/update operation. A read-only deployment audit
   must prove those existing messages are already compliant, or the migration
   needs an explicit durable outcome before Health mode is re-enabled.

Question answers: the lone queued-versus-claimed path is serialized, the
persisted-source synthetic guard works, and the exact-three-plan gate now
covers `telegram.main` plus `telegram.*`. Those local checks do not establish
the required single-card invariant for mixed claimed/sent rows or generic
consumer roots. Diagnosis/remediation deliveries were not executed by this
review.

Minimum proof to proceed: add red-then-green regressions for claimed initial
plus queued plan, sent initial plus claimed custom, and generic Telegram root
coalescing; reconcile every active Telegram row to one durable canonical
outcome; address or explicitly gate lease-generation duplicates; then run the
required read-only queue/message audit before any re-enable. This review
authorizes no Telegram send, Hermes egress, deployment, restart, or other
external action.

No production, Telegram, Hermes, database, queue, deployment, restart, or other external state was changed; runtime checks used only repository code and temporary test databases.

## Implementation addendum — 2026-08-11

The red-first regressions from the preceding CHANGES_REQUIRED verdict were
implemented in the selected NoticePlace paths:

- the health coalescer now queries every `telegram.%` delivery key, cancels
  all non-canonical active `queued` and `claimed` rows, and preserves sent
  history without scheduling a second card;
- Telegram delivery runs under the shared send lock and validates the exact
  `attempt` plus `claimed_at` lease generation before the external adapter;
- stale workers cannot complete a reclaimed delivery generation;
- added regression coverage for claimed initial + queued plan, sent main +
  claimed custom, generic Telegram root, and stale lease generation.

Evidence:

- focused coalescing/lease scenarios: `5 passed`;
- full selected suite (`delivery_worker`, `health_workflow`, `gptadmin_agent`,
  `http_api`, `admin_console`, `telegram_topics`, `consumer_policy`):
  `110 passed in 49.78s`;
- `git diff --check`: passed;
- production, Telegram, Hermes, queue, deployment, restart, and external
  state remain unchanged.

## Fresh gate findings and red-first fixes — 2026-08-11

Fresh Reviewer/Critic/Overseer identified three remaining risks:

- stale exception handling called `complete_delivery(..., retry)` without the
  claim generation;
- generic health Telegram policy repeats could create a second card after the
  first successful send;
- an old sent card without durable evidence of the exact three plan IDs could
  be silently retained as the canonical user-facing card.

Red-first regressions were added for all three. The implementation now passes
`claimed_at` and `attempt` through the exception path, suppresses health
Telegram repeats/successors while preserving non-Telegram escalation stages,
and records Bot API `message_id` plus plan IDs for new Telegram receipts.
When a sent Health row lacks proof of the attached three-plan set, the domain
records `health.legacy_card_migration_required`, cancels other active rows,
and does not pretend that the old card satisfies selection.

Evidence after these fixes:

- focused stale-exception/generic-repeat/legacy/compliance scenarios: `5
  passed`;
- Telegram sender receipt test: `1 passed`;
- selected suite before this addendum: `112 passed in 52.52s`;
- no production, Telegram, Hermes, queue, deployment, restart, or external
  mutation was performed.

Overseer verdict: `CONTINUE`; route remains disabled and queue remains
isolated until fresh Reviewer/Critic pass and the approved reconciliation.

## Durable send-boundary addendum — 2026-08-11

To close the crash-after-send duplicate window, Telegram claims now pass
through a durable `sending` reservation using the exact lease generation.
The lease reclaimer ignores `sending`; a transport exception becomes an
explicit `uncertain` terminal outcome instead of an automatic retry; and a
post-send follow-up failure cannot CAS a `sent` row back to `queued`.
Incident resolution also cancels `sending` work. This favors no duplicate
external message over an unverified resend; the supervisor must surface
`uncertain`/stuck sends for reconciliation.

Evidence:

- red-first reservation, uncertain-send, and post-send CAS regressions:
  `4 passed` focused;
- selected suite after the durable boundary: `115 passed in 51.31s`;
- no production, Telegram, Hermes, queue, deployment, restart, or external
  mutation performed.

## Final delivery-gate revision — 2026-08-11

The next fresh gate found four concrete issues and they were addressed before
the next review:

- health legacy `initial` deliveries no longer schedule critical repeats or
  other Telegram follow-up cards;
- `sending` and `uncertain` are first-class active/reconciliation states;
  coalescing never cancels an in-flight send, and an uncertain send blocks a
  second card until explicit reconciliation;
- Health delivery now fails closed when signed callback configuration is
  missing;
- a compliant sent card requires message ID, the exact three plan IDs,
  `health_button_count == 3`, and `health_signed_callback_count == 3`.

New focused regressions cover in-flight coalescing, uncertain delivery, no
codec, legacy repeat suppression, and receipt metadata. They pass (`4` in
delivery and `2` sender tests). The selected full suite remains green at
`115 passed`; a final full run and fresh Reviewer/Critic verdict are pending.

The next fresh gate additionally required explicit handling of multiple
already-sent rows and `sent + sending`, and verified that skipping Telegram
repeats must not hide a configured Matrix/phone successor. The implementation
now blocks new Health plan delivery for those reconciliation combinations,
keeps non-Telegram successors, and readiness is degraded whenever any
`sending` or `uncertain` delivery exists. New regressions cover these cases;
the full suite is being rerun before the final gate.

Further final-snapshot fixes:

- readiness now counts active reconciliation blockers (multiple sent cards,
  noncompliant legacy cards, `sending`, and `uncertain`) and reports degraded;
- post-send retry is refused for any already terminal `sent` row;
- Health phone pre-call context is suppressed so it cannot emit a second card;
- sent legacy rows with a durable `message_id` now schedule one `telegram.edit`
  migration, edit the original Bot API message with the signed three-plan
  keyboard, update the source receipt, and close the edit delivery as
  `superseded`; rows without message identity remain explicit manual-audit
  blockers.

Focused migration/edit and readiness checks are green. A fresh independent
review of this latest snapshot remains required before commit/deploy.

The latest red-first pass also covered Health phone pre-call suppression and
the `NULL` legacy-receipt readiness edge. Full selected evidence is now
`125 passed in 54.98s`; no production or external mutation occurred.

The subsequent red-first pass closed successor/migration races: readiness
counts queued `telegram.edit` jobs, sent rows cannot be requeued after
successor persistence failure, nested Telegram stages search for the first
non-Telegram successor, and repeated plan attachment reuses the existing
edit job. The selected suite is now `127 passed in 59.16s`.

## Reviewer evidence — 2026-08-11 final re-review

Scope reviewed: only the current uncommitted diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`notification_center/http_api.py`, and `tests/test_delivery_worker.py`.
The unrelated `package.json` diff and all untracked files were left untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `110 passed in 52.16s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- No production, Telegram, Hermes, database, queue, deployment, restart, or
  other external state was changed.

Confirmed addressed paths:

- `core.py:2170-2215` coalesces all `telegram.%` rows under the SQLite-backed
  center lock, cancels queued/claimed non-canonical rows, and does not create a
  second row when a sent row exists.
- `http_api.py:407-466` applies the persisted synthetic-source guard and exact
  three-plan validation to `telegram.main`, `telegram.consumer:*`, and generic
  `telegram.*` channels. The send lock plus exact `claimed_at`/`attempt` check
  prevents the stale worker generation from sending or completing a reclaimed
  delivery in the same process.
- Deterministic temporary-DB/no-network checks observed one send for claimed
  initial plus queued plan, zero new sends for sent main plus claimed custom,
  and one send after stale-lease reclaim. The previously added generic channel
  gate and synthetic tests remain green.

Decisive findings:

1. P0 — generic Telegram health policies can still emit multiple user-facing
   Health cards. `core.py:728-746` materializes a generic root with
   `policy_step_id`; after the first send, `core.py:900-912` schedules the
   next repeat/successor for any policy with `max_repeats > 1`, while
   `http_api.py:407-466` sends every `telegram.*` row after the same valid
   three-plan bundle is attached. A no-network temporary-DB reproduction with
   a `telegram.message` root and `max_repeats=2` sent two cards and left both
   rows `sent`. The new generic-root regression at
   `tests/test_delivery_worker.py:331-355` checks only pre-send coalescing and
   does not cover post-send repeat/successor scheduling. The health path must
   stop Telegram repeats/successors after the one canonical Health card, or
   explicitly exclude generic repeatable Telegram policies from Health.

2. P1 — the preserved sent-legacy migration state still assumes that an old
   card already contains the required signed plan buttons. When
   `core.py:2177-2184` sees any sent Telegram row it cancels all other active
   rows and keeps the sent row; no selected-diff code edits or replaces that
   Telegram message. `http_api.py:225-249` only adds plan text and signed
   callbacks when the outgoing payload has plans and an action codec, so a
   pre-deploy initial sent before plan attachment can remain a user-facing
   card with no three plan buttons and no selectable workflow. A read-only
   queue/message audit proving all preserved sent cards are compliant, or an
   explicit durable edit/replacement migration outcome, is still required
   before Health mode is re-enabled.

3. P1 — exact-one external send remains at-least-once across process crash or
   multiple worker processes. `http_api.py:459` calls the external Telegram
   adapter before `core.py:461-466` records `sent`; a crash after the adapter
   succeeds and before completion allows lease reclaim and a second send.
   `core.py:859-863` is only an in-process `RLock`, and
   `core.py:865-875` validates a lease generation but does not provide an
   external idempotency key. The selected stale-generation regression proves
   same-process stale workers, not crash-after-send or multi-process behavior.
   Either the exact-one contract needs a durable/idempotent send boundary, or
   this residual duplicate risk must be an explicit deployment gate.

Question answers:

- Q1: the claimed-versus-queued migration path is serialized and the local
  mixed-row reproductions now yield one send; this is not a complete durable
  exact-one guarantee because of findings 1 and 3.
- Q2: `telegram.%` coalescing and the shared worker branch cover the default,
  custom, and generic Telegram channel families for synthetic/exact-three
  guards, but generic repeat/successor rows can bypass the one-card invariant
  after the first send.
- Q3: a fresh built-in health incident creates no initial Telegram row; valid
  plan attachment releases or creates one canonical row and the focused tests
  prove one send in the covered migration states. The generic repeat case and
  pre-deploy sent-card button state prevent acceptance of the full contract.
- Q4: findings 1 and 2 are release blockers; finding 3 is an explicit
  at-least-once/deployment assumption.

Result: `CHANGES_REQUIRED`.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; all runtime checks used temporary databases,
fake adapters, and repository tests only.

## Critic receipt — 2026-08-11 01:46 MSK

Verdict: `CHANGES_REQUIRED`.

Re-reviewed the actual current uncommitted diff only in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`notification_center/http_api.py`, and `tests/test_delivery_worker.py`.
The unrelated `package.json` diff and all untracked files were excluded and
left untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `110 passed in 50.61s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- Read-only temporary-database checks reproduced the stale exception mutation
  and the pre-deploy sent-card state; no production, Telegram, Hermes, queue,
  deployment, restart, or other external state was changed.

Decisive findings:

1. P0 — a sent legacy Health card can remain the sole canonical delivery
   without the required three signed plan buttons. In
   `notification_center/core.py:2177-2190`, the `sent` branch cancels every
   queued/claimed Telegram row and deliberately keeps the already-sent row;
   there is no Telegram edit/update path in the selected diff, and
   `TelegramSender.send()` only creates a new `sendMessage` at
   `notification_center/http_api.py:218-254`. A deterministic temporary-DB
   reproduction captured a legacy payload with no `health_plans`, marked its
   row `sent`, attached three valid plans, and observed only the original
   `telegram.main:initial` row remaining `sent` with no plan delivery. This
   leaves the known pre-deploy card without selectable signed plans and can
   stall the required explicit-selection/remediation workflow. The deployment
   must either durably migrate/edit such messages or prove by a read-only
   message audit that every retained sent row is already compliant before
   re-enable; the current source review does neither.

2. P1 — a stale worker exception can mutate a newer lease and recreate a
   duplicate-send opportunity. The Telegram path passes `claimed_at` and
   `attempt` on its normal completion at
   `notification_center/http_api.py:459-466`, but the outer exception handler
   calls `complete_delivery()` without either generation at
   `notification_center/http_api.py:523-525`. `core.py:883-899` accepts that
   unqualified retry and updates the row even if it has already been
   reclaimed. A deterministic temporary-DB reproduction claimed attempt 1,
   reclaimed attempt 2, then applied the same unqualified retry used by the
   exception path; the row changed from current `claimed` attempt 2 to
   `queued` attempt 2. If the old send reached Telegram before raising, the
   requeued delivery can send the same Health card again. The stale-generation
   regression at `tests/test_delivery_worker.py:357-378` covers only the
   successful stale-worker early return, not this exception path.

Question answers:

- The ordinary queued-versus-claimed coalescing path is serialized by the
  shared in-process lock, and the current coalescer cancels mixed active
  Telegram rows. The exact lease read prevents a stale successful worker in
  this process, but it is not a durable external-send reservation and the
  exception path above bypasses the generation check.
- `notification_center/http_api.py:407-457` gates every `telegram.*` channel
  on persisted synthetic detection and exactly three validated plans; the
  coalescer query at `core.py:2171-2175` also covers generic Telegram roots.
- A fresh built-in health event starts without a Telegram initial and valid
  plan attachment reuses one active Telegram slot while diagnosis/remediation
  deliveries remain separately scheduled. That does not repair the known
  already-sent pre-deploy card or establish exact-one delivery across stale
  exception/restart failure domains.

Minimum proof to proceed: provide a durable sent-message migration/edit or a
fresh read-only audit proving every retained sent Health row has exactly three
signed buttons; make every failure completion compare-and-set the lease
generation (with a red-then-green exception/reclaim regression); then rerun
the selected suite and the quarantined queue audit before any re-enable.

No production or external state was changed; all runtime checks used repository
code and temporary databases only.

## Critic receipt — 2026-08-11 02:09 MSK

Verdict: `CHANGES_REQUIRED`.

Re-reviewed the current uncommitted diff only in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated `package.json`
diff and untracked files were excluded and left untouched.

Checks on the stable current snapshot:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `115 passed in 50.85s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- No-network temporary-database reproductions exercised unknown-send, in-flight-send, and duplicate-sent states; a mocked Bot API response exercised the sender without a callback codec.

Decisive findings:

1. P0 — the new `uncertain` send outcome can schedule a second Health card. `core.py:2191` excludes `uncertain` rows from `active`, and `core.py:2260-2261` schedules a fresh `telegram.main:health.plans` row when no active row remains. A temporary DB with a Telegram row marked `uncertain` followed by a valid `health.plans_attached` update retained the uncertain row and created a queued plan row. If the unknown outcome actually reached Telegram, the next row creates the required duplicate-card failure. Unknown outcomes must remain a durable reconciliation blocker, not become an empty-slot signal.

2. P0 — the coalescer can cancel a delivery after it crossed the external send boundary. `core.py:2191` treats `sending` as active, but the cancellation loops at `core.py:2203-2210`, `core.py:2223-2229`, and `core.py:2237-2248` cancel every non-canonical row, including `sending`. A temporary DB with one reserved `sending` row and one queued `health.plans` row left the sending row `cancelled` and the plan row claimed. If the first worker is inside `Telegram.send`, it can still emit its card, while the surviving row emits another card. The durable sending state needs an explicit no-cancel/no-coalesce rule or a compare-and-set outcome that accounts for an in-flight external operation.

3. P1 — the persisted compliance proof does not prove three signed buttons. `http_api.py:245-249` adds `reply_markup` only when `_action_codec` is configured, while `core.py:2265-2279` accepts a sent row as compliant from only `message_id` plus matching `health_plan_ids`. A mocked Bot API call through `TelegramSender` with valid three plans and no action codec returned those IDs but sent a request with no `reply_markup`; the migration gate would nevertheless accept an equivalent receipt. The sender must fail closed for Health without the signed callback codec, or persist/verify durable keyboard evidence.

4. P0 — an already-sent noncompliant legacy card still has no source-level migration outcome. `core.py:2203-2222` records `health.legacy_card_migration_required`, cancels other active rows, and deliberately retains the old sent row; the selected diff has no Telegram edit/replacement path (`http_api.py:245-251` only sends a new message). The current regression at `tests/test_delivery_worker.py:221-250` verifies the audit marker, not a user-facing card with three signed buttons. Re-enable requires either a durable edit/replacement migration or a read-only Bot API audit proving every retained sent message is already compliant.

5. P1 — multiple already-sent compliant rows are all retained. In the `sent_rows` branch at `core.py:2199-2236`, the code cancels only non-sent rows and never rejects or quarantines `len(sent_rows) > 1`. A temporary DB with two sent rows carrying the same three plan IDs kept both rows `sent` after plan attachment. This is a pre-existing duplicate-card state the deployment queue/message audit must detect before any re-enable.

Question answers: the ordinary claimed-versus-queued path, generic Telegram gate, persisted synthetic guard, and stale-generation completion tests are green. They do not establish the exact-one business invariant for `uncertain` rows, in-flight `sending` cancellation, sent-message button compliance, or pre-existing multiple sent rows. The selected suite is therefore technical proxy evidence only, not business acceptance.

Minimum proof to proceed: add red-then-green regressions for uncertain-plus-plan attachment, sending-plus-queued coalescing, no-codec Health send, and multiple sent rows; make uncertain/sending states durable reconciliation blockers; make Health sends fail closed unless the signed keyboard is present and auditable; then perform the required read-only queue and Bot API message audit before re-enable. No Telegram send, Hermes egress, deployment, restart, or external state change is authorized by this review.

No production, Telegram, Hermes, database, queue, deployment, restart, or other external state was changed; runtime checks used repository code, mocked transport, and temporary databases only.

## Critic receipt — 2026-08-11 02:27 MSK

Verdict: `CHANGES_REQUIRED`.

Scope reviewed: only the current uncommitted NoticePlace diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated
`package.json` diff and all untracked files were not inspected or changed.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `118 passed in 49.91s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- A temporary-database/no-network reproduction created two already-sent
  Telegram rows (`telegram.main` and `telegram.consumer:custom`), each with
  the exact three plan IDs and signed-button receipt metadata. After a valid
  `health.plans_attached`, both rows remained `sent`:
  `multiple_sent_compliant: [{'channel': 'telegram.consumer:custom', 'status': 'sent'}, {'channel': 'telegram.main', 'status': 'sent'}]`.

Decisive finding:

1. P0 — multiple already-sent compliant Health cards are silently accepted,
   violating the exact-one-card invariant. At
   `notification_center/core.py:2207-2210`, compliance is computed with
   `all(...)` and does not reject `len(sent_rows) > 1`. The `elif sent_rows`
   branch at `core.py:2271-2284` skips every sent row, emits no duplicate or
   reconciliation audit, and cancels only non-sent rows. Thus a pre-existing
   duplicate state can be treated as compliant and survive re-enable with two
   user-visible cards. The current tests cover one compliant sent row and
   single-row legacy migration, but no multiple-sent regression.

2. P1 — an already-sent row coexisting with an in-flight `sending` row has no
   durable duplicate blocker. The `sending_rows` branch at
   `core.py:2231-2250` deliberately retains both the in-flight row and all
   sent rows. This is correct for avoiding cancellation after the external
   boundary, but it does not record that the incident already has a sent card
   plus a second send candidate. If the in-flight adapter succeeds, the
   incident can finish with two cards; if it is stranded after a process
   crash, `health_status()` reports `sending_deliveries` but still computes
   `ready` without requiring `sending == 0` (`core.py:2087-2096`). The
   re-enable gate therefore still depends on an unperformed queue/message
   audit.

The exact-three-plan gate, persisted synthetic-source guard, generic/custom
Telegram coverage, uncertain-send blocker, in-flight no-cancel behavior,
signed-codec fail-closed path, stale lease-generation checks, and generic
Health repeat suppression were covered by the green selected suite or the
existing focused regressions. They do not repair the duplicate-sent state.

`QUESTIONS_FOR_L`: none; the blocker is reproducible from the selected source
and temporary databases.

Minimum proof to proceed, with two viable routes:

- Route A: add red-then-green coverage for two sent Telegram rows (including
  compliant metadata) and mixed sent/in-flight state; make reconciliation
  fail closed, quarantine the duplicate state, and provide an explicit
  message-level migration/edit/replacement outcome before Health re-enable.
- Route B: leave the source behavior unchanged only if the deployment gate is
  strengthened to keep Health disabled and a read-only queue plus Bot API
  message audit proves and repairs every duplicate/noncompliant sent row and
  every stranded `sending` row; the audit must show exactly one compliant
  card per incident before re-enable.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; all runtime checks used repository code,
fake adapters, and temporary databases only.

## Reviewer evidence — 2026-08-11 independent final pass

Scope reviewed: only the selected uncommitted NoticePlace diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated `package.json`
diff and all untracked files were left untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `118 passed in 45.77s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- No-network temporary-database checks reproduced: two compliant sent Telegram
  rows remain sent after plan attachment; a generic Health Telegram root with
  `max_repeats=2` sends once but never schedules its Matrix successor; and a
  reserved `sending` row leaves `health()` at `status=ok` with
  `sending_deliveries=1`.

Decisive findings:

1. P0 — multiple already-sent compliant Health cards are still accepted as
   one canonical state. `core.py:2207-2210` computes compliance with `all(...)`
   but never requires exactly one sent row; `core.py:2271-2284` then retains
   every sent row and emits no duplicate-state audit. A temporary DB with
   `telegram.main` and `telegram.consumer:custom`, both `sent` with the exact
   three plan IDs and signed-button receipt metadata, remained two `sent` rows
   after valid `health.plans_attached`. This violates the exact-one-card
   contract and is not covered by the one-sent-row regression.

2. P1 — generic Health Telegram repeat suppression drops non-Telegram policy
   successors. In `core.py:920-939`, the `repeat_number < max_repeats` branch
   suppresses the Telegram repeat, but its `elif` successor branch is skipped
   entirely. A generic Health policy with a Telegram root at `max_repeats=2`
   followed by a Matrix call produced only `telegram.message=sent`; no
   `matrix.call` row was materialized. This regresses the explicit requirement
   to preserve non-Telegram Matrix/phone escalation while suppressing Health
   Telegram repeats.

3. P1 — a stranded in-flight send is reported as ready. `core.py:2087-2097`
   counts `sending_deliveries` but computes readiness from storage, heartbeat,
   and `uncertain == 0`; a temporary DB with one durable `sending` row returned
   `{'status': 'ok', 'sending_deliveries': 1, 'uncertain_deliveries': 0}`.
   Because the lease reclaimer intentionally ignores `sending`, a crash can
   strand the Health card while the readiness surface permits re-enable. The
   deployment gate must explicitly block/reconcile nonzero `sending` state.

Question answers:

- Q1: the lone claimed-versus-queued migration path remains serialized and
  stale generations do not send in the covered same-process path; mixed sent
  rows still violate the linearizable single-card outcome.
- Q2: the worker's persisted-synthetic and exact-three-plan checks cover
  `telegram.main`, custom, and generic `telegram.*` rows. The coalescer does
  not repair multiple sent history, and generic follow-up scheduling loses
  non-Telegram successors.
- Q3: a fresh built-in Health event creates no initial Telegram row and valid
  plan attachment releases one covered slot; the duplicate-sent migration
  state and custom escalation regression prevent acceptance of the full
  contract.
- Q4: findings 1 and 2 are source-level blockers; finding 3 is a deployment
  readiness blocker. No production, Telegram, Hermes, database, queue,
  deployment, restart, or other external state was changed.

Result: `CHANGES_REQUIRED`.

## Critic receipt — 2026-08-11 02:41 MSK

Verdict: `CHANGES_REQUIRED`.

Re-reviewed the actual current uncommitted diff only in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated
`package.json` diff and all untracked files were excluded and left untouched.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `121 passed in 51.57s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- Existing graphify query was used only to trace the selected delivery graph.
- No production, Telegram, Hermes, queue, database, deployment, restart, or
  other external state was changed; behavioral reproductions used temporary
  SQLite databases and fake adapters only.

Decisive findings:

1. P0 — a critical Health phone escalation bypasses the single-card gate and
   emits a second Telegram Health card. Legacy custom policy materialization
   at `core.py:755-761` preserves an `android.phone.call` stage after the
   Telegram row is released. When that phone row is delivered,
   `http_api.py:430-438` calls `self._telegram.send(payload)` directly from
   `_send_critical_pre_call_context`, and `http_api.py:539-543` invokes it for
   every critical phone delivery, including Health. This path does not pass
   through the Telegram `health.plans`/synthetic/coalescing gate, durable send
   reservation, or one-slot reconciliation. A no-network temporary-DB
   reproduction with a critical `health.degraded` custom `telegram` + `phone`
   policy, three attached plans, and fake adapters observed two fake Telegram
   sends (phone pre-call context plus the canonical Health delivery) and one
   phone call. The current successor regression covers Matrix only at
   `tests/test_delivery_worker.py:664-716`; no Health-phone regression exists.

2. P0 — multiple already-sent compliant cards are still accepted as a
   readiness-safe state. `core.py:2225-2244` records
   `health.multiple_sent_delivery_reconciliation_required` but deliberately
   retains every sent row; `core.py:2091-2117` computes readiness only from
   storage, dispatcher heartbeat, `sending`, and `uncertain`, so it returns
   `status=ok` once the duplicate rows are sent. A no-network temporary-DB
   reproduction with sent `telegram.main` and `telegram.consumer:custom` rows,
   both carrying the exact three plan IDs and signed-button receipt metadata,
   left both rows `sent` after plan attachment and returned
   `{'status': 'ok', 'sending_deliveries': 0, 'uncertain_deliveries': 0}`.
   `tests/test_delivery_worker.py:537-575` verifies only the audit marker and
   does not establish exactly one durable/user-visible card or a re-enable
   blocker.

3. P0 — a noncompliant pre-deploy sent Health card is retained without a
   user-facing migration outcome. `core.py:2305-2324` cancels other active
   rows and records `health.legacy_card_migration_required`, but keeps the old
   sent row. The selected diff has no Telegram edit/replacement operation;
   `http_api.py:245-253` only constructs a new `sendMessage` and fails closed
   for future Health sends without the callback codec. A sent card whose
   `result_json` lacks the exact three plan IDs/button counts therefore remains
   the only canonical card while offering no signed choices. The existing
   regression at `tests/test_delivery_worker.py:221-250` checks the audit marker,
   not a compliant user-facing message or an explicit durable migration.

Question answers:

- Q1: the claimed-versus-queued plan-attachment branch is serialized and the
  exact lease generation/send reservation prevents the covered stale worker
  from sending. That does not cover direct phone pre-call sends or multiple
  already-sent rows.
- Q2: the shared worker branch covers `telegram.main`, custom, and generic
  `telegram.*` rows for persisted synthetic-source and exact-three-plan
  guards. The phone pre-call path is outside that branch.
- Q3: a fresh built-in Health incident has no initial Telegram row and valid
  plan attachment releases one covered slot. A supported critical custom
  phone policy can still produce two Telegram sends, and retained legacy
  sent cards can lack selectable signed plans.
- Q4: findings 1–3 block the exact business contract. `QUESTIONS_FOR_L`: none;
  each blocker is reproducible from the selected source and temporary state.

Minimum proof to proceed, with two viable routes:

- Route A: add red-then-green Health-phone coverage and make critical Health
  pre-call context skip or use the same single durable Telegram slot; add
  red-then-green coverage for duplicate sent rows and noncompliant legacy
  cards, with a durable quarantine/migration outcome and readiness gate.
- Route B: keep Health disabled until an explicit reconciliation process proves
  exactly one compliant Bot API message per incident and resolves all legacy,
  duplicate, and in-flight states, while separately constraining Health phone
  escalation so it cannot invoke an untracked Telegram send.

No Telegram send, Hermes egress, deployment, restart, queue mutation, or
other production/external action was authorized or performed by this review.

## Critic receipt — 2026-08-11 current pass final index

Verdict: `CHANGES_REQUIRED`.

The detailed current-pass evidence is recorded above at lines 378-467. The
selected suite was green (`125 passed in 53.02s`), but isolated temporary-DB
reproductions still show: false `health()` readiness for a wrong/missing
Health plan-ID receipt and queued migration; a successor-persistence failure
that requeues a successfully sent Health card and yields two fake Telegram
sends; nested Telegram stages that drop a Matrix successor; and repeated plan
attachment that cancels its own queued edit migration. These are release
blockers requiring the minimum proof and one of the two routes recorded in
that receipt.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed.

## Reviewer final handoff — 2026-08-11

Verdict: `CHANGES_REQUIRED`.

The independent current-snapshot review found three source-level blockers:
readiness omits exact Health plan-ID proof and queued migration state;
post-send successor persistence can requeue a successful Telegram send; and
nested Telegram policy stages can suppress a later Matrix/phone successor.
The selected suite passed `125` tests, but these boundaries remain untested.
No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed.

## Reviewer evidence — 2026-08-11 current independent snapshot pass

Scope reviewed: only the current uncommitted NoticePlace diff in
`/home/admin/agents-projects/noticeplace/notification_center/core.py`,
`http_api.py`, and `tests/test_delivery_worker.py`. The unrelated `package.json`
diff, modified files outside the selected scope, and all untracked files were
left untouched. The selected source files were initially under the shared-
worktree five-minute freshness shield; I did not edit them, and they were
stable before validation.

Checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `127 passed in 59.42s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Scoped `git diff --check`: passed.
- No-network temporary SQLite/fake-adapter reproductions were used for the
  readiness findings below; no production or external state was changed.

Decisive findings:

1. P0 — readiness reports `ok` for a sent Health card whose durable receipt
   has the wrong plan set. `core.py:2165-2175` checks only `message_id`,
   `health_button_count`, and `health_signed_callback_count`; it does not
   require `health_plan_ids` to exist, contain exactly three unique IDs, or
   equal the latest persisted `health.plans_attached` bundle. A temporary
   incident with three valid attached plans and a sent receipt carrying
   `health_plan_ids=["old-a", "old-b", "old-c"]` returned
   `center.health()["status"] == "ok"`, with
   `reconciliation_required == 0`. The same false-ready state is possible
   for a missing plan-ID field. The current compliant-card test at
   `tests/test_delivery_worker.py:253-287` covers matching metadata only.
   Readiness must compare the sent receipt to the latest exact plan bundle
   and fail closed until a migration/edit is complete, with a red regression.

2. P0 — readiness does not detect a queued or claimed second Telegram Health
   delivery alongside one already-sent card after the plan-attachment
   coalescer has run. The readiness query at `core.py:2147-2177` detects
   multiple `sent` rows and selected `telegram.edit` work, but has no
   sent-plus-active-duplicate condition for ordinary `telegram.%` rows. A
   temporary database with one compliant `telegram.main` row in `sent` and
   one `telegram.consumer:legacy` row in `queued` returned `status=ok`,
   `queued_deliveries=1`, and `reconciliation_required=0`; the queued row
   remained sendable. The coalescer at `core.py:2282-2476` runs when a new
   `health.plans_attached` update is processed, so it cannot make a later or
   pre-existing queue state safe for re-enable. Readiness/reconciliation must
   block any non-canonical active Telegram row when a sent Health card exists,
   with a regression for sent-plus-queued and sent-plus-claimed states.

Question answers:

- Q1: the covered claimed-versus-queued attachment path is serialized by the
  shared center lock; the send lock plus exact `claimed_at`/`attempt` CAS
  prevents the stale same-process worker from sending. The readiness gaps
  above still allow unsafe pre-existing state to pass the external gate.
- Q2: `http_api.py:547-623` applies the persisted synthetic-source and
  exact-three-plan checks to `telegram.main`, `telegram.consumer:*`, and
  generic `telegram.*`; `telegram.edit` separately validates the signed
  three-plan migration. This channel coverage does not repair the incomplete
  readiness query.
- Q3: a fresh built-in Health event creates no Telegram initial, and valid
  plan attachment releases one canonical slot in the covered queue states;
  the selected suite and fake-adapter checks support that result. The two P0
  readiness gaps prevent acceptance of the full exactly-one-card contract.
- Q4: the two P0 findings are release blockers. A process crash while a row is
  `sending` remains an unverified deployment assumption: the reclaimer leaves
  it stranded and readiness correctly degrades, but no production queue or
  Bot API reconciliation audit was authorized here.

Result: `CHANGES_REQUIRED`.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; all runtime checks used repository tests,
temporary SQLite databases, and fake adapters only.

## Critic receipt — 2026-08-11 narrow final handoff

Verdict: `CHANGES_REQUIRED`.

One concrete P0 blocker from the latest independently recorded review: the
Health readiness surface can accept a sent card whose persisted
`health_plan_ids` are missing or stale relative to the latest attached
three-plan bundle, while migration/edit work is still queued. That permits
re-enable without a proven user-facing card containing the current three
signed choices. PASS requires the readiness gate to fail closed for this
mismatch and a focused red-then-green regression. No full suite or external
state change was performed by this handoff.

## Lead follow-up — 2026-08-11

The two P0 findings were converted to red-first regressions in
`tests/test_http_api.py`:

- a sent Health receipt with stale plan IDs beside a newer attached bundle;
- a queued `telegram.consumer:*` duplicate added after a compliant sent card.

The red run reproduced the second false-ready case. `core.py` now makes
readiness fail closed for both cases: it compares sent receipts with the
latest attached three-plan bundle and counts queued/claimed Telegram rows
beside any sent Health card as reconciliation-required. The targeted checks
passed (`3 passed`); the selected suite passed (`129 passed in 78.09s`);
`py_compile` and `git diff --check` passed. Production, Telegram, Hermes,
database, queue, deployment, and restart state remain unchanged pending a
fresh independent review and commit.

## Critic receipt — 2026-08-11 03:31 MSK

Verdict: `CHANGES_REQUIRED`.

Scope audited: only the current uncommitted NoticePlace diff in
`notification_center/core.py`, `notification_center/http_api.py`, and
`tests/test_delivery_worker.py`. The unrelated `package.json` diff and all
untracked files were excluded and untouched.

Fresh checks:

- `pytest -q tests/test_delivery_worker.py tests/test_health_workflow.py tests/test_gptadmin_agent.py tests/test_http_api.py tests/test_admin_console.py tests/test_telegram_topics.py tests/test_consumer_policy.py`: `127 passed in 60.68s`.
- `python -m py_compile notification_center/core.py notification_center/http_api.py`: passed.
- Selected `git diff --check`: passed.
- No-network temporary SQLite/fake-adapter reproductions were used only; no
  production or external state changed.

Decisive findings:

1. P0 — readiness accepts a sent Health receipt without exact plan-set proof.
   `core.py:2165-2175` checks `message_id` and the two button counters, but
   does not require `health_plan_ids` to contain exactly three unique IDs or
   match the latest `health.plans_attached` bundle. A temporary sent Health
   row with three button counters and `health_plan_ids=["old-a", "old-b",
   "old-c"]` returned `center.health()["status"] == "ok"` and
   `reconciliation_required == 0`. The same false-ready path exists when the
   field is absent. `record_health_delivery_migrated()` at
   `core.py:975-990` also persists an unvalidated receipt, so a completed
   migration can leave this false-ready state. Add a red-then-green readiness
   regression requiring exact persisted plan IDs and validated migration
   receipts.

2. P0 — readiness does not quarantine a later queued Telegram duplicate beside
   an already-sent compliant card. The coalescer at `core.py:2282-2476` runs
   only when `health.plans_attached` is processed, while the readiness query at
   `core.py:2147-2177` detects only multiple sent rows, in-flight rows, and
   `telegram.edit`; it does not flag ordinary `telegram.%` rows in queued or
   claimed state alongside a sent row. A temporary DB with one compliant sent
   `telegram.main` row, valid plans already attached, and a subsequently added
   queued `telegram.consumer:legacy` row returned `status=ok` with
   `queued_deliveries=1`. That row remains valid under the worker's
   `http_api.py:547-623` synthetic/exact-three-plan gate and can emit a second
   Health card. Add a sent-plus-queued/claimed reconciliation blocker and
   regression, or make the durable canonical-slot invariant apply on every
   later scheduling path.

Question answers:

- Q1: the covered plan-attachment queued/claimed coalescing path is serialized
  and exact `claimed_at`/`attempt` checks prevent the stale same-process worker
  from sending. It does not make later ordinary Telegram rows safe.
- Q2: `http_api.py:547-623` covers default, custom, and generic `telegram.*`
  rows for persisted synthetic and exact-three-plan guards; `telegram.edit`
  separately validates migration payloads. These worker guards do not replace
  readiness reconciliation.
- Q3: a fresh built-in Health event creates no Telegram initial and valid plan
  attachment releases one canonical slot in covered states. The two readiness
  gaps allow unsafe pre-existing state to pass the re-enable surface, so the
  full exactly-one-card contract is not proven.
- Q4: both findings are P0 release blockers. The `sending` crash state remains
  a deployment reconciliation assumption even though readiness degrades for
  it; no production queue or Bot API audit was authorized here.

Minimum proof to proceed, with two viable routes:

- Route A: add red-then-green tests and fixes for exact receipt/plan-ID
  readiness and sent-plus-active duplicate quarantine; rerun the selected
  suite and the read-only quarantined queue/message audit.
- Route B: keep Health disabled and require a read-only queue plus Bot API
  audit proving exactly one compliant message per incident, while separately
  preventing any later queued/claimed Telegram row from draining beside the
  canonical card.

No production, Telegram, Hermes, database, queue, deployment, restart, or
other external state was changed; all runtime checks used repository tests,
temporary SQLite databases, and fake adapters only.
