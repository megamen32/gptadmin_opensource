# Lazy child MCP health final lifecycle review

## Role

Reviewer. Fresh, read-only gate after the Server.Close cancellation fix.

## Contract

Review health refresh concurrency/lifecycle, process/protocol semantics, heartbeat serialization, and blackbox coverage. Return severity-ranked findings and verdict only.

## Reviewer evidence — 2026-08-07

Scope reviewed: the selected lazy MCP health/lifecycle changes in `go-shellmcp/internal/server/server.go`, `server_test.go`, `server_health_blackbox_test.go`, `supervisor_handler.go`, and heartbeat `Beat` catalog changes in `internal/hub/client.go` plus its test. Unrelated dirty worktree paths were not reviewed or touched.

Validation:

- `go test ./internal/server ./internal/mcpclient ./internal/hub` passed.
- `go test -race ./internal/server ./internal/mcpclient ./internal/hub` passed.
- `go test ./...` passed.
- `git diff --check` passed for the selected tracked source paths.
- Existing tests cover ready/stopped/disabled health, black-box `/capabilities` and heartbeat publication, and cancellation of a blocked remote `tools/list` during `Server.Close`.

Finding [P1] — `Server.Close` can lose the cancel function for a newly started refresh. In `go-shellmcp/internal/server/server.go:915-923`, `defer s.healthBusy.Store(false)` is registered after the defer that clears `s.healthCancel`, so it executes first. The old refresh can therefore set `healthBusy=false`; a new heartbeat refresh can acquire `healthMu` and install its own `healthCancel`; then the old goroutine acquires the mutex and sets `healthCancel=nil`. If `Close` runs after that interleaving, it cannot cancel the new refresh and waits in `server.go:373` until the per-check 15-second timeout (or indefinitely for a non-context-aware child implementation). Smallest fix: make clearing `healthCancel` and releasing `healthBusy` one synchronized finalization step, or register the busy-release defer before the cancel-clear defer so cancel-clear executes first, with a regression test forcing the completion/start interleaving and asserting `Close` cancels the active refresh.

Verdict: CHANGES_REQUIRED.

Unverified assumption: the finding depends on a heartbeat/start call interleaving with the prior goroutine's final defers; the existing race detector and cancellation test do not force this schedule.
