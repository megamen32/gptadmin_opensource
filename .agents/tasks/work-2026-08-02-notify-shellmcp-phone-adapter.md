# Notify -> GPTAdmin -> Android ShellMCP phone adapter

## Исходный запрос

Нужен адаптер Notify Center для звонка через GPTAdmin -> Android ShellMCP ->
call, чтобы ADB не был необходим.

## Цель

Добавить узкий, allowlisted Android phone-call contract в GPTAdmin/ShellMCP и
адаптер Notify Center, который вызывает именно этот contract, а не
произвольную shell-команду или ADB.

## Business canary

Без ADB Notify вызывает GPTAdmin adapter; Hub подтверждает Android target и
вызывает выделенный Android call tool. До side-effect call выполняется
read-only discovery/identity canary.

## Подтверждённый scope

- Исследование текущих Hub/MCP relay и Android ShellMCP capabilities.
- После выбранного плана — dedicated tool, policy, Notify adapter, tests и
  live no-ADB validation.

## Явные исключения

- Arbitrary `shell_exec`, ADB transport/fallback removal, token exposure,
  forwarding user-controlled shell text, silent real phone call before call
  contract is confirmed.

## Оценка

- Initial active-minute estimate (optimistic / likely / pessimistic): 180 / 360 / 600.
- Revision log: none.

## Начальный план

1. Подтвердить target discovery, authentication и Android execution contract.
2. Представить три варианта и дождаться выбора.
3. Показать call stack, file diff и signatures выбранного варианта.
4. Реализовать dedicated tool и Notify adapter с no-ADB canary.

## Progress log

- 2026-08-02: Task created. Existing evidence shows only generic ShellMCP
  shell_exec; no Android-specific no-ADB call contract is confirmed yet.
- 2026-08-02: Source research confirmed generic MCP tools/list and tools/call,
  including shell_exec and system_info. No dedicated Android call tool or
  narrow Notify integration exists; arbitrary shell execution is not an
  acceptable adapter boundary.
- 2026-08-02: User selected YAGNI: fixed Android-side phone command only,
  invoked through a named adapter; Notify must never forward arbitrary command
  text or a caller-provided number to shell_exec.
