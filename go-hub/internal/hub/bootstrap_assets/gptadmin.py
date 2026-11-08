#!/usr/bin/env python3
"""GPTAdmin client for an external Python 3 sandbox. Standard library only."""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

VERSION = "1.1.0"
MODERN = "2026-07-28"
LEGACY = "2025-11-25"
MAX_RESPONSE = 16 * 1024 * 1024
MAX_FILE = 16 * 1024 * 1024
CHUNK_BYTES = 32768
HEADER_NAME = re.compile(r"^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")


class ClientError(Exception):
    pass


class TransportError(ClientError):
    pass


class RPCError(ClientError):
    def __init__(self, error, status=None):
        self.error, self.status = error, status
        super().__init__("MCP error " + json.dumps(error, ensure_ascii=False))


def origin(url):
    try:
        parsed = urllib.parse.urlsplit(url)
        port = parsed.port
    except ValueError as exc:
        raise ClientError("Invalid URL") from exc
    if parsed.username or parsed.password or parsed.fragment:
        raise ClientError("URL credentials and fragments are not allowed")
    host = (parsed.hostname or "").lower()
    if parsed.scheme != "https" and not (
        parsed.scheme == "http" and host in ("localhost", "127.0.0.1", "::1")
    ):
        raise ClientError("HTTPS is required (HTTP is allowed only on loopback)")
    if not host:
        raise ClientError("URL has no host")
    return parsed.scheme, host, port or (443 if parsed.scheme == "https" else 80)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise TransportError("HTTP redirect refused; use the canonical URL")


class HTTP:
    def __init__(self, timeout=60, max_bytes=MAX_RESPONSE):
        self.timeout, self.max_bytes = timeout, max_bytes
        self.opener = urllib.request.build_opener(NoRedirect())

    def open(self, url, data=None, headers=None):
        origin(url)
        request = urllib.request.Request(
            url, data=data, headers=headers or {},
            method="POST" if data is not None else "GET",
        )
        try:
            return self.opener.open(request, timeout=self.timeout)
        except urllib.error.HTTPError as exc:
            # Error bodies can contain JSON-RPC errors. Never include the URL.
            return exc
        except (urllib.error.URLError, OSError) as exc:
            raise TransportError("HTTP transport failed: " + type(exc).__name__) from exc

    def read(self, response):
        try:
            data = response.read(self.max_bytes + 1)
        except (OSError, ValueError) as exc:
            raise TransportError("HTTP response interrupted; outcome may be unknown") from exc
        if len(data) > self.max_bytes:
            raise TransportError("Response exceeds byte limit; use expand or file transfer")
        return data

    def get(self, url):
        with self.open(url, headers={"Accept": "application/json, text/plain", "Cache-Control": "no-store"}) as response:
            data = self.read(response)
            if response.status != 200:
                raise TransportError("Download failed: HTTP " + str(response.status))
            return data

    def post(self, url, message, headers, expected_id):
        payload = json.dumps(message, ensure_ascii=False, allow_nan=False).encode("utf-8")
        with self.open(url, payload, headers) as response:
            status = response.status
            response_headers = response.headers
            if expected_id is None and status == 202:
                return None, response_headers
            content_type = response.headers.get_content_type()
            if content_type == "text/event-stream" and status == 200:
                frame = self.read_sse(response, expected_id)
            else:
                raw = self.read(response)
                try:
                    frame = json.loads(raw)
                except (ValueError, UnicodeError) as exc:
                    raise TransportError("Expected JSON-RPC response, HTTP " + str(status)) from exc
            if not isinstance(frame, dict) or frame.get("jsonrpc") != "2.0":
                raise ClientError("Invalid JSON-RPC response")
            if "error" in frame:
                raise RPCError(frame["error"], status)
            if status != 200:
                raise TransportError("MCP request failed: HTTP " + str(status))
            if frame.get("id") != expected_id or "result" not in frame:
                raise ClientError("JSON-RPC response ID/result mismatch")
            return frame["result"], response_headers

    def read_sse(self, response, expected_id):
        total, lines, frames = 0, [], 0
        deadline = time.monotonic() + self.timeout
        while True:
            if time.monotonic() > deadline:
                raise TransportError("SSE deadline exceeded; outcome may be unknown")
            try:
                raw = response.readline(min(self.max_bytes - total + 1, 1024 * 1024))
            except OSError as exc:
                raise TransportError("SSE interrupted; outcome may be unknown") from exc
            total += len(raw)
            if total > self.max_bytes:
                raise TransportError("SSE exceeds byte limit")
            if not raw:
                raise TransportError("SSE ended before the matching response; do not replay a write")
            try:
                line = raw.decode("utf-8").rstrip("\r\n")
            except UnicodeError as exc:
                raise ClientError("SSE is not valid UTF-8") from exc
            if not line:
                if not lines:
                    continue
                frames += 1
                if frames > 4096:
                    raise TransportError("Too many SSE events")
                try:
                    message = json.loads("\n".join(lines))
                except ValueError as exc:
                    raise ClientError("Invalid SSE JSON") from exc
                lines = []
                if not isinstance(message, dict):
                    raise ClientError("Invalid SSE message")
                if message.get("id") == expected_id and ("result" in message or "error" in message):
                    return message
                if "method" in message and "id" not in message:
                    continue
                raise ClientError("Unexpected SSE response or unsupported server request")
            elif line.startswith(":"):
                continue
            else:
                field, _, value = line.partition(":")
                if field == "data":
                    lines.append(value[1:] if value.startswith(" ") else value)


