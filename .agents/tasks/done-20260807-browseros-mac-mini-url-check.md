# BrowserOS Mac mini URL check

Status: work

## Оригинальный запрос

Определить URL MCP BrowserOS на Mac mini и проверить, был ли он подключён.

## Цель / canary

Получить URL из Mac mini/runtime config и подтвердить его read-only HTTP/MCP health или зафиксировать exact blocker.

## Scope / exclusions

- Только config, listener и read-only HTTP/tools-list checks.
- Не запускать BrowserOS, не создавать туннели и не менять MCP config.

## Initial estimate

- optimistic: 5 min
- likely: 10 min
- pessimistic: 20 min

## План (русский)

1. Проверить GPTADMIN target/schema.
2. Сверить Mac mini BrowserOS config и listeners.
3. Проверить URL и сообщить connected/not connected.

## Evidence — 2026-08-07

- GPTADMIN schema target `mac-mini-4-nanokvm-picoclaw` failed: stdio server exited `-15`; no connected tools list.
- Mac mini BrowserOS process listens on TCP `*:9000`; TCP `*:9001` belongs to local gptadmin hub.
- BrowserOS HTTP probes on Mac mini `http://127.0.0.1:9000/{/,json/version,json/list,mcp,healthz,version}` all returned `503 Service Unavailable`.
- GPTADMIN config `/etc/gptadmin/mcp-agents.d/browseros-mac.json` contains stale local relay URL `http://127.0.0.1:19000/mcp`; no listener exists on server-100 or Mac mini port 19000.
- Correct candidate URL for direct Mac mini access is `http://203.0.113.10:9000/mcp` (or `http://mac-mini-2012.lan:9000/mcp`), but it is currently not healthy/connected.
