"""End-to-end coverage for the external installer-link verifier CLI."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
VERIFIER = ROOT / "tools" / "verify_installer_links.py"
INSTALLER = ROOT / "deploy" / "install.sh"


def test_verifier_runs_install_bootstrap_and_fetches_platform_artifacts() -> None:
    """Verify the checker runs the installer over HTTP for two platform targets."""
    result = subprocess.run(
        [
            sys.executable,
            str(VERIFIER),
            "--installer",
            str(INSTALLER),
            "--target",
            "linux/amd64",
            "--target",
            "darwin/arm64",
            "--android",
            "--json",
        ],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )
    assert result.returncode == 0, result.stderr
    report = json.loads(result.stdout)
    assert report["ok"] is True
    assert [run["target"] for run in report["runs"]] == ["linux/amd64", "darwin/arm64", "android/arm64"]
    expected = {
        "linux/amd64": "/releases/gptadmin-ubuntu-x64-full.tar.gz",
        "darwin/arm64": "/releases/gptadmin-macos-arm64-full.tar.gz",
        "android/arm64": "/releases/gptadmin-android-arm64-client.tar.gz",
    }
    for run in report["runs"]:
        assert expected[run["target"]] in run["requests"]
        if run["target"] == "android/arm64":
            assert run["cli_args"] == []
        else:
            assert "/gptadmin.py" in run["requests"]
