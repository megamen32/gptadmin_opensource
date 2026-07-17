#!/usr/bin/env python3
"""Record public GPTAdmin Hub availability through an explicit HTTP proxy."""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import ssl
import subprocess
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path


def probe(opener: urllib.request.OpenerDirector, url: str, timeout: float) -> dict[str, object]:
    """Return one timing/status observation without treating HTTP auth as outage."""
    started = time.monotonic()
    request = urllib.request.Request(url, headers={"User-Agent": "gptadmin-public-monitor/1"})
    try:
        with opener.open(request, timeout=timeout) as response:
            return {"ok": True, "status": response.status, "elapsed_ms": round((time.monotonic() - started) * 1000, 1)}
    except urllib.error.HTTPError as error:
        return {"ok": True, "status": error.code, "elapsed_ms": round((time.monotonic() - started) * 1000, 1), "http_error": True}
    except (urllib.error.URLError, TimeoutError, ssl.SSLError, OSError) as error:
        return {"ok": False, "error": str(error), "elapsed_ms": round((time.monotonic() - started) * 1000, 1)}


def api_json(opener: urllib.request.OpenerDirector, url: str, token: str, payload: dict[str, object] | None, timeout: float) -> tuple[dict[str, object], float]:
    """Call an authenticated Custom GPT action and return its JSON response."""
    body = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(url, data=body, headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json", "User-Agent": "gptadmin-public-monitor/1"}, method="POST" if body is not None else "GET")
    started = time.monotonic()
    with opener.open(request, timeout=timeout) as response:
        return json.loads(response.read().decode("utf-8")), round((time.monotonic() - started) * 1000, 1)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", help="Public Hub base URL; defaults to HUB_PUBLIC_URL in --env-file")
    parser.add_argument("--proxy", default=os.environ.get("GPTADMIN_MONITOR_PROXY"), help="Optional HTTP(S) proxy; defaults to direct network access")
    parser.add_argument("--env-file", type=Path, default=Path("/etc/gptadmin/gptadmin.env"))
    parser.add_argument("--interval", type=float, default=5.0)
    parser.add_argument("--timeout", type=float, default=12.0)
    parser.add_argument("--output", type=Path, default=Path("trash/logs/custom-gpt-monitor.jsonl"))
    parser.add_argument("--max-seconds", type=float, default=900.0)
    args = parser.parse_args()
    try:
        raw_env = args.env_file.read_text(encoding="utf-8")
    except PermissionError:
        result = subprocess.run(["sudo", "-n", "cat", str(args.env_file)], text=True, capture_output=True)
        if result.returncode:
            parser.error("run sudo -v once so the monitor can mint its own JWT from gptadmin.env")
        raw_env = result.stdout
    env = dict(line.strip().split("=", 1) for line in raw_env.splitlines() if "=" in line and not line.lstrip().startswith("#"))
    args.proxy = args.proxy or env.get("GPTADMIN_MONITOR_PROXY")
    args.url = args.url or env.get("HUB_PUBLIC_URL") or env.get("PUBLIC_ORIGIN")
    if not args.url:
        parser.error("provide --url, or set HUB_PUBLIC_URL/PUBLIC_ORIGIN in the env file")
    secret = env.get("OAUTH_CLIENT_SECRET")
    if not secret:
        parser.error("OAUTH_CLIENT_SECRET is missing from the env file")
    now = int(time.time())
    encode = lambda value: base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).rstrip(b"=").decode()
    signing = f"{encode({'alg': 'HS256', 'typ': 'JWT'})}.{encode({'sub': 'admin', 'scope': 'gptadmin.read gptadmin.exec', 'client_id': 'availability-monitor', 'iss': args.url.rstrip('/'), 'aud': args.url.rstrip('/'), 'iat': now, 'exp': now + 3600})}"
    token = signing + "." + base64.urlsafe_b64encode(hmac.new(secret.encode(), signing.encode(), hashlib.sha256).digest()).rstrip(b"=").decode()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({"http": args.proxy, "https": args.proxy}) if args.proxy else urllib.request.ProxyHandler())
    base_url = args.url.rstrip("/")
    endpoints = [
        base_url + "/actions/openapi.yaml",
        base_url + "/mcp-relay/servers",
    ]
    deadline = time.monotonic() + args.max_seconds
    args.output.parent.mkdir(parents=True, exist_ok=True)
    while time.monotonic() < deadline:
        probes = {endpoints[0]: probe(opener, endpoints[0], args.timeout)}
        try:
            payload, elapsed = api_json(opener, endpoints[1], token, None, args.timeout)
            servers = payload.get("servers", [])
            probes[endpoints[1]] = {"ok": isinstance(servers, list), "status": 200, "elapsed_ms": elapsed, "server_count": len(servers)}
            for suffix in ("100", "44"):
                target = next((str(server.get("server_id")) for server in servers if str(server.get("server_id", "")).startswith("shell:") and suffix in str(server.get("server_id"))), "")
                key = f"shell-{suffix}"
                if not target:
                    probes[key] = {"ok": False, "error": "shell server is absent from listMcpServers"}
                    continue
                tools, tools_elapsed = api_json(opener, base_url + "/mcp-relay/tools", token, {"target": target}, args.timeout)
                tool_list = tools.get("tools", tools.get("response", {}).get("tools", [])) if isinstance(tools, dict) else []
                if not isinstance(tool_list, list) or not any(tool.get("name") == "shell_exec" for tool in tool_list if isinstance(tool, dict)):
                    probes[key] = {"ok": False, "target": target, "elapsed_ms": tools_elapsed, "error": "shell_exec missing from listMcpTools"}
                    continue
                result, call_elapsed = api_json(opener, base_url + "/mcp-relay/call", token, {"target": target, "tool_name": "shell_exec", "arguments": {"cmd": "uptime", "timeout": 20}}, args.timeout + 20)
                probes[key] = {"ok": str(result.get("status", "")) in {"completed", "success"}, "target": target, "elapsed_ms": call_elapsed, "result_status": result.get("status")}
        except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, ssl.SSLError, OSError, ValueError) as error:
            probes[endpoints[1]] = {"ok": False, "error": str(error)}
        observation = {"time": datetime.now(timezone.utc).isoformat(), "probes": probes}
        with args.output.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(observation, ensure_ascii=False) + "\n")
        if any(not result["ok"] for result in observation["probes"].values()):
            print(json.dumps(observation, ensure_ascii=False), flush=True)
            return 1
        time.sleep(args.interval)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
