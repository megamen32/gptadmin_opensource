"""Contract tests for the secret-safe remote deployment runtime probe."""

from __future__ import annotations

import importlib.util
import json
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RUNNER_PATH = ROOT / "tests" / "e2e" / "deployment_runtime.py"
SPEC = importlib.util.spec_from_file_location("deployment_runtime", RUNNER_PATH)
assert SPEC and SPEC.loader
deployment_runtime = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(deployment_runtime)


def test_hub_probe_rejects_router_conflict_and_stale_listener() -> None:
    """Hub readiness requires both a running unit and a real health response."""
    report = deployment_runtime.parse_probe_output(
        "\n".join(
            [
                "unit|gptadmin-hub.service|inactive|dead",
                "unit|gptadmin-tunnel-frpc.service|failed|failed",
                "port|9001|000",
                "router_conflict|true",
            ]
        ),
        kind="hub",
    )

    assert report["status"] == "failed"
    assert "hub_service_not_running" in report["issues"]
    assert "hub_health_failed" in report["issues"]
    assert "tunnel_router_conflict" in report["issues"]


def test_hub_probe_accepts_a_configured_non_default_port() -> None:
    """Hub parsing must follow the reported configured port, not hard-code 9001."""
    report = deployment_runtime.parse_probe_output(
        "\n".join(
            [
                "unit|gptadmin-hub.service|active|running",
                "unit|gptadmin-tunnel-frpc.service|active|running",
                "port|9101|200",
                "router_conflict|false",
            ]
        ),
        kind="hub",
    )

    assert report["status"] == "passed"
    assert report["issues"] == []


def test_hub_probe_rejects_failed_tunnel_even_when_hub_is_healthy() -> None:
    """A Hub/Tunnel deployment is not ready while its Tunnel unit is failed."""
    report = deployment_runtime.parse_probe_output(
        "\n".join(
            [
                "unit|gptadmin-hub.service|active|running",
                "unit|gptadmin-tunnel-frpc.service|failed|failed",
                "port|9001|200",
                "router_conflict|false",
            ]
        ),
        kind="hub",
    )

    assert report["status"] == "failed"
    assert "tunnel_service_not_running" in report["issues"]


def test_hub_probe_anchors_tunnel_conflict_to_current_service_start() -> None:
    """Historical Tunnel journal entries must not fail a current Hub probe."""
    script = deployment_runtime.REMOTE_SCRIPTS["hub"]

    assert "ExecMainStartTimestamp" in script
    assert "journalctl -u gptadmin-tunnel-frpc.service --since" in script
    assert "-n 200" not in script


def test_shellmcp_probe_rejects_legacy_binary_and_queue_auth_failure() -> None:
    """ShellMCP readiness must detect stale binary and queue authentication drift."""
    report = deployment_runtime.parse_probe_output(
        "\n".join(
            [
                "unit|shellmcp.service|active|running",
                "legacy_exec|true",
                "queue_auth|true",
            ]
        ),
        kind="shellmcp",
    )

    assert report["status"] == "failed"
    assert "legacy_shellmcp_binary" in report["issues"]
    assert "queue_auth_failed" in report["issues"]


def test_remote_probe_returns_redacted_json_without_remote_output(monkeypatch) -> None:
    """Remote probe errors and output must not echo command or credential material."""
    calls: list[list[str]] = []

    def fake_run(command, **_kwargs):
        calls.append(command)
        return subprocess.CompletedProcess(
            command,
            0,
            stdout="unit|gptadmin-hub.service|active|running\nunit|gptadmin-tunnel-frpc.service|active|running\nport|9001|200\nrouter_conflict|false\n",
            stderr="remote-token=must-not-return",
        )

    monkeypatch.setattr(deployment_runtime.subprocess, "run", fake_run)
    report = deployment_runtime.run_remote_probe("192.0.2.10", 22104, "roomhacker", "hub")

    assert report["status"] == "passed"
    assert "remote-token" not in json.dumps(report)
    assert "must-not-return" not in json.dumps(report)
    assert calls and "BatchMode=yes" in calls[0]
    assert all("token" not in part.lower() for part in calls[0])
