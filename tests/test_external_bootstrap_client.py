"""Local client logic and HTTP framing tests; not deployed-Hub acceptance."""
import base64
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CLIENT_PATH = Path(__file__).resolve().parents[1] / "go-hub/internal/hub/bootstrap_assets/gptadmin.py"
SPEC = importlib.util.spec_from_file_location("gptadmin_bootstrap_client", CLIENT_PATH)
client = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(client)


@contextlib.contextmanager
def server(responder):
    requests = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def run_request(self):
            raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            payload = json.loads(raw) if raw else None
            requests.append({"method": self.command, "path": self.path, "headers": self.headers, "body": payload})
            response = responder(self, payload)
            if response is None:
                self.connection.shutdown(socket.SHUT_RDWR)
                self.connection.close()
                return
            status, headers, data = response
            if isinstance(data, dict):
                data = json.dumps(data).encode()
                headers = {"Content-Type": "application/json", **headers}
            elif isinstance(data, str):
                data = data.encode()
            self.send_response(status)
            for key, value in headers.items():
                self.send_header(key, value)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        do_POST = run_request
        do_GET = run_request

    httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        yield "http://127.0.0.1:" + str(httpd.server_port), requests
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join()


def config(url):
    return {"mcp_url": url + "/mcp", "access_token": "fixture-token",
            "protocol_version": client.MODERN, "expires_at": time.time() + 600, "profile_id": "fixture"}


def reply(payload, result):
    return 200, {}, {"jsonrpc": "2.0", "id": payload["id"], "result": result}


def tool_list(payload, tools=None):
    return reply(payload, {"resultType": "complete", "tools": tools or [
        {"name": "exec", "inputSchema": {"type": "object"}},
        {"name": "echo", "inputSchema": {"type": "object"}},
    ]})


