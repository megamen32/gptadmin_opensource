from pathlib import Path
from types import SimpleNamespace

import cli

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "cli.py"


def test_update_restores_auth_material_if_package_install_rewrites_env(monkeypatch, tmp_path):
    """An interrupted/package update must not invalidate existing JWTs."""
    env_file = tmp_path / "gptadmin.env"
    original = {
        "CTL_TOKEN": "ctl-before",
        "SHELLMCP_TOKEN": "shell-before",
        "ADMIN_PASSWORD": "admin-before",
        "OAUTH_CLIENT_SECRET": "oauth-before",
        "GPTADMIN_CODEX_MCP_BEARER": "jwt-before",
        "INSTALL_HUB": "true",
    }
    env_file.write_text("\n".join(f"{key}={value}" for key, value in original.items()) + "\n")

    monkeypatch.setattr(cli, "ENV_FILE", env_file)
    monkeypatch.setattr(cli, "UNIT_PATH_HUB", tmp_path / "hub.unit")
    monkeypatch.setattr(cli, "UNIT_PATH_SHELLMCP", tmp_path / "shell.unit")
    monkeypatch.setattr(cli, "BIN_DIR", tmp_path / "bin")
    monkeypatch.setattr(cli, "INSTALL_DIR", tmp_path / "install")
    monkeypatch.setattr(cli, "CLI_PATH", tmp_path / "bin" / "gptadmin")
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "download", lambda _url, path: path.write_bytes(b"package"))
    monkeypatch.setattr(cli, "install_component_from_pkg", lambda _pkg, _component: env_file.write_text("HUB_URL=http://127.0.0.1:9001\n"))
    monkeypatch.setattr(cli, "_remote_artifact_build_info", lambda _url: {})
    monkeypatch.setattr(cli, "_installed_build_info", lambda _env, _hub: {})
    monkeypatch.setattr(cli, "svc_stop_multi", lambda _pairs: None)
    monkeypatch.setattr(cli, "_write_installed_build_marker", lambda _info, _pkg: None)
    monkeypatch.setattr(cli, "_cleanup_obsolete_runtime_files", lambda: None)
    monkeypatch.setattr(cli, "write_hub_unit", lambda *_args: None)
    monkeypatch.setattr(cli, "write_shellmcp_unit", lambda *_args: None)
    monkeypatch.setattr(cli, "svc_daemon_reload", lambda: None)
    monkeypatch.setattr(cli, "svc_enable_start", lambda *_args: None)
    monkeypatch.setattr(cli, "wait_local_hub_health", lambda *_args, **_kwargs: None)
    monkeypatch.setattr(cli, "svc_autoupdate_enable_start", lambda *_args: None)
    monkeypatch.setattr(cli, "auto_configure_ai_mcp_clients", lambda *_args: None)

    cli.cmd_update(SimpleNamespace(
        hub=False,
        shellmcp=False,
        no_hub=False,
        no_shellmcp=True,
        pkg_all="https://example.test/all.tgz",
        pkg_hub="https://example.test/hub.tgz",
        pkg_shellmcp=None,
        force=True,
        auto=False,
    ))

    after = cli.env_read()
    for key, value in original.items():
        if key == "INSTALL_HUB":
            continue
        assert after[key] == value


def test_update_prefers_explicit_component_flags_over_stale_files():
    text = CLI.read_text()
    assert "if 'INSTALL_HUB' in env else" in text
    assert "if 'INSTALL_SHELLMCP' in env else" in text


def test_update_refreshes_automatic_client_registration_without_autoapprove():
    text = CLI.read_text()
    start = text.index("def cmd_update(args):")
    end = text.index("\n\n# ===== AI client MCP auto-configuration =====", start)
    block = text[start:end]
    assert "maybe_autoapprove_local_shellmcp(" not in block
    assert "auto_configure_ai_mcp_clients(env_read(), install_hub)" in block


def test_macos_launchd_bootout_is_not_duplicated_before_bootstrap():
    # After the fix that splits svc_enable_start into svc_enable (load-only)
    # + svc_enable_start (load + kickstart), the bootstrap call lives in
    # svc_enable. timer_disable must use svc_enable (not svc_enable_start)
    # so a config reload never fires an unintended kickstart.
    text = CLI.read_text()
    enable_start = text.index("    def svc_enable_start(label: str, unit_path: Path):")
    enable_end = text.index("\n    def svc_restart", enable_start)
    enable_block = text[enable_start:enable_end]
    # svc_enable_start now delegates the bootout + bootstrap to svc_enable.
    assert "bootout', domain, str(unit_path)" not in enable_block
    assert "bootstrap = _launchctl_capture" not in enable_block
    assert "svc_enable(label, unit_path)" in enable_block

    # svc_enable owns the bootout + bootstrap + enable + load -w fallback.
    enable_fn = text.index("    def svc_enable(label: str, unit_path: Path):")
    enable_fn_end = text.index("\n    def svc_enable_start", enable_fn)
    load_block = text[enable_fn:enable_fn_end]
    assert "bootout', domain, str(unit_path)" not in load_block
    assert "bootstrap = _launchctl_capture" in load_block

    # timer_disable must use svc_enable (load-only) so the kickstart does
    # NOT fire on disable — that was the original CRITICAL bug.
    timer_disable_start = text.index("    def timer_disable(timer_unit: str):")
    timer_disable_end = text.index("\n    def timer_status", timer_disable_start)
    disable_block = text[timer_disable_start:timer_disable_end]
    assert "svc_enable(SVC_AUTO_UPDATE_LABEL" in disable_block
    assert "svc_enable_start(SVC_AUTO_UPDATE_LABEL" not in disable_block

    # timer_enable may still kick once (the first run of a freshly enabled
    # periodic update is intentional), so it keeps svc_enable_start.
    timer_enable_start = text.index("    def timer_enable(timer_unit: str):")
    timer_enable_end = text.index("\n    def timer_disable", timer_enable_start)
    enable_block_timer = text[timer_enable_start:timer_enable_end]
    assert "svc_enable_start(SVC_AUTO_UPDATE_LABEL" in enable_block_timer
