# Задача: синхронизировать security/hub/webhooks/architecture/integrations/docs map с Custom GPT Actions/OAuth/virtual MCP контрактом

Status: complete

## Оригинальный запрос

Новая Worker-задача с непересекающимся scope: `docs/SECURITY_DOCS.md`, `docs/HUB.md`, `docs/WEBHOOKS.md`, `docs/ARCHITECTURE.md`, `docs/INTEGRATIONS.md`, `docs/DOCUMENTATION_MAP.md` only. Нужно упростить и исправить stale-формулировки относительно Custom GPT Actions/OAuth/virtual MCP contract. Canonical Custom GPT schema — `/actions/openapi.yaml` (или per-server), вызовы идут через `/mcp-relay/*`; `CTL_TOKEN` — legacy migration only и не должен рекомендоваться для нового Custom GPT setup; OAuth возвращает/использует Bearer; `network-proxy` и `webhooks` — отдельные default-off virtual MCPs с dedicated schemas после enablement. Не трогать другие файлы и не откатывать другие воркеры. Добавить/подправить только необходимые doc links, прогнать focused docs contract/product-auth tests, отчитаться exact files/tests.

## Objective

Привести указанные docs-файлы в соответствие с актуальным контрактом:

- canonical Custom GPT schema: `/actions/openapi.yaml` и per-server `/server/{slug}/actions/openapi.yaml`
- relay calls: `/mcp-relay/*`
- `CTL_TOKEN` only legacy migration, not a new setup recommendation
- OAuth setup/issues/tokens are Bearer-based
- `network-proxy` and `webhooks` are separate default-off virtual MCPs

## Business canary

Проверить, что docs:

- не советуют `CTL_TOKEN` для нового Custom GPT setup;
- называют `/actions/openapi.yaml` canonical schema;
- явно разделяют `/mcp-relay/*` и `/mcp`;
- фиксируют default-off virtual MCPs и dedicated schemas after enablement;
- не содержат stale wording about webhook/proxy/Actions auth.

## Подтверждённый scope

- Только `docs/SECURITY_DOCS.md`
- Только `docs/HUB.md`
- Только `docs/WEBHOOKS.md`
- Только `docs/ARCHITECTURE.md`
- Только `docs/INTEGRATIONS.md`
- Только `docs/DOCUMENTATION_MAP.md`

## Явные исключения

- Не трогать любые другие файлы.
- Не менять runtime/code/tests кроме запуска focused checks.
- Не расширять предмет на новый продуктовый контур.
- Не переписывать большие секции, если достаточно точечных правок.

## Оценка времени

- optimistic: 15 минут
- likely: 30 минут
- pessimistic: 45 минут

## План

1. Проверить текущие формулировки в шести docs-файлах и найти подтверждающие места в исходниках.
2. Внести минимальные правки и только необходимые doc links.
3. Запустить focused docs contract/product-auth tests.
4. Сообщить изменённые пути и exact tests.
