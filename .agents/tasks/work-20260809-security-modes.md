# Configurable GPTAdmin/ShellMCP security modes

Status: complete (v150 deployed and live canary passed; release-numbering regression follow-up is shipped)

## Исходный запрос

Сделать обычный режим работы беспроблемным, а максимальную и кастомную защиту явно настраиваемыми через админку и конфигурацию; сохранить philosophy-подход проекта.

## Objective

Устранить скрытое противоречие между privilege execution и systemd hardening. Ввести понятные режимы: обычный, максимальная защита, кастомный набор проверок для bearer/process/ShellMCP installation.

## Business canary

В обычном режиме штатный ShellMCP privilege flow работает; максимальный режим проверяет bearer ownership/signature и process restrictions; кастомный режим отражается в админке, CLI/config и live unit; все режимы покрыты тестами.

## Explicit exclusions

Не удалять `philosophy`; не менять production security mode до отдельного подтверждённого canary; не скрывать режимы в hardcoded systemd шаблоне.

## Initial active-minute estimate

90 active minutes.

## План

1. Инвентаризировать `philosophy`, текущие режимы, bearer/auth и systemd generation.
2. Сформировать компактный контракт режимов и конфигурации.
3. Реализовать CLI/config/admin UI и генерацию units.
4. Добавить red/green unit, auth, process and UI tests.
5. Провести isolated canary, затем согласовать production mode/apply.

## Implementation progress (English, append-only)

- 2026-08-09: Added process profiles `normal`, `maximum`, and `custom` in Hub security state. Default is `normal` with privileged execution allowed; `maximum` requires all systemd hardening flags and disallows privileged execution; `custom` exposes each flag explicitly and rejects the contradictory state of denying privileged execution without `NoNewPrivileges`.
- 2026-08-09: Added typed Hub API `GET/PUT /admin/api/security/profile`, admin-dashboard controls, persisted profile state, audit event, and restart-bound response. Existing bearer/OAuth security presets remain separate.
- 2026-08-09: Added `gptadmin security profile` CLI read/write workflow and setup support. Unit rendering now evaluates the selected profile at write time, so a setup or CLI change cannot be lost because the Python module was imported earlier.
- 2026-08-09: Focused verification passed: `pytest -q tests/test_security_modes.py tests/test_shellmcp_service_templates.py tests/test_site_docs.py` (13 passed); `go test ./internal/hub -count=1` passed; `python -m py_compile cli.py` passed; temporary-directory CLI maximum-profile canary passed; `git diff --check` passed.
- 2026-08-09: Commits `2e1118c` and `4a40c7e` contain only this feature's selected files. Unrelated shared-worktree changes remain unstaged and untouched.
- 2026-08-09: Added a typed `bearer_profile` alongside `process_profile`. Signature verification remains unconditional; maximum requires issuer, audience, resource, scope, subject, issued-at, expiry, PKCE, token lifecycle, and redirect/resource allowlists. Normal preserves the established legacy-compatible contract; custom controls these checks individually. Added CLI `gptadmin security bearer` and dashboard controls.
- 2026-08-09: Full Go Hub suite passed after regression repair (`go test ./... -count=1 -timeout=120s`); ShellMCP full suite passed (`go test ./... -count=1 -timeout=120s`); Python focused suite passed (14 tests). Isolated live Go Hub canary passed health, profile update `normal -> maximum`, bearer issuance, and real MCP `initialize`; isolated ShellMCP stdio canary passed `initialize` and explicit `shell_exec`.
- 2026-08-09: v149 rollout completed after fixing the updater's Go Hub health predicate (`gptadmin-go-hub`); v150 release `31287945905` passed all release gates and was deployed with the v150 CLI.
- 2026-08-09: Production marker is build 150 / git `17344b648a10d9af2d1cbd3505a96970cd53a5f1`; Hub, ShellMCP, and FRP are active. The old `100-gptadmin-user-mode.conf` `ProtectHome=read-only` override was removed by the update cleanup while preserving `User=admin`.
- 2026-08-09: Production `/admin/api/security/profile` reports process and bearer mode `normal`, `allow_privileged_execution=true`, and all normal-mode process hardening flags false. Real local MCP initialize returned protocol `2024-11-05`, server `gptadmin-go-hub`, version `150`; signed ShellMCP heartbeat endpoint returned a structured registration response.
- 2026-08-09: Release-numbering follow-up shipped in commits `c9e9aa9`, `afa651a`, and `045125f`: auto-tag now runs on every main push, advances an already-published VERSION without rewriting immutable tags, serializes runs, and retries dispatch while a newly-pushed tag propagates. Contract tests passed (14 tests); auto-tag successfully created v151, dispatched its build, and v151 completed successfully with 13 public release assets.

## Acceptance result

Production was regenerated and restarted under the previously granted restart authorization. The live normal profile and MCP canary passed. Maximum/custom remain explicit opt-in modes through the Hub API, dashboard, CLI, and persisted security state; no mode switch was applied to production.
