# Fix CI docs-as-code mirror

Status: in_progress

## Исходный запрос

Починить упавший build.

## Objective

Синхронизировать документацию API с website mirrors, чтобы Build, Sync, Release снова проходил.

## Business canary

`tests/test_site_docs.py::test_site_docs_mirror_root_source_and_public_tree` зелёный; полный GitHub Actions workflow зелёный.

## Explicit exclusions

Не менять runtime и production; не трогать unrelated dirty worktree.

## Initial active-minute estimate

20 active minutes.

## План

1. Воспроизвести конкретное падение и сравнить зеркала документации.
2. Синхронизировать только расхождение API Reference.
3. Прогнать focused/full tests и новый CI workflow.

## Evidence

- Latest run `31261373741` for `f9d4cf3` failed only in `Docs-as-code contract`; `30 passed, 1 failed`.
- Failing test: `tests/test_site_docs.py::test_site_docs_mirror_root_source_and_public_tree`.
- Root `docs/API_REFERENCE.md` and `docs/ru/API_REFERENCE.md` contain the new `/admin/api/connection-debug` row; website source/public `en` and `ru` mirrors did not.
