# Critic gate: direct child MCP publication and lazy health

## Scope

Review the staged changes for the direct child MCP endpoint publication and
lazy child health heartbeat feature before commit and rollout.

## Acceptance

- No unresolved P0/P1/P2 release blocker in the staged diff.
- Check topology, disabled-child exposure, heartbeat health propagation,
  cancellation/single-flight lifecycle, and blackbox coverage.
- Base findings on the current worktree and tests; do not edit production code.

## Evidence available

- `go test ./...` passes in `go-hub` and `go-shellmcp`.
- `go test -race ./internal/server ./internal/mcpclient` passes.
- Both Go release binaries build successfully.
- Focused direct-child and lazy-health blackbox tests are staged.

## Status

Pending independent Critic verdict.

## Independent Critic evidence (2026-08-08)

- Scope audited: staged changes only; no production code edited.
- `git diff --cached --check`: clean.
- `go test ./...` passes in `go-hub` and `go-shellmcp`.
- `go test -race ./...` passes in `go-shellmcp`; the focused race packages also pass.
- `go build ./cmd/gptadmin-hub` and `go build ./cmd/shellmcp-go` pass from their respective modules.
- Direct-child topology is covered by staged hub tests: enabled children are published with direct cards and `/server/{slug}/mcp` routing; disabled children are excluded; tools/list and tools/call are routed through the parent shell queue.
- Lazy-health behavior is covered by staged unit and blackbox tests: ready/tool count propagation, stopped stdio non-start behavior, disabled state, malformed/stopped child unknown state, heartbeat/capabilities propagation, cancellation on `Close`, and single-flight trigger behavior.
- Independent review found no unresolved P0/P1/P2 release blocker in the staged diff. The initial failed combined build invocation was a reviewer command-path error; the two module-local builds passed.

## Independent Critic verdict

PASS

The staged implementation meets the stated acceptance gate for topology, disabled-child exposure, heartbeat health propagation, cancellation/single-flight lifecycle, and blackbox coverage. Proceeding beyond this gate still requires the owning release/rollout authorization and its live business canary; this verdict is not a deployment claim.
