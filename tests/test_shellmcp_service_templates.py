"""Public ShellMCP service templates must use installed, portable paths."""

from pathlib import Path

import cli


ROOT = Path(__file__).resolve().parents[1]


def test_systemd_shellmcp_uses_installed_go_binary() -> None:
    unit = (ROOT / "deploy/systemd/shellmcp.service").read_text(encoding="utf-8")
    assert "ExecStart=/opt/gptadmin/bin/shellmcp" in unit
    assert "rootd-go-canary" not in unit
    assert "/home/roomhacker" not in unit


def test_cli_uses_the_canonical_shellmcp_unit_name() -> None:
    assert cli.SYSTEMD_SHELLMCP == "shellmcp.service"
