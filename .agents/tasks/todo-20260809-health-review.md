# Independent Reviewer gate: health incident autopilot

Role: Reviewer
Status: done
Owner: fresh independent Reviewer
Parent task: work-20260809-health-monitoring-orchestration.md

## Objective

Review the selected Normal-plan implementation against the immutable business
acceptance contract:

degradation -> one deduplicated incident -> diagnosis -> exactly three plans ->
explicit user choice -> controlled remediation -> useful-progress supervision
-> independent verification of the original signal -> resolved NoticePlace
receipt with elapsed time and trace IDs.

## Read-only scope

Inspect the selected diffs and focused evidence in:

- `/home/admin/ServersAdministartion/automation/health-incident-monitor/`
- `/home/admin/agents-projects/noticeplace/notification_center/` and its
  health/API/GPTAdmin tests
- `/home/admin/gptadmin/go-hub/internal/hub/webhook_gateway.go` and tests
- `/home/admin/agents-projects/agent-herder/src/health-progress.ts`,
  `src/web/server.ts`, and API tests
- `/home/admin/agents-projects/hermes-config/plugins/agent-herder-bridge/`
- `/home/admin/gptadmin/tests/e2e/health_incident_vertical_canary.py`

Do not edit product code, runtime state, secrets, or external systems. Do not
send Telegram messages. Record evidence and a verdict in this task file only.

## Required verdict

Report PASS or BLOCKED with severity-ranked findings, exact file/line evidence,
whether the vertical canary proves the claim, and the smallest required fixes.

## Independent Reviewer evidence (2026-08-09)

Reviewed only the selected health implementation paths listed above and the
focused worker evidence; unrelated dirty worktree paths were not investigated
or touched.

### Verification

- `python3 tests/e2e/health_incident_vertical_canary.py` passed and returned one
  local incident, three plans, useful progress `[true, false]`, resolved state,
  `elapsed_ms=4210`, and three trace refs.
- Health producer focused suite: `5 passed`.
- NoticePlace health/API/GPTAdmin focused suite: `34 passed`.
- GPTAdmin Hub package suite: `go test ./internal/hub` passed.
- Agent Herder typecheck plus focused API suite: `5 passed`.
- Hermes Agent-Herder bridge suite: `8 passed`.

### Findings, ordered by severity

1. **BLOCKER — the vertical canary is not proof of the requested real
   cross-system/user-facing business path.** The canary explicitly substitutes
   `FakeAgentHerder`, `FakeOmniRoute`, and `FakeGPTAdmin`
   (`tests/e2e/health_incident_vertical_canary.py:1-7,35-71`) and supplies a
   mock Telegram API (`:132-140`). It posts one selected disk event
   (`:103-105`), not all five producer events, and never exercises the real
   GPTAdmin webhook, Agent Herder HTTP endpoint, Hermes bridge, provider/model,
   or Telegram delivery. Therefore the passing canary proves only the local
   fixture/NoticePlace state seam; it does not prove the immutable business
   acceptance contract. Smallest fix: run a separately approved disposable
   target through the actual producer, GPTAdmin webhook, Agent Herder/Hermes
   consumer, health Telegram topic, and final NoticePlace receipt, or mark the
   release blocked until those boundaries are safely enabled and evidenced.

2. **HIGH — resolution does not enforce the required explicit choice and
   useful-progress/remediation gates.** `resolve_health_incident` checks only
   intake, source identity, verification identity, and `healthy`
   (`/home/admin/agents-projects/noticeplace/notification_center/core.py:1284-1320`);
   it never requires a selected plan or useful progress. Progress itself can be
   recorded without a selection (`:1154-1214`). The focused test resolves after
   plan attachment and verification without selecting a plan or recording
   progress (`/home/admin/agents-projects/noticeplace/tests/test_health_workflow.py:146-167`).
   Smallest fix: require the durable selected plan and at least one useful
   progress receipt for that plan before controlled remediation/resolution, and
   add a regression test for both rejection paths.

3. **MEDIUM — the GPTAdmin progress endpoint mutates state on GET.**
   `webhookJobEndpoint` accepts both GET and POST and routes any `/progress`
   suffix to `handleWebhookJobProgress`
   (`/home/admin/gptadmin/go-hub/internal/hub/webhook_gateway.go:296-333`),
   while that handler appends progress and persists state
   (`:337-400`). A GET carrying a valid JSON body can therefore create a
   progress receipt, contrary to the read-only GET/job-status and POST-update
   contract. Smallest fix: reject `isProgress && r.Method != POST` with 405
   before reading/mutating the body, and add a focused GET-progress regression.

### Unverified assumptions and verdict

Production route/topic/token configuration, real outbound Telegram delivery,
installed provider/model authorization, and the actual cross-system execution
consumer remain intentionally unexecuted approval boundaries. The focused
tests and local canary are green, but they cannot close the proof gaps above.

**Verdict: BLOCKED (CHANGES_REQUIRED).**

## Lead remediation

- Added atomic lock coverage for first-winner plan selection plus a concurrent
  two-callback regression test.
- Resolution now requires explicit plan selection and at least one useful
  receipt for that selected plan; heartbeat-only latest entries do not erase a
  prior useful receipt.
- GPTAdmin progress GET is now read-only and returns 405 for `/progress`.
- The remaining real cross-system/Telegram canary is intentionally an external
  approval boundary, not silently reclassified as local proof.

## Final concise handoff (2026-08-09)

**BLOCKED.** The local vertical canary passed, but it uses test doubles and
does not prove the real GPTAdmin → Agent Herder/Hermes → Telegram → NoticePlace
path. Top implementation findings remain: (1) NoticePlace resolve bypasses
plan-selection/useful-progress gates (`notification_center/core.py:1284-1320`),
(2) GPTAdmin `/progress` can mutate through GET
(`go-hub/internal/hub/webhook_gateway.go:296-400`), and (3) the canary posts
only one of the five generated source signals (`tests/e2e/health_incident_vertical_canary.py:103-105`).
No further exploration or changes were made.
