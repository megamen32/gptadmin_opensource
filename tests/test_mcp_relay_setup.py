"""Regression tests for standalone MCP relay installation."""

from __future__ import annotations

import argparse

import pytest

import cli


def test_mcp_token_file_uses_target_hub_relay_token(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """A shell-only host must not authenticate its relays with its local CTL key."""
    token_file = tmp_path / "mcp-relay.token"
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)
    monkeypatch.setattr(cli, "env_read", lambda: {"CTL_TOKEN": "local-admin-key", "MCP_RELAY_AGENT_TOKEN": "hub-relay-key"})

    cli._mcp_ensure_token_file()

    assert token_file.read_text(encoding="utf-8") == "hub-relay-key\n"


def test_mcp_add_rejects_an_option_as_the_executable(monkeypatch: pytest.MonkeyPatch, tmp_path, capsys: pytest.CaptureFixture[str]) -> None:
    """Avoid persisting a broken command from `mcp add npx -y ...`."""
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", tmp_path / "mcp.json")
    args = argparse.Namespace(
        name="npx",
        command="-y",
        args=["chrome-devtools-mcp@latest"],
        url=None,
        stdio_format=None,
        cwd=None,
        env=[],
        disabled=False,
        force=False,
        install=False,
        status=False,
        agent_id=None,
        run_as_user=None,
        hub_url=None,
    )

    with pytest.raises(SystemExit):
        cli.cmd_mcp_add(args)

    assert "invalid MCP command" in capsys.readouterr().err
    assert not (tmp_path / "mcp.json").exists()


def test_mcp_add_install_starts_and_checks_the_relay(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """`mcp add --install --status` must create a live relay, not just JSON."""
    config_file = tmp_path / "mcp.json"
    agents_dir = tmp_path / "mcp-agents.d"
    token_file = tmp_path / "mcp-relay.token"
    commands: list[tuple[str, bool]] = []
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", config_file)
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", agents_dir)
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "hub-relay-key"})
    monkeypatch.setattr(cli, "_mcp_manager_cmd", lambda action, _path: ["manager", action])
    monkeypatch.setattr(cli, "run", lambda command, check=True: commands.append((command[-1], check)))
    args = argparse.Namespace(
        name="chrome-mac",
        command="npx",
        args=["-y", "chrome-devtools-mcp@latest", "--autoConnect"],
        url=None,
        stdio_format=None,
        cwd=None,
        env=[],
        disabled=False,
        force=False,
        install=True,
        status=True,
        agent_id=None,
        run_as_user=None,
        hub_url=None,
    )

    cli.cmd_mcp_add(args)

    saved = cli._json_read(config_file, {})["mcpServers"]["chrome-mac"]
    assert saved["command"] == "npx"
    assert saved["args"] == ["-y", "chrome-devtools-mcp@latest", "--autoConnect"]
    assert token_file.read_text(encoding="utf-8") == "hub-relay-key\n"
    assert commands == [("install", True), ("status", False)]


def test_mcp_add_does_not_install_a_duplicate_standalone_relay_when_supervised(
    monkeypatch: pytest.MonkeyPatch, tmp_path, capsys: pytest.CaptureFixture[str]
) -> None:
    """A ShellMCP-owned relay must not also receive a legacy service unit."""
    commands: list[list[str]] = []
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", tmp_path / "mcp.json")
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", tmp_path / "mcp-agents.d")
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", tmp_path / "mcp-relay.token")
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "token", "SHELLMCP_MCP_CONFIG": "/etc/gptadmin/mcp-supervisor.json"})
    monkeypatch.setattr(cli, "run", lambda command, check=True: commands.append(command))
    args = argparse.Namespace(
        name="supervised",
        command="npx",
        args=["-y", "example-mcp"],
        url=None,
        stdio_format=None,
        cwd=None,
        env=[],
        disabled=False,
        force=False,
        install=True,
        status=False,
        agent_id=None,
        run_as_user=None,
        hub_url=None,
    )

    cli.cmd_mcp_add(args)

    assert commands == []
    assert "ShellMCP supervisor will manage MCP server supervised" in capsys.readouterr().out


