# GrepMesh persistent index and filesystem watcher

Role: Worker
Status: done
Parent task: /home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-mcp.md

## Bounded objective

Implement an isolated persistent local index module for GrepMesh. It must be
usable later by `LocalBackend` but must not modify the backend integration in
this lane.

## Owned paths

- `/home/admin/gptadmin/grepmesh/src/index.rs`
- `/home/admin/gptadmin/grepmesh/tests/index.rs`
- `grepmesh/Cargo.toml` and `grepmesh/Cargo.lock` only for index/watcher
  dependencies.
- Do not edit `src/backend.rs`, `src/mcp.rs`, `src/config.rs`, topology files,
  GPTAdmin files, or production state.

## Required behavior

- Persist an atomic JSON index snapshot under a configured state path.
- Track absolute path, size, modified marker, generation, scan timestamps,
  binary/text eligibility, and a bounded trigram-to-path candidate map.
- Explicitly exclude configured glob patterns and generated/cache directories.
- Provide a deterministic reconciliation scan and an event-driven watcher;
  watcher events must update generation and persist state.
- Expose `ready`, `building`, `degraded`, and `corrupt` state without making
  callers trust a damaged snapshot.
- Keep index failures recoverable so `rg` can remain the correctness fallback.

## Acceptance checks

- Unit test initial build, persistence/reload, excluded paths, binary files,
  changed/removed files, trigram candidates, watcher event, and corrupt JSON.
- Tests use temporary roots/state only and do not require a daemon or network.

## Report contract

Append exact files, commands, test results, and unresolved API issues here;
finish with TL;DR for L. Do not broaden into backend integration.

## Evidence

Changed files:

- `/home/admin/gptadmin/grepmesh/src/lib.rs`
- `/home/admin/gptadmin/grepmesh/src/index.rs`
- `/home/admin/gptadmin/grepmesh/tests/index.rs`
- `/home/admin/gptadmin/grepmesh/Cargo.toml`

Commands:

- `cargo test --test index`

Results:

- Added isolated persistent index module with atomic JSON snapshot persistence,
  deterministic scan/reconcile, exclusion handling, binary/text eligibility,
  trigram candidate map, corrupt snapshot recovery, and notify-backed watcher.
- Added focused tests covering initial build, persistence/reload, excluded
  paths, binary files, changed/removed files, trigram candidates, watcher
  refresh, and corrupt JSON.
- `cargo test --test index` passed: 7/7 tests green.

Unresolved API issues:

- None in the assigned lane.

TL;DR: isolated GrepMesh index + watcher slice is implemented and tested
without touching backend integration.
