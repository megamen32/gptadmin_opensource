# GrepMesh MCP protocol negotiation fix

Role: Worker
Parent task: /home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-mcp.md

## Assignment

Fix only the Inspector-discovered MCP interoperability issue in
`/home/admin/gptadmin/grepmesh/`: the server currently always returns
`protocolVersion=2026-07-28` from `initialize`, and the independent official
Inspector CLI rejects that version before `tools/list`. Implement protocol
version negotiation so a client requesting a supported older version receives
that version, while a client explicitly requesting `2026-07-28` keeps modern
behavior. Keep existing transport-header validation and legacy compatibility.

Add focused tests for default/older/current negotiation and ensure the
two-process mesh canary remains green. Do not touch GPTAdmin, unrelated dirty
work, production services, secrets, ACLs, or deployment state.

Run `cargo fmt --all`, `cargo clippy --all-targets -- -D warnings`, and
`cargo test --all-targets`. Append exact evidence and a TL;DR here. This is a
bounded Worker fix; no production deployment is authorized.

## Recovery report by L (2026-08-10)

The Worker stopped after bounded waits without changing code. L is recovering
the same single-file protocol negotiation fix and tests; no duplicate Worker
was spawned and no production state was touched.

## Recovery result by L (2026-08-10)

`server.rs` now negotiates `2025-03-26`, `2025-06-18`, `2025-11-25`,
`2024-11-05`, or explicit `2026-07-28` based on the initialize request, with
`2025-06-18` as the compatibility default. Added focused default/older/current
tests. `cargo fmt --all`, `cargo clippy --all-targets -- -D warnings`, and
`cargo test --all-targets` passed: 6 unit, 7 index, 3 mesh, 3 search/root, and
10 topology-cache tests; 0 failures.

TL;DR: Inspector protocol negotiation fixed and green; no production state
touched.

## Current status

- 2026-08-10: inspected `grepmesh/src/server.rs`, `grepmesh/src/mcp.rs`, and
  `grepmesh/tests/mesh.rs`; the negotiation gap is isolated to the
  `initialize` response in `server.rs`, which still hardcodes
  `protocolVersion = "2026-07-28"`.
- 2026-08-10: `validate_transport_headers` already preserves the required
  legacy/modern transport-header behavior, so the intended change is a small
  helper that echoes the requested supported protocol version for
  `initialize` while keeping the current version for explicit `2026-07-28`
  requests.
- 2026-08-10: no code patch had been applied before the user paused the turn;
  the next bounded step is to implement the helper and add focused initialize
  negotiation tests plus a current-version canary assertion.