def test_installed_go_shellmcp_enables_supervisor_without_legacy_env(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Upgraded hosts must not need an old env flag to avoid Python relays."""
    binary = tmp_path / "bin" / "shellmcp"
    binary.parent.mkdir(parents=True)
    binary.write_bytes(b"\x7fELF" + b"\0" * 64)
    monkeypatch.setattr(cli, "BIN_DIR", binary.parent)
    monkeypatch.setattr(cli, "env_read", lambda: {})

    assert cli._mcp_go_supervisor_enabled() is True


def test_mcp_add_disabled_install_does_not_start_a_legacy_relay(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """The disabled flag applies equally to inline standalone installation."""
    commands: list[list[str]] = []
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", tmp_path / "mcp.json")
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", tmp_path / "mcp-agents.d")
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", tmp_path / "mcp-relay.token")
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "token"})
    monkeypatch.setattr(cli, "run", lambda command, check=True: commands.append(command))
    args = argparse.Namespace(
        name="disabled",
        command="npx",
        args=["-y", "example-mcp"],
        url=None,
        stdio_format=None,
        cwd=None,
        env=[],
        disabled=True,
        force=False,
        install=True,
        status=False,
        agent_id=None,
        run_as_user=None,
        hub_url=None,
    )

    cli.cmd_mcp_add(args)

    assert commands == []
    assert cli._json_read(tmp_path / "mcp.json", {})["mcpServers"]["disabled"]["enabled"] is False


def test_mcp_import_refreshes_the_shellmcp_supervisor_registry(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Imported MCP definitions must be visible to ShellMCP after restart."""
    source = tmp_path / "claude.json"
    source.write_text('{"mcpServers":{"imported":{"command":"npx","args":["-y","example-mcp"]}}}', encoding="utf-8")
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", tmp_path / "mcp.json")
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", tmp_path / "mcp-agents.d")
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", tmp_path / "mcp-relay.token")
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "token"})

    cli.cmd_mcp_import(argparse.Namespace(format="claude", user=None, path=str(source), force=False))

    registry = cli._json_read(tmp_path / "mcp-supervisor.json", [])
    assert [agent["ref"] for agent in registry] == [cli._mcp_agent_id("imported", {})]


def test_go_supervisor_runs_the_real_mcp_child_without_python_relay(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Go ShellMCP must own the configured child instead of nesting the legacy relay."""
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    cfg = {
        "mcpServers": {
            "direct": {
                "command": "npx",
                "args": ["-y", "example-mcp"],
                "env": {"API_TOKEN": "${EXAMPLE_TOKEN}"},
                "cwd": "/workspace",
                "enabled": True,
            }
        }
    }

    cli._mcp_sync_go_supervisor_config(cfg)

    [agent] = cli._json_read(tmp_path / "mcp-supervisor.json", [])
    assert agent["command"] == "npx"
    assert agent["args"] == ["-y", "example-mcp"]
    assert agent["env"] == {"API_TOKEN": "${EXAMPLE_TOKEN}"}
    assert agent["cwd"] == "/workspace"
    assert "generic_stdio_mcp_relay" not in " ".join([agent["command"], *agent["args"]])


def test_supervised_install_retires_legacy_relays_and_restarts_shellmcp(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Migration must stop duplicate Python relay units before Go reloads the registry."""
    config = {"mcpServers": {"direct": {"command": "npx", "args": ["-y", "example-mcp"]}}}
    commands: list[tuple[list[str], bool]] = []
    restarts: list[tuple[str, object]] = []
    env_updates: list[dict[str, str]] = []
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", tmp_path / "mcp.json")
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", tmp_path / "mcp-agents.d")
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", tmp_path / "mcp-relay.token")
    monkeypatch.setattr(cli, "SYSTEMD_DIR", tmp_path / "systemd")
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "token", "SHELLMCP_MCP_CONFIG": str(tmp_path / "mcp-supervisor.json")})
    monkeypatch.setattr(cli, "_mcp_config", lambda: config)
    monkeypatch.setattr(cli, "_mcp_manager_exists", lambda: True)
    monkeypatch.setattr(cli, "_mcp_manager_cmd", lambda action, path, backend=None: ["manager", action, str(path)])
    monkeypatch.setattr(cli, "run", lambda command, check=True: commands.append((command, check)))
    monkeypatch.setattr(cli, "svc_restart", lambda name, path: restarts.append((name, path)))
    monkeypatch.setattr(cli, "env_set_many", lambda values: env_updates.append(values))

    cli.cmd_mcp_install(argparse.Namespace(name=None, backend=None))

    assert commands == [(["manager", "uninstall", str(tmp_path / "mcp-agents.d/direct.json")], False)]
    assert env_updates == [{"SHELLMCP_MCP_CONFIG": str(tmp_path / "mcp-supervisor.json")}]
    assert restarts == [(cli.svc_shellmcp_name(), cli.UNIT_PATH_SHELLMCP)]


