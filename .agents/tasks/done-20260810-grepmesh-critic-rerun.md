# GrepMesh adversarial completion audit rerun

Role: Critic

## Immutable objective

The selected Normal goal is to implement an isolated Rust GrepMesh MCP with
local search, direct peer federation, persistent per-root index/watchers,
cached/periodic topology, and a minimal GPTAdmin control-only projection. The
handoff claim is source-level canary readiness only; five-host production
enrollment, auth/mTLS/firewall/ACL changes, and service restart are separate
authorization gates.

## Business canary

An AI agent connects to one local MCP, omits `hosts` or passes `hosts="*"`,
gets local plus peer results with host and absolute path, reads a remote file,
and still gets local results with `partial=true` when the peer is stopped.
Discovery failure must retain cached/static peers. No search request may go
through GPTAdmin.

## Exact evidence delta

Worktree base for attribution: `HEAD=073ed2b12ecabb65b4d15755fd3be350adc5adb9`.
Selected-file aggregate SHA-256 (all files under `grepmesh/` except target plus
the two new GPTAdmin seam files):
`b6bc795fd936487ef2a6f561da47bdc7096484878d33856658cd13122f99fb5b`.

Commands and observed results:

1. `cd grepmesh && cargo clippy --all-targets -- -D warnings` completed with
   exit code 0.
2. `cd grepmesh && cargo test --all-targets` completed with exit code 0:
   2 unit tests, 7 index tests, 3 mesh tests, 3 search/root tests, and 9
   topology-cache tests passed; 0 failed.
3. The concrete black-box test is
   `tests/mesh.rs::black_box_two_process_peer_fanout_and_partial_results`.
   It starts process A and B on temporary loopback ports; A's peer record uses
   B's routable HTTP URL (not B's loopback URL), sends current transport
   headers, initializes, lists four tools, searches `canary` with `hosts="*"`,
   finds both canary paths, reads B's canary through A, kills B, and confirms
   A results remain while `partial=true` and B results disappear.
4. `tests/search_modes.rs` proves named-root selection, unknown-root rejection,
   and two independent persistent indexes/watchers with `indexed_files == 2`.
   `tests/index.rs` proves atomic reload, corruption handling, bounded
   trigrams, binary filtering, change/remove reconciliation, and watcher
   refresh. `tests/topology_cache.rs` proves fresh/stale/expired/empty states,
   atomic cache, deterministic merge, validation, and stale peer retention.
5. `cd go-hub && go test ./...` completed with exit code 0. The focused
   `TestGrepMeshTopology` tests prove the read-only projection allowlist and
   endpoint routing; the Rust GPTAdmin client test proves local-node filtering
   and preservation of the routable peer URL.

## Proposed next action

Run the independent Reviewer, then this Critic, then a fresh `only-new` CLI
Tester. If all pass, hand off the local implementation with the explicit
production gates above; do not install, restart, enroll, bind publicly, or
claim official `rmcp` crate wiring.

## Explicit exclusions

No production mutation, deployment, secret use, security rollout, unrelated
GPTAdmin code, or implementation edits.

Append the independent audit, questions, alternatives for non-PASS, and exactly
one verdict: `PASS`, `RETHINK`, `STOP`, `STOP_SCOPE_DRIFT`, or
`STOP_MISSING_CONTEXT`.

## Independent Critic audit — 2026-08-10

### Reconstructed done condition

Source-level readiness requires that a local MCP can complete the stated
two-process direct-peer canary (including surviving a stopped peer), while
GPTAdmin remains a read-only topology provider.  It does not authorize
production enrollment, auth/mTLS/firewall/ACL work, public binding, install,
or restart.

### Decisive evidence

1. Independent execution of `cd grepmesh && cargo test --test mesh
   black_box_two_process_peer_fanout_and_partial_results` failed during Rust
   library compilation; the black-box process pair never started.  `src/gptadmin.rs:107-124`
   uses `?` in a `.map(|node| TopologyNode { ... })` closure returning
   `TopologyNode`, producing E0277 at lines 113 and 117.  Therefore the
   claimed green `cargo test --all-targets` cannot be reproduced against the
   current selected source.
2. The attribution aggregate stated in the contract also does not match the
   current files: recomputing SHA-256 over sorted per-file SHA-256 records for
   every `grepmesh/` file except `grepmesh/target/`, plus the two GrepMesh Hub
   seam files, produced
   `779f8719d4f3b1280c94caaf8c747de03858aacd808bfeba7a921613dddfff72`,
   not the claimed `b6bc795fd936487ef2a6f561da47bdc7096484878d33856658cd13122f99fb5b`.
   `grepmesh/src/gptadmin.rs` was modified at 10:14:22 +0300, so this may be
   an active shared-worktree delta rather than a falsified historical run; it
   still makes the proposed handoff evidence non-attributable to current code.
3. Source inspection does support the intended route separation conditional
   on compilation: `MeshService` calls peers by `PeerConfig.routable_url`, and
   the only GPTAdmin client operation is topology `GET`; Hub registers
   `/mcp-relay/grepmesh` as a control-only, `requireCtl` endpoint.  This is
   insufficient to offset the failed compile/canary.

### BUSINESS_DELTA and P0_DISTANCE

BUSINESS_DELTA is unproven: no current build can launch the one-local-MCP,
two-peer scenario.  P0_DISTANCE is one reproducible compile repair plus a
fresh source-attributed test run; it is not a production rollout request.

### Excluded hypotheses

- This is not a peer availability or loopback-routing failure: compilation
  stops before either server binary launches.
- This is not caused by a production secret, network, firewall, or GPTAdmin
  service dependency: the failing compiler path is local and deterministic.
- The existing static/source route separation does not demonstrate an actual
  business canary while the crate fails to build.

### QUESTIONS_FOR_L

1. Is the 10:14 `gptadmin.rs` edit an active replacement of the exact
   evidence set?  If so, which immutable selected-file digest and test output
   are the candidate handoff supposed to represent?

### Alternatives

1. Wait for the active editor to finish, then have a Worker repair the
   iterator/result construction and regenerate the full evidence set for that
   exact digest; rerun a fresh Critic afterward.
2. If the claimed digest is the intended candidate, restore *only through the
   owning workflow* that exact reviewed version into a clean or quiescent
   worktree, then independently compile and run the black-box canary there.

### Minimum proof to proceed

- A stable, attributed selected-file digest; successful `cargo clippy
  --all-targets -- -D warnings` and `cargo test --all-targets` from that exact
  source.
- A fresh pass of `black_box_two_process_peer_fanout_and_partial_results`,
  which proves fanout, remote read, and local partial survival; and focused
  Hub topology tests proving the control-only projection.
- Resolution of the question above.  No production action is needed or
  authorized for this rerun.

## Verdict: RETHINK
