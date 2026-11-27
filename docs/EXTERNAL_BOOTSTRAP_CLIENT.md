# Клиент GPTAdmin для внешней песочницы

`gptadmin.py` работает на Python 3.8+ без `pip`. Основной протокол: MCP
`2026-07-28` поверх JSON-RPC 2.0 и Streamable HTTP. Для старого сервера есть
явный флаг `--legacy` с MCP `2025-11-25`. Клиент не выбирает другой
протокол после ошибки операции записи и не повторяет такую операцию.

## Первый запуск

В консоли GPTAdmin создайте временную ссылку. Подставьте её вместо
`BOOTSTRAP_URL`, скачайте клиент и выполните:

```sh
curl --fail --silent --show-error "BOOTSTRAP_URL/client.py" -o gptadmin.py
python3 gptadmin.py bootstrap "BOOTSTRAP_URL"
cat GPTADMIN_INSTRUCTIONS.md
python3 gptadmin.py discover
```

Bootstrap сверяет SHA-256 клиента с manifest, проверяет срок credential и
совпадение origin адресов bootstrap и MCP. Записывает конфигурацию
`.gptadmin.json` и инструкцию `GPTADMIN_INSTRUCTIONS.md` с правами 0600.
Credential не печатается. Секретную ссылку и конфигурацию нельзя включать
в Git, отчёты или публичные артефакты. Не включайте `curl -v`.

Инструкция содержит текущие правила профиля, Last Human Commit, обнаружение
проекта через реально доступные MCP-инструменты и условную регистрацию в
Agent Herder. Скачивание само не регистрирует агента и не запускает работу.

Для запуска без секретной ссылки в аргументах процесса сохраните ссылку
в защищённый файл с правами 0600 и используйте stdin:

```sh
python3 gptadmin.py bootstrap - < /private/bootstrap-url
```

Этот вариант тоже не печатает ссылку или credential. При автоматической
приёмке читайте ссылку из защищённого grant receipt и передавайте её в stdin,
не подставляйте её в shell-команду или argv.

Все глобальные опции ставятся перед командой:

```sh
python3 gptadmin.py --config /tmp/session/.gptadmin.json --timeout 90 discover
```

## Поиск, схемы и вызовы

```sh
python3 gptadmin.py search "find_paths"
python3 gptadmin.py search shell_exec --target shell:admin-node-100
python3 gptadmin.py schema shell:admin-node-100 shell_exec
python3 gptadmin.py call shell:admin-node-100 shell_exec --arguments @args.json
python3 gptadmin.py exec --file operation.js
python3 gptadmin.py job JOB_ID --owner-target shell:admin-node-100
python3 gptadmin.py expand OPERATION_ID --path /result --offset 0 --limit 32768
python3 gptadmin.py rpc server/discover
```

`exec` принимает JavaScript через `--code`, `--file` или stdin.
Последний результат нужно явно вернуть через `return`. Если Hub требует
instruction context, передайте полученный receipt через `exec --context`
или `schema --context`. Для `shell/call/upload/download` передайте общий
флаг перед командой: `python3 gptadmin.py --context RECEIPT shell ...`.
Явный context у `exec` или `schema` имеет приоритет над общим. Клиент
не создаёт подтверждения прочтения инструкций автоматически.
`call` оборачивает точный вложенный MCP-вызов
в `exec`; аргументы проходят JSON-сериализацию.

Обычный вывод сохраняет полный результат, включая `content`,
`structuredContent`, `isError` и идентификаторы задания.
`--data` выводит только удобное представление `structuredContent`.
Значения `false`, `null`, `[]` и `0` сохраняются.
Ошибка инструмента завершает CLI с кодом 1, `input_required` с кодом 2.
Клиент не подтверждает запросы за пользователя.

## Shell без повторного написания клиента

Передавайте программу и argv непосредственно, а текст через stdin:

```sh
python3 gptadmin.py shell --cwd /home/admin/gptadmin shell:admin-node-100 -- git status --short
python3 gptadmin.py shell --stdin script.py shell:admin-node-100 -- python3 -
```

Опции `shell` нужно ставить до `target`. Всё после target передаётся
как буквальные аргументы. `--stdin -` читает локальный stdin.
Для намеренного shell-скрипта можно явно передать `bash -s`.
Клиент не выполняет подстановку shell и не фильтрует ANSI-последовательности
в машинных результатах.

## Двоичные файлы

```sh
python3 gptadmin.py upload file:admin-node-100 ./local.bin /allowed/root/new-file.bin
python3 gptadmin.py download file:admin-node-100 /allowed/root/file.bin ./download.bin
```

Используется штатный `file_transfer`, с теми же ACL и разрешёнными корнями.
Предел одного файла 16 MiB, блоки до 32768 байт, base64 и SHA-256.
Загрузка на сервер создаёт новый файл и не перезаписывает существующий.
При скачивании существующий локальный файл тоже не перезаписывается.

Upload commit проверяет целый файл на сервере. Download проверяет каждый
блок, общий хеш, offsets и EOF, затем атомарно публикует новый локальный
файл с правами 0600. Повреждённый файл не публикуется.
После потери квитанции upload chunk клиент запрашивает `upload_status`.
Только когда статус подтверждает отсутствие именно этого блока, допускается
один повтор того же идемпотентного блока. Обычные MCP-записи не повторяются.

Срок жизни transfer handle определяется сервером. Нельзя считать
handle сохранным после перезапуска ноды. Длительные обычные операции
проверяйте через возвращённый job/task ID; обрыв HTTP не доказывает,
что действие не произошло.

## Ограничения транспорта и проверки

Credential передаётся только в Authorization header. HTTP redirects
запрещены. HTTPS обязателен, HTTP разрешён только для loopback.
Поддерживаются JSON и SSE: комментарии, progress notifications,
несколько data-строк и проверка response ID. Клиент переносит стандартные
metadata/header fields MCP 2026 и `x-mcp-header` из схем инструментов.
Опциональные возможности sampling/elicitation не объявляются.

`tests/test_external_bootstrap_client.py` проверяет локальную логику,
HTTP framing и обработку ошибок. Эти тесты не доказывают доступность
развёрнутого Hub. Приёмка релиза отдельно использует выданную сервером
ссылку, настоящий MCP endpoint, разрешённый shell и file_transfer.
