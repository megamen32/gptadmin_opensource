# Independent Critic gate: health incident autopilot

Role: Critic
Status: done
Owner: fresh independent Critic
Parent task: work-20260809-health-monitoring-orchestration.md

## Objective

Adversarially challenge the Normal-plan implementation and its evidence. Look
for false readiness claims, race/idempotency failures, ways to bypass the
three-plan/user-choice gate, terminal-status-only resolution, heartbeat-only
progress, correlation/trace loss, secret/raw-log leakage, and unsafe Telegram
or production side effects.

## Read-only scope

Inspect only the selected health workflow code, tests, technical preview, and
vertical canary across:

- `/home/admin/ServersAdministartion/automation/health-incident-monitor/`
- `/home/admin/agents-projects/noticeplace/`
- `/home/admin/gptadmin/go-hub/internal/hub/`
- `/home/admin/agents-projects/agent-herder/`
- `/home/admin/agents-projects/hermes-config/plugins/agent-herder-bridge/`

Do not edit product code, runtime state, secrets, or external systems. Do not
send Telegram messages. Record evidence and a verdict in this task file only.

## Required verdict

Return PASS or BLOCKED, with concrete reproductions or proof, severity-ranked
findings, and a precise acceptance impact. Distinguish local synthetic proof
from unexecuted production/external approval boundaries.

## Critic audit receipt — 2026-08-09

### Independently reconstructed done condition

The Normal plan is done only when a real health signal can enter the selected
workflow, exactly three signed plans are offered, one user choice is durable
even under retries/concurrency, progress receipts are useful and secret-safe,
and resolution requires an independent healthy verification receipt. A local
green unit/build result is not sufficient evidence of the production,
Telegram, or external-approval boundary.

### Evidence executed

- Fixture health monitor: `test_health_monitor.py` — 5 passed.
- NoticePlace health workflow and HTTP routes: `test_health_workflow.py`
  plus `test_http_api.py` — 21 passed.
- Hermes Agent-Herder bridge: `tests/test_plugin.py` — 8 passed.
- Agent-Herder build and Vitest suite — 47 files / 180 tests passed.
- Go hub focused webhook suite: `go test ./internal/hub -run
  'TestWebhookGateway' -count=1` — passed.
- No Telegram message, NoticePlace production mutation, or external approval
  was executed. The health README explicitly says the fixture canary did not
  use `--send`; bridge HTTP checks used a fake local response.

### Severity-ranked findings

#### P1 — BLOCKER: permitted bridge fields bypass the secret/raw-log boundary

Evidence: `/home/admin/agents-projects/hermes-config/plugins/agent-herder-bridge/__init__.py:215-250`
validates only type/length for `step`, `fingerprint`, `correlation_id`, and
`evidence_refs`, then forwards those values verbatim both through `_send()` and
the optional direct NoticePlace request. The allowlist removes an extra
`prompt` field, but does not make the allowed fields opaque or redact their
contents.

Reproduction (fake transport, no external request): calling
`record_health_progress` with a synthetic secret/raw-log string in `step` and
`evidence_refs` returned `{"ok": true, "direct": true, ...}` and the captured
direct JSON body contained those exact strings. The `Authorization` value was
only in the header; the failure is the user-controlled content in the body.
The existing bridge test does not exercise adversarial content in an allowed
field, so the green suite does not prove the stated guarantee.

Acceptance impact: a Hermes agent can cause secret or raw tool/log content to
leave the process while the health tool claims a bounded, secret-safe receipt.
This blocks readiness and any production enablement of the bridge health path.

#### P1 — BLOCKER: Telegram plan selection is not atomic

Evidence: `/home/admin/agents-projects/noticeplace/notification_center/core.py:1127-1145`
checks `_health_selected_plan()` before entering the lock/transaction used by
`_record_health_event()`. A two-thread reproduction gated both pre-checks to
return `None`, then submitted `observe` and `repair`; both calls returned
`idempotent: false`, and the database contained two `health.plan_selected`
events. The reported latest selection was whichever event happened to sort
last, so the one-choice/user-gate contract is race-dependent.

Acceptance impact: two concurrent signed Telegram callbacks can produce two
accepted plans and cause downstream work to follow a nondeterministic choice.
The current idempotency tests cover replay of one callback, not concurrent
different callbacks.

#### P1/P2 acceptance gap — no live business canary or external approval proof

The local tests prove synthetic fixtures, fake Telegram/HTTP transports, and
the internal state gates. They do not prove the configured health producer can
reach the real NoticePlace endpoint, that the real Telegram recipient sees
exactly one usable choice, or that an independent verifier/approval is wired
in production. This is an acceptance gap rather than evidence that a live
side effect occurred.

### Excluded hypotheses

- No reviewed command sent Telegram or changed a production service; all
  transport tests were stubbed and the monitor default is side-effect free.
- No finding was raised against the focused Go webhook tests; their selected
  auth/progress cases passed.
- The bridge token itself was not observed in an event body; the defect is
  leakage through fields that the implementation treats as safe references.

### QUESTIONS_FOR_L

- Are `step`, `fingerprint`, `correlation_id`, `trace_refs`, and evidence
  references contractually opaque identifiers? If yes, where is their strict
  format/redaction boundary before `_send()` and the direct NoticePlace POST?
- What is the intended winner when two signed plan callbacks race? The
  acceptance contract needs an atomic first-write/unique constraint proof, not
  only callback replay idempotency.
- Which controlled live canary will prove the real NoticePlace/Telegram path
  and independent verification before production readiness is claimed?

### Two routes to proceed

1. Repair the bridge boundary with strict opaque-reference validation (or
   deterministic redaction), add adversarial secret/raw-log tests, and make
   plan selection check-and-insert atomic with a concurrency regression test;
   then rerun the focused suites and a controlled live canary.
2. Keep this at technical-preview status: disable the bridge health tools and
   do not claim readiness/completion until the two local blockers and the live
   canary are addressed.

### Minimum proof required

Show that adversarial strings never appear in either bridge event or direct
NoticePlace JSON, show a concurrent two-callback test leaves exactly one
`health.plan_selected` event with deterministic replay behavior, and provide
the real consumer-path canary receipt with independent healthy verification.

### Verdict

`STOP` — acceptance status `BLOCKED`. The green local suites are insufficient
because the two P1 reproductions violate the secret-safe and exactly-one-plan
contracts; completion/readiness claims must not proceed.

## Concise closeout — 2026-08-09

Scope closed; no further exploration performed. Existing local canaries remain
green (health 5/5, NoticePlace 21/21, bridge 8/8, Agent-Herder 180/180, Go
webhook suite passed), but they do not cover the two reproductions above.

Top findings: (1) bridge forwards attacker/agent-controlled `step` and
`evidence_refs` verbatim, including secret/raw-log content; (2) plan selection
checks state outside the transaction, allowing two concurrent signed choices;
(3) no real Telegram/production business canary or external approval proof.

Final adversarial verdict: `BLOCKED` (`STOP` under Critic decision codes).

## Lead remediation

- Hermes health progress and resolution tools now accept only strict opaque
  identifier/reference formats and reject synthetic secret/raw content before
  bridge or direct transport; adversarial regression coverage is green.
- Plan selection check-and-insert is serialized under the NotificationCenter
  lock; concurrent different callbacks leave one durable winner.
- Production/external canary remains explicitly pending approval and is not
  claimed by the local synthetic canary.
