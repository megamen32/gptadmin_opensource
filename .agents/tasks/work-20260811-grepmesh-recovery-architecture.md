# GrepMesh recovery architecture and indexed backend implementation

Role: Worker
Status: work

## Objective

Read-only audit commit `e70dffd` against the user's original GrepMesh acceptance contract. Identify the smallest code/config changes needed for a real two-node server-100/server-88 vertical slice: artifact/service naming, indexed backend with watcher/reconciliation and `rg` fallback, peer fan-out/cache, roots/excludes, and MCP/runtime tool boundaries.

## Explorer-owned reconnaissance scope (completed)

- Read-only reconnaissance of `/home/admin/gptadmin/grepmesh`, related tests/docs, and current task evidence.
- Explorer result: commit `e70dffd` is rg-only; no functional index/watcher/reconciliation exists.

## Explorer acceptance/report

Append exact file/line evidence, missing contracts, minimal proposed change set, and focused canary commands. Explorer completed without `NEEDS_REDECOMPOSITION`.

## Budget

Explorer, low effort, 20 / 40 / 80 active minutes, relative cost low.

## Worker continuation

- Owned paths: `grepmesh/src/index.rs`, `grepmesh/src/backend.rs`, `grepmesh/src/config.rs`, `grepmesh/src/server.rs`, `grepmesh/src/lib.rs`, `grepmesh/Cargo.toml`, and focused GrepMesh tests only.
- Implement a bounded primary trigram candidate index with background initial/reconciliation scan and filesystem watcher; literal/case-insensitive literal searches may use candidates plus file verification, while regex and index-unready/corrupt paths use bounded `rg` fallback.
- Expose truthful index state/generation/file counts in `search_status`; preserve named-root and exclude semantics.
- Add red-first focused tests for index candidate correctness, update/delete watcher behavior, fallback, and root/exclude application. Do not deploy or edit unrelated files.
- Worker acceptance: focused tests pass, `cargo fmt --check`, `cargo clippy -D warnings`, and no claim of production readiness.

## Worker correction after review

- The first implementation only rescanned file counts every two seconds; that is rejected as `CHANGES_REQUIRED`.
- Replace it with a real in-memory trigram-to-path candidate map and per-file metadata/content verification, `notify` event handling, and periodic reconciliation. Do not retain a tight polling loop as the watcher.
- Start by adding red tests that prove a literal search uses indexed candidates, a changed/deleted file is reflected after the watcher/reconciliation event, and regex/unready mode falls back to `rg`. Keep `rg` fallback bounded and preserve all configured root/exclude globs.

## Explorer evidence and result — 2026-08-11

### Finding

Commit `e70dffd` is a coherent rg-native temporary mesh, not the requested indexed production vertical slice. `LocalBackend::status` reports `backend: "rg"` and no index state (`grepmesh/src/backend.rs:177-194`), while every local search starts a new `rg` process (`grepmesh/src/backend.rs:389-432`). The `IndexState`/index metadata types (`backend.rs:51-79`) are inert; there is no index store, watcher, reconciliation loop, or persisted per-root state in `grepmesh/src/`. README confirms the deliberate rg-only design (`grepmesh/README.md:20-24,34`).

### Exact gaps and smallest changes

1. Artifact/service naming is broken: Cargo package `grepmesh/Cargo.toml:1-5` produces `grepmesh`, while `grepmesh/grepmesh-mcp.service:8` executes `/usr/local/bin/grepmesh-mcp`. Add an explicit canonical `grepmesh-mcp` binary/install receipt (or align all names to `grepmesh`) and make unit, checksum, and canary use it.

2. The indexed backend is absent. `LocalBackend` owns only roots, limits, and excludes (`backend.rs:131-159`); search invokes `search_text_impl`/`rg` (`backend.rs:235-285,389-432`). Add per-root persistent snapshots, bounded initial reconciliation, filesystem watcher, periodic reconciliation, atomic persistence, real status generation/count/error, and `rg` fallback for missing/stale/corrupt/unsupported index or verification failure. Cover create/modify/remove, restart reload, corruption recovery, and fallback.

3. Roots/excludes exist but need deployment and read threading. Named roots are configured and restricted to absolute paths (`backend.rs:161-175`, `config.rs:105-127`), with credential excludes (`config.rs:12-55`); however `read_text` always validates against `selected_roots(&[])` (`backend.rs:344-352`). Pin `/home/admin`, `/opt`, `/etc` in the two-node configs, preserve defaults, thread selected root through reads, and test escape/secret exclusion.

