# Радикально простое онбординг-руководство Custom GPT — 2026-08-04

Status: complete
Classification: Short

## Original request

You are one of several 5.4-mini documentation workers in shared /home/roomhacker/gptadmin. Do not revert or touch unrelated work. Own ONLY README.md, docs/GETTING_STARTED.md, docs/ADAPTERS.md. Update them for a radically simple Custom GPT onboarding: quick start and copy/paste one-liners; correctly explain Bearer and OAuth paths and the generated Actions schema. Mention proxy/webhook only as optional capabilities linking to the dedicated docs, not default steps. Preserve security: never encourage copying internal secrets. Make edits and run relevant docs/link tests. Report changed paths and exact tests.

## Objective

Обновить только `README.md`, `docs/GETTING_STARTED.md` и `docs/ADAPTERS.md`, чтобы Custom GPT onboarding был максимально простым, с короткими copy/paste командами, корректным объяснением Bearer/OAuth и генерации Actions schema, а proxy/webhook оставались только опциональными ссылками на отдельные docs.

## Business canary

После правок новые пользователи видят один простой путь для Custom GPT: install → get URL → import generated Actions schema → choose Bearer or OAuth; без рекомендаций копировать внутренние секреты и без обязательных шагов про proxy/webhook.

## Confirmed scope

- Только `README.md`, `docs/GETTING_STARTED.md`, `docs/ADAPTERS.md`.
- Текстовые правки, ориентированные на быстрый старт и one-liners.
- Проверка ссылок и релевантных docs tests после правок.

## Explicit exclusions

- Не трогать другие файлы.
- Не менять runtime/code.
- Не добавлять proxy/webhook как дефолтные шаги.
- Не поощрять копирование внутренних секретов.

## Initial estimate (immutable)

- Optimistic: 15 active minutes
- Likely: 25 active minutes
- Pessimistic: 45 active minutes

## План (русский)

1. Проверить текущие формулировки в трёх разрешённых docs и сверить терминологию Bearer/OAuth/Actions schema.
2. Внести минимальные правки, чтобы Quick Start стал короче и прямее, а proxy/webhook остались optional.
3. Прогнать релевантные проверки ссылок и docs-тесты.

## Progress (English)

- 2026-08-04: task created; inspecting the current docs contract and link targets before editing.
- 2026-08-04: updated README.md, docs/GETTING_STARTED.md, and docs/ADAPTERS.md to simplify Custom GPT onboarding and keep proxy/webhook optional.
- 2026-08-04: verification passed: `python3 -m pytest tests/test_docs_product_contract.py tests/test_product_auth_language.py tests/test_install_win.py -q`.
