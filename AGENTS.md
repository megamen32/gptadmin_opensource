# AGENTS instructions

## Обзор

GPT‑Админ — self-hosted MCP hub. Три основные компоненты:

1. **go-hub/** — Go hub/proxy: хранит метаданные, auth state, маршрутизирует MCP-вызовы. Версия инжектится через ldflags (`BuildVersion`, `GitCommit`).
2. **go-shellmcp/** — Go shell execution agent (порт старого Python `services/shellmcp.py`, удалён в PR #22). Полный parity: audit, nonce, fsmeta, update, supervisor, ws, ssh.
3. **cli.py** — однофайльный (~3900 строк) Python-установщик + CLI (`gptadmin setup/update/auto-update/mcp/...`). Платформо-зависимый: systemd на Linux, launchd на macOS.

Дополнительно:
- `public/admin/` — vanilla-JS SPA админки (без фреймворка). `app.js` `renderAll()` читает `/admin/api/overview`.
- `admin-ui/` — source новой React+TypeScript+Vite админки. Node используется только на build-time; runtime получает compiled static только после explicit parity gate. До этого `public/admin/` остаётся production.
- `public/openapi.yaml` — описание API hub.
- `tools/build.sh` — сборка/релиз: бампит VERSION, инжектит версию в Go через ldflags, пакует tarballs.
- `deploy/` — install-скрипты (Linux/macOS/Windows), systemd/launchd юниты, nginx setup.

## План и межагентная работа

- Канонический execution plan: [`docs/PROJECT_PLAN.md`](docs/PROJECT_PLAN.md).
  Публичный roadmap в `docs/ROADMAP.md` не заменяет его.
- Каноническая продуктовая философия: [`docs/PHILOSOPHY.md`](docs/PHILOSOPHY.md).
  Новые MCP surfaces должны иметь минимальный стабильный контекст и лениво
  загружать только реально выбранные данные.
- Границы admin profiles и внешних workspaces определены в
  [`docs/ADMIN_PROFILES.md`](docs/ADMIN_PROFILES.md). Не записывайте
  instance-specific machine IDs и пути в публичный репозиторий.
- Канонический append-only handoff log: [`docs/WORKLOG.md`](docs/WORKLOG.md).
- Перед самостоятельной реализацией оркестратор явно проверяет: можно ли
  отдать ограниченный срез субагенту по чёткой инструкции. Делегируйте
  независимую диагностику, тесты или изолированные изменения; оставляйте у
  основного агента интеграцию, рискованные решения, deploy и acceptance.
- Оркестратор обязан давать субагенту самодостаточный подробный task brief, а
  не короткий prompt. Brief включает: цель milestone и пользовательскую
  причину; подтверждённое текущее состояние и pre-fix evidence; точный
  контракт и примеры; нужные файлы, symbols и тесты для изучения; разрешённую
  write scope и области других агентов; non-goals, privacy/compatibility
  constraints и известные dirty files; TDD/verification commands и
  проверяемый результат. В конце задайте формат handoff: assumptions,
  изменённые файлы, точные команды и результаты тестов, риски и следующий
  action. Полный контекст в prompt дешевле и надёжнее, чем прерывание агента
  ради уточнений.
- Не прерывайте работающего субагента только из-за отсутствия ответа через
  минуту, отсутствия git-изменений или желания сузить его задачу. Для
  изолированного implementation/TDD slice дайте минимум 10 минут
  непрерывной работы; для repo/runtime reconnaissance и cross-cutting slice —
  минимум 15–20 минут. Не дублируйте его тесты, поиск и правки параллельными
  командами оркестратора.
- Ожидайте работающего субагента настоящим blocking wait столько, сколько
  требует исходная задача. Не запрашивайте heartbeat и не устраивайте
  повторные polling только из-за тишины; silence не является blocker.
  Не меняйте задачу посреди цикла без необходимости.
- Interrupt/re-scope допустим только если пользователь явно отменил или
  перенаправил работу, найден конфликт ownership/риск данных, агент сообщил
  blocker, либо есть доказанный hard failure (повторяемая ошибка test/build)
  и ему нужен новый контракт. Если slice оказался шире ожиданий, сначала
  получите normal handoff или non-interrupting boundary, затем создайте
  следующий slice; не обрывайте полезный TDD-цикл.
- Перед существенной работой прочитайте оба файла, выберите один milestone и
  создайте `active` entry по шаблону из worklog. Перед завершением замените его
  на factual `completed`, `blocked` или `handed-off` entry с тестами, commit,
  CI/deploy evidence и единственным next action.
- Для поведенческих изменений применяйте TDD: сначала зафиксируйте failing
  regression test или точное pre-fix evidence, затем реализацию и focused/full
  verification. Не отмечайте milestone/stage завершённым без его exit gate.
- Не записывайте в worklog токены, приватные URL, customer data или raw logs.
- **Проактивная работа с багами:** при любом найденном баге или неожиданном
  поведении немедленно добавьте запись в [`docs/BUGS.md`](docs/BUGS.md) с
  immutable evidence path/ID, подтверждённым фактом, гипотезой root cause,
  статусом и следующим действием. Не записывайте секреты или raw logs.
  Сразу после завершения текущей цели разберите все actionable открытые записи
  и исправьте их до handoff; не откладывайте известный исправимый баг без
  явного внешнего blocker. Закрывайте запись только после focused
  verification, а для behavior changes сначала фиксируйте pre-fix evidence или
  failing regression test.
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
- Admin UI: `admin-ui/` — source новой React+TypeScript+Vite админки. Node используется только на build-time; runtime получает compiled static только после explicit parity gate. До этого `public/admin/` остаётся production.
- Перед коммитом запускать тесты (см. блок Команды выше).
