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
    assert "SHELLMCP_HEARTBEAT" not in html
    assert "function setShellHeartbeatFromPanel" in js
    assert "/admin/api/security/heartbeat" in js
    assert "env.shellmcp_heartbeat" in js


def test_homeassistant_runtime_keeps_heartbeat_opt_in() -> None:
    """The add-on launcher must not override its false schema default."""
    root = cli.Path(__file__).resolve().parents[1]
    run_script = (root / "deploy/homeassistant/gptadmin_shellmcp/run.sh").read_text()
    assert 'SHELL_HEARTBEAT="$(opt heartbeat \'false\')"' in run_script


def test_homeassistant_deploy_refreshes_existing_addon_options() -> None:
    """Rebuilds must replace persisted credentials before restarting the add-on."""
    root = cli.Path(__file__).resolve().parents[1]
    deploy_script = (root / "scripts/deploy_haos_shellmcp.sh").read_text(encoding="utf-8")

    options_call = 'api_file POST /addons/local_gptadmin_shellmcp/options "$REMOTE_OPTIONS"'
    assert '"$OUT_DIR/options.json"' in deploy_script
    assert '--data-binary "@$data_file"' in deploy_script
    assert options_call in deploy_script
    assert deploy_script.index(options_call) < deploy_script.index("api POST /addons/local_gptadmin_shellmcp/rebuild")
    assert 'curl -fsS "http://$HAOS_HOST:25900/version"' not in deploy_script
