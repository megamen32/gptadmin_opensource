# GrepMesh architecture and rg latency diagnosis

Role: Lead
Class: Full
Status: read-only diagnosis in progress
Created: 2026-08-10

## Original request

Почему GrepMesh медленный, где архитектура свернула не туда, и можно ли
использовать обычный `rg` на каждом хосте с MCP-federation между хостами.

## Objective

Trace the current search path and explain the latency overhead, then assess a
minimal rg-native mesh design against the current index/watcher design.

## Business canary

Produce an evidence-backed causal path from MCP request to local/remote search,
identify the measurable latency contributors, and state what should be kept,
removed, or deferred. No performance claim from source inspection alone.

## Confirmed scope and exclusions

- Read-only code and bounded benchmark diagnosis.
- No code changes, service installation, restart, deployment, or config mutation.
- No secret output and no production traffic mutation.

## Initial estimate

20 / 40 / 70 active minutes.

## Evidence log

- 2026-08-10: graphify fast-path was invoked, but the existing `graphify-out/graph.json` predates the GrepMesh files and returned no relevant `search_text`/`PersistentIndex` nodes; diagnosis therefore uses the current source and bounded black-box measurements rather than stale graph output.
- 2026-08-10: current path is `tools/call` -> `MeshService::call_search` -> `search_across` -> local `LocalBackend::search_text` or peer `call_remote`; local search uses `spawn_blocking`, clones the entire persistent index snapshot for candidate lookup, and then still starts `rg` to verify candidates. Peer fan-out waits for `join_all` under the overall timeout.
- 2026-08-10: controlled A/B benchmark on the same 1,000-file temporary corpus: indexed GrepMesh MCP HTTP search median 0.970 s after warm-up; GrepMesh configured without `index_path` (rg-only backend) median 0.050 s. This isolates the index/watcher path as the dominant overhead in the current implementation, while raw `rg` itself was about 0.010 s in the earlier seven-run comparison.
- 2026-08-10: source evidence: `PersistentIndex::snapshot()` clones `IndexSnapshot` containing all entries and trigram maps; `with_index()` performs an initial build and starts a watcher; `search_text()` calls `index_candidate_paths()` and then `search_text_impl()`, which always invokes a new `rg` process. The current index is therefore an O(index-size) per-query copy plus an `rg` subprocess, not an in-memory search engine.

## Result

Diagnosis complete: the simplest correct architecture is a thin MCP wrapper
around local `rg` on every node, with direct parallel MCP fan-out and remote
read. Persistent indexing/watching should be removed from the critical search
path and reconsidered only after profiling a real corpus.
