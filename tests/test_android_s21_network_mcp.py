"""Contract tests for the ADB-free S21 network MCP registration."""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "deploy" / "android-s21-network-mcp.sh"
SERVICE = ROOT / "deploy" / "systemd" / "android-s21-network-mcp-tunnel.service"


def test_tunnel_is_network_native_and_boot_persistent() -> None:
    unit = SERVICE.read_text(encoding="utf-8")

    assert "User=admin" in unit
    assert "127.0.0.1:18080:127.0.0.1:8080" in unit
    assert "-p 22220 127.0.0.1" in unit
    assert "ExitOnForwardFailure=yes" in unit
    assert "ServerAliveInterval=30" in unit
    assert "Restart=always" in unit
    assert "WantedBy=multi-user.target" in unit
    assert "adb" not in unit.lower()
    assert "5555" not in unit
    assert "22221" not in unit


def _fake_systemctl(tmp_path: Path) -> tuple[Path, Path]:
    calls = tmp_path / "systemctl.calls"
    executable = tmp_path / "systemctl"
    executable.write_text(
        "#!/usr/bin/env bash\nset -eu\nprintf '%s\\n' \"$*\" >>\"$SYSTEMCTL_CALLS\"\n",
        encoding="utf-8",
    )
    executable.chmod(0o755)
    return executable, calls


def _run_installer(tmp_path: Path, action: str = "apply") -> subprocess.CompletedProcess[str]:
    systemctl, calls = _fake_systemctl(tmp_path)
    config = tmp_path / "mcp-supervisor.json"
    if not config.exists():
        config.write_text(
            json.dumps([{"ref": "KeepMe", "name": "unrelated", "enabled": True}]) + "\n",
            encoding="utf-8",
        )
    token = tmp_path / "android-s21-mcp.env"
    token.write_text("ANDROID_S21_MCP_TOKEN=" + "a" * 64 + "\n", encoding="utf-8")
    env = {
        **os.environ,
        "S21_MCP_HUB_CONFIG": str(config),
        "S21_MCP_TOKEN_FILE": str(token),
        "S21_MCP_SYSTEMD_DIR": str(tmp_path / "systemd"),
        "S21_MCP_HUB_DROPIN_DIR": str(tmp_path / "dropin"),
        "S21_MCP_SYSTEMCTL": str(systemctl),
        "SYSTEMCTL_CALLS": str(calls),
    }
    return subprocess.run(
        [str(INSTALLER), action], env=env, text=True, capture_output=True, check=False
    )


def test_apply_registers_one_dedicated_child_without_copying_the_secret(tmp_path: Path) -> None:
    first = _run_installer(tmp_path)
    second = _run_installer(tmp_path)

    assert first.returncode == second.returncode == 0, first.stderr + second.stderr
    config_path = tmp_path / "mcp-supervisor.json"
    agents = json.loads(config_path.read_text(encoding="utf-8"))
    assert [agent["ref"] for agent in agents].count("S21NetworkAgentDevice") == 1
    assert any(agent["ref"] == "KeepMe" for agent in agents)
    child = next(agent for agent in agents if agent["ref"] == "S21NetworkAgentDevice")
    assert child["transport"] == "streamable-http"
    assert child["url"] == "http://127.0.0.1:18080/mcp"
    assert child["headers"]["Authorization"] == "Bearer ${ANDROID_S21_MCP_TOKEN}"
    assert "a" * 64 not in config_path.read_text(encoding="utf-8")
    assert "a" * 64 not in first.stdout + first.stderr
    dropin = (tmp_path / "dropin" / "90-s21-network-mcp.conf").read_text(encoding="utf-8")
    assert f"EnvironmentFile={tmp_path / 'android-s21-mcp.env'}" in dropin


def test_apply_installs_and_starts_the_tunnel_before_restarting_hub(tmp_path: Path) -> None:
    result = _run_installer(tmp_path)

    assert result.returncode == 0, result.stderr
    installed = tmp_path / "systemd" / SERVICE.name
    assert installed.read_text(encoding="utf-8") == SERVICE.read_text(encoding="utf-8")
    calls = (tmp_path / "systemctl.calls").read_text(encoding="utf-8").splitlines()
    assert calls.index("enable --now android-s21-network-mcp-tunnel.service") < calls.index(
        "restart gptadmin-hub.service"
    )


def test_rollback_restores_config_and_removes_runtime_files(tmp_path: Path) -> None:
    assert _run_installer(tmp_path).returncode == 0
    result = _run_installer(tmp_path, "rollback")

    assert result.returncode == 0, result.stderr
    agents = json.loads((tmp_path / "mcp-supervisor.json").read_text(encoding="utf-8"))
    assert [agent["ref"] for agent in agents] == ["KeepMe"]
    assert not (tmp_path / "systemd" / SERVICE.name).exists()
    assert not (tmp_path / "dropin" / "90-s21-network-mcp.conf").exists()
