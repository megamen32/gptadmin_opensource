"""Regression coverage for removing stale systemd hardening overrides."""

from pathlib import Path

import cli


def test_cleanup_removes_only_legacy_shellmcp_home_hardening(tmp_path: Path, monkeypatch) -> None:
    dropin_dir = tmp_path / "shellmcp.service.d"
    dropin_dir.mkdir()
    dropin = dropin_dir / "100-gptadmin-user-mode.conf"
    dropin.write_text(
        "[Service]\nUser=admin\nGroup=admin\nProtectHome=read-only\n",
        encoding="utf-8",
    )
    monkeypatch.setattr(cli, "SYSTEMD_DIR", tmp_path)
    monkeypatch.setattr(cli, "SYSTEMD_SHELLMCP", "shellmcp.service")
    monkeypatch.setattr(cli, "IS_MACOS", False)
    monkeypatch.setattr(cli, "IS_USER_INSTALL", False)

    cli._cleanup_obsolete_runtime_files()

    assert not dropin.exists()
