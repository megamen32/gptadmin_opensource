from pathlib import Path
from types import SimpleNamespace
import json
import hashlib
import pytest

import cli

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "cli.py"


def test_update_reads_canonical_release_manifest_artifact_list(monkeypatch):
    class Response:
        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def read(self):
            return json.dumps(
                {
                    "schema": "gptadmin.release-manifest/v1",
                    "artifacts": [
                        {
                            "path": "build/gptadmin-linux-amd64.tar.gz",
                            "sha256": "a" * 64,
                            "size": 42,
                            "build_version": 9,
                        }
                    ],
                }
            ).encode()

    monkeypatch.setattr(cli.urllib.request, "urlopen", lambda *_args, **_kwargs: Response())

    info = cli._remote_artifact_build_info("https://mirror.example/gptadmin-linux-amd64.tar.gz")

    assert info["sha256"] == "a" * 64
    assert info["size"] == 42


def test_update_rejects_download_that_does_not_match_manifest(tmp_path: Path):
    package = tmp_path / "package.tar.gz"
    package.write_bytes(b"actual bytes")
    metadata = {"sha256": hashlib.sha256(b"expected bytes").hexdigest(), "size": len(b"expected bytes")}

    with pytest.raises(SystemExit):
        cli._verify_downloaded_artifact(package, metadata)


def test_update_rejects_missing_manifest_metadata_when_required(monkeypatch, tmp_path: Path):
    """Normal updates must not install an artifact that has no published digest."""
    monkeypatch.delenv("GPTADMIN_UPDATE_SKIP_MANIFEST", raising=False)
    package = tmp_path / "package.tar.gz"
    package.write_bytes(b"unsigned bytes")

    with pytest.raises(SystemExit):
        try:
            cli._verify_downloaded_artifact(package, {}, require_metadata=True)
        except SystemExit:
            raise
        except Exception as exc:
            pytest.fail(f"missing metadata raised the wrong error: {exc}")


