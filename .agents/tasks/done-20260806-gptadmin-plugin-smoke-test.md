# GPTAdmin plugin smoke test

Status: work

## Оригинальный запрос

[$gptadmin](app://asdk_app_6a58185cb3c88191a208d863f7281ca9) протестируй работу плагина, потому что соседняя задача `codex://threads/019fd08b-d2fc-75d3-bd3a-f821329542f5` не смогла.

## Цель

Проверить, что GPTAdmin connector/plugin способен обнаружить target, получить его schema и выполнить безопасный read-only smoke-test.

## Бизнес-canary

Один реальный вызов через GPTAdmin plugin возвращает валидный результат от выбранного target; при наличии shell target — read-only `pwd` или эквивалентный bounded check без мутаций.

## Подтверждённый scope

- Только discovery → schema → безопасный read-only execute.
- Использовать текущий подключённый GPTAdmin app connector.

## Явные исключения

- Не менять конфигурацию, права, ACL, секреты, сервисы или файлы.
- Не исправлять соседнюю задачу и не делать deployment/restart.

## Оценка активного времени

- Initial optimistic: 5 min
- Initial likely: 10 min
- Initial pessimistic: 20 min

## План (русский)

1. Обнаружить доступные targets.
2. Получить schema выбранного target.
3. Выполнить минимальный read-only canary.
4. Зафиксировать точный результат и blocker, если plugin не проходит.

## Evidence

Append-only; результаты smoke-test добавляются ниже.

### 2026-08-06 — completed

- `gptadmin_discover`: completed; Hub online, `shell:server01` online.
- `gptadmin_schema(target=shell:server01)`: completed; schema `gptadmin.mcp-schema/v1`, digest `66602eee64f6fae5b540775d388b06e7f2b9834fb561404a4f7cbcc4a3c12f47`; exposed `shell_exec`.
- `gptadmin_execute(target=shell:server01, tool=shell_exec, cmd=pwd)`: completed; task/trace `1670113b8f3a89743e39f4818d6f553d` / `6a74dd14000000007ca1b308f33333b1`; returncode `0`; user `admin`; cwd `/home/admin`; stdout `/home/admin`; stderr empty; duration 19 ms.
- Result: business canary passed. No configuration, permissions, secrets, service state, or files were changed.
