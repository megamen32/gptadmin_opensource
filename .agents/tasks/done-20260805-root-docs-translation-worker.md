# Root docs translation source repair

Role: Worker

## Assignment

Original request: Fix the pushed docs migration regression with subagents.

Objective: make `website/scripts/translate-docs.mjs` enumerate the published English document set from the root docs contract, never from a website mirror.

Business canary: after adding a manifest-listed English document under root `docs/` without syncing the website mirror, translation selection accepts that document and uses its root content.

Owned paths: `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py` only.

Explicit exclusions: no changes to routes, docs content, manifest entries, workflows, deployment, commits, pushes, or unrelated website code. Preserve any concurrent edits.

Acceptance: first establish a focused failing regression for root-only enumeration, implement the smallest fix, then run that regression plus the existing site-docs contract tests. Append detailed evidence here and return TL;DR only.

## Progress (EN, append-only)

- Scoped implementation completed only in the two owned paths:
  - `website/scripts/translate-docs.mjs`
  - `tests/test_site_docs.py`
- Root-cause fix: `englishFiles()` now reads `scripts/docs-manifest.json` from the repo root instead of enumerating `website/src/content/docs/en`, so translation selection follows the root docs contract and not the website mirror.
- New regression added: `test_translate_docs_enumerates_root_manifest_not_website_mirror()` creates a throwaway repo layout with:
  - a root docs manifest containing `alpha.md` and `beta.md`
  - root `docs/alpha.md` and `docs/beta.md`
  - a stale website mirror containing only `alpha.md`
  - a stub translator plus `KEEP_TRANSLATION_TMP=1`
  - assertion that the dry-run staging tree contains both manifest-listed English docs
- Failing regression before the fix:
  - `pytest -q tests/test_site_docs.py -k 'translate_docs_enumerates_root_manifest_not_website_mirror'`
  - result: failed because the staged docs tree contained only `alpha.md`
  - observed assertion: `AssertionError: assert ['alpha.md'] == ['alpha.md', 'beta.md']`
- Passing verification after the fix:
  - `pytest -q tests/test_site_docs.py -k 'translate_docs_enumerates_root_manifest_not_website_mirror'`
  - result: `1 passed, 4 deselected`
  - `pytest -q tests/test_site_docs.py`
  - result: `5 passed`
- Changed symbols:
  - `website/scripts/translate-docs.mjs`: added `docsManifestPath`, changed `englishFiles()`
  - `tests/test_site_docs.py`: added regression imports and `test_translate_docs_enumerates_root_manifest_not_website_mirror`
- Not tested / not changed:
  - no routes, docs content, manifest contents, workflows, deployment, commits, pushes, or unrelated website code
  - did not touch the concurrent worktree files outside the owned paths
- Remaining risk:
  - the translator still depends on `scripts/docs-manifest.json` being current; this is intentional and matches the root docs contract

- Cleanup repair for the regression test:
  - `tests/test_site_docs.py` now imports `shutil` and wraps the dry-run invocation in a `try/finally`
  - the exact retained temp root parsed from `KEEP_TRANSLATION_TMP=1` is removed with `shutil.rmtree(retained_tmp, ignore_errors=True)` even if the manifest assertion fails
  - the stale-mirror assertion remains unchanged: the test still proves the translator enumerates the root manifest while the website mirror is stale
- Verification after cleanup repair:
  - `pytest -q tests/test_site_docs.py -k 'translate_docs_enumerates_root_manifest_not_website_mirror'`
  - result: `1 passed, 4 deselected`
  - `pytest -q tests/test_site_docs.py`
  - result: `5 passed`
- Notes:
  - pre-existing `/tmp/gptadmin-docs-*` directories were present from earlier runs; the repaired test now removes its own retained diagnostic directory reliably

- Final canary strengthening:
  - `tests/test_site_docs.py` now asserts the retained staged `alpha.md` and `beta.md` contents equal the root `docs/` content
  - the test also asserts staged `alpha.md` differs from the stale website mirror `alpha.md`
  - this keeps the stale-mirror behavior proof while checking the exact staged payload, not just filenames
- Verification after strengthening:
  - `pytest -q tests/test_site_docs.py -k 'translate_docs_enumerates_root_manifest_not_website_mirror'`
  - result: `1 passed, 4 deselected`
  - `pytest -q tests/test_site_docs.py`
  - result: `5 passed`
