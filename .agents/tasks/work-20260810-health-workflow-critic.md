# Independent critic: Health business completion gate

Status: work

## Immutable objective

The system must observe host CPU/RAM/disk, failed services, and log
error/keyword signals; deduplicate degradation; invoke GPTAdmin → Agent Herder
→ OpenCode/OmniRoute for diagnosis and exactly three plans; obtain explicit
user selection; run Hermes/OpenAI/Codex/gpt-5.6-luna/high remediation with
useful-progress supervision; independently verify the original source; and
emit a resolved NoticePlace receipt with elapsed time and trace IDs.

## Current delta to critique

NoticePlace now has a local implementation slice that maps a completed
health-remediation receipt into progress, independent verification, and
resolved state, plus a queued resolved Telegram delivery. GPTAdmin polling
forwards useful progress and stops stale health remediation. Fleet's source
prompt requires source_fingerprint and verifier_id. Focused local tests and a
real-local no-send canary pass, but live deployment and Telegram topic/send
remain gated.

## Critic scope and rules

Read-only, independent, no edits, no deploy/restart, no Telegram send, no
Hermes egress. Inspect current source and evidence, and return a verdict of
CONTINUE, STOP, or ASK_USER with concrete reasons. Do not treat tests, fake
transports, or terminal receipts alone as proof of the business result.

## Estimate

Initial: 20 / 30 / 60 active minutes.

## Critic audit receipt (2026-08-10)

### Independently reconstructed done condition

Completion requires more than a local state-machine receipt: one real health
signal must reach the real NoticePlace/GPTAdmin/Agent-Herder/OpenCode or Hermes
consumer path, produce exactly three plans, accept one explicit user choice,
supervise useful (non-heartbeat) remediation progress, independently verify the
original source, and leave a resolved NoticePlace receipt with elapsed time and
trace IDs. The user-facing Telegram Health topic and real downstream execution
are part of that condition; fake transports and a terminal local receipt cannot
substitute for them.

### Fresh read-only evidence

- A disposable loopback HTTP probe against the current NoticePlace handler
  submitted one signal, three plans, one selected plan, then a progress receipt
  with `step=heartbeat`, empty `evidence_refs`, `heartbeat_at=1`, and the
  non-empty fingerprint `heartbeat-only`. All requests were accepted; the
  progress response reported `useful_progress=true`. After an independent
  healthy verification, the same temporary incident resolved successfully.
  This is a direct current-code reproduction, not a historical task claim.
- The cause is in
  `/home/admin/agents-projects/noticeplace/notification_center/core.py:1539-1545`:
  `heartbeat_only` is true only when the heartbeat has neither evidence nor a
  fingerprint. A heartbeat carrying a fingerprint therefore proceeds to
  `/home/admin/agents-projects/noticeplace/notification_center/core.py:1575-1597`,
  where the first changed receipt is marked useful. Resolution only asks
  whether any useful receipt exists at `core.py:1674-1693,2124-2130`.
  `health_workflow.py:337-340` has the same loophole at the wrapper boundary.
- The focused HTTP test covers the empty-fingerprint case only
  (`/home/admin/agents-projects/noticeplace/tests/test_http_api.py:149-161`);
  it does not cover the reproduced `heartbeat-only` fingerprint variant.
  Existing progress tests prove ordinary changed-step/evidence behavior but do
  not prove that a heartbeat-labelled receipt with no non-heartbeat evidence is
  rejected (`tests/test_health_workflow.py:286-300`).
- A fresh 30-iteration disposable race using two independent
  `NotificationCenter` instances sharing SQLite produced one durable selection
  row and one `health.plan_selected` event in every iteration. The durable
  single-winner guard is therefore not the current blocker.
- The local vertical canary explicitly uses deterministic fakes for GPTAdmin,
  OmniRoute, Agent-Herder, and Telegram
  (`/home/admin/gptadmin/tests/e2e/health_incident_vertical_canary.py:1-7,35-72,132-194`).
  It proves local NoticePlace/producer behavior only; it does not prove the
  real Telegram click, Hermes execution, or a real resolved delivery.
