# BrowserOS health vs BrowserClaw MCP log audit

Status: work

## Оригинальный запрос

Понять, почему BrowserOS считался здоровым, посмотреть логи и проверить open-source проект.

## Цель

Разделить состояние старого BrowserOS.app и фактического BrowserClaw MCP server на Mac mini, опираясь на process/listener, crash logs, MCP handshake и upstream documentation/source.

## Business canary

Фактический MCP endpoint выполняет `initialize` и `tools/list`; crash state старого server подтверждён логами.

## Scope / exclusions

- Read-only Mac logs, process/listener checks, MCP handshake, upstream docs/source.
- Не перезапускать BrowserOS, не менять URL/config и не создавать GitHub fork без отдельной необходимости.

## Initial estimate

- optimistic: 10 min
- likely: 25 min
- pessimistic: 45 min

## План (русский)

1. Собрать BrowserOS/BrowserClaw runtime и crash evidence.
2. Проверить настоящий MCP handshake.
3. Сопоставить с upstream open-source docs/repository.
4. Выдать точный ответ healthy/not healthy и URL.

## Evidence — 2026-08-07

- Installed `/Applications/BrowserOS.app` is version `0.47.18`; its bundled `browseros_server` crash reports repeatedly show `EXC_BAD_INSTRUCTION` on 2026-08-05.
- The active MCP process is `/Applications/BrowserClaw.app/.../browseros-claw-server` with config version `0.48.1.0`, listening on `127.0.0.1:9210`.
- Correct local MCP URL: `http://127.0.0.1:9210/mcp`; GET returns 406, while MCP POST initialize returns HTTP 200 and a session id; `tools/list` returns HTTP 200 with BrowserClaw tools.
- Config has `allow_remote_in_mcp=false`; this endpoint is local-only until a deliberate tunnel/relay is configured.
- Upstream repository identified: `https://github.com/browseros-ai/BrowserOS`; upstream docs describe Streamable HTTP MCP and the BrowserOS neo manual endpoint pattern. No fork or source mutation was needed for this diagnosis.
- Conclusion: BrowserClaw MCP is healthy; old BrowserOS.app server is not. GPTAdmin was pointed at stale `127.0.0.1:19000/mcp`, so it was not connected to the healthy `:9210` server.
