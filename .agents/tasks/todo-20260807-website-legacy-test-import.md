# Legacy website test import is stale

## Symptom

Root `pytest` collection fails in `website/test_adapters.py` with `ModuleNotFoundError: gptadmin_hub`.

## Smallest evidence

The test inserts the repository root and imports `gptadmin_hub`, but the current repository contains the Go Hub and only historical `gptadmin_hub.py.bak.*` files.

## Blocker / scope

Unselected legacy website-suite maintenance; excluded from the current Go GPTAdmin/FRP release. Main Python suite with `--ignore=website` and both Go suites pass.
