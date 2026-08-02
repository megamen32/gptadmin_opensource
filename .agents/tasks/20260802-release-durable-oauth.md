# Задача: выпустить durable OAuth и HAOS recovery

## Исходный запрос

«пуш сделай и релиз и тэг выпусти и документацию обнови»

## Цель

Опубликовать уже запушенный main как версионированный GitHub release с tag и
обновить пользовательскую документацию о durable OAuth и HAOS standby.

## Business canary

Tag указывает на текущий main, GitHub Release опубликован, а документация
содержит корректные шаги первой авторизации и ожидаемого восстановления токена.

## Scope

- Определить безопасный следующий release tag по существующей истории.
- Обновить только release- и integration-документацию.
- Прогнать релевантные проверки, создать commit, tag и GitHub Release.

## Explicit exclusions

- Не менять runtime, OAuth state или credentials.
- Не включать чужие незакоммиченные изменения.

## Классификация и оценка

- Classification: Full (public release/tag is irreversible).
- Initial active-minute estimate: 20 minutes.

## План (RU)

1. Сверить текущие tags, release и release-doc conventions.
2. Подготовить documentation/release notes и проверить claims.
3. Провести единственный release review, затем создать tag и GitHub Release.

## Progress (EN, append-only)

- 2026-08-02: Task opened. The user explicitly requested push, tag, release,
  and documentation update. Existing main is already published through 27538a9.
- 2026-08-02: Overseer checkpoint requested; no independent delegate transport
  is exposed, so release evidence will remain bounded and secret-free.
- 2026-08-02: Selected v137 because public gptadmin_opensource already has a
  v136 release while private source VERSION is 134; a lower release would
  regress public version identity. Updated VERSION, CHANGELOG, README,
  GETTING_STARTED, INTEGRATIONS, and the stale HAOS roadmap claim only.
- 2026-08-02: Release review passed: 25 release identity/workflow/provenance
  tests and 8 documentation contract tests are green; git diff --check is
  clean. The explicitly staged scope excludes all foreign Last Human Commit
  instruction changes and backups.
- 2026-08-02: Release build 137 was pushed as 84bc4df. Auto-tag completed
  successfully and created annotated v137 at that commit; the immutable
  Build, Sync, Release workflow is in progress. agent-resume job
  20260802T001648Z-529e1c2e is armed to verify its public release result.
- 2026-08-02: Four prerequisite build jobs completed successfully; only the
  final build-and-release job remains in progress, so the public release is
  not claimed yet. agent-resume job 20260802T002017Z-6f45bc7c is armed for the
  next bounded verification.
- 2026-08-02: v137 Build, Sync, Release failed before publication because a
  clean CI checkout lacked two host-local incident artifacts referenced by
  docs. v137 remains immutable and correctly tagged; it is not claimed as a
  public release. v138 removes all such Markdown dependencies while preserving
  the incident facts. The full preflight passed 45 tests, and the final
  release-blocker regressions passed 6 tests with a clean link scan.
- 2026-08-02: v138 passed build, manifest/SBOM, installer and vulnerability
  gates but failed only when GitHub rejected artifact attestation for a
  user-owned private repository; release upload therefore did not run. v139
  skips that unsupported attestation solely on private source repositories,
  keeps every other blocking gate, and documents the boundary. 23 targeted
  workflow/policy/provenance/docs tests pass; v138 remains immutable.
