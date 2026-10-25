# Единая точка Notice Place в AI-клиентах

Notice Place — один MCP-сервер с одним контрактом. Раньше он был зарегистрирован
в одном и том же клиенте под несколькими именами (`AskHuman`, `notify`,
`NoticePlace`, `noticeplace`), и агент видел 18–54 одинаковых инструмента вместо
одной точки. Теперь регистрация приводится к одному каноническому имени
`noticeplace`, а остальные имена отключаются на месте (запись остаётся в файле и
обратима).

## Команда

```bash
gptadmin notice-place status                 # только чтение: где и как зарегистрировано
gptadmin notice-place install --dry-run      # план изменений, без записи
gptadmin notice-place install                # применить
gptadmin notice-place install --refresh-plugin   # свести установленные копии плагина к каноническому пакету
```

`--client` (повторяемый) ограничивает список сред: `opencode`, `codex`,
`hermes`, `zcode`, `fast-agent`.

## Каноническая точка и контракт

* Имя: `noticeplace`.
* Контракт один и тот же везде: 18 инструментов Notice Place
  (`noticeplace_ask_human`, `noticeplace_pending_questions`,
  `noticeplace_answer`, `noticeplace_question_result`,
  `noticeplace_incident_progress`, `noticeplace_instructions`,
  `noticeplace_list_mutes`, `noticeplace_set_notifications`, `ask_human`,
  `send_message`, `call`, `attach_pid`, `attach_query`, `run_and_notify`,
  `list_jobs`, `job_status`, `job_tail`, `kill_job`) — один сервер, одно
  состояние, один бэкенд.
* Транспорт зависит только от хоста, а не от клиента:
  * хост с установленным релизом Notice Place (`/opt/noticeplace`) — локальный
    stdio `/opt/noticeplace/bin/ask-human-mcp` с
    `ASK_HUMAN_ENV_FILE=/home/admin/.config/ask-human/env`, без
    встроенных учётных данных (это же контракт опубликованного пакета);
  * хост без релиза — опубликованная точка `https://notify.bezrabotnyi.com/mcp`
    с тем токеном, который у хоста уже был в конфиге (токен переносится внутри
    того же файла, новый не создаётся).

Если каноническую точку создать нельзя (нет релиза и нет токена), алиас
остаётся живым: среда никогда не теряет доступ к Notice Place.

## Безопасность изменения

* Ничего не удаляется. Алиас той же точки отключается (`enabled: false`) и
  остаётся в файле — изменение обратимо одной правкой.
* Перед каждой записью создаётся резервная копия
  `<файл>.bak.noticeplace.<время>.<случайные байты>`; режим и владелец файла
  сохраняются.
* Комментарии в JSONC-конфиге сохраняются: такой файл не переписывается, а
  команда сообщает об отказе.
* Остальные MCP-серверы, провайдеры, разрешения и хуки не затрагиваются —
  меняется только запись Notice Place.
* Команда идемпотентна: повторный запуск ничего не пишет и не создаёт новых
  копий.
* Учётные данные не печатаются в выводе.

## Проверенные среды (реальный tools/list)

Замер сделан реальным MCP-рукопожатием с той же командой/переменными и тем же
URL, которые использует клиент.

| Хост | Среда | Живая точка | Инструментов | Транспорт |
|---|---|---|---|---|
| admin-node-100 | opencode | `noticeplace` | 18 | stdio |
| admin-node-100 | hermes | `noticeplace` | 18 | stdio |
| admin-node-100 | fast-agent | `noticeplace` | 18 | stdio |
| admin-node-100 | codex (плагин) | `noticeplace` | 18 | stdio |
| admin-node-100 | zcode (плагин) | `noticeplace` | 18 | stdio |
| node-44 | opencode / hermes / codex | `noticeplace` | 18 | stdio |
| MacBook-Pro-User.local | opencode / hermes / codex / zcode | `noticeplace` | 18 | streamable-http |

Отключённые алиасы: `AskHuman` (opencode, codex, hermes, zcode на всех трёх
хостах) и `notify` (opencode на admin-node-100).

Проверка новой сессии OpenCode (`opencode run`, отдельная сессия) перечисляет
пространства MCP: `AskSecret, UserIO, agent-capabilities, agent-herder,
agent-resume, agentmemory, gptadmin, grepmesh, noticeplace, opencode,
telegrammain` — точка Notice Place ровно одна.

`hermes mcp list` показывает `AskHuman … disabled` и `noticeplace … enabled`.
`codex mcp list` показывает `AskHuman … disabled`; `codex mcp get`/парсер
конфигурации подтверждают, что конфиг остаётся валидным TOML.

## Путь обновления

`gptadmin notice-place install --refresh-plugin` сводит установленные копии
плагина (`~/.config/opencode/plugins/noticeplace/*`, `~/.codex/plugins/cache/
noticeplace-local/noticeplace/*`, `~/.zcode/cli/plugins/cache/noticeplace-local/
noticeplace/*`) с опубликованным пакетом: после этого все четыре файла
проекции (`mcp.json`, `mcp.codex.json`, `opencode.json`, `fast-agent.yaml`)
побайтово совпадают с каноническим пакетом, и в пакете больше нет встроенного
bearer-токена.

Повторный `install --refresh-plugin` ничего не меняет и не создаёт новых
копий: дубли не возвращаются.

## Что осталось за пределами проверенного

* **mac-mini-2012.lan** — Notice Place там не зарегистрирован ни в одной среде
  (`/Users/admin/.config/opencode/opencode.json`,
  `/Users/admin/.codex/config.toml`, `/Users/admin/.hermes/config.yaml`,
  `/Users/admin/.zcode/cli/config.json`, `/Users/admin/.fast-agent/
  fast-agent.yaml`): алиасов нет, дублировать нечего, а каноническую точку
  создать нельзя — нет локального релиза и нет токена в конфигах. Владелец
  решения по этой среде — владелец хоста; безопасного пути регистрации из
  GPTAdmin нет.
* **Установленный CLI на MacBook-Pro-User.local** — `gptadmin` там собран до
  этой правки, поэтому команда `notice-place` появляется там только после
  обновления CLI. Правка применялась тем же кодом (`cli.py` передан на хост).
* **Проекция fast-agent в каноническом пакете** называет сервер `NoticePlace`
  (с большой буквы), тогда как остальные проекции — `noticeplace`. Если
  издатель применит эту проекцию в `~/.fast-agent/fast-agent.yaml`, рядом с
  канонической точкой появится алиас. Точная правка: в пакете Notice Place
  (`packages/noticeplace/0.1.0/fast-agent.yaml`) заменить `NoticePlace:` на
  `noticeplace:`. Владелец пакета — репозиторий noticeplace.
* **Установщик ask-human в agent-harness-fleet**
  (`plugins/ask-human/scripts/setup_mcp.py`) дописывает в
  `~/.codex/config.toml` блок `[mcp_servers."AskHuman"]`. Он запускается вручную
  и не входит в автоматический путь обновления, но при следующем запуске
  вернёт алиас. Точная правка: называть сервер `noticeplace` либо не запускать
  этот установщик там, где каноническая точка уже есть.
* **Plugin API Codex/ZCode** показывает MCP-серверы плагина отдельно от
  `codex mcp list`; наличие точки подтверждено чтением проекции и реальным
  tools/list, но не отдельной командой `codex`/`zcode`.
