# Custom GPT Actions and internal MCP HTTP Remote canary

Status: work

## Оригинальный запрос

Проверить не просто внешний curl через VPN2, а Custom GPT Actions endpoint; отдельно вызвать внутренний MCP; проверить на сайте выбор MCP и получение инструкции подключения как HTTP Remote с URL, Bearer и, если применимо, командой `npx -y`.

## Цель

Доказать полный бизнес-путь public Actions/OpenAPI и MCP catalog/UI → HTTP Remote connection details.

## Business canary

- Через VPN2 публичный Action/OpenAPI endpoint возвращает валидную схему.
- Через GPTADMIN внутренний MCP выполняется read-only вызов.
- UI/API для выбранного MCP возвращает HTTP URL и Bearer-инструкцию без утечки секрета.

## Scope

- Read-only public GET/OPTIONS/canary calls.
- Read-only GPTADMIN MCP discovery/schema/status or safe tool call.
- Read-only website/API contract inspection.

## Exclusions

- Не включать/перерегистрировать MCP.
- Не выдавать и не печатать реальные Bearer tokens.
- Не менять Custom GPT schema, OAuth, ACL или production routing.

## Initial estimate

- optimistic: 20 min
- likely: 40 min
- pessimistic: 75 min

## Plan (русский)

1. Найти canonical public Action/OpenAPI URL и MCP catalog endpoints.
2. Проверить их через VPN2 с redacted output.
3. Вызвать один внутренний read-only MCP через GPTADMIN.
4. Сверить UI/API выдачу HTTP Remote URL/Bearer contract.

## Evidence — 2026-08-06

- VPN2 external canaries: `https://example.com` 200 and `https://httpbin.org/status/204` 204; these proved egress only, not Custom GPT Actions.
- Canonical host check: `became.bezrabotnyi.com` redirects to `became.bezrabotnyi.com`; after redirect `/actions/openapi.yaml`, `/connect.json`, `/mcp`, `/mcp-relay/servers`, `/server/hub/mcp`, and `/server/hub/actions/openapi.yaml` return 404.
- Internal MCP: `memos-shared` appeared in GPTADMIN catalog after permission repair; `mcp_tools` returned `memory_health`, `memory_search`, `memory_add`; read-only `memory_health` returned `status=healthy`, service `memos`, version `1.0.1`.
- UI/source contract: public `mcp-server` page emits generic `https://your-hub.example.com/mcp` and `https://your-hub.example/server/openmemory/mcp`; it does not select a live MCP or produce a Bearer header. Admin UI shows MCP/actions paths and has separate token issuance, but does not render a selected-MCP HTTP Remote config.
- Result: network egress and internal MCP are proven; Custom GPT Actions and selected-MCP HTTP Remote business canaries are NOT CONFIRMED due live 404 and missing UI contract.
