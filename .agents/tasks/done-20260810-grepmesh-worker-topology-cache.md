# GrepMesh cached topology provider

Role: Worker
Status: work in progress
Parent task: /home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-mcp.md

## Исходный запрос

Сделать изолированный модуль кэша топологии для GrepMesh, с атомарной
загрузкой/сохранением, валидацией узлов, TTL/устареванием и детерминированным
слиянием свежих данных провайдера. Интеграционные файлы MCP/Topology не трогаю.

## Цель

Добавить самостоятельный `topology_cache` в `grepmesh/src/` и тесты в
`grepmesh/tests/`, не затрагивая `src/topology.rs`, `src/config.rs`,
MCP/server files или manifests.

## Business canary

`cargo test -p grepmesh topology_cache` с временными файлами и без сети/демона.

## Scope

- Внутренний модуль кэша топологии и его тесты.
- Атомарное сохранение, загрузка, валидация, TTL, stale fallback, corrupt cache,
  duplicate host IDs, unreachable peers.

## Exclusions

- Любые изменения интеграции `Topology`/MCP/server.
- Любые изменения Cargo manifests.
- Любые сетевые вызовы к GPTAdmin или авторизация.

## Initial estimate

20–30 active minutes.

## Progress log

- 2026-08-10: Started as Worker, renamed task to `work-*`, inspected existing
  crate layout and confirmed owned paths only.
- 2026-08-10: Implemented `topology_cache.rs` plus focused integration tests,
  then verified the isolated cache test target passed.
- 2026-08-10: A later full-crate `cargo test --test topology_cache` run hit a
  fresh unrelated compile error in `grepmesh/src/index.rs` outside this slice;
  reran the same test file in a standalone temp crate and got a clean pass.

## Bounded objective

Implement an isolated cached topology module for GrepMesh. It must later be
consumed by the existing `Topology`/MCP service, but this lane must not modify
those integration files.

## Owned paths

- `/home/admin/gptadmin/grepmesh/src/topology_cache.rs`
- `/home/admin/gptadmin/grepmesh/tests/topology_cache.rs`
- Do not edit Cargo manifests, `src/topology.rs`, `src/config.rs`, MCP/server
  files, GPTAdmin files, or production state.

## Required behavior

- Model `host_id`, local URL, routable peer URL, capabilities, roots,
  generation, fetched/expiry timestamps, freshness, and last refresh error.
- Load/save the cache atomically and reject malformed or incomplete nodes.
- Support fresh, stale-but-usable, expired, and empty states.
- Merge a fresh provider result deterministically and preserve cached peers
  when refresh fails, with explicit stale/error status.
- Keep provider transport abstract; do not call GPTAdmin or invent auth here.

## Acceptance checks

- Unit tests for validation, generation, TTL, stale fallback, corrupt cache,
  atomic persistence, duplicate host IDs, and unreachable peers.
- Tests use temporary files only and do not require a daemon or network.

## Report contract

Append exact files, commands, test results, and unresolved API issues here;
finish with TL;DR for L. Do not broaden into MCP integration.

## Implementation report

Changed files:

- `/home/admin/gptadmin/grepmesh/src/topology_cache.rs`
- `/home/admin/gptadmin/grepmesh/tests/topology_cache.rs`
- `/home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-worker-topology-cache.md`

Commands:

- `cargo test --test topology_cache`
- `cargo test --test topology_cache` in `/tmp/tmp.gbKpqf6AA8`

Results:

- 9 tests passed
- 0 failed
- 0 warnings in the standalone verification run
- Main crate compile blocker: fresh unrelated error in `grepmesh/src/index.rs`
  from another lane, left untouched

Unresolved API issues:

- None for this isolated lane.

TL;DR: cached topology seam is implemented and validated in isolation; MCP
integration remains untouched.