class ProtocolTests(unittest.TestCase):
    def test_modern_headers_metadata_structured_false_and_nested_header(self):
        definition = {"name": "echo", "inputSchema": {"type": "object", "properties": {
            "nested": {"type": "object", "properties": {
                "tenant": {"type": "string", "x-mcp-header": "Tenant"},
            }},
        }}}

        def respond(handler, payload):
            if payload["method"] == "tools/list":
                return tool_list(payload, [definition])
            return reply(payload, {"resultType": "complete", "content": [], "structuredContent": False})

        with server(respond) as (url, requests):
            result = client.Client(config(url)).call_tool("echo", {"nested": {"tenant": " привет "}})
        self.assertIs(client.result_data(result), False)
        call = requests[-1]
        self.assertEqual(call["headers"]["Authorization"], "Bearer fixture-token")
        self.assertEqual(call["headers"]["Mcp-Method"], "tools/call")
        self.assertEqual(call["headers"]["Mcp-Name"], "echo")
        self.assertEqual(call["headers"]["Mcp-Param-Tenant"], client.header_value(" привет "))
        self.assertEqual(call["headers"]["MCP-Protocol-Version"], client.MODERN)
        self.assertIn("text/event-stream", call["headers"]["Accept"])
        meta = call["body"]["params"]["_meta"]
        self.assertEqual(meta["io.modelcontextprotocol/protocolVersion"], client.MODERN)
        self.assertEqual(meta["io.modelcontextprotocol/clientCapabilities"], {})
        self.assertEqual([r["body"]["method"] for r in requests], ["tools/list", "tools/call"])

    def test_sse_comments_progress_multiline_and_final_id(self):
        def respond(handler, payload):
            message = json.dumps({"jsonrpc": "2.0", "id": payload["id"],
                                  "result": {"resultType": "complete", "value": "x[m y[h z[c"}})
            split = message.index('"result"')
            data = ": keepalive\r\n\r\ndata: " + json.dumps({
                "jsonrpc": "2.0", "method": "notifications/progress", "params": {"progress": 1},
            }) + "\r\n\r\nevent: message\r\ndata: " + message[:split] + "\r\ndata: " + message[split:] + "\r\n\r\n"
            return 200, {"Content-Type": "text/event-stream"}, data

        with server(respond) as (url, _):
            result = client.Client(config(url)).discover()
        self.assertEqual(result["value"], "x[m y[h z[c")

    def test_error_body_and_write_not_replayed(self):
        def respond(handler, payload):
            if payload["method"] == "tools/list":
                return tool_list(payload)
            return 400, {}, {"jsonrpc": "2.0", "id": payload["id"], "error": {
                "code": -32022, "message": "Unsupported protocol version", "data": {"supported": ["other"]},
            }}

        with server(respond) as (url, requests):
            with self.assertRaises(client.RPCError) as error:
                client.Client(config(url)).call_tool("exec", {"code": "write()"})
        self.assertEqual(error.exception.error["code"], -32022)
        self.assertEqual(len([r for r in requests if r["body"]["method"] == "tools/call"]), 1)

    def test_lost_write_response_not_replayed(self):
        def respond(handler, payload):
            return tool_list(payload) if payload["method"] == "tools/list" else None

        with server(respond) as (url, requests):
            with self.assertRaises(client.TransportError):
                client.Client(config(url)).call_tool("exec", {"code": "write()"})
        self.assertEqual(len([r for r in requests if r["body"]["method"] == "tools/call"]), 1)

    def test_redirect_cannot_forward_credentials(self):
        with server(lambda h, p: reply(p, {})) as (other, other_requests):
            with server(lambda h, p: (307, {"Location": other + "/stolen"}, b"")) as (url, requests):
                with self.assertRaises(client.TransportError):
                    client.Client(config(url)).discover()
        self.assertEqual(len(requests), 1)
        self.assertEqual(other_requests, [])

    def test_legacy_is_explicit_and_keeps_session(self):
        def respond(handler, payload):
            if payload["method"] == "initialize":
                status, headers, body = reply(payload, {"protocolVersion": client.LEGACY, "capabilities": {}})
                return status, {"Mcp-Session-Id": "fixture-session"}, body
            if payload["method"] == "notifications/initialized":
                return 202, {}, b""
            return reply(payload, {"content": [{"type": "text", "text": "ok"}]})

        with server(respond) as (url, requests):
            instance = client.Client(config(url), legacy=True)
            instance.call_tool("echo")
        self.assertEqual([r["body"]["method"] for r in requests], [
            "initialize", "notifications/initialized", "tools/call",
        ])
        self.assertEqual(requests[-1]["headers"]["Mcp-Session-Id"], "fixture-session")
        self.assertEqual(requests[-1]["headers"]["MCP-Protocol-Version"], client.LEGACY)
        self.assertNotIn("Mcp-Method", requests[-1]["headers"])

    def test_data_values_and_error_remain_distinct(self):
        for data in (None, False, [], "", 0, {"a": 1}):
            self.assertEqual(client.result_data({"structuredContent": data, "content": []}), data)
        failure = {"isError": True, "structuredContent": {"message": "bad"}}
        self.assertTrue(client.result_failed(failure))
        self.assertTrue(client.result_failed({"structuredContent": {"status": "failed"}}))
        self.assertFalse(client.result_failed({"structuredContent": False}))

    def test_invalid_header_schema_and_parameter_rejected(self):
        for schema in (
            {"allOf": [{"type": "string", "x-mcp-header": "Bad"}]},
            {"properties": {"foo": {"type": "string", "x-mcp-header": "bad\r\nInjected"}}},
            {"properties": {"foo": {"type": "number", "x-mcp-header": "Number"}}},
        ):
            with self.assertRaises(client.ClientError):
                client.header_fields(schema)
        self.assertEqual(client.header_value(True), "true")
        self.assertTrue(client.header_value("=?base64?literal?=").startswith("=?base64?PT"))

    def test_expiry_stops_network_calls(self):
        with server(lambda h, p: reply(p, {})) as (url, requests):
            values = config(url)
            values["expires_at"] = time.time() - 1
            with self.assertRaises(client.ClientError):
                client.Client(values)
        self.assertEqual(requests, [])

    def test_cli_redacts_bearer_in_rpc_error(self):
        def respond(handler, payload):
            return 401, {}, {"jsonrpc": "2.0", "id": payload["id"],
                             "error": {"code": -32000, "message": "fixture-token"}}

        with tempfile.TemporaryDirectory() as directory, server(respond) as (url, _):
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(config(url)))
            output, errors = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                code = client.main(["--config", str(path), "discover"])
            self.assertEqual(code, 1)
            self.assertNotIn("fixture-token", output.getvalue() + errors.getvalue())
            self.assertIn("<redacted>", errors.getvalue())

    def test_bootstrap_private_files_and_no_token_in_result(self):
        with tempfile.TemporaryDirectory() as directory:
            def respond(handler, payload):
                url = "http://127.0.0.1:" + str(handler.server.server_port)
                if handler.path.endswith("/manifest.json"):
                    return 200, {}, {"client_sha256": hashlib.sha256(CLIENT_PATH.read_bytes()).hexdigest()}
                if handler.path.endswith("/config.json"):
                    return 200, {}, config(url)
                return 200, {"Content-Type": "text/markdown"}, "Read LHC, then Entity Finder.\n"

            with server(respond) as (url, requests):
                path = Path(directory) / ".gptadmin.json"
                result = client.bootstrap(url + "/bootstrap/fixture", path)
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)
            instructions = Path(directory) / "GPTADMIN_INSTRUCTIONS.md"
            self.assertEqual(os.stat(instructions).st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(path.read_text())["access_token"], "fixture-token")
            self.assertNotIn("fixture-token", json.dumps(result))
            self.assertTrue(all(request["method"] == "GET" for request in requests))

    def test_bootstrap_rejects_credential_for_other_origin(self):
        def respond(handler, payload):
            if handler.path.endswith("/manifest.json"):
                return 200, {}, {"client_sha256": hashlib.sha256(CLIENT_PATH.read_bytes()).hexdigest()}
            return 200, {}, config("https://example.invalid")

        with tempfile.TemporaryDirectory() as directory, server(respond) as (url, requests):
            path = Path(directory) / ".gptadmin.json"
            with self.assertRaises(client.ClientError):
                client.bootstrap(url + "/bootstrap/fixture", path)
            self.assertFalse(path.exists())
            self.assertEqual(len(requests), 2)


    def test_bootstrap_stdin_keeps_capability_out_of_argv(self):
        def respond(handler, payload):
            url = "http://127.0.0.1:" + str(handler.server.server_port)
            if handler.path.endswith("/manifest.json"):
                return 200, {}, {"client_sha256": hashlib.sha256(CLIENT_PATH.read_bytes()).hexdigest()}
            if handler.path.endswith("/config.json"):
                return 200, {}, config(url)
            return 200, {"Content-Type": "text/markdown"}, "Current instructions.\n"

        with tempfile.TemporaryDirectory() as directory, server(respond) as (url, requests):
            capability = url + "/bootstrap/private-capability"
            path = Path(directory) / ".gptadmin.json"
            output, errors = io.StringIO(), io.StringIO()
            previous_stdin = client.sys.stdin
            try:
                client.sys.stdin = io.StringIO(capability + "\n")
                with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                    code = client.main(["--config", str(path), "bootstrap", "-"])
            finally:
                client.sys.stdin = previous_stdin
            self.assertEqual(code, 0, errors.getvalue())
            self.assertEqual(json.loads(path.read_text())["access_token"], "fixture-token")
            visible = output.getvalue() + errors.getvalue()
            self.assertNotIn(capability, visible)
            self.assertNotIn("fixture-token", visible)
            self.assertEqual(len(requests), 3)


    def test_global_context_reaches_shell_wrapper_without_clobbering_exec_context(self):
        def respond(handler, payload):
            return tool_list(payload) if payload["method"] == "tools/list" else reply(payload, {
                "structuredContent": {"status": "completed", "result": {"exit_code": 0, "stdout": "ok"}},
            })

        with tempfile.TemporaryDirectory() as directory, server(respond) as (url, requests):
            path = Path(directory) / ".gptadmin.json"
            path.write_text(json.dumps(config(url)))
            output, errors = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                code = client.main([
                    "--config", str(path), "--context", "explicit-receipt",
                    "shell", "shell:fixture", "--", "printf", "ok",
                ])
            self.assertEqual(code, 0, errors.getvalue())
            request = requests[-1]["body"]["params"]["arguments"]
            self.assertEqual(request["context"], "explicit-receipt")
            self.assertIn("shell_exec", request["code"])
            self.assertEqual(len([r for r in requests if r["body"]["method"] == "tools/call"]), 1)
        args = client.build_parser().parse_args([
            "--context", "global-receipt", "exec", "--context", "specific-receipt", "--code", "return 1;",
        ])
        self.assertEqual(args.instruction_context, "global-receipt")
        self.assertEqual(args.context, "specific-receipt")


