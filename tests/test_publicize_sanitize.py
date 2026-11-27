from __future__ import annotations

import re
import shutil
import subprocess
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]


def test_sanitizer_preserves_supervisor_api_key_and_redacts_hostname(tmp_path: Path) -> None:
    if not (ROOT / ".gitpublic" / "replace").exists():
        pytest.skip("private replacement rules are absent from the sanitized public source")
    project = tmp_path / "project"
    scripts = project / "scripts"
    scripts.mkdir(parents=True)
    sanitizer = scripts / "publicize_sanitize.sh"
    shutil.copy2(ROOT / "scripts/publicize_sanitize.sh", sanitizer)

    config = project / "config.yaml"
    private_host = "home" + "assistant.local"
    config.write_text(
        f"homeassistant_api: true\nproxy_host: {private_host}\nprivate_marker: admin-node-100\n",
        encoding="utf-8",
    )

    subprocess.run(
        ["bash", str(sanitizer)],
        check=True,
        capture_output=True,
        text=True,
    )

    sanitized = config.read_text(encoding="utf-8")
    assert "homeassistant_api: true" in sanitized
    assert "proxy_host: server01.local" in sanitized
    assert "private_marker: server01" in sanitized
    assert "__GPTADMIN_KEEP_HA_API_7F02__" not in sanitized


def test_public_mirror_replacement_preserves_api_identifier() -> None:
    rules = ROOT / ".gitpublic" / "replace"
    if not rules.exists():
        pytest.skip("private public-mirror replacement rules are not included in this checkout")

    rule = next(
        line
        for line in rules.read_text(encoding="utf-8").splitlines()
        if line.startswith("regex:") and "server01" in line
    )
    pattern, replacement = rule.split("==>", 1)
    pattern = pattern.removeprefix("regex:").strip()
    replacement = replacement.strip()

    sample = "homeassistant_api: true\nhost: " + "home" + "assistant.local\n"
    result = re.sub(pattern, replacement, sample)
    assert "homeassistant_api: true" in result
    assert "host: server01.local" in result
