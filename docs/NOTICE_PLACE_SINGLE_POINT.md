# Единая точка Notice Place в AI-клиентах

Notice Place — один MCP-сервер с одним контрактом. Он должен быть **одной
общей точкой** для всех клиентов: единственный работающий сервер
`https://notify.bezrabotnyi.com/mcp` (Streamable HTTP), одно каноническое имя
`noticeplace`, один и тот же список инструментов.

Раньше один и тот же бэкенд был зарегистрирован в клиенте под несколькими
именами (`AskHuman`, `notify`, `NoticePlace`, `noticeplace`), а часть
регистраций поднимала **свой локальный MCP-процесс на клиента** вместо
общего сервера. Теперь:

* каноническая точка — HTTP-эндпойнт общей службы; локальный stdio-процесс
  клиентами больше не используется;
* остальные имена отключаются на месте (`enabled: false`), запись остаётся в
  файле и обратима;
* MCP-поверхность плагина `noticeplace` отключается — плагин остаётся
  носителем SKILL-контракта, а сервер регистрирует только конфиг клиента;
* алиас отключается **только после того**, как каноническая точка доказана
  реальным `initialize` + `tools/list`.

## Команда

```bash
gptadmin notice-place status                      # только чтение
gptadmin notice-place install --dry-run           # план изменений, без записи
gptadmin notice-place install                     # применить
gptadmin notice-place install --refresh-plugin    # свести копии плагина к каноническому пакету

python3 scripts/notice_place_readback.py          # установленный readback: реальный initialize/tools/list
```

`--client` (повторяемый): `opencode`, `codex`, `hermes`, `zcode`, `fast-agent`.

## Каноническая точка

| Параметр | Значение |
|---|---|
| Имя сервера | `noticeplace` |
| Транспорт | Streamable HTTP, `https://notify.bezrabotnyi.com/mcp` |
| Инструментов | 18 (список одинаков для всех клиентов) |
| Учётные данные | уже имеющийся у клиента bearer Notification Center; переносится внутри того же файла конфигурации, не ротируется и не кладётся в новое хранилище |

Локальный stdio-процесс Notice Place на клиенте не используется: ни одна
среда не поднимает собственный MCP-сервер, все работают с одним сервером
службы.

Если у клиента нет своей учётной записи, берётся та, что уже есть в
конфигурации другого клиента этого же хоста (тот же общий bearer). Если её
нет нигде на хосте, каноническая точка не создаётся, алиас остаётся живым и
команда сообщает об этом.

## Безопасность изменения

* Ничего не удаляется: алиас остаётся в файле отключённым.
* Перед каждой записью — резервная копия `<файл>.bak.noticeplace.<время>.<случай>`;
  режим и владелец файла сохраняются.
* JSONC-конфиг с комментариями не переписывается — команда сообщает об отказе.
* Остальные MCP-серверы, провайдеры, разрешения и хуки не затрагиваются.
* Идемпотентность: повторный запуск ничего не пишет и не создаёт новых копий.
* Учётные данные никогда не печатаются: в выводе только имена, флаги, счётчики
  и имена ключей конфигурации.

## Проверенные среды (установленный readback)

Readback (`scripts/notice_place_readback.py`) берёт регистрацию ровно так, как
её использует клиент (тот же URL и те же заголовки) и выполняет настоящее
MCP-рукопожатие: `initialize` + `tools/list`. Отправляются только эти два
метода — ничего не уведомляется, не спрашивается и не меняется.

Замер: 2026-10-25T22:34Z (UTC).