- Current live evidence records one real signal and one `health.plans_attached`
  result, but zero `health.plan_selected`, remediation, verification, or
  resolved events, with Telegram deliveries left queued
  (`.agents/tasks/work-20260810-health-runtime-continuation.md:320-325`).
  The current active Telegram modes have no Health route and the fresh
  BrowserOS inspections found no visible Health topic/card or three-plan
  receipt (`.agents/tasks/work-20260810-health-runtime-continuation.md:227-249`,
  `.agents/tasks/work-20260810-telegram-health-topic-tester.md:74-81`,
  `.agents/tasks/work-20260810-browseros-health-surface-tester.md:42-48`).

### BUSINESS_DELTA / P0_DISTANCE

Business delta is real intake plus real diagnosis/three-plan attachment, but
the business path remains stopped before explicit user selection. P0 distance is
therefore still the missing Health-topic user action, real Hermes remediation,
and independent resolved receipt. Additional local tests or disposable canaries
cannot close that distance while the visible topic and approved external path
are absent.

### Safeguards and excluded hypotheses

- Durable plan-selection concurrency passed the fresh multi-instance probe.
- The inspected resolution path requires selected plan, useful progress,
  matching source, healthy verification, and a distinct verifier; no separate
  finding was raised against those ordinary-path checks.
- No source, runtime, deployment, restart, Telegram, Hermes, provider, or
  external system was modified or called by this audit. The only mutations were
  temporary disposable SQLite/loopback test state.
- The conclusion does not rely on a failed unit/build or on a missing test
  alone: the heartbeat loophole was reproduced through the current loopback
  HTTP consumer path and reached `resolved`.

### QUESTIONS_FOR_L

- Does the contract treat a receipt explicitly labelled `heartbeat` with no
  evidence and only a heartbeat fingerprint as non-useful? The immutable
  objective says yes; the current implementation says no. Until that semantic
  boundary is fixed and regression-tested, completion claims are blocked.
- Which exact approved external action will expose the real Health topic and
  permit the bounded real user-selection/remediation canary? Current evidence
  proves that boundary is still pending, not green.

### Alternatives

1. Add a narrow state-machine fix that rejects heartbeat-only receipts even when
   they carry a fingerprint, add the missing regression through the HTTP route,
   rerun the focused suites and fresh race probe, then request the exact
   Telegram/Hermes gate for one real business canary.
2. Keep the implementation at technical-preview status: leave the Health route
   disabled/queued, make no completion or readiness claim, and wait for the
   operator-owned Health topic plus the supported user-facing surface before
   spending more on downstream execution.

### Minimum proof to proceed

- The reproduced heartbeat payload returns `useful_progress=false` (or is
  rejected) and cannot resolve an incident; a subsequent non-heartbeat useful
  receipt plus matching independent verification can resolve it.
- One fresh user-facing canary visibly shows the Health topic/card and exactly
  three plans, with one durable explicit selection.
- With the required approval, the real downstream path emits useful progress,
  performs Hermes remediation, independently verifies the original source, and
  produces one resolved NoticePlace receipt containing elapsed time and trace
  IDs. The evidence must identify real transports and not deterministic fakes.

### Verdict

`STOP` — the heartbeat-only fingerprint reproduction violates the useful-
progress gate, and the real user-facing/downstream business result remains
unproven. Do not proceed to completion or readiness claims until the local
semantic gap and the explicitly gated live business canary are addressed.

## Critic rerun receipt (2026-08-10 04:54 MSK)

### Fresh read-only evidence

- The earlier heartbeat-fingerprint reproduction is no longer current. NoticePlace `HEAD` is `fa7a1f7` (`Harden health receipt and callback boundaries`), and the working tree has no source/test diff for this path; the only tracked modification is an unrelated `package.json` change, while untracked task metadata is outside the source/test path.
- The current core rejects a heartbeat-labelled receipt with no evidence even when it carries a fingerprint at `/home/admin/agents-projects/noticeplace/notification_center/core.py:1539-1546`; the wrapper applies the same boundary at `notification_center/health_workflow.py:337-354` and returns the core's authoritative useful value.
- The HTTP regression now exercises `step=heartbeat`, empty evidence, `heartbeat_at=123`, and `progress_fingerprint=heartbeat-only`, expecting HTTP 400 and zero durable progress rows (`/home/admin/agents-projects/noticeplace/tests/test_http_api.py:149-175`). The workflow regression likewise asserts rejection and no persisted progress (`tests/test_health_workflow.py:253-273`).
- Fresh focused verification passed: `python -m unittest discover -s tests -p 'test_http_api.py' -v` ran 13 tests; `python -m unittest discover -s tests -p 'test_health_workflow.py' -v` ran 17 tests. Both suites passed, including the new fingerprint-only heartbeat case.
- The independent live evidence remains unchanged in the business-critical portion: the deployed real signal produced exactly three plans and queued Telegram delivery, but no `health.plan_selected`, remediation, verification, or resolved event; Telegram send and Hermes egress remain false (`.agents/tasks/work-20260810-health-runtime-continuation.md:320-325`). The visible Telegram/BrowserOS checks still found no Health topic/card or readable three-plan receipt (`.agents/tasks/work-20260810-telegram-health-topic-tester.md:74-81`, `.agents/tasks/work-20260810-browseros-health-surface-tester.md:42-48`).

