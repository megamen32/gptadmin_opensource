# GrepMesh final adversarial gate

Role: Critic

## Immutable objective

The selected Normal goal is a local Rust GrepMesh MCP installed symmetrically
per host: local/default-wildcard/explicit-host search, direct MCP-to-MCP
fan-out, remote read, persistent per-root index/watchers, cached/periodic
topology, and a minimal GPTAdmin control-only projection. The intended handoff
claim is local canary readiness, not production enrollment.

## Business done condition

An AI agent reaches one local MCP, uses default `hosts="*"`, receives local and
peer matches with host/absolute path/line/context, reads a remote file, and
receives useful local results with `partial=true` when one peer fails. A valid
fresh discovery result with zero peers must be accepted; a successful fresh
discovery must remove peers that disappeared; failed discovery must retain the
cached/static fallback. GPTAdmin must not proxy search.

## Current evidence

Attribution base: `HEAD=073ed2b12ecabb65b4d15755fd3be350adc5adb9`.
Selected GrepMesh plus seam-file aggregate SHA-256:
`da84cf31b3b9f7e9b9653f91162035236ec1927e4b10bc0c0a2afc9d5463a3db`.

All current checks passed:

- `cd grepmesh && cargo clippy --all-targets -- -D warnings`.
- `cd grepmesh && cargo test --all-targets`: 4 unit, 7 index, 3 mesh, 3
  search/root, and 10 topology-cache tests passed.
- The two-process black-box mesh test uses modern MCP headers, default /
  wildcard fan-out, distinct routable peer URL, path discovery, remote read,
  and stopped-peer partial output.
- `cd go-hub && go test ./...` passed. The control-only endpoint tests and
  Rust provider tests prove local filtering, endpoint-only URL fallback,
  routable URL preservation, and no relay payload/credential projection.
- Fresh Reviewer rerun inspected the selected scope and returned `APPROVE`;
  it directly ran the empty-provider removal and worst-index-state tests.

## Proposed next action

If this gate passes, run a fresh `only-new` CLI tester against a temporary
two-node canary, then hand off the local implementation with explicit notes
that five-host enrollment, authentication/mTLS/firewall/ACL, public bind,
restart/deploy, and official `rmcp` crate wiring remain separate gates.

## Explicit exclusions

No production mutation, deployment, secret use, security rollout, unrelated
GPTAdmin changes, or implementation edits.

Append decisive evidence, questions, alternatives for any non-PASS route, and
exactly one verdict: `PASS`, `RETHINK`, `STOP`, `STOP_SCOPE_DRIFT`, or
`STOP_MISSING_CONTEXT`.

## Critic evidence — 2026-08-10

### Reconstructed done condition

The local-canary claim is not merely that two HTTP processes exchange MCP
messages.  A fresh MCP consumer must call the local endpoint with `hosts`
omitted, obtain results attributed to both A and B, read B through A, and still
obtain A results plus `partial=true` after B is unavailable.  It must also
demonstrate the specified discovery/cache transitions without turning GPTAdmin
into a data-plane proxy.

### Decisive evidence

- I independently ran `cargo test --test mesh
  black_box_two_process_peer_fanout_and_partial_results` in `grepmesh`: PASS.
  The test starts two separate server processes, reaches A over MCP HTTP,
  observes A+B search hits, performs a B read via A, then stops B and observes
  local results with `partial=true`.
- That same black-box invocation explicitly sends `"hosts": "*"` for each
  cross-host search/path call (`grepmesh/tests/mesh.rs:191,207,246`).  It does
  **not** exercise the contractual default where `hosts` is absent.  The
  implementation appears to default correctly (`hosts.unwrap_or("*")` in
  `grepmesh/src/mcp.rs:129`), but a code reading cannot substitute for the
  required consumer-level proof.
- I independently ran `cargo test --test topology_cache
  fresh_empty_provider_result_is_valid_and_removes_cached_peers`: PASS.  The
  test proves a fresh empty provider snapshot removes B.  The retained-cache
  failure behaviour is covered by the named unit scenario
  `unreachable_peers_keep_cached_entries_and_error_status`, but was not a
  consumer/path-level canary in the supplied evidence.
- I independently ran `go test ./internal/hub -run TestGrepMeshTopology` from
  `go-hub`: PASS.  The projection code returns only topology fields and the
  tests reject credential/executable-tool leakage.  This supports the
  control-only boundary, not an end-user search result.

### QUESTIONS_FOR_L

1. Will the proposed fresh `only-new` CLI tester call `search_text` through A
   with no `hosts` member at all (rather than `hosts="*"`) and retain its raw
   result/exit evidence?  The task does not state this acceptance assertion.
2. Which exact fresh CLI/client and command prove the stated "AI agent reaches
   one local MCP" boundary rather than reusing Rust test helpers?  A test name
   alone is insufficient to distinguish a separate consumer from the existing
   in-process test fixture.

### Excluded hypotheses

- No evidence suggests GPTAdmin is currently proxying search/read: the
  topology implementation and focused Hub tests support a read-only,
  control-plane projection.
- No production-enrollment claim is warranted or needed here; authentication,
  mTLS/firewall/ACL, public bind, service restart, and five-host rollout remain
  correctly excluded.

### Alternatives

1. Refine the fresh tester contract and run it: independent CLI -> A, omit
   `hosts`, verify A+B attribution and remote read, stop B and verify the raw
   `partial=true` response; then attach that receipt to this task.
2. Add the missing omitted-`hosts` assertion to the existing two-process test,
   rerun it, and still use a context-free CLI tester as the final consumer
   acceptance gate before any readiness handoff.

### Proof required to proceed

A context-free executable-client receipt that explicitly records an omitted
`hosts` request and all four business outcomes above, plus an identified command
and retained raw response/exit status.  The cache-transition unit receipts and
control-plane Hub receipt may remain supporting evidence.

## Verdict

RETHINK
