# Root docs translation contract exploration

Role: Explorer

## Assignment

Original request: Fix the pushed docs migration regression with subagents.

Objective: independently map the root-docs manifest, translation selection, and mirror dependencies, then state the minimum behavior the repair and regression must prove.

Business canary: a new manifest-listed root English document can be selected for translation before website mirrors are refreshed.

Owned paths: read-only review of `scripts/docs-manifest.json`, `docs/`, `website/scripts/translate-docs.mjs`, `website/scripts/sync-docs.mjs`, and `tests/test_site_docs.py`.

Explicit exclusions: no edits, tests that mutate files, commits, pushes, deployments, or scope expansion.

Acceptance: return a concise evidence-backed contract, identify any adjacent blocker inside this scope, and append detailed evidence here. Return TL;DR only.

## Progress (EN, append-only)

### Explorer evidence (2026-08-05)

- `scripts/docs-manifest.json:1-19` is the canonical allowlist of 17 root document filenames. The root `docs/` currently contains 45 Markdown files, while `docs/ru`, `docs/cn`, and all website mirror locale trees contain exactly the 17 manifest entries.
- `website/scripts/translate-docs.mjs:23-30` implements `englishFiles()` by listing `website/src/content/docs/en`, filtering `.md`, sorting, and optionally validating `--file` against that mirror. It does not read `scripts/docs-manifest.json` or enumerate `docs/` directly. Therefore a new root `docs/<manifest-listed>.md` is invisible to translation until `website/scripts/sync-docs.mjs` has refreshed the English mirror.
- `website/scripts/translate-docs.mjs:114-119` reads each selected source from `docs/<file>` and stages it for translation; `:134-149` expects generated `<stem>.ru.md` and `<stem>.cn.md` and writes them back to `docs/ru/<file>` and `docs/cn/<file>`. The business canary requires selection before mirror refresh, so the selector's source of truth must be the root manifest/root docs, not the website mirror.
- `website/scripts/sync-docs.mjs:20-28,85-97` reads the same manifest and mirrors root `docs/` (for `en`) plus locale roots into both `website/src/content/docs/{en,ru,cn}` and `website/public/docs/{en,ru,cn}`. It is a downstream publication step, not a prerequisite for selecting translation inputs. `:58-75` also prunes non-allowlisted destination files.
- `tests/test_site_docs.py:18-36,51-54` already defines the mirror contract: every locale mirror's filename set must equal the manifest and file contents must equal the corresponding canonical source. It does not test translation selection or invoke the translator. The minimum regression must isolate the selector and prove a manifest-listed root file is selected while the website English mirror is intentionally stale/missing it; it should also preserve rejection of an unknown `--file`.
- Adjacent in-scope dependency risk: after translation, website mirrors remain stale until `sync-docs.mjs` runs (the package hooks are `website/package.json:6-9`: explicit `sync-docs`, plus `predev`/`prebuild`). The repair must not conflate “translation selected/generated” with “website refreshed”; the canary needs separate proof of pre-refresh selection and later mirror sync.
- Checked: manifest, root/localized docs file sets, translation and sync scripts, site-doc mirror tests, package script references, git history (`f6a9bed` changed sync/tests to root manifest but did not update translation selector). Excluded edits, mutation tests, deployment, and files outside the assigned scope.

### Contract

Translation selection source of truth is `scripts/docs-manifest.json` intersected with existing canonical root files under `docs/`; it must not depend on `website/src/content/docs/en`. `--file` must accept a manifest-listed canonical root file even when its website mirror is absent/stale and reject non-manifest/unknown names. Mirror synchronization remains a separate downstream operation, and existing mirror equality/pruning behavior must stay intact.
