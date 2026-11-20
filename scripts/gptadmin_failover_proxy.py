#!/usr/bin/env python3
"""Tiny fallback-side HTTP proxy for GPTAdmin failover.

It serves the public service route during takeover. The special reclaim accept
path writes a signed demote command for the watchdog; all other HTTP requests
are proxied to the local standby hub.
"""
from __future__ import annotations

import argparse
import hashlib
import http.client
import ipaddress
import json
import os
import re
import socket
import ssl
import sys
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

RECLAIM_PATH = "/admin/api/failover/reclaim/accept"


# The edge and the check service both consume this routing-only projection.
# Never give the edge a voter's enrollment key or Raft configuration.
IDENTIFIER = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}")
DNS_LABEL = re.compile(r"[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?")


@dataclass(frozen=True)
class Member:
    node_id: str
    host: str
    address: str
    port: int

    @property
    def destination(self) -> str:
        ip = ipaddress.ip_address(self.address)
        return f"[{ip}]:{self.port}" if ip.version == 6 else f"{ip}:{self.port}"


@dataclass(frozen=True)
class IngressPool:
    cluster_id: str
    members: tuple[Member, ...]
    ca_file: str

    @classmethod
    def parse(cls, data: dict) -> IngressPool:
        if not isinstance(data, dict) or set(data) != {"cluster_id", "members", "ca_file"}:
            raise ValueError("invalid quorum ingress fields")
        cluster, entries, ca = data["cluster_id"], data["members"], data["ca_file"]
        if not isinstance(cluster, str) or not IDENTIFIER.fullmatch(cluster):
            raise ValueError("invalid cluster ID")
        if not isinstance(entries, list) or len(entries) != 3:
            raise ValueError("three enrolled ingress members required")
        if not isinstance(ca, str) or not re.fullmatch(r"/[A-Za-z0-9._/-]+", ca):
            raise ValueError("an absolute CA bundle path is required")
        members = []
        for entry in entries:
            if not isinstance(entry, dict) or set(entry) != {"node_id", "host", "address", "port"}:
                raise ValueError("invalid member fields")
            member = Member(**entry)
            if not isinstance(member.node_id, str) or not IDENTIFIER.fullmatch(member.node_id):
                raise ValueError("invalid member ID")
            if (not isinstance(member.host, str) or len(member.host) > 253
                    or not all(DNS_LABEL.fullmatch(label) for label in member.host.split("."))):
                raise ValueError("invalid physical TLS name")
            if (not isinstance(member.address, str) or "%" in member.address
                    or str(ipaddress.ip_address(member.address)) != member.address
                    or type(member.port) is not int or not 1 <= member.port <= 65535):
                raise ValueError("a pinned IP and port are required")
            members.append(member)
        if len({p.host for p in members}) != 3 or len({p.node_id for p in members}) != 3:
            raise ValueError("distinct physical TLS names and node IDs required")
        return cls(cluster, tuple(members), ca)

    def projection(self) -> dict:
        return {"cluster_id": self.cluster_id, "members": [asdict(p) for p in self.members], "ca_file": self.ca_file}

    def check_path(self, member: Member) -> str:
        digest = hashlib.sha256(json.dumps(self.projection(), sort_keys=True).encode()).hexdigest()
        return f"/ready/{digest}/{member.node_id}"


def quorum_health(pool: IngressPool, member: Member, context: ssl.SSLContext) -> tuple[str, str] | None:
    # Pin the socket independently of public DNS. Use the data connection's
    # physical SNI and Host. A redirect is never an identity assertion.
    connection = http.client.HTTPSConnection(member.host, context=context, timeout=2)
    try:
        with socket.create_connection((member.address, member.port), timeout=2) as tcp:
            connection.sock = context.wrap_socket(tcp, server_hostname=member.host)
            connection.auto_open = False
            connection.request("GET", "/healthz", headers={
                "Host": member.host, "X-GPTAdmin-Quorum-Ingress": "probe", "Connection": "close",
            })
            response = connection.getresponse()
            body = response.read(8193)
            if response.status not in (200, 503) or len(body) > 8192:
                return None
            value = json.loads(body)
            if (not isinstance(value, dict) or value.get("cluster_id") != pool.cluster_id
                    or value.get("node_id") != member.node_id or value.get("replica_ready") is not True
                    or type(value.get("voter_count")) is not int or value["voter_count"] != len(pool.members)):
                return None
            leader = value.get("leader_id")
            if (response.status == 200 and value.get("role") == "leader" and leader == member.node_id
                    and value.get("ok") is True and value.get("hub_ready") is True):
                return ("leader", leader)
            if (response.status == 503 and isinstance(leader, str) and value.get("role") == "follower" and leader != member.node_id
                    and leader in {p.node_id for p in pool.members} and value.get("ok") is False
                    and value.get("hub_ready") is False and value.get("baseline_ready") is True):
                return ("follower", leader)
    except (OSError, ValueError, http.client.HTTPException, RecursionError):
        pass
    finally:
        connection.close()
    return None


