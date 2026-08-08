# Задача: глобальное чтение LHC-инструкций context-mode

## Исходный запрос

«дай глобально ему такое право плз: Контекстный обработчик не имеет права
читать глобальный файл инструкции вне workspace…»

## Цель

Разрешить context-mode `ctx_execute_file` читать канонические глобальные
инструкции Last Human Commit вне project workspace во всех проектах этого
пользователя.

## Business canary

Из workspace `/home/roomhacker/gptadmin` context-mode успешно обрабатывает
`/home/roomhacker/.local/share/last-human-commit/current/common/agents/Reviewer.md`
без `File access blocked`, при этом произвольный файл вне workspace остаётся
запрещённым.

## Confirmed scope

- Глобальное host-правило `Read(...)` только для канонического дерева
  `/home/roomhacker/.local/share/last-human-commit/current/**`.
- Проверка положительного и отрицательного canary через `ctx_execute_file`.

## Explicit exclusions

- Не разрешать чтение всего `/home/roomhacker/**`, `.ssh`, credentials или
  произвольных файлов вне workspace.
- Не менять sandbox mode, approval policy или deny-правила.

## Классификация и оценка

- Classification: Direct.
- Initial optimistic active-minute estimate: 5 minutes.
- Initial likely active-minute estimate: 10 minutes.
- Initial pessimistic active-minute estimate: 20 minutes.

## План (RU)

1. Подтвердить механизм глобального allow-rule и активный settings-файл.
2. Добавить узкое `Read`-разрешение для LHC current tree.
3. Проверить разрешённый LHC-файл и запрещённый посторонний путь.

## Progress (EN, append-only)

- 2026-08-02: context-mode reproduced the denial and explicitly identified a
  global host `permissions.allow` `Read(...)` rule as the supported escape
  hatch. Its documentation confirms all platforms consume the Claude settings
  format and `~/.claude/settings.json` is the global policy source.
- 2026-08-02: Added exactly
  `Read(/home/roomhacker/.local/share/last-human-commit/current/**)` to the
  existing global allow list. Existing deny rules, default mode, sandbox and
  unrelated permissions were preserved.
- 2026-08-02: Positive canary passed from the gptadmin workspace: context-mode
  processed the global `Reviewer.md` and reported its title plus 37 lines.
  Negative canary passed: `/etc/passwd` remains blocked as outside the project
  root. The settings file remains valid JSON and contains the exact rule once.
- 2026-08-02: Independent Overseer verdict APPROVE: the rule is narrowly
  limited to canonical LHC instructions; no broad HOME, sandbox or deny-policy
  change is required.
