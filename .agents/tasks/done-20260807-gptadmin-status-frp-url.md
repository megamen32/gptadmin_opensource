# GPTAdmin status and FRP URL audit

Status: work

## Оригинальный запрос

Проверить через bash локальный `gptadmin status`, потому что у пользователя другой FRP URL.

## Цель

Получить фактический GPTAdmin status и canonical FRP/public URL из локального CLI/runtime.

## Business canary

Read-only `gptadmin status` завершается успешно и возвращает актуальный URL/health данные без секретов.

## Scope / exclusions

- Только локальный status/URL inspection.
- Не менять FRP, сервисы, DNS, OAuth или MCP.

## Initial estimate

- optimistic: 3 min
- likely: 7 min
- pessimistic: 15 min

## План (русский)

1. Найти локальный CLI.
2. Выполнить status в JSON/readable mode.
3. Извлечь URL и проверить его read-only.

## Evidence — 2026-08-07

- Local bash `gptadmin status`: user scope has no installed services; `gptadmin --system` cannot read `/etc/gptadmin/gptadmin.env` without root.
- Server-100 `sudo gptadmin --system urls`: canonical FRP URL is `https://your-subdomain.t.became.bezrabotnyi.com`.
- Server-100 `sudo gptadmin --system doctor --json`: Hub, ShellMCP and FRP tunnel active; public hub configured; one error is permissive env-file permissions; legacy bearer warning is expired.
- VPN2 checks against the correct URL: `/healthz` 200, `/version` 200, `/actions/openapi.yaml` 200, `/connect.json` 200, `/mcp` 401 (expected protected endpoint), `/server/hub/mcp` 401, `/server/hub/actions/openapi.yaml` 200.
- `connect.json` advertises streamable HTTP `/mcp` and OAuth2 PKCE for clients; no token was printed or issued.
