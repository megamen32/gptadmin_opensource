#!/usr/bin/env python3
"""Run the signed GPTAdmin -> Agent Herder -> Notify live supertest.

The route owns the action chain and its secret. This helper reads that route
locally, never prints credentials, refuses the configured sleep window, and
prints only bounded job receipts.
"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path
from zoneinfo import ZoneInfo


def request_json(url: str, method: str, body: bytes, headers: dict[str, str]) -> dict:
    request = urllib.request.Request(url, data=body, method=method, headers=headers)
    with urllib.request.urlopen(request, timeout=15) as response:
        value = json.loads(response.read())
    if not isinstance(value, dict):
        raise RuntimeError("GPTAdmin returned a non-object response")
    return value


def signed_request(base_url: str, path: str, secret: str, method: str, body: bytes, idempotency_key: str) -> dict:
    timestamp = str(int(time.time()))
    canonical = "\n".join((method.upper(), path, timestamp, idempotency_key, hashlib.sha256(body).hexdigest()))
    signature = "sha256=" + hmac.new(secret.encode(), canonical.encode(), hashlib.sha256).hexdigest()
    return request_json(
        base_url.rstrip("/") + path,
        method,
        body,
        {
            "Content-Type": "application/json",
            "X-Webhook-Timestamp": timestamp,
            "X-Webhook-Signature": signature,
            "Idempotency-Key": idempotency_key,
        },
    )


def quiet_hours_active() -> bool:
    local = datetime.now(timezone.utc).astimezone(ZoneInfo("Europe/Moscow"))
    return 1 <= local.hour < 9


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", default="/etc/gptadmin/webhooks.json")
    parser.add_argument("--route", default="noticeplace-full-supertest-5")
    parser.add_argument("--base-url", default="http://127.0.0.1:9001")
    parser.add_argument("--max-wait-seconds", type=int, default=900)
    args = parser.parse_args()
    if quiet_hours_active():
        raise SystemExit("refusing live call canary during 01:00-09:00 Europe/Moscow quiet hours")

    routes = json.loads(Path(args.config).read_text())
    route = next((item for item in routes.get("routes", []) if item.get("id") == args.route), None)
    if not isinstance(route, dict) or not route.get("hmac_secret"):
        raise SystemExit(f"route {args.route!r} with local HMAC secret was not found")

    correlation = "live-supertest-" + uuid.uuid4().hex
    event = {
        "correlation_id": correlation,
        "event": "presentation-supertest",
        "severity": "critical",
        "message": "Mandatory full orchestration canary.",
        "requested_at": datetime.now(timezone.utc).isoformat(),
    }
    body = json.dumps(event, ensure_ascii=False, separators=(",", ":")).encode()
    path = f"/webhooks/v1/{args.route}"
    accepted = signed_request(args.base_url, path, route["hmac_secret"], "POST", body, correlation)
    job_id = str(accepted.get("job_id") or "")
    if not job_id:
        raise SystemExit("webhook did not return a job_id")

    deadline = time.monotonic() + max(1, args.max_wait_seconds)
    terminal: dict = {}
    while time.monotonic() < deadline:
        terminal = signed_request(
            args.base_url,
            f"/webhook-jobs/{job_id}",
            route["hmac_secret"],
            "GET",
            b"",
            "poll-" + uuid.uuid4().hex,
        )
        if terminal.get("status") in {"completed", "failed"}:
            break
        time.sleep(5)

    safe = {
        "correlation_id": correlation,
        "job_id": job_id,
        "route_id": terminal.get("route_id", args.route),
        "status": terminal.get("status", "timeout"),
        "error": terminal.get("error"),
        "result": terminal.get("result"),
    }
    print(json.dumps(safe, ensure_ascii=False, indent=2))
    return 0 if safe["status"] == "completed" else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.HTTPError as error:
        raise SystemExit(f"GPTAdmin HTTP error: {error.code}")
