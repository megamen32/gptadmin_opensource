#!/usr/bin/env python3
"""Deterministic third-party-shaped stdio MCP adapter for SDK conformance."""

from __future__ import annotations

import json
import sys
from typing import Any


def response(message_id: Any, result: dict[str, Any]) -> None:
    """Write one bounded JSON-RPC response without logging request contents."""

    print(json.dumps({"jsonrpc": "2.0", "id": message_id, "result": result}), flush=True)


for raw in sys.stdin:
    message = json.loads(raw)
    message_id = message.get("id")
    if message_id is None:
        continue
    method = message.get("method")
    if method == "initialize":
        response(message_id, {"protocolVersion": "2025-03-26", "capabilities": {}, "serverInfo": {"name": "example.echo", "version": "1.0.0"}})
    elif method == "tools/list":
        response(message_id, {"tools": [{"name": "echo", "description": "Return a bounded non-secret message.", "inputSchema": {"type": "object", "properties": {"message": {"type": "string", "maxLength": 256}}, "required": ["message"], "additionalProperties": False}}]})
    elif method == "tools/call":
        params = message.get("params") or {}
        arguments = params.get("arguments") or {}
        text = str(arguments.get("message", ""))[:256]
        response(message_id, {"content": [{"type": "text", "text": text}]})
    else:
        response(message_id, {"isError": True, "content": [{"type": "text", "text": f"unsupported method: {method}"}]})
