# GrepMesh product recovery and real two-node rollout

Role: Lead

## Original request

The user supplied a failure analysis stating that the previous GrepMesh work validated only a temporary single-node MCP and requested restoration of the original five-host product result. The required first delivery is a real `server-100 ↔ server-88` vertical slice, followed by fleet rollout and acceptance.

## Objective

Restore and prove GrepMesh as a deployed multi-host product: a local MCP entrypoint on each host, direct peer fan-out with cached/static topology, bounded indexed search with `rg` fallback, remote reads, and an operational domain-to-source workflow with equal runtime capabilities on both comparison agents.

## Business canary

From both server-100 and server-88, a fresh agent calls only its local GrepMesh MCP, searches with `hosts="*"`, finds per-host canaries, reads the remote canary, and after either node is stopped returns the remaining result with `partial=true`. Separately, both equal-capability agents independently resolve `all.bezrabotnyi.com` to `mobile-browser.service` and `/opt/mobile-browser`.

## Confirmed scope

- Current implementation baseline: commit `e70dffd4df1610f8c47c0cca07ecb50056acbf58`.
- First rollout slice: server-100 and server-88 only.
- Production roots must use `/home/admin`, not all `/home`; `/opt` and `/etc` are explicit named roots.
- `rg` remains a correctness/build-recovery fallback; the primary path must avoid a full broad-root scan per request.
- Runtime diagnostics are separate from file search; both A/B agents receive the same runtime capability.
- No new Reviewer/Critic cycle before the real two-node canary.

## Explicit exclusions

- No five-host rollout until the two-node canary passes.
- No public exposure, unauthenticated bind, secret printing, or ad-hoc credentials.
- No destructive cleanup, rollback, or unrelated service restart.
- No final readiness claim from unit tests, temporary Inspector peers, or a manual parent SSH rescue.

## Initial estimate

- 120 / 240 / 480 active minutes (optimistic / likely / pessimistic).
- Estimate revisions: none yet.

## Stop conditions

- `stop_when`: the two-node canary passes end-to-end, or an exact external authority/credential/OS blocker is reached.
- `abandon_when`: the next action requires an unprovided security/permission decision or a target outside the explicitly scoped server-100/server-88 slice.
- `forbidden_without_explicit_user_request`: public bind, mTLS/firewall/ACL changes, secret creation/rotation, destructive rollback, or deployment beyond the two-node slice.

## Plan selected from user instruction

Implement the smallest coherent vertical slice: fix artifact/service/install contracts; add a repeatable manifest and receipts; restore primary bounded index + watcher + reconciliation while preserving `rg` fallback; add the runtime diagnostic seam needed by both agents; deploy server-100 ↔ server-88; prove the canary; then stop before fleet expansion.

## Progress

- [x] Read-only architecture/topology reconnaissance.
- [x] Technical preview and exact install/restart gate prepared.
- [x] Implement vertical slice.
- [ ] Deploy server-100 ↔ server-88.
- [ ] Real two-node canary.

## 2026-08-11 — Implementation progress

- Read-only topology completed: ShellMCP transport is online for server-100 and server-88; port 9419 is free on both; `peer.env` is absent/empty on both.
- Implemented the primary in-memory trigram candidate index, notify watcher, periodic reconciliation, default exclude merging, and `rg` fallback; duplicate local/named root scans are deduplicated.
- Added bearer-authenticated peer calls, protected non-loopback bind startup checks, local-only agent bind, canonical release binary, and rollback-capable install receipts.
- Evidence: `cargo test --all-targets` passed 28 tests; `cargo clippy --all-targets -- -D warnings` passed; release artifact SHA-256 `3a5c26c35c49992807a7279c34795b4101bb4c12e8fb00c0684e627e7227b9d0`; preview confirmation `GREPMESH-d137d1f8189485e6b835fccc`.
- Gate: no production mutation made. Direct two-node install/restart requires a provisioned shared peer token (or explicit approved GPTAdmin identity mapping); creating/rotating that trust material is not assumed.

## 2026-08-11 — Provenance and permission preflight

- The two GrepMesh commits are `c3c7fe67df348226de1b7f7d53f350e1cad16ade` (indexed/authenticated backend) and `9754748c725f8eba366f765f283b0698441d5ae5` (guarded deployment). The manifest pins the former as binary source and proves it is an unchanged ancestor of the latter.
- ShellMCP preflight confirmed free `9419` on both targets. server-100 UFW already allows source `203.0.113.10`; server-88 input policy is accept. No firewall mutation is required for the first slice.
- server-88 `/home/admin` is `0750`; the committed unit uses `SupplementaryGroups=admin`, so the service can traverse only group-readable project paths. Private files stay excluded and unreadable unless their existing mode permits otherwise.
- Remaining exact authority: provision an existing or newly approved shared peer token into root-only `/etc/grepmesh-mcp/peer.env` on both hosts, then permit the two-node install/restart through GPTAdmin ShellMCP.

## 2026-08-11 — Blocked deployment gate

- The same external blocker persisted across three goal continuations: no existing `GREPMESH_PEER_TOKEN`, no supplied GPTAdmin secret reference, and no explicit authorization to create/rotate this shared trust material.
- No service, firewall, ACL, token, or unit state was mutated. The exact next action remains bounded to server-100 and server-88 through GPTAdmin ShellMCP once authority is supplied.
