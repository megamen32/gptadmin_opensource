"""Regression tests for the redacted admin password-flow runner."""

from __future__ import annotations

import importlib.util
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, ClassVar

import pytest

ROOT = Path(__file__).resolve().parents[1]
RUNNER_PATH = ROOT / "tests" / "e2e" / "admin_user_flow.py"
SPEC = importlib.util.spec_from_file_location("admin_user_flow", RUNNER_PATH)
assert SPEC and SPEC.loader
admin_user_flow = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(admin_user_flow)


class _AdminHandler(BaseHTTPRequestHandler):
    """Minimal admin surface that records the runner's exact request paths."""

    paths: ClassVar[list[str]] = []

    def _send(self, status: int, payload: Any, content_type: str = "text/html") -> None:
        body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler contract
        type(self).paths.append(self.path)
        if self.path == "/admin/login":
            self._send(200, b'<form><input name="password"></form>')
        elif self.path == "/admin/":
            self._send(200, b"GPTAdmin")
        elif self.path == "/admin/api/overview?limit=1":
            self._send(200, {"ok": True}, "application/json")
        elif self.path == "/admin/api/access-profiles":
            self._send(200, {"profiles": []}, "application/json")
        else:
            self._send(404, b"not found")

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler contract
        type(self).paths.append(self.path)
        if self.path == "/admin/login":
            self.send_response(200)
            self.send_header("Set-Cookie", "gptadmin_admin_session=test; Path=/")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self._send(404, b"not found")

    def log_message(self, *_args: object) -> None:
        return


def test_admin_page_url_is_normalized_to_hub_origin() -> None:
    """An admin page URL must not produce a duplicated /admin path."""

    _AdminHandler.paths = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), _AdminHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        summary = admin_user_flow.run_admin_user_flow(
            f"http://127.0.0.1:{server.server_port}/admin/",
            "test-password",
        )
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()

    assert summary["base_url"] == f"http://127.0.0.1:{server.server_port}"
    assert _AdminHandler.paths == [
        "/admin/login",
        "/admin/login",
        "/admin/",
        "/admin/api/overview?limit=1",
        "/admin/api/access-profiles",
    ]


@pytest.mark.parametrize(
    "base_url",
    [
        "https://hub.example.test/other",
        "http://[::1",
        "https://hub.example.test:bad-port",
        "https://hub.example.test\n.evil",
        "https://hub.example.test\t",
        "https:// hub.example.test",
        "https://hub.example.test\x7f",
        "https://hub.example.\u00a0test",
        "https://hub.example\\test",
        "https://%68ub.example.test",
        "https://0x7f000001",
        "https://0x7f.0.0.1",
    ],
)
def test_invalid_origin_is_rejected_before_creating_a_network_opener(
    monkeypatch: pytest.MonkeyPatch,
    base_url: str,
) -> None:
    """Only a Hub origin or its admin page is safe runner input."""

    def unexpected_network_opener(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("invalid input must not create a network opener")

    monkeypatch.setattr(admin_user_flow.urllib.request, "build_opener", unexpected_network_opener)

    with pytest.raises(admin_user_flow.AdminFlowError, match="Hub origin or /admin page"):
        admin_user_flow.run_admin_user_flow(base_url, "test-password")


def test_ipv6_hub_origin_is_accepted() -> None:
    """A host may be an IPv6 literal with an optional port."""

    assert admin_user_flow._hub_origin("http://[::1]") == "http://[::1]"
    assert admin_user_flow._hub_origin("http://[::1]:9001") == "http://[::1]:9001"


def test_dns_label_containing_0x_is_accepted() -> None:
    """The legacy numeric check must not reject ordinary DNS labels."""

    assert admin_user_flow._hub_origin("https://0xample.test") == "https://0xample.test"
