# Fresh Reviewer gate 3: final health incident hardening

Role: Reviewer
Status: todo
Owner: fresh independent Reviewer gate 3
Parent task: work-20260809-health-monitoring-orchestration.md

Read-only final gate after the heartbeat-only fix. Do not edit product code,
secrets, runtime state, or external systems; do not send Telegram. Append a
concise PASS or CHANGES_REQUIRED verdict with exact evidence.

Check the selected paths for:

1. Two independent NoticePlace instances have one durable plan-selection
   winner, and selection before plan attachment is rejected.
2. Progress with `heartbeat_at` and `step=heartbeat` but empty evidence and
   fingerprint is not useful and cannot resolve; useful progress plus an
   independent matching healthy verification can resolve.
3. Source fingerprint preservation/rejection, Hub progress GET read-only,
   bounded Hermes actor/receipt and secret-prefix rejection.
4. Canary provenance says which transports are real local/fake and says no
   external sends.

Use only the bounded health paths from the parent task. No production or
external actions.

## Independent Reviewer gate 3 evidence (2026-08-09, English)

Review scope was limited to the selected health-monitor, NoticePlace,
GPTAdmin Hub, Hermes bridge, and vertical-canary paths. No product code,
secrets, runtime state, external system, or Telegram transport was changed or
contacted. Per the user's stop instruction, no additional exploratory command
was run after the bounded code/test evidence already inspected.

Passing bounded evidence:

- Durable NoticePlace core classifies `heartbeat_at` plus `step=heartbeat`
  with empty evidence and fingerprint as `heartbeat_only`, and persists
  `useful=false` / `useful_progress=false` at
  `noticeplace/notification_center/core.py:1243-1267`. The selected regression
  is present at `noticeplace/tests/test_health_workflow.py:175-187` and checks
  that resolution remains rejected without useful progress.
- Durable plan selection has the `health_plan_selections.incident_id PRIMARY
  KEY` winner gate at `noticeplace/notification_center/core.py:185-190`, and
  the separate-instance and pre-attachment regressions are present at
  `noticeplace/tests/test_health_workflow.py:154-201`.
- Source fingerprint preservation and matching verification are covered by
  `noticeplace/notification_center/health_workflow.py:81-87` and
  `noticeplace/notification_center/core.py:1390-1410`.
- Hub progress GET is rejected before the progress mutation handler at
  `go-hub/internal/hub/webhook_gateway.go:302-307`. Hermes opaque values reject
  secret prefixes and overlong actors at
  `hermes-config/plugins/agent-herder-bridge/__init__.py:84-90,292-300`, and
  direct resolution bodies are bounded at `:163-178`.
- The canary declares local versus fake transports and
  `external_sends=false` at
  `tests/e2e/health_incident_vertical_canary.py:1-6,180-195`.

### CHANGES_REQUIRED — wrapper reports heartbeat-only progress as useful

The durable core fix is not propagated faithfully through the real
NoticePlace workflow/HTTP consumer. `HealthWorkflow.record_progress()` uses
`useful_progress(previous, ...)`, whose `previous is None` branch returns true
at `noticeplace/notification_center/health_workflow.py:150-157`, then
overwrites the core result with `{"useful_progress": useful}` at
`noticeplace/notification_center/health_workflow.py:247-258`. The HTTP route
calls this wrapper at `noticeplace/notification_center/http_api.py:633-643`.
Therefore the first heartbeat-only receipt can be durably stored with
`useful=false` while the user-facing/API `useful_progress` field still claims
`true`. Resolution remains blocked by the durable flag, but the liveness
consumer receives a false useful-progress signal, so the requested contract is
not fully proven on the selected path.

Smallest in-scope fix: return the core's authoritative `useful_progress` value
without overriding it, or apply the same heartbeat-only predicate in the
wrapper, and add an HTTP/workflow regression asserting both returned flags are
false for the first empty heartbeat receipt and resolution remains rejected.

Verdict: `CHANGES_REQUIRED`. The durable target fix and the other bounded gates
pass by the evidence above, but the integrated workflow response still violates
the heartbeat-only useful-progress contract.