| Хост | Клиент | Живая точка | Инструментов | Транспорт | Конфиг |
|---|---|---|---|---|---|
| admin-node-100 | opencode | `noticeplace` | 18 | streamable-http | `~/.config/opencode/opencode.json` |
| admin-node-100 | hermes | `noticeplace` | 18 | streamable-http | `~/.hermes/config.yaml` |
| admin-node-100 | zcode | `noticeplace` | 18 | streamable-http | `~/.zcode/cli/config.json` |
| admin-node-100 | fast-agent | `noticeplace` | 18 | streamable-http | `~/.fast-agent/fast-agent.yaml` |
| admin-node-100 | codex | `noticeplace` | 18 | streamable-http | `~/.codex/config.toml` |
| node-44 | opencode | `noticeplace` | 18 | streamable-http | `~/.config/opencode/opencode.json` |
| node-44 | hermes | `noticeplace` | 18 | streamable-http | `~/.hermes/config.yaml` |
| node-44 | codex | `noticeplace` | 18 | streamable-http | `~/.codex/config.toml` |
| MacBook-Pro-User.local | opencode | `noticeplace` | 18 | streamable-http | `~/.config/opencode/opencode.json` |
| MacBook-Pro-User.local | hermes | `noticeplace` | 18 | streamable-http | `~/.hermes/config.yaml` |
| MacBook-Pro-User.local | zcode | `noticeplace` | 18 | streamable-http | `~/.zcode/cli/config.json` |
| MacBook-Pro-User.local | fast-agent | `noticeplace` | 18 | streamable-http | `~/.fast-agent/fast-agent.yaml` |
| MacBook-Pro-User.local | codex | `noticeplace` | 18 | streamable-http | `~/.codex/config.toml` |

Отключённые алиасы (остаются в файлах, обратимы): `AskHuman` в opencode,
codex, hermes, zcode на всех трёх хостах; `notify` в opencode на
admin-node-100. Ни одного локального stdio-процесса Notice Place на
хостах не осталось: MCP-поверхность установленного плагина пуста, регистрация
ведётся только конфигом клиента.

Новая сессия OpenCode (`opencode run`, отдельная сессия) перечисляет
пространства MCP: `AskSecret, UserIO, agent-capabilities, agent-herder,
agent-resume, agentmemory, gptadmin, grepmesh, noticeplace, opencode,
telegrammain` — точка Notice Place ровно одна, переподключение не требовалось.

`hermes mcp list` показывает `AskHuman … disabled` и `noticeplace … enabled`.
`codex mcp list` показывает `AskHuman … disabled`; конфиг Codex остаётся
валидным TOML.

## Что осталось за пределами проверенного

* **mac-mini-2012.lan — UNKNOWN.** Notice Place не зарегистрирован ни в одной
  среде (`/Users/admin/.codex/config.toml`,
  `/Users/admin/.hermes/config.yaml`): алиасов нет, дублировать нечего.
  Каноническую точку создать нельзя — на хосте нет локального релиза Notice
  Place и нет bearer ни в одном конфиге клиента.
  Следующий шаг: владелец хоста переносит общий bearer Notification Center в
  конфиг клиента (или в переменную окружения клиента) — после этого
  `gptadmin notice-place install` на этом хосте создаёт каноническую точку.
* **node-44: zcode, fast-agent — не установлены.** `~/.zcode/cli/config.json`
  и `~/.fast-agent/fast-agent.yaml` на хосте отсутствуют, среды не выбраны и
  не затронуты.
* **Пакет плагина noticeplace (репозиторий noticeplace) — UNKNOWN.** Его
  проекции `mcp.json`, `mcp.codex.json`, `opencode.json`, `fast-agent.yaml`
  объявляют локальный stdio-процесс. Установленные копии на хостах приведены к
  HTTP-контракту (MCP-поверхность отключена), но следующая публикация пакета
  вернёт stdio-проекцию.
  Следующий шаг: проекции пакета должны объявлять
  `https://notify.bezrabotnyi.com/mcp` (для Codex/ZCode — с
  `bearer_token_env_var`, без токена в пакете). До этого изменения
  `gptadmin notice-place install --refresh-plugin` после публикации снова
  очистит MCP-поверхность установленной копии.
* **Установщик ask-human в agent-harness-fleet — UNKNOWN.**
  (`plugins/ask-human/scripts/setup_mcp.py`) дописывает в
  `~/.codex/config.toml` блок `[mcp_servers."AskHuman"]`. Он запускается
  вручную и не входит в автоматический путь обновления, но при следующем
  запуске вернёт алиас.
  Следующий шаг: называть сервер `noticeplace` либо не запускать этот
  установщик там, где каноническая точка уже есть.
* **Установленный CLI на MacBook-Pro-User.local** собран до этой правки, так
  что команда `notice-place` появится там только после обновления CLI; правка
  применялась тем же кодом.
