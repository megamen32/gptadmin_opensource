#!/usr/bin/env python3
"""Push signed primary reclaim/demote message to the current GPTAdmin service URL."""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import secrets
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


def load_json(path: str) -> dict[str, Any]:
    return json.loads(Path(path).read_text())


def load_env(path: str) -> dict[str, str]:
    env: dict[str, str] = {}
    p = Path(path)
    if not p.exists():
        return env
    for line in p.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        env[k.strip()] = v.strip().strip('"').strip("'")
    return env


def sig_input(action: str, node_id: str, nonce: str, issued_at: int, expires_at: int, primary_health_url: str) -> str:
    return "\n".join([action, node_id, nonce, str(issued_at), str(expires_at), primary_health_url])


def sign(secret: str, text: str) -> str:
    digest = hmac.new(secret.encode(), text.encode(), hashlib.sha256).digest()
    return base64.urlsafe_b64encode(digest).decode().rstrip("=")


def post_json(url: str, payload: dict[str, Any], timeout: float) -> tuple[int, str]:
    data = json.dumps(payload).encode()
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json", "User-Agent": "gptadmin-primary-reclaim/1"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:  # noqa: S310 - admin configured URL
            return int(getattr(r, "status", 0)), r.read().decode(errors="replace")[:1000]
    except urllib.error.HTTPError as exc:
        return int(exc.code), exc.read().decode(errors="replace")[:1000]
    except OSError as exc:
        return 0, str(exc)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--config", default=os.environ.get("GPTADMIN_FAILOVER_CONFIG_FILE", "/etc/gptadmin/failover_config.json"))
    ap.add_argument("--env", default="/etc/gptadmin/gptadmin.env")
    ap.add_argument("--attempts", type=int, default=3)
    ap.add_argument("--delay", type=float, default=2.0)
    ap.add_argument("--timeout", type=float, default=5.0)
    ap.add_argument("--best-effort", action="store_true", default=True)
    args = ap.parse_args()
    try:
        cfg = load_json(args.config)
        env = {**load_env(args.env), **os.environ}
        secret = env.get("MCP_BRIDGE_KEY") or env.get("CTL_TOKEN") or ""
        if not secret:
            print(json.dumps({"ok": False, "detail": "missing signing key"}, sort_keys=True))
            return 0 if args.best_effort else 2
        service = str(cfg.get("primary_public_url") or env.get("HUB_PUBLIC_URL") or "").rstrip("/")
        url = str(cfg.get("primary_reclaim_accept_url") or "").strip()
        if not url:
            if not service:
                print(json.dumps({"ok": False, "detail": "missing service URL"}, sort_keys=True))
                return 0 if args.best_effort else 2
            url = service + "/admin/api/failover/reclaim/accept"
        primary_health_url = str(cfg.get("primary_health_url") or service + "/healthz")
        ttl = int(cfg.get("reclaim_max_age_sec") or 120)
        nodes = [n for n in cfg.get("nodes") or [] if n.get("enabled")]
        if not nodes:
            print(json.dumps({"ok": True, "detail": "no enabled failover nodes"}, sort_keys=True))
            return 0
        results = []
        for attempt in range(1, max(1, args.attempts) + 1):
            issued_at = int(time.time())
            expires_at = issued_at + ttl
            for node in nodes:
                node_id = str(node.get("server_id") or "")
                if not node_id:
                    continue
                nonce = secrets.token_urlsafe(18)
                payload = {
                    "action": "demote",
                    "node_id": node_id,
                    "issued_at": issued_at,
                    "expires_at": expires_at,
                    "nonce": nonce,
                    "primary_health_url": primary_health_url,
                    "alg": "hmac-sha256",
                }
                payload["signature"] = sign(secret, sig_input("demote", node_id, nonce, issued_at, expires_at, primary_health_url))
                status, body = post_json(url, payload, args.timeout)
                results.append({"attempt": attempt, "node_id": node_id, "status": status, "body": body})
                if 200 <= status < 300 and '"accepted":true' in body.replace(" ", ""):
                    print(json.dumps({"ok": True, "accepted": True, "url": url, "node_id": node_id, "attempt": attempt, "results": results}, sort_keys=True))
                    return 0
            if attempt < args.attempts:
                time.sleep(args.delay)
        print(json.dumps({"ok": True, "accepted": False, "url": url, "results": results}, sort_keys=True))
        return 0
    except Exception as exc:
        print(json.dumps({"ok": False, "detail": str(exc)}, sort_keys=True))
        return 0 if args.best_effort else 1


if __name__ == "__main__":
    raise SystemExit(main())
