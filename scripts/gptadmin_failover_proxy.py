#!/usr/bin/env python3
"""Tiny fallback-side HTTP proxy for GPTAdmin failover.

It serves the public service route during takeover. The special reclaim accept
path writes a signed demote command for the watchdog; all other HTTP requests
are proxied to the local standby hub.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

RECLAIM_PATH = "/admin/api/failover/reclaim/accept"


def write_json_0600(path: str, data: dict[str, Any]) -> None:
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_suffix(p.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
    os.chmod(tmp, 0o600)
    tmp.replace(p)


class Handler(BaseHTTPRequestHandler):
    server_version = "GPTAdminFailoverProxy/1"

    def log_message(self, fmt: str, *args: Any) -> None:
        sys.stderr.write("%s - %s\n" % (self.log_date_time_string(), fmt % args))

    def _json(self, status: int, body: dict[str, Any]) -> None:
        data = json.dumps(body, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self) -> None:  # noqa: N802
        if self.path.split("?", 1)[0] == RECLAIM_PATH:
            try:
                length = int(self.headers.get("Content-Length") or "0")
                payload = json.loads(self.rfile.read(length).decode() or "{}")
                if payload.get("action") != "demote" or not payload.get("node_id") or not payload.get("signature"):
                    self._json(400, {"ok": False, "detail": "invalid reclaim payload"})
                    return
                payload["received_at_proxy"] = int(__import__("time").time())
                payload["accepted_by_proxy"] = self.server.node_id  # type: ignore[attr-defined]
                write_json_0600(self.server.command_file, payload)  # type: ignore[attr-defined]
                self._json(200, {"ok": True, "accepted": True, "queued": True, "node_id": payload.get("node_id")})
            except Exception as exc:
                self._json(500, {"ok": False, "detail": str(exc)})
            return
        self._proxy()

    def do_GET(self) -> None:  # noqa: N802
        self._proxy()

    def do_HEAD(self) -> None:  # noqa: N802
        self._proxy(head=True)

    def _proxy(self, head: bool = False) -> None:
        upstream = self.server.upstream.rstrip("/") + self.path  # type: ignore[attr-defined]
        data = None
        if self.command not in ("GET", "HEAD"):
            length = int(self.headers.get("Content-Length") or "0")
            data = self.rfile.read(length) if length else None
        headers = {k: v for k, v in self.headers.items() if k.lower() not in {"host", "content-length", "connection"}}
        req = urllib.request.Request(upstream, data=data, headers=headers, method=self.command)
        try:
            with urllib.request.urlopen(req, timeout=self.server.timeout_sec) as resp:  # type: ignore[attr-defined] # noqa: S310
                body = resp.read() if not head else b""
                self.send_response(int(getattr(resp, "status", 200)))
                for k, v in resp.headers.items():
                    if k.lower() not in {"transfer-encoding", "connection", "content-length"}:
                        self.send_header(k, v)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                if not head:
                    self.wfile.write(body)
        except urllib.error.HTTPError as exc:
            body = exc.read() if not head else b""
            self.send_response(exc.code)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if not head:
                self.wfile.write(body)
        except Exception as exc:
            self._json(502, {"ok": False, "detail": str(exc)})


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen", default=os.environ.get("GPTADMIN_FAILOVER_PROXY_LISTEN", "127.0.0.1:9101"))
    ap.add_argument("--upstream", default=os.environ.get("GPTADMIN_FAILOVER_PROXY_UPSTREAM", "http://127.0.0.1:9001"))
    ap.add_argument("--command-file", default=os.environ.get("GPTADMIN_FAILOVER_RECLAIM_COMMAND_FILE", "/var/lib/gptadmin/failover_reclaim_command.json"))
    ap.add_argument("--node-id", default=os.environ.get("GPTADMIN_FAILOVER_NODE_ID", "shell:haos"))
    ap.add_argument("--timeout", type=float, default=30.0)
    args = ap.parse_args()
    host, _, port_s = args.listen.rpartition(":")
    host = host or "127.0.0.1"
    port = int(port_s or "9101")
    httpd = ThreadingHTTPServer((host, port), Handler)
    httpd.upstream = args.upstream  # type: ignore[attr-defined]
    httpd.command_file = args.command_file  # type: ignore[attr-defined]
    httpd.node_id = args.node_id  # type: ignore[attr-defined]
    httpd.timeout_sec = args.timeout  # type: ignore[attr-defined]
    print(json.dumps({"ok": True, "listen": args.listen, "upstream": args.upstream, "command_file": args.command_file, "node_id": args.node_id}, sort_keys=True), flush=True)
    httpd.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
