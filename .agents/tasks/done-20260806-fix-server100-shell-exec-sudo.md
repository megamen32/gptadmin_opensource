# Fix server-100 GPTADMIN shell executor

Status: work

## Оригинальный запрос

`так фикси`

## Контекст

Предыдущий read-only аудит подтвердил, что на `shell:admin-server-100` любой `shell_exec` падает до запуска команды: `fork/exec /usr/bin/sudo: no such file or directory`.

## Цель

Восстановить безопасный `shell_exec` на server-100 без установки пакетов и без расширения inspection roots, затем доказать реальным read-only canary.

## Бизнес-canary

`gptadmin_execute(target=shell:admin-server-100, tool=shell_exec, cmd=id)` возвращает `returncode=0`, ожидаемого non-root пользователя и пустой stderr.

## Scope

- Найти источник обязательного sudo в GPTADMIN deployment/configuration.
- Минимально перенастроить executor на существующий разрешённый пользовательский запуск.
- Проверить live endpoint и сохранить rollback receipt.

## Исключения

- Не устанавливать sudo.
- Не расширять filesystem inspection roots.
- Не менять ACL, секреты, БД, MCP-конфигурации или unrelated dirty work.

## Оценка активного времени

- Initial optimistic: 20 min
- Initial likely: 45 min
- Initial pessimistic: 90 min

## План (русский)

1. Локализовать источник sudo и текущий deployment path read-only.
2. Написать и запустить focused red canary.
3. Выбрать минимальный reversible config/code fix.
4. Применить и проверить реальный shell_exec canary.

## Evidence — 2026-08-06

- RED: `gptadmin_execute(shell:admin-server-100, shell_exec, id)` failed with `fork/exec /usr/bin/sudo: no such file or directory`.
- RED TDD: missing `deploy/systemd/shellmcp-server100-user-mode.conf` caused the new deployment contract test to fail.
- GREEN: focused deployment test passed; Go tests `go test ./internal/shell ./internal/server` passed.
- Applied live drop-in `/etc/systemd/system/shellmcp.service.d/100-gptadmin-user-mode.conf`; backup retained under `/var/backups/gptadmin/shellmcp/`; restarted `shellmcp.service`.
- Live service: `active`, `MainPID=3428021`, `User=admin`, `Group=admin`.
- Live GPTADMIN canary: `shell_exec(id -un && pwd)` returned `admin` and `/home/admin`, returncode `0`.
- Live `system_inspect(list_directory, /home/admin)` passed; home is visible while `ProtectHome=read-only` remains enforced.
- Phone proxy `203.0.113.10:3122` accepted TCP but HTTPS CONNECT aborted (`curl 000`, `Proxy CONNECT aborted`). VPN2 external canaries passed: `example.com` HTTP `200`; `httpbin.org/status/204` HTTP `204`.
- TDD gap: existing tests covered Go command selection and generic installed-binary unit shape, but no server-100 systemd user-mode/ProtectHome integration contract or live endpoint canary.
