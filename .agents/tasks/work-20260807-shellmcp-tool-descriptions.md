# Clarify ShellMCP MCP tool descriptions

## Original request

Сделать descriptions существующих MCP-инструментов ShellMCP понятными; без новых операций и архитектурных изменений.

## Objective

Ясно описать, что управление MCP выполняется на выбранном ShellMCP-host, а `mcp_manage`, `mcp_tools` и `mcp_call` делают.

## Canary

Схема Hub и прямая схема Go ShellMCP содержат одинаковые понятные descriptions для трёх MCP-инструментов.

## Exclusions

Никаких catalog/activate/deactivate/plan/apply и изменений runtime-поведения.

## Estimate

Initial optimistic / likely / pessimistic: 5 / 10 / 20 минут.

## Applied

- Updated only descriptions in Go ShellMCP and Hub's ShellMCP façade.
- No new tools, actions, schema changes, activation state, or runtime behavior.
