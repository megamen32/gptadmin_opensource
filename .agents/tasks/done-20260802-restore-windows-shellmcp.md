# Восстановить удалённый Windows v141 smoke runtime

Status: complete
Class: Short

## Original request

`зачем удалил? зря`

## Objective

Исправить ошибочный cleanup ровно в удалённом объёме: вернуть точный публичный ShellMCP v141 на Windows-машину `BEYONDINFINITY`, снова запустить localhost listener и оставить этот процесс и файлы на месте.

## Business canary

После завершения точный публичный v141 ZIP и распакованный runtime остаются на Windows, `shellmcp.exe` продолжает работать после завершения установочной SSH-сессии, а `/version` на `127.0.0.1:45941` возвращает build `141`, ожидаемый commit и `ready`.

## Confirmed scope

- Read-only определить, затронул ли cleanup существующий canonical runtime.
- Вернуть только удалённый L temp ZIP/runtime и localhost process/listener.
- Не выводить и не использовать существующие credentials.
- Оставить восстановленный процесс и файлы на месте после `/version` canary.

## Explicit exclusions

- Не менять firewall, ACL, учётные записи или пароли.
- Не создавать и не использовать credentials.
- Не запускать Notify→agent delivery без отдельного запроса.
- Не создавать или менять service/task/autostart и Hub registration без отдельного запроса.
- Не трогать чужие изменения в shared worktree.

## Initial estimate (immutable)

- Optimistic: 8 active minutes.
- Likely: 18 active minutes.
- Pessimistic: 35 active minutes.

## Initial plan

1. Проверить, что прежний cleanup затронул только созданные L temp paths/process.
2. Вернуть тот же точный публичный v141 ZIP и распакованный runtime.
3. Запустить его на прежнем localhost listener без service/task/autostart.
4. Проверить `/version` и что процесс пережил завершение установочной SSH-сессии.
5. Получить независимый Overseer verdict и оставить runtime работающим.

## Evidence

- Existing pre-smoke state was not deleted: legacy `gptadmin-rootd`, `GPTAdminRootd`, and `GPTAdmin MCP BeyondInfinity-windows-mcp` scheduled tasks plus `C:\ProgramData\gptadmin` remain present.
- No canonical Go ShellMCP scheduled task or running `shellmcp.exe` existed during discovery; prior cleanup affected only L-created `gptadmin-v141-lan-smoke-20260802.zip`, temp directory, PID, and localhost listener.
- Repository documentation classifies `gptadmin-rootd` as an obsolete legacy task and separately calls for one clean post-logon Go ShellMCP process; modifying that topology is outside this correction.
- Preflight proved public ZIP size `3,637,301`, SHA-256 `0f6428a10e3ceccd165d36528e8f050c7e803eb2a80548bb8a6496616b8358fb`, and no conflicting remote ZIP/runtime/listener.
- Exact ZIP/runtime were restored. A first ordinary `Start-Process` became attached to the OpenSSH job and exited when that session ended; files remained intact.
- Without service/task/autostart/Hub/credentials changes, the same runtime was relaunched detached through `Win32_Process.Create` using a credential-free `run-restored.cmd` inside the restored runtime directory.
- Independent second SSH acceptance: ZIP exists, runtime exists, runner exists, exactly one matching `shellmcp.exe` process exists (PID `543304`), listener `127.0.0.1:45941` exists, and `/version` returns build `141`, commit `04980acfd3d6f9924177683dac86dee5727eb959`, status `ready`.
- No cleanup was performed; restored files and process remain on Windows.
- Delayed independent check at `2026-08-02T12:45:12+03:00`: exactly one matching process PID `543304`, listener present, build `141`, status `ready`.
- Final Overseer gate: `APPROVE`; exact reversal is complete, restored runtime/files must remain untouched and no persistent Hub/service expansion is authorized.

## Estimate revisions

- None.
