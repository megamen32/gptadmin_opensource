"""Regression checks for the Windows installer-to-Go ShellMCP contract."""

from pathlib import Path


INSTALLER = Path(__file__).resolve().parents[1] / "deploy" / "install_win.ps1"


def test_windows_installer_writes_canonical_go_shellmcp_environment() -> None:
    """Polling installs must configure the variables read by Go ShellMCP."""
    script = INSTALLER.read_text(encoding="utf-8")

    assert '"SHELLMCP_QUEUE=$queueEnabled"' in script
    assert "SHELLMCP_HOST=$ShellmcpBind" in script
    assert "$env:SHELLMCP_QUEUE = '$queueEnabled'" in script
    assert "$env:SHELLMCP_HOST = '$ShellmcpBind'" in script
    assert "QUEUE_URL=1" not in script
    assert "$env:QUEUE_URL = '1'" not in script
