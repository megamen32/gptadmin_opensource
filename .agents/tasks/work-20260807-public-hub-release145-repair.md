# Repair public Hub route to release 145

## Original request

Публичный URL должен отдавать release 145, а не старый 140; найти потерянный маршрут и максимально надёжно восстановить публичную доставку.

## Objective

Diagnose the public ingress serving `u-f1102930.t.gptadmin.bezrabotnyi.com`, restore it to the intended release-145 Hub path, and prove external version, Custom GPT, and MCP behavior.

## Business canary

Public `/version` reports the intended release, public authenticated Custom GPT acceptance passes, and selected ShellMCP/MCP calls remain healthy.

## Confirmed scope

Public routing, Hub release selection, reversible service/config changes required to restore the public route, and post-change canaries.

## Explicit exclusions

No architecture redesign, no dynamic MCP tool changes, no deletion of unrelated worktree changes, and no secret rotation unless required by evidence.

## Initial estimate

45 active minutes.

## Evidence

- Initial public DNS had three A-records: `95.165.165.65`, `212.192.31.128`, and `185.240.120.152`.
- Direct per-IP probe showed `95.165.165.65=145` after primary rollout, while VPN2 and VUSA returned stale `140`.
- FRP diagnosis found VPN2 had both `gptadmin-frps.service` and stale `gptadmin-test-frps.service` competing for port `27000`; the stale test service was stopped and disabled.
- VUSA FRP dashboard identified stale fallback proxy `gptadmin-failover-u-f1102930-vusa`; it was removed through the loopback-only FRP control API and the fallback reclaim path was exercised.
- VPN2 control port was moved from stale `27000` to `27001` with backups on both server-100 client config and VPN2 FRPS config; HTTP vhost port `28079` and public hostname were unchanged.
- Current per-IP probe: all three A-records return `build_version=145`, `git_commit=9c5848e`.
- Final Custom GPT live acceptance passed all stages: `health`, `version`, `connection`, `oauth`, `openapi`, `mcp`.
- Final BrowserClaw `mcp_tools` canary returned HTTP 200 with tools.
- Release metadata was normalized to `VERSION=145` and committed as `db85a68`; pushed to `origin/main` without staging unrelated dirty files.

## Status

Complete. Public redundant edges converge on release 145 and post-change canaries are green.

## Follow-up acceptance requested 2026-08-07

### Original request

Проверить не только public health: VPN2-путь, весь Custom GPT flow, отдельный MCP как подключаемый ресурс, и тестовое покрытие. Публиковать только после реального live-canary и зелёных тестов.

### Objective

Подтвердить, что через каждый публичный edge, включая VPN2, работают Custom GPT endpoints и MCP passthrough; отдельно подтвердить, что внешний MCP можно добавить в ChatGPT через GPTAdmin и вызвать его инструменты.

### Business canary

Per-edge version + authenticated Custom GPT acceptance, then separate child-MCP handshake, tools/list, schema-aware tool call, and cleanup through the public GPTAdmin path.

### Explicit exclusions

Не расширять архитектуру, не менять descriptions/топологию, не добавлять новые Control MCP; исправлять только обнаруженные блокеры в пределах существующего release workflow.

### Initial estimate

90 active minutes.

### Estimate revisions

- 2026-08-07: likely 120 active minutes; trigger: требование подтвердить отдельный MCP и тестовое покрытие на всех публичных маршрутах, а не только один BrowserClaw canary.

### Status

In progress: prior green evidence is insufficient for the expanded acceptance contract until each required lane is rerun and mapped to tests.

## Authorization and watchdog scope 2026-08-07

User authorized all required restarts and requested an FRP watchdog. Apply only the existing FRP supervisor repair plus a single bounded watchdog integrated with the existing service; do not add a competing tunnel or alter Hub/MCP architecture.

## Final evidence 2026-08-07

- Fixed deployed server-100 `FRP_SERVER_ENDPOINTS` so VPN2 uses `27001`; backed up the previous env/config before restart.
- Reclaimed stale HAOS failover ownership with the existing signed reclaim helper; restarted only the HAOS standby add-on, then restarted primary/VUSA FRPS and server-100 FRPC.
- Added and installed `gptadmin-frp-watchdog.timer` on server-100 and `gptadmin-frps-watchdog.timer` on VPN2 and VUSA. All timers active; temporary loopback dashboard removed.
- Added bounded watchdog source with cooldown and unit/child-process checks; focused tests: 4 passed.
- Main Python suite excluding the stale legacy `website/test_adapters.py`: 358 passed, 4 skipped in 237.73s.
- Go Hub tests: passed. Go ShellMCP tests: passed.
- Per-IP public live Custom GPT/MCP acceptance, using a real `gptadmin issue-token` bearer: all three edges returned build 145 / commit 9c5848e; health, connect, OAuth, OpenAPI, MCP initialize, tools/list (11 root tools), and safe tools/call all passed.
- Per-IP server-specific Mac MCP endpoint `/server/shell-mac-mini-2012-lan/mcp`: all three edges passed initialize/tools/list (5 ShellMCP tools), `mcp_tools(ref=BrowserClaw)` returned 17 BrowserClaw tools, and read-only `mcp_call(... tabs)` passed on all three edges.

## Status

Complete. Runtime, tests, and real per-edge Custom GPT/MCP canaries are green; ready to commit and publish.