def header_value(value):
    if isinstance(value, bool):
        value = "true" if value else "false"
    else:
        value = str(value)
    safe = value == value.strip() and all(c == "\t" or 32 <= ord(c) <= 126 for c in value)
    if safe and not (value.startswith("=?base64?") and value.endswith("?=")):
        return value
    return "=?base64?" + base64.b64encode(value.encode("utf-8")).decode("ascii") + "?="


def header_fields(schema):
    fields, names = [], set()

    def visit(node, path, reachable):
        if isinstance(node, list):
            for child in node:
                visit(child, path, False)
            return
        if not isinstance(node, dict):
            return
        if "x-mcp-header" in node:
            name, typ = node["x-mcp-header"], node.get("type")
            if not reachable or not path or not isinstance(name, str) or not HEADER_NAME.fullmatch(name):
                raise ClientError("Invalid x-mcp-header annotation")
            if typ not in ("string", "integer", "boolean") or name.lower() in names:
                raise ClientError("Invalid or duplicate x-mcp-header")
            names.add(name.lower())
            fields.append((path, name, typ))
        for key, child in node.items():
            if key == "properties" and isinstance(child, dict) and reachable:
                for prop, definition in child.items():
                    visit(definition, path + (prop,), True)
            elif isinstance(child, (dict, list)):
                visit(child, path, False)

    visit(schema, (), True)
    return fields


def result_data(result):
    """Convenience view only. The normal CLI output preserves the full result."""
    if isinstance(result, dict) and "structuredContent" in result:
        return result["structuredContent"]
    if isinstance(result, dict):
        content = result.get("content")
        if isinstance(content, list) and len(content) == 1 and content[0].get("type") == "text":
            try:
                return json.loads(content[0]["text"])
            except (ValueError, KeyError):
                pass
    return result


def result_failed(result):
    if isinstance(result, dict) and result.get("isError") is True:
        return True
    data = result_data(result)
    return isinstance(data, dict) and data.get("status") == "failed"


def require_success(result):
    if result_failed(result):
        raise ClientError("Tool failed: " + json.dumps(result, ensure_ascii=False))
    return result_data(result)


