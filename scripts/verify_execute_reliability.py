#!/usr/bin/env python3
"""Exercise a candidate unified Node, real shell, TCP reconnect and restart.

Build: (cd go-hub && go build -o /tmp/gptadmin-node ./cmd/gptadmin-node)
Run: python3 scripts/verify_execute_reliability.py /tmp/gptadmin-node
Only the supplied executable and task-owned temporary state are used. No live
credentials, production service restart, fabricated executor or upstream result.
"""

import hashlib
import json
import os
from pathlib import Path
import pwd
import secrets
import shlex
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def verify(binary: Path, root: Path) -> dict:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    origin = f"http://127.0.0.1:{port}"
    token = secrets.token_urlsafe(32)
    name = "execute-reliability-" + secrets.token_hex(4)
    target = "shell:" + name
    marker = root / "executions.txt"
    config = root / "config"
    config.mkdir()
    instructions = config / "startup_instructions.md"
    instructions.write_text("Read exact schema before execution. Never replay an unknown operation.\n")
    # Do not inherit production tokens, config locations, peers or MCP children.
    env = {k: os.environ[k] for k in ("PATH", "HOME", "LANG", "TZ") if k in os.environ}
    env.update({
        "GPTADMIN_ROOT": str(root), "GPTADMIN_CONFIG_DIR": str(config),
        "GPTADMIN_HUB_HOST": "127.0.0.1", "GPTADMIN_HUB_PORT": str(port),
        "PUBLIC_ORIGIN": origin, "MCP_RESOURCE": origin,
        "CTL_TOKEN": token, "SHELL_TOKEN": secrets.token_urlsafe(32),
        "OAUTH_CLIENT_SECRET": secrets.token_urlsafe(32),
        "GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED": "1",
        "SHELL_NAME": name, "SHELL_HOST": "127.0.0.1", "SHELL_PORT": "0",
        "SHELL_DEFAULT_USER": pwd.getpwuid(os.getuid()).pw_name,
        "SHELL_DEFAULT_CWD": str(root), "QUEUE_LONG_POLL_TIMEOUT_S": "1",
        "SHELLMCP_QUEUE_RETRY_S": "1", "POLL_INTERVAL_S": "1",
        "SHELLMCP_MCP_CONFIG": str(config / "empty-children.json"),
    })
    (config / "empty-children.json").write_text("[]")
    process = None

    def rpc(method, params):
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
        request = urllib.request.Request(origin + "/mcp", body, headers={
            "Authorization": "Bearer " + token, "Content-Type": "application/json",
            "MCP-Protocol-Version": "2025-11-25",
        })
        # A fresh connection on every call is an intentional reconnect check.
        with urllib.request.urlopen(request, timeout=20) as response:
            result = json.load(response)
        if "error" in result:
            raise AssertionError(f"protocol failure: {result['error']}")
        return result["result"]

    def call(tool, args):
        return rpc("tools/call", {"name": tool, "arguments": args})

    def payload(result):
        assert result.get("isError") is not True, result
        return result["structuredContent"]

    def start(log):
        nonlocal process
        process = subprocess.Popen([str(binary)], cwd=root, env=env, stdout=log, stderr=log)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise AssertionError("candidate exited during startup")
            try:
                rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                   "clientInfo": {"name": "execute-reliability", "version": "1"}})
                return
            except (urllib.error.URLError, TimeoutError):
                time.sleep(0.1)
        raise AssertionError("candidate startup timed out")

    def stop():
        nonlocal process
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise AssertionError("candidate failed graceful shutdown")
        process = None

    def schema():
        result = payload(call("schema", {"target": target, "tool": "shell_exec", "detail": "full"}))
        assert result.get("call_ref") and result.get("context"), result
        assert [x["name"] for x in result["response"]["tools"]] == ["shell_exec"], result
        return result["call_ref"]

    def args(text):
        command = f"printf '%s\\n' {shlex.quote(text)} >> {shlex.quote(str(marker))}; printf '%s\\n' {shlex.quote(text)}"
        return {"cmd": command, "timeout": 10}

    def execute(ref, text):
        result = payload(call("execute", {"ref": ref, "args": args(text)}))
        deadline = time.monotonic() + 15
        while result.get("status") in ("queued", "running") and time.monotonic() < deadline:
            time.sleep(0.1)
            result = payload(call("job", {"id": result["job_id"], "owner_target": target}))
        assert result.get("returncode") == 0 and result.get("stdout") == text + "\n", result

    with (root / "candidate.log").open("w") as log:
        try:
            start(log)
            before = rpc("tools/list", {})["tools"]
            assert [x["name"] for x in before] == ["ui", "discover", "schema", "execute", "job", "expand"]
            blocked = call("execute", {"target": target, "tool": "shell_exec", "args": args("blocked")})
            assert blocked.get("isError") is True and blocked["structuredContent"]["executed"] is False, blocked
            assert blocked["structuredContent"]["error"]["code"] == "instructions_required", blocked
            assert not marker.exists()
            discovery = payload(call("discover", {"detail": "compact", "query": name}))
            assert target in [row["server_id"] for row in discovery["servers"]], discovery
            old_ref = schema()
            execute(old_ref, "before-restart")
            stop()
            start(log)
            after = rpc("tools/list", {})["tools"]
            assert after == before, "tool schemas changed after restart"
            stale = call("execute", {"ref": old_ref, "args": args("must-not-run")})
            assert stale.get("isError") is True and stale["structuredContent"]["executed"] is False, stale
            assert stale["structuredContent"]["error"]["code"] == "schema_reference_unavailable", stale
            assert marker.read_text() == "before-restart\n"
            execute(schema(), "after-restart")
            assert marker.read_text() == "before-restart\nafter-restart\n", "missing or replayed command"
            return {"status": "PASS", "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                    "tools_stable": True, "instruction_gate": True, "real_shell_before_after_restart": True,
                    "stale_reference_non_execution": True, "command_count": 2,
                    "scope": "isolated candidate Node; no ChatGPT UI or production deployment claim"}
        finally:
            stop()


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("Usage: verify_execute_reliability.py /path/to/candidate/gptadmin-node")
    with tempfile.TemporaryDirectory(prefix="gptadmin-execute-") as directory:
        print(json.dumps(verify(Path(sys.argv[1]).resolve(), Path(directory)), indent=2))
