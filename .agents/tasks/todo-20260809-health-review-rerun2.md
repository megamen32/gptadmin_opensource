# Fresh Reviewer gate 2: health incident autopilot after durable fixes

Role: Reviewer
Status: todo
Owner: fresh independent Reviewer gate 2
Parent task: work-20260809-health-monitoring-orchestration.md

Read-only independent gate. Do not edit product code, secrets, runtime state,
or external systems; do not send Telegram. Inspect only the selected health
paths and append a concise PASS or CHANGES_REQUIRED verdict with evidence.

Verify:

1. Two separate `NotificationCenter` instances racing different signed plan
   selections produce one durable winner; selection before plan attachment is
   rejected.
2. Empty/heartbeat-only progress is not useful and cannot satisfy resolve;
   useful progress plus independent matching healthy verification is required.
3. An original source fingerprint is preserved and a mismatched verification
   is rejected.
4. `/webhook-jobs/{id}/progress` GET remains read-only; Hermes resolution
   actor/receipt fields are bounded and secret-prefix values are rejected.
5. The canary output identifies real local versus fake downstream transports
   and explicitly says no external send occurred.

Use bounded local commands only in these paths:
`/home/admin/ServersAdministartion/automation/health-incident-monitor`,
`/home/admin/agents-projects/noticeplace`,
`/home/admin/gptadmin/go-hub/internal/hub`,
`/home/admin/agents-projects/agent-herder`,
`/home/admin/agents-projects/hermes-config/plugins/agent-herder-bridge`,
and `/home/admin/gptadmin/tests/e2e/health_incident_vertical_canary.py`.

## Independent review evidence (2026-08-09, English)

Scope reviewed: only the health-monitor, NoticePlace, GPTAdmin Hub,
Agent-Herder, Hermes bridge, and vertical-canary paths named above. No product
code, secret, runtime state, external system, or Telegram transport was
changed or contacted.

Passing checks:

- Two separate `NotificationCenter` instances raced different signed callback
  selections for 12 bounded iterations: every iteration produced exactly one
  accepted winner and one `health.plan_selected` row. Selection before plan
  attachment was rejected with `unknown health plan`. The durable primary-key
  gate is in `noticeplace/notification_center/core.py:1152-1196` and callback
  decoding is in `noticeplace/notification_center/health_workflow.py:216-226`.
- The original source fingerprint was preserved as `fp-original`; a healthy
  verification carrying `fp-other` was rejected, while a matching independent
  verification resolved after useful progress. Relevant checks are
  `noticeplace/notification_center/health_workflow.py:85-87` and
  `noticeplace/notification_center/core.py:1382-1410`.
- Hub progress GET is rejected as method-not-allowed before the progress
  handler at `go-hub/internal/hub/webhook_gateway.go:301-307`; the focused
  Hub package test passed.
- Hermes adversarial checks rejected `fingerprint=token:raw` and a 129-byte
  session actor. Resolution fields are validated/bounded at
  `hermes-config/plugins/agent-herder-bridge/__init__.py:23-25,84-100,162-178,276-294`;
  the bridge suite passed (`10 passed`).
- The vertical canary passed and its stdout explicitly reported
  `health_producer=real_local`, `noticeplace_http=real_local`, downstream
  GPTAdmin/OmniRoute/Agent-Herder as `fake`, Telegram as `fake_no_send`, and
  `external_sends=false`. NoticePlace focused tests passed (`39 passed`).

### CHANGES_REQUIRED — heartbeat-only progress remains a resolution bypass

Bounded local reproduction: after plan selection, the first receipt with
`heartbeat_at=1`, `step="heartbeat"`, empty evidence, and
`progress_fingerprint="heartbeat-only"` returned and persisted
`useful=true`; after an independent matching healthy verification,
resolution returned `resolved`. This violates the required heartbeat-only
gate. `notification_center/core.py:1243-1255` treats any non-empty step or
fingerprint as useful and does not exclude a heartbeat-only receipt; the
resolution gate accepts the persisted flag at `notification_center/core.py:1344-1348`.
The wrapper can also report the first receipt as useful through
`notification_center/health_workflow.py:247-258`.

Smallest in-scope fix: classify an explicitly heartbeat-only receipt as
`useful_progress=false` both in its durable payload and returned response, and
add a regression proving that such a receipt plus matching verification cannot
resolve; a later genuinely useful step/evidence/fingerprint receipt must still
be able to resolve.

Focused evidence also included the vertical canary (exit 0), NoticePlace
health/API/GPTAdmin tests (`39 passed`), Hermes bridge tests (`10 passed`),
and `go test ./internal/hub` (passed). No production or real Telegram canary
was attempted; that boundary remains pending approval and is not claimed green.

Verdict: `CHANGES_REQUIRED`.