class Client:
    def __init__(self, config, timeout=60, legacy=False, instruction_context=None):
        self.url = config.get("mcp_url", "")
        origin(self.url)
        self.token = config.get("access_token", "")
        if not isinstance(self.token, str) or not self.token or any(ord(c) < 32 or ord(c) > 126 for c in self.token):
            raise ClientError("Config has no valid access_token")
        expiry = config.get("expires_at")
        if not isinstance(expiry, (float, int)) or isinstance(expiry, bool) or expiry <= time.time():
            raise ClientError("Bootstrap credential expired; create a new link in GPTAdmin")
        self.expires_at = expiry
        self.protocol = LEGACY if legacy else config.get("protocol_version", MODERN)
        if self.protocol not in (MODERN, LEGACY):
            raise ClientError("Unsupported configured protocol version")
        self.http = HTTP(timeout)
        self.session_id = None
        self.initialization = None
        self.schemas = None
        self.instruction_context = instruction_context
        self.invalid_schemas = {}

    def rpc(self, method, params=None):
        if self.protocol == LEGACY and self.initialization is None and method != "initialize":
            self.initialize()
        return self._rpc(method, params or {})

    def _rpc(self, method, params, notification=False, extra_headers=None):
        if time.time() >= self.expires_at:
            raise ClientError("Bootstrap credential expired; create a new link in GPTAdmin")
        if not isinstance(params, dict):
            raise ClientError("RPC params must be a JSON object")
        params = dict(params)
        headers = {
            "Authorization": "Bearer " + self.token,
            "Accept": "application/json, text/event-stream",
            "Content-Type": "application/json",
            "Cache-Control": "no-store",
        }
        if self.protocol == MODERN:
            meta = params.get("_meta", {})
            if not isinstance(meta, dict):
                raise ClientError("_meta must be an object")
            params["_meta"] = dict(meta, **{
                "io.modelcontextprotocol/protocolVersion": MODERN,
                "io.modelcontextprotocol/clientInfo": {"name": "gptadmin-bootstrap", "version": VERSION},
                "io.modelcontextprotocol/clientCapabilities": {},
            })
            headers["MCP-Protocol-Version"] = MODERN
            headers["Mcp-Method"] = method
            if method in ("tools/call", "prompts/get", "resources/read"):
                name = params.get("uri" if method == "resources/read" else "name")
                if not isinstance(name, str):
                    raise ClientError("RPC name/uri must be a string")
                headers["Mcp-Name"] = header_value(name)
        else:
            if method != "initialize":
                headers["MCP-Protocol-Version"] = self.protocol
            if self.session_id:
                headers["Mcp-Session-Id"] = self.session_id
        headers.update(extra_headers or {})
        request_id = None if notification else uuid.uuid4().hex
        message = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            message["id"] = request_id
        result, response_headers = self.http.post(self.url, message, headers, request_id)
        if self.protocol == LEGACY and method == "initialize":
            self.session_id = response_headers.get("Mcp-Session-Id")
        return result

    def initialize(self):
        if self.initialization is not None:
            return self.initialization
        result = self._rpc("initialize", {
            "protocolVersion": LEGACY, "capabilities": {},
            "clientInfo": {"name": "gptadmin-bootstrap", "version": VERSION},
        })
        if not isinstance(result, dict) or result.get("protocolVersion") != LEGACY:
            raise ClientError("Server did not negotiate the explicit legacy version")
        self.initialization = result
        self._rpc("notifications/initialized", {}, notification=True)
        return result

    def discover(self):
        return self.initialize() if self.protocol == LEGACY else self.rpc("server/discover")

    def load_tools(self):
        if self.schemas is not None:
            return self.schemas
        schemas, cursors = {}, set()
        cursor = None
        for _ in range(100):
            result = self.rpc("tools/list", {"cursor": cursor} if cursor else {})
            if not isinstance(result, dict) or not isinstance(result.get("tools"), list):
                raise ClientError("Invalid tools/list result")
            for tool in result["tools"]:
                try:
                    fields = header_fields(tool.get("inputSchema", {}))
                except ClientError as exc:
                    self.invalid_schemas[tool.get("name", "")] = str(exc)
                    continue
                schemas[tool["name"]] = fields
            cursor = result.get("nextCursor")
            if not cursor:
                self.schemas = schemas
                return schemas
            if cursor in cursors:
                raise ClientError("Repeated tools/list cursor")
            cursors.add(cursor)
        raise ClientError("Too many tools/list pages")

    def call_tool(self, name, arguments=None):
        arguments = arguments or {}
        if not isinstance(arguments, dict):
            raise ClientError("Tool arguments must be a JSON object")
        extra = {}
        if self.protocol == MODERN:
            schemas = self.load_tools()
            if name not in schemas:
                raise ClientError("Tool unavailable or invalid schema: " + name)
            for path, header, typ in schemas[name]:
                value = arguments
                for part in path:
                    value = value.get(part) if isinstance(value, dict) else None
                if value is None:
                    continue
                valid = (
                    (typ == "string" and isinstance(value, str)) or
                    (typ == "boolean" and isinstance(value, bool)) or
                    (typ == "integer" and isinstance(value, int) and not isinstance(value, bool) and abs(value) <= 2**53 - 1)
                )
                if not valid:
                    raise ClientError("Invalid header parameter: " + ".".join(path))
                extra["Mcp-Param-" + header] = header_value(value)
        return self._rpc("tools/call", {"name": name, "arguments": arguments}, extra_headers=extra) if self.protocol == MODERN else self.rpc("tools/call", {"name": name, "arguments": arguments})

    def exec(self, code, **options):
        arguments = {"code": code, "max_output_bytes": 65536}
        if self.instruction_context is not None:
            arguments["context"] = self.instruction_context
        arguments.update(options)
        return self.call_tool("exec", arguments)

    def call_target(self, target, tool, arguments):
        code = "return await mcp.call(" + ",".join(json.dumps(x, ensure_ascii=False) for x in (target, tool, arguments)) + ");"
        return self.exec(code)

    def transfer(self, target, action, path, **arguments):
        result = require_success(self.call_target(target, "file_transfer", {"action": action, "path": path, **arguments}))
        if isinstance(result, dict) and "result" in result:
            result = require_success(result["result"])
        result = require_success(result)
        if not isinstance(result, dict) or not isinstance(result.get("transfer"), dict):
            raise ClientError("Missing file_transfer result")
        return result["transfer"]

    def upload(self, target, local_path, remote_path):
        path = Path(local_path)
        size = path.stat().st_size
        if size > MAX_FILE:
            raise ClientError("File exceeds the 16 MiB native transfer limit")
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
        checksum = digest.hexdigest()
        state = self.transfer(target, "upload_begin", remote_path, size=size, sha256=checksum)
        transfer_id = state["transfer_id"]
        try:
            chunk_bytes = min(state.get("chunk_bytes", CHUNK_BYTES), CHUNK_BYTES)
            if not isinstance(chunk_bytes, int) or chunk_bytes < 1:
                raise ClientError("Invalid transfer chunk size")
            offset = 0
            with path.open("rb") as source:
                for block in iter(lambda: source.read(chunk_bytes), b""):
                    args = {"transfer_id": transfer_id, "offset": offset,
                            "data_base64": base64.b64encode(block).decode("ascii"),
                            "sha256": hashlib.sha256(block).hexdigest()}
                    try:
                        receipt = self.transfer(target, "upload_chunk", remote_path, **args)
                    except TransportError:
                        # Inspect accepted bytes before retrying this exact idempotent chunk.
                        receipt = self.transfer(target, "upload_status", remote_path, transfer_id=transfer_id)
                        if receipt.get("received") == offset:
                            receipt = self.transfer(target, "upload_chunk", remote_path, **args)
                    offset += len(block)
                    if receipt.get("received") != offset:
                        raise ClientError("Upload offset mismatch")
            if offset != size:
                raise ClientError("Local file changed during upload")
            result = self.transfer(target, "upload_commit", remote_path, transfer_id=transfer_id)
            return {"path": remote_path, "size": size, "sha256": checksum, "transfer": result}
        finally:
            try:
                self.transfer(target, "upload_abort", remote_path, transfer_id=transfer_id)
            except ClientError:
                pass

    def download(self, target, remote_path, local_path):
        path = Path(local_path)
        if path.exists() or path.is_symlink():
            raise ClientError("Destination already exists")
        state = self.transfer(target, "download_begin", remote_path)
        transfer_id, temporary = state["transfer_id"], None
        try:
            size, checksum = state["size"], state["sha256"]
            if not isinstance(size, int) or not 0 <= size <= MAX_FILE or not SHA256.fullmatch(checksum):
                raise ClientError("Invalid download metadata")
            fd, temporary = tempfile.mkstemp(prefix=".gptadmin-download-", dir=path.parent)
            digest, offset = hashlib.sha256(), 0
            with os.fdopen(fd, "wb") as dest:
                while offset < size:
                    part = self.transfer(target, "download_chunk", remote_path,
                                         transfer_id=transfer_id, offset=offset, max_bytes=CHUNK_BYTES)
                    try:
                        block = base64.b64decode(part["data_base64"], validate=True)
                    except (ValueError, KeyError) as exc:
                        raise ClientError("Invalid download base64") from exc
                    if not block or len(block) > CHUNK_BYTES or offset + len(block) > size:
                        raise ClientError("Invalid download chunk length")
                    if base64.b64encode(block).decode("ascii") != part["data_base64"]:
                        raise ClientError("Noncanonical download base64")
                    if hashlib.sha256(block).hexdigest() != part.get("chunk_sha256"):
                        raise ClientError("Download chunk checksum mismatch")
                    offset += len(block)
                    if part.get("next_offset") != offset or part.get("eof") != (offset == size):
                        raise ClientError("Download offset/EOF mismatch")
                    dest.write(block)
                    digest.update(block)
                dest.flush()
                os.fsync(dest.fileno())
            if digest.hexdigest() != checksum:
                raise ClientError("Download checksum mismatch")
            os.link(temporary, path)
            return {"path": str(path), "size": size, "sha256": checksum}
        finally:
            if temporary is not None:
                Path(temporary).unlink(missing_ok=True)
            try:
                self.transfer(target, "download_abort", remote_path, transfer_id=transfer_id)
            except ClientError:
                pass


