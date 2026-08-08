# Задача: обновить публичные docs по Custom GPT и virtual MCP

Status: complete

## Оригинальный запрос

Work in shared `/home/roomhacker/gptadmin`. Do not revert or touch unrelated work. Own ONLY `docs/SECURITY_DOCS.md`, `docs/HUB.md`, `docs/WEBHOOKS.md`, `docs/ARCHITECTURE.md`, `docs/INTEGRATIONS.md`, `docs/DOCUMENTATION_MAP.md`. Update stale public docs for simple, factual Custom GPT and virtual MCP documentation: generated Custom GPT schema is `/actions/openapi.yaml` (or per-server); actions use `/mcp-relay/*`; `CTL_TOKEN` is legacy migration only, not new GPT setup; OAuth is a way to obtain a Bearer access token; network-proxy and webhooks are separate default-off virtual MCPs with dedicated schemas only after enable. Make pages concise and preserve valid existing non-Custom-GPT content. Run focused docs/product-auth tests. Report paths + test results.

## Objective

Сделать указанные public docs короче и фактически точнее для Custom GPT / virtual MCP:

- canonical schema: `/actions/openapi.yaml` и per-server вариант
- relay actions: `/mcp-relay/*`
- `CTL_TOKEN` only for legacy migration
- OAuth = путь к Bearer access token
- `network-proxy` и `webhooks` = separate default-off virtual MCPs

## Business canary

После правок читатель быстро видит:

- где брать schema для Custom GPT;
- что `/mcp-relay/*` — это action/relay surface;
- что `CTL_TOKEN` не рекомендуется для new setup;
- что OAuth дает Bearer token;
- что `network-proxy` и `webhooks` включаются отдельно и имеют dedicated schema only after enable.

## Подтверждённый scope

- Только `docs/SECURITY_DOCS.md`
- Только `docs/HUB.md`
- Только `docs/WEBHOOKS.md`
- Только `docs/ARCHITECTURE.md`
- Только `docs/INTEGRATIONS.md`
- Только `docs/DOCUMENTATION_MAP.md`

## Явные исключения

- Не трогать другие файлы.
- Не менять runtime/code.
- Не расширять документацию на другие продуктовые контуры.
- Не делать широкую перепись, если хватает точечных factual правок.

## Оценка времени

- optimistic: 15 active minutes
- likely: 30 active minutes
- pessimistic: 45 active minutes

## План

1. Проверить текущие формулировки в шести docs-файлах и найти места, где нужно сократить или уточнить wording.
2. Внести минимальные правки, сохраняя non-Custom-GPT sections intact.
3. Прогнать focused docs/product-auth tests и собрать paths + exact results.

## Progress

- 2026-08-04: task created; checking current docs text and contract-aligned phrasing before editing.
- 2026-08-04: verified the current docs edits stay within the six owned files and keep non-Custom-GPT content intact.
- 2026-08-04: verification passed: `python3 -m pytest tests/test_docs_product_contract.py tests/test_hub_contract.py::test_hub_contract_relay_and_openapi -q`.
