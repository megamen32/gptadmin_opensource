"""Regression coverage for FRP endpoint persistence and the FRP watchdog."""

from pathlib import Path
import subprocess

import cli
import scripts.gptadmin_frp_watchdog as watchdog


ROOT = Path(__file__).resolve().parents[1]


def test_vpn2_default_endpoint_uses_current_control_port() -> None:
    assert "vpn2=vpn2.bezrabotnyi.com:27001" in cli.FRPC_SERVER_ENDPOINTS_DEFAULT
    assert "vpn2=vpn2.bezrabotnyi.com:27000" not in cli.FRPC_SERVER_ENDPOINTS_DEFAULT


def test_frp_watchdog_templates_are_bounded_and_restart_existing_units() -> None:
    script = (ROOT / "scripts/gptadmin_frp_watchdog.py").read_text(encoding="utf-8")
    service = (ROOT / "deploy/systemd/gptadmin-frp-watchdog.service").read_text(encoding="utf-8")
    timer = (ROOT / "deploy/systemd/gptadmin-frp-watchdog.timer").read_text(encoding="utf-8")

    assert 'run("systemctl", "restart", args.unit)' in script
    assert "cooldown" in script
    assert "ExecStart=/usr/local/bin/gptadmin-frp-watchdog" in service
    assert "OnUnitActiveSec=30s" in timer


def test_watchdog_restarts_inactive_unit_and_records_reason(monkeypatch, tmp_path, capsys) -> None:
    calls: list[tuple[str, ...]] = []

    monkeypatch.setattr(watchdog, "active", lambda _unit: False)

    def fake_run(*args: str) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, "", "")

    monkeypatch.setattr(watchdog, "run", fake_run)
    assert watchdog.main(["--unit", "frps.service", "--state", str(tmp_path / "state.json")]) == 0

    assert ("systemctl", "restart", "frps.service") in calls
    assert '"result": "restart_requested"' in capsys.readouterr().out


def test_watchdog_cooldown_suppresses_restart_storm(monkeypatch, tmp_path, capsys) -> None:
    state = tmp_path / "state.json"
    state.write_text('{"last_restart": 9999999999}\n', encoding="utf-8")
    calls: list[tuple[str, ...]] = []
    monkeypatch.setattr(watchdog, "active", lambda _unit: False)
    monkeypatch.setattr(watchdog, "run", lambda *args: calls.append(args) or subprocess.CompletedProcess(args, 0, "", ""))

    assert watchdog.main(["--unit", "frps.service", "--state", str(state)]) == 2
    assert ("systemctl", "restart", "frps.service") not in calls
    assert '"result": "restart_suppressed"' in capsys.readouterr().out
