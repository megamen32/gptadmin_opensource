# GrepMesh fast live delivery and A/B proof

## Raw request

"сделай чтобы работал grepmesh и работал быстро. выбери удаленный файл хост где, то и запусти сабагентам 5.4mini аб тест"

Continuation: "работай как L запускай terra агентов"

Persistent user-message record:
`.agents/user-messages/20260812-grepmesh-fast-ab.md`.

## P0

GrepMesh must work correctly and quickly on the real two-node path. A caller on
server-100 must search and read a real file on server-88 through its local MCP,
without pathological CPU/RAM use. Independent `gpt-5.4-mini` agents must A/B
the same query against direct SSH+rg and GrepMesh.

## Business canary

- Remote host/file/query: `server-88`, `/opt/mobile-browser/package.json`,
  literal `scramjet-demo`.
- `search_text` through server-100 local MCP with host `server-88`, root `opt`,
  and glob `mobile-browser/package.json` returns the exact remote path/text with
  a healthy server-88 status.
- `read_text` through server-100 local MCP reads that remote file.
- Two fresh independent `gpt-5.4-mini` A/B testers report success count,
  median, and p95 for direct SSH+rg and GrepMesh after rollout.
- Both GrepMesh services are active on the new digest and no longer retain the
  old multi-gigabyte index memory/continuous CPU profile.
- A controlled one-node loss returns remaining results with `partial=true`;
  the node is restored before completion.

## Scope

- `grepmesh/` source, tests, release artifact, guarded two-node rollout.
- Live targets only `server-100` and `server-88`.
- Terra agents for Overseer, Worker, Reviewer, Critic, and final Testers.

## Exclusions and authorization

- No five-host expansion, firewall/ACL mutation, secret creation/rotation,
  public exposure, unrelated service changes, or destructive cleanup.
- Source/test/build work is authorized by the P0.
- Applying the release and restarting `grepmesh-mcp.service` on the two named
  hosts still requires one explicit user approval at that exact boundary.
- Controlled stop/start failure canary also requires explicit approval at its
  exact boundary unless included in the same explicit answer.

## Immutable initial estimate

- Minimum / maximum active minutes: 30 / 90.
- Started at: 2026-08-12T07:50:00+03:00.
- Lifecycle provenance: current persistent goal plus explicit Lead/Terra request.
- Last task-file mtime observed: creation time 2026-08-12T07:50:00+03:00.

## Runtime identity

- Harness: Codex desktop
- PID: unknown (harness-managed)
- Agent session: current Codex task
- PID status: active (harness-observed)
- Last PID signal: Lead active
- Last task-file transition: todo created

## Current evidence

- Baseline A/B by two independent `gpt-5.4-mini` agents: direct SSH+rg 45/45,
  median 284-290 ms; old live GrepMesh 0/45 semantic successes, median
  949-992 ms, `rg exit status: 2`, `partial=true`.
- Old live resource snapshot: about 7.8 GiB RSS / 99.7% CPU on server-100 and
  45.9 GiB RSS / 79.3% CPU plus swap on server-88.
- Source fix `ca10d327...`; manifest commit `e4db2831...`; release artifact
  digest `9fbb43349e...`; no post-fix live rollout/restart.
- Terra Reviewer found a P1: accepting every rg exit 2 hides invalid regex/glob.
- First bounded Worker returned `NEEDS_RETHINK`: truthful permission-denied
  partial propagation also requires `grepmesh/src/mcp.rs` ownership.
