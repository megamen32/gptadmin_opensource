# GrepMesh protocol-fix review

Role: Reviewer

## Assignment

Freshly review only the last selected-scope fix in `grepmesh/src/server.rs`:
MCP initialize protocol negotiation added after the independent Inspector
reported rejection of `2026-07-28`. Check direct regressions in the existing
GrepMesh MCP/peer canary and transport-header compatibility. Do not edit code,
touch unrelated work, deploy, restart, or change secrets/ACLs.

## Evidence

The independent official Inspector CLI reached a real temporary A/B GrepMesh
surface but failed at `tools/list` with raw:
`{"error":{"code":"error","message":"Server's protocol version is not supported: 2026-07-28"}}`.
The fix negotiates requested `2025-03-26`, `2025-06-18`, `2025-11-25`,
`2024-11-05`, or explicit `2026-07-28`, defaulting to `2025-06-18`.

Current checks pass: `cargo clippy --all-targets -- -D warnings` and
`cargo test --all-targets` (6 unit, 7 index, 3 mesh, 3 search/root, 10
topology-cache), with the two-process mesh test still green. No GPTAdmin or
production files changed in this fix.

Append exact scoped findings and finish with `APPROVE` or
`CHANGES_REQUIRED`.

## Review evidence

- Inspected only the selected scope in `grepmesh/src/server.rs`, plus the
  existing black-box peer canary in `grepmesh/tests/mesh.rs`.
- The new `initialize` path negotiates among `2025-03-26`, `2025-06-18`,
  `2025-11-25`, `2024-11-05`, and explicit `2026-07-28`, defaulting to
  `2025-06-18`.
- The transport-header gate accepts all supported protocol versions and only
  enforces `Mcp-Method` / `Mcp-Name` mirroring for `2026-07-28`, preserving
  older-header compatibility.
- Verified locally:
  - `cargo test --manifest-path grepmesh/Cargo.toml protocol_negotiation -- --nocapture`
  - `cargo test --manifest-path grepmesh/Cargo.toml --test mesh black_box_two_process_peer_fanout_and_partial_results -- --nocapture`
  Both passed.

APPROVE
