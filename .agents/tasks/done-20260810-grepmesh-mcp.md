# GrepMesh MCP — архитектура и план реализации

Status: completed; normal plan selected; production gates intentionally open
Class: Full
Owner: L
Created: 2026-08-10

## Оригинальный запрос

Пользователь утвердил симметричную архитектуру GrepMesh MCP: один одинаковый
MCP-сервер на каждом из пяти хостов, локальный индекс на каждом узле,
непосредственная MCP-to-MCP федерация по `hosts="*"`, четыре инструмента
`search_text`, `find_paths`, `read_text`, `search_status`, а GPTAdmin остаётся
installer/discovery/health control plane и не проксирует поисковый трафик.

## Objective

Подготовить и после отдельного выбора/технического approval реализовать
проверяемый локальный вертикальный срез GrepMesh MCP, совместимый с текущим
GPTAdmin control plane, без центрального поискового сервера и без изменения
рабочих production-узлов до явного consequential gate.

## Business canary

AI на каждом из пяти хостов вызывает только локальный
`127.0.0.1:9419/mcp`, передаёт `hosts="*"`, получает host + абсолютный path +
line + context для пяти canary-файлов, затем через `read_text` читает удалённый
диапазон. При недоступности одного узла остальные четыре возвращаются с
`partial=true`; при недоступности GPTAdmin используется cached topology.

## Подтверждённый scope

- единый проект/бинарник GrepMesh MCP;
- локальные `search_text`, `find_paths`, `read_text`, `search_status`;
- локальный индекс с watcher/reconciliation и `rg` fallback;
- `hosts=local`, `hosts=*`, список узлов, fan-out, timeout, partial results,
  `request_id`/`hop_count`/deduplication;
- GPTAdmin discovery registration/readback seam;
- тесты контракта и acceptance canary, соответствующие бизнес-пути.

## Явные исключения

- нет центрального search-сервера, MCP-proxy или общей базы;
- не выполнять production install/upgrade/restart/deploy на пяти хостах;
- не менять ACL, mTLS, host tokens, firewall, secrets или permission policy
  автономно; это отдельная authorization boundary;
- не смешивать GrepMesh с существующими ShellMCP/tgrep демонами;
- не исправлять несвязанные dirty-worktree дефекты и не переписывать ROADMAP
  без подтверждённого scope.

## Initial active-minute estimate (immutable)

- optimistic: 60 minutes
- likely: 120 minutes
- pessimistic: 240 minutes

## Estimate revisions (append-only)

- 2026-08-10 revision 1: trigger = independent Adviser found that the
  defining two-node federation canary requires a routable peer endpoint and a
  new control-only topology seam, while current Hub discovery routes are
  executable relays; evidence = `done-20260810-grepmesh-adviser.md` and
  `done-20260810-grepmesh-explorer.md`. Revised likely active time for the
  recommended Shape A is 90 / 150 / 260 minutes; the immutable initial
  estimate remains above.

## Stop conditions

- `stop_when`: три плана и acceptance proof для одинакового business canary
  подготовлены, пользователь выбрал план, дал технический approval, а затем
  выбранный локальный срез прошёл focused checks, review и реальный canary.
- `abandon_when`: подтверждено, что текущий GPTAdmin transport не может
  предоставить нужный consumer seam без архитектурной замены, либо пользователь
  отменяет GrepMesh; сохранить evidence и не подменять результат соседним
  ShellMCP/relay.
- `forbidden_without_explicit_user_request`: production install/upgrade,
  restart, deploy, rollback, ACL/mTLS/firewall/secret/permission changes,
  destructive filesystem/index cleanup, public exposure, or a new central
  service.

## Initial Russian plan

1. Сопоставить существующий GPTAdmin discovery/control-plane контракт и
   определить минимальные seams без мутации.
2. Получить независимое архитектурное заключение и bounded reconnaissance по
   локальному MCP/индексу и тестовым поверхностям.
3. Предложить три полных варианта с одинаковым business outcome, оценками,
   рисками и графом параллельных lane; ждать выбора пользователя.
4. После выбора показать технический preview и ждать второго approval перед
   реализацией.

## Evidence log

- 2026-08-10: worktree branch is ahead 11 commits and contains many existing
  modified/untracked files; no files newer than five minutes were observed.
- 2026-08-10: existing GPTAdmin Hub exposes canonical `discover → schema →
  execute`; legacy MCP list aliases remain compatibility paths.
- 2026-08-10: no existing `grepmesh` match was found in the repository scan.
- 2026-08-10: independent Explorer found the managed MCP projection in
  `cli.py:132-138,2439-2463,2732-2797`, the single ShellMCP lifecycle seam in
  `go-shellmcp/internal/supervisor/supervisor.go:32-65,276-292,352-385`, and
  Hub registration/readback in `go-hub/internal/hub/server.go:872-890,
  2409-2531,3627-3640`.
