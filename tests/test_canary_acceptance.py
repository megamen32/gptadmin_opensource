"""Process-level canary, reconnect and rollback acceptance."""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RUNNER_PATH = ROOT / "tests" / "e2e" / "canary_acceptance.py"
SPEC = importlib.util.spec_from_file_location("canary_acceptance", RUNNER_PATH)
assert SPEC and SPEC.loader
canary_acceptance = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = canary_acceptance
SPEC.loader.exec_module(canary_acceptance)


def test_disposable_canary_reconnects_and_rolls_back_real_hub_processes() -> None:
    """A real Hub process must survive candidate swap and bad-candidate rollback."""

    result = canary_acceptance.run_disposable_canary()
    assert result == {
        "status": "passed",
        "old_version": "canary-old",
        "new_version": "canary-new",
        "reconnected": True,
        "rollback": True,
    }
    assert "ctl" not in json.dumps(result)
