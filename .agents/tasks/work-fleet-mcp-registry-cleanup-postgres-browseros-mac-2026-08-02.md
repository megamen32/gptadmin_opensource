# Fleet MCP Registry Cleanup, Local PostgresMCP, and Mac BrowserOS

Status: active
Class: Full
Selected plan: Normal

## Original User Request

> о выключи chromedevtools и openmemory мы теперь memos tensor используем и agentmonitor больше не нжуен а вот postgresmcp надо бы добавть в локалхост opencode/zcode/codex/claude посмотреи ~/agents-projects/   fleet
>
> и там наверное есть какие то еще тестовые вещи которые уже давно не онлайн и их можно просто удалить и mac chrome переведи на browseros mac

## Objective

Design and, after explicit user plan selection, implement a topology-aware Fleet cleanup: retire obsolete ChromeDevTools/OpenMemory/AgentMonitor registrations, expose the canonical PostgresMCP locally to OpenCode/ZCode/Codex/Claude, migrate Mac browser access to BrowserOS Mac, and remove only independently proven obsolete test registrations.

## Business Canary

On the selected local/Fleet hosts, OpenCode, ZCode, Codex, and Claude each discover and complete a harmless read-only PostgresMCP call; Mac browser automation discovers and completes a harmless BrowserOS Mac call; retired registrations are absent; required existing BrowserOS/LAN paths remain functional; and every removed target has pre-change evidence plus rollback data.

## Confirmed Scope

- Read-only inventory of GPTADMIN MCP targets and `~/agents-projects` Fleet configuration.
- Identify current owners/config sources for ChromeDevTools, OpenMemory, AgentMonitor, PostgresMCP, Mac Chrome, and BrowserOS Mac.
- Distinguish stale/offline from intentionally dormant before proposing deletion.
- Prepare exactly three implementation plans with rollback and verification.
- Perform no mutation until explicit human selection.

## Explicit Exclusions

- No credential copying, printing, rotation, or auth redesign.
- No database/schema/data changes; only MCP registration and harmless read-only canaries.
- No deletion based solely on `stale`/offline status or naming.
- No unrelated security, permissions, observability, provider, deployment, or host cleanup.
- Preserve the existing required BrowserOS MCP/LAN path unless the user explicitly changes that requirement.

## Initial Active-Minute Estimate (immutable)

- Optimistic: 90 minutes
- Likely: 180 minutes
- Pessimistic: 360 minutes

## Estimate Revisions (append-only)

- 2026-08-02: likely 210 minutes / pessimistic 420 minutes. Trigger: research
  confirmed that Fleet has no per-client MCP translators, ZCode lacks persistent
  MCP materialization, and Hub exposes no supported stale-registry prune API.

## Plan (RU)

1. Найти канонические Fleet/agent config sources и текущую topology.
2. Проверить live identity и безопасные read-only canary для затронутых MCP.
3. Отделить подтверждённо устаревшие targets от временно offline/dormant.
4. Провести Overseer scope-check и предложить три варианта без реализации.
5. После явного выбора выполнить выбранные стадии с отдельным Overseer gate.

## Progress (EN, append-only)

- 2026-08-02: Task opened as Full. No registration, service, credential, host, or runtime mutation has started.
- 2026-08-02: Current server-100 supervisor inventory contains BrowserOS,
  PostgresMCP, and `memos-shared`; ChromeDevTools, OpenMemory, and AgentMonitor
  return `unknown agent ref` and survive only as stale Hub discovery records.
- 2026-08-02: PostgresMCP is enabled but cannot start: its configured
  `cwd=/home/roomhacker` does not exist in the active server-100 execution
  environment. The config uses restricted mode and a `DATABASE_URI` environment
  key; no value was retained or copied.
- 2026-08-02: Mac identity is `MBP-User`/arm64. BrowserOS PID 2275 answers HTTP
  200 on port 9000 but currently binds `*:9000`, while the Mac supervisor still
  contains only enabled legacy `chrome-mac` and no BrowserOS registration.
- 2026-08-02: Local configs already contain `memtensor-shared` for OpenCode,
  Codex, and Claude. OpenCode's old remote `memory` entry is disabled. Global
  PostgresMCP is absent; Claude has one project-local `postgres` entry only.
- 2026-08-02: Fleet models OpenCode/Codex/Claude/ZCode MCP capability but only
  copies opaque resources; it has no serializers. ZCode 0.15.2 exposes `/mcp`
  interactively, but Fleet records persistent config as unsupported.
- 2026-08-02: Candidate extra stale records are `manual-probe`, `shell:probe`,
  `shell:.env`, `shell:ubuntu24`, and duplicate-looking `shell:MacBook`; none is
  approved for deletion without exact owner/dependency/last-seen proof.
- 2026-08-02: A supervisor-list response unexpectedly contained an unrelated
  unredacted argument secret. It was not repeated, stored, or used; all later
  live outputs were field-whitelisted.
- 2026-08-02: Overseer approved presentation of exactly three plans only. It
  requires an explicit Postgres architecture choice, honest ZCode limitation,
  Mac BrowserOS binding correction, preservation gates, and proof-backed extra
  target deletion. Implementation remains blocked pending human selection.
- 2026-08-02: Added one Proposed ROADMAP entry while preserving the concurrent
  selected Notify/Agent-Herder work and the still-open P0.2 tradeoff.
- 2026-08-02: User selected the recommended Normal plan. Implementation is
  queued immediately after the newly explicit P0 resource-receipt goal.

## Research Summary

