#!/usr/bin/env python3
"""Adapter from GPTAdmin Webhooks to Agent Herder's local session wake API."""
from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path

HERDER_URL = "http://127.0.0.1:18787/api/sessions/new-or-resume"
ALLOWED_HARNESSES = {"codex", "opencode", "zcode"}
EXPECTED_SCHEMA = "gptadmin.agent-wake.v1"
MAX_MESSAGE_BYTES = 32_000


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser()
    p.add_argument("--schema", required=True)
    p.add_argument("--harness", required=True)
    p.add_argument("--name", required=True)
    p.add_argument("--cwd", required=True)
    p.add_argument("--event-id", required=True)
    p.add_argument("--source", required=True)
    p.add_argument("--subject", required=True)
    p.add_argument("--payload", required=True)
    return p.parse_args()


def main() -> int:
    a = parse_args()
    if a.schema != EXPECTED_SCHEMA:
        print(json.dumps({"ok": False, "error": "unsupported schema"}), file=sys.stderr)
        return 2
    if a.harness not in ALLOWED_HARNESSES:
        print(json.dumps({"ok": False, "error": "unsupported harness"}), file=sys.stderr)
        return 2
    try:
        resolved = Path(a.cwd).resolve()
    except (OSError, RuntimeError, ValueError):
        print(json.dumps({"ok": False, "error": "invalid cwd"}), file=sys.stderr)
        return 2
    if not Path(a.cwd).is_absolute() or Path('/home/roomhacker') not in resolved.parents:
        print(json.dumps({"ok": False, "error": "cwd outside allowed root"}), file=sys.stderr)
        return 2
    a.cwd = str(resolved)
    if not a.name.strip() or len(a.name) > 128 or len(a.event_id) > 256 or len(a.subject) > 512:
        print(json.dumps({"ok": False, "error": "invalid target or event id"}), file=sys.stderr)
        return 2

    try:
        source = json.loads(a.source)
        payload = json.loads(a.payload)
    except json.JSONDecodeError as exc:
        print(json.dumps({"ok": False, "error": f"invalid structured event field: {exc}"}), file=sys.stderr)
        return 2

    message = "\n".join((
        "[gptadmin.agent-wake.v1]",
        f"event_id: {a.event_id}",
        f"source: {json.dumps(source, ensure_ascii=False, separators=(',', ':'))}",
        f"subject: {a.subject}",
        f"payload: {json.dumps(payload, ensure_ascii=False, separators=(',', ':'))}",
    ))
    if len(message.encode("utf-8")) > MAX_MESSAGE_BYTES:
        print(json.dumps({"ok": False, "error": "wake message too large"}), file=sys.stderr)
        return 2

    request_body = json.dumps({
        "harness": a.harness,
        "name": a.name,
        "cwd": a.cwd,
        "message": message,
        "mode": "queue",
    }, ensure_ascii=False).encode()
    request = urllib.request.Request(
        HERDER_URL,
        data=request_body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            result = json.load(response)
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        print(json.dumps({"ok": False, "error": f"Agent Herder request failed: {exc}"}), file=sys.stderr)
        return 3

    if not isinstance(result, dict) or result.get("ok") is not True:
        print(json.dumps(result, ensure_ascii=False), file=sys.stderr)
        return 4
    print(json.dumps({
        "ok": True,
        "sessionId": result.get("sessionId"),
        "created": result.get("created"),
        "delivery": result.get("delivery"),
        "harness": result.get("harness"),
        "name": result.get("name"),
        "cwd": result.get("cwd"),
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
