# Direct child MCP catalog and endpoint exposure

## Исходный запрос

Хаб должен под публичным `/server/` выводить все заданные MCP-серверы отдельными прямыми endpoint-ами, включая BrowserClaw; BrowserClaw нельзя оставлять доступным только через `mcp_call` внутри ShellMCP.

## Цель

Добавить в каталог GPTAdmin прямое представление зарегистрированных child MCP с сохранением host-local ownership: `/server/{child-slug}/mcp` маршрутизирует вызовы через родительский ShellMCP, а не запускает новый процесс и не создаёт второй Hub.

## Бизнес-canary

После heartbeat Mac mini каталог публичных серверов содержит BrowserClaw с прямым `mcp_endpoint`; Bearer-authenticated initialize/tools/list и безопасный `tabs` через `/server/browserclaw/mcp` проходят, а прямой endpoint не меняет владельца процесса.

## Подтверждённый scope

- Использовать существующий ShellMCP child catalog `mcpAgentsForCapabilities()`; не создавать новый каталог.
- Go ShellMCP heartbeat: протянуть уже существующий catalog в `Beat.MCPAgents`, чтобы Hub мог его использовать.
- Go Hub public server catalog: включить child MCP aliases.
- Go Hub direct per-child endpoint: tools/list и tools/call через parent ShellMCP.
- Unit/integration/black-box tests и live canary после сборки.

## Явные исключения

- Не менять ShellMCP ownership/topology.
- Не создавать новый Hub, SSH-туннель или второй child process.
- Не менять секреты, ACL/security policy или unrelated dirty worktree.
- Не добавлять dynamic activate/deactivate Control MCP.

## Оценка

Initial estimate: 25 / 45 / 75 active minutes (optimistic / likely / pessimistic).

Stop when: direct BrowserClaw catalog URL и MCP browser canary проходят.
Abandon when: текущий heartbeat не может безопасно передать child identity без новой auth/schema политики.
Forbidden without explicit user request: production deployment/restart, public release, secret rotation, rollback.

## План и gates

1. Красный тест: child registry в heartbeat → public child server card/endpoint → direct tools/list/call.
2. Реализация минимального parent-routed alias.
3. Go tests, Python contract tests, build.
4. Reviewer/Critic/Tester gates перед commit/release.

## Уточнение после проверки инфраструктуры 2026-08-07

Пользователь подтвердил, что ShellMCP уже владеет и передаёт child catalog. Проверка кода показала точный разрыв: `mcpAgentsForCapabilities()` существует и используется `mcp_manage list`/`/capabilities`, но `hub.Beat` и `newBeat()` его не включали; Hub `publicAgentsLocked()` видел только `s.agents`. Поэтому реализация не добавляет новый источник данных: она подключает существующий catalog к heartbeat и разворачивает child aliases в Hub.

Красный тест: `TestDiscoveryPublishesShellMCPChildAsDirectServer` не находил BrowserClaw в `/mcp-relay/servers`.

Зелёные focused tests после исправления:

- `go-hub`: discovery публикует `/server/browserclaw/mcp`;
- `go-hub`: direct child `tools/list` создаёт job именно для parent ShellMCP и возвращает child tool catalog;
- `go-shellmcp`: heartbeat Beat сериализует существующий `mcp_agents` catalog.

Никаких production restart/deploy пока не выполнено.

## Review gates 2026-08-07

- Reviewer initially found disabled child publication; fixed fail-closed by excluding `enabled:false` children and adding a regression assertion.
- Focused direct endpoint tests pass: discovery/card, parent-routed `tools/list`, parent-routed `tools/call`.
- Full `go test ./...` passes in both `go-hub` and `go-shellmcp`; builds pass; `git diff --check` passes.
- Critic verdict: `STOP/ASK_USER` only because live business canary is not run yet. Production rollout/restart requires explicit user authorization under this task.

## Status

Blocked pending explicit authorization to build/deploy the changed Hub and Mac ShellMCP and restart them for the live BrowserClaw endpoint canary.

## Health extension requested 2026-08-07

Пользователь добавил требование: ShellMCP должен между heartbeat-ами лениво проверять child MCP, а следующий heartbeat должен передавать process/protocol health.

### Проверенный текущий контракт

- `mcpAgentsForCapabilities()` уже отдаёт registry, но только static config.
- `mcpclient.Status()` даёт process/session runtime state.
- `mcpclient.ListTools()` выполняет реальный MCP initialize/tools/list handshake.
- Health cache и lazy refresh отсутствовали.

### Реализация

- Добавлен bounded single-flight lazy refresh после каждого heartbeat; refresh не запускается каждую секунду и не накладывается сам на себя.
- `mcp_agents[*].health` теперь содержит `process` и `protocol`, включая `running/stopped/exited/remote`, `ready/failed/unknown`, timestamps, tools_count и redacted generic error.
- Следующий heartbeat передаёт накопленный health; Hub child alias переносит health в direct `/server/{slug}/mcp` catalog/card.
- Disabled children не запускаются health refresh-ом; remote transports обозначаются как `process=remote`.

### Проверки

- Unit: ready child, failed child без raw error, heartbeat serialization.
- HTTP blackbox: `/capabilities` и fake Hub `/heartbeat` получили ready child health после реального child handshake.
- Full Go suites: `go test ./...` в `go-shellmcp` и `go-hub` проходят.

### Status

Code/tests complete; production live canary still requires explicit rollout/restart authorization.
