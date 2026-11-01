"""Local policy tests: no service or release acceptance is inferred."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone

import pytest

from deploy.server01.gptadmin_hub_standby.gptadmin_failover_config import build_failover_config
from scripts import gptadmin_failover_watchdog as watchdog


def options(value):
    return {"failover": {
        "node_id": "standby", "primary_health_url": "http://primary.invalid/healthz",
        "primary_public_url": "https://hub.invalid", "maintenance_until": value,
    }}


@pytest.mark.parametrize("value", [None, "", "2026-11-01T12:30:00+03:00", "2026-11-01T09:30:00Z", "9999-12-31T23:59:59+00:00"])
def test_deadline_is_optional_or_a_finite_zoned_timestamp(value):
    config = build_failover_config(options(value))
    assert config["maintenance_until"] == (value or "")
    actual = watchdog.maintenance_deadline(config["maintenance_until"])
    assert (actual is None) == (value is None or value == "")
    if value and value.startswith("2026"):
        assert actual == datetime(2026, 11, 1, 9, 30, tzinfo=timezone.utc).timestamp()


@pytest.mark.parametrize("value", [True, False, 0, 123, [], {}, "forever", "Infinity", "2026-11-01", "2026-11-01T12:30:00", "2026-02-30T12:30:00Z"])
def test_invalid_deadlines_fail_closed_without_echoing_input(value):
    with pytest.raises(ValueError, match="ISO-8601 timestamp with a timezone"):
        build_failover_config(options(value))
    with pytest.raises(ValueError, match="ISO-8601 timestamp with a timezone"):
        watchdog.maintenance_deadline(value)


@pytest.fixture
def policy(tmp_path, monkeypatch):
    now = datetime(2026, 11, 1, 9, 0, tzinfo=timezone.utc).timestamp()
    monkeypatch.setattr(watchdog.time, "time", lambda: now)
    args = argparse.Namespace(
        config=str(tmp_path / "config.json"), state=str(tmp_path / "state.json"),
        runtime_state=str(tmp_path / "runtime.json"), node_id="standby", dry_run=True,
        reclaim_command_file=str(tmp_path / "reclaim.json"), frpc_service="none",
        frpc_config=str(tmp_path / "frpc.toml"),
    )
    watchdog.save_json(args.config, build_failover_config(options("2026-11-01T12:30:00+03:00")))
    watchdog.save_json(args.runtime_state, {"fail_count": 8, "last_promotion_at": now - 1000})
    return args, now


def test_active_window_clears_failures_and_skips_health_and_promotion(policy, monkeypatch):
    args, _ = policy
    def unexpected(*_args, **_kwargs):
        pytest.fail("maintenance must not reach health or promotion")
    monkeypatch.setattr(watchdog, "http_ok", unexpected)
    monkeypatch.setattr(watchdog, "promote", unexpected)
    assert watchdog.check_once(args) == 0
    runtime = watchdog.load_json(args.runtime_state, {})
    assert runtime["fail_count"] == 0
    assert runtime["last_decision"] == "maintenance"


@pytest.mark.parametrize("offset", [1800, 1801])
def test_expiry_resumes_normal_checks_with_fresh_failure_count(policy, monkeypatch, offset):
    args, now = policy
    assert watchdog.check_once(args) == 0
    monkeypatch.setattr(watchdog.time, "time", lambda: now + offset)
    checks = []
    def unhealthy(url, timeout):
        checks.append(url)
        return False
    monkeypatch.setattr(watchdog, "http_ok", unhealthy)
    assert watchdog.check_once(args) == 0
    runtime = watchdog.load_json(args.runtime_state, {})
    assert runtime["fail_count"] == 1
    assert runtime["last_decision"] == "waiting_rank_threshold"
    assert checks == ["http://primary.invalid/healthz"]


@pytest.mark.parametrize("deadline", ["2026-11-01T12:30:00+03:00", "invalid"])
def test_verified_reclaim_takes_priority_over_maintenance(policy, monkeypatch, deadline):
    args, now = policy
    config = watchdog.load_json(args.config, {})
    config["maintenance_until"] = deadline
    watchdog.save_json(args.config, config)
    monkeypatch.setenv("MCP_BRIDGE_KEY", "unit-test-only-key")
    payload = {
        "action": "demote", "node_id": args.node_id, "nonce": "maintenance-unit",
        "issued_at": int(now), "expires_at": int(now) + 60,
        "primary_health_url": config["primary_health_url"],
    }
    text = watchdog.reclaim_sig_input("demote", args.node_id, payload["nonce"], payload["issued_at"], payload["expires_at"], payload["primary_health_url"])
    payload["signature"] = watchdog.sign_reclaim("unit-test-only-key", text)
    watchdog.save_json(args.reclaim_command_file, payload)
    assert watchdog.check_once(args) == 0
    runtime = watchdog.load_json(args.runtime_state, {})
    assert runtime["last_decision"] == "reclaimed_primary"
    assert runtime["fail_count"] == 0
    assert runtime["last_promotion_at"] == 0
    assert runtime["reclaim_nonces"] == ["maintenance-unit"]


def test_invalid_runtime_deadline_blocks_health_and_promotion(policy, monkeypatch):
    args, _ = policy
    config = watchdog.load_json(args.config, {})
    config["maintenance_until"] = "forever"
    watchdog.save_json(args.config, config)
    def unexpected(*_args, **_kwargs):
        pytest.fail("invalid maintenance must not reach health or promotion")
    monkeypatch.setattr(watchdog, "http_ok", unexpected)
    monkeypatch.setattr(watchdog, "promote", unexpected)
    with pytest.raises(SystemExit, match="ISO-8601 timestamp with a timezone"):
        watchdog.check_once(args)


def test_public_schema_exposes_optional_maintenance_deadline():
    from pathlib import Path
    schema = (Path(__file__).resolve().parents[1] / "deploy/server01/gptadmin_hub_standby/public_config.yaml").read_text()
    assert 'maintenance_until: ""' in schema
    assert "maintenance_until: str?" in schema
