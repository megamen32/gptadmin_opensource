# Временно упростить проверки Bearer/OAuth

## Исходный запрос

Убрать лишние проверки для ключей: оставить только проверку наличия ключа; просроченность, issuer и PKCE временно не проверять, отдельные проверки вернуть позже через TODO.

## Цель

Узко изменить runtime-проверку существующего Bearer-ключа, сохранив проверку самого значения ключа и не раскрывая секреты.

## Business canary

Существующий Bearer проходит на canonical GPTAdmin MCP после deploy; неизвестный ключ остаётся отвергнутым.

## Scope

Только auth guard и focused regression tests в Go Hub.

## Explicit exclusions

Не менять OAuth origin, ingress, DNS, токены в production state или Custom GPT schema в рамках этого шага.

## Initial estimate

15 active minutes.

## План

1. Найти runtime guards и существующие auth tests.
2. Добавить failing regression test для временно relaxed проверки.
3. Внести минимальный фикс и TODO на возврат проверок.
4. Запустить focused Go tests и проверить diff.

## Evidence

- Added `GPTADMIN_RELAX_AUTH_CHECKS`, default `0`; `FromEnv` regression covers `1` and `0`.
- Relaxed mode preserves JWT signature verification and managed/configured key digest matching, while skipping managed-token expiry/revocation/type gates, JWT claim gates, and OAuth PKCE checks.
- Added tests for signed legacy claims, unknown-key rejection, and OAuth authorize/token without a PKCE verifier.
- Focused tests: `go test ./internal/hub -run 'TestRelaxAuthChecks|TestFromEnvReadsRelaxAuthChecksFlag|TestJWTRequestContextRejectsWrongAudienceAndExpiredConnection' -count=1` -> passed.
- Full Go Hub suite: `go test ./...` -> passed.
- `git diff --check` -> passed.

## Rollout status

Production rollout completed on 2026-08-08: commits `1b58f74` (runtime flag/tests) and `df30ea5` (docs mirror/release metadata) pushed to `origin/main`; GitHub Build/Sync/Release `31254337241` passed all required jobs. Server-100 is on build 146 / `1b58f74` with `GPTADMIN_RELAX_AUTH_CHECKS=1`. HAOS standby image was backed up as `local/aarch64-addon-gptadmin_hub_standby:backup-c970-20260808`, updated to build 146 / `1b58f74`, configured with the same custom MCP bearer and `relax_auth_checks=1`, and restarted.

## Production canary

- Canonical `u-f1102930.t.gptadmin.bezrabotnyi.com/version`: HTTP 200, build 146 / `1b58f74`.
- Canonical OAuth authorize with the admin password and PKCE request: HTTP 302 with authorization code redirect.
- Canonical Bearer `/mcp-relay/servers` and `/mcp`: HTTP 200.
- GPTADMIN plugin `discover`: passed; Hub online and BrowserClaw online.
- GPTADMIN plugin `shell_exec(uptime)` on `shell:roomhacker-server-100`: completed, returncode 0.
- GPTADMIN plugin BrowserClaw `tabs(new example.com)` returned Example Domain snapshot; `tabs(close)` completed successfully.
- The Bearer used for the canary was supplied in chat and must be rotated after handoff.
