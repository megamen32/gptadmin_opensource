#!/usr/bin/env python3
"""Small NDJSON MCP server used only by the failover black-box suite."""

from __future__ import annotations

import json
import sys
from typing import Any


def reply(message_id: Any, result: dict[str, Any]) -> None:
    """Write one JSON-RPC response to the relay."""
    print(json.dumps({"jsonrpc": "2.0", "id": message_id, "result": result}), flush=True)


for raw in sys.stdin:
    message = json.loads(raw)
    message_id = message.get("id")
    if message_id is None:
        continue
    method = message.get("method")
    if method == "initialize":
        reply(message_id, {"protocolVersion": "2025-03-26", "capabilities": {}, "serverInfo": {"name": "failover-e2e", "version": "1"}})
    elif method == "tools/list":
        reply(message_id, {"tools": [{"name": "echo", "description": "Echoes text", "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}}}]})
    elif method == "tools/call":
        text = (message.get("params") or {}).get("arguments", {}).get("text", "")
        reply(message_id, {"content": [{"type": "text", "text": str(text)}]})
    else:
        print(json.dumps({"jsonrpc": "2.0", "id": message_id, "error": {"code": -32601, "message": f"unsupported method {method}"}}), flush=True)
