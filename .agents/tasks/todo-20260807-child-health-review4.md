# Lazy child MCP health release review

## Role

Reviewer. Fresh read-only gate after final defer ordering fix.

## Contract

Review only current health lifecycle code and its tests. Check cancellation, handle ordering, race safety, heartbeat output, and blackbox behavior. Return verdict and any remaining severity-ranked findings.

## Reviewer evidence (2026-08-07)

- Scope reviewed read-only: `go-shellmcp/internal/server/server.go`, `supervisor_handler.go`, `server_test.go`, `server_health_blackbox_test.go`, and the related `internal/mcpclient` lifecycle/status code. Unrelated dirty worktree paths were not touched.
- Validation passed: `go test ./internal/server -count=1`; `go test -race ./internal/server -count=1`; focused lazy-health/blackbox tests passed; no source edits made.
- Cancellation and handle ordering: `Server.Close` marks health closed, cancels the active refresh while holding `healthMu`, then waits. The refresh finalizer clears `healthCancel` before `healthBusy=false` in the same deferred critical sequence (`server.go:914-924`), so the prior lost-cancel interleaving is fixed. New starts are rejected after close (`server.go:904-910`).
- Race safety: the health snapshot is published via `atomic.Value`; the single-flight guard is atomic; the focused race run reported no races. The existing cancellation blackbox/unit test reaches a blocked remote `tools/list` and confirms `Close` completes after cancellation.
- Heartbeat/blackbox: current blackbox coverage proves ready stdio health, disabled health, `/capabilities` propagation, and heartbeat propagation. `heartbeatLoop` sends the current snapshot and starts the next lazy refresh (`server.go:888-895`), so the first post-start heartbeat may legitimately contain the initial `unknown` snapshot before the asynchronous refresh completes.

Finding [P2] — the defer-ordering fix is not protected by a deterministic regression test. `server.go:914-924` now has the correct ordering, but no test forces the old-refresh-finalizer/new-refresh-start/`Server.Close` schedule. The existing cancellation test only covers one in-flight refresh and would not catch a future reintroduction of the lost-handle bug. Smallest in-scope fix: add a synchronization-controlled test that holds the old finalizer at completion, starts a replacement refresh, calls `Close`, and asserts the replacement context is canceled promptly.

Verdict: CHANGES_REQUIRED — no P0/P1 implementation findings remain, but add the focused ordering regression test before release.
