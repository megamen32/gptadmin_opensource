"""Fast units only: owner/auth/source/admission logic, no runtime acceptance.

Expected 1s, maximum 5s. Real SDK/module/service tests are a separate gate.
"""
import asyncio
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("python_mcp_http", Path(__file__).parents[1] / "tools/python_mcp_http.py")
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class BindingTests(unittest.TestCase):
    def setUp(self):
        self.area = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.addCleanup(self.area.cleanup)
        root = Path(self.area.name)
        self.module = root / "module.py"
        self.module.write_bytes(b"# synthetic source, never executed\n")
        self.token = root / "token"
        self.token.write_bytes(b"a" * 64)
        self.token.chmod(0o600)
        self.config = root / "owner.json"
        self.value = {"schema": 1, "module": str(self.module), "module_sha256": hashlib.sha256(self.module.read_bytes()).hexdigest(), "token_file": str(self.token), "port": 19470}
        self.seal()

    def seal(self):
        self.config.write_text(json.dumps(self.value))
        self.config.chmod(0o600)

    def test_exact_source_and_separate_upstream_token(self):
        config, source, token = bridge.load_binding(self.config)
        self.assertEqual(config, self.value)
        self.assertEqual(source, self.module.read_bytes())
        self.assertEqual(token, b"a" * 64)

    def test_source_drift_refuses_before_any_module_execution(self):
        self.module.write_bytes(b"# replaced source\n")
        with self.assertRaisesRegex(ValueError, "source drift"):
            bridge.load_binding(self.config)

    def test_symlink_and_public_token_refused(self):
        self.token.chmod(0o644)
        with self.assertRaises(ValueError): bridge.load_binding(self.config)
        self.token.chmod(0o600)
        link = self.config.with_suffix(".link")
        link.symlink_to(self.config)
        with self.assertRaises(OSError): bridge.load_binding(link)

    def test_unknown_config_and_nonloopback_binding_refused(self):
        for change in [{"host": "0.0.0.0"}, {"port": True}, {"port": 80}, {"module_sha256": "unsealed"}]:
            before = self.value.copy(); self.value.update(change); self.seal()
            with self.assertRaises(ValueError): bridge.load_binding(self.config)
            self.value = before


class OwnerHTTPTests(unittest.IsolatedAsyncioTestCase):
    async def test_foreign_missing_and_duplicate_auth_never_reach_backend(self):
        reached = []
        async def app(*args): reached.append(True)
        owner = bridge.OwnerHTTP(app, b"a" * 64)
        for headers in [[], [(b"authorization", b"Bearer " + b"b" * 64)], [(b"authorization", owner.expected)] * 2]:
            sent = []
            async def send(message): sent.append(message)
            await owner({"type": "http", "path": "/mcp", "headers": headers}, None, send)
            self.assertEqual(sent[0]["status"], 401)
        self.assertFalse(reached)

    async def test_request_ids_and_body_are_not_rewritten(self):
        body = {"type": "http.request", "body": b'{"id":"own-exact"}', "more_body": False}
        captured = []
        async def app(scope, receive, send): captured.append(await receive())
        async def receive(): return body
        owner = bridge.OwnerHTTP(app, b"a" * 64)
        await owner({"type": "http", "path": "/mcp", "headers": [(b"Authorization", owner.expected)]}, receive, None)
        self.assertEqual(captured, [body])

    async def test_busy_owner_fails_without_queue_or_backend(self):
        entered, release = asyncio.Event(), asyncio.Event()
        async def app(*args): entered.set(); await release.wait()
        owner = bridge.OwnerHTTP(app, b"a" * 64, maximum_active=1)
        scope = {"type": "http", "path": "/mcp", "headers": [(b"authorization", owner.expected)]}
        first = asyncio.create_task(owner(scope, None, None)); await entered.wait()
        sent = []
        async def send(message): sent.append(message)
        await owner(scope, None, send)
        self.assertEqual(sent[0]["status"], 503)
        release.set(); await first
        self.assertFalse(owner.slots.locked())

    async def test_cancellation_releases_owned_admission(self):
        entered = asyncio.Event()
        async def app(*args): entered.set(); await asyncio.Event().wait()
        owner = bridge.OwnerHTTP(app, b"a" * 64, maximum_active=1)
        scope = {"type": "http", "path": "/mcp", "headers": [(b"authorization", owner.expected)]}
        task = asyncio.create_task(owner(scope, None, None)); await entered.wait(); task.cancel()
        with self.assertRaises(asyncio.CancelledError): await task
        self.assertFalse(owner.slots.locked())

    async def test_lifespan_reaches_native_sdk_and_other_paths_refuse(self):
        reached = []
        async def app(scope, *args): reached.append(scope["type"])
        owner = bridge.OwnerHTTP(app, b"a" * 64)
        await owner({"type": "lifespan"}, None, None)
        sent = []
        async def send(message): sent.append(message)
        await owner({"type": "http", "path": "/other", "headers": []}, None, send)
        self.assertEqual(reached, ["lifespan"])
        self.assertEqual(sent[0]["status"], 404)


if __name__ == "__main__":
    unittest.main()
