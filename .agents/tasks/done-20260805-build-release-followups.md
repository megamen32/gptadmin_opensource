# Build-and-release follow-ups

## Symptom

CI run `30981097243` passes the repaired docs, admin UI, macOS, Windows, and failover jobs, but `build-and-release` fails during `Install Python dependencies and test`.

## Smallest evidence

- `tests/test_no_secrets.py::test_no_static_passwords`: `advanced.md` contains placeholder `mypassword`.
- `tests/test_release_workflow_contract.py::test_publication_waits_for_every_platform_and_ui_gate`: expected `needs` order is stale after adding `docs-as-code-contract`.

## Blocker / scope

Separate follow-up scope from the completed CI repair; no changes made for these findings in this task.

## Resolution

- Replaced both documentation-only password placeholders in `website/skills/xlsx/scenes/advanced.md` with `your-password`.
- Updated the release workflow contract test for the current `docs-as-code-contract` gate.
- Added the legacy-token preservation rule to `README.md`.
- Focused verification: `24 passed`.
- Full verification: `uv run pytest tests/ -q` -> `351 passed, 4 skipped, 1 warning`.
