# Root docs translation repair review

Role: Reviewer

## Assignment

Original request: Fix the pushed docs migration regression with subagents.

Review target: uncommitted repair limited to `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py`.

Objective: independently determine whether the patch makes root `scripts/docs-manifest.json` plus root `docs/` the translation selector, with a hermetic regression that fails under the previous mirror-derived selection.

Business canary: a manifest-listed root document absent from the website English mirror is selected and its root content is used before any sync.

Explicit exclusions: read-only review; no edits, commits, pushes, deployments, test rewrites, or scope expansion.

Acceptance: report only actionable findings with severity and exact evidence, including test cleanup/isolation and behavior-contract gaps. Append detailed evidence here and return TL;DR only.

## Progress (EN, append-only)

- Reviewed the selected uncommitted diff in `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py` only.
- Verified the selector now reads `scripts/docs-manifest.json` from repo root instead of the website English mirror.
- Ran `pytest -q tests/test_site_docs.py -k translate_docs_enumerates_root_manifest_not_website_mirror`; the new hermetic canary passed (`1 passed, 4 deselected`).
- The test constructs a root `docs-manifest.json`, a root `docs/` tree, and a mismatched website English mirror so the previous mirror-derived selection would have failed on file set mismatch.
- No scoped regressions or behavior-contract gaps were found in the reviewed patch.

Result: APPROVE
