#!/usr/bin/env python3
"""Prepare the existing HAOS HAProxy ingress offline. Outputs are new, private files.

project reads one enrolled voter's configuration on its owner. Copy only the
resulting pool to /data/config/ingress-quorum.json in the standby add-on and
enable quorum_ingress_checks. Both containers must trust the specified CA.
For the current HAOS SNI routes, use --haos-physical-port 19103 and pin each name to 127.0.0.1:19103.

Render shadow and active from the same hash-pinned private copy of the edge.
Shadow adds a loopback HTTP frontend (19102) and leaves public routing intact.
Validate both with the recovery image's haproxy -c and its router-users.cfg.
Install shadow using the edge owner's locked hash-checked reload. Probe
19102 with the public Host, verify OAuth discovery and an authenticated MCP
read, then apply active only if the installed hash still equals shadow.
Verify through the public hostname from outside. To roll back, compare the
installed hash to active and restore the saved shadow (or original) with the
same controller. Reconcile an unknown reload by reading its receipt and hash.
Keep physical routes, public DNS, grants and existing sessions throughout.

An FRP tunnel can explicitly target 127.0.0.1:19102 after the public checks:
the signed reclaim POST still reaches the existing proxy on port 9101.
"""
from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import sys
from urllib.parse import urlsplit

from gptadmin_failover_proxy import IngressPool, Member, verify_cutover

PUBLIC_ROUTES = ("child", "hub")
FRONTEND = "recovery_https_http"
PREFIX = "gptadmin_quorum_"


def project(quorum: dict, pins: list[str], ca_file: str) -> IngressPool:
    endpoints = {}
    for pin in pins:
        host, separator, destination = pin.partition("=")
        target = urlsplit("//" + destination)
        if (not separator or host in endpoints or not target.hostname or not target.port
                or target.username is not None or target.path or target.query or target.fragment):
            raise ValueError("each physical hostname requires exactly one IP:port pin")
        endpoints[host] = (str(ipaddress.ip_address(target.hostname)), target.port)
    members = []
    for peer in quorum["peers"]:
        origin = urlsplit(peer["hub_url"])
        if (origin.scheme != "https" or not origin.hostname or origin.username is not None
                or origin.path not in ("", "/") or origin.query or origin.fragment):
            raise ValueError("invalid physical Hub HTTPS origin")
        if origin.hostname not in endpoints:
            raise ValueError("missing or duplicate pin for enrolled physical origin")
        address, port = endpoints.pop(origin.hostname)
        members.append({"node_id": peer["id"], "host": origin.hostname, "address": address, "port": port})
    if endpoints:
        raise ValueError("pin outside enrolled voter set")
    return IngressPool.parse({"cluster_id": quorum["cluster_id"], "members": members, "ca_file": ca_file})


def section(text: str, kind: str, name: str) -> re.Match:
    # Indentation is optional in HAProxy, including for access-control rules.
    # Stop only at a section declaration; refuse unfamiliar backend directives.
    sections = (r"global|defaults|frontend|backend|listen|userlist|resolvers|peers|mailers|"
                r"cache|ring|program|http-errors|log-forward|fcgi-app|crt-store|traces")
    pattern = rf"(?ms)^[ \t]*{kind}[ \t]+{name}[ \t]*\n.*?(?=^[ \t]*(?:{sections})(?:[ \t]|$)|\Z)"
    matches = list(re.finditer(pattern, text))
    if len(matches) != 1:
        raise ValueError("missing or ambiguous HAProxy section: " + name)
    return matches[0]


def backend_name(index: int) -> str:
    return f"{PREFIX}voter{index + 1}"


def selection(pool: IngressPool, condition: str) -> str:
    lines = []
    for index in range(len(pool.members)):
        name = backend_name(index)
        tests = f"{condition} " if condition else ""
        lines.append(f"    use_backend {name} if {tests}{{ nbsrv({name}) gt 0 }}")
    fallback = "use_backend" if condition else "default_backend"
    suffix = " if " + condition if condition else ""
    lines.append(f"    {fallback} {PREFIX}unavailable{suffix}")
    return "\n".join(lines) + "\n"


