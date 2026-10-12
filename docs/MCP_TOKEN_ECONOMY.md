# GPTAdmin: компактный обмен с агентами

Обычный MCP-каталог содержит `ui`, `discover`, `schema`, `execute`, `job`,
`expand`. Остальные возможности доступны через `schema(target="hub")` и
`execute`; полный каталог можно получить через `tools/list` с
`params.detail="full"`. Явно ограниченные профили сохраняют свой список.
После обновления новые MCP-подключения получают этот каталог автоматически;
старому клиенту, который кеширует схемы, нужно обновить подключение.

1. `discover()` возвращает цели и статус. `query` и `status` фильтруют список.
   Mesh, транспорт и полные метаданные доступны через `expand(operation_id)`
   или `discover(detail="full")`.
2. `schema(target)` возвращает названия, краткие описания, обязательные поля
   и annotations. Это каталог, а не полный контракт аргументов.
3. `schema(target, tool)` возвращает **неизменённую точную схему** выбранного
   инструмента и `call_ref`. Вызов `execute(ref=call_ref, args=...)` сокращает
   исходящие аргументы; старый `execute(target, tool, args)` остаётся доступен.
   Ссылка разрешается до авторизации, approval, маршрутизации и idempotency.
4. Крупные обычные ответы получают явные `preview`, `truncated`, `bytes`,
   `operation_id`. Ошибки, approvals, медиа и resource blocks не обрезаются.
   Маленький shell-ответ остаётся коротким; его durable `job_id` раскрывается
   через `job(id, detail="full")`. Размер preview — 1000 UTF-8 байт.
5. `expand(id, path, offset, limit)` читает сохранённый результат **без нового
   выполнения**. `path` — JSON Pointer, например `/result/settings/0`.
   Маленькая выборка возвращается в `data`, крупная — страницами в `text`;
   При раскрытии целой точной схемы инструмента также возвращается `call_ref`;
   повторно запрашивать tools/list не требуется. Завершённый async schema job
   возвращает каталог и operation_id тем же способом.
   `next_offset` указывает следующий байтовый offset. `limit` по умолчанию
   4000, максимум 16000 UTF-8 байт. При ошибке ссылки неизвестную запись не
   следует повторять; для повторного получения схемы достаточно read-only schema.

HTTP/Actions используют те же компактные схемы и результаты:
`POST /mcp-relay/tools`, `POST /mcp-relay/call`,
`POST /mcp-relay/expand`. Вызов по `ref` также поддерживается HTTP call.

Хранилище результатов локально процессу: 30 минут, 128 записей, 32 MiB всего,
4 MiB на запись. Оно привязано к исходной credential/profile/session identity;
чужая ссылка не открывает данные. Перезапуск, TTL или eviction делают ссылку
недоступной. Если результат нельзя сохранить, возвращаются полные данные.
Существующие durable jobs и idempotency остаются отдельным механизмом.

Экономия детерминирована, без LLM-суммаризации. Обёртка собственных Hub-ответов
схлопывается только если единственный text block является тем же JSON, что
structuredContent; содержательные тексты и метаданные сохраняются. Собственная
обёртка child mcp_call также удаляется при точном совпадении маршрута;
исходный результат и полный durable receipt остаются доступны. Внешний MCP envelope
с text и structuredContent сохраняется для совместимости с потребителями.

Измерения нужно отличать от тарификации модели. Проверка сравнивает реальные
JSON запросы и ответы с `o200k_base` и `cl100k_base`; это воспроизводимые
прокси, не точные токенизаторы MiniMax/GLM. Аргументы исполняемой операции
сами по себе не сжимаются с потерей смысла. Большая экономия входа модели
получается от каталога, схем по запросу и bounded output; экономия выхода —
от `ref` вместо повторения target/tool и выборочного чтения деталей.

Быстрые unit-регрессии: `TestToken*`; проверки HTTP/MCP, idempotency,
схем и ownership: соответствующие `TestCompactToolOutput*`, `TestAppsSDK*`,
`TestConnection*`, `TestChildScope*`, `TestMCPRelayCallIdempotency*`.
Обычный release-run имеет общий deadline 180 секунд, включая vet/build;
focused tests ожидаются до 20 секунд. Полная Hub suite (542 проверки),
vet и build прошли за 145 секунд. Старый общий hub/node run получил timeout;
это не GREEN, slow Node coverage остаётся в nightly.
Live acceptance проверяет настоящий процесс, реальные schema/execute/expand,
полноту результата и отсутствие повторного side effect. Результаты замеров
и точная runtime identity сохраняются в `.tmp/token-candidate/`.

FastAgent и ZCode поддерживаются штатным установщиком:
`gptadmin connect-mcp --client fast-agent --client zcode`.
Он меняет только запись `gptadmin`, сохраняет модели и соседние MCP,
делает защищённую резервную копию и атомарно записывает конфигурацию.
FastAgent использует HTTP в `.fast-agent/fast-agent.yaml`; отдельный профиль
MiniMax и его AgentCard в `.config/agent-herder/` обновляются, если существуют.
ZCode использует исходный формат `.zcode/cli/config.json`.

В старом stdio launcher поле `user` само по себе не переключает UID процесса.
FastAgent отклоняет хранилище durable processes при несовпадении владельца;
это приводит к падению до handshake и внешнему `EOF`. Для Linux-определения
FastAgent запуск явно закрепляется за его исходным владельцем через
`runuser -u admin -- /home/admin/.local/bin/fast-agent-mcp ...`.
Реальный handshake/tools/list через GPTAdmin проверяет исправление.
Проверка моделей использует исходные маршруты FastAgent и ZCode, а usage
провайдера отделяет вход, выход и кеш; прокси-токенизатор не заменяет эту проверку.
