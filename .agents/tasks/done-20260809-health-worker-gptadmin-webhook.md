# Worker lane: GPTAdmin webhook correlation and progress

Role: Worker
Status: done
Owner: Worker
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Extend the existing GPTAdmin webhook job state with bounded health correlation,
useful-progress receipts, and trace references. Keep the signed/idempotent
webhook path and approval modes intact.

## Allowed write set

- `/home/admin/gptadmin/go-hub/internal/hub/webhook_gateway.go`
- `/home/admin/gptadmin/go-hub/internal/hub/webhook_gateway_test.go`
- Do not edit route manifests, secrets, runtime state, deployment files, or
  unrelated dirty paths.

## Required behavior

- Capture bounded `correlation_id` and opaque trace refs from a health event.
- Expose progress in GET `/webhook-jobs/{job_id}`.
- Add authenticated/idempotent POST progress updates for a non-terminal job;
  reject malformed/unbounded updates and reject updates after terminal state.
- Useful progress requires step/evidence/fingerprint change; timestamp-only
  heartbeat must not claim progress.
- Preserve existing route validation, HMAC/idempotency, approval modes, callback
  semantics, and secret-safe error behavior.

## Acceptance check

Write focused red tests first, then prove green tests for correlation capture,
progress update/auth/idempotency, useful-progress classification, terminal
rejection, and existing webhook behavior. Run the narrow Go package tests.

## Budget and stop conditions

- Model: gpt-5.4-mini, low reasoning
- Active minutes: 45 / 90 / 180
- Relative cost: medium; durable job-state integration
- Stop if another dirty file or a schema/provider/deploy action is needed;
  return NEEDS_REDECOMPOSITION.

## Report contract

Append detailed evidence and changed paths to this task file. Return L only
TL;DR, test commands, and integration assumptions. Do not stage or commit.

## Evidence log

- Changed paths:
  - `/home/admin/gptadmin/go-hub/internal/hub/webhook_gateway.go`
  - `/home/admin/gptadmin/go-hub/internal/hub/webhook_gateway_test.go`
- Implementation notes:
  - Added bounded webhook job receipts for `correlation_id`, `trace_refs`,
    `progress`, `last_progress_at`, `progress_fingerprint`, and
    `useful_progress`.
  - Captured health metadata from incoming webhook events using top-level and
    nested `health.*` fields.
  - Added authenticated/idempotent `POST /webhook-jobs/{job_id}/progress`
    handling with malformed, oversized, duplicate, and terminal-state rejection.
  - Preserved existing route auth, HMAC verification, callback dispatch, and
    secret-redacted error responses.
- Test commands:
  - `cd /home/admin/gptadmin/go-hub && go test ./internal/hub -run 'TestWebhookGateway(CapturesCorrelationAndTraceRefsFromHealthEvent|ProgressUpdateRequiresUsefulChangeAndRejectsTerminalJob|ProgressRejectsMalformedAndOversizedPayloads|ProgressGETIncludesJobReceipts|RejectsMissingOrInvalidHMAC|RouteSupportsOrderedMCPActionsWithDelay|RendersJSONAndDispatchesConfiguredShell|ShellTemplateValuesCannotBecomeShellSource|IdempotencyReturnsOriginalJob|DeliversConfiguredCallback|SignatureFormatUsesRawBody|V2SignatureBindsMethodPathAndIdempotencyKey|ShellResultRequiresSuccessfulExit|TemplatesPreserveTypedJSONValues|RoutesCRUDPersistsWithoutExposingSecrets|JobsSurviveRestart|JobEndpointRequiresRouteAuth|CallbackRetriesWithBoundedAttempts)'`
  - `cd /home/admin/gptadmin/go-hub && go test ./internal/hub`

## Final integration evidence

- Added duplicate-progress regression coverage so a replayed idempotency key
  returns `useful_progress: false`.
- Root verification: `gofmt -w internal/hub/webhook_gateway.go
  internal/hub/webhook_gateway_test.go && go test ./internal/hub` → green.
