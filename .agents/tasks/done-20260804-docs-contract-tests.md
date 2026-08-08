# Docs contract regression tests — 2026-08-04

Status: complete
Classification: Short

## Original request

You are one of several 5.4-mini workers in shared /home/roomhacker/gptadmin. Do not revert or touch unrelated work. Own ONLY tests/test_docs_product_contract.py and, if absolutely needed, tests/test_product_auth_language.py. Strengthen focused docs regressions for the simplified Custom GPT quickstart and separate default-off proxy/webhook story, based on current intended docs contract. Do not edit production docs. Run the focused tests and report changed paths and exact test result.

## Objective

Tighten focused regression coverage for the docs contract around the simplified Custom GPT quickstart and the default-off proxy/webhook story, while leaving production docs untouched.

## Business canary

The focused docs contract tests pass and clearly separate the simplified Custom GPT quickstart from the default-off proxy/webhook path.

## Confirmed scope

Only `tests/test_docs_product_contract.py` and, if absolutely necessary, `tests/test_product_auth_language.py`.

## Explicit exclusions

No production docs edits, no unrelated tests, no broad refactors, no behavior changes outside the docs contract test surface.

## Initial estimate (immutable)

- Optimistic: 20 active minutes
- Likely: 35 active minutes
- Pessimistic: 60 active minutes

## План (русский)

1. Найти текущие фокусные тесты и понять, какие именно контрактные пробелы надо усилить.
2. Внести минимальные изменения только в разрешённые тестовые файлы.
3. Запустить только целевые тесты и зафиксировать точный результат.

## Progress (English)

- 2026-08-04: task created; starting focused inspection of the docs contract tests.
- 2026-08-04: added focused regressions for the simplified Custom GPT quickstart and the default-off proxy/webhook story in `tests/test_docs_product_contract.py`.
- 2026-08-04: verified with `pytest -q tests/test_docs_product_contract.py` → 7 passed.
