"""Opt-in real Chromium WebAuthn enrollment smoke for the admin SPA."""

from __future__ import annotations

import os
import shutil
import signal
import socket
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import sync_playwright


ROOT = Path(__file__).resolve().parents[1]


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@pytest.mark.skipif(os.environ.get("GPTADMIN_BROWSER_TESTS") != "1", reason="opt-in browser acceptance lane")
def test_admin_spa_can_enroll_passkey_with_virtual_authenticator(tmp_path: Path) -> None:
    """Exercise password login, SPA enrollment and the real WebAuthn ceremony."""

    chrome = shutil.which("google-chrome") or shutil.which("chromium")
    if not chrome:
        pytest.fail("Google Chrome or Chromium is required for browser acceptance")

    binary = tmp_path / "gptadmin-hub"
    subprocess.run(
        ["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/gptadmin-hub"],
        cwd=ROOT / "go-hub",
        check=True,
        timeout=120,
    )
    port = _free_port()
    config_dir = tmp_path / "config"
    config_dir.mkdir()
    env = os.environ.copy()
    env.update(
        {
            "GPTADMIN_HUB_HOST": "127.0.0.1",
            "GPTADMIN_HUB_PORT": str(port),
            "PORT": str(port),
            "CTL_TOKEN": "ctl",
            "ADMIN_PASSWORD": "pw",
            "PUBLIC_ORIGIN": f"http://localhost:{port}",
            "MCP_RESOURCE": f"http://localhost:{port}",
            "GPTADMIN_CONFIG_DIR": str(config_dir),
            "GPTADMIN_ROOT": str(ROOT),
            "NO_PROXY": "localhost,127.0.0.1",
            "no_proxy": "localhost,127.0.0.1",
            "HTTP_PROXY": "",
            "HTTPS_PROXY": "",
            "ALL_PROXY": "",
        }
    )
    process = subprocess.Popen(
        [str(binary)],
        cwd=ROOT,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    try:
        base_url = f"http://localhost:{port}"
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(base_url + "/version", timeout=1) as response:
                    if response.status == 200:
                        break
            except OSError:
                time.sleep(0.1)
        else:
            pytest.fail("disposable Hub did not become ready")

        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True, executable_path=chrome, args=["--no-sandbox"])
            context = browser.new_context()
            page = context.new_page()
            cdp = context.new_cdp_session(page)
            cdp.send("WebAuthn.enable")
            cdp.send(
                "WebAuthn.addVirtualAuthenticator",
                {
                    "options": {
                        "protocol": "ctap2",
                        "transport": "internal",
                        "hasResidentKey": True,
                        "hasUserVerification": True,
                        "isUserVerified": True,
                        "automaticPresenceSimulation": True,
                    }
                },
            )
            page.goto(base_url + "/admin/login", wait_until="domcontentloaded")
            page.locator("#password").fill("pw")
            page.locator('#login-form button[type="submit"]').click()
            page.wait_for_url(base_url + "/admin/", timeout=10000)
            page.evaluate("() => { showView('security'); return loadSecurityControls(); }")
            page.evaluate("() => enrollSecurityPasskey()")
            page.wait_for_function(
                "() => document.querySelector('#securityPasskeyResult')?.textContent.includes('mfa_enrolled')",
                timeout=10000,
            )
            assert "mfa_enrolled" in page.locator("#securityPasskeyResult").inner_text()
            browser.close()
    finally:
        if process.poll() is None:
            os.killpg(os.getpgid(process.pid), signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                process.wait(timeout=5)
