# Research lane: GPTAdmin, NoticePlace, and Agent Herder

Role: Explorer
Status: complete
Owner: Explorer
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Trace the current incident intake, NoticePlace notification/approval, GPTAdmin
dispatch, Agent Herder handoff, idempotency, progress, and completion paths. This
is read-only research only.

## Allowed paths

- `/home/admin/gptadmin`
- `/home/admin/agents-projects/noticeplace`
- `/home/admin/agents-projects/agent-herder`
- Directly referenced user-facing docs or tests in those roots.

## Excluded paths and actions

- Do not edit source, configs, task files, generated files, or service state.
- Do not send Telegram/Matrix/phone messages, call external endpoints, restart,
  deploy, change secrets/providers, or inspect secret values.
- Do not treat unit tests or historical task notes as live business proof.

## Acceptance check

Return a call-stack/evidence table from an incoming event to NoticePlace and the
Agent Herder job, including real endpoint/tool names, auth/approval boundary,
deduplication/idempotency key, progress/liveness signal, completion/failure
callback, trace storage, and exact gaps for the requested health incident.
Separate proven implementation from documentation-only references.

## Budget and stop conditions

- Model: gpt-5.6-luna explorer, low reasoning
- Active minutes: 30 / 55 / 110
- Relative cost: low; read-only search and bounded file inspection
- Stop when the call-stack and gap table are proven or when the next fact
  requires an external mutation or a new architectural choice.
- Return `NEEDS_REDECOMPOSITION` if the scope expands into execution adapters.

## Report contract

Append detailed evidence and result to this file. Return L only a TL;DR with
status, proven consumer chain, approval/liveness facts, and exact blockers.

## Explorer evidence (2026-08-09)

### Proven call-stack

| Stage | Evidence | Finding |
|---|---|---|
| Incoming NoticePlace event | `noticeplace/notification_center/http_api.py:568-576`; `core.py:529-613` | `POST /v1/events` requires bearer auth and `Idempotency-Key`; event is validated, persisted in SQLite, incident deduplicated by `(project, recipient, consumer_id, dedup_key)` while unresolved, and an optional `agent_job` delivery is scheduled. |
| NoticePlace authorization | `core.py:543-551` | `agent_job` must be in the token scope's `agent_jobs`; unauthorized jobs are rejected before scheduling. |
| Delivery identity / claim | `core.py:673-683`, `core.py:803-808` | Delivery key is deterministic: `{incident_id}:{channel}:{step}`; existing key returns same delivery. Worker claims queued or expired claimed rows with lease semantics. |
| NoticePlace -> GPTAdmin | `http_api.py:399-410`; `gptadmin_agent.py:141-181` | Channel `gptadmin.agent:<job>` selects only configured adapter. It POSTs signed JSON to `/webhooks/v1/<route>`, using `Idempotency-Key = delivery_key`, then polls `/webhook-jobs/{job_id}` until `completed` or `failed`. |
| GPTAdmin webhook ingress | `gptadmin/go-hub/internal/hub/webhook_gateway.go:191-260` | Route authenticates token/HMAC, derives delivery identity from Idempotency-Key / provider IDs / body hash, rejects changed payload under reused key (409), persists accepted job and launches async execution. |
| GPTAdmin dispatch -> Agent Herder | `noticeplace/notification_center/agent_job_helper.py:83-125`; Herder `src/web/server.ts:139-154` | The fixed host-local profile POSTs to `http://127.0.0.1:.../api/sessions/new-or-resume` with allowlisted harness/name/CWD/mode/fixed instruction plus untrusted telemetry. Herder returns `sessionId`, `created`, and delivery state. |
| Herder idempotency / delivery | `agent-herder/src/named-session.ts:64-118`, `:121-166` | Exact identity is `(harness, canonical cwd, name)`; in-process and file locks serialize concurrent requests. Existing exact session resumes; queue mode returns `delivery: accepted`, sync returns `completed`; send failure preserves session ID and returns `failed`. |
| GPTAdmin terminal state | `gptadmin/go-hub/internal/hub/webhook_gateway.go:389-443` | Job transitions accepted -> running -> completed/failed, with timestamps/result/error persisted. Optional callback is posted after terminal state, retried up to 3 times; callback failure is separately recorded and does not change job terminal status. |
| NoticePlace completion / trace | `http_api.py:404-416`; `core.py:940-957`, `:994-1069` | Terminal receipt is reduced to bounded hub/route/status/session metadata and audit event `agent_job_completed`/`agent_job_failed`; delivery becomes sent/failed or retry. Event history exposes deliveries, results, and audit trail. |

### Approval, liveness, and exact gaps

- GPTAdmin webhook actions pass through `requestWithAutomationProfile` (search result and `webhook_gateway.go` dispatch around lines 454-524). `approval_mode` is constrained to `ask_before_write` or `bounded_autonomous`; the latter is the configured automation boundary. No live approval was exercised. A route configured as `ask_before_write` must be approved before it can queue the write, as documented in `gptadmin/docs/WEBHOOKS.md:75-79,142-145`; this is documentation plus implementation policy coverage, not live proof.
- NoticePlace worker liveness is only a local readiness signal: `core.py:1088-1091` updates dispatcher heartbeat and `:1120-1129` marks it ready if heartbeat is <=30s and SQLite is available. Delivery leases provide recovery, but there is no per-agent-job heartbeat/progress stream.
- GPTAdmin exposes only coarse job states and timestamps (`accepted`, `running`, `completed`, `failed`) and the polled terminal result (`webhook_gateway.go:72-81,389-418`). Agent Herder queue mode exposes accepted delivery, not task progress or completion callback to NoticePlace. The existing callback is GPTAdmin webhook callback to a configured external URL, not an Agent Herder completion callback.
- Trace storage is split: NoticePlace SQLite stores event/incident/delivery/audit history (`core.py:994-1069`); GPTAdmin persists webhook state at its configured `webhook_state.json` (referenced by `webhook_store.go:25-32`); Agent Herder persists lineage in its configured `lineage.json` and transcript archives under `.agent-herder/transcripts` (README.md:76-82,176-182). There is no proven shared trace/correlation record joining NoticePlace event ID, GPTAdmin job ID, and Herder session ID beyond bounded audit fields.
- The largest incident gap is absence of an end-to-end liveness SLA/watchdog: a queued/running webhook can be polled until adapter timeout, then NoticePlace retries the delivery; Herder's accepted queue may remain active without progress evidence. No notification-specific progress callback, deadline escalation, or automatic reconciliation of orphaned GPTAdmin jobs was found.
- Unit tests were inspected as implementation corroboration only; no tests were treated as live business proof, and no services/endpoints were called.

### Result

Status: COMPLETE for bounded read-only research. Proven chain is NoticePlace `POST /v1/events` -> durable scoped `gptadmin.agent:<job>` delivery -> signed GPTAdmin `/webhooks/v1/<route>` -> durable `/webhook-jobs/{job_id}` -> fixed localhost Agent Herder `/api/sessions/new-or-resume` -> named Herder session -> terminal receipt/audit. Blocker for a health-incident-grade canary is missing per-job progress/liveness and a single cross-system correlation/reconciliation mechanism; closing it requires an architectural choice and implementation, excluded from this Explorer pass.
