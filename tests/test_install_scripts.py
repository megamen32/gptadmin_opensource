"""Static smoke tests for install scripts.

Validates that install scripts exist, have correct shebangs, and reference
the expected download URLs — without actually executing them.
"""

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEPLOY = ROOT / "deploy"
EXPECTED_URL_FRAGMENT = "became.bezrabotnyi.com"


def test_install_sh_exists():
    assert (DEPLOY / "install.sh").exists(), "deploy/install.sh not found"


def test_install_sh_has_shebang():
    content = (DEPLOY / "install.sh").read_text()
    assert content.startswith("#!/usr/bin/env bash") or content.startswith("#!/bin/bash"), \
        "install.sh missing bash shebang"


def test_install_sh_has_set_strict():
    content = (DEPLOY / "install.sh").read_text()
    assert "set -euo pipefail" in content or "set -e" in content, \
        "install.sh should use set -e for fail-fast"


def test_install_sh_references_download_url():
    content = (DEPLOY / "install.sh").read_text()
    assert EXPECTED_URL_FRAGMENT in content, \
        f"install.sh should reference {EXPECTED_URL_FRAGMENT}"


def test_android_installer_configures_platform_specific_auto_update():
    """Android must poll its raw-binary manifest and restart after a swap."""
    content = (DEPLOY / "install_android.sh").read_text()
    assert "SHELLMCP_AUTO_UPDATE=1" in content
    assert "SHELLMCP_UPDATE_MANIFEST_URL=$HUB_URL/artifacts/shellmcp-android-arm64.json" in content
    assert "SHELLMCP_UPDATE_TOKEN=$SHELLMCP_TOKEN" in content
    assert "SHELLMCP_RESTART_CMD='kill -TERM $PPID'" in content


def test_install_win_exists():
    """Windows install script should exist in deploy/ or public/."""
    p1 = DEPLOY / "install_win.ps1"
    p2 = ROOT / "public" / "install_win.ps1"
    assert p1.exists() or p2.exists(), "install_win.ps1 not found in deploy/ or public/"


def test_install_win_has_powershell_syntax():
    """Windows installer should use iwr/Invoke-WebRequest."""
    for p in [DEPLOY / "install_win.ps1", ROOT / "public" / "install_win.ps1"]:
        if p.exists():
            content = p.read_text()
            assert "iwr" in content or "Invoke-WebRequest" in content, \
                "install_win.ps1 should use iwr or Invoke-WebRequest"
            return
    assert False, "install_win.ps1 not found"


def test_cli_has_version():
    """VERSION file should exist and be non-empty."""
    v = ROOT / "VERSION"
    assert v.exists(), "VERSION file not found"
    assert v.read_text().strip(), "VERSION file is empty"


def test_openapi_schema_exists():
    """OpenAPI schema should be available for Custom GPT import."""
    p = ROOT / "public" / "openapi.yaml"
    assert p.exists(), "public/openapi.yaml not found"
    content = p.read_text()
    assert "openapi: 3.1.0" in content, "public/openapi.yaml should use OpenAPI 3.1.0"
    assert 'version: "1.0.0"' in content, "public/openapi.yaml should use a stable semver info.version"


def test_userscript_installable_url():
    """The userscript should be servable (file exists in public/)."""
    p = ROOT / "public" / "mcp-bridge.user.js"
    assert p.exists(), "public/mcp-bridge.user.js not found"
