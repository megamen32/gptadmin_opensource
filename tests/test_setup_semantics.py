"""Regression coverage for fail-closed CLI installation semantics."""

from __future__ import annotations

import inspect

import pytest

import cli


def test_setup_health_gate_fails_closed_when_hub_never_becomes_healthy(monkeypatch: pytest.MonkeyPatch) -> None:
    """A setup must abort instead of reporting success for an unhealthy Hub."""

    monkeypatch.setattr(cli, "wait_local_hub_health", lambda *_args, **_kwargs: False)

    with pytest.raises(RuntimeError, match="local Hub health check failed during setup"):
        cli._require_local_hub_health({"HUB_URL": "http://127.0.0.1:9001"})


def test_setup_uses_the_fatal_health_gate_after_starting_hub() -> None:
    """Keep the setup integration wired to the shared fail-closed helper."""

    source = inspect.getsource(cli.setup_interactive)
    assert "_require_local_hub_health(env)" in source
