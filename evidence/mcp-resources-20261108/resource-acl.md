# M18L1: доступ к MCP-ресурсам

Дата: 8 ноября 2026 года. Bug: M18-RESOURCE-ACL.

## Подтверждённый дефект

При чтении `gptadmin://servers` и `gptadmin://agents` сервер возвращал
весь инвентарь без проверки AllowedTargets и владельца личного MCP.
Прямой endpoint сервера также позволял resources/list и resources/read
после отзыва доступа к этому target.

Исходная ревизия:
`7d43d3bafe1409dcb5d8797a229b71bc80b106a7`.

## Изменение

- Инвентарь ресурсов строится из текущего registry с фильтрацией по target
  и точному владельцу управляемого подключения.
- Прямые list/read ресурса требуют разрешения на конкретный target.
- Hub остаётся фасадом: профиль с разрешением на один target может получить
  его отфильтрованный инвентарь через /mcp и /server/hub/mcp.
- Чтение инвентаря не выполняет discovery дочерних схем и не создаёт jobs.
- Existing task-output и startup-instructions handlers сохранены.

Новые helpers: `resourceAgentsForRequest`, `resourceServersForRequest`
в `go-hub/internal/hub/mcp_resource_acl.go`.

## Проверки

Одна и та же команда до и после исправления, cwd `go-hub`:

```sh
go test ./internal/hub -run '^(TestResourceInventory|TestAgentResourcesRejectDeniedTarget)' -count=1 -timeout=120s
```

RED: exit 1, package 0.561 s; GREEN: exit 0, package 0.422 s.
Время процесса с подготовкой: 9.088 s для каждого финального запуска.

Покрытие: два caller/profile с разными targets; обе inventory URI;
личный MCP с точным connection scope, без connection, с чужим principal
и profile; запрещённые list/read без dispatch; сохранённое изменение профиля,
повторные аутентифицированные JSON-RPC reads и прямой endpoint после отзыва.
Последний сценарий проходит через настоящий HTTP router, JWT verification и
чтение сохранённого access profile внутри тестового процесса.

Дополнительная связанная проверка:

```sh
go test ./internal/hub -run '^(TestResourceInventory|TestAgentResourcesRejectDeniedTarget|TestConnectionOwner|TestStartupInstructionsMCPDelivery|TestResourceReceipt|TestAdminMCPResourceReadHonorsTargetPolicy|TestMCPTaskAndResourceReadsRequireBoundCaller)' -count=1 -timeout=120s
```

Exit 0, package 0.578 s; время процесса 1.969 s.

Полные результаты: `acl-red.txt`, `acl-green.txt`, `acl-related.txt`.
Команды, exit codes и хеши файлов: `acl-checks.json`.

## Граница результата

Это source и in-process HTTP/policy evidence. Исправление не установлено
этим worker на общий runtime. Реальный resource picker, новая функция
схем MCP и интеграционная проверка установленного кандидата относятся к
следующим листьям lead. VERSION, roadmap и общий plan не изменялись.