class TransferFixture(client.Client):
    """Pure transfer algorithm fixture. It is not a native file-transfer server."""
    def __init__(self, payload=b"", corrupt=False, lose_receipt=False):
        self.payload = payload
        self.corrupt = corrupt
        self.lose_receipt = lose_receipt
        self.calls, self.received = [], bytearray()

    def transfer(self, target, action, path, **args):
        self.calls.append((action, args))
        if action == "upload_begin":
            self.expected = args
            return {"transfer_id": "fixture-upload", "chunk_bytes": client.CHUNK_BYTES}
        if action == "upload_chunk":
            if args["offset"] == len(self.received):
                block = base64.b64decode(args["data_base64"], validate=True)
                assert hashlib.sha256(block).hexdigest() == args["sha256"]
                self.received.extend(block)
            if self.lose_receipt:
                self.lose_receipt = False
                raise client.TransportError("Receipt lost")
            return {"received": len(self.received)}
        if action == "upload_status":
            return {"received": len(self.received)}
        if action == "upload_commit":
            assert len(self.received) == self.expected["size"]
            assert hashlib.sha256(self.received).hexdigest() == self.expected["sha256"]
            return {"status": "committed", "sha256": self.expected["sha256"]}
        if action == "download_begin":
            return {"transfer_id": "fixture-download", "size": len(self.payload),
                    "sha256": hashlib.sha256(self.payload).hexdigest()}
        if action == "download_chunk":
            block = self.payload[args["offset"]:args["offset"] + args["max_bytes"]]
            return {"data_base64": base64.b64encode(block).decode(),
                    "chunk_sha256": "0" * 64 if self.corrupt else hashlib.sha256(block).hexdigest(),
                    "next_offset": args["offset"] + len(block),
                    "eof": args["offset"] + len(block) == len(self.payload)}
        if action in ("download_abort", "upload_abort"):
            return {}
        raise AssertionError(action)