- 2026-08-10: independent Adviser recommended Shape A: two identical local
  MCP processes with bounded `rg` backend and static topology fixture first;
  index/watcher and GPTAdmin cached topology follow the green two-node
  black-box canary.
- 2026-08-10: current MCP documentation confirms Streamable HTTP Origin
  validation, localhost binding guidance, and authentication requirements;
  current draft also changes protocol/session/header details, so protocol
  version must be pinned in the technical preview rather than assumed.

## Plan preview delivered (2026-08-10)

- Максимально идеальный: сначала полный control-only GPTAdmin topology API,
  затем Rust trigram index/federation/conformance and five-node rollout proof;
  160 / 280 / 480 active minutes, high cost.
- Нормальный (recommended): isolated `grepmesh/`, two-node black-box MCP
  federation with `rg`, then index/watcher and cached topology seam;
  90 / 150 / 260 active minutes, medium cost.
- YAGNI 80/20 — полный результат: same business canary with minimal durable
  index, required federation and minimal topology adapter; omit advanced
  optimization/observability/conformance extras; 75 / 125 / 210 active
  minutes, low-medium cost.

Status: selected `Нормальный`; implementation started under active goal continuation.

## Selected technical preview (2026-08-10)

### Call stack

`AI -> 127.0.0.1:9419/mcp -> rmcp tool router -> ToolFacade ->
TargetResolver -> FederationCoordinator -> LocalSearchBackend + PeerClient ->
ResultMerger -> MCP response`.

`read_text` uses `HostRouter -> LocalFileReader` or the same MCP `PeerClient`;
`search_status` reads `TopologyProvider + IndexState`. The local client URL and
routable peer URL are separate fields; a peer never calls another host's
loopback address.

### Proposed file tree

`grepmesh/Cargo.toml`, `src/main.rs`, `src/config.rs`, `src/model.rs`,
`src/mcp/{server,client,tools}.rs`, `src/federation.rs`,
`src/search/{mod,rg,indexed}.rs`, `src/index/{store,watcher,reconcile}.rs`,
`src/topology/{mod,static_cache,gptadmin}.rs`, `src/health.rs`,
`tests/{mcp_blackbox,federation_blackbox,index_differential}.rs`,
`tests/fixtures/topology-2.json`, `scripts/acceptance-two-node.sh`, and
`deploy/grepmesh-mcp.service.example`.

Later, after the local/federation join, the narrow GPTAdmin seam may touch
`go-hub/internal/hub/{server,grepmesh_discovery}.go` plus focused tests and the
existing managed-MCP projection in `cli.py`; no parallel supervisor or search
proxy is introduced.

### Key contracts

`HostId`, `Hosts::{Local,All,Explicit}`, `SearchTextRequest`,
`FindPathsRequest`, `ReadTextRequest`, `SearchResponse`, `HostStatus`,
`TopologyNode{host_id,local_url,peer_url,generation,expires_at}`,
`SearchBackend`, and `TopologyProvider`.

The ingress node alone expands `All`; forwarded calls carry
`hosts=Local`, the same `request_id`, and `hop_count=1`. Results are sorted
deterministically and include host/path/line/context, per-host status,
`partial`, and `truncated`.

### Pseudocode

```text
search_text(request):
  normalized = validate_defaults_and_limits(request)
  targets = topology.resolve(normalized.hosts)
  responses = parallel_with_deadline(targets,
    local -> backend.search(normalized),
    peer -> mcp_call(peer_url, normalized with hosts=local, hop=1))
  return merge_sorted(responses, partial=any_non_success)
```

### Migration and proof

Start with two local processes and static topology, prove MCP black-box
initialize/tools/list/tools/call, wildcard fan-out, failed-peer partial result,
remote read, canary update/delete, and exclusion globs. Add index/watcher with
`rg` differential proof, then cached topology, then GPTAdmin registration and
readback. The five-host live canary is a later gated action, not part of the
first source mutation.

### Execution graph

After approval: L freezes schemas; two independent `5.4-mini` Worker lanes own
`src/search` and `src/mcp/federation`; L joins them at the two-node black-box
canary; index and topology lanes then run in parallel; L integrates; a fresh
`5.4-mini` Reviewer checks the coherent diff, `5.6-terra` Critic gates release,
and a fresh Tester runs the real MCP surface in `only-new` mode.

### Authorization boundaries

No production install, upgrade, restart, deployment, public bind, ACL, mTLS,
firewall, secret, or permission change is included. The exact five-host
enrollment/restart action will require a separate direct approval at the point
of execution.

## Implementation progress (English)

- 2026-08-10: verified the active goal continuation as implementation
  authorization; no `grepmesh/` files existed before the first Worker.
- 2026-08-10: Rust 1.96.1/Cargo 1.96.1 is available; no existing Cargo
  workspace or cached Rust MCP SDK was found.
- 2026-08-10: Worker package dispatched with exclusive ownership of
  `grepmesh/`; GPTAdmin and production paths remain untouched.

