#!/usr/bin/env python3
"""Run a secret-safe authenticated smoke against a deployed Hub.

This runner validates the public endpoint/discovery contract and one harmless
authenticated MCP call. It deliberately does not claim client, Tunnel,
profile, file-sharing or webhook certification; those require the deployment
and the corresponding configured target. Use it as the first gate in an
authorized deployment session.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from typing import Any


class LiveAcceptanceError(RuntimeError):
    """Raised when a live acceptance stage fails without exposing response data."""


def _request(base_url: str, path: str, bearer: str | None = None, payload: dict[str, Any] | None = None) -> tuple[int, bytes]:
    """Perform one bounded request and return only status/body bytes.

    The caller decides how to parse the response. HTTP error bodies are never
    included in an exception because deployed responses can contain secrets.
    """

    url = base_url.rstrip("/") + path
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    headers = {"Accept": "application/json"}
    if payload is not None:
        headers["Content-Type"] = "application/json"
    if bearer:
        headers["Authorization"] = f"Bearer {bearer}"
    request = urllib.request.Request(url, data=data, headers=headers, method="POST" if payload is not None else "GET")
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        raise LiveAcceptanceError(f"{path}: HTTP {exc.code}") from None
    except (OSError, urllib.error.URLError) as exc:
        raise LiveAcceptanceError(f"{path}: transport failure ({type(exc).__name__})") from None


def _json_response(base_url: str, path: str, bearer: str | None = None, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    """Return a successful JSON object from one live endpoint."""

    status, body = _request(base_url, path, bearer, payload)
    if status != 200:
        raise LiveAcceptanceError(f"{path}: HTTP {status}")
    try:
        decoded = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise LiveAcceptanceError(f"{path}: invalid JSON response ({type(exc).__name__})") from None
    if not isinstance(decoded, dict):
        raise LiveAcceptanceError(f"{path}: JSON object required")
    return decoded


def _status_stage(base_url: str, path: str) -> None:
    """Require a successful status response without retaining its body."""

    status, _ = _request(base_url, path)
    if status != 200:
        raise LiveAcceptanceError(f"{path}: HTTP {status}")


def run_acceptance(base_url: str, bearer: str, required_tools: set[str] | None = None) -> dict[str, Any]:
    """Run the bounded live Hub smoke and return a redacted result summary.

    Args:
        base_url: Hub origin, without a required trailing slash.
        bearer: Existing short-lived scoped connection token. It is never
            included in the return value or error text.
        required_tools: Optional tool names that must be present in `tools/list`.
    """

    if not base_url.strip() or not bearer.strip():
        raise LiveAcceptanceError("base URL and bearer connection are required")
    stages: list[str] = []
    _status_stage(base_url, "/healthz")
    stages.append("health")
    _status_stage(base_url, "/version")
    stages.append("version")

    connection = _json_response(base_url, "/connect.json")
    if not connection.get("mcp_endpoint") or not connection.get("oauth_authorization_server"):
        raise LiveAcceptanceError("/connect.json: MCP/OAuth discovery is incomplete")
    stages.append("connection")

    oauth = _json_response(base_url, "/.well-known/oauth-authorization-server")
    for key, suffix in (("authorization_endpoint", "/oauth/authorize"), ("token_endpoint", "/oauth/token")):
        if not str(oauth.get(key, "")).endswith(suffix):
            raise LiveAcceptanceError(f"/.well-known/oauth-authorization-server: {key} is not canonical")
    stages.append("oauth")

    _status_stage(base_url, "/actions/openapi.yaml")
    stages.append("openapi")

    listed = _json_response(base_url, "/mcp", bearer, {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}})
    tools = listed.get("result", {}).get("tools", [])
    if not isinstance(tools, list):
        raise LiveAcceptanceError("/mcp tools/list: tools array is missing")
    tool_names = {tool.get("name") for tool in tools if isinstance(tool, dict)}
    missing = sorted((required_tools or set()) - tool_names)
    if missing:
        raise LiveAcceptanceError(f"/mcp tools/list: required tool count missing ({len(missing)})")

    called = _json_response(base_url, "/mcp", bearer, {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "demo", "arguments": {}}})
    if called.get("error") is not None:
        raise LiveAcceptanceError("/mcp tools/call: safe demo call returned an error")
    stages.append("mcp")
    return {"status": "passed", "stages": stages, "tool_count": len(tools)}


def main(argv: list[str] | None = None) -> int:
    """Parse deployment-session inputs and print only redacted JSON."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.environ.get("GPTADMIN_LIVE_BASE_URL", ""))
    parser.add_argument("--bearer", default=os.environ.get("GPTADMIN_LIVE_BEARER", ""), help=argparse.SUPPRESS)
    parser.add_argument("--required-tool", action="append", default=[])
    args = parser.parse_args(argv)
    try:
        result = run_acceptance(args.base_url, args.bearer, set(args.required_tool))
    except LiveAcceptanceError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, ensure_ascii=False))
        return 1
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
