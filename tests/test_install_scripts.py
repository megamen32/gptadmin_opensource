"""Static smoke tests for install scripts.

Validates that install scripts exist, have correct shebangs, and reference
the expected download URLs — without actually executing them.
"""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEPLOY = ROOT / "deploy"
EXPECTED_URL_FRAGMENT = "github.com/megamen32/gptadmin_opensource"


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


def test_android_installer_configures_github_self_repair():
    """Android must use the public GitHub client bundle and the unified self-repair path."""
    content = (DEPLOY / "install_android.sh").read_text()
    assert "gptadmin-android-arm64-client.tar.gz" in content
    assert "SHELLMCP_AUTO_UPDATE=0" in content
    assert "SHELLMCP_SELF_REPAIR_DISABLE=0" in content
    assert "SHELLMCP_LEGACY_MANIFEST_UPDATE=0" in content
    assert "raw.githubusercontent.com/megamen32/gptadmin_opensource/main/deploy/install_android.sh" in content


def test_android_installer_reuses_existing_shellmcp_credentials():
    """Rerunning Android install must not rotate the stored agent credential."""
    content = (DEPLOY / "install_android.sh").read_text()
    assert '"$ENV_FILE"' in content
    assert 'SHELLMCP_TOKEN=$(read_existing_env SHELLMCP_TOKEN)' in content
    assert 'HUB_URL=$(read_existing_env HUB_URL)' in content


def test_android_installer_persists_explicit_hub_dns_server():
    """Android must preserve the resolver used by the Hub transport."""
    content = (DEPLOY / "install_android.sh").read_text()
    assert 'SHELLMCP_HUB_DNS_SERVER=${SHELLMCP_HUB_DNS_SERVER:-1.1.1.1:53}' in content
    assert 'SHELLMCP_HUB_DNS_SERVER=$SHELLMCP_HUB_DNS_SERVER' in content
    assert 'SHELLMCP_HUB_RESOLVE_TO=${SHELLMCP_HUB_RESOLVE_TO:-}' in content
    assert 'SHELLMCP_HUB_RESOLVE_TO=$SHELLMCP_HUB_RESOLVE_TO' in content


def test_standalone_shellmcp_installer_persists_credentials_without_printing_them():
    """Standalone installer must reuse a state-file credential on upgrades."""
    content = (DEPLOY / "install_shellmcp.sh").read_text()
    assert 'SHELLMCP_TOKEN_FILE=' in content
    assert 'SHELLMCP_TOKEN=$(cat "$SHELLMCP_TOKEN_FILE")' in content
    assert 'printf \'%s\\n\' "$SHELLMCP_TOKEN"' in content
    assert 'Use SHELLMCP_TOKEN=$TOKEN' not in content


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


def test_cli_setup_completion_does_not_print_raw_bearer_credentials():
    """Setup completion must keep the AdminPassword/OAuth boundary secret-safe."""
    content = (ROOT / "cli.py").read_text(encoding="utf-8")
    assert "API-Ключ (Bearer)" not in content
    assert "вставьте ключ" not in content


def test_install_completion_uses_product_auth_vocabulary():
    """The installer quickstart must not teach operators legacy credential names."""
    content = (DEPLOY / "install.sh").read_text(encoding="utf-8")
    assert "CTL_TOKEN" not in content
    assert "AdminPassword/OAuth" in content


def test_cli_bootstrap_runs_without_optional_cryptography_dependency():
    """A curl-installed CLI must reach setup even on stock Python.

    ``-S`` skips site-packages, matching the Ubuntu user-install failure mode
    where no project dependencies have been installed yet.
    """
    result = subprocess.run(
        [sys.executable, "-S", str(ROOT / "cli.py"), "--help"],
        check=False,
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stderr


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


def test_cli_installs_zero_downtime_handover_components_on_system_linux():
    """System Linux installs must materialize the standby unit and handover helper."""
    content = (ROOT / "cli.py").read_text(encoding="utf-8")
    assert "gptadmin-hub-standby.service" in content
    assert "gptadmin-handover" in content
    assert "GPTADMIN_HANDOVER_DRAIN_SECONDS" in content
    assert "UNIT_PATH_HUB_STANDBY.write_text" in content


def test_handover_helper_requires_managed_nginx_upstream():
    """Zero-downtime switching must fail closed when nginx is not managed by GPTAdmin."""
    content = (ROOT / "cli.py").read_text(encoding="utf-8")
    assert "managed nginx upstream missing" in content
    assert "nginx -t; systemctl reload nginx" in content


def test_linux_cli_materializes_handover_helper_and_standby(tmp_path):
    """Linux branch must define and actually write both handover components."""
    if not sys.platform.startswith("linux"):
        return
    import runpy
    ns = runpy.run_path(str(ROOT / "cli.py"), run_name="gptadmin_install_contract")
    assert callable(ns.get("_write_handover_helper"))
    write_hub = ns["write_hub_unit"]
    g = write_hub.__globals__
    g["BIN_DIR"] = tmp_path / "bin"
    g["UNIT_PATH_HUB"] = tmp_path / "gptadmin-hub.service"
    g["UNIT_PATH_HUB_STANDBY"] = tmp_path / "gptadmin-hub-standby.service"
    g["IS_USER_INSTALL"] = False
    g["env_read"] = lambda: {}
    g["process_hardening_for_env"] = lambda _env: ""
    g["render_unit_with_hardening"] = lambda text, _hardening: text
    g["BIN_DIR"].mkdir(parents=True)
    write_hub(True, False)
    helper = g["BIN_DIR"] / "gptadmin-handover"
    assert helper.exists() and helper.stat().st_mode & 0o111
    assert g["UNIT_PATH_HUB_STANDBY"].exists()
    standby = g["UNIT_PATH_HUB_STANDBY"].read_text()
    assert "ExecStart=/usr/bin/env GPTADMIN_HUB_HOST=127.0.0.1 GPTADMIN_HUB_PORT=19001 HUB_PORT=19001" in standby
    assert "DRAIN_SECONDS=${GPTADMIN_HANDOVER_DRAIN_SECONDS:-65}" in helper.read_text()


def test_android_installer_fails_closed_without_registered_hub_credential():
    source = (DEPLOY / "install_android.sh").read_text(encoding="utf-8")
    assert "unregistered random credential" in source
    assert "openssl rand -hex 16" not in source


def test_android_installer_recreates_termux_service_parent_before_autostart():
    source = (DEPLOY / "install_android.sh").read_text(encoding="utf-8")
    marker = 'if command -v sv-enable >/dev/null 2>&1; then\n  mkdir -p "${PREFIX:-/data/data/com.termux/files/usr}/var/service"'
    assert marker in source
    assert source.index(marker) < source.index('sv-enable "$SERVICE_NAME"')


def test_android_installer_rejects_malformed_multiline_credentials():
    source = (DEPLOY / "install_android.sh").read_text(encoding="utf-8")
    assert "must be one non-whitespace line" in source
    assert '[[ "$SHELLMCP_TOKEN" =~ [[:space:]] ]]' in source


def test_android_installer_requires_real_autostart_process():
    source = (DEPLOY / "install_android.sh").read_text(encoding="utf-8")
    assert 'sv status "$SERVICE_NAME"' in source
    assert "did not remain running after install" in source
    assert 'kill -0 "$shellmcp_pid"' in source
