# GrepMesh deployment and `earn or halt` MCP canary

Role: Explorer

## Original request

> Выполни развертвывние и найди earn or halt где исходники(как тест mcp)

## Objective

Safely determine the real GrepMesh deployment target and control plane, deploy the committed GrepMesh revision only after an exact target/action preview, and verify the deployed MCP by searching for `earn or halt` in source files.

## Business canary

Through the real deployed MCP endpoint, `search_text` must return the source host, absolute path, line, and context for the exact phrase `earn or halt`; if the exact phrase is absent, the result must explicitly prove no match and report the closest source locations for the separate terms.

## Confirmed scope

- Repository: `/home/admin/gptadmin`.
- Current GrepMesh implementation: committed revision `e70dffd` (verify live before deployment).
- Read-only source discovery and deployment preflight.
- Deployment of GrepMesh only when the exact target hosts, artifact, config, and action are identified.
- Real MCP canary after deployment.

## Explicit exclusions

- Do not deploy LastHumanCommit or use its rollout manifest for GrepMesh.
- Do not change authentication, mTLS, firewall, public bind, agent configuration, or peer topology without an explicit separate gate.
- Do not restart or stop unrelated services.
- Do not discard, stash, reset, clean, or overwrite unrelated worktree changes.

## Estimate

- Initial active-minute estimate: 20 / 60 / 120 active minutes (optimistic / likely / pessimistic).
- Estimate revisions: none yet.

## Stop conditions

- `stop_when`: the exact deployment target/action preview and source locations are evidenced, or the real MCP canary passes after a gated deployment.
- `abandon_when`: no GrepMesh-specific deploy path exists and the missing target/authority cannot be resolved from local evidence; return the blocker instead of guessing.
- `forbidden_without_explicit_user_request`: public bind, auth/mTLS, firewall, ACL, credentials, unrelated service changes, destructive cleanup, or deployment to a host not named in an exact preview.

## Bounded Explorer package

- Goal: read-only identify the real GrepMesh deployment control plane and exact source checkout(s) for Earn or Halt.
- Known facts: commit `e70dffd` contains the GrepMesh implementation and only a service/config template; local port `9419` is currently not listening.
- Allowed paths: this repository's GrepMesh files and bounded local source/deployment evidence under `/home/admin/agents-projects`, `/home/admin/PycharmProjects`, `/opt`, and `/tmp`; no secrets or credentials.
- Excluded paths/actions: no edits except appending this task report, no deploy/restart/stop, no public HTTP mutation, no GitHub push.
- Acceptance check: report exact matching source paths/lines and whether a GrepMesh-specific manifest/deployer and target list exist; stop on unknown deployment authority.
- Selected class: Explorer, lowest sufficient read-only model, low effort; active budget 15 / 30 / 60 minutes; relative cost low with filesystem-search uncertainty.
- Report contract: append concise evidence, commands/results, exact blocker or deploy preview inputs, and TL;DR to this task file.

## Progress

- [x] Read-only preflight and source search.
- [x] Exact deployment preview and authorization boundary.
- [ ] Deployment, if target and gate are satisfied.
- [x] Reversible local MCP canary (not a production installation).

## Lead evidence — 2026-08-11

- `cargo build --release --manifest-path grepmesh/Cargo.toml` passed at revision `e70dffd4df1610f8c47c0cca07ecb50056acbf58`; release artifact is `grepmesh/target/release/grepmesh`, SHA-256 `d982507f5910b176dae1b20e1fd5d98d29d0fe9425a807dbbd537397aa3f4ed8`.
- A temporary local config rooted at `/tmp/eoh-docs-stage` was run on `127.0.0.1:9419` and tested only through the official MCP Inspector CLI. `tools/list` exposed all four tools; `search_text` found Earn or Halt source references, `find_paths` returned `earn_or_halt` files, `read_text` read `earn-or-halt-zcode/earn_or_halt/runtime.py:272-301`, and `search_status` reported backend `rg`, file_count `187`, `partial=false`. The process was stopped and the temporary config removed; no listener remains.
- The exact lowercase contiguous phrase `earn or halt` is absent from the bounded runtime source search; the canonical staged checkouts are `/tmp/eoh-docs-stage/earn-or-halt` (commit `f1a2d15e...`) and `/tmp/eoh-docs-stage/earn-or-halt-zcode` (commit `635ad1d9...`), from GitHub `meanwebuser/earn-or-halt` and `meanwebuser/earn-or-halt-zcode`.
- Production apply is blocked: no GrepMesh-specific deployment manifest/deployer/target list exists, `/etc/grepmesh-mcp`, `/usr/local/bin/grepmesh-mcp`, `/var/lib/grepmesh-mcp`, and the systemd unit are absent, and the service user/group `admin-search` is absent. The example config is not authorization for `server-100` or its peer values.

## Explorer evidence (2026-08-11)

- `git rev-parse HEAD` confirms GrepMesh repository revision `e70dffd4df1610f8c47c0cca07ecb50056acbf58`.
- GrepMesh has only a service template, not a deployer/manifest: `grepmesh/grepmesh-mcp.service:1-24` runs `/usr/local/bin/grepmesh-mcp --config /etc/grepmesh-mcp/config.json` as `admin-search`; `grepmesh/README.md:51-54` explicitly says production enrollment, credentials, mTLS/firewall policy, and restart are separate deployment gates. `find`/`rg` over repository deploy artifacts found no GrepMesh-specific installer, rollout manifest, or target list.
- The template/example identify a hypothetical target but do not authorize or evidence a live target: `grepmesh/config.example.json:2-5` says `host_id=server-100`, bind `203.0.113.10:9419`, root `/home/admin`; `:11-16` names `server-88` as a peer; `:27-30` references `/var/lib/grepmesh-mcp` and `https://gptadmin.internal/mcp-relay/grepmesh`. These are example values and README says they must be replaced before use.
- Current local live state is absent: `ss -ltnp` showed no `:9419` listener; `systemctl list-unit-files | rg grepmesh` showed no installed unit; bounded filesystem probe found no `/etc/grepmesh-mcp`, `/usr/local/bin/grepmesh-mcp`, or `/var/lib/grepmesh-mcp`. Prior live audit independently records the same on 2026-08-10 (`.agents/tasks/done-20260810-grepmesh-live-audit.md:40,46`).
- Exact literal phrase `earn or halt` was not found in the bounded source roots via `rg -n -F`. Separate source locations exist in staged Earn-or-Halt checkouts: `/tmp/eoh-docs-stage/earn-or-halt-zcode/earn_or_halt/__init__.py:1` (`Earn or Halt`), `.../types.py:2` (`Earn or Halt`), and `.../runtime.py:14,272-301` (halt behavior); `/tmp/eoh-docs-stage/earn-or-halt/earn_or_halt/__init__.py:1` (`Earn or Halt`) and `.../policy.py:16,46-85` (halt decisions). The exact lowercase phrase is absent, so no deployed-MCP match can currently be proven.
- The staged checkouts are distinct source snapshots, not GrepMesh deployment targets: `git rev-parse` gives `earn-or-halt=f1a2d15e229f1fa150ebc41a3816fdad8b8e263f` and `earn-or-halt-zcode=635ad1d994a8097ab2ccfa731417ecf8ebc7b857`.

### TL;DR for L

No exact GrepMesh deployment authority/target/action exists in local evidence; do not deploy or restart by guessing. GrepMesh remains source-only on host-100. The requested literal `earn or halt` is absent from searched source; closest meaningful matches are the staged checkouts and lines listed above. Highest-value next probe requires an explicit deployment target/authority (or a named real MCP endpoint) before any mutation or canary.
