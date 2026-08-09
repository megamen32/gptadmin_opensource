# Configurable GPTAdmin/ShellMCP security modes

Status: in_progress (implementation and isolated verification complete; production apply pending explicit canary approval)

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
- 2026-08-09: Read-only production probe shows server-100 Hub is healthy on build 147 but still has the old unit hardening (`NoNewPrivileges=true`, `ProtectSystem=full`, `ProtectHome=true`). New code is not live there yet.

## Remaining acceptance boundary

Production units have not been regenerated or restarted in this task. Before applying a non-normal mode, run a host-local canary showing normal ShellMCP privilege flow, then obtain explicit confirmation for the restart/apply boundary. The default source behavior is normal/frictionless; existing production units are not silently changed by these commits.