def physical_listener(base: str, frontend: str, pool: IngressPool, port: int, certs: str) -> str:
    # The public HAOS frontend rewrites X-Forwarded-Host. Its physical health
    # checks can also hide a follower's identity behind a synthetic 503. A
    # loopback SNI listener reaches exactly the same fixed LAN destinations
    # while preserving the client Host and the Node's full health response.
    if not re.fullmatch(r"/[A-Za-z0-9._/-]+", certs):
        raise ValueError("invalid HAOS certificate directory")
    destinations = []
    routes = []
    for index, member in enumerate(pool.members):
        if member.address != "127.0.0.1" or member.port != port:
            raise ValueError("all physical names must pin the dedicated loopback TLS listener")
        names = re.findall(r"(?m)^\s*acl\s+(host_gptadmin(?:100|44|88))\s+"
                           r"hdr\(host\),field\(1,:\),lower\s+-i\s+" + re.escape(member.host) + r"[ \t]*$", frontend)
        if len(names) != 1:
            raise ValueError("missing or ambiguous exact physical Host ACL")
        rules = re.findall(r"(?m)^\s*use_backend\s+(be_gptadmin(?:100|44|88)_http)\s+if\s+"
                           + names[0] + r"[ \t]*$", frontend)
        if len(rules) != 1:
            raise ValueError("missing or ambiguous exact physical backend")
        directives = reviewed_backend(base, rules[0])
        item = next(line for line in directives if line.startswith("server ")).split()[2]
        target = urlsplit("//" + item)
        if target.username is not None or target.path or target.query or target.fragment or not target.port:
            raise ValueError("physical backend must identify a fixed IP:port")
        address = str(ipaddress.ip_address(target.hostname))
        lan = Member(member.node_id, member.host, address, target.port).destination
        if lan in destinations:
            raise ValueError("physical voters must have distinct LAN destinations")
        destinations.append(lan)
        routes.append(f"    use_backend {PREFIX}lan{index + 1} if {{ ssl_fc_sni -i {member.host} }}")
    result = (f"\nfrontend {PREFIX}physical\n    mode http\n"
              f"    bind 127.0.0.1:{port} ssl crt {certs} alpn http/1.1\n"
              + "\n".join(routes) + f"\n    default_backend {PREFIX}unavailable\n")
    for index, address in enumerate(destinations):
        result += (f"\nbackend {PREFIX}lan{index + 1}\n    mode http\n    retries 0\n"
                   f"    retry-on none\n    no option redispatch\n    server node {address}\n")
    return result


def reviewed_backend(base: str, name: str) -> list[str]:
    directives = [line.split("#", 1)[0].strip() for line in section(base, "backend", name)[0].splitlines()[1:]]
    allowed = {"mode http", "option httpchk", "http-check expect status 200", ""}
    servers = [line for line in directives if line.startswith("server ")]
    if (len(servers) != 1 or not re.fullmatch(r"server [A-Za-z0-9_.-]+ [0-9a-fA-F:.\[\]]+ check"
                                              r"(?: (?:inter|fall|rise) [0-9]+(?:ms|s)?)*", servers[0])
            or any(line not in allowed and not line.startswith(
                ("http-check send meth GET uri /healthz ", "server ")) for line in directives)):
        raise ValueError("existing backend policy requires review: " + name)
    return directives


