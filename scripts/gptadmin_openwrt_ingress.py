#!/usr/bin/env python3
"""Prepare the three public MCP SNI routes in OpenWrt's haproxy-became service.

Render on server 100 using a private copy of /etc/haproxy-became.cfg and its
local owner_local voter enrollment. Output contains the public routing checks.
The renderer never installs a file, starts a Node, or imports a state store.

Before activation, verify both pinned TLS paths with an existing read-only
bearer. Repeat the server100 check with HAOS absent in the isolated rehearsal.
Validate the private candidate on OpenWrt with haproxy -c -f /dev/stdin.
After operator approval, the existing swap helper accepts this invocation on the
gateway (the candidate and both reviewed digests are supplied by the operator):
  sh gptadmin_haos_ingress_swap.sh --openwrt-became CANDIDATE BASE_SHA256 NEW_SHA256
It saves /etc/gptadmin-wan-before.BASE_SHA256.cfg, checks the procd master and
gracefully reloads just that master. Verify the three public names from WAN.
For rollback pass the saved file, NEW_SHA256 and BASE_SHA256 to the same helper.
On an ambiguous reload reconcile its lock, installed hash and worker first.
"""
from __future__ import annotations

import argparse
from contextlib import closing
from dataclasses import dataclass
import hashlib
import http.client
import json
from pathlib import Path
import re
import socket
import ssl
import sys
from urllib.parse import urlsplit

from gptadmin_failover_proxy import IDENTIFIER
from gptadmin_quorum_ingress import section, write_new

HUB_NAMES = ("mcp.bezrabotnyi.com", "gptadminmcp.bezrabotnyi.com",
             "u-f1102930.t.gptadmin.bezrabotnyi.com")
ISSUER = "https://" + HUB_NAMES[0]
SERVER100 = "192.168.2.100"
VOTERS = {SERVER100, "192.168.2.5", "192.168.2.75"}
TLS_TARGETS = {"haos": ("192.168.2.101", 8443), "server100": (SERVER100, 443)}
CA = "/etc/ssl/certs/ca-certificates.crt"
PREFIX = "gptadmin_wan_"
DISCOVERY = "/.well-known/oauth-authorization-server"


class GatewayError(ValueError):
    """A diagnostic that may safely be shown to the operator."""


@dataclass(frozen=True)
class Enrollment:
    cluster: str
    local: str
    others: tuple[str, str]

    @classmethod
    def read(cls, path: Path) -> Enrollment:
        info = json.loads(path.read_bytes())
        # The running Node must also attest the effective mode in /healthz.
        if not isinstance(info, dict) or info.get("mode", "") not in ("", "owner_local"):
            raise GatewayError("review the owner_local enrollment for server 100")
        ids = {}
        peers = info.get("peers")
        if not isinstance(peers, list) or len(peers) != 3 or not all(isinstance(peer, dict) for peer in peers):
            raise GatewayError("review the three enrolled voters")
        for peer in peers:
            dest = urlsplit(peer.get("internal_hub_url", ""))
            if (dest.scheme != "https" or dest.hostname not in VOTERS or not dest.port
                    or dest.username is not None or dest.path not in ("", "/") or dest.query or dest.fragment
                    or dest.hostname in ids):
                raise GatewayError("each voter needs a distinct direct LAN mTLS listener")
            ids[dest.hostname] = peer["id"]
        if (set(ids) != VOTERS or len(set(ids.values())) != 3 or ids[SERVER100] != info.get("node_id")
                or not all(isinstance(v, str) and IDENTIFIER.fullmatch(v)
                           for v in (info["cluster_id"], *ids.values()))):
            raise GatewayError("review the cluster and all three enrolled identities on server 100")
        return cls(info["cluster_id"], ids[SERVER100], (ids["192.168.2.5"], ids["192.168.2.75"]))


