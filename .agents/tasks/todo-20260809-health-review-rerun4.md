# Fresh Reviewer gate 4: final health incident acceptance

Role: Reviewer
Status: done
Owner: fresh independent Reviewer gate 4
Parent task: work-20260809-health-monitoring-orchestration.md

Read-only gate after the HTTP wrapper and Hermes progress-actor fixes. Do not
edit product code, secrets, runtime state, or external systems; do not send
Telegram. Append PASS or CHANGES_REQUIRED with exact bounded evidence.

Verify the selected implementation paths:

- independent NoticePlace instances have one durable plan winner and no
  pre-attachment selection;
- HTTP heartbeat-only progress (`heartbeat_at`, `step=heartbeat`, empty
  evidence/fingerprint) is rejected and leaves no progress event;
- useful progress plus independent matching healthy verification resolves;
- source fingerprint guard, Hub GET read-only behavior, bounded Hermes
  progress/resolution actors, secret-prefix rejection;
- canary reports real local/fake transports and `external_sends: false`.

No production or external delivery actions.

## Independent Reviewer gate 4 evidence (2026-08-09)

Reviewed the current checkout only within the bounded health producer,
NoticePlace, GPTAdmin Hub, Hermes bridge, and vertical-canary paths. No product
code, secrets, runtime state, external system, or Telegram transport was
changed or contacted.

Passing evidence:

- NoticePlace focused suite: `python3 -m pytest
  tests/test_health_workflow.py tests/test_http_api.py
  tests/test_gptadmin_agent.py -q` -> `39 passed`. Separate
  `NotificationCenter` instances race different selections and leave exactly
  one durable `health_plan_selections` row; the primary-key gate is declared
  at `noticeplace/notification_center/core.py:185-190`, with regressions at
  `noticeplace/tests/test_health_workflow.py:153-172`. Selection before plan
  attachment is rejected at `noticeplace/tests/test_health_workflow.py:196-198`.
- Heartbeat-only HTTP progress is rejected before persistence: the workflow
  rejects `heartbeat_at` with `step=heartbeat` and empty evidence/fingerprint
  at `noticeplace/notification_center/health_workflow.py:243-260`, and the
  regression asserts HTTP 400 plus zero `health.progress` rows at
  `noticeplace/tests/test_http_api.py:149-161`. The direct workflow regression
  also asserts no event row at `noticeplace/tests/test_health_workflow.py:174-194`.
- Useful progress plus an independent healthy verification resolves; missing,
  wrong-source, and mismatched-fingerprint verification are rejected. The
  source-fingerprint regression is at `noticeplace/tests/test_health_workflow.py:200-223`,
  and the successful independent resolution with elapsed/trace receipt is at
  `noticeplace/tests/test_health_workflow.py:266-298`. Core resolution requires
  selected plan, useful progress, and matching independent verification at
  `noticeplace/notification_center/core.py:1342-1368`.
- GPTAdmin Hub progress GET is method-gated to 405 before any update handler at
  `gptadmin/go-hub/internal/hub/webhook_gateway.go:296-309`; `go test
  ./internal/hub` passed. Hermes bridge tests passed (`10 passed`), including
  secret-prefix rejection and 129-character progress/resolution session actor
  rejection at `hermes-config/plugins/agent-herder-bridge/tests/test_plugin.py:213-229`.
  Resolution bodies are capped at 4096 bytes at
  `hermes-config/plugins/agent-herder-bridge/__init__.py:164-180`.
- Health producer focused suite passed (`5 passed`). The disposable vertical
  canary exited 0 with `signals_seen=5`, one incident, three plans, useful
  progress `[true, false]`, resolved status, elapsed time, and trace refs. Its
  output reports `health_producer=real_local`, `noticeplace_http=real_local`,
  GPTAdmin/OmniRoute/Agent-Herder as `fake`, Telegram as `fake_no_send`, and
  `external_sends=false`; provenance is declared in
  `tests/e2e/health_incident_vertical_canary.py:1-6,177-194`.

Unverified assumptions / explicit boundary: this remains a local fixture canary;
real production routing, installed provider/model authorization, and external
Telegram delivery were intentionally not attempted and are not claimed by this
gate.

Verdict: PASS (bounded local acceptance gate; external delivery remains an
explicit approval-boundary verification).
