# GPTAdmin fleet uptime and local MCP check

Status: work

## Оригинальный запрос

`[@GPTADMIN](plugin://dev-6a58185cb3c88191a208d863f7281ca9@created-by-me-remote) uptime на всех запусти. потом какие сервисы упали? потом запусти на каждом какой-то локальный mcp если есть`

## Цель

Через GPTADMIN проверить uptime всех доступных fleet targets, определить упавшие сервисы по read-only данным и подготовить точный список локальных MCP для запуска.

## Бизнес-canary

Для каждого доступного target получен результат `uptime`; для каждого online shell target получен read-only список локальных MCP и их состояния. Изменяющий запуск выполняется только после подтверждения точного списка.

## Scope

- GPTADMIN discovery, uptime, read-only service/MCP inspection.
- Запуск только явно названного локального MCP после отдельного подтверждения на точном target/action.

## Исключения

- Не перезапускать сервисы и не менять конфигурации автоматически.
- Не трогать stale targets без отдельного решения после причины недоступности.

## Оценка активного времени

- Initial optimistic: 10 min
- Initial likely: 20 min
- Initial pessimistic: 40 min

## План (русский)

1. Discover targets.
2. Выполнить uptime на доступных targets.
3. Проверить service/MCP state read-only.
4. Согласовать точечный запуск MCP.

## Read-only evidence — 2026-08-06

- Discovery: online shell targets — `haos`, `admin-server-88`, `BeyondInfinity`, `admin-server-100`, `MacBook-Pro-User.local`, `server01`; remaining discovered targets are stale.
- Uptime: HAOS `up 21 days 6:47`, server-88 `up 5 days 22:32`, MacBook `up 7 days 1:06`, server01 `up 6 days 39 min`.
- Windows `BeyondInfinity`: standard `uptime` is unavailable; PowerShell fallback was not evaluated by the target wrapper, so Windows uptime is not confirmed.
- `admin-server-100` uptime failed before command execution: `/usr/bin/sudo` not found. This is an execution-wrapper blocker, not proof of host downtime.
- MCP supervisors: HAOS, server-88, server01 have no configured agents. `admin-server-100` has 9 configured agents, all `enabled=true`, `running=false`, `pid=0`. MacBook has `chrome-mac`, `enabled=true`, `running=false`, `pid=0`.
- No restart/enable/start mutation has been performed. Proposed minimal start set: `BrowserOS` on `shell:admin-server-100` and `MacBook-Pro-User.local-chrome-mac` on `shell:MacBook-Pro-User.local`; exact action still needs confirmation.

## User follow-up — 2026-08-07

- Original request: записать BrowserClaw с Mac mini поверх существующей записи BrowserOS/«Prozros».
- Confirmed transport: BrowserClaw listens only on Mac mini `127.0.0.1:9210`; server-100 cannot reach it directly. Existing GPTAdmin record expects local `127.0.0.1:19000`, so the selected reversible transport is an SSH local-forward service on server-100.
- Planned canary: TCP/Streamable HTTP MCP initialize через `127.0.0.1:19000`, затем `tools/list`; verify registry entry points to BrowserClaw Mac mini and old endpoint is absent.
- Exclusions: no Mac mini app/config change; no replacement of unrelated `mac-mini-4-nanokvm-picoclaw`.

## Apply evidence — 2026-08-07

- Installed and enabled `deploy/systemd/gptadmin-browserclaw-macmini-tunnel.service` on server-100; tunnel is `active`, listening on `127.0.0.1:19000`, forwarding to Mac mini `127.0.0.1:9210`.
- Replaced the existing `ref=BrowserOS` payload in `/etc/gptadmin/mcp-supervisor.json` with `name=BrowserClaw on Mac mini`, URL `http://127.0.0.1:19000/mcp`, and `mcp-remote` streamable-http args. Backup: `/var/backups/gptadmin/browserclaw/mcp-supervisor.json.20260807-030951` (plus a second retry backup).
- Updated `/etc/gptadmin/mcp-agents.d/browseros-mac.json` display name; preserved its existing `127.0.0.1:19000/mcp` endpoint.
- Canary passed through server-100: MCP `initialize` HTTP 200, `serverInfo.name=browserclaw`, title `BrowserClaw`, version `0.0.14`; Mac mini app/config was not changed.
- Remaining defect: GPTAdmin `mcp_manage list/status` runtime reports empty/unknown ref and `upsert` fails creating `/etc/gptadmin/.mcp-agents-*.tmp` with permission denied, despite the registry file and tunnel being valid. This is a separate ShellMCP supervisor persistence/runtime issue; no unrelated MCP was changed.

## Heartbeat apply — 2026-08-07

- Confirmed Go ShellMCP behavior: polling and heartbeat are independent; Go logs explicitly report `polling mode ... heartbeat=<bool> queue=true`.
- Enabled `SHELLMCP_HEARTBEAT=1`, retained `SHELLMCP_TRANSPORT=polling`, and set `HB_INTERVAL_S=3600` on server-100 and Mac mini; backups retained beside each env/config.
- Restarted only the Go ShellMCP services. Fresh Mac mini log confirms `polling mode name=mac-mini-2012.lan heartbeat=true queue=true`; server-100 env confirms heartbeat=1 and polling.
