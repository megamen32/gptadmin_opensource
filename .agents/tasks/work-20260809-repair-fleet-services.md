# Repair confirmed fleet service failures

Status: in_progress

## Исходный запрос

Починить найденные сломанные сервисы и проверить Android.

## Objective

Диагностировать и восстановить только реально неисправные сервисы, не включая намеренно отключённые legacy-владельцы FRP.

## Business canary

Все исправляемые systemd/service-ops units healthy; GPTAdmin Android target снова heartbeat online либо зафиксирован внешний блокер; Hermes gateway/egress healthy.

## Explicit exclusions

Не запускать server01 FRP без подтверждения отсутствия duplicate ownership; не трогать unrelated dirty worktree; не раскрывать секреты.

## Initial active-minute estimate

45 active minutes.

## План

1. Снять узкие read-only причины отказов.
2. Получить подтверждение на consequential restart/apply, если он необходим.
3. Восстановить сервисы минимальными действиями.
4. Прогнать fleet/GPTAdmin/Android/Hermes canaries.

## Новое подтверждение

- Это не случайный флаг на одном хосте: `cli.py` генерирует `LINUX_HARDENING` для каждого system install с `NoNewPrivileges=true`, `ProtectSystem=full`, `ProtectHome=true` и `ReadWritePaths`.
- Тот же hardening вставляется в `UNIT_HUB`, `UNIT_SHELLMCP`, FRP и cloudflared units.
- Live server-100 подтверждает generated units: `gptadmin-hub.service` и `shellmcp.service` имеют `NoNewPrivileges=yes`; `shellmcp.service` запускается как `User=admin`.
- Значит, это deployment/installer design mismatch: API ShellMCP принимает `run_as_user=root`, но system-install sandbox заранее запрещает privilege gain. Это надо исправлять в архитектуре/шаблоне, а не снимать флаг вручную вслепую.
