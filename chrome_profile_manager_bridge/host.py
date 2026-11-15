#!/usr/bin/env python3
"""One native host per explicitly paired Chrome profile."""
import json
import fcntl
import os
import re
import select
import socket
import struct
import sys
import time

MAX = 1024 * 1024
RESPONSE_TIMEOUT = 20
METHODS = {"list", "get", "enable", "disable", "uninstall", "launch_app"}

def exact(stream, n):
    data = bytearray()
    while len(data) < n:
        part = stream.read(n - len(data))
        if not part: raise EOFError("connection closed")
        data.extend(part)
    return bytes(data)

def receive(stream):
    n = struct.unpack("=I", exact(stream, 4))[0]
    if n > MAX: raise ValueError("message too large")
    return json.loads(exact(stream, n))

def receive_deadline(fd, timeout):
    # Read directly from the pipe: buffered reads can block after select reports
    # only part of a frame, or hide already buffered bytes from select.
    deadline = time.monotonic() + timeout
    def read_exact(n):
        data = bytearray()
        while len(data) < n:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([fd], [], [], remaining)[0]:
                raise TimeoutError("response deadline exceeded")
            part = os.read(fd, n - len(data))
            if not part: raise EOFError("connection closed")
            data.extend(part)
        return bytes(data)
    n = struct.unpack("=I", read_exact(4))[0]
    if n > MAX: raise ValueError("message too large")
    return json.loads(read_exact(n))

def send(stream, value):
    data = json.dumps(value, separators=(",", ":")).encode()
    if len(data) > MAX: raise ValueError("message too large")
    stream.write(struct.pack("=I", len(data)) + data)
    stream.flush()

def socket_path(secret):
    if not isinstance(secret, str) or not re.fullmatch("[a-f0-9]{64}", secret):
        raise ValueError("invalid capability")
    # macOS tempfile paths can exceed the AF_UNIX path limit.
    return os.path.join("/tmp", "gptadmin-extension-%d" % os.getuid(), secret[:32] + ".sock")

def serve():
    # Avoid buffering any subsequent native messages before deadline reads.
    hello = receive(sys.stdin.buffer.raw)
    if not isinstance(hello, dict): raise ValueError("hello required")
    if hello.get("type") == "auto":
        origin = sys.argv[1] if len(sys.argv) > 1 else ""
        if not re.fullmatch(r"chrome-extension://[a-p]{32}/", origin): raise ValueError("invalid origin")
        config = os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")), "gptadmin-extension-bridge", "profiles.json")
        info = os.lstat(config)
        if info.st_uid != os.getuid() or info.st_mode & 0o077 or os.path.islink(config): raise ValueError("unsafe profile registry")
        with open(config, encoding="utf-8") as f: registry = json.load(f)
        secret = registry.get(origin)
    elif hello.get("type") == "hello":
        secret = hello.get("secret")
    else: raise ValueError("hello required")
    path = socket_path(secret)
    directory = os.path.dirname(path)
    os.makedirs(directory, mode=0o700, exist_ok=True)
    info = os.lstat(directory)
    if info.st_uid != os.getuid() or info.st_mode & 0o077 or os.path.islink(directory) or not os.path.isdir(directory):
        raise ValueError("unsafe socket directory")
    lock = open(path + ".lock", "a")
    os.chmod(path + ".lock", 0o600)
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    server = socket.socket(socket.AF_UNIX)
    if os.path.lexists(path):
        info = os.lstat(path)
        if info.st_uid != os.getuid() or not __import__("stat").S_ISSOCK(info.st_mode): raise ValueError("unsafe stale endpoint")
        os.unlink(path)
    server.bind(path)
    os.chmod(path, 0o600)
    server.listen(4)
    try:
        while True:
            ready, _, _ = select.select([server, sys.stdin.buffer], [], [])
            if sys.stdin.buffer in ready: break
            conn, _ = server.accept()
            conn.settimeout(30)
            with conn, conn.makefile("rwb") as io:
                dispatched = False
                try:
                    request = receive(io)
                    if not isinstance(request, dict) or request.get("secret") != secret: raise ValueError("unauthorized")
                    method = request.get("method")
                    if method not in METHODS: raise ValueError("unsupported method")
                    ext = request.get("extension_id")
                    if method != "list" and (not isinstance(ext, str) or not re.fullmatch("[a-p]{32}", ext)): raise ValueError("invalid extension id")
                    # A failed write/flush may still have delivered a command.
                    dispatched = True
                    send(sys.stdout.buffer, {"id": 1, "method": method, "extension_id": ext})
                    response = receive_deadline(sys.stdin.fileno(), RESPONSE_TIMEOUT)
                    if not isinstance(response, dict) or response.get("id") != 1: raise ValueError("invalid response")
                    send(io, response)
                except (ValueError, EOFError, OSError) as exc:
                    error = {"error": "command outcome unknown", "outcome": "unknown"} if dispatched else {"error": str(exc)}
                    try: send(io, error)
                    except OSError: pass
                    if dispatched: return
    finally:
        server.close()
        os.unlink(path)
        lock.close()

if __name__ == "__main__":
    try: serve()
    except (EOFError, OSError, ValueError) as exc:
        print("bridge stopped: " + str(exc), file=sys.stderr)
        sys.exit(1)
