"""Behavioral tests for generic MCP relay recovery."""

from __future__ import annotations

import importlib.util
from pathlib import Path
from types import ModuleType, SimpleNamespace

import pytest


RELAY_PATH = Path(__file__).parents[1] / "agents" / "generic_stdio_mcp_relay" / "generic_stdio_mcp_relay.py"


def load_relay_module() -> ModuleType:
    """Load the standalone relay without requiring it to be a package."""
    spec = importlib.util.spec_from_file_location("generic_stdio_mcp_relay_test", RELAY_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load relay from {RELAY_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_relay_reregisters_after_poll_transport_failure(monkeypatch: pytest.MonkeyPatch) -> None:
    """A relay must claim its agent ID again after the Hub behind its URL changes."""
    relay_module = load_relay_module()
    calls: list[tuple[str, str]] = []

    def fake_http_json(method: str, url: str, _token: str, data=None, timeout: int = 70):  # noqa: ANN001
        calls.append((method, url))
        if method == "POST" and url.endswith("/mcp-relay/register"):
            return {"ok": True}
        if method == "GET":
            if sum(1 for call_method, _ in calls if call_method == "GET") == 1:
                raise RuntimeError("Hub connection was replaced")
            relay_module.STOP = True
            return {}
        raise AssertionError(f"unexpected request {method} {url} data={data} timeout={timeout}")

    monkeypatch.setattr(relay_module, "http_json", fake_http_json)
    monkeypatch.setattr(relay_module.time, "sleep", lambda _seconds: None)
    relay_module.STOP = False
    client = SimpleNamespace(requested_stdio_format="ndjson", stdio_format="ndjson")
    relay = relay_module.Relay(
        "http://hub.example",
        "relay-token",
        "survivor",
        "Survivor",
        client,
        {"command": "fake-mcp"},
    )

    relay.run()

    registrations = [url for method, url in calls if method == "POST" and url.endswith("/mcp-relay/register")]
    assert len(registrations) == 2


def test_relay_retries_registration_before_polling_after_failed_recovery(monkeypatch: pytest.MonkeyPatch) -> None:
    """A failed recovery registration must not fall back to an unknown-agent poll."""
    relay_module = load_relay_module()
    calls: list[tuple[str, str]] = []
    registration_attempts = 0

    def fake_http_json(method: str, url: str, _token: str, data=None, timeout: int = 70):  # noqa: ANN001
        nonlocal registration_attempts
        calls.append((method, url))
        if method == "POST" and url.endswith("/mcp-relay/register"):
            registration_attempts += 1
            if registration_attempts == 2:
                raise RuntimeError("fallback route is not ready")
            return {"ok": True}
        if method == "GET":
            if sum(1 for call_method, _ in calls if call_method == "GET") == 1:
                raise RuntimeError("primary Hub stopped")
            relay_module.STOP = True
            return {}
        raise AssertionError(f"unexpected request {method} {url} data={data} timeout={timeout}")

    monkeypatch.setattr(relay_module, "http_json", fake_http_json)
    monkeypatch.setattr(relay_module.time, "sleep", lambda _seconds: None)
    relay_module.STOP = False
    client = SimpleNamespace(requested_stdio_format="ndjson", stdio_format="ndjson")
    relay = relay_module.Relay("http://hub.example", "relay-token", "survivor", "Survivor", client, {"command": "fake-mcp"})

    relay.run()

    registration_indices = [index for index, (method, url) in enumerate(calls) if method == "POST" and url.endswith("/mcp-relay/register")]
    assert len(registration_indices) == 3
    assert registration_indices[2] == registration_indices[1] + 1