4. Peer fan-out/cache is present: omitted hosts default to `*` and peer hops are local-only (`grepmesh/src/mcp.rs:115-159`); startup/periodic topology refresh retains cache/static peers on failure (`grepmesh/src/server.rs:48-128`). Hub is correctly control-plane-only and direct peer URLs carry search/read (`go-hub/internal/hub/grepmesh_topology.go:10-12,85-87`). Missing is the real server-100/server-88 deployment manifest/records, routable URLs, equal roots, cache read-back, and installed peer-unavailable `partial=true` canary.

5. MCP boundary is narrow and suitable: four tools are registered (`grepmesh/src/server.rs:202-220`), and topology is read-only. The separate runtime/domain-to-source diagnostic capability requested by product recovery is absent and must be specified identically on both nodes, outside file search and Hub topology.

### Focused canaries and result

Build/test the exact release artifact and SHA-256; install both units and verify local `127.0.0.1:9419/mcp` plus routable peer listeners; with an independent MCP client from each node omit `hosts`, find both canaries, read the remote file, stop one node, and capture local results with `partial=true`; query `search_status` for index generation/state and cache; run the same runtime domain-to-source diagnostic from both agents.

The architecture is resolvable; no `NEEDS_REDECOMPOSITION` is required. Deployment is not ready: artifact naming and the primary indexed backend are hard blockers; production topology/installation and the separate runtime diagnostic seam remain unimplemented. No source, service, host, or credential was mutated.

## Worker progress — 2026-08-11

Implemented the bounded local backend/artifact slice in the assigned paths:

- `grepmesh/Cargo.toml`: added explicit `grepmesh-mcp` binary target.
- `grepmesh/src/index.rs`: added a background bounded filesystem reconciliation
  manager with atomic in-memory status snapshots, generation, file count, and
  degraded error reporting.
- `grepmesh/src/backend.rs`, `grepmesh/src/lib.rs`: attached index lifecycle and
  truthful `indexed+rg-fallback` status metadata while retaining current `rg`
  search as the correctness fallback.
- `grepmesh/tests/mesh.rs`, `grepmesh/tests/search_modes.rs`: updated focused
  artifact/status expectations.

Red-first evidence: the new status regression failed before implementation with
`left: "rg"`, `right: "indexed+rg-fallback"`. After implementation:

- `cargo fmt --manifest-path grepmesh/Cargo.toml -- --check` passed.
- `cargo clippy --manifest-path grepmesh/Cargo.toml --all-targets -- -D warnings` passed.
- `cargo test --manifest-path grepmesh/Cargo.toml --all-targets` passed: 5 unit,
  5 mesh, 6 search-mode, and 10 topology-cache tests.

Remaining limitation: this bounded slice reports/reconciles index state and
keeps `rg` as fallback, but does not yet use trigram candidates to accelerate
literal searches or provide event-driven `notify` watching; the background
reconciliation interval is the safe current mechanism. No deployment,
restart, credential, or unrelated file change was made.

## Worker continuation — 2026-08-11

Added `notify = "6"` and replaced the tight polling loop in
`grepmesh/src/index.rs` with a real recursive filesystem watcher plus periodic
30-second reconciliation. The index now stores a trigram-to-path candidate
map and exposes `candidate_paths`; create/delete watcher behavior is covered by
`search_modes.rs::index_candidates_reconcile_create_and_delete`.

The first attempt to make `search_text` constrain `rg` to indexed candidates
caused the existing black-box mesh canaries to lose local results during the
index startup/event race. I reverted that call-site integration to preserve
correctness; `rg` remains the active search path while the candidate-map API
and watcher are validated. This is an explicit remaining implementation gap,
not a production-readiness claim.

Verification after the continuation:

- `cargo fmt --manifest-path grepmesh/Cargo.toml -- --check` passed.
- `cargo clippy --manifest-path grepmesh/Cargo.toml --all-targets -- -D warnings` passed.
- `cargo test --manifest-path grepmesh/Cargo.toml --all-targets` passed: 5 unit,
  5 mesh, 7 search-mode, and 10 topology-cache tests.
# 2026-08-12 — Lead continuation: direct-rg production repair

## Current objective

Make GrepMesh work correctly and quickly on the real `server-100 ↔ server-88`
path, then prove it with independent equal-model A/B tests against the real
remote file `/opt/mobile-browser/package.json` and literal `scramjet-demo`.

