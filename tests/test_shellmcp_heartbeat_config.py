"""Regression coverage for the opt-in ShellMCP heartbeat policy."""

from __future__ import annotations

import cli


def test_noninteractive_webhook_disables_heartbeat_by_default() -> None:
    """Webhook must not turn on an extra heartbeat unless explicitly requested."""
    env = {"HUB_URL": "https://hub.example.test"}

    cli.configure_shellmcp_transport_noninteractive(env, "webhook")

    assert env["SHELLMCP_TRANSPORT"] == "webhook"
    assert env["SHELLMCP_HEARTBEAT"] == "0"


def test_noninteractive_webhook_can_explicitly_enable_heartbeat() -> None:
    """Operators retain an explicit opt-in for diagnostic heartbeat traffic."""
    env = {"HUB_URL": "https://hub.example.test"}

    cli.configure_shellmcp_transport_noninteractive(env, "webhook", heartbeat=True)

    assert env["SHELLMCP_HEARTBEAT"] == "1"


def test_interactive_webhook_asks_before_enabling_heartbeat(monkeypatch) -> None:
    """An interactive webhook install must make the extra traffic an explicit choice."""
    answers = iter(["2", "n", "https://shell.example.test"])
    monkeypatch.setattr(cli, "ask", lambda *_args: next(answers))
    env = {"HUB_URL": "https://hub.example.test"}

    cli.configure_shellmcp_transport(env, install_hub=False, install_shellmcp=True)

    assert env["SHELLMCP_HEARTBEAT"] == "0"


def test_admin_ui_exposes_heartbeat_as_an_explicit_setting() -> None:
    """The dashboard must expose the same opt-in setting as the CLI."""
    root = cli.Path(__file__).resolve().parents[1]
    html = (root / "public" / "admin" / "index.html").read_text(encoding="utf-8")
    js = (root / "public" / "admin" / "app.js").read_text(encoding="utf-8")

    assert 'id="shellHeartbeatEnabled"' in html
    assert "SHELLMCP_HEARTBEAT" in html
    assert "function setShellHeartbeatFromPanel" in js
    assert "heartbeatInput.checked" in js
