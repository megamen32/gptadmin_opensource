#!/usr/bin/env python3
"""Secret-safe protocol receipt for the private Android Remote Control MCP."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


def read_token(path: Path) -> str:
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key.strip() == "ANDROID_S21_MCP_TOKEN":
            token = value.strip().strip('"').strip("'")
            if token:
                return token
    raise ValueError("ANDROID_S21_MCP_TOKEN is missing")


def rpc(
    url: str,
    payload: dict[str, Any],
    *,
    token: str = "",
    session_id: str = "",
    timeout: float,
) -> tuple[int, dict[str, Any], str]:
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "User-Agent": "gptadmin-s21-private-probe/1",
    }
    if token:
        headers["Authorization"] = "Bearer " + token
    if session_id:
        headers["mcp-session-id"] = session_id
    request = urllib.request.Request(
        url,
        data=json.dumps(payload, separators=(",", ":")).encode(),
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            body = response.read(16 << 20)
            parsed = json.loads(body) if body else {}
            return int(response.status), parsed, response.headers.get("mcp-session-id", "")
    except urllib.error.HTTPError as exc:
        # Never echo a body from an authentication failure: it is not evidence
        # and may contain implementation details.
        return int(exc.code), {}, exc.headers.get("mcp-session-id", "")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:18080/mcp")
    parser.add_argument(
        "--token-file",
        type=Path,
        default=Path("/etc/gptadmin/android-s21-mcp.env"),
    )
    parser.add_argument("--timeout", type=float, default=8.0)
    parser.add_argument("--attempts", type=int, default=10)
    parser.add_argument("--retry-delay", type=float, default=1.0)
    parser.add_argument("--expected-tool-count", type=int, required=True)
    parser.add_argument("--expected-tools-sha256", required=True)
    args = parser.parse_args()

    try:
        token = read_token(args.token_file)
        initialize = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "gptadmin-s21-probe", "version": "1"},
            },
        }
        status = 0
        initialized: dict[str, Any] = {}
        session_id = ""
        for attempt in range(max(args.attempts, 1)):
            try:
                status, initialized, session_id = rpc(
                    args.url,
                    initialize,
                    token=token,
                    timeout=args.timeout,
                )
            except (OSError, TimeoutError, urllib.error.URLError):
                status, initialized, session_id = 0, {}, ""
            if status == 200 and "result" in initialized and session_id:
                break
            if attempt + 1 < max(args.attempts, 1):
                time.sleep(max(args.retry_delay, 0.0))
        if status != 200 or "result" not in initialized or not session_id:
            raise RuntimeError(
                "authenticated initialize failed: "
                f"status={status} result={bool(initialized.get('result'))} "
                f"session={bool(session_id)}"
            )

        unauth_status, _, _ = rpc(args.url, initialize, timeout=args.timeout)

        notification = {
            "jsonrpc": "2.0",
            "method": "notifications/initialized",
            "params": {},
        }
        notified_status, _, _ = rpc(
            args.url,
            notification,
            token=token,
            session_id=session_id,
            timeout=args.timeout,
        )
        if notified_status not in {200, 202, 204}:
            raise RuntimeError("initialized notification failed")

        tools_request = {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}
        tools_status, tools_payload, _ = rpc(
            args.url,
            tools_request,
            token=token,
            session_id=session_id,
            timeout=args.timeout,
        )
        tools = tools_payload.get("result", {}).get("tools", [])
        names = sorted(
            tool.get("name", "")
            for tool in tools
            if isinstance(tool, dict) and isinstance(tool.get("name"), str) and tool["name"]
        )
        if tools_status != 200 or not names or len(names) != len(tools):
            raise RuntimeError("authenticated tools/list failed")
        digest = hashlib.sha256(("\n".join(names) + "\n").encode()).hexdigest()
        if len(names) != args.expected_tool_count or digest != args.expected_tools_sha256:
            raise RuntimeError(
                "tool surface mismatch: "
                f"count={len(names)} digest={digest} "
                f"expected_count={args.expected_tool_count} "
                f"expected_digest={args.expected_tools_sha256}"
            )
        receipt = {
            "authenticated": True,
            "ok": unauth_status == 401,
            "session": True,
            "tool_count": len(names),
            "tools_sha256": digest,
            "unauthenticated_status": unauth_status,
        }
        print(json.dumps(receipt, sort_keys=True, separators=(",", ":")))
        return 0 if receipt["ok"] else 1
    except Exception as exc:
        print(
            json.dumps(
                {"ok": False, "error": type(exc).__name__, "detail": str(exc)},
                sort_keys=True,
                separators=(",", ":"),
            ),
            file=sys.stderr,
        )
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