- 2026-08-10: Integrated the isolated Rust project and the read-only GPTAdmin
  projection. The current implementation includes four tools, wildcard/local/
  explicit host routing, request_id deduplication, hop_count loop protection,
  direct routable peer fan-out, remote read, per-host partial results, named
  roots, dual local/routable bind support, atomic per-root indexes, notify
  watchers, reconciliation, bounded trigram acceleration with safe `rg`
  fallback, binary/size/sensitive-path exclusions, cached topology, periodic
  GPTAdmin refresh, response bounds, current transport-header compatibility,
  Origin validation, and a control-only `/mcp-relay/grepmesh` endpoint.
- 2026-08-10: Evidence after integration: `cd grepmesh && cargo test
  --all-targets` passed 2 unit-test binaries plus 7 index, 3 mesh, 3
  search/root, and 9 topology-cache tests; the mesh test exercises modern
  `MCP-Protocol-Version`/`Mcp-Method`/`Mcp-Name` headers, wildcard fan-out,
  remote read, and failed-peer partial output. `cd go-hub && go test
  ./internal/hub -run 'TestGrepMeshTopology' -count=1` passed.
- 2026-08-10: Official MCP transport review recorded a remaining release gate:
  the adapter is stateless JSON-response Streamable HTTP with compatibility
  validation, but it is not yet wired to the official `rmcp` transport crate;
  deployment authentication/mTLS and five-host live acceptance remain gated
  and were not performed.
- 2026-08-10: Independent Reviewer returned `CHANGES_REQUIRED` with three
  scoped correctness findings: empty fresh discovery must be valid; successful
  provider refresh must remove decommissioned peers instead of unioning them
  forever; and aggregate status must surface a degraded/corrupt secondary-root
  index. A bounded Worker fix task was opened before any further gate.
- Estimate revision (2026-08-10): add 20 / 35 / 60 active minutes to the
  Normal estimate because Reviewer evidence exposed three correctness paths
  requiring implementation and regression proof before the fresh gates.
- 2026-08-10: Recovery fixed all three Reviewer findings. Fresh empty topology
  is valid, successful provider refresh is authoritative for peer membership,
  and aggregate index state reports the worst configured root. The corrected
  implementation passed clippy and all tests: 4 unit, 7 index, 3 mesh, 3
  search/root, and 10 topology-cache tests. Fresh Reviewer rerun returned
  `APPROVE`; the final Critic and only-new Tester remain.
- 2026-08-10: Final Critic returned `RETHINK` solely for an acceptance-proof
  gap: the fresh real-surface test must explicitly omit `hosts` and capture
  raw output from an independent CLI MCP consumer. The Tester task was amended
  before rerunning that gate; no implementation defect was identified.
- 2026-08-10: Final Critic rerun returned `PASS` after the acceptance task
  explicitly required an omitted `hosts` field and raw independent-client
  evidence. Fresh `only-new` Tester is now the final handoff gate.
- 2026-08-10: The fresh official MCP Inspector CLI reached the temporary
  GrepMesh node but returned raw `Server's protocol version is not supported:
  2026-07-28` before `tools/list`. This is a selected-scope interoperability
  defect, not a missing-surface blocker; a bounded Worker task now implements
  client-version negotiation and regression tests. Temporary A/B processes
  were stopped and their exact temp directory was removed by the Tester.
- 2026-08-10: Recovered the protocol fix in `server.rs`: initialize now
  negotiates a client-supported MCP version and defaults to `2025-06-18`, while
  explicit modern `2026-07-28` requests remain supported. Lint and full Rust
  tests are green; a fresh Reviewer is checking this last fix before the
  Inspector Tester rerun.
- 2026-08-10: The first post-fix Inspector rerun reached the CLI but hit a
  temporary setup race (`ECONNREFUSED` on A before the server listener was
  ready); no protocol response was observed. The temporary processes/data were
  cleaned. The next fresh Tester assignment adds a pre-build, readiness wait,
  and one bounded Inspector retry.
- 2026-08-10: Final official Inspector Tester returned `PASS`. Raw evidence
  proves `tools/list` exposes all four tools; `search_text` with `hosts` omitted
  returns A+B canaries and `partial=false`; `find_paths` returns both paths;
  remote `read_text` returns B content through A; status reports both healthy;
  after stopping B, omitted-host search retains A and returns `partial=true`.
- Estimate revision (2026-08-10): add 15 / 25 / 45 active minutes for the
  Inspector protocol negotiation fix, fresh review, readiness retry, and raw
  user-surface rerun; trigger was the real Inspector rejection and the first
  listener-readiness race.
- Final handoff state (2026-08-10): selected implementation, Reviewer, Critic,
  and official Inspector Tester are complete and green. No production
  enrollment, restart, deployment, public bind, ACL, mTLS, firewall, or secret
  operation was performed.