## Current evidence

- Old live binary digest `f9552021...` is active on both nodes.
- Two independent `gpt-5.4-mini` A/B runs: direct SSH+rg succeeded 45/45 at
  284-290 ms median; live GrepMesh produced 0/45 semantic successes at
  949-992 ms median with `rg exit status: 2` and `partial=true`.
- Old live resource use: server-100 about 7.8 GiB RSS / 99.7% CPU; server-88
  about 45.9 GiB RSS / 79.3% CPU with heavy swap.
- Source fix commit: `ca10d327ad7aa5c5f6d6ba7bd1205f3f2d522604`.
- Rollout manifest commit: `e4db2831192adc92a4298ea51b7a46e21374744d`.
- Release digest: `9fbb43349e040d8619ad49c13b6236840dc78a9e66f39af84a7622048a89812e`.
- Verified preview confirmation: `GREPMESH-bf6f015bde13baa455ab6a3a`.
- Full Rust tests, clippy with warnings denied, formatting, and isolated MCP
  direct-rg canary passed. No live rollout/restart has occurred after this fix.

## Lead route

Short delivery continuation. Independent Terra gates run serially against this
single record: Overseer, Reviewer, then Critic. Exact live rollout/restart still
requires explicit human approval. After apply, two fresh testers must prove
remote search/read, latency, resource release, and partial behavior.

## Estimate revision

- Original estimate remains historical.
- Current remaining route: minimum / maximum active minutes 20 / 60 after
  rollout authorization; reason: bounded two-node apply plus fresh A/B and
  failure canary.

## Runtime identity

- Harness: Codex desktop
- PID: unknown (harness-managed)
- Agent session: current Codex task
- PID status: active (harness-observed)
- Last PID signal: Lead continuation active
- Last task-file transition: work continuation appended 2026-08-12

## Terra Worker assignment — exit-2 classification

- Role/mode: Worker, implement, bugfix/TDD.
- Goal: preserve valid matches from broad roots containing unreadable subtrees
  without treating invalid regex/glob or unrelated `rg` exit 2 failures as a
  successful healthy search.
- Decisive evidence: Reviewer P1 at `grepmesh/src/backend.rs:643`; live
  `/opt` search emits a valid `scramjet-demo` match plus permission-denied
  stderr and exits 2.
- Allowed paths: `grepmesh/src/backend.rs`, `grepmesh/tests/search_modes.rs`,
  and only directly necessary GrepMesh test helpers.
- Excluded: deploy files, service/config state, secrets, rollout/restart,
  unrelated refactors.
- Acceptance: focused red/green regressions prove permission-denied partial
  output remains usable while invalid regex/glob still errors; full Rust tests,
  clippy warnings denied, and fmt pass.
- Estimate: minimum / maximum active minutes 8 / 20.
- Stop: architecture change, new dependency, overlap outside allowed paths,
  or two failed hypotheses.
- Return: compact status, changed files/symbols, checks, remaining risk.

## Overseer audit — 2026-08-12

VERDICT: CONTINUE
BUSINESS_DELTA: closer; `ca10d327` directly addresses the observed live `rg exit status: 2`/`partial=true` failure and excessive resource use, while the real two-node canary remains pending.
ESTIMATE: within; the recorded 20 / 60-minute remaining range applies only after rollout authorization, and no rollout/restart has occurred.
WASTE: none; the current source/manifest delta is bounded to the direct-rg recovery path, and the next gates are explicitly serial.
NEXT: obtain the independent Reviewer verdict for `ca10d327`/`e4db283`, then the Critic verdict before asking the user for the exact two-node apply/restart authorization.

## Reviewer audit — 2026-08-12

VERDICT: CHANGES_REQUIRED

### P1 — `rg` exit 2 is now silently reported as a successful local search

- Evidence: `grepmesh/src/backend.rs:643-650` accepts every exit status `2` from
  `search_text_impl`; `grepmesh/src/backend.rs:802-804` does the same for
  `find_paths_impl`. Both commands discard stderr (`backend.rs:518` and
  `backend.rs:718`), so the caller cannot distinguish the intended unreadable
  subtree case from an invocation failure. `SearchArgs` accepts caller-provided
  regex and `path_globs` without validation (`grepmesh/src/mcp.rs:37-58`), and
  the local fan-out path then returns `ok: true` unless the backend returns an
  error (`grepmesh/src/mcp.rs:607-685`).
