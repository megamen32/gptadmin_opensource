# GPTAdmin Codex plugin and marketplace

## Исходный запрос

Создать plugin для gptadmin как MCP server с OAuth-установкой, профилями,
workflow и проксированием дочерних MCP; добавить marketplace и push.

## Цель

Доставить в репозитории GPTAdmin устанавливаемый Codex plugin `gptadmin` версии
1.0.0, который явно сообщает об установке OAuth-защищённого GPTAdmin MCP,
направляет пользователя в браузерный OAuth consent flow с вводом пароля, и
содержит skills для профилей, памяти и установки дочерних MCP через Hub.

## Бизнес-canary

Manifest и marketplace валидны; пакет содержит `.mcp.json` и все skills; в
конфигурации указан реальный `/mcp` OAuth endpoint без секретов; focused test,
build и package inspection проходят. После этого изменения опубликованы в
разрешённой git-ветке.

## Подтверждённый scope

- `plugins/gptadmin/` — plugin manifest, OAuth MCP config, skills и README.
- `.agents/plugins/marketplace.json` — repo marketplace entry.
- focused contract test для manifest/marketplace/secret boundary.

## Явные исключения

- не менять runtime OAuth/MCP реализацию GPTAdmin;
- не добавлять bearer tokens, passwords или private URLs;
- не менять чужие dirty paths (`.agents/shared-session/`, `grepmesh/`);
- не выполнять force-push и не переписывать расходящуюся `main` без отдельного
  решения о способе интеграции.

## Оценка цикла

- minimum active minutes: 10
- maximum active minutes: 30
- started at: 2026-08-17T22:38:24+03:00
- active time source: task-controlled from explicit cycle start; no prior active
  time counted

## План

1. Создать thin plugin vertical и marketplace entry вокруг реального OAuth MCP.
2. Добавить focused contract test и прогнать тесты/build/package inspection.
3. Закоммитить только task-owned paths и выполнить безопасный push либо сообщить
   точную границу из-за divergence `main`.

## Implementation progress (English)

- Created `plugins/gptadmin` with a version `1.0.0` Codex manifest and remote
  HTTP MCP entry for `https://became.bezrabotnyi.com/mcp`.
- Added `gptadmin-connect`, `gptadmin-mcp-install`, and `gptadmin-workflow`
  skills covering browser OAuth/password handling, profile-scoped child MCP
  installation, and explicit memory routing.
- Created `.agents/plugins/marketplace.json` with an AVAILABLE/ON_INSTALL
  `gptadmin` entry in the Developer Tools category.
- Added `tests/test_gptadmin_codex_plugin.py`; focused result: `3 passed`.
- Plugin validator result: `Plugin validation passed`.
- JSON parse checks passed for all three manifests.

## Push boundary

- Initial commit: `4435ff3 feat(plugin): add GPTAdmin OAuth MCP marketplace plugin`.
- The first non-force push was rejected because local `main` was `13 ahead / 86
  behind` `origin/main`.
- After the user's explicit request to merge all features, `origin/main` was
  merged into local `main`. One conflict in `.agents/last-human-commit/self-improve.md`
  was resolved by preserving both feature histories.
- Focused post-merge suite: `27 passed`; plugin validator passed.
- Merge commit `d65c520` was pushed successfully to `origin/main` without force.
- Foreign untracked `.agents/shared-session/`, `grepmesh/`, and concurrent docs/task
  paths were preserved and not staged by this task.