def test_update_restores_auth_material_if_package_install_rewrites_env(monkeypatch, tmp_path):
    """An interrupted/package update must not invalidate existing JWTs."""
    monkeypatch.setenv("GPTADMIN_UPDATE_SKIP_MANIFEST", "1")
    env_file = tmp_path / "gptadmin.env"
    original = {
        "CTL_TOKEN": "ctl-before",
        "SHELLMCP_TOKEN": "shell-before",
        "SHELL_TOKEN": "shell-before",
        "ROOTD_TOKEN": "rootd-before",
        "ROOTD_UPDATE_TOKEN": "rootd-update-before",
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
    monkeypatch.setattr(cli, "wait_local_hub_health", lambda *_args, **_kwargs: True)
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


def test_sync_oauth_origin_repairs_stale_internal_fallback_when_frp_is_enabled():
    """FRP installs must not publish a loopback/private Hub origin after update."""
    env = {
        "FRP_ENABLE": "true",
        "FRP_SUBDOMAIN": "u-f1102930",
        "FRP_DOMAIN": "t.gptadmin.bezrabotnyi.com",
        "HUB_PUBLIC_URL": "http://95.165.165.65:9001",
        "HUB_URL": "http://127.0.0.1:9001",
    }

    cli.sync_oauth_origin_env(env)

    expected = "https://u-f1102930.t.gptadmin.bezrabotnyi.com"
    assert env["HUB_PUBLIC_URL"] == expected
    assert env["PUBLIC_ORIGIN"] == expected
    assert env["MCP_RESOURCE"] == expected


def test_sync_oauth_origin_preserves_explicit_public_origin_over_loopback_hub():
    """Token issuance must not silently replace the Hub's published identity."""
    env = {
        "HUB_URL": "http://127.0.0.1:9001",
        "PUBLIC_ORIGIN": "https://u-example.t.gptadmin.bezrabotnyi.com/",
        "MCP_RESOURCE": "https://u-example.t.gptadmin.bezrabotnyi.com/",
    }

    cli.sync_oauth_origin_env(env)

    expected = "https://u-example.t.gptadmin.bezrabotnyi.com"
    assert env["PUBLIC_ORIGIN"] == expected
    assert env["MCP_RESOURCE"] == expected


def test_cleanup_removes_obsolete_shellmcp_primary_override(monkeypatch, tmp_path):
    """Updates must stop an old drop-in from splitting Hub and Shell credentials."""
    systemd_dir = tmp_path / "systemd"
    dropins = systemd_dir / "shellmcp.service.d"
    dropins.mkdir(parents=True)
    obsolete = dropins / "90-go-primary.conf"
    newer_obsolete = dropins / "95-go-primary.conf"
    preserved = dropins / "80-spool-readable.conf"
    obsolete.write_text("[Service]\nEnvironmentFile=/etc/gptadmin/go-shellmcp-primary.env\n")
    newer_obsolete.write_text("[Service]\nEnvironmentFile=/etc/gptadmin/go-shellmcp.env\n")
    preserved.write_text("[Service]\nExecStartPre=/usr/bin/true\n")

    monkeypatch.setattr(cli, "IS_MACOS", False)
    monkeypatch.setattr(cli, "SYSTEMD_DIR", systemd_dir)
    monkeypatch.setattr(cli, "SYSTEMD_SHELLMCP", "shellmcp.service")
    monkeypatch.setattr(cli, "BIN_DIR", tmp_path / "bin")
    monkeypatch.setattr(cli, "CLI_PATH", tmp_path / "bin" / "gptadmin")

    cli._cleanup_obsolete_runtime_files()

    assert not obsolete.exists()
    assert not newer_obsolete.exists()
    assert preserved.exists()


def test_update_refreshes_automatic_client_registration_without_autoapprove():
    text = CLI.read_text()
    start = text.index("def cmd_update(args):")
    end = text.index("\n\n# ===== AI client MCP auto-configuration =====", start)
    block = text[start:end]
    assert "maybe_autoapprove_local_shellmcp(" not in block
    assert "auto_configure_ai_mcp_clients(env_read(), install_hub)" in block


def test_update_transaction_restores_runtime_snapshot_after_failed_canary(tmp_path: Path):
    """A failed update must restore replaced runtime files before rethrowing."""

    binary = tmp_path / "bin" / "gptadmin_hub"
    binary.parent.mkdir()
    binary.write_bytes(b"version-old")
    rollback_callbacks: list[str] = []

    def failed_update() -> None:
        binary.write_bytes(b"version-new")
        raise RuntimeError("health check failed")

    with pytest.raises(RuntimeError, match="health check failed"):
        cli._run_update_transaction(
            [binary],
            failed_update,
            rollback_callback=lambda: rollback_callbacks.append("restarted"),
        )

    assert binary.read_bytes() == b"version-old"
    assert rollback_callbacks == ["restarted"]


def test_transactional_update_restarts_services_after_pre_download_failure(monkeypatch):
    """A failed automatic update must not strand the already-stopped Hub."""

    restarts: list[str] = []
    monkeypatch.setattr(cli, "need_root", lambda: None)
    monkeypatch.setattr(cli, "_update_runtime_paths", lambda: [])
    monkeypatch.setattr(cli, "_restart_update_services_after_rollback", lambda: restarts.append("restart"))

    def failed_before_download(_args) -> None:
        cli._mark_update_runtime_started()
        raise RuntimeError("release manifest unavailable")

    wrapped = cli._transactional_update(failed_before_download)
    with pytest.raises(RuntimeError, match="release manifest unavailable"):
        wrapped(SimpleNamespace())

    assert restarts == ["restart"]
    assert cli._active_update_snapshot is None


def test_update_stages_release_before_stopping_services():
    """A transient release failure must leave the running Hub untouched."""

    source = CLI.read_text()
    start = source.index("def cmd_update(args):")
    end = source.index("\n\n# ===== AI client MCP auto-configuration =====", start)
    update = source[start:end]

    assert update.index("with tempfile.TemporaryDirectory()") < update.index("svc_stop_multi(")


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