def health_pattern(enrolled: Enrollment, route: str, follower: bool) -> str:
    # /healthz is a flat JSON object. Match whole top-level fields in any order.
    # Bind leader_id and node_id to each other for an elected primary. Single
    # quotes in the HAProxy config preserve the PCRE escapes verbatim.
    def field(name, value):
        return rf'(?=(?:[^{{}}]*,)?\s*"{name}"\s*:\s*{value}\s*[,}}])'

    def one_of(ids):
        return "(?:" + "|".join(re.escape(i) for i in ids) + ")"

    local = re.escape(enrolled.local)
    if follower:
        leader = '"' + one_of(enrolled.others) + '"'
        node = '"' + local + '"'
    else:
        eligible = local if route == "server100" else one_of((enrolled.local, *enrolled.others))
        leader = '"(?P<elected>' + eligible + ')"'
        node = '"(?P=elected)"'
    ready = "false" if follower else "true"
    fields = {"quorum_mode": '"owner_local"', "cluster_id": '"' + re.escape(enrolled.cluster) + '"', "leader_id": leader,
              "node_id": node, "voter_count": "3", "replica_ready": "true", "ok": ready,
              "hub_ready": ready, "role": '"follower"' if follower else '"leader"'}
    if follower:
        fields["baseline_ready"] = "true"
    return r"^\s*\{" + "".join(field(k, v) for k, v in fields.items()) + r"[^{}]*\}\s*$"


def checked_backend(label: str, host: str, route: str, follower: bool, enrolled: Enrollment, ca: str) -> str:
    address, port = TLS_TARGETS[route]
    def probe(path):
        # Select the H1 multiplexer explicitly for these TLS health checks.
        return (f"    http-check connect ssl sni {host} alpn http/1.1 proto h1\n"
                f"    http-check send meth GET uri {path} ver HTTP/1.1 hdr Host {host}")

    # Discovery passes through Node's quorum admission, including the private
    # mTLS hop on a follower. Local /healthz alone cannot prove that hop.
    # HAProxy 3.0 starts checked servers UP. The per-worker latch is unset
    # until both HTTP checks complete; thereafter nbsrv tracks failures.
    identity = health_pattern(enrolled, route, follower)
    issuer = rf'[{{,]\s*"issuer"\s*:\s*"{re.escape(ISSUER)}"\s*[,}}]'
    return f"""
backend {label}
    mode tcp
    retries 0
    no option redispatch
    load-server-state-from-file none
    option httpchk
    timeout check 3s
{probe("/healthz")}
    http-check expect status {503 if follower else 200}
    http-check expect hdr name content-type value -m beg application/json
    http-check expect rstring '{identity}'
{probe(DISCOVERY)}
    http-check expect status 200
    http-check expect hdr name content-type value -m beg application/json
    http-check expect rstring '{issuer}'
    http-check set-var(proc.{label}) int(1)
    server tls {address}:{port} check verify required verifyhost {host} ca-file {ca} inter 4s fall 1 rise 1
"""


def directives(block):
    return [line for line in (s.partition("#")[0].split() for s in block.splitlines()[1:]) if line]


def render(base: str, enrolled: Enrollment, ca: str = CA) -> str:
    if PREFIX in base or not re.fullmatch(r"/[a-zA-Z0-9._/-]+", ca):
        raise GatewayError("review the original gateway configuration and CA path")
    front = section(base, "frontend", "public_https_sni")
    # This service is small and independently managed. Require its reviewed
    # semantics; a new wildcard ACL before our rules would shadow this change.
    if directives(front[0]) != [
        ["bind", "192.168.2.1:19443"], ["tcp-request", "inspect-delay", "4s"],
        ["tcp-request", "content", "accept", "if", "{", "req.ssl_hello_type", "1", "}"],
        ["acl", "is_became", "req.ssl_sni", "-i", "became.bezrabotnyi.com"],
        ["use_backend", "public_became", "if", "is_became"], ["default_backend", "existing_haos_ingress"],
    ]:
        raise GatewayError("gateway frontend changed since review")
    for backend, name, route in (("public_became", "website100", "server100"),
                                 ("existing_haos_ingress", "haos_ingress", "haos")):
        addr, port = TLS_TARGETS[route]
        if directives(section(base, "backend", backend)[0]) != [
                ["server", name, f"{addr}:{port}", "check", "inter", "3s", "fall", "2", "rise", "2"]]:
            raise GatewayError("installed TLS destination or backend policy changed")

    rules, backends = [], []
    for index, host in enumerate(HUB_NAMES):
        acl = f"{PREFIX}sni{index + 1}"
        rules.append(f"    acl {acl} req.ssl_sni -i {host}\n")
        for route, follower in (("haos", False), ("server100", False), ("server100", True)):
            label = f"{acl}_{route}" + ("_follower" if follower else "")
            rules.append(f"    use_backend {label} if {acl} {{ var(proc.{label}) -m int 1 }} {{ nbsrv({label}) gt 0 }}\n")
            backends.append(checked_backend(label, host, route, follower, enrolled, ca))
        rules.append(f"    use_backend {PREFIX}closed if {acl}\n")

    pos = front.start() + re.search(r"(?m)^[ \t]*default_backend\b", front[0]).start()
    return base[:pos] + "".join(rules) + base[pos:] + f"\nbackend {PREFIX}closed\n    mode tcp\n" + "".join(backends)