def test_supervised_macos_install_persists_an_ordinary_default_user(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """A root LaunchDaemon migration must retain the invoking Mac account for normal calls."""
    env = {"SHELLMCP_MCP_CONFIG": str(tmp_path / "old.json")}
    updates: list[dict[str, str]] = []
    monkeypatch.setattr(cli, "IS_MACOS", True)
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "env_read", lambda: dict(env))
    monkeypatch.setattr(cli, "ensure_shellmcp_default_user", lambda values: values.update({
        "SHELLMCP_DEFAULT_USER": "alice",
        "SHELLMCP_DEFAULT_HOME": "/Users/alice",
        "SHELLMCP_DEFAULT_CWD": "/Users/alice",
    }))
    monkeypatch.setattr(cli, "env_set_many", lambda values: updates.append(values))
    monkeypatch.setattr(cli, "_mcp_config", lambda: {"mcpServers": {"direct": {"command": "npx"}}})
    monkeypatch.setattr(cli, "_mcp_go_supervisor_enabled", lambda: True)
    monkeypatch.setattr(cli, "_mcp_refresh_generated_configs", lambda _cfg: None)
    monkeypatch.setattr(cli, "_mcp_retire_legacy_relay_services", lambda *_args: None)
    monkeypatch.setattr(cli, "svc_restart", lambda *_args: None)
    monkeypatch.setattr(cli, "need_root", lambda: None)

    cli.cmd_mcp_install(argparse.Namespace(name=None, backend=None))

    assert updates == [{
        "SHELLMCP_MCP_CONFIG": str(tmp_path / "mcp-supervisor.json"),
        "SHELLMCP_DEFAULT_USER": "alice",
        "SHELLMCP_DEFAULT_HOME": "/Users/alice",
        "SHELLMCP_DEFAULT_CWD": "/Users/alice",
    }]


def test_go_migration_removes_orphaned_python_relay_units(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Old services absent from mcp.json must not survive the Go migration."""
    systemd_dir = tmp_path / "systemd"
    systemd_dir.mkdir()
    orphan = systemd_dir / "gptadmin-mcp-Orphan.service"
    orphan.write_text("[Service]\nExecStart=python3 generic_stdio_mcp_relay.py\n", encoding="utf-8")
    stopped: list[str] = []
    monkeypatch.setattr(cli, "IS_MACOS", False)
    monkeypatch.setattr(cli, "SYSTEMD_DIR", systemd_dir)
    monkeypatch.setattr(cli, "_mcp_manager_exists", lambda: False)
    monkeypatch.setattr(cli, "svc_disable_stop", lambda name, path: stopped.append(name) or True)
    monkeypatch.setattr(cli, "svc_daemon_reload", lambda: None)

    cli._mcp_retire_legacy_relay_services({"mcpServers": {}}, [])

    assert stopped == ["gptadmin-mcp-Orphan.service"]
    assert not orphan.exists()


def test_go_migration_removes_orphaned_macos_python_relay_plists(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Darwin upgrades must retire relay plists that are no longer present in mcp.json."""
    services_dir = tmp_path / "LaunchDaemons"
    services_dir.mkdir()
    orphan = services_dir / "com.gptadmin.mcp.orphan.plist"
    orphan.write_text("<plist><string>generic_stdio_mcp_relay.py</string></plist>\n", encoding="utf-8")
    stopped: list[tuple[str, str]] = []
    monkeypatch.setattr(cli, "IS_MACOS", True)
    monkeypatch.setattr(cli, "SERVICES_DIR", services_dir, raising=False)
    monkeypatch.setattr(cli, "_mcp_manager_exists", lambda: False)
    monkeypatch.setattr(cli, "svc_disable_stop", lambda name, path: stopped.append((name, path.name)) or True)

    cli._mcp_retire_legacy_relay_services({"mcpServers": {}}, [])

    assert stopped == [("com.gptadmin.mcp.orphan", orphan.name)]
    assert not orphan.exists()


def test_mcp_edit_refreshes_the_shellmcp_supervisor_registry(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """Enabled-state changes made in the editor must update ShellMCP's registry."""
    config_file = tmp_path / "mcp.json"
    cli._json_write(config_file, {"mcpServers": {"edited": {"command": "npx", "args": ["-y", "example-mcp"]}}})
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "MCP_CONFIG_FILE", config_file)
    monkeypatch.setattr(cli, "MCP_AGENTS_DIR", tmp_path / "mcp-agents.d")
    monkeypatch.setattr(cli, "MCP_SUPERVISOR_CONFIG", tmp_path / "mcp-supervisor.json")
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", tmp_path / "mcp-relay.token")
    monkeypatch.setattr(cli, "env_read", lambda: {"MCP_RELAY_AGENT_TOKEN": "token"})
    monkeypatch.setenv("EDITOR", "test-editor")
    monkeypatch.setattr(
        cli,
        "run",
        lambda _command: cli._json_write(
            config_file,
            {"mcpServers": {"edited": {"command": "npx", "args": ["-y", "example-mcp"], "enabled": False}}},
        ),
    )

    cli.cmd_mcp_edit(argparse.Namespace())

    assert cli._json_read(tmp_path / "mcp-supervisor.json", []) == []


def test_mcp_add_drops_the_standard_command_separator() -> None:
    """The documented `mcp add NAME -- npx ...` form must keep npx as command."""
    args = argparse.Namespace(command="--", args=["npx", "-y", "chrome-devtools-mcp@latest"])

    cli._mcp_extract_tail_options(args)

    assert args.command == "npx"
    assert args.args == ["-y", "chrome-devtools-mcp@latest"]
