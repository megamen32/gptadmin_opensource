# DEBUG_VERIFY_WORK_LOW_SECURITY_MODE

Status: complete (public canary passed; runtime flag restored to 0)

## Цель

Добавить явно opt-in debug-режим для восстановления OAuth/ShellMCP
потока: сохранять проверку ключа/подписи и `ADMIN_PASSWORD`, но не требовать
claims, срок токена или PKCE; после корректного signed enrollment ShellMCP не
требовать ручного `approve_pending_server`.

## Границы

- Режим включается только `DEBUG_VERIFY_WORK_LOW_SECURITY_MODE=1`. В этом
  цикле пользователь явно разрешил кратковременное включение на текущем
  публичном Hub; по завершении canary флаг возвращается в `0`.
- Реальные bearer, пароли, OAuth client secrets и relay credentials никогда не
  возвращаются браузерному UI, логам или документации.
- Подпись JWT/managed bearer и signed enrollment ShellMCP не обходятся.

## Canary

Focused Go tests доказывают: флаг снимает PKCE/claim gates, неизвестный ключ
остаётся отвергнутым, а signed ShellMCP enrollment получает credential без
ручного approval; явно включённый флаг действует и на публичном Hub.

## Evidence

- `go test ./internal/hub -run 'TestFromEnvReadsDebugVerifyWorkLowSecurityMode|TestDebugVerifyWorkLowSecurityModeAutoApprovesSignedRelayEnrollment|TestRelaxAuthChecksAllowsOAuthWithoutPKCEVerifier|TestRelaxAuthChecksAcceptsSignedJWTWithLegacyClaims' -count=1` passed.
- `go test ./internal/hub -count=1` and `git diff --check` passed.
- Hub commit `6e7c6b8c06e4fe39fc3fefb831baafd8665d28d7`, SHA-256 `09ba61715d9b4f6421763ea7e009c924e44b91102a12df2e2ff544638a873c48`, is active on `gptadmin-hub.service`; local `/healthz` and `/version` passed.
- Real ChatGPT plugin canary selected GPTADMIN, was allowed once for the conversation, performed the uptime operation through two tool calls, and returned status for six servers.
- After canary `DEBUG_VERIFY_WORK_LOW_SECURITY_MODE=0` was verified in the private EnvFile, service is active, and `/healthz` plus `/version` passed again.

## Оценка

Старт текущего контролируемого цикла: 2026-08-18T00:52:14+03:00
Минимум / максимум active: 15 / 30 минут.
