# Fresh Reviewer snapshot: NoticePlace health workflow

Status: work

## Role

You are an independent, read-only Reviewer. Review the current shared source
and runtime evidence for the health workflow; do not edit source, deploy,
restart, send Telegram, invoke Hermes egress, or modify external systems.

## Objective

The business contract is host/service/log degradation -> deduplicated
NoticePlace incident -> GPTAdmin/Agent-Herder/OpenCode/OmniRoute diagnosis ->
exactly three plans -> explicit user choice -> controlled Hermes remediation
with useful progress -> independent matching source verification -> resolved
NoticePlace receipt with elapsed time and trace IDs.

## Current source snapshot

- NoticePlace HEAD should be `90720c7` (`Guard running heartbeat progress`).
- Previous safety commits are `c2bf01d`, `fa7a1f7`, and `4897d9a`.
- Current local evidence before this review: scoped NoticePlace suite `60
  passed`; Fleet health runtime preview is ready but `90720c7` is not deployed.
- Live evidence: production intake -> GPTAdmin health diagnosis -> exactly
  three plans is proven with Telegram/Hermes disabled; selection, remediation,
  independent verification, and resolved receipt are not proven.

## Review questions

1. Does every running and terminal agent receipt reject heartbeat-labelled
   progress with no evidence, even when a fingerprint is synthesized?
2. Does the adapter return and NoticePlace audit persist only sanitized bounded
   receipt/incident strings, including prefix and Bearer secret forms?
3. Are malformed four/five-part Telegram callbacks handled without escaping
   the poller?
4. Are useful-progress supervision, independent source/fingerprint checks,
   elapsed bounds, exactly-three plans, idempotency, and durable traces intact?
5. Distinguish source/test PASS from the missing real user-facing Telegram
   selection/remediation/resolved business canary.

## Required result

Append detailed evidence and a final PASS or CHANGES_REQUIRED verdict to this
file, then return L only a concise TL;DR. Treat tests and terminal receipts as
insufficient proof of the full business result.

## Reviewer evidence — 2026-08-10

Reviewed only the selected NoticePlace health seams in `c2bf01d^..90720c7`:
`notification_center/core.py`, `notification_center/gptadmin_agent.py`,
`notification_center/health_workflow.py`, `notification_center/http_api.py`,
`notification_center/telegram_interactions.py`, and their scoped tests. No
source, deployment, restart, Telegram send, Hermes egress, or external system
was changed.

Scoped test evidence:

- `python -m unittest discover -s tests -p 'test_health_workflow.py'`: 17/17 OK.
- `python -m unittest discover -s tests -p 'test_gptadmin_agent.py'`: 22/22 OK.
- `python -m unittest discover -s tests -p 'test_http_api.py'`: 13/13 OK.
- The initial `python -m unittest tests.test_health_workflow ...` invocation
  failed only because `tests` is not an importable package; it ran zero tests
  and was corrected with unittest discovery.

Scoped findings, ordered by impact:

1. `[P1]` `notification_center/gptadmin_agent.py:54-71` —
   `HealthProgressSupervisor.observe()` trusts `useful_progress is True`
   without independently rejecting a heartbeat-labelled entry that has no
   evidence. A running receipt such as `step=heartbeat`, empty
   `evidence_refs`, synthetic `fingerprint`, and `useful_progress=True` is
   classified as useful/progressing while the NoticePlace persistence seam
   rejects the same receipt at `notification_center/core.py:1009-1017` and
   `1541-1546`. Repeated such receipts can keep a remediation job from the
   stale-progress stop despite providing no useful work. The smallest fix is
   to filter heartbeat/keepalive/heartbeat-only entries without evidence in
   the supervisor before calculating useful progress, with a regression test
   using `useful_progress=True` and a synthetic fingerprint.

2. `[P1]` `notification_center/gptadmin_agent.py:322-325` — the adapter adds a
   sanitized `agent_receipt` but returns the complete Hub response unchanged,
   including raw nested `result` data. A bounded probe reproduced
   `agent_receipt.session_id == 'secret=[redacted]'` while the returned
   `result.session_id` remained `secret:raw-session`. This violates the
   adapter-return boundary even though the selected `agent_job_completed`
   audit summary is sanitized at `notification_center/core.py:1164-1181`.
   The smallest fix is to return an allowlisted sanitized terminal response
   (or remove raw `result`/raw progress) instead of returning `current`
   wholesale, with an assertion over the serialized adapter result.

Gate status:

- Source and scoped unit/HTTP evidence pass the covered contracts: exact three
  plans, signed callback handling, atomic selection, useful-progress storage,
  independent source/fingerprint checks, bounded elapsed/trace data, and
  idempotent storage paths.
- The independent real user-facing Telegram selection -> Hermes remediation
  -> independent verification -> resolved NoticePlace receipt canary is not
  proven. The task snapshot explicitly says Telegram/Hermes are disabled and
  `90720c7` is not deployed. Therefore this review does not claim the full
  business result.

## Verdict

CHANGES_REQUIRED

The two scoped adapter/supervision findings must be fixed and reviewed again;
the real Telegram/Hermes business canary remains a separate required gate
before release acceptance.