def private_write(path, data):
    path = Path(path)
    fd, temporary = tempfile.mkstemp(prefix=".gptadmin-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def bootstrap(url, config_path, timeout=60):
    base = url.rstrip("/")
    for suffix in ("/instructions.md", "/client.py", "/config.json", "/manifest.json"):
        if base.endswith(suffix):
            base = base[:-len(suffix)]
            break
    bootstrap_origin = origin(base)
    if urllib.parse.urlsplit(base).query:
        raise ClientError("Bootstrap URL must not have a query string")
    http = HTTP(timeout)
    try:
        manifest = json.loads(http.get(base + "/manifest.json"))
        config = json.loads(http.get(base + "/config.json"))
    except ValueError as exc:
        raise ClientError("Invalid bootstrap JSON") from exc
    if not isinstance(config, dict) or origin(config.get("mcp_url", "")) != bootstrap_origin:
        raise ClientError("Bootstrap MCP endpoint origin mismatch")
    Client(config, timeout)
    actual_hash = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    if not isinstance(manifest, dict) or manifest.get("client_sha256") != actual_hash:
        raise ClientError("Client checksum mismatch; download client.py from this link again")
    instructions = http.get(base + "/instructions.md")
    try:
        instructions.decode("utf-8")
    except UnicodeError as exc:
        raise ClientError("Instructions are not valid UTF-8") from exc
    config_path = Path(config_path)
    instructions_path = config_path.with_name("GPTADMIN_INSTRUCTIONS.md")
    private_write(instructions_path, instructions)
    private_write(config_path, (json.dumps(config, ensure_ascii=False, indent=2) + "\n").encode("utf-8"))
    return {"config": str(config_path), "instructions": str(instructions_path),
            "expires_at": config["expires_at"], "profile_id": config.get("profile_id"),
            "client_sha256": actual_hash}


def json_argument(value):
    if value == "-":
        return json.load(sys.stdin)
    if value.startswith("@"):
        return json.loads(Path(value[1:]).read_text())
    return json.loads(value)


def build_parser():
    parser = argparse.ArgumentParser(description="Готовый MCP-клиент GPTAdmin. Python 3, без pip.")
    parser.add_argument("--config", default=".gptadmin.json", help="Конфигурация с временным credential")
    parser.add_argument("--timeout", type=float, default=60, help="HTTP timeout, секунды")
    parser.add_argument("--legacy", action="store_true", help="Явный режим MCP 2025-11-25")
    parser.add_argument("--context", dest="instruction_context", help="Явный instruction receipt для exec и его wrappers")
    parser.add_argument("--data", action="store_true", help="Вывести structuredContent вместо полного результата; допускается до или после подкоманды")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("bootstrap", help="Скачать конфигурацию и инструкции (права 0600)").add_argument("url", help="Ссылка или - для чтения ссылки из stdin")
    sub.add_parser("discover", help="Информация и инструкции MCP-сервера")
    search = sub.add_parser("search", help="Найти инструменты")
    search.add_argument("query")
    search.add_argument("--target")
    search.add_argument("--limit", type=int, default=10)
    schema = sub.add_parser("schema", help="Точная схема инструмента")
    schema.add_argument("target")
    schema.add_argument("tool", nargs="?")
    schema.add_argument("--context")
    execute = sub.add_parser("exec", help="JavaScript для Hub, из --code, --file или stdin")
    execute.add_argument("--code")
    execute.add_argument("--file")
    execute.add_argument("--context")
    execute.add_argument("--max-output-bytes", type=int, default=65536)
    execute.add_argument("--timeout-ms", type=int)
    execute.add_argument("--max-calls", type=int)
    call = sub.add_parser("call", help="Вызвать вложенный MCP-инструмент")
    call.add_argument("target")
    call.add_argument("tool")
    call.add_argument("--arguments", default="{}", help="JSON, @файл или - для stdin")
    job = sub.add_parser("job", help="Прочитать существующее задание, не запускать повторно")
    job.add_argument("id")
    job.add_argument("--owner-target")
    expand = sub.add_parser("expand", help="Прочитать сохранённую часть большого ответа")
    expand.add_argument("id")
    expand.add_argument("--path", default="")
    expand.add_argument("--offset", type=int, default=0)
    expand.add_argument("--limit", type=int, default=32768)
    shell = sub.add_parser("shell", help="Запустить argv; опции ставьте до target")
    shell.add_argument("--cwd")
    shell.add_argument("--stdin", dest="stdin_path", help="Файл для stdin; - читает локальный stdin")
    shell.add_argument("--command-timeout", type=int)
    shell.add_argument("target")
    shell.add_argument("argv", nargs=argparse.REMAINDER)
    for command in ("upload", "download"):
        transfer = sub.add_parser(command, help="Двоичная передача нового файла с SHA-256, до 16 MiB")
        transfer.add_argument("target", help="file:<host>")
        transfer.add_argument("source")
        transfer.add_argument("destination")
    raw = sub.add_parser("rpc", help="Произвольный MCP метод; без автоматического повтора")
    raw.add_argument("method")
    raw.add_argument("--params", default="{}", help="JSON, @файл или - для stdin")
    return parser


def dispatch(client, args):
    command = args.command
    if command == "discover":
        return client.discover()
    if command == "rpc":
        return client.rpc(args.method, json_argument(args.params))
    if command == "search":
        values = {"query": args.query, "limit": args.limit}
        if args.target:
            values["target"] = args.target
        return client.call_tool("search", values)
    if command == "schema":
        values = {"target": args.target, "detail": "full"}
        for key in ("tool", "context"):
            if getattr(args, key):
                values[key] = getattr(args, key)
        if "context" not in values and args.instruction_context is not None:
            values["context"] = args.instruction_context
        return client.call_tool("schema", values)
    if command == "exec":
        if args.code is not None and args.file:
            raise ClientError("Use either --code or --file")
        code = args.code if args.code is not None else (Path(args.file).read_text() if args.file else sys.stdin.read())
        if not code.strip():
            raise ClientError("Empty exec source")
        options = {key: getattr(args, key) for key in ("context", "max_output_bytes", "timeout_ms", "max_calls") if getattr(args, key) is not None}
        return client.exec(code, **options)
    if command == "call":
        return client.call_target(args.target, args.tool, json_argument(args.arguments))
    if command == "job":
        values = {"id": args.id, "detail": "full"}
        if args.owner_target:
            values["owner_target"] = args.owner_target
        return client.call_tool("job", values)
    if command == "expand":
        return client.call_tool("expand", {key: getattr(args, key) for key in ("id", "path", "offset", "limit")})
    if command == "shell":
        argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
        if not argv:
            raise ClientError("shell requires a program and literal argv")
        values = {"argv": argv}
        if args.cwd:
            values["cwd"] = args.cwd
        if args.command_timeout is not None:
            values["timeout"] = args.command_timeout
        if args.stdin_path:
            values["stdin"] = sys.stdin.read() if args.stdin_path == "-" else Path(args.stdin_path).read_text()
        return client.call_target(args.target, "shell_exec", values)
    if command == "upload":
        return client.upload(args.target, args.source, args.destination)
    if command == "download":
        return client.download(args.target, args.source, args.destination)
    raise ClientError("Unknown command")


def main(argv=None):
    # --data is a pure output-mode flag: accept it both before and after the
    # subcommand, because argparse subparsers reset global defaults.
    raw = list(sys.argv[1:] if argv is None else argv)
    force_data = "--data" in raw
    if force_data:
        raw = [a for a in raw if a != "--data"]
    args = build_parser().parse_args(raw)
    if force_data:
        args.data = True
    secrets = []
    try:
        if args.timeout <= 0 or args.timeout > 600:
            raise ClientError("Timeout must be between 0 and 600 seconds")
        if args.command == "bootstrap":
            url = sys.stdin.read().strip() if args.url == "-" else args.url
            if not url:
                raise ClientError("Bootstrap URL is required on stdin")
            secrets.append(url)
            result = bootstrap(url, args.config, args.timeout)
        else:
            config = json.loads(Path(args.config).read_text())
            if isinstance(config, dict) and isinstance(config.get("access_token"), str):
                secrets.append(config["access_token"])
            client = Client(config, args.timeout, args.legacy, instruction_context=args.instruction_context)
            result = dispatch(client, args)
        output = json.dumps(result_data(result) if args.data else result, ensure_ascii=False, indent=2)
        for secret in secrets:
            if secret:
                output = output.replace(secret, "<redacted>")
        print(output)
        if isinstance(result, dict) and result.get("resultType") == "input_required":
            return 2
        return 1 if result_failed(result) else 0
    except (ClientError, OSError, ValueError, KeyError, TypeError) as exc:
        message = str(exc)
        for secret in secrets:
            if secret:
                message = message.replace(secret, "<redacted>")
        print(json.dumps({"error": message}, ensure_ascii=False), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
