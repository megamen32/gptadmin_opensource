# Fix Go ShellMCP Mac mini polling/heartbeat

## Оригинальный запрос

«значит shellmcp умер. его надо рестратить, и починить исходники.»

## Objective

Восстановить рабочий Go ShellMCP на Mac mini: агент должен стабильно polling-забирать команды и отправлять heartbeat; затем исправить подтверждённый дефект в исходниках с regression test.

## Business canary

Через GPTAdmin target `shell:mac-mini-2012.lan` вызвать `mcp_manage list` без зависания в queued, затем `mcp_manage upsert` BrowserClaw и подтвердить свежим Go-логом `polling mode ... heartbeat=true queue=true`.

## Scope

- Mac mini Go ShellMCP, Hub queue/polling/heartbeat path, соответствующие исходники и focused tests.
- Штатный restart Go ShellMCP разрешён прямым запросом пользователя.

## Exclusions

- Python relay и legacy launchd relay.
- SSH-туннели, BrowserClaw application changes, unrelated server agents.

## Оценка активного времени

- Initial optimistic / likely / pessimistic: 25 / 60 / 120 минут.

## План (русский)

1. Перезапустить Go ShellMCP и снять свежий polling/heartbeat evidence.
2. Найти, почему queued Hub jobs не исполняются после heartbeat=true.
3. Написать focused red regression test, исправить Go source, прогнать тесты и бизнес-canary.

## Execution evidence

- Go process on Mac mini restarted; fresh log confirms `heartbeat=true queue=true`.

## Diagnosis and fix evidence — 2026-08-07

- Mac mini had an unintended local Hub process/launchd duplicate on port 9001; local Hub was removed. Only server-100 remains the Hub.
- Mac ShellMCP was switched to central Hub `https://your-subdomain.t.became.bezrabotnyi.com`; public health from Mac returned 200. Queue polling then completed a previously queued `mcp_manage list` job.
- Root cause of BrowserClaw child decode failure: launchd's minimal PATH omitted `/usr/local/bin`; `/usr/local/bin/npx` invokes `env node`, which exited before JSON-RPC. Go supervisor now merges macOS package-manager paths into child PATH.
- Red test initially failed with `undefined: mergeChildEnv`; green focused test: `TestMergeChildEnvAddsMacOSPackagePaths`. Full `go test ./...` passed.
- Deployed cross-compiled Go ShellMCP Mach-O binary to Mac mini with backup `/Users/admin/.local/share/gptadmin/bin/shellmcp.bak.20260807-path-fix` and restarted the launchd Go agent.
- Go `mcp_tools` canary passed before binary replacement and returned BrowserClaw tools; post-deploy verification job is still running in Hub and must not be treated as a new failure until completion.
- GPTAdmin `mcp_manage` calls still remain queued; source diagnosis pending.
