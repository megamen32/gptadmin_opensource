"""Regression coverage for fail-closed CLI installation semantics."""

from __future__ import annotations

from pathlib import Path
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


def test_setup_requires_real_local_execution_before_reporting_success() -> None:
    """Fresh bundled installs must prove actual Hub -> ShellMCP execution."""

    source = inspect.getsource(cli.setup_interactive)
    approved = source.index("maybe_autoapprove_local_shellmcp(env, install_hub, install_shellmcp)")
    executed = source.index("_require_real_local_shell_exec(env_read(), timeout_s=90)")
    configured = source.index("auto_configure_ai_mcp_clients(env_read(), install_hub)")
    assert approved < executed < configured


def test_grepmesh_is_default_on_with_explicit_opt_out() -> None:
    from types import SimpleNamespace

    default_args = SimpleNamespace(grepmesh=False, no_grepmesh=False)
    disabled_args = SimpleNamespace(grepmesh=False, no_grepmesh=True)
    assert cli._grepmesh_enabled_from_args(default_args, {}, True) is True
    assert cli._grepmesh_enabled_from_args(disabled_args, {}, True) is False
    assert cli._grepmesh_enabled_from_args(default_args, {}, False) is False


def test_setup_wires_grepmesh_before_shellmcp_start() -> None:
    source = inspect.getsource(cli.setup_interactive)
    configured = source.index("_configure_builtin_grepmesh")
    grep_started = source.index("svc_enable_start(svc_grepmesh_name(), UNIT_PATH_GREPMESH)")
    shell_started = source.index("svc_enable_start(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)")
    assert configured < grep_started < shell_started


def test_builtin_grepmesh_system_permissions_allow_operator_traverse_without_env_read(monkeypatch, tmp_path):
    from types import SimpleNamespace

    config = tmp_path / "etc" / "gptadmin" / "grepmesh.json"
    mcp_config = {"mcpServers": {}}
    chmod_calls = []
    chown_calls = []

    monkeypatch.setattr(cli, "IS_USER_INSTALL", False)
    monkeypatch.setattr(cli, "BIN_DIR", tmp_path / "bin")
    monkeypatch.setattr(cli, "GREPMESH_CONFIG_FILE", config)
    monkeypatch.setattr(cli, "_mcp_config", lambda: mcp_config)
    monkeypatch.setattr(cli, "_mcp_save", lambda _cfg: None)
    monkeypatch.setattr(cli, "_mcp_refresh_generated_configs", lambda _cfg: None)
    monkeypatch.setattr(cli.pwd, "getpwnam", lambda _name: SimpleNamespace(pw_gid=1234))
    monkeypatch.setattr(cli.os, "chmod", lambda path, mode: chmod_calls.append((Path(path), mode)))
    monkeypatch.setattr(cli.os, "chown", lambda path, uid, gid: chown_calls.append((Path(path), uid, gid)))
    (tmp_path / "bin").mkdir()
    (tmp_path / "bin" / "grepmesh-mcp").write_text("binary")

    assert cli._configure_builtin_grepmesh({"SHELLMCP_DEFAULT_USER": "operator"}, True) is True
    assert (config.parent, 0o710) in chmod_calls
    assert (config, 0o640) in chmod_calls
    assert (config.parent, 0, 1234) in chown_calls
    assert (config, 0, 1234) in chown_calls


def test_builtin_grepmesh_preserves_lowercase_operator_definition(monkeypatch, tmp_path):
    operator = {
        "command": "/usr/local/bin/npx",
        "args": ["-y", "mcp-remote", "http://127.0.0.1:9419/mcp"],
        "url": "http://127.0.0.1:9419/mcp",
        "enabled": True,
        "agent_id": "GrepMesh",
    }
    cfg = {"mcpServers": {"grepmesh": dict(operator)}}
    saved = []
    monkeypatch.setattr(cli, "_mcp_config", lambda: cfg)
    monkeypatch.setattr(cli, "_mcp_save", lambda value: saved.append(value))
    monkeypatch.setattr(cli, "BIN_DIR", tmp_path / "bin")

    assert cli._configure_builtin_grepmesh({"SHELLMCP_DEFAULT_USER": "operator"}, True) is False
    assert cfg["mcpServers"] == {"grepmesh": operator}
    assert saved == []


def test_setup_stops_bundled_grepmesh_when_operator_provider_is_selected() -> None:
    source = inspect.getsource(cli.setup_interactive)
    assert "elif UNIT_PATH_GREPMESH.exists():" in source
    assert "svc_disable_stop(svc_grepmesh_name(), UNIT_PATH_GREPMESH)" in source