class TransferTests(unittest.TestCase):
    def test_binary_multiple_chunks_and_receipt_recovery(self):
        data = bytes(range(256)) * 300 + "привет [m [h [c".encode()
        with tempfile.TemporaryDirectory() as directory:
            source, destination = Path(directory) / "source", Path(directory) / "destination"
            source.write_bytes(data)
            instance = TransferFixture(data, lose_receipt=True)
            uploaded = instance.upload("file:fixture", source, "/fixture/new-file")
            downloaded = instance.download("file:fixture", "/fixture/new-file", destination)
            self.assertEqual(bytes(instance.received), data)
            self.assertEqual(destination.read_bytes(), data)
            self.assertEqual(uploaded["sha256"], downloaded["sha256"])
            self.assertEqual(os.stat(destination).st_mode & 0o777, 0o600)
            actions = [action for action, _ in instance.calls]
            self.assertIn("upload_status", actions)
            self.assertEqual(actions.count("upload_chunk"), 3)
            self.assertIn("upload_abort", actions)
            self.assertEqual(actions[-1], "download_abort")

    def test_corrupt_chunk_not_published(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "destination"
            instance = TransferFixture(b"bad bytes", corrupt=True)
            with self.assertRaises(client.ClientError):
                instance.download("file:fixture", "/fixture/file", destination)
            self.assertFalse(destination.exists())
            self.assertEqual(list(Path(directory).iterdir()), [])
            self.assertEqual(instance.calls[-1][0], "download_abort")

    def test_existing_destination_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "destination"
            destination.write_bytes(b"keep")
            instance = TransferFixture(b"replace")
            with self.assertRaises(client.ClientError):
                instance.download("file:fixture", "/fixture/file", destination)
            self.assertEqual(destination.read_bytes(), b"keep")
            self.assertEqual(instance.calls, [])

    def test_empty_file(self):
        with tempfile.TemporaryDirectory() as directory:
            source, destination = Path(directory) / "empty", Path(directory) / "download"
            source.write_bytes(b"")
            instance = TransferFixture()
            instance.upload("file:fixture", source, "/fixture/new")
            instance.download("file:fixture", "/fixture/new", destination)
            self.assertEqual(destination.read_bytes(), b"")

    def test_shell_keeps_argv_and_stdin_literal(self):
        class Capture:
            def call_target(self, target, tool, arguments):
                return {"target": target, "tool": tool, "arguments": arguments}

        with tempfile.TemporaryDirectory() as directory:
            script = Path(directory) / "script.py"
            script.write_text('print("x[m", "$HOME", "`id`")\n')
            args = client.build_parser().parse_args([
                "shell", "--cwd", "/tmp", "--stdin", str(script),
                "shell:fixture", "--", "python3", "-",
            ])
            result = client.dispatch(Capture(), args)
            self.assertEqual(result["arguments"]["argv"], ["python3", "-"])
            self.assertEqual(result["arguments"]["stdin"], script.read_text())
            self.assertNotIn("cmd", result["arguments"])


if __name__ == "__main__":
    unittest.main()
