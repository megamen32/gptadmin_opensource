# Lazy child MCP health final review

## Role

Reviewer. Independent read-only final review after the stopped-stdio and disabled-child fixes.

## Contract

Check the current health implementation, process/protocol semantics, heartbeat payload, lazy single-flight refresh, cancellation, timestamps, error redaction, and blackbox tests. Return severity-ranked findings and verdict. Do not edit source.

## Scope

Current worktree changes under `go-shellmcp` plus Hub child health propagation.

## Reviewer evidence (2026-08-07)

- Reviewed read-only: `go-shellmcp/internal/server/server.go`, `supervisor_handler.go`, `server_health_blackbox_test.go`, related server tests, `internal/mcpclient/client.go`, and Hub child propagation in `go-hub/internal/hub/server.go` plus its tests.
- Validation passed: `go test -count=1 ./...` in `/home/admin/gptadmin/go-shellmcp`; `go test -count=1 ./...` in `/home/admin/gptadmin/go-hub`; `go test -count=1 -race ./internal/server ./internal/mcpclient`; and `git diff --check` for the scoped trees.
- No P0/P1 implementation finding: disabled children are filtered from Hub direct publication; enabled child routing remains parent-mediated; stopped stdio children are reported without starting a process; health snapshots are atomic and refresh launch is guarded by `CompareAndSwap`; zero runtime values become JSON null; health errors are reduced to `health check failed`.
- P2 coverage finding: `go-shellmcp/internal/server/server_health_blackbox_test.go:10-131` covers a ready stdio child and heartbeat/capabilities propagation, but does not black-box assert disabled-child state, remote-child state, null timestamp/PID/exit fields, cancellation while a health check is in flight, or overlapping refresh single-flight behavior. These are explicit parts of the review contract and remain unverified by black-box evidence (some are only indirectly covered by unit tests).
- Unverified lifecycle assumption: `startLazyMCPHealthRefresh` receives the caller-owned `ListenAndServeContext` but `Server.Close` does not wait for or cancel an in-flight health goroutine. This is safe only if shutdown always cancels the serving context before/with `Close`; no test documents that ordering.

Verdict: `CHANGES_REQUIRED` — add focused black-box coverage for the contract cases above and document or enforce the health-refresh shutdown ordering. No source edits made.
