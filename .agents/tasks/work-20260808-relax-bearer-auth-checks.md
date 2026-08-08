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

Production rollout requested by user on 2026-08-08: commit only the scoped auth-flag source/tests/docs/task files, build and test, enable `GPTADMIN_RELAX_AUTH_CHECKS=1` on the production Hub, restart, then run plugin and direct bearer canaries. Preserve unrelated dirty work.