### BUSINESS_DELTA / P0_DISTANCE

The local semantic gap is closed and independently regression-tested. Business delta is still only real intake plus real diagnosis/three-plan attachment. P0 distance remains the missing real user-facing Health surface and explicit selection, followed by approved Hermes remediation, useful-progress supervision, independent source verification, and one resolved NoticePlace receipt with elapsed time and trace IDs. No completion or readiness claim is justified.

### Safeguards and excluded hypotheses

- This rerun made no source, runtime, deployment, restart, Telegram, Hermes, provider, or external-system change. Test state was disposable.
- The old heartbeat finding is not carried forward as a current defect; it is recorded here only as resolved by `fa7a1f7` and the fresh 13/17 focused suites.
- The live blocker is not inferred from tests or terminal receipts: current live event counts and fresh black-box surface evidence show the business path has not reached selection or resolution.

### QUESTIONS_FOR_L

- What exact approved external action will expose the real Health topic/card and permit one bounded user-selection/remediation canary? A guessed thread ID, general-chat fallback, synthetic send, or Hermes egress without that gate remains out of scope.
- Should the route remain paused until that surface and the required approval are available, with no readiness claim in the interim?

### Alternatives

1. Keep the route at technical-preview status; wait for the operator-owned Health topic/card and explicit approval, then run one fresh context-free user-facing canary and collect real selection, remediation, verification, and resolved-receipt evidence.
2. If the product needs a readiness decision before the surface is available, record this as a blocked acceptance with the exact missing external boundary and do not deploy/restart, send Telegram, or start Hermes egress merely to create proxy evidence.

### Minimum proof to proceed

- A fresh supported user-facing canary visibly shows the Health topic/card and exactly three plans, followed by one durable explicit selection.
- The approved real downstream path emits useful non-heartbeat progress, performs the requested Hermes/OpenAI/Codex/gpt-5.6-luna/high remediation, independently verifies the original source with matching fingerprint and distinct verifier, and persists one resolved NoticePlace receipt containing elapsed time and trace IDs.
- The evidence identifies real transports and user action; local tests, fake transports, queued deliveries, and terminal receipts alone do not satisfy the gate.

### Verdict

`STOP` — the prior heartbeat loophole is fixed, but the real user-facing selection, downstream remediation, independent verification, and resolved business receipt remain unproven. Do not claim completion or readiness until the explicitly approved live canary supplies that evidence.

## Critic rerun receipt (2026-08-10 05:06 MSK)

### Fresh read-only evidence