def render(base: str, pool: IngressPool, active: bool, check_port: int, tunnel_port: int, reclaim_port: int,
           physical_port: int | None = None, certs: str = "/data/certs/") -> str:
    ports = [check_port, tunnel_port, reclaim_port]
    if physical_port is not None:
        ports.append(physical_port)
    if len(set(ports)) != len(ports) or not all(1 <= port <= 65535 for port in ports):
        raise ValueError("distinct valid ports required")
    if re.search(rf"(?m)^(frontend|backend|listen)\s+{PREFIX}", base):
        raise ValueError("render both stages from the original reviewed edge")
    frontend = section(base, "frontend", FRONTEND)
    updated = frontend[0]
    for route in PUBLIC_ROUTES:
        original_name = "be_gptadmin_" + route
        reviewed_backend(base, original_name)
        condition = "host_gptadmin_" + route
        rule = rf"(?m)^[ \t]+use_backend[ \t]+{original_name}[ \t]+if[ \t]+{condition}[ \t]*\n"
        if len(re.findall(rule, updated)) != 1:
            raise ValueError("existing public Host rule requires review")
        if active:
            updated = re.sub(rule, lambda _: selection(pool, condition), updated)
    result = base[:frontend.start()] + updated + base[frontend.end():]
    if physical_port is not None:
        result += physical_listener(base, frontend[0], pool, physical_port, certs)
    result += f"""
frontend {PREFIX}tunnel
    mode http
    bind 127.0.0.1:{tunnel_port}
    use_backend {PREFIX}reclaim if {{ method POST }} {{ path /admin/api/failover/reclaim/accept }}
""" + selection(pool, "")
    result += f"""
backend {PREFIX}reclaim
    mode http
    retries 0
    retry-on none
    server local_reclaim 127.0.0.1:{reclaim_port}

backend {PREFIX}unavailable
    mode http
    http-request return status 503
"""
    for index, member in enumerate(pool.members):
        # Direct TLS vhosts receive the same authority as their readiness probe.
        # The dedicated HAOS SNI bridge accepts the original public Host.
        host_rule = "" if physical_port is not None else f"    http-request set-header Host {member.host}\n"
        result += f"""
backend {backend_name(index)}
    mode http
    retries 0
    retry-on none
    no option redispatch
    load-server-state-from-file none
    http-request deny deny_status 503 if {{ req.hdr(X-GPTAdmin-Quorum-Ingress) -m found }}
    http-request set-header X-GPTAdmin-Quorum-Ingress 1
    http-request set-header X-Forwarded-Host %[req.hdr(host)]
    http-request set-header X-Forwarded-Proto https
{host_rule}    option httpchk
    timeout check 3s
    http-check connect addr 127.0.0.1 port {check_port}
    http-check send meth GET uri {pool.check_path(member)} ver HTTP/1.1 hdr Host localhost
    http-check expect status 200
    server physical {member.destination} ssl sni str({member.host}) verify required verifyhost {member.host} ca-file {pool.ca_file} alpn http/1.1 check inter 2s fastinter 1s fall 2 rise 1 init-state fully-down
"""
    return result


def write_new(path: Path, content: bytes) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(content)
    except BaseException:
        path.unlink()
        raise
    print(hashlib.sha256(content).hexdigest())


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="action", required=True)
    projection = sub.add_parser("project", help="export only enrolled IDs, verified TLS names and TCP pins")
    projection.add_argument("--quorum", type=Path, required=True)
    projection.add_argument("--pin", action="append", required=True, metavar="TLS_NAME=IP:PORT")
    projection.add_argument("--ca-file", default="/etc/ssl/certs/ca-certificates.crt")
    projection.add_argument("--out", type=Path, required=True)
    preflight = sub.add_parser("verify", help="require three distinct ready replicas agreeing on one leader before cutover")
    preflight.add_argument("--pool", type=Path, required=True)
    preflight.add_argument("--rollback-host", help="check the original physical backend before reverting the edge")
    build = sub.add_parser("render", help="write a new HAProxy candidate without installing it")
    build.add_argument("--base", type=Path, required=True)
    build.add_argument("--base-sha256", required=True)
    build.add_argument("--pool", type=Path, required=True)
    build.add_argument("--stage", choices=["shadow", "active"], required=True)
    build.add_argument("--check-port", type=int, default=19101)
    build.add_argument("--tunnel-port", type=int, default=19102)
    build.add_argument("--reclaim-port", type=int, default=9101)
    build.add_argument("--haos-physical-port", type=int, help="generate the dedicated loopback TLS-to-LAN bridge")
    build.add_argument("--certs", default="/data/certs/", help="HAOS physical listener certificate directory")
    build.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if args.action == "project":
        pool = project(json.loads(args.quorum.read_bytes()), args.pin, args.ca_file)
        write_new(args.out, (json.dumps(pool.projection(), indent=2) + "\n").encode())
    elif args.action == "verify":
        if not verify_cutover(IngressPool.parse(json.loads(args.pool.read_bytes())), args.rollback_host):
            sys.exit("quorum ingress is not ready for cutover")
        print("ready")
    else:
        raw = args.base.read_bytes()
        if hashlib.sha256(raw).hexdigest() != args.base_sha256:
            raise ValueError("base hash changed")
        pool = IngressPool.parse(json.loads(args.pool.read_bytes()))
        candidate = render(raw.decode(), pool, args.stage == "active", args.check_port, args.tunnel_port,
                           args.reclaim_port, args.haos_physical_port, args.certs)
        write_new(args.out, candidate.encode())


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, TypeError) as error:
        # Input may contain private TLS material or enrollment credentials.
        sys.exit("quorum ingress preparation refused (" + type(error).__name__ + ")")