def pinned_read(host, route, context, path, bearer=None):
    address = TLS_TARGETS[route]
    headers = {"Host": host, "Connection": "close"}
    body = None
    if bearer is not None:
        headers.update({"Authorization": "Bearer " + bearer, "Content-Type": "application/json",
                        "Accept": "application/json, text/event-stream"})
        body = b'{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
    with closing(http.client.HTTPSConnection(host, context=context, timeout=5)) as conn:
        with socket.create_connection(address, timeout=5) as sock:
            conn.sock = context.wrap_socket(sock, server_hostname=host)
            conn.auto_open = False
            conn.request("POST" if body else "GET", path, body=body, headers=headers)
            response = conn.getresponse()
            limit = (1 << 20) if body else 16384
            raw = response.read(limit + 1)
            if len(raw) > limit:
                raise GatewayError("probe response too large")
            return response.status, raw


def verify(enrolled, routes, bearer, ca=CA):
    context = ssl.create_default_context(cafile=ca)
    for route in routes:
        for host in HUB_NAMES:
            status, health = pinned_read(host, route, context, "/healthz")
            follower = status == 503 and route == "server100"
            if (status != (503 if follower else 200)
                    or not re.fullmatch(health_pattern(enrolled, route, follower), health.decode())):
                raise GatewayError(f"{route} {host} lacks the enrolled ready quorum identity")
            status, meta = pinned_read(host, route, context, DISCOVERY)
            metadata = json.loads(meta)
            if status != 200 or not isinstance(metadata, dict) or metadata.get("issuer") != ISSUER:
                raise GatewayError(f"{route} {host} cannot reach the public OAuth issuer")
            status, result = pinned_read(host, route, context, "/mcp", bearer)
            rpc = json.loads(result)
            result = rpc.get("result") if isinstance(rpc, dict) else None
            tools = result.get("tools") if isinstance(result, dict) else None
            if (status != 200 or not isinstance(tools, list) or rpc.get("id") != 1 or "error" in rpc
                    or not any(isinstance(t, dict) and t.get("name") == "search" for t in tools)):
                raise GatewayError(f"{route} {host} authenticated tools/list failed")
            print(f"{route} {host} TLS, {'follower' if follower else 'leader'}, OAuth, MCP search OK")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="action", required=True)
    build = sub.add_parser("render", help="write a new mode-0600 candidate and print its SHA256")
    build.add_argument("--base", type=Path, required=True)
    build.add_argument("--base-sha256", required=True)
    build.add_argument("--out", type=Path, required=True)
    check = sub.add_parser("verify", help="direct TLS, identity, issuer and authenticated tools/list on all three names")
    check.add_argument("--route", choices=["haos", "server100", "both"], default="both")
    check.add_argument("--bearer-file", type=Path, required=True)
    for command in (build, check):
        command.add_argument("--quorum", required=True, type=Path, help="server 100's reviewed owner-local enrollment")
    args = parser.parse_args()
    enrolled = Enrollment.read(args.quorum)
    if args.action == "render":
        raw = args.base.read_bytes()
        if hashlib.sha256(raw).hexdigest() != args.base_sha256:
            raise GatewayError("installed base hash differs from review")
        write_new(args.out, render(raw.decode(), enrolled).encode())
    else:
        bearer = args.bearer_file.read_text().strip()
        if not bearer or any(c.isspace() or not c.isascii() or not c.isprintable() for c in bearer):
            raise GatewayError("invalid bearer file")
        verify(enrolled, TLS_TARGETS if args.route == "both" else [args.route], bearer)


if __name__ == "__main__":
    try:
        main()
    except GatewayError as exc:
        sys.exit(str(exc))
    except (OSError, TypeError, KeyError, ValueError, http.client.HTTPException):
        # Do not echo enrollment values, HTTP bodies, headers or the bearer.
        sys.exit("gateway preparation or verification failed")
