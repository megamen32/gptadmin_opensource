# Lazy child MCP health review

## Role

Reviewer. Independent and read-only.

## Contract

Review the lazy child health refresh and heartbeat payload. Check concurrency, cancellation, child process/protocol distinction, zero timestamps, error redaction, disabled/remote children, and blackbox coverage. Do not edit source. Return severity-ranked findings and verdict.

## Scope

`go-shellmcp/internal/server/server.go`, `supervisor_handler.go`, `server_health_blackbox_test.go`, related tests, and `internal/mcpclient` status behavior.

## Stop condition

Stop after independent review.

## Reviewer evidence (2026-08-07)

- Reviewed selected implementation in `go-shellmcp/internal/server/server.go` and `supervisor_handler.go`, the new black-box test, related server tests, and `internal/mcpclient` status/ListTools behavior. No source edits made.
- Validation: `go test ./internal/server ./internal/mcpclient` passed; `go test -race ./internal/server ./internal/mcpclient` passed; `git diff --check` passed.
- The implementation separates disabled/remote/stdio process states, converts zero timestamps and absent PID/exit code to JSON null, and redacts the raw ListTools error. The atomic health snapshot plus heartbeat `CompareAndSwap` guard did not show a race in the race run.
- P2 coverage finding: `go-shellmcp/internal/server/server_health_blackbox_test.go:10-97` covers only one successful stdio child and one failure path indirectly through unit tests. It does not black-box assert disabled children, remote children, zero timestamp/null fields, cancellation during a 15-second check, or overlapping heartbeat refreshes. These are explicit parts of the task contract and remain unverified.
- Unverified assumption: `ListTools` starts a stopped enabled stdio child (`go-shellmcp/internal/mcpclient/client.go:216-224, 286-344`), so the health refresh reports the post-check process state and can launch configured children. This may be intended by the lazy-refresh design, but the task record does not state that side effect as an inclusion.

Verdict: CHANGES_REQUIRED — add focused black-box coverage for the contract cases above and explicitly confirm whether refresh is allowed to start stopped stdio children.
