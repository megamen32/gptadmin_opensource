# Direct child MCP endpoint review

## Role

Reviewer. Independent, read-only review of the current diff.

## Contract

Review only the direct child MCP publication change. Check deadlocks, slug/identity collisions, auth/policy routing, response unwrapping, heartbeat compatibility, and tests. Do not edit source. Return findings by severity with exact paths/lines and a verdict.

## Scope

`go-hub/internal/hub/server.go`, `go-hub/internal/hub/server_test.go`, `go-shellmcp/internal/hub/client.go`, `go-shellmcp/internal/hub/client_test.go`, `go-shellmcp/internal/server/server.go`.

## Stop condition

Stop after the independent review and evidence-backed verdict.

## Reviewer evidence (2026-08-07)

- Scope reviewed read-only: `go-hub/internal/hub/server.go`, `go-hub/internal/hub/server_test.go`, `go-shellmcp/internal/hub/client.go`, `go-shellmcp/internal/hub/client_test.go`, `go-shellmcp/internal/server/server.go`.
- Validation: `go test ./...` passed in both `/home/admin/gptadmin/go-hub` and `/home/admin/gptadmin/go-shellmcp`; `git diff --check` passed for the selected paths.
- Finding [P1]: `go-hub/internal/hub/server.go:2824-2841` marks an `enabled:false` child as `status:"disabled"` but still adds it to `publicAgentsLocked`; `go-hub/internal/hub/server.go:2995-3005` and `:5995-6002` then route direct tools/list and tools/call for that published child through the parent. This exposes and executes a disabled supervisor entry, violating the catalog's enabled policy. Smallest fix: exclude disabled children from direct publication/routing, or reject direct calls before dispatch while preserving an explicitly non-callable discovery status; add a regression test.
- No other scoped deadlock, slug collision, response-unwrapping, heartbeat-serialization, or test regressions were found. Existing tests cover enabled child discovery and parent-routed list/call, but not disabled children or access-profile routing for the synthetic child target.
- Verdict: CHANGES_REQUIRED.
