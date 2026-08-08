"""Contract for the secret-safe live credential acceptance matrix."""

from __future__ import annotations

import importlib.util
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MATRIX_PATH = ROOT / "tests" / "e2e" / "credential_matrix.py"


class _MatrixHubHandler(BaseHTTPRequestHandler):
    """Disposable authenticated surface for all supported credential paths."""

    bearer = "matrix-secret-bearer"

    def _send(self, status: int, payload: dict[str, object]) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler contract
        if self.path != "/mcp-relay/servers" or self.headers.get("Authorization") != f"Bearer {self.bearer}":
            self._send(401, {"error": "unauthorized"})
            return
        self._send(200, {"servers": []})

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler contract
        if self.path not in {"/mcp", "/server/hub/mcp"} or self.headers.get("Authorization") != f"Bearer {self.bearer}":
            self._send(401, {"error": "unauthorized"})
            return
        self._send(200, {"jsonrpc": "2.0", "id": 1, "result": {"tools": [{"name": "demo"}]}})

    def log_message(self, *_args: object) -> None:
        return


def test_credential_matrix_probes_each_entitled_path_without_exposing_bearers() -> None:
    """Every inventory credential must be exercised on each named path safely."""

    assert MATRIX_PATH.is_file(), "credential matrix runner is missing"
    spec = importlib.util.spec_from_file_location("credential_matrix", MATRIX_PATH)
    assert spec and spec.loader
    matrix = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(matrix)

    server = ThreadingHTTPServer(("127.0.0.1", 0), _MatrixHubHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        summary = matrix.run_credential_matrix(
            f"http://127.0.0.1:{server.server_port}",
            [{"id": "preexisting-codex", "env": "TEST_MATRIX_BEARER", "paths": ["custom", "mcp_remote", "relay_vrp"]}],
            {"TEST_MATRIX_BEARER": _MatrixHubHandler.bearer},
        )
    finally:
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()

    encoded = json.dumps(summary)
    assert summary == {
        "status": "passed",
        "credentials": [{"id": "preexisting-codex", "paths": ["custom", "mcp_remote", "relay_vrp"]}],
    }
    assert _MatrixHubHandler.bearer not in encoded