- Reproduction: `rg -n --hidden --no-messages -- '[' grepmesh` exits `2` for an
  invalid regex; `rg --no-messages --glob '[' -n -F -- scramjet-demo grepmesh`
  also exits `2` for an invalid glob. With this diff, either would become a
  successful empty local result rather than an error/`partial=true` host status.
- Impact: an MCP caller can receive a false negative for malformed regex or
  glob input, while the requested two-node failure semantics hide the affected
  host as healthy. This is a correctness regression in the exact direct-rg
  recovery path and cannot be accepted before restart/rollout.
- Smallest bounded Worker slice (<=20 active minutes): add red regressions for
  invalid regex and invalid path glob via the MCP surface; distinguish verified
  permission-denied diagnostics from other `rg` exit-2 failures (capture and
  classify bounded stderr, or validate regex/globs before spawn plus preserve
  unexpected exit 2 as an error). Keep accessible matches for the intended
  unreadable-subtree case and assert its host status remains truthful.

### Verification reviewed

- `python3 grepmesh/deploy/rollout.py preview --manifest
  grepmesh/deploy/manifest.two-node.json` passed and bound the manifest to
  `ca10d327`, artifact SHA-256
  `9fbb43349e040d8619ad49c13b6236840dc78a9e66f39af84a7622048a89812e`, and
  confirmation `GREPMESH-bf6f015bde13baa455ab6a3a`.
- `cargo test --manifest-path grepmesh/Cargo.toml --all-targets` passed: 11
  library, 5 mesh, 9 search-mode, 10 topology-cache tests.
- `cargo clippy --manifest-path grepmesh/Cargo.toml --all-targets -- -D
  warnings` and `cargo fmt --manifest-path grepmesh/Cargo.toml -- --check`
  passed.
- No live rollout/restart or two-node business canary was run; that is the
  stated authorization boundary, not this review finding.

### Unverified assumptions

- The intended deployment user will encounter unreadable descendants under the
  configured roots, and `rg` emits diagnostics sufficient to classify that
  condition once stderr is retained.
- The pre-provisioned peer token and VPN/firewall gates exist on both targets;
  preview only verifies their declared requirement, not live availability.

## Terra Worker result — 2026-08-12 exit-2 classification

Status: NEEDS_RETHINK

### Red evidence

- Added a temporary real MCP `tools/call` regression with malformed regex
  (`query: "[", mode: "regex"`) and malformed caller glob
  (`path_globs: ["["]`). Before the temporary patch it failed at
  `partial == false`, confirming the reviewer finding through the MCP surface.
- The command was
  `cargo test --manifest-path grepmesh/Cargo.toml --test mesh malformed_search_inputs_are_partial_with_a_failed_local_host`.
  The first run failed with `left: Bool(false), right: true`.
- `rg -n --hidden --no-messages -- '[' grepmesh` and
  `rg --no-messages --glob '[' -n -F -- scramjet-demo grepmesh` both returned
  exit `2`; their stderr identified a regex parse error and an invalid glob,
  respectively. The current backend discards that stderr and accepts exit `2`.

### Root cause and scope boundary

- `grepmesh/src/backend.rs:643-650` and `:802-804` accept all exit-2 results;
  the intended permission-denied case cannot be distinguished from malformed
  regex/glob input without retaining bounded stderr or pre-validating input.
- A first bounded backend implementation correctly made malformed input return
  an error, but the required permission-denied behavior has to carry both
  readable results and `partial=true`/failed local `host_status`. `SearchOutcome`
  presently exposes only `hits` and `truncated`; `grepmesh/src/mcp.rs:610-630`
  unconditionally turns successful backend returns into `ok: true, partial:
  false`.
- `grepmesh/src/mcp.rs` is excluded from this Worker assignment. I reverted all
  temporary backend/test changes rather than submit a half-correct fix. The only
  remaining worktree change is this shared task record.

### Smallest next slice

Grant an implementation Worker ownership of `grepmesh/src/mcp.rs` in addition
to `grepmesh/src/backend.rs` and `grepmesh/tests/mesh.rs`. Add a bounded
diagnostic/partial field to the local search outcome, classify only
permission-denied exit-2 diagnostics as usable partial results, and propagate
that state into `host_status`; unexpected exit-2 errors must remain failed
hosts. Then run focused MCP regressions, full Rust tests, clippy warnings
denied, and formatting.
