from __future__ import annotations

import importlib.util
import os
import subprocess
from pathlib import Path

import pytest


MODULE_PATH = Path(__file__).with_name("health_incident_production_preflight.py")
SPEC = importlib.util.spec_from_file_location("health_incident_production_preflight", MODULE_PATH)
assert SPEC and SPEC.loader
preflight = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(preflight)


def test_readiness_requires_live_health_route_active_stack_and_collector_runtime() -> None:
    units = {
        name: {"ActiveState": "active"}
        for name in ("notification-center", "gptadmin-hub", "agent-herder", "opencode", "omniroute", "hermes")
    }
    endpoints = {
        "noticeplace_health": {"status": 404},
        "opencode_health": {"status": 401},
        "omniroute_health": {"status": 200},
    }
    blockers = preflight.readiness_blockers(
        {
            "noticeplace_live_health_workflow": True,
            "agent_herder_live_progress_route": True,
            "health_source_producer": True,
            "health_source_fleet_adapter": True,
            "health_source_fleet_config": True,
        },
        units,
        endpoints,
        {"collector_installed": False},
    )
    assert "live NoticePlace health endpoint is not HTTP 200" in blockers
    assert "OpenCode authenticated readiness is not proven" in blockers
    assert "health collector runtime is not installed with an active timer/config" in blockers


def test_readiness_distinguishes_health_route_auth_from_missing_route() -> None:
    units = {name: {"ActiveState": "active"} for name in ("notification-center", "gptadmin-hub", "agent-herder", "opencode", "omniroute", "hermes")}
    blockers = preflight.readiness_blockers(
        {
            "noticeplace_live_health_workflow": True,
            "agent_herder_live_progress_route": True,
            "health_source_producer": True,
            "health_source_fleet_adapter": True,
            "health_source_fleet_config": True,
        },
        units,
        {"noticeplace_health": {"status": 401}, "noticeplace_health_authenticated": {"status": 401}, "agent_herder_health_route": {"status": 405}, "opencode_health": {"status": 200}, "omniroute_health": {"status": 200}},
        {"collector_installed": True},
    )
    assert "NoticePlace health route exists but authenticated readiness is not proven" in blockers
    assert "live NoticePlace health endpoint is not HTTP 200" not in blockers


def test_preflight_diagnostic_helpers_do_not_return_raw_body_or_stderr(monkeypatch: pytest.MonkeyPatch) -> None:
    class Response:
        status = 200
        headers = {"content-type": "application/json"}

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def read(self, _limit):
            return b'{"token":"private"}'

    monkeypatch.setattr(preflight, "_direct_open", lambda *_args, **_kwargs: Response())
    result = preflight._http_probe("http://127.0.0.1:1")
    assert "body_prefix" not in result
    assert "private" not in str(result)


def test_runtime_config_readability_is_checked_for_the_declared_group(tmp_path: Path) -> None:
    config = tmp_path / "health.json"
    config.write_text("{}", encoding="utf-8")
    config.chmod(0o600)
    assert preflight.runtime_config_readable(config, "group-that-does-not-exist") is False


def test_secret_file_safety_checks_metadata_without_reading_content(tmp_path: Path) -> None:
    secret = tmp_path / "health.env"
    secret.write_text("TOKEN=not-read\n", encoding="utf-8")
    secret.chmod(0o600)
    assert preflight.runtime_secret_file_safe(secret) is False


def test_systemd_probe_failure_is_bounded_and_not_raised(monkeypatch: pytest.MonkeyPatch) -> None:
    def fail(*_args, **_kwargs):
        raise subprocess.TimeoutExpired("systemctl", 5)

    monkeypatch.setattr(preflight.subprocess, "run", fail)
    result = preflight._systemd_show("missing.service")
    assert result["probe_error"] == "systemd_probe_failed"


def test_authenticated_probe_returns_metadata_without_credential(monkeypatch: pytest.MonkeyPatch) -> None:
    class Response:
        status = 200
        headers = {"content-type": "application/json"}

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def read(self, _limit):
            return b'{"status":"ok"}'

    captured: dict[str, str] = {}

    def fake_urlopen(request, **_kwargs):
        captured["authorization"] = request.headers.get("Authorization", "")
        return Response()

    monkeypatch.setattr(preflight, "_direct_open", fake_urlopen)
    result = preflight._authenticated_http_probe("http://127.0.0.1:8091/health", "private-token")
    assert result == {"status": 200, "content_type": "application/json", "body_bytes": 15}
    assert captured["authorization"] == "Bearer private-token"
    assert "private-token" not in str(result)


def test_live_artifact_probe_accepts_health_remediation_route_in_web_bundle() -> None:
    assert preflight.live_herder_route_present("url.pathname === '/api/health/remediation'") is True
    assert preflight.live_herder_route_present("useful_progress: true") is True
    assert preflight.live_herder_route_present("unrelated route") is False


def test_service_run_success_requires_a_real_start_timestamp() -> None:
    base = {"LoadState": "loaded", "Result": "success", "ExecMainStatus": "0"}
    now_us = preflight.time.monotonic_ns() // 1_000
    assert preflight._service_run_succeeded({**base, "ExecMainStartTimestamp": "Sun 2026-08-09 18:00:00 MSK", "ExecMainStartTimestampMonotonic": str(now_us)}) is True
    assert preflight._service_run_succeeded({**base, "ExecMainStartTimestamp": "-", "ExecMainStartTimestampMonotonic": str(now_us)}) is False
    assert preflight._service_run_succeeded({**base, "ExecMainStartTimestamp": "Sun 2026-08-09 18:00:00 MSK", "ExecMainStartTimestampMonotonic": str(now_us - 181_000_000)}) is False
