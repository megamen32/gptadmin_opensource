"""Fast unit: transport protocol only; simulated upstream is not native acceptance.

Purpose: expired pre-dispatch refusals recover, admitted/uncertain actions never
replay. Expected 5s, emergency 50s under gptadmin-focused100. The separate native
ZCode scenario waits the actual five-minute TTL and is focused integration.
"""
import json
import hashlib
import importlib.util
import select
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

HELPER = Path(__file__).parents[1] / "tools/managed_mcp_stdio.py"
EXPIRED = "connection session expired or unavailable; initialize again"


class TransportTests(unittest.TestCase):
    def setUp(self):
        self.state = {"initializes": 0, "actions": [], "headers": [], "refusal": None, "closed": []}
        state = self.state

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_DELETE(self):
                state["closed"].append(self.headers.get("Mcp-Session-Id"))
                self.send_response(204)
                self.end_headers()

            def do_POST(self):
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                state["headers"].append({k.lower(): v for k, v in self.headers.items()})
                method = request["method"]
                if method == "initialize":
                    state["initializes"] += 1
                    self.send_response(200)
                    self.send_header("Mcp-Session-Id", "session-" + str(state["initializes"]))
                    self.end_headers()
                    self.wfile.write(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": {
                        "protocolVersion": "2025-03-26", "capabilities": {"tools": {}},
                        "serverInfo": {"name": "unit", "version": "1"}}}).encode())
                    return
                if method == "notifications/initialized":
                    self.send_response(204)
                    self.end_headers()
                    return
                refusal = state["refusal"]
                if refusal:
                    if refusal[0] == "once":
                        state["refusal"] = None
                        status, detail = refusal[1:]
                    elif refusal[0] == "always":
                        status, detail = refusal[1:]
                    elif refusal[0] == "accepted_disconnect":
                        state["actions"].append(request["params"]["arguments"]["marker"])
                        self.connection.shutdown(socket.SHUT_RDWR)
                        self.connection.close()
                        return
                    self.send_response(status)
                    self.end_headers()
                    self.wfile.write(json.dumps({"detail": detail}).encode())
                    return
                state["actions"].append(request["params"]["arguments"]["marker"])
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({"jsonrpc": "2.0", "id": request["id"],
                                            "result": {"content": [{"type": "text", "text": "ok"}]}}).encode())

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.temp = tempfile.TemporaryDirectory()
        upstream = Path(self.temp.name) / "upstream.json"
        upstream.write_text(json.dumps({"url": "http://127.0.0.1:%s/connection/unit/mcp" % self.server.server_port,
                                        "headers": {"Authorization": "Bearer UNIT_ONLY"}}))
        self.child = subprocess.Popen([sys.executable, str(HELPER), "--upstream", str(upstream)],
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                      text=True, bufsize=1)
        self.addCleanup(self.cleanup)
        self.send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "unit", "version": "1"}}})
        self.assertIn("result", self.response())
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def cleanup(self):
        if not self.child.stdin.closed:
            self.child.stdin.close()
        try:
            self.child.wait(timeout=3)
        except subprocess.TimeoutExpired:
            self.child.kill()
            self.child.wait()
        self.child.stdout.close()
        self.child.stderr.close()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.temp.cleanup()

    def send(self, request):
        self.child.stdin.write(json.dumps(request) + "\n")
        self.child.stdin.flush()

    def response(self):
        self.assertTrue(select.select([self.child.stdout], [], [], 6)[0], "Bounded stdio response missing")
        line = self.child.stdout.readline()
        self.assertTrue(line, self.child.stderr.read() if self.child.poll() is not None else "Bridge closed")
        return json.loads(line)

    def action(self, marker, request_id=2):
        self.send({"jsonrpc": "2.0", "id": request_id, "method": "tools/call",
                   "params": {"name": "owned_action", "arguments": {"marker": marker}}})
        return self.response()

    def test_exact_expired_refusal_reinitializes_and_executes_action_once(self):
        self.assertIn("result", self.action("before"))
        self.state["refusal"] = ("once", 404, EXPIRED)
        result = self.action("after", 3)
        self.assertEqual(result["id"], 3)
        self.assertIn("result", result)
        self.assertEqual(self.state["initializes"], 2)
        self.assertEqual(self.state["actions"], ["before", "after"])
        self.assertTrue(all(h.get("authorization") == "Bearer UNIT_ONLY" for h in self.state["headers"]))
        self.assertEqual(self.state["headers"][-1].get("mcp-session-id"), "session-2")
        self.assertEqual(self.state["headers"][-1].get("mcp-method"), "tools/call")
        self.assertEqual(self.state["headers"][-1].get("mcp-name"), "owned_action")

    def test_stdio_eof_closes_its_managed_scope(self):
        self.child.stdin.close()
        self.child.wait(timeout=3)
        self.assertEqual(self.state["closed"], ["session-1"])

    def test_response_lost_after_admission_never_replays(self):
        self.state["refusal"] = ("accepted_disconnect",)
        self.assertIn("error", self.action("once"))
        self.assertEqual(self.state["actions"], ["once"])
        self.assertEqual(self.state["initializes"], 1)

    def test_ordinary_404_never_reinitializes_or_replays(self):
        self.state["refusal"] = ("once", 404, "tool or resource does not exist")
        self.assertIn("error", self.action("not-admitted"))
        self.assertEqual(self.state["actions"], [])
        self.assertEqual(self.state["initializes"], 1)

    def test_auth_failure_never_reinitializes_or_replays(self):
        self.state["refusal"] = ("once", 401, EXPIRED)
        self.assertIn("error", self.action("not-admitted"))
        self.assertEqual(self.state["actions"], [])
        self.assertEqual(self.state["initializes"], 1)

    def test_repeated_expiry_has_one_recovery_attempt_only(self):
        self.state["refusal"] = ("always", 404, EXPIRED)
        self.assertIn("error", self.action("not-admitted"))
        self.assertEqual(self.state["actions"], [])
        self.assertEqual(self.state["initializes"], 2)


class InstallTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location(
            "installer", HELPER.with_name("install_zcode_mcp_recovery.py"))
        self.installer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.installer)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = self.root / "config.json"
        self.original = {"model": "preserved", "hooks": {"unchanged": True}, "mcp": {"servers": {
            "agent-herder": {"type": "http", "url": "https://unit.invalid/connection/owned/mcp",
                             "headers": {"Authorization": "Bearer UNIT_ONLY"}, "enabled": True},
            "disabled": {"enabled": False, "command": "never-start"}}}}
        self.config.write_text(json.dumps(self.original))
        self.state = self.root / "private"

    def install(self, expected=None):
        expected = expected or hashlib.sha256(self.config.read_bytes()).hexdigest()
        return self.installer.install(self.config, "agent-herder", self.state, HELPER, expected)

    def test_install_preserves_other_definitions_and_protects_credentials(self):
        raw = self.config.read_bytes()
        receipt = self.install()
        installed = json.loads(self.config.read_text())
        entry = installed["mcp"]["servers"].pop("agent-herder")
        original = json.loads(raw)
        original["mcp"]["servers"].pop("agent-herder")
        self.assertEqual(installed, original)
        self.assertEqual(entry["type"], "stdio")
        self.assertNotIn("headers", entry)
        upstream = Path(entry["args"][-1])
        self.assertEqual(json.loads(upstream.read_text()), self.original["mcp"]["servers"]["agent-herder"])
        self.assertEqual(upstream.stat().st_mode & 0o777, 0o600)
        self.assertEqual(Path(receipt["backup"]).read_bytes(), raw)
        self.assertNotIn("UNIT_ONLY", json.dumps(receipt))
        self.assertFalse(self.install()["changed"])

    def test_stale_review_refuses_config_write(self):
        expected = hashlib.sha256(self.config.read_bytes()).hexdigest()
        self.config.write_text(json.dumps({**self.original, "foreign_edit": True}))
        before = self.config.read_bytes()
        with self.assertRaises(RuntimeError):
            self.install(expected)
        self.assertEqual(self.config.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
