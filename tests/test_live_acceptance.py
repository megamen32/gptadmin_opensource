"""Process-level contract for the optional live Hub acceptance runner."""

from __future__ import annotations

import http.cookiejar
import importlib.util
import json
import os
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


ROOT = Path(__file__).resolve().parents[1]
RUNNER_PATH = ROOT / "tests" / "e2e" / "live_acceptance.py"
SPEC = importlib.util.spec_from_file_location("live_acceptance", RUNNER_PATH)
assert SPEC and SPEC.loader
live_acceptance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(live_acceptance)


class _Handler(BaseHTTPRequestHandler):
    """Local logic fixture; actual acceptance uses the compiled Hub below."""

    calls: list[dict[str, Any]] = []
    failure: str | None = None

    def _send(self, status: int, payload: Any, content_type: str = "application/json") -> None:
        body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler contract
        if self.path == "/actions/openapi.yaml":
            self._send(200, b"openapi: 3.1.0\n", "application/yaml")
        elif self.path == "/connect.json":
            self._send(200, {"mcp_endpoint": "/mcp", "oauth_authorization_server": "/.well-known/oauth-authorization-server"})
        elif self.path == "/.well-known/oauth-authorization-server":
            self._send(200, {"authorization_endpoint": "http://127.0.0.1/oauth/authorize", "token_endpoint": "http://127.0.0.1/oauth/token"})
        elif self.path in {"/healthz", "/version"}:
            self._send(200, {"ok": True})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler contract
        if self.path != "/mcp" or self.headers.get("Authorization") != "Bearer test-bearer":
            self._send(401, {"error": "unauthorized"})
            return
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.calls.append(request)
        if request["method"] == "tools/list":
            self._send(200, {"jsonrpc": "2.0", "id": request["id"], "result": {"tools": [{"name": name} for name in ("ui", "discover", "schema", "execute", "job", "expand")]}})
        else:
            name = request["params"]["name"]
            if name == "discover":
                payload = {"servers": [{"server_id": "hub"}], "instructions": "Read-only local fixture.", "context": "fixture-context"}
            elif name == "schema":
                payload = {"response": {"tools": [{"name": "demo", "inputSchema": {"type": "object"}}]}, "call_ref": "fixture-ref"}
            else:
                payload = {"status": "ok", "capabilities": ["read_only_demo"]}
            result = {"structuredContent": payload}
            if name == "execute" and self.failure:
                payload.update({"status": "failed", "executed": False, "error": {"code": self.failure, "message": "test-bearer private-provider-text"}})
                if self.failure == "tool_error":
                    result["isError"] = True
            self._send(200, {"jsonrpc": "2.0", "id": request["id"], "result": result})

    def log_message(self, *_args: object) -> None:
        return


def test_live_runner_checks_public_and_authenticated_surfaces_without_echoing_bearer() -> None:
    """The runner must exercise the minimum deployment smoke without leaking auth."""

    server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    _Handler.calls = []
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        summary = live_acceptance.run_acceptance(
            f"http://127.0.0.1:{server.server_port}",
            "test-bearer",
            required_tools={"demo"},
        )
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()

    assert summary["status"] == "passed"
    assert summary["stages"] == ["health", "version", "connection", "oauth", "openapi", "mcp"]
    assert "test-bearer" not in json.dumps(summary)
    calls = [call["params"] for call in _Handler.calls if call["method"] == "tools/call"]
    assert [call["name"] for call in calls] == ["discover", "schema", "execute"]
    assert calls[1]["arguments"] == {"target": "hub", "tool": "demo", "detail": "full", "context": "fixture-context"}
    assert calls[2]["arguments"] == {"ref": "fixture-ref", "args": {}}


@pytest.mark.parametrize("failure", ["instructions_required", "tool_error"])
def test_live_runner_rejects_tool_failure_without_echoing_provider_data(failure: str) -> None:
    """HTTP success cannot turn a refused or failed tool into acceptance."""

    server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    _Handler.failure = failure
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with pytest.raises(live_acceptance.LiveAcceptanceError) as caught:
            live_acceptance.run_acceptance(f"http://127.0.0.1:{server.server_port}", "test-bearer")
        assert "test-bearer" not in str(caught.value)
        assert "private-provider-text" not in str(caught.value)
    finally:
        _Handler.failure = None
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()


