#!/usr/bin/env python3
"""Run an existing, sealed FastMCP tool module as one owner-local HTTP service.

The project virtualenv supplies its installed MCP/uvicorn implementations.
No stdio subprocess, tool translation, credential merge, or fallback is used.
The service manager must provide a finite workload budget and original env.
"""
from __future__ import annotations

import argparse
import asyncio
import fcntl
import hashlib
import hmac
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import sys


def read_owned(path: Path, limit: int, *, private: bool) -> bytes:
    if not path.is_absolute():
        raise ValueError("absolute owner path required")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid not in (0, os.geteuid()):
            raise ValueError("owner file required")
        mode = stat.S_IMODE(info.st_mode)
        if (private and mode not in (0o400, 0o600)) or mode & 0o022:
            raise ValueError("unsafe owner file permissions")
        raw = stream.read(limit + 1)
        if len(raw) > limit:
            raise ValueError("owner file too large")
        return raw


def load_binding(path: Path) -> tuple[dict, bytes, bytes]:
    config = json.loads(read_owned(path, 16384, private=True))
    required = {"schema", "module", "module_sha256", "token_file", "port"}
    if not isinstance(config, dict) or set(config) != required or config["schema"] != 1:
        raise ValueError("unsupported HTTP binding")
    if type(config["port"]) is not int or not 1024 <= config["port"] <= 65535:
        raise ValueError("invalid loopback port")
    if not isinstance(config["module_sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", config["module_sha256"]):
        raise ValueError("sealed source hash required")
    source = read_owned(Path(config["module"]), 1024 * 1024, private=False)
    if hashlib.sha256(source).hexdigest() != config["module_sha256"]:
        raise ValueError("module source drift")
    token = read_owned(Path(config["token_file"]), 128, private=True).strip()
    if not re.fullmatch(rb"[0-9a-f]{64}", token):
        raise ValueError("dedicated upstream credential required")
    return config, source, token


class OwnerHTTP:
    """Protect only this module's owner, before any SDK or backend dispatch."""

    def __init__(self, app, token: bytes, maximum_active: int = 2):
        self.app, self.expected = app, b"Bearer " + token
        self.slots = asyncio.Semaphore(maximum_active)

    async def __call__(self, scope, receive, send):
        if scope["type"] == "lifespan":
            return await self.app(scope, receive, send)
        if scope["type"] != "http":
            return
        headers = [value for key, value in scope.get("headers", []) if key.lower() == b"authorization"]
        code = 404 if scope.get("path") != "/mcp" else 0
        if not code and (len(headers) != 1 or len(headers[0]) > 1024 or not hmac.compare_digest(headers[0], self.expected)):
            code = 401
        if not code and self.slots.locked():
            code = 503
        if code:
            await send({"type": "http.response.start", "status": code, "headers": [(b"content-length", b"0"), (b"cache-control", b"no-store")]})
            return await send({"type": "http.response.body", "body": b""})
        async with self.slots:
            total = 0

            async def bounded_receive():
                nonlocal total
                message = await receive()
                total += len(message.get("body", b""))
                if total > 64 * 1024 * 1024:
                    raise ValueError("HTTP input exceeds existing MCP envelope limit")
                return message

            return await self.app(scope, bounded_receive, send)


def make_app(config: dict, source: bytes, token: bytes):
    path = Path(config["module"])
    name = "_gptadmin_owner_mcp_" + config["module_sha256"][:16]
    spec = importlib.util.spec_from_loader(name, loader=None, origin=str(path))
    module = importlib.util.module_from_spec(spec)
    module.__file__ = str(path)
    sys.modules[name] = module
    sys.path.insert(0, str(path.parent))
    # Execute the bytes we verified, never reopen a mutable source path.
    exec(compile(source, str(path), "exec"), module.__dict__)
    server = module.mcp
    from mcp.server.fastmcp import FastMCP
    if not isinstance(server, FastMCP):
        raise ValueError("existing native FastMCP export required")
    server.settings.host = "127.0.0.1"
    server.settings.port = config["port"]
    server.settings.streamable_http_path = "/mcp"
    server.settings.stateless_http = True
    server.settings.json_response = True
    return OwnerHTTP(server.streamable_http_app(), token)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path)
    args = parser.parse_args()
    config, source, token = load_binding(args.config)
    lock = os.open(str(args.config) + ".lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(lock)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError("unsafe singleton lock")
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        app = make_app(config, source, token)
        import uvicorn
        uvicorn.run(app, host="127.0.0.1", port=config["port"], workers=1,
                    access_log=False, log_level="error", limit_concurrency=6,
                    backlog=16, timeout_graceful_shutdown=8)
    finally:
        os.close(lock)


if __name__ == "__main__":
    main()