def verify_cutover(pool: IngressPool, rollback_host: str | None = None) -> bool:
    tls = ssl.create_default_context(cafile=pool.ca_file)
    if rollback_host:
        return any(member.host == rollback_host and quorum_health(pool, member, tls) == ("leader", member.node_id)
                   for member in pool.members)
    views = [quorum_health(pool, member, tls) for member in pool.members]
    leaders = [view[1] for view in views if view and view[0] == "leader"]
    return len(leaders) == 1 and all(view and view[1] == leaders[0] for view in views)


def serve_quorum_checks(path: Path, port: int) -> None:
    pool = IngressPool.parse(json.loads(path.read_text()))
    tls = ssl.create_default_context(cafile=pool.ca_file)
    routes = {pool.check_path(member): member for member in pool.members}

    class Readiness(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            member = routes.get(self.path)
            ready = member and quorum_health(pool, member, tls) == ("leader", member.node_id)
            self.send_response(200 if ready else 503)
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, *_args: Any) -> None:
            pass

    with ThreadingHTTPServer(("127.0.0.1", port), Readiness) as server:
        server.serve_forever()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


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
            with self.server.opener.open(req, timeout=self.server.timeout_sec) as resp:  # type: ignore[attr-defined] # noqa: S310
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
            for k, v in exc.headers.items():
                if k.lower() not in {"transfer-encoding", "connection", "content-length"}:
                    self.send_header(k, v)
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
    ap.add_argument("--timeout", type=float, default=130.0, help="upstream request timeout; must exceed the Hub long-poll limit")
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--quorum-checks", type=Path, help="serve HAProxy readiness checks on loopback")
    mode.add_argument("--verify-quorum", type=Path, help="verify the staged pool from the installed add-on")
    ap.add_argument("--rollback-host", help="require the original physical backend to be the verified leader")
    ap.add_argument("--check-port", type=int, default=19101)
    args = ap.parse_args()
    if args.rollback_host and not args.verify_quorum:
        ap.error("--rollback-host requires --verify-quorum")
    if args.verify_quorum:
        try:
            ready = verify_cutover(IngressPool.parse(json.loads(args.verify_quorum.read_bytes())), args.rollback_host)
        except (OSError, ValueError):
            ready = False
        print("ready" if ready else "quorum ingress verification failed")
        return 0 if ready else 1
    if args.quorum_checks:
        try:
            serve_quorum_checks(args.quorum_checks, args.check_port)
        except (OSError, ValueError):
            ap.exit(1, "invalid or unavailable quorum ingress configuration or check listener\n")
        return 0
    host, _, port_s = args.listen.rpartition(":")
    host = host or "127.0.0.1"
    port = int(port_s or "9101")
    httpd = ThreadingHTTPServer((host, port), Handler)
    httpd.opener = urllib.request.build_opener(NoRedirect())
    httpd.upstream = args.upstream  # type: ignore[attr-defined]
    httpd.command_file = args.command_file  # type: ignore[attr-defined]
    httpd.node_id = args.node_id  # type: ignore[attr-defined]
    httpd.timeout_sec = args.timeout  # type: ignore[attr-defined]
    print(json.dumps({"ok": True, "listen": args.listen, "upstream": args.upstream, "command_file": args.command_file, "node_id": args.node_id}, sort_keys=True), flush=True)
    httpd.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