@pytest.fixture
def disposable_real_hub(tmp_path: Path):
    """Build and run the actual Go Hub for a process-level live smoke."""

    binary = tmp_path / "gptadmin-hub"
    subprocess.run(
        ["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/gptadmin-hub"],
        cwd=ROOT / "go-hub",
        check=True,
        timeout=120,
    )
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        port = int(sock.getsockname()[1])
    config_dir = tmp_path / "config"
    config_dir.mkdir()
    env = os.environ.copy()
    env.update(
        {
            "GPTADMIN_HUB_HOST": "127.0.0.1",
            "GPTADMIN_HUB_PORT": str(port),
            "HUB_HOST": "127.0.0.1",
            "HUB_PORT": str(port),
            "PORT": str(port),
            "CTL_TOKEN": "ctl",
            "ADMIN_PASSWORD": "pw",
            "GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED": "1",
            "PUBLIC_ORIGIN": f"http://127.0.0.1:{port}",
            "MCP_RESOURCE": f"http://127.0.0.1:{port}",
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
    base_url = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"{base_url}/version", timeout=1) as response:
                if response.status == 200:
                    break
        except (OSError, urllib.error.URLError):
            time.sleep(0.1)
    else:
        process.kill()
        process.wait(timeout=5)
        pytest.fail("disposable Go Hub did not become ready")
    try:
        yield base_url
    finally:
        if process.poll() is None:
            os.killpg(os.getpgid(process.pid), signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                process.wait(timeout=5)


def test_live_runner_checks_actual_go_hub_process(disposable_real_hub: str) -> None:
    """The deployment runner must work against the real Hub binary, not only a mock."""

    summary = live_acceptance.run_acceptance(disposable_real_hub, "ctl", required_tools={"demo"})
    assert summary["status"] == "passed"
    assert summary["tool_count"] == 6


def test_admin_password_login_persists_across_redirect_refresh_and_api(disposable_real_hub: str) -> None:
    """Black-box login must retain its cookie through redirects, refresh and API access."""

    cookie_jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(cookie_jar),
    )

    with opener.open(
        urllib.request.Request(disposable_real_hub + "/admin/login", headers={"Accept": "text/html"}),
        timeout=10,
    ) as response:
        assert response.status == 200
        assert b'name="password"' in response.read(8192)

    login_request = urllib.request.Request(
        disposable_real_hub + "/admin/login",
        data=urllib.parse.urlencode({"password": "pw", "next": "/admin/"}).encode(),
        headers={"Accept": "text/html", "Content-Type": "application/x-www-form-urlencoded"},
        method="POST",
    )
    with opener.open(login_request, timeout=10) as response:
        login_body = response.read(32768)
        assert response.status == 200
        assert response.status == 200, {
            "url": response.geturl(),
            "cookies": [cookie.name for cookie in cookie_jar],
            "login_page": b"GPTAdmin Login" in login_body,
        }
        assert b"GPTAdmin Login" not in login_body

    session_cookie = next((cookie for cookie in cookie_jar if cookie.name == "gptadmin_admin_session"), None)
    assert session_cookie is not None

    duplicate_cookie_request = urllib.request.Request(
        disposable_real_hub + "/admin/",
        headers={
            "Accept": "text/html",
            "Cookie": f"gptadmin_admin_session=stale.invalid; gptadmin_admin_session={session_cookie.value}",
        },
    )
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(duplicate_cookie_request, timeout=10) as response:
        duplicate_cookie_body = response.read(32768)
        assert response.status == 200
        assert b"GPTAdmin Login" not in duplicate_cookie_body

    with opener.open(
        urllib.request.Request(disposable_real_hub + "/admin/", headers={"Accept": "text/html"}),
        timeout=10,
    ) as response:
        refresh_body = response.read(32768)
        assert response.status == 200
        assert response.geturl().endswith("/admin/")
        assert b"GPTAdmin Login" not in refresh_body

    with opener.open(
        urllib.request.Request(disposable_real_hub + "/admin/api/overview?limit=1", headers={"Accept": "application/json"}),
        timeout=10,
    ) as response:
        overview = json.loads(response.read())
        assert response.status == 200
        assert isinstance(overview.get("build"), dict)
