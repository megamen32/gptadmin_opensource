# GPTAdmin CodeMode: baseline M1

Дата фиксации: 7 ноября 2026. Измерение относится к установленному build 1104 до следующей сборки родителя.

## Подтверждённый проход

Обычная сессия Codex вызвала через штатный GPTAdmin MCP exec три операции: `hub.discover` → `hub.schema` для `hub.status` → `hub.status`. Все три вызова завершились. Повторяемый измеритель сообщил 3 вложенных вызова, 1515 ms wrapper time и 6487 байт сериализованных JSON payloads: discovery 6214 B / 212 ms, schema 196 B / 148 ms, status 77 B / 1134 ms. `handover_status` подтвердил runtime build 1104, commit `e791f763564db0bf6b2640949033c6d6cb2d751f` и versioned executable под `/opt/gptadmin-node/versions/`.

6487 B — сумма UTF‑8 длин `JSON.stringify` для результатов трёх вложенных MCP вызовов. Это измеренный размер сериализованных payloads, не HTTP framing и не полный wire traffic. RAM и native token usage не раскрыты; модель текущей Codex-сессии consumer-инструментами не раскрыта. Токены из байтов не оценивались. Время вложенных операций получено через `Date.now()` и округлено до миллисекунд; wrapper сообщил 1515 ms. Отдельная первая попытка с лимитом 3000 bytes создала недоступное после истечения хранилище; успешный измеренный проход вернул все поля в рамках лимита 18000 bytes.

Подробная машинная запись: [codex-current1104.json](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/baseline/codex-current1104.json). Повторяемый измеритель для передачи родителю: [measure.js](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/baseline/measure.js). Скрипт считает сериализованные UTF-8 байты возвращаемых MCP payloads и wall latency каждой вложенной операции; byte counts являются размером сериализованного вывода и не являются токенами.

## OpenCode

Локально подтверждены `opencode v2.2.4`, команда `run` и подключённый GPTAdmin MCP. Herder native inventory показал активный посторонний OpenCode на node-100; его не занимал. Каталог и локальный `opencode models` подтвердили явный route `omniroute/cx/gpt-6-luna`; native session inspection также подтвердил именно эту модель в изолированной сессии `ses_e49108bbfffeIzz2CkxMpUHo0m`. Model quotas не показывают OmniRoute usage, поэтому квота/авторизация этой модели ими не подтверждена.

В той же изолированной OpenCode native session `ses_e49108bbfffeIzz2CkxMpUHo0m` Luna route завершила один harmless GPTAdmin exec вызов `hub.status`: `ok=true`, 12 серверов, 12 relay jobs, 21 shell jobs, 0 awaiting approvals. Итоговый native session inspection подтвердил `idle`, ту же модель `omniroute/cx/gpt-6-luna` и успешный результат. Точная запись tool receipt: `call_1a597cec85ca4607b0bac90195eb8bcc`. Native usage для model turn, вызвавшего exec: input 346, output 60, reasoning 177, cache read 38400; финальный ответный turn: input 268, output 114, reasoning 0, cache read 38656. Это нативные counters по двум отдельным turns; они не складывают cached input в обычный input.

OpenCode baseline здесь покрывает один `hub.status`. Идентичный трёхшаговый маршрут discovery → schema/inspect → exec пока не измерялся. Нельзя сравнивать native costs OpenCode с Codex: для Codex native usage counters отсутствуют, а измеренные consumer-сценарии различаются. Кандидат для последующего ordinary Codex exec после установки 1105: [candidate-journey.js](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/baseline/candidate-journey.js). В этом проходе используется `mcp.inspect` и call reference; он возвращает только результат, UTF‑8 bytes сериализованного результата и latency.

Первая попытка продолжить session содержала неверный sandbox identifier `mcp`; следующая — `call`. Обе завершились `ReferenceError` до provider call, с нулём вложенных вызовов. Уточнение globals показало `tools.gptadmin.exec`, через который выполнен единственный status вызов. Ранее записанный `Step interrupted` был вызван Ctrl-C этого worker в собственном foreground shell, а не владельцем. Запись и полная цепочка ошибок/результата: [opencode-current.json](../.agents/tasks/20261107-gptadmin-fullcycle/evidence/baseline/opencode-current.json).

## Внешняя проверка

Public mirror CI run [42389566521](https://github.com/megamen32/gptadmin_opensource/actions/runs/42389566521) завершён со статусом `success`, commit `3fefef268b10454e21f0b91ce4f59904e00e8004`.

## Повторение

Вызвать тело `evidence/baseline/measure.js` через обычный Codex GPTAdmin MCP exec с лимитом вывода, достаточным для результата. Сохранить raw output и добавить фактические latency/byte значения в новую запись evidence. Для OpenCode повторить то же тело в установленной обычной сессии после того, как её маршрут будет доступен. Токены записывать только при наличии native usage от клиента.

WIP, уже присутствовавший в checkout, оставлен на месте; ничего из него не включалось и не изменялось.
