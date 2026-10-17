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
исходный результат и полный durable receipt остаются доступны. Обычный MCP-ответ
сохраняет `structuredContent` и `outputSchema`, но удаляет единственный text block,
если он содержит ровно тот же JSON. Кешированная output-схема остаётся действительной,
а модель получает одну копию. `X-GPTAdmin-Result-Mode: dual` явно возвращает оба
представления. UI widgets, ошибки, медиа, annotations и содержательные
несовпадающие тексты сохраняются.

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

Установщик FastAgent/ZCode добавляет `X-GPTAdmin-Result-Mode: text`.
Эти клиенты иначе сериализуют одновременно text и structuredContent.
В этом режиме facade tools/list не требует structuredContent через outer
outputSchema; tools/call убирает structuredContent только когда единственный
обычный text block содержит ровно тот же JSON. Исходные content, metadata,
медиа, ошибки и несовпадающие представления сохраняются. UI widgets и
подключения без этого заголовка сохраняют обычный MCP-контракт.
Выбранные input-схемы и внутренние схемы дочерних инструментов не меняются.

GrepMesh `read_text` и компактный `search` показывают нумерованные строки или
совпадения с источником; JSON в единственном text block сначала декодируется.
Статус источников, `partial`, `truncated`, происхождение и artifact handle
сохраняются. Большой текст сокращается по границе строки/UTF-8, а исходный
receipt доступен через `expand`. Это работает и при чтении завершённого `job`.
`execute(target="hub", tool="expand", args=...)` также не обрезает страницу
повторно. Для клиента с устаревшим импортом доступны
`execute(target="hub", tool="discover", args={query,status})` и
`execute(target="hub", tool="schema", args={target,tool})`.
Прямые query/status/tool/ref/expand доступны в MCP и Actions-контрактах;
hosted приложение должно обновить собственный импорт инструментов.

Реальные сравнительные вызовы сохраняют исходные provider usage отдельно от
прокси-замеров. MiniMax может возвращать cache_creation_input_tokens=null:
это не означает нулевой input, нужно читать raw input_tokens. В ZCode
нормализованный inputTokens включает cacheReadTokens. Сравнивать следует
также полный вход с кешем и все модельные раунды, включая schema/expand.
Рассуждения модели могут увеличить её output даже при меньшем MCP-каталоге;
сокращение JSON-аргументов не гарантирует снижение всего provider output.
