# Исправить manifest updater и BrowserOS MCP через GPTAdmin

Status: completed
Original user request: «Супер, раз нашел баг, то исправь его в GPT-админе. А еще почему-то попробуй через GPT-админ вызвать браузер osMCP. Там тоже какая-то ошибка. Сейчас это сделай, зафиксируй два бага, ставь цель их исправить.»
Objective: Исправить два подтверждаемых дефекта GPTAdmin: несовместимый тип `build_version` в ShellMCP auto-update manifest и ошибку вызова BrowserOS MCP через GPTAdmin; добавить регрессии и доказать оба исправления живыми E2E.
Business canary: (1) установленный на Mac mini build 141 успешно читает manifest через действующий Hub без parse error; (2) реальный GPTAdmin target выполняет BrowserOS MCP tool и возвращает валидный BrowserOS-ответ через полный Hub → ShellMCP → MCP supervisor → BrowserOS маршрут.
Confirmed scope: GPTAdmin manifest producer/contract и его тесты; GPTAdmin MCP relay/ShellMCP/BrowserOS integration dependency chain; локальная разработка; необходимые обратимые deploy/restart шаги только после human plan selection и gates; live Mac mini/BrowserOS canaries.
Explicit exclusions: обновление macOS; изменение BrowserOS shared profile/data; ротация секретов; unrelated Hub/UI/infra/security audits; очистка чужого dirty worktree; reboot любого хоста.
Acceptance: BUG-1 имеет красную регрессию на тип manifest, зелёную реализацию и live updater canary; BUG-2 воспроизводится через GPTAdmin, имеет точный root cause, красную регрессию, зелёную реализацию и artifact-producing live BrowserOS MCP E2E; direct LAN GPTAdmin route и shared authenticated BrowserOS topology сохранены.
Initial estimate (optimistic / likely / pessimistic active minutes): 60 / 120 / 240
Estimate revisions (append-only; trigger and evidence):
- 2026-08-03, после Overseer `REPLAN` и выбора YAGNI MVP: 55 / 95 / 170. Trigger: artifact-version ownership and two live BrowserOS ownership surfaces had to be proven. Evidence: live Hub has `gptadmin-shellmcp.json`; system ShellMCP loads `/etc/gptadmin/mcp-supervisor.json`; direct relay loads the separate user agent config.
- 2026-08-03, после selected-plan Overseer `REPLAN`: 75 / 130 / 220. Trigger: live archive, sidecar, and `.sha256` are three different historical builds/digests, so fail-closed handler deployment requires a coherent artifact triple. Evidence: served archive embeds build 102, sidecar reports build 107, while installed Mac and release tag are build 141; all three digests disagree.
Cycle: full
Workflow: YAGNI → Normal → Ultimate; конкретный объём будет выбран пользователем после трёх evidence-backed планов.
Current stage: completed

## Bug records

### BUG-GPTADMIN-MANIFEST-BUILD-VERSION-TYPE

- Symptom: ShellMCP build 141 пишет `parse manifest: json: cannot unmarshal string into Go struct field .build_version of type int`.
- Expected: опубликованный `/artifacts/shellmcp.json` соответствует updater schema build 141 и читается без ошибки.
- Reproduction/status: live symptom captured on `mac-mini-2012.lan`; source trace confirms `go-hub/internal/hub/server.go:shellmcpArtifactManifest` emits string `BuildVersion`, while `go-shellmcp/internal/update/update.go:CheckOnce` decodes `build_version` as `int`.
- Smallest regression: producer-shaped manifest with `"build_version":"141"` fails the current updater decoder; Hub endpoint coverage currently checks availability/body but not the JSON type.

### BUG-GPTADMIN-BROWSEROS-MCP-ROUTE

- Symptom: direct GPTAdmin target `roomhacker-server-100-browseros` leaves `schema` permanently `running`; GPTAdmin ShellMCP call `mcp_tools(ref=BrowserOS)` completes with `error: EOF`.
- Expected: GPTAdmin обнаруживает BrowserOS tool schema и выполняет безопасный BrowserOS MCP canary через выбранный target.
- Reproduction/status: confirmed live through GPTAdmin. The enabled relay config launches `mcp-remote http://127.0.0.1:9200/mcp`; port 9200 refuses connections. The preserved shared BrowserOS process is healthy and serves MCP on `127.0.0.1:9002/mcp` (HTTP 200). Root cause is stale GPTAdmin target configuration, not BrowserOS availability.

