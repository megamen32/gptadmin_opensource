#!/usr/bin/env python3
"""Black-box smoke for standalone ShellMCP and one managed stdio child."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


CHILD = r'''import json, sys
for line in sys.stdin:
    req = json.loads(line)
    method = req.get("method")
    if method == "notifications/initialized":
        continue
    if method == "initialize":
        result = {"protocolVersion": "2025-03-26", "capabilities": {}}
    elif method == "tools/list":
        result = {"tools": [{"name": "echo", "inputSchema": {"type": "object"}}]}
    elif method == "tools/call":
        result = {"content": [{"type": "text", "text": req.get("params", {}).get("arguments", {}).get("value", "")}]}
    else:
        result = {}
    print(json.dumps({"jsonrpc": "2.0", "id": req.get("id"), "result": result}), flush=True)
'''


def rpc(process: subprocess.Popen[str], request_id: int, method: str, params: dict) -> dict:
    """Send one JSON-RPC request and return its result."""
    assert process.stdin is not None and process.stdout is not None
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}) + "\n")
    process.stdin.flush()
    response = json.loads(process.stdout.readline())
    if "error" in response:
        raise RuntimeError(response["error"])
    return response.get("result", {})


def main() -> int:
    """Run the standalone host smoke against the supplied native binary."""
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", help="Path to shellmcp native binary")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="shellmcp-smoke-") as temp:
        root = Path(temp)
        env = os.environ.copy()
        env.update({
            "SHELLMCP_MCP_CONFIG": str(root / "mcp.json"),
            "SHELLMCP_SPOOL_DIR": str(root / "spool"),
            "SHELLMCP_DEFAULT_HOME": str(root),
            "SHELLMCP_DEFAULT_CWD": str(root),
            "SHELLMCP_HEARTBEAT": "0",
            "SHELLMCP_QUEUE": "0",
        })
        process = subprocess.Popen(
            [args.binary, "--mcp-stdio"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=env,
        )
        try:
            initialized = rpc(process, 1, "initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}})
            assert initialized.get("protocolVersion")
            config = {
                "ref": "smoke-child",
                "transport": "stdio",
                "command": sys.executable,
                "args": ["-u", "-c", CHILD],
                "enabled": True,
            }
            rpc(process, 2, "tools/call", {"name": "mcp_manage", "arguments": {"action": "upsert", "config": config}})
            listed = rpc(process, 3, "tools/call", {"name": "mcp_tools", "arguments": {"ref": "smoke-child"}})
            tools = listed["structuredContent"]["tools"]
            assert [tool["name"] for tool in tools] == ["echo"]
            called = rpc(process, 4, "tools/call", {"name": "mcp_call", "arguments": {"ref": "smoke-child", "name": "echo", "arguments": {"value": "standalone-ok"}}})
            content = called["structuredContent"]["result"]["content"]
            assert content[0]["text"] == "standalone-ok"
            print("standalone MCP host smoke: PASS")
            return 0
        finally:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()


if __name__ == "__main__":
    raise SystemExit(main())