- Explicit retirement set: server-100 ChromeDevTools, OpenMemory, AgentMonitor,
  Mac `chrome-mac`, and Mac Chrome CDP historical registrations. Active
  supervisor evidence must remain separate from Hub historical-registry cleanup.
- Extra stale candidates remain preview-only: `manual-probe`, `shell:probe`,
  `shell:.env`, `shell:ubuntu24`, and `shell:MacBook`. Desktop, BeyondInfinity,
  PtyMcp, OmniRoute, AgentResume, and every other unproven target are excluded.
- Required preservation: MemTensor/MemOS, server-100 BrowserOS, authenticated
  browser profiles, direct MCP/LAN paths, database/schema/data, and unrelated
  Fleet/client entries.

## Варианты реализации (ожидают выбора пользователя)

### 1. Максимально идеальный

- **PostgresMCP:** ввести versioned credential-free canonical MCP model в
  `agent-harness-fleet` и полноценные materializer/translator adapters для
  OpenCode JSONC, Codex TOML, Claude JSON и persistent ZCode settings. Один
  локальный wrapper читает только имя secret/environment source, не копируя URI.
- **Registry:** добавить в GPTADMIN поддерживаемый preview/forget lifecycle с
  owner, last-seen, tombstone, exact allowlist, atomic receipt и idempotent
  restore; убрать ручное редактирование `registry_state.json`.
- **Mac:** сначала перевести BrowserOS с `*:9000` на loopback-only, затем
  зарегистрировать `browseros-mac` через direct-SSH local forward/OAuth relay;
  удалить legacy Chrome только после `initialize`/`tools/list` canary.
- **Удаление:** explicit retirement set плюс доказательно бесхозные test targets;
  остальные stale records сохраняются.
- **Не входит:** DB/schema/data, profile/cookie/Keychain transfer, credential
  rotation, public BrowserOS port, unrelated registry cleanup.
- **Риски/компромисс:** лучший долговременный контракт и минимальный future
  drift, но новая Fleet/Hub архитектура и самый большой blast radius.
- **Проверка:** red/green adapters and registry lifecycle tests; dry-run diff for
  each client; four-client `tools/list` plus harmless read-only Postgres canary;
  Mac identity/loopback/BrowserOS canary; exact absence and preservation matrix.
- **Rollback/migration cost:** high; versioned state migration, per-client
  backups, Hub registry restore, and legacy Mac Chrome re-enable receipt.
- **Оценка:** 240 / 420 / 720 active minutes; relative cost high; multiple local
  and live-host tool rounds, with ZCode as the largest uncertainty.

### 2. Нормальный (рекомендуется)

- **PostgresMCP:** добавить узкий `deploy/postgresmcp` installer/materializer в
  Fleet только для этого MCP. Он создаёт native entries for OpenCode/Codex/
  Claude and a tested persistent ZCode adapter; all four call one localhost
  restricted wrapper using `DATABASE_URI` indirection. Исправить invalid cwd в
  server-100 child отдельно и не смешивать с local-client config.
- **Registry:** сделать one-shot, exact-allowlist, dry-run-first migration for
  only the explicit retirement set, with pre/post hashes and atomic backup.
  Extra stale candidates are removed only when the same run proves no active
  supervisor/config/service owner; otherwise they remain in the report.
- **Mac:** обязательно bind BrowserOS to loopback, register via short-lived
  direct-SSH forward, canary, then retire both legacy Mac Chrome records.
- **Не входит:** generic Fleet MCP schema, permanent Hub TTL/tombstone API,
  automatic cleanup of every stale target, credential audit/rotation.
- **Риски/компромисс:** ZCode adapter and Hub one-shot migration are bespoke,
  but scope remains reviewable and reversible; moderate maintenance cost.
- **Проверка:** failing fixtures first; installer dry-run/idempotency; backups;
  four-client discovery/read-only Postgres canary; Mac HTTP/MCP identity and
  loopback check; retired/retained target matrix after Hub reload.
- **Rollback/migration cost:** medium; restore four client backups, Postgres
  child config, Hub state backup, and legacy Mac registration independently.
- **Оценка:** 120 / 210 / 360 active minutes; relative cost medium; tool overhead
  medium and ZCode persistence remains the main uncertainty.

### 3. YAGNI MVP

- **PostgresMCP:** directly back up and edit OpenCode, Codex and Claude configs
  with one local restricted wrapper. ZCode receives only a documented/session
  `/mcp connect` path because durable materialization is not proven.
- **Registry/Mac:** fix Mac loopback binding, add BrowserOS Mac, canary, then
  remove exact ChromeDevTools/OpenMemory/AgentMonitor/Mac Chrome records using a
  one-time state backup. Leave every extra stale/test target untouched.
- **Не входит:** Fleet source integration, persistent ZCode support, generic or
  proof-driven test-target cleanup, durable Hub prune mechanism.
- **Риски/компромисс:** fastest recovery, but three configs drift manually,
  ZCode requirement is incomplete, and future stale records may reappear.
- **Проверка:** three persistent clients plus one ZCode session canary; Mac
  BrowserOS canary; exact explicit target absence; preservation checks.
- **Rollback/migration cost:** low/medium; restore direct client configs, Hub
  state backup, and legacy Mac registration.
- **Оценка:** 45 / 90 / 150 active minutes; relative cost low; uncertainty moves
  into ZCode incompleteness and manual Hub-state handling.

## Recommendation

Choose **Нормальный**. It satisfies the requested durable four-client outcome
without turning this cleanup into a general registry/Fleet redesign, while
retaining exact backups and refusing stale-name deletion without proof.
