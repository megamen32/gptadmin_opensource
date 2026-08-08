# Fix CI/CD failures

## Original request

а чинить кто будет?

## Objective

Fix the two failures from CI run `30980545108` for commit `2e5d118`: admin UI lint and root docs-as-code contract verification.

## Business canary

The focused local checks pass and a pushed fix produces a successful CI/CD workflow.

## Confirmed scope

- `/home/roomhacker/gptadmin/admin-ui/src/App.tsx`
- Root docs-as-code contract files identified by the failing workflow
- CI run `30980545108`

## Explicit exclusions

- No unrelated refactors.
- No deployment, tag, release, or rollback.

## Estimate

- Initial active-minute estimate: 25 minutes.

## Evidence

- Red baseline: CI run `30980545108` failed `admin-ui-build` on unused `CapabilitiesScreen` and docs contract on stale OpenAPI expectations.
- Red local baseline: `python3 -m pytest tests/test_openapi_artifact.py tests/test_public_mirror.py -q` -> `2 failed, 1 passed`.
- Green docs verification: `python3 -m pytest tests/test_site_docs.py tests/test_openapi_artifact.py tests/test_docs_product_contract.py tests/test_public_mirror.py tests/test_install_scripts.py -q` -> `31 passed`.
- Green UI verification from `admin-ui`: `npm run lint && npm test -- --run && npm run build` -> lint passed, 22 tests passed, build passed.
- `git diff --check` passed.

## Implementation

- Connected the existing `CapabilitiesScreen` to the hash router and navigation, with keyboard-navigation coverage.
- Added the renderer-required `EmptyObject` schema to the public OpenAPI artifact.
- Updated the OpenAPI contract test to assert the current four-operation global relay surface and its bearer security/job parameter invariants.
