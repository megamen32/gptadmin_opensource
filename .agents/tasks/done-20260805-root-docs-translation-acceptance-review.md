# Root docs translation acceptance review

Role: Reviewer

## Assignment

Original request: Fix the pushed docs migration regression with subagents.

Review target: final uncommitted repair in `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py`.

Objective: independently verify that the final behavioral canary proves both root-manifest selection and root-content staging while the website mirror is stale.

Business canary: `alpha.md` and manifest-only `beta.md` are staged from root `docs/`; `alpha.md` differs from the stale website mirror; retained diagnostics are cleaned.

Explicit exclusions: read-only review; no edits, commits, pushes, deployments, test rewrites, or scope expansion.

Acceptance: report only actionable findings with severity and exact evidence. Append detailed evidence here and return TL;DR only.

## Progress (EN, append-only)

- Reviewed target diff in `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py` only.
- Verified the acceptance canary with `pytest -q tests/test_site_docs.py -k translate_docs_enumerates_root_manifest_not_website_mirror` → `1 passed`.
- Verified the full site-docs suite with `pytest -q tests/test_site_docs.py` → `5 passed`.
- Result: APPROVE. No actionable findings in the scoped diff; the new test proves manifest-driven selection from root `scripts/docs-manifest.json`, root-doc staging into the retained temp tree, and divergence from the stale website mirror while cleanup is preserved on the success path.
