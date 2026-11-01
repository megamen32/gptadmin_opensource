#!/usr/bin/env python3
from __future__ import annotations
import json, re, sys, urllib.error, urllib.request
from pathlib import Path

HERDER_MCP = "http://127.0.0.1:18787/mcp"
EXPECTED = "gptadmin.agent-deliver.v1"
ALLOWED = {"opencode", "codex", "zcode", "claude", "qoder", "hermes", "fast-agent", "chatgpt"}

def fail(msg: str, code: int = 2) -> int:
    print(json.dumps({"ok": False, "error": msg}, ensure_ascii=False), file=sys.stderr)
    return code

def main() -> int:
    if len(sys.argv) != 2:
        return fail("expected one JSON event argument")
    try:
        event = json.loads(sys.argv[1])
    except Exception as exc:
        return fail(f"invalid event JSON: {exc}")
    if not isinstance(event, dict) or event.get("schema") != EXPECTED:
        return fail("unsupported schema")
    target = event.get("target", {})
    delivery = event.get("delivery", {})
    if not isinstance(target, dict) or not isinstance(delivery, dict):
        return fail("target and delivery must be objects")
    harness = str(target.get("harness") or "").strip()
    name = str(target.get("name") or "").strip()
    cwd = str(target.get("cwd") or "").strip()
    if not Path(cwd).is_absolute():
        return fail("cwd must be absolute")
    try:
        resolved = Path(cwd).resolve()
    except (OSError, RuntimeError, ValueError):
        return fail("invalid cwd")
    if harness not in ALLOWED or not name or len(name) > 128 or Path('/home/roomhacker') not in resolved.parents:
        return fail("invalid target")
    cwd = str(resolved)
    for key, allowed in {"create": {"if_missing", "never"}, "activation": {"always", "if_running", "defer"}, "mode": {"queue", "sync"}}.items():
        if key in delivery and (not isinstance(delivery[key], str) or delivery[key] not in allowed):
            return fail(f"invalid delivery.{key}")
    args = {
        "harness": harness,
        "name": name,
        "cwd": cwd,
        "message": "\n".join([
            "[gptadmin.agent-deliver.v1]",
            f"event_id: {event.get('event_id','')}",
            f"source: {json.dumps(event.get('source'), ensure_ascii=False, separators=(',',':'))}",
            f"subject: {str(event.get('subject') or '')}",
            f"payload: {json.dumps(event.get('payload'), ensure_ascii=False, separators=(',',':'))}",
        ]),
        "create": str(delivery.get("create") or "if_missing"),
        "activation": str(delivery.get("activation") or "always"),
        "mode": str(delivery.get("mode") or "queue"),
    }
    if len(args["message"].encode("utf-8")) > 32_000:
        return fail("delivery message too large")
    request_body = json.dumps({"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deliver","arguments":args}}, ensure_ascii=False).encode()
    req = urllib.request.Request(HERDER_MCP, data=request_body, headers={"Content-Type":"application/json","Accept":"application/json, text/event-stream"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=180) as response:
            raw = response.read().decode()
        match = re.search(r"^data: (.*)$", raw, re.M)
        payload = json.loads(match.group(1) if match else raw)
        if not isinstance(payload, dict) or payload.get("error"):
            return fail("Agent Herder returned a JSON-RPC error", 4)
        result = payload.get("result") or {}
        if not isinstance(result, dict) or result.get("isError"):
            return fail("Agent Herder tool failed", 4)
        content = result.get("content") or []
        text = next((x.get("text") for x in content if isinstance(x, dict) and x.get("type") == "text"), None)
        parsed = result.get("structuredContent") or (json.loads(text) if text else result)
    except (urllib.error.URLError, OSError, TimeoutError, ValueError, TypeError) as exc:
        return fail(f"Agent Herder request failed: {exc}", 3)
    if not isinstance(parsed, dict) or parsed.get("ok") is not True:
        print(json.dumps(parsed, ensure_ascii=False), file=sys.stderr)
        return 4
    print(json.dumps(parsed, ensure_ascii=False))
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
