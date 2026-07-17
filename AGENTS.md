# AGENTS instructions

## Обзор

GPT‑Админ — self-hosted MCP hub. Три основные компоненты:

1. **go-hub/** — Go hub/proxy: хранит метаданные, auth state, маршрутизирует MCP-вызовы. Версия инжектится через ldflags (`BuildVersion`, `GitCommit`).
2. **go-shellmcp/** — Go shell execution agent (порт старого Python `services/shellmcp.py`, удалён в PR #22). Полный parity: audit, nonce, fsmeta, update, supervisor, ws, ssh.
3. **cli.py** — однофайльный (~3900 строк) Python-установщик + CLI (`gptadmin setup/update/auto-update/mcp/...`). Платформо-зависимый: systemd на Linux, launchd на macOS.

Дополнительно:
- `public/admin/` — vanilla-JS SPA админки (без фреймворка). `app.js` `renderAll()` читает `/admin/api/overview`.
- `public/openapi.yaml` — описание API hub.
- `tools/build.sh` — сборка/релиз: бампит VERSION, инжектит версию в Go через ldflags, пакует tarballs.
- `deploy/` — install-скрипты (Linux/macOS/Windows), systemd/launchd юниты, nginx setup.

## План и межагентная работа

- Канонический execution plan: [`docs/PROJECT_PLAN.md`](docs/PROJECT_PLAN.md).
  Публичный roadmap в `docs/ROADMAP.md` не заменяет его.
- Каноническая продуктовая философия: [`docs/PHILOSOPHY.md`](docs/PHILOSOPHY.md).
  Новые MCP surfaces должны иметь минимальный стабильный контекст и лениво
  загружать только реально выбранные данные.
- Канонический append-only handoff log: [`docs/WORKLOG.md`](docs/WORKLOG.md).
- Перед самостоятельной реализацией оркестратор явно проверяет: можно ли
  отдать ограниченный срез субагенту по чёткой инструкции. Делегируйте
  независимую диагностику, тесты или изолированные изменения; оставляйте у
  основного агента интеграцию, рискованные решения, deploy и acceptance.
- Перед существенной работой прочитайте оба файла, выберите один milestone и
  создайте `active` entry по шаблону из worklog. Перед завершением замените его
  на factual `completed`, `blocked` или `handed-off` entry с тестами, commit,
  CI/deploy evidence и единственным next action.
- Для поведенческих изменений применяйте TDD: сначала зафиксируйте failing
  regression test или точное pre-fix evidence, затем реализацию и focused/full
  verification. Не отмечайте milestone/stage завершённым без его exit gate.
- Не записывайте в worklog токены, приватные URL, customer data или raw logs.
- При конфликтующей активной области другого агента не редактируйте те же
  файлы/рантайм без явной координации. `AGENTS.md` и `CLAUDE.md` должны
  содержать одинаковые правила этой секции.
- Product-surface vocabulary is **Hub**, **MCP clients** and **Tunnel**. Do not
  expose `CTL_TOKEN`, FRP/frpc or internal key names in normal setup, status,
  UI or quickstarts. Read `docs/AUTH_SIMPLIFICATION.md` before auth, installer,
  client-connect or documentation work. `AdminPassword` is the only
  user-owned secret; internal JWT/signing/device credentials must stay hidden.

## Команды (копировать-вставить)

```bash
# Go тесты — из каждой директории модуля
cd go-hub && go test ./...
cd go-shellmcp && go test ./...

# Python тесты (без медленных e2e)
python3 -m pytest tests/ --ignore=tests/e2e

# Кросс-компиляция для macOS (мака в локальном dev нет)
cd go-hub && GOOS=darwin GOARCH=arm64 go build ./... && GOOS=darwin GOARCH=amd64 go build ./...

# Smoke CLI
python3 cli.py version
python3 cli.py auto-update status
```

## Релиз (нeочевидный флоу)

1. Бамп `VERSION` (целое число) + коммит "Release build N" → push `main`.
2. `auto-tag.yml` создаёт тег `v<N>` → диспатчит `release.yml` → GitHub Release.
3. `build-and-sync.yml` прогоняет тесты, собирает, синкает бинари в зеркало `megamen32/gptadmin_opensource` (нужен секрет `OPENSOURCE_PAT`).
4. macOS CI: job `macos-build` гоняет Go-тесты на `macos-latest` (настоящий darwin-runtime).

## Архитектурные готчи

- **Мака в локальном dev нет.** Darwin launchd/systemd-код кросс-компилируется на Linux; реальное поведение launchd проверяется `tests/mac/launchd_verify.py` (skip на Linux, исполняется на Mac).
- `cli.py` намеренно однофайльный — не разбивать на модули.
- Auto-update service-unit **всегда установлен**; timer включается/выключается по preference пользователя. На macOS триггер унифицирован через `launchctl kickstart` (не nohup).
- Read-only MCP clients не получают raw shell. Они используют типизированный
  `system_inspect`; корни чтения ограничены `SHELLMCP_INSPECT_ROOTS`, а
  распознаваемые credentials скрываются до MCP-ответа. См.
  `docs/READONLY_MODE.md`.
- `AGENTS.md` и `CLAUDE.md` несут один контекст (первый — для не-Claude агентов как Codex, второй — для Claude). При изменении архитектуры — держать синхронно.

## Стиль кода

- Go: следовать существующим паттернам `internal/hub` / `internal/server`.
- Python: f-строки, явное логирование, соответствие окружающему коду.
- Admin UI: без билд-степа, без фреймворка — редактировать `app.js`/`index.html`/`style.css` напрямую.
- Перед коммитом запускать тесты (см. блок Команды выше).
