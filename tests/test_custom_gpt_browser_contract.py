"""Opt-in browser acceptance for the Custom GPT Actions contract.

This is deliberately an end-user shaped flow: the schema is fetched as a
browser would fetch it, a readonly Bearer is issued through the Hub control
plane and used for ``discover``, then the authorization-code/PKCE form is
completed in a real Chromium page.  It never prints access tokens or the
administrator password.

Set ``GPTADMIN_BROWSER_TESTS=1`` for a disposable local Hub.  Set
``GPTADMIN_BROWSER_CDP_URL`` to run the browser portion through BrowserOS on a
separate Mac; otherwise a local headless Chromium is used.  If that browser is
on another host, set ``GPTADMIN_BROWSER_HUB_HOST`` to the LAN/public address it
can reach (for example ``192.168.2.100``); the test binds its disposable Hub
and callback there.  The address must point to a non-production test Hub.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import secrets
import shutil
import signal
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest
from playwright.sync_api import Browser, Playwright, sync_playwright


ROOT = Path(__file__).resolve().parents[1]
def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _test_secret(label: str) -> str:
    """Return disposable test-only credential material without committing a value."""
    return f"{label}-{secrets.token_urlsafe(18)}"


def _request(
    base_url: str,
    path: str,
    *,
    method: str = "GET",
    token: str | None = None,
    payload: dict[str, Any] | None = None,
    form: dict[str, str] | None = None,
) -> tuple[int, dict[str, str], bytes]:
    if payload is not None and form is not None:
        raise ValueError("only one request body is allowed")
    data: bytes | None = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(base_url + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, dict(response.headers.items()), response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers.items()), exc.read()
    except urllib.error.URLError:
        # Startup probing intentionally races the disposable Hub process.
        return 0, {}, b""


def _json_request(*args: Any, **kwargs: Any) -> tuple[int, dict[str, str], dict[str, Any]]:
    status, headers, body = _request(*args, **kwargs)
    try:
        decoded = json.loads(body)
    except json.JSONDecodeError as exc:
        raise AssertionError(f"request returned non-JSON response (HTTP {status})") from exc
    assert isinstance(decoded, dict)
    return status, headers, decoded


class _CallbackHandler(BaseHTTPRequestHandler):
    query: dict[str, str] = {}
    event = threading.Event()

    def do_GET(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlparse(self.path)
        self.__class__.query = {key: values[-1] for key, values in urllib.parse.parse_qs(parsed.query).items()}
        self.__class__.event.set()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.end_headers()
        self.wfile.write(b"Custom GPT test callback accepted. This window can be closed.")

    def log_message(self, format: str, *args: object) -> None:  # noqa: A003
        return


def _pkce_challenge(verifier: str) -> str:
    return base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()


def _browser(playwright: Playwright) -> tuple[Browser, bool]:
    cdp_url = os.environ.get("GPTADMIN_BROWSER_CDP_URL", "").strip()
    if cdp_url:
        return playwright.chromium.connect_over_cdp(cdp_url), False
    chrome = shutil.which("google-chrome") or shutil.which("chromium")
    if not chrome:
        pytest.fail("Google Chrome or Chromium is required for browser acceptance")
    return playwright.chromium.launch(headless=True, executable_path=chrome, args=["--no-sandbox"]), True


@pytest.mark.skipif(os.environ.get("GPTADMIN_BROWSER_TESTS") != "1", reason="opt-in Custom GPT browser acceptance lane")
def test_custom_gpt_actions_schema_bearer_and_oauth_pkce_in_browser(tmp_path: Path) -> None:
    """Prove the exact schema, Bearer and browser OAuth/PKCE contract together."""

    admin_password = _test_secret("admin-password")
    control_token = _test_secret("control-token")
    oauth_client_secret = _test_secret("oauth-signing-secret")
    binary = tmp_path / "gptadmin-hub"
    subprocess.run(["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/gptadmin-hub"], cwd=ROOT / "go-hub", check=True, timeout=120)
    hub_port = _free_port()
    callback_port = _free_port()
    browser_hub_host = os.environ.get("GPTADMIN_BROWSER_HUB_HOST", "127.0.0.1").strip()
    if not browser_hub_host or "/" in browser_hub_host:
        pytest.fail("GPTADMIN_BROWSER_HUB_HOST must be a host name or IP address")
    base_url = f"http://127.0.0.1:{hub_port}"
    browser_base_url = f"http://{browser_hub_host}:{hub_port}"
    hub_bind_host = "127.0.0.1" if browser_hub_host in {"127.0.0.1", "localhost"} else "0.0.0.0"
    env = os.environ.copy()
    env.update(
        {
            "GPTADMIN_HUB_HOST": hub_bind_host,
            "GPTADMIN_HUB_PORT": str(hub_port),
            "PORT": str(hub_port),
            "CTL_TOKEN": control_token,
            "ADMIN_PASSWORD": admin_password,
            "OAUTH_CLIENT_SECRET": oauth_client_secret,
            "PUBLIC_ORIGIN": browser_base_url,
            "MCP_RESOURCE": browser_base_url,
            "GPTADMIN_CONFIG_DIR": str(tmp_path / "config"),
            "GPTADMIN_ROOT": str(ROOT),
            "NO_PROXY": "localhost,127.0.0.1",
            "no_proxy": "localhost,127.0.0.1",
            "HTTP_PROXY": "",
            "HTTPS_PROXY": "",
            "ALL_PROXY": "",
        }
    )
    process = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    callback = ThreadingHTTPServer((hub_bind_host, callback_port), _CallbackHandler)
    callback_thread = threading.Thread(target=callback.serve_forever, daemon=True)
    callback_thread.start()
    try:
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            status, _, _ = _request(base_url, "/healthz")
            if status == 200:
                break
            time.sleep(0.1)
        else:
            pytest.fail("disposable Hub did not become ready")

        # This is the exact OpenAPI document ChatGPT imports.  Keep its
        # unsupported operations and conflicting security schemes out.
        status, _, schema_bytes = _request(base_url, "/actions/openapi.yaml")
        assert status == 200
        schema = schema_bytes.decode()
        assert schema.count("bearerAuth:") == 2  # one security requirement + one scheme definition
        assert "X-GPTAdmin-Approval-ID" not in schema
        assert "/webhook-routes" not in schema
        assert "/network-proxy" not in schema
        assert "operationId: discover" in schema

        status, _, issued = _json_request(
            base_url,
            "/admin/api/mcp/issue-token",
            method="POST",
            token=control_token,
            payload={"client_id": "custom-gpt-browser-test", "ttl_days": 1, "access_mode": "readonly"},
        )
        assert status == 200
        bearer = str(issued["access_token"])
        status, _, discovered = _json_request(base_url, "/mcp-relay/servers", token=bearer)
        assert status == 200
        assert discovered.get("servers") is not None

        redirect_uri = f"http://{browser_hub_host}:{callback_port}/oauth-callback"
        status, _, registration = _json_request(base_url, "/register", method="POST", payload={"redirect_uris": [redirect_uri]})
        assert status == 201
        client_id = str(registration["client_id"])
        verifier = "custom-gpt-browser-pkce-verifier"
        authorize_query = urllib.parse.urlencode(
            {
                "response_type": "code",
                "client_id": client_id,
                "redirect_uri": redirect_uri,
                "resource": browser_base_url,
                "scope": "gptadmin.read",
                "state": "custom-gpt-browser-state",
                "code_challenge": _pkce_challenge(verifier),
                "code_challenge_method": "S256",
            }
        )
        with sync_playwright() as playwright:
            browser, owns_browser = _browser(playwright)
            context = browser.contexts[0] if browser.contexts else browser.new_context()
            page = context.new_page()
            page.goto(browser_base_url + "/oauth/authorize?" + authorize_query, wait_until="domcontentloaded")
            page.locator('input[name="password"]').fill(admin_password)
            page.get_by_role("button", name="Authorize").click()
            assert _CallbackHandler.event.wait(timeout=10), "browser OAuth callback was not reached"
            page.close()
            if owns_browser:
                browser.close()

        code = _CallbackHandler.query.get("code", "")
        assert code
        status, _, exchanged = _json_request(
            base_url,
            "/oauth/token",
            method="POST",
            form={
                "grant_type": "authorization_code",
                "code": code,
                "client_id": client_id,
                "redirect_uri": redirect_uri,
                "resource": browser_base_url,
                "code_verifier": verifier,
            },
        )
        assert status == 200
        oauth_bearer = str(exchanged["access_token"])
        status, _, oauth_discovery = _json_request(base_url, "/mcp-relay/servers", token=oauth_bearer)
        assert status == 200
        assert oauth_discovery.get("servers") is not None
    finally:
        callback.shutdown()
        callback.server_close()
        if process.poll() is None:
            os.killpg(os.getpgid(process.pid), signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                process.wait(timeout=5)
