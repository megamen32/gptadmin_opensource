"""Executable contract for supported platform/client golden paths."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path


FIXTURE = Path(__file__).parent / "fixtures" / "golden-paths.json"
ROOT = FIXTURE.parents[2]
REQUIRED_STAGES = {"install", "auth", "first_tool", "uninstall_rollback"}


def test_golden_paths_have_complete_platform_and_client_contracts() -> None:
    """Require every supported path to state all four operator actions."""

    matrix = json.loads(FIXTURE.read_text(encoding="utf-8"))
    assert matrix["schema"] == "gptadmin.golden-paths/v1"
    assert set(matrix["clients"]) == {"codex", "claude", "chatgpt"}
    assert {path["platform"] for path in matrix["paths"]} == {"linux", "macos", "windows", "android"}
    for path in matrix["paths"]:
        assert set(path["clients"]) == set(matrix["clients"])
        assert set(path["commands"]) == REQUIRED_STAGES
        for command in path["commands"].values():
            assert command.startswith("python3 -m pytest ")


def test_golden_path_commands_execute_once_per_platform_contract() -> None:
    """Run the declared install/auth/tool/rollback checks without duplication."""

    matrix = json.loads(FIXTURE.read_text(encoding="utf-8"))
    commands = {
        (stage, command)
        for path in matrix["paths"]
        for stage, command in path["commands"].items()
    }
    for stage, command in sorted(commands):
        result = subprocess.run(
            command,
            cwd=ROOT,
            shell=True,
            capture_output=True,
            text=True,
            timeout=300,
        )
        if result.returncode != 0:
            output = (result.stdout + "\n" + result.stderr)[-4000:]
            raise AssertionError(f"{stage} golden-path command failed: {command}\n{output}")
