import os
import subprocess
from pathlib import Path

import pytest


def test_mac_codex_update_mcp_e2e():
    if os.environ.get("GPTADMIN_E2E_MAC_CODEX") != "1":
        pytest.skip("set GPTADMIN_E2E_MAC_CODEX=1 on macOS with Codex installed to run")
    repo = Path(__file__).resolve().parents[1]
    subprocess.run([str(repo / "scripts" / "e2e_mac_codex_update_mcp.sh")], cwd=repo, check=True, timeout=900)