## Candidate plans

### Максимально идеальный

- Outcome/scope: make the manifest contract explicit and versioned; emit numeric `build_version`, retain strict and compatibility decoder tests, add a durable BrowserOS target declaration/validator, migrate the live registration to the canonical shared endpoint, and verify both public and ShellMCP BrowserOS routes plus updater E2E.
- Omitted: no macOS update, shared-profile mutation, secret rotation, or unrelated fleet cleanup.
- Tradeoffs/risks: strongest drift prevention, but introduces a new configuration ownership surface and broader deploy/test scope.
- Verification: focused Go/Python regressions, full affected suites, live Hub manifest decode on build 141, direct target schema/tool call, ShellMCP `mcp_tools`/safe `mcp_call`, rollback rehearsal.
- Migration/rollback: back up exact BrowserOS registration and Hub artifact state; atomically switch; restore prior config/build if either canary fails.
- Execution profile: Lead current Codex model; Explorer children `gpt-5.6-luna` low; Reviewer/Overseer role-selected models; OpenAI priority quota. Active minutes 120 / 210 / 360; relative cost high, tool overhead high, uncertainty medium.

### Нормальный (recommended)

- Outcome/scope: add a failing contract regression, make Hub emit numeric `build_version`, make updater tolerate both numeric and numeric-string manifests during rollout while rejecting invalid values; back up and update only the live BrowserOS GPTAdmin registration from stale `9200` to canonical `9002`; add a focused configuration regression/check that catches an unreachable configured MCP endpoint; deploy/restart only affected GPTAdmin components and prove both E2E canaries.
- Omitted: no new fleet-wide declarative configuration framework and no BrowserOS profile/service changes.
- Tradeoffs/risks: backward-compatible and durable at the manifest boundary; BrowserOS drift prevention is targeted rather than a general registry reconciler.
- Verification: red/green focused tests, affected Go/Python suites, live Mac updater manifest fetch, GPTAdmin direct schema plus harmless BrowserOS tool, ShellMCP tools/call path, config backup/rollback receipt.
- Migration/rollback: exact backup of BrowserOS config; reversible service restart; old Hub/ShellMCP artifacts retained until canaries pass.
- Execution profile: Lead current Codex model; Explorer children `gpt-5.6-luna` low; Reviewer/Overseer role-selected models; OpenAI priority quota. Active minutes 75 / 130 / 240; relative cost medium, tool overhead medium, uncertainty low-medium.

### YAGNI MVP

- Outcome/scope: make the live Hub handler read numeric `build_version` and `git_commit` from the artifact-local `gptadmin-shellmcp.json`, verify its digest against the exact served archive, and add a handler regression; build a coherent build-141 ShellMCP archive/JSON/SHA triple from immutable tag `v141` in an isolated worktree; build the patched Hub hotfix from the same `v141` base; atomically publish that matched set; add a distinct canonical system entry `browseros-local` with `agent_id=BrowserOS` and endpoint `127.0.0.1:9002/mcp`, update only its selected runtime row plus the direct-relay user config, and restart only `gptadmin-hub.service`, `shellmcp.service`, and user `gptadmin-mcp-browseros.service`; run both live canaries.
- Ownership: immutable tag `v141` is the release source; its built `/opt/gptadmin/build/gptadmin-shellmcp.json` is artifact-local version/digest authority for the exact archive; `/etc/gptadmin/mcp.json` owns new `browseros-local` without changing existing `browseros-mac`; `/etc/gptadmin/mcp-supervisor.json` is the selected ShellMCP runtime projection; the user agent config owns the separately discovered direct target.
- Omitted: no updater compatibility decoder for old string manifests, no general fleet reconciler/validator, no BrowserOS process/profile/service changes, and no unrelated MCP row changes.
- Tradeoffs/risks: minimal selected scope; old string-emitting Hubs remain incompatible until updated, and general config drift detection remains outside this plan.
- Verification: RED Hub handler regression before code; RED secret-safe BrowserOS GPTAdmin route receipt before config; GREEN focused Go tests; exact pre/post config assertions; live Mac updater manifest fetch without parse error; GPTAdmin direct `schema` and `tabs(action=list)`; ShellMCP `mcp_tools` and `mcp_call` through `ref=BrowserOS`.
- Migration/rollback: exact timestamped backups plus SHA-256 for all three config files, previous Hub binary, and old artifact triple; publish archive/JSON/SHA together through staged files and atomic renames; restore and restart exact owner service on any failed canary.
- Execution profile: Lead current Codex model; Reviewer/Overseer role-selected models; OpenAI priority quota. Revised active minutes 55 / 95 / 170; relative cost low-medium, tool overhead medium, uncertainty low-medium.

