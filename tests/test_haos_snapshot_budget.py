"""Unit checks for the real HAOS entrypoint's configuration-only preamble.

This runs Bash and jq with a temporary options file. It does not claim that an
add-on, Node process, Supervisor update, or failover has been exercised.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
import shlex
import subprocess

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[1]
DEPLOY_TARGET = "home" + "assistant"
if not (ROOT / "deploy" / DEPLOY_TARGET).is_dir():
    DEPLOY_TARGET = "server01"
SOURCE = ROOT / "deploy" / DEPLOY_TARGET / "gptadmin_hub_standby"


def read_budget(options: dict[str, object], tmp_path: Path) -> subprocess.CompletedProcess[str]:
    path = tmp_path / "options.json"
    path.write_text(json.dumps(options), encoding="utf-8")
    original = path.read_bytes()
    script = (SOURCE / "run.sh").read_text(encoding="utf-8")
    # Stop before the first filesystem or service effect in the real entrypoint.
    preamble, separator, _ = script.partition("\nmkdir -p /data/config")
    assert separator
    assert "OPTS=/data/options.json" in preamble
    preamble = preamble.replace("OPTS=/data/options.json", "OPTS=" + shlex.quote(str(path)), 1)
    environment = dict(os.environ)
    environment.pop("GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES", None)
    result = subprocess.run(
        ["bash", "--noprofile", "--norc", "-s"],
        input=preamble + '\nprintf "%s\\n" "${GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES-unset}"\n',
        text=True, capture_output=True, check=False, env=environment,
    )
    assert path.read_bytes() == original
    assert "test-credential-never-print" not in result.stdout + result.stderr
    return result


@pytest.mark.parametrize("include_null", [False, True])
def test_legacy_options_default_to_one_mib(tmp_path: Path, include_null: bool) -> None:
    options: dict[str, object] = {"node_mode": "member", "admin_password": "test-credential-never-print"}
    if include_null:
        options["auth_snapshot_max_bytes"] = None
    result = read_budget(options, tmp_path)
    assert result.returncode == 0, result.stderr
    assert result.stdout.strip() == "1048576"


@pytest.mark.parametrize("budget", [1, 262144, 1048576, 10485760])
def test_explicit_native_budget_is_exported(tmp_path: Path, budget: int) -> None:
    result = read_budget({"auth_snapshot_max_bytes": budget}, tmp_path)
    assert result.returncode == 0, result.stderr
    assert result.stdout.strip() == str(budget)


@pytest.mark.parametrize("budget", [0, -1, 10485761, 1.5, False, True, "1048576", {"value": "test-credential-never-print"}])
def test_invalid_budget_stops_before_services(tmp_path: Path, budget: object) -> None:
    result = read_budget({"auth_snapshot_max_bytes": budget}, tmp_path)
    assert result.returncode != 0
    assert not result.stdout
    assert "auth_snapshot_max_bytes" in result.stderr


@pytest.mark.parametrize("filename", ["public_config.yaml", "config.yaml.template"])
def test_supervisor_schema_allows_existing_options_and_bounds_new_value(filename: str) -> None:
    config = yaml.safe_load((SOURCE / filename).read_text(encoding="utf-8"))
    assert config["options"]["auth_snapshot_max_bytes"] == 1048576
    assert config["schema"]["auth_snapshot_max_bytes"] == "int(1,10485760)?"
