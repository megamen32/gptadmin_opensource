# GrepMesh MCP finish

## Raw request

мы работаем над grepmesh твоя задача доделать referenced conversations "Архитектура GrepMesh MCP".

## Confirmed objective

Finish the current GrepMesh MCP recovery path without changing the selected architecture: one local MCP per host, local persistent index, direct MCP-to-MCP federation for peer fan-out, GPTAdmin only as installer/discovery/health control plane.

## Business canary

- Source/test evidence: focused Rust tests prove persistent index scanner/search/read behavior and federation protocol compatibility.
- Local runtime evidence: GrepMesh reaches a usable indexed/search-ready state on bounded fixture roots and search/read uses the persistent index path instead of full in-memory `snapshot()` critical path.
- Deployment/live evidence: server-100 <-> server-88 rollout, restart, and failure-injection canary are not authorized by this card until the exact rollout/restart boundary is explicitly approved.

## Scope

Owned:

- `grepmesh/` implementation and focused tests needed to integrate persistent FTS5 index storage into scanner/search.
- Minimal docs or task-card notes required to distinguish source/test, local runtime, and live deployment proof.

Excluded unless explicitly approved:

- Branch/worktree changes, merges, deployment, service restart, rollback, and remote host mutation.
- GPTAdmin Hub data-plane routing for routine search.
- Central search service, shared DB, or MCP proxy architecture.

## Initial estimate

Started at: 2026-08-12 06:26:31 MSK
Minimum / maximum active minutes: 45 / 120
Lifecycle provenance: new task from current user request in `/home/admin/gptadmin` on branch `agent/gptadmin-parallel-browser-flows-scoped`, commit `5ab8285a81f08e32c8c0d8661dd648fa469a4227`.
Last task-file mtime observed: 2026-08-12 06:26:31 MSK at creation.

## Runtime identity

Harness: Codex desktop
PID: unknown
Agent session: current Codex task
PID status: unknown
Last PID signal: none
Last task-file transition: todo created

## Route

Short-to-Full boundary: treat as Short until research proves the remaining work requires a fresh >30-minute architecture decision. The user already selected the Normal direct-federation architecture and authorized finishing the persistent index path in prior context.

Immediate plan:

1. Inspect current dirty GrepMesh changes and preserve foreign edits.
2. Add/repair a focused regression test that fails on persistent index not being used by scanner/search.
3. Implement the smallest persistent scanner/search integration.
4. Run focused Rust checks.
5. Stop before remote deployment/restart and report exact evidence plus remaining live canary boundary.

## Stop conditions

- Any required deployment, restart, branch/worktree operation, or destructive cleanup.
- A conflicting fresh foreign edit in the same `grepmesh/` paths.
- Persistent index integration requires a new architecture choice rather than a bounded implementation fix.
