"""Machine-readable acceptance coverage for the requested platform surfaces."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest


MATRIX_PATH = Path(__file__).parent / "fixtures" / "completion-matrix.json"
REPOSITORY_ROOT = MATRIX_PATH.parents[2]
REQUIRED_SURFACES = {
    "proxy",
    "endpoints",
    "hooks",
    "mcp_forwarding",
    "operations",
    "file_sharing",
    "golden_paths",
    "integration_certification",
    "profiles",
    "release_provenance",
    "security_policies",
}


def test_completion_matrix_covers_every_requested_surface() -> None:
    """Require every user-facing acceptance surface to have executable checks."""

    matrix = json.loads(MATRIX_PATH.read_text(encoding="utf-8"))
    assert set(matrix) == REQUIRED_SURFACES

    for surface, checks in matrix.items():
        assert checks, f"surface {surface!r} has no acceptance checks"
        for check in checks:
            assert check["id"]
            assert check["command"]
            assert check["evidence"]


@pytest.mark.parametrize("surface", sorted(REQUIRED_SURFACES))
def test_completion_matrix_commands_execute(surface: str) -> None:
    """Run every executable acceptance command for one requested surface."""

    matrix = json.loads(MATRIX_PATH.read_text(encoding="utf-8"))
    for check in matrix[surface]:
        result = subprocess.run(
            check["command"],
            cwd=REPOSITORY_ROOT,
            shell=True,
            capture_output=True,
            text=True,
            timeout=300,
        )
        if result.returncode != 0:
            output = (result.stdout + "\n" + result.stderr)[-4000:]
            pytest.fail(f"{surface}/{check['id']} failed with {result.returncode}:\n{output}")
