# Worker lane: Agent Herder useful-progress surface

Role: Worker
Status: done
Owner: Worker
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Add a read-only Agent Herder progress endpoint that exposes useful progress for
a session rather than only process heartbeat: session status, last activity,
message/tool activity, a stable progress fingerprint, and bounded evidence
references. Reuse existing SessionSupervisor details APIs.

## Allowed write set

- New `/home/admin/agents-projects/agent-herder/src/health-progress.ts`
- `/home/admin/agents-projects/agent-herder/src/web/server.ts`
- New or focused tests under `/home/admin/agents-projects/agent-herder/tests/`
- Do not edit the already modified `src/session-supervisor.ts`, runtime files,
  deploy files, or unrelated tasks.

## Required behavior

- GET `/api/sessions/:harness/:id/progress` is read-only and bounded.
- Useful progress changes only when status/message/tool/evidence fingerprint
  changes; a timestamp-only heartbeat is false.
- No raw credentials or unbounded transcript output.
- Preserve all existing routes and TypeScript public types.

## Acceptance check

Add focused red tests first, then prove green tests for unchanged heartbeat,
new message/tool activity, stable fingerprint, missing session, and bounded
response. Run the narrow test plus the package typecheck/build if available.

## Budget and stop conditions

- Model: gpt-5.4-mini, low reasoning
- Active minutes: 35 / 70 / 140
- Relative cost: low/medium; disjoint endpoint and helper
- Stop if `src/session-supervisor.ts` or runtime/deploy state is required;
  return NEEDS_REDECOMPOSITION.

## Report contract

Append detailed evidence and changed paths to this task file. Return L only
TL;DR, test commands, and any compatibility assumption. Do not stage or commit.

## Progress report

Implemented a read-only `/api/sessions/:harness/:id/progress` endpoint that reuses `SessionSupervisor.getSessionDetails()` and derives a bounded useful-progress summary instead of a timestamp-only heartbeat.

Changed paths:

- `/home/admin/agents-projects/agent-herder/src/health-progress.ts`
- `/home/admin/agents-projects/agent-herder/src/web/server.ts`
- `/home/admin/agents-projects/agent-herder/tests/http-api.test.ts`

Behavior covered:

- 404 remains for missing sessions.
- Progress response includes session status, lastActivity, message/tool activity, bounded evidence refs, and a stable fingerprint.
- Fingerprint ignores timestamp-only heartbeat churn.
- Evidence output is bounded and does not expose raw transcripts or credentials.

Verification:

- `npm test -- --run tests/http-api.test.ts`
- `npm run build`

Result:

- Both commands passed.

## Final integration evidence

- Added secret-like preview redaction for message/tool evidence and session
  `lastMessage`; the useful fingerprint still ignores timestamp-only churn.
- Source/type verification: `npx tsc && npx vitest run --root . tests/http-api.test.ts` → 2 files, 5 tests passed.
- The package-level `npm test` remains blocked by an unrelated concurrent Vite
  UI tree missing `src/web-ui/main.tsx`, recorded in
  `todo-20260809-herder-build-ui-root.md`.
