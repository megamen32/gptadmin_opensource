# GrepMesh core federation-first vertical slice

Role: Worker
Status: completed by L after worker recovery
Parent task: /home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-mcp.md

## Assignment

Implement the isolated `grepmesh/` Rust project for the selected Normal plan.
This is the first vertical slice: one binary/config model, local MCP tools,
direct peer fan-out through the same MCP surface, static topology fixture, and
`rg`-backed local search. The code must be ready for L to add the persistent
index/watcher and GPTAdmin topology adapter later.

## Owned paths

- Create and edit only `/home/admin/gptadmin/grepmesh/`.
- Do not edit `go-hub/`, `go-shellmcp/`, `cli.py`, existing tests, ROADMAP, or
  any production/configuration path.
- Do not install services, bind production ports, access secrets, or change
  permissions/ACLs.

## Required behavior

- Four MCP tools: `search_text`, `find_paths`, `read_text`, `search_status`.
- Streamable HTTP-compatible JSON-RPC surface on a configurable address.
- `hosts` accepts `local`, `*`, or an explicit host list; default is `*`.
- Only ingress expands wildcard. Peer calls are local-only and cannot recurse.
- Carry `request_id`, `origin_host`, and `hop_count`; deduplicate a destination
  per request and reject hop counts above one.
- Parallel fan-out with per-peer/overall deadlines; one failed peer yields
  successful results plus `partial=true` and per-host error status.
- Preserve host ID, absolute path, line number, context, deterministic order,
  explicit truncation, and bounded response size.
- `read_text` routes by host and absolute path; never substitute another host.
- Static topology must support distinct local and routable peer URLs.
- Search backend may use `rg` for the first slice, but expose a trait/seam for
  the later indexed backend.

## Acceptance checks

- Unit tests for request normalization, wildcard/hop/dedup rules, result merge,
  limits, exclusions, and path/host routing.
- Black-box two-process test: initialize/tools/list/tools/call through node A,
  find a canary on A and B, read B remotely, mutate/delete canaries, stop B,
  and prove `partial=true` with A results intact.
- No test may require production directories or ports; use temporary roots and
  ephemeral loopback ports.

## Implementation constraints

- Prefer the official Rust MCP SDK if its pinned API supports the required
  server/client transport; otherwise keep the transport adapter small and
  clearly isolated for replacement.
- Do not invent GPTAdmin integration in this lane.
- Run formatting and focused tests before reporting.

## Report contract

Append detailed files, commands, failures, and acceptance evidence to this file.
Finish with a short TL;DR for L and list any unresolved dependency/API issue.

## Recovery report by L (2026-08-10)

- The Worker timed out across three bounded waits. Its implementation was
  present in `grepmesh/` when the lane was stopped; no files outside the owned
  root were changed by the Worker.
- Created files include `Cargo.toml`, `Cargo.lock`, `src/{backend,config,lib,main,mcp,server,topology}.rs`, and `tests/mesh.rs`.
- Before L's contract correction, the two-node test reported the remote
  `read_text` payload identity as peer `B` while the test contract expected
  ingress `A`; the lane stopped without resolving this.
- L corrected the contract by adding `target_host_id`, preserving ingress
  `host_id`, switching all peer calls from `local_url` to `routable_url`, and
  making the fixture's local peer URL intentionally unreachable.
- Current focused evidence: `cargo test --all-targets` passed 3 tests, including
  the two-process fan-out/partial canary and remote read; compilation emitted
  only pre-existing unused-import warnings, which L is removing.

### TL;DR for L

Core slice recovered and green after L-owned contract fix. No unresolved SDK
dependency issue; the implementation uses an isolated small JSON-RPC adapter.

## Final integrated evidence (2026-08-10)

The core slice is included in the selected GrepMesh project. Modern transport
headers, Origin validation, notification 202, named roots, per-host failure
identity, request deduplication, response bounds, dual local/routable binds,
and multi-root persistent index integration were added after recovery. `cargo
clippy --all-targets -- -D warnings` and `cargo test --all-targets` both pass.
