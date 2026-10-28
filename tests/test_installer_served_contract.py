"""Live contract tests for the public installer endpoints.

These tests download the REAL bytes served by the production installer proxy
(became.bezrabotnyi.com) and compare them with the reviewed repository copies.
They would have caught both shipped regressions:

1. the served installer drifting from the repository copy, and
2. the Windows installer demanding an operator-provided SHELLMCP_TOKEN,
   which broke the documented ``iwr | iex`` one-liner.

They exercise the real network path (no mocks). If the endpoint is
unreachable the tests skip rather than lie about coverage; CI runners have
network access, so a skip there is itself a signal.
"""

import hashlib
from pathlib import Path

import pytest
import urllib.request

ROOT = Path(__file__).resolve().parents[1]

BASE = "https://became.bezrabotnyi.com"

SERVED_ARTIFACTS = {
    "/mcp-bridge.user.js": ROOT / "public" / "mcp-bridge.user.js",
    "/install_win.ps1": ROOT / "deploy" / "install_win.ps1",
    "/install.sh": ROOT / "deploy" / "install.sh",
    "/install_shellmcp.sh": ROOT / "deploy" / "install_shellmcp.sh",
}


def _fetch(path: str) -> bytes:
    try:
        with urllib.request.urlopen(BASE + path, timeout=20) as resp:
            return resp.read()
    except OSError as exc:  # network down / DNS failure / proxy down
        pytest.skip(f"installer endpoint unreachable: {exc}")


@pytest.mark.parametrize("route", sorted(SERVED_ARTIFACTS))
def test_served_installer_matches_repository_copy(route: str) -> None:
    """The proxy must serve exactly the reviewed bytes from the repo."""

    local = SERVED_ARTIFACTS[route].read_bytes()
    served = _fetch(route)
    assert hashlib.sha256(served).hexdigest() == hashlib.sha256(local).hexdigest(), (
        f"served {route} differs from {SERVED_ARTIFACTS[route]}; "
        "the installer proxy reads deploy/ from the live working tree — "
        "deploy the repository copy or fix the proxy"
    )


def test_served_windows_installer_self_contained_or_self_enrolling() -> None:
    """The documented one-liner must work with no operator credential."""

    served = _fetch("/install_win.ps1").decode("utf-8", "utf-8")
    assert "unregistered random credential" not in served
    assert "function New-RandomToken" in served
    assert "$LocalHub = (-not $HubUrl) -or (Test-LoopbackUrl $HubUrl)" in served
    assert "AdminPassword: $AdminPassword" in served


def test_served_linux_agent_installer_self_enrolls() -> None:
    """The Linux agent installer mints its own token when none is provided."""

    served = _fetch("/install_shellmcp.sh").decode("utf-8", "utf-8")
    assert "secrets.token_hex" in served
