# Root docs translation final review

Role: Reviewer

## Assignment

Original request: Fix the pushed docs migration regression with subagents.

Review target: uncommitted final repair limited to `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py`.

Objective: independently validate the root manifest selector, the stale-mirror regression, and its retained temporary-directory cleanup.

Business canary: a manifest-listed root document absent from the website English mirror is selected from the root contract, and the test leaves no diagnostic temporary directory after assertion.

Explicit exclusions: read-only review; no edits, commits, pushes, deployments, test rewrites, or scope expansion.

Acceptance: report only actionable findings with severity and exact evidence. Append detailed evidence here and return TL;DR only.

## Progress (EN, append-only)

- Reviewed the final repair diff in `website/scripts/translate-docs.mjs` and `tests/test_site_docs.py` only.
- Verified the new selector reads `scripts/docs-manifest.json` from the repo root instead of the website mirror, which matches the requested root-contract behavior.
- Verified the new regression test builds an isolated repo fixture with a stale website mirror entry, confirms the temporary translation workspace contains only manifest-listed root docs, and removes the retained temp directory in `finally`.
- Ran `pytest -q tests/test_site_docs.py -q`; all 5 tests passed.
- Result: APPROVE.
