from pathlib import Path
from types import SimpleNamespace

import pytest

import cli


def test_standalone_enrollment_keeps_only_scoped_device_credential(monkeypatch, tmp_path: Path):
    token_file = tmp_path / "mcp-relay.token"
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)
    env = {
        "ADMIN_PASSWORD": "fleet-admin",
        "OAUTH_CLIENT_SECRET": "fleet-oauth",
        "CTL_TOKEN": "fleet-control",
        "MCP_RELAY_ENROLLMENT_PASSWORD": "legacy-admin-password",
        "SHELLMCP_TOKEN": "device-token",
        "INSTALL_HUB": "false",
        "INSTALL_SHELLMCP": "true",
        "INSTALL_ROLE": "executor",
        "SHELLMCP_IDENTITY_DIR": str(tmp_path / "identity"),
    }

    cli._prepare_standalone_credentials(env, enrollment_token="scoped-device-grant")
    cli._persist_scoped_enrollment_token(env)

    assert env["INSTALL_ROLE"] == "executor"
    assert env["SHELLMCP_TOKEN"] == "device-token"
    assert env["SHELLMCP_IDENTITY_DIR"] == str(tmp_path / "identity")
    assert "ADMIN_PASSWORD" not in env
    assert "OAUTH_CLIENT_SECRET" not in env
    assert "CTL_TOKEN" not in env
    assert "MCP_RELAY_ENROLLMENT_PASSWORD" not in env
    assert "MCP_RELAY_AGENT_TOKEN" not in env
    assert token_file.read_text() == "scoped-device-grant\n"
    assert token_file.stat().st_mode & 0o077 == 0


def test_standalone_enrollment_rejects_admin_password():
    with pytest.raises(RuntimeError, match="scoped enrollment token"):
        cli._prepare_standalone_credentials({}, admin_password="fleet-admin")


def test_scoped_token_matching_known_admin_secret_fails_closed():
    with pytest.raises(RuntimeError, match="fleet administrator credential"):
        cli._prepare_standalone_credentials(
            {"ADMIN_PASSWORD": "fleet-admin"},
            enrollment_token="fleet-admin",
        )


def test_legacy_admin_token_file_is_removed_but_unrelated_device_token_is_preserved(
    monkeypatch, tmp_path: Path
):
    token_file = tmp_path / "mcp-relay.token"
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)

    token_file.write_text("fleet-admin\n")
    cli._remove_known_legacy_standalone_token({"fleet-admin", "fleet-oauth"})
    assert not token_file.exists()

    token_file.write_text("existing-device-token\n")
    cli._remove_known_legacy_standalone_token({"fleet-admin", "fleet-oauth"})
    assert token_file.read_text() == "existing-device-token\n"


def test_scoped_token_persistence_does_not_clobber_concurrent_device_token(
    monkeypatch, tmp_path: Path
):
    token_file = tmp_path / "mcp-relay.token"
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)
    token_file.write_text("first-device-token\n")

    cli._persist_scoped_enrollment_token({"MCP_RELAY_AGENT_TOKEN": "second-device-token"})

    assert token_file.read_text() == "first-device-token\n"


def test_standalone_update_preserves_device_identity_and_role(monkeypatch, tmp_path: Path):
    env_file = tmp_path / "gptadmin.env"
    token_file = tmp_path / "mcp-relay.token"
    identity_dir = tmp_path / "identity"
    env_file.write_text(
        "\n".join([
            "ADMIN_PASSWORD=fleet-admin",
            "OAUTH_CLIENT_SECRET=fleet-oauth",
            "CTL_TOKEN=fleet-control",
            "SHELLMCP_TOKEN=device-token",
            "INSTALL_HUB=false",
            "INSTALL_SHELLMCP=true",
            "INSTALL_ROLE=executor",
            f"SHELLMCP_IDENTITY_DIR={identity_dir}",
        ]) + "\n"
    )
    token_file.write_text("scoped-device-grant\n")

    monkeypatch.setenv("GPTADMIN_UPDATE_SKIP_MANIFEST", "1")
    monkeypatch.setattr(cli, "ENV_FILE", env_file)
    monkeypatch.setattr(cli, "MCP_TOKEN_FILE", token_file)
    monkeypatch.setattr(cli, "UNIT_PATH_HUB", tmp_path / "hub.service")
    monkeypatch.setattr(cli, "UNIT_PATH_SHELLMCP", tmp_path / "shellmcp.service")
    monkeypatch.setattr(cli, "BIN_DIR", tmp_path / "bin")
    monkeypatch.setattr(cli, "INSTALL_DIR", tmp_path / "install")
    monkeypatch.setattr(cli, "CLI_PATH", tmp_path / "bin" / "gptadmin")
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "_local_unified_node_enabled", lambda: False)
    monkeypatch.setattr(cli, "_update_runtime_paths", lambda: [])
    monkeypatch.setattr(cli, "_remote_artifact_build_info", lambda _url: {})
    monkeypatch.setattr(cli, "_installed_build_info", lambda _env, _hub: {})
    monkeypatch.setattr(cli, "download", lambda _url, path: path.write_bytes(b"package"))
    monkeypatch.setattr(cli, "install_component_from_pkg", lambda *_args: None)
    monkeypatch.setattr(cli, "_write_installed_build_marker", lambda *_args: None)
    monkeypatch.setattr(cli, "_cleanup_obsolete_runtime_files", lambda: None)
    monkeypatch.setattr(cli, "write_hub_unit", lambda *_args: None)
    monkeypatch.setattr(cli, "write_shellmcp_unit", lambda *_args: None)
    monkeypatch.setattr(cli, "svc_daemon_reload", lambda: None)
    monkeypatch.setattr(cli, "svc_stop_multi", lambda *_args: None)
    monkeypatch.setattr(cli, "svc_enable_start", lambda *_args: None)
    monkeypatch.setattr(cli, "svc_restart", lambda *_args: None)
    monkeypatch.setattr(cli, "svc_autoupdate_enable_start", lambda *_args: None)
    monkeypatch.setattr(cli, "auto_configure_ai_mcp_clients", lambda *_args: None)
    monkeypatch.setattr(cli, "_configure_builtin_grepmesh", lambda *_args: False)
    monkeypatch.setattr(cli, "write_grepmesh_unit", lambda *_args: None)
    monkeypatch.setattr(cli, "maybe_autoapprove_local_shellmcp", lambda *_args: None)

    cli.cmd_update(SimpleNamespace(
        hub=False,
        shellmcp=False,
        no_hub=False,
        no_shellmcp=False,
        pkg_all="unused-all",
        pkg_hub="unused-hub",
        pkg_shellmcp="unused-shellmcp",
        force=True,
        auto=False,
        grepmesh=False,
        no_grepmesh=True,
    ))

    saved = cli.env_read()
    assert saved["INSTALL_ROLE"] == "executor"
    assert saved["SHELLMCP_IDENTITY_DIR"] == str(identity_dir)
    assert saved["SHELLMCP_TOKEN"] == "device-token"
    assert saved["INSTALL_HUB"] == "false"
    assert "ADMIN_PASSWORD" not in saved
    assert "OAUTH_CLIENT_SECRET" not in saved
    assert "CTL_TOKEN" not in saved
    assert token_file.read_text() == "scoped-device-grant\n"
