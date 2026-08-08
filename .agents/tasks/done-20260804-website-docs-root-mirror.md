# Задача: корневой docs как canonical source для сайта

Status: complete

## Исходный запрос

«Worker task: work on the branch where website is now an ordinary subtree. Implement one versioned docs source without breaking current docs URLs: root docs/ remains canonical English source; add clearly derived RU/CN trees under root docs/ru and docs/cn; update website doc sync/translation scripts so website/src/content/docs and website/public/docs are generated mirrors from root docs, not independently authored source. Preserve /docs/<locale>/<slug>.md URLs. Protect technical literals. Do not edit .github workflows, do not push/deploy, do not touch unrelated product code. Add focused tests or extend existing docs tests; run relevant tests and website scripts. Commit your changes locally and report SHA, files changed, and test evidence.»

## Цель

Сделать `docs/` в корне единственным источником английской документации и перевести `website/src/content/docs` / `website/public/docs` в зеркала, не ломая текущие `/docs/<locale>/<slug>.md` URL.

## Business canary

Сайт по-прежнему загружает `/docs/en|ru|cn/<slug>.md`, но все эти файлы синхронизируются из корневого `docs/`, а локали `ru` и `cn` существуют как явные derived tree в корне.

## Scope

- Добавить `docs/ru` и `docs/cn` как derived trees от корневых English markdown-файлов.
- Поменять website sync/translation scripts на чтение из корневого `docs/`.
- Сохранить публичные URL `/docs/<locale>/<slug>.md`.
- Добавить или расширить focused tests для layout/literals/mirror contract.
- Запустить релевантные проверки и website scripts.

## Explicit exclusions

- Не трогать `.github/workflows`.
- Не пушить и не деплоить.
- Не менять unrelated product code.
- Не менять runtime docs routing, если URL contract уже сохраняется.

## Классификация и оценка

- Classification: Short.
- Initial active-minute estimate: 20 минут.

## План (RU)

1. Проверить текущий layout `docs/` и website docs mirror.
2. Перенести источник translation/sync на корневой `docs/` и добавить derived `docs/ru`, `docs/cn`.
3. Обновить тесты и прогнать релевантные проверки.

## Progress (EN, append-only)

- 2026-08-04: Added a root-docs mirror plan, switched website sync/translation scripts to source root `docs/`, and added a mirror test for `website/src/content/docs` and `website/public/docs`.
- 2026-08-04: Seeded `docs/ru` and `docs/cn` from the existing website locale trees, then validated `node scripts/sync-docs.mjs` and `node scripts/check-translation-layout.mjs`; layout mirror check passed for 17 published docs.
- 2026-08-04: Failed literal-preservation attempt #1: numeric placeholder token `999999000019999999` was dropped by the translator for `ru/API_REFERENCE.md`. Retained temp tree `/tmp/gptadmin-docs-iEkcrJ` confirmed the token was absent from the generated Russian file while present in CN.
- 2026-08-04: Switched the placeholder scheme to `__GPTADMIN_LITERAL_000001__` style tokens and reran translation.
- 2026-08-04: Failed literal-preservation attempt #2: the new code-like placeholder was still changed by the translator, now on `ru/CONFIGURATION.md` with token `__GPTADMIN_LITERAL_000055__`.
- 2026-08-04: `node scripts/translate-docs.mjs --file API_REFERENCE.md` succeeded with the new placeholder after one-file repro, but the full 17-file translation pass still fails on protected-literal restoration, so the root locale trees are not yet trustworthy enough to commit.
- 2026-08-04: Current verified state: `node scripts/sync-docs.mjs` passes; `node scripts/check-translation-layout.mjs` passes; `node scripts/check-translation-literals.mjs` fails on protected-literal restoration; `pytest` has not been rerun after the failed full translation pass. No commit SHA yet.
- 2026-08-04: Translator repo fixed separately in `/home/roomhacker/agents-projects/translate` with `protectMarkdown`/`restoreMarkdown` regression tests, but the website pipeline still depends on the exact backend output.
- 2026-08-04: Failed literal-preservation attempt #3 on the website side after switching to alphabetic placeholders: the backend still changed `__GPTADMIN_LITERAL_BJ__` in `ru/INTEGRATIONS.md`. This is the second independent repair hypothesis for the website-side placeholder scheme, so the implementation loop is stopped here per worker instructions.
- 2026-08-05: Fixed the website-side collision by switching to a distinct `__GPTADMIN_DOC_LITERAL_*__` prefix and restoring link suffixes from a synthetic URL anchor rather than the translator's own placeholder prefix.
- 2026-08-05: Verified the single-file `INTEGRATIONS.md` canary and the full 17-file translation pass after the restore fix, then resynced the website mirrors from root `docs/`.
- 2026-08-05: Final verification passed: `node scripts/sync-docs.mjs`, `node scripts/check-translation-layout.mjs`, `node scripts/check-translation-literals.mjs`, and `pytest -q tests/test_site_docs.py`.
- 2026-08-05: New follow-up request: add root CI guards for the docs-as-code contract. Keep scope root-only (`.github/workflows/`, `tests/`, root scripts if needed), assert website is not a gitlink or `.gitmodules`-backed submodule, and verify `public/openapi.yaml` against the live Go renderer with deterministic local input.
- 2026-08-05: Review follow-up: move the mirror manifest fully to root `docs/*.md` and strengthen OpenAPI artifact coverage to compare generated operation structure, not only path names.
- 2026-08-05: Lead recovery request after `f6a9bed` was pushed: independent Critic found that `website/scripts/translate-docs.mjs` enumerates English files from `website/src/content/docs/en`, making a newly manifest-listed root document impossible to translate before the mirror is synced. Confirmed scope is limited to restoring root-docs enumeration and adding a focused regression; no deploy, push, or route change is authorized.
- 2026-08-05: Root-cause repair changed translation selection to `scripts/docs-manifest.json`; the new black-box regression creates a stale website mirror, proves both manifest-only selection and root-content staging, and cleans its retained diagnostic directory.
- 2026-08-05: Final local evidence: focused canary `1 passed, 4 deselected`; `pytest -q tests/test_site_docs.py` `5 passed`; `website` layout and protected-literal checks passed. Fresh acceptance Reviewer approved the final diff. No deployment or push was performed.
