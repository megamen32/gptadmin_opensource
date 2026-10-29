from __future__ import annotations

import shutil
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_sanitizer_preserves_supervisor_api_key_and_redacts_hostname(tmp_path: Path) -> None:
    project = tmp_path / "project"
    scripts = project / "scripts"
    scripts.mkdir(parents=True)
    sanitizer = scripts / "publicize_sanitize.sh"
    shutil.copy2(ROOT / "scripts/publicize_sanitize.sh", sanitizer)

    config = project / "config.yaml"
    private_host = "home" + "assistant.local"
    config.write_text(
        f"server01_api: true\nproxy_host: {private_host}\nprivate_marker: admin-node-100\n",
        encoding="utf-8",
    )

    subprocess.run(
        ["bash", str(sanitizer)],
        check=True,
        capture_output=True,
        text=True,
    )

    sanitized = config.read_text(encoding="utf-8")
    assert "server01_api: true" in sanitized
    assert "proxy_host: server01.local" in sanitized
    assert "private_marker: server01" in sanitized
    assert "__GPTADMIN_KEEP_HA_API_7F02__" not in sanitized
