# Fleet service health and Android check

Status: complete

## Исходный запрос

Найти все сломанные сервисы на компьютерах и проверить Android.

## Objective

Собрать read-only health evidence через server-health и GPTAdmin, отдельно подтвердить физический S21.

## Business canary

Конкретные failed systemd units названы; Android serial `R5CR702SRFP` обнаружен и snapshot выполнен.

## Explicit exclusions

Не перезапускать и не чинить сервисы без отдельного указания.

## Initial active-minute estimate

15 active minutes.

## Evidence

- Compact fleet probe: server-100 failed_units=3, server-88=1, server01=1, server01=1; Hermes egress/gateway not listening; Android not live in GPTAdmin.
- GPTAdmin: Android ShellMCP and Termux targets are stale; physical S21 is reachable locally via agent-device.
- Physical Android `SM G998B`, serial `R5CR702SRFP`, booted; snapshot showed Samsung AOD/system surface with 3 notifications. Session closed cleanly.
- Concrete failed units: server-100 `autovpnallowip.service`, `backup_db_video_stats.service`, `gptadmin-auto-update.service`; server-88 `certbot.service`; server01 `gptadmin-tunnel-frpc.service`.
- server01 command remained queued, so its `failed_units=1` was not independently named through GPTAdmin.
