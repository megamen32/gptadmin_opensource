# Server-100 GPTADMIN blocker audit

Status: work

## Оригинальный запрос

Проверить утверждение другого ИИ: на server-100 `shell_exec` всегда сломан из-за отсутствующего `/usr/bin/sudo`, а `system_inspect` не видит `/home/admin/`; предложены установка sudo или перенастройка агента.

## Цель

Read-only подтвердить или опровергнуть оба блокера на актуальном GPTADMIN target `shell:admin-server-100`.

## Бизнес-canary

Безопасная команда через `shell_exec` и bounded read через `system_inspect` дают точный результат или воспроизводимый blocker.

## Scope / исключения

- Только повторная диагностика GPTADMIN.
- Не устанавливать пакеты, не менять конфигурацию, права, сервисы или файлы.

## Оценка активного времени

- Initial optimistic: 5 min
- Initial likely: 10 min
- Initial pessimistic: 15 min

## План (русский)

1. Выполнить `shell_exec` с безопасной командой.
2. Выполнить `system_inspect` для `/home/admin` и ограниченного дочернего пути.
3. Сопоставить результаты с заявлением и зафиксировать точный scope блокера.

## Evidence — 2026-08-06

- `gptadmin_execute(target=shell:admin-server-100, tool=shell_exec, cmd=id)`: reproduced exactly `fork/exec /usr/bin/sudo: no such file or directory`; command did not start.
- `gptadmin_inspect(list_directory, /home/admin)`: returned `lstat /home/admin: no such file or directory`.
- `gptadmin_inspect(list_directory, / and /home)`: returned `path ... is outside configured read-only inspection roots` for both.
- Conclusion: shell_exec blocker is confirmed. Inspection-root restriction is confirmed for `/` and `/home`, but the existence of `/home/admin` is not proven by the failed lstat response.
- No package/config/service/file mutation performed. Installing sudo or changing roots remains unapproved.