## План

1. Через существующий Graphify graph (если он есть) проследить producers/consumers manifest и GPTAdmin→BrowserOS MCP route.
2. Воспроизвести оба дефекта на точных live маршрутах, сохранив secret-safe receipts.
3. Запустить bounded Explorer/Overseer research и интегрировать root causes.
4. Представить три плана в порядке «Максимально идеальный», «Нормальный», «YAGNI MVP» и дождаться выбора пользователя.
5. После выбора реализовать каждый fix test-first, развернуть обратимо и доказать оба business canary.

## Overseer decision history (append-only)

- Timestamp: 2026-08-03
  Stage: research
  Evidence: source producer/consumer trace, live GPTAdmin schema/job receipts, live port/MCP initialize checks
  Current user P0: исправить два GPTAdmin бага с live E2E
  Business delta: 0/2 fixes and 0/2 post-fix canaries; root causes localized
  P0 distance: SAME
  Questions for L: artifact version ownership; canonical/generated BrowserOS config; secret-safe receipt; exact canary tool
  Decision: REPLAN; binding verdict RETHINK
- Timestamp: 2026-08-03
  Stage: selected-plan pre-implementation
  Evidence: preflight receipt plus live artifact triple hashes and embedded build info
  Current user P0: selected YAGNI fix for both bugs with direct and ShellMCP BrowserOS E2E
  Business delta: 0/2 fixes and 0/2 post-fix canaries; deployment hazard localized
  P0 distance: SAME
  Questions for L: coherent artifact publication; distinct canonical local BrowserOS name
  Decision: REPLAN; build coherent v141 artifact triple and add `browseros-local` without touching `browseros-mac`
- Timestamp: 2026-08-03
  Stage: corrected selected-plan pre-implementation
  Evidence: immutable v141 artifact plan, atomic triple publication, distinct `browseros-local`, dual-route canaries
  Current user P0: selected YAGNI fix for both bugs with live E2E
  Business delta: 0/2 fixes and 0/2 post-fix canaries; all implementation blockers closed
  P0 distance: CLOSER
  Questions for L: none
  Decision: PROCEED
- Timestamp: 2026-08-03
  Stage: completion audit
  Evidence: RED/GREEN regressions, coherent live manifest, Mac updater receipt, direct and full-route BrowserOS tab jobs, stable service/hash/config receipts
  Current user P0: fix both GPTAdmin bugs and prove both production routes
  Business delta: 2/2 fixes and 2/2 business canaries
  P0 distance: CLOSER
  Questions for L: none
  Decision: PROCEED; task may be marked done

## Critic decision history (append-only)

Не запускать до release/необратимого решения.

## Decision

Research: Graphify fast path plus two bounded Explorer reports integrated; exact live failures and root causes confirmed.
Plans: corrected after Overseer REPLAN with artifact and BrowserOS ownership paths.
Human selection: `3` — YAGNI MVP, 2026-08-03.
Selected-plan WSFF: minimal producer handler plus exact live config repair; no general validator or compatibility decoder.

## Work

Current: Completed.
Next: none.
Blocked by: нет.
Evidence: `trash/logs/20260803-gptadmin-manifest-browseros-preflight.md`; RED/GREEN tests, reviewed hotfix commit `2bbf657`, atomic backup receipt, numeric live manifest, direct and ShellMCP BrowserOS tab jobs.

## Result

Summary: Both requested fixes implemented, deployed, and accepted by independent completion audit. 2/2 business canaries pass.
Tests: RED exact string-unmarshal and digest-mismatch regressions; GREEN focused and full `go-hub/internal/hub`; pristine tagged v141 artifact provenance verifier; direct and full-route BrowserOS live canaries.
Review: Reviewer APPROVE; only low non-blocking missing sidecar-corruption matrix noted.
Commit: `2bbf6579b3cd9a44ad3b64c468c3a5233e61f507` on `codex/v141-manifest-hotfix`; not pushed/tagged.
Unresolved: canary exposed a separate pre-existing tarball-as-executable updater contract for older builds. server-100 was restored to verified build 141 and is stable; broader platform-specific raw-binary update design remains outside selected YAGNI scope and is non-blocking per completion Overseer.