- Current NoticePlace source is `HEAD 4897d9a` (`Reject heartbeat-only remediation receipts`). Fresh focused verification of the current source passed: `python3 -m pytest tests/test_health_workflow.py tests/test_http_api.py tests/test_gptadmin_agent.py tests/test_telegram_interactions.py tests/test_telegram_topics.py -q` -> `59 passed in 24.55s`. The GPTAdmin production-preflight regression also passed (`9 passed`). These are source/runtime-readiness checks, not business completion proof.
- Fresh read-only production preflight returned `ready_for_external_canary=true`, authenticated NoticePlace health HTTP 200, active notification-center/GPTAdmin-Hub/Agent-Herder/OpenCode/OmniRoute/Hermes units, aligned fleet collector artifacts, and `external_send=false`. The preflight only checks NoticePlace file presence, not digest equality for the changed NoticePlace files, and therefore cannot establish that the current safety commit is live.
- Direct digest comparison shows source/live drift for the files relevant to the acceptance path: `core.py` source `5b379cb257302468d0aece54d4902034be589fd4451d2fb23f905c297f8fdb8c` vs live `a076c1e1a48a2efcb4192ef8ca255e96fb080ce3f4e4721d4806325d40e4ddb1`; `gptadmin_agent.py` source `3718ebefa20e1b847df5732366c3bb49e13c3d9913fc33ca27813c07bed19727` vs live `91ae95c06730fb05bf78552c32bd852c85f3ef85b61ecc124f1c332cf03ae677`. The live service started at 04:20:27 MSK, before the current source commit at 05:02:20 MSK, and the runtime task records no deploy/restart for `fa7a1f7` or `4897d9a`.
- The live `core.py` still passes literal `False` to both agent/terminal `record_health_progress` calls (`/opt/noticeplace/notification_center/core.py:1009-1017,1094-1103`), while current source `4897d9a` passes a heartbeat-labelled predicate at `notification_center/core.py:1101`. Live still has the old conditional `heartbeat_only = bool(heartbeat) and not (evidence_refs or safe_fingerprint)` at `core.py:1541`, so the local heartbeat-bypass fix is not exercised by production. Live `gptadmin_agent.py:204-228` also still uses raw `str(... )[:limit]` receipt bounding, whereas current source routes those fields through `_safe_health_ref`.
- The business-critical live evidence remains unchanged: the real signal produced exactly three plans and a queued Telegram delivery, but no `health.plan_selected`, remediation/progress, independent verification, or `health.resolved` event; Telegram send and Hermes egress remain false (`.agents/tasks/work-20260810-health-runtime-continuation.md:320-325`). Fresh user-facing evidence still reports no visible Health topic/card or readable three-plan receipt (`.agents/tasks/work-20260810-telegram-health-topic-tester.md:74-81`, `.agents/tasks/work-20260810-browseros-health-surface-tester.md:42-48`).

### BUSINESS_DELTA / P0_DISTANCE

The local source gate improved: the heartbeat regression and focused suites are green. Production readiness probes also pass, but the deployed NoticePlace safety code is stale relative to the reviewed source. Business delta is still only real intake plus diagnosis/three-plan attachment. P0 distance remains runtime parity, a real visible Health user action with one durable selection, approved downstream Hermes remediation with useful progress, independent source verification, and one resolved NoticePlace receipt containing elapsed time and trace IDs.

### Safeguards and excluded hypotheses

- No source, runtime, deployment, restart, Telegram, Hermes, provider, or external-system state was modified or called by this audit. Tests used disposable/local state; preflight used bounded read-only probes and returned no external send.
- The current local heartbeat finding is not reported as an unfixed source defect: it is covered by the current source tests. The acceptance defect is that the live runtime still contains the pre-fix path.
- Passing preflight, service health, local tests, queued deliveries, deterministic fakes, and the disposable diagnosis canary are explicitly excluded as substitutes for user selection, remediation, independent verification, or a resolved business receipt.

### QUESTIONS_FOR_L

- Will the current live source/runtime digest mismatch be treated as an acceptance blocker, requiring an explicitly approved backup-first deployment/restart and post-apply digest verification before any live business canary?
- What exact approved external action will expose the real Health topic/card and permit one bounded user selection/remediation canary? A guessed thread, general-chat fallback, synthetic send, or Hermes egress without that gate remains out of scope.

### Alternatives

1. After the explicit deployment/restart gate, deploy the reviewed `4897d9a` runtime set, verify the changed live digests and post-apply service state, then run one fresh user-facing Health canary and collect real selection, remediation, verification, and resolved-receipt evidence.
2. Keep the route at technical-preview/blocked-acceptance status; make no deployment, Telegram send, or Hermes egress, and wait for both live runtime parity and the operator-owned Health topic/user surface.

### Minimum proof to proceed

- An approved deployment makes the live `core.py` and `gptadmin_agent.py` digests equal to the reviewed source, with post-apply verification showing the heartbeat guard and receipt sanitization in the running path.
- A fresh supported user-facing canary visibly shows the Health topic/card and exactly three plans, followed by one durable explicit selection.
- The approved real downstream path emits useful non-heartbeat progress, performs the requested Hermes/OpenAI/Codex/gpt-5.6-luna/high remediation, independently verifies the original source with matching fingerprint and distinct verifier, and persists one resolved NoticePlace receipt with elapsed time and trace IDs. Evidence must identify real transports and user action.

### Verdict

`STOP` — current local tests and readiness probes pass, but the live safety code is demonstrably stale and the real user-facing selection, downstream remediation, independent verification, and resolved business receipt remain unproven. Do not claim completion or readiness until runtime parity and the explicitly approved live canary supply that evidence.
