"""Focused checks for the edge contract, including an ambiguous upstream POST."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import threading
import time
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import gptadmin_quorum_ingress as ingress
from gptadmin_failover_proxy import IngressPool, quorum_health


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def base_config(bind, old):
    shared = ""
    for name in ("child", "hub"):
        shared += f"""
backend be_gptadmin_{name}
    mode http
    option httpchk
    http-check send meth GET uri /healthz ver HTTP/1.1 hdr Host public.test
    http-check expect status 200
    server old 127.0.0.1:{old} check
"""
    return f"""global
    nbthread 1
defaults
    mode http
    timeout connect 2s
    timeout client 10s
    timeout server 10s
frontend recovery_https_http
    bind {bind}
    acl host_gptadmin_child hdr(host) -i child.test
    acl host_gptadmin_hub hdr(host) -i public.test
    use_backend be_gptadmin_child if host_gptadmin_child
    use_backend be_gptadmin_hub if host_gptadmin_hub
""" + shared


def pool_for(port=8443, ca="/etc/ssl/certs/ca-certificates.crt"):
    hosts = ["a.test", "b.test", "c.test"]
    private = {"cluster_id": "cluster", "node_key": "private-input-only",
               "peers": [{"id": "node-" + host[0], "hub_url": "https://" + host,
                          "node_key_sha256": "private-input-only"} for host in hosts]}
    return ingress.project(private, [f"{host}=127.0.0.1:{port}" for host in hosts], str(ca))


def test_projection_binds_identity_pin_and_ca_and_excludes_credentials():
    pool = pool_for()
    member = pool.members[0]
    assert "private-input-only" not in json.dumps(pool.projection())
    assert pool.check_path(member) != pool_for(443).check_path(member)
    assert pool.check_path(member) != pool.check_path(pool.members[1])
    for field, bad in (("host", "a.test\nbackend injected"), ("port", True), ("address", "public.test"),
                       ("host", pool.members[1].host), ("node_id", pool.members[1].node_id)):
        data = pool.projection()
        data["members"][0] = {**data["members"][0], field: bad}
        with pytest.raises(ValueError):
            IngressPool.parse(data)


def test_render_preserves_edge_and_refuses_unreviewed_policy(tmp_path):
    base = base_config("127.0.0.1:12345", 12346)
    pool = pool_for()
    shadow = ingress.render(base, pool, False, 19101, 19102, 9101)
    assert shadow.startswith(base)
    active = ingress.render(base, pool, True, 19101, 19102, 9101)
    start, _, backends = base.partition("backend be_gptadmin_child\n")
    assert "backend be_gptadmin_child\n" + backends in active
    for route in ingress.PUBLIC_ROUTES:
        assert f"use_backend be_gptadmin_{route} if" not in active
    for changed in (base.replace("mode http\n    option", "mode http\n    http-request deny\n    option"),
                    base.replace("    option httpchk", "http-request deny\n    option httpchk"),
                    base.replace(" if host_gptadmin_child\n", " if some_other_acl\n"),
                    base + start, active):
        with pytest.raises(ValueError):
            ingress.render(changed, pool, True, 19101, 19102, 9101)
    target = tmp_path / "candidate"
    ingress.write_new(target, b"first")
    assert target.stat().st_mode & 0o777 == 0o600
    with pytest.raises(FileExistsError):
        ingress.write_new(target, b"second")
    assert target.read_bytes() == b"first"


@pytest.fixture
def tls_backend(tmp_path):
    cert = tmp_path / "ca.pem"
    key = tmp_path / "server.key"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                    "-subj", "/CN=test", "-addext", "subjectAltName=DNS:a.test,DNS:b.test,DNS:c.test",
                    "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True)
    state = {"leader": "a.test", "requests": [], "override": None, "status": 200}

    class Peer(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            host = self.connection.peer_name
            if self.path == "/healthz":
                leader = state["leader"]
                is_leader = host == leader
                payload = {"ok": is_leader, "hub_ready": is_leader, "replica_ready": True, "baseline_ready": True,
                           "cluster_id": "cluster", "node_id": "node-" + host[0], "voter_count": 3,
                           "leader_id": "node-" + leader[0], "role": "leader" if is_leader else "follower"}
                if state["override"] is not None:
                    payload = state["override"]
                self.send_response(state["status"] if is_leader else 503)
            else:
                payload = {"member": host, "public": self.headers.get("X-Forwarded-Host"), "host": self.headers["Host"]}
                self.send_response(200)
            data = json.dumps(payload).encode()
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_POST(self):
            state["requests"].append((self.connection.peer_name, self.rfile.read(int(self.headers["Content-Length"]))))
            state["leader"] = "b.test"
            # The upstream consumed and committed the request, then lost the response.
            self.connection.shutdown(socket.SHUT_RDWR)

    with ThreadingHTTPServer(("127.0.0.1", 0), Peer) as server:
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(cert, key)
        ctx.set_servername_callback(lambda sock, name, _ctx: setattr(sock, "peer_name", name))
        server.socket = ctx.wrap_socket(server.socket, server_side=True)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            yield pool_for(server.server_port, cert), state
        finally:
            server.shutdown()
            worker.join()


def test_quorum_requires_verified_tls_and_exact_ready_identity(tls_backend):
    pool, state = tls_backend
    member = pool.members[0]
    context = ssl.create_default_context(cafile=pool.ca_file)
    assert ingress.verify_cutover(pool)
    assert ingress.verify_cutover(pool, "a.test")
    assert not ingress.verify_cutover(pool, "b.test")
    assert quorum_health(pool, member, ssl.create_default_context()) is None
    assert quorum_health(pool, pool.members[1], context) == ("follower", "node-a")
    good = dict(ok=True, hub_ready=True, replica_ready=True, role="leader", cluster_id="cluster",
                node_id="node-a", leader_id="node-a", voter_count=3)
    for changed in ({"ok": True}, {"ok": True, "role": "leader"}, [], {**good, "role": "follower"},
                    {**good, "cluster_id": "other"}, {**good, "node_id": "node-b"}, {**good, "leader_id": "node-b"},
                    {**good, "voter_count": 1}, {**good, "hub_ready": 1}, {**good, "replica_ready": False},
                    {**good, "padding": "x" * 9000}):
        state["override"] = changed
        assert quorum_health(pool, member, context) is None
    state.update(override=good, status=307)
    assert not ingress.verify_cutover(pool)


def haproxy_binary():
    binary = os.getenv("HAPROXY_BIN") or shutil.which("haproxy") or "/usr/sbin/haproxy"
    version = subprocess.run([binary, "-v"], capture_output=True, text=True) if Path(binary).is_file() else None
    if version is None or tuple(map(int, version.stdout.split()[2].split(".")[:2])) < (3, 1):
        pytest.skip("set HAPROXY_BIN to the recovery edge HAProxy 3.2 binary")
    return str(Path(binary).resolve())


def test_real_haproxy_never_replays_ambiguous_post(tmp_path, tls_backend):
    binary = haproxy_binary()
    pool, state = tls_backend
    ports = set()
    while len(ports) < 5:
        ports.add(free_port())
    public, checks, tunnel, reclaim, old = ports
    config = tmp_path / "edge.cfg"
    config.write_text(ingress.render(base_config(f"127.0.0.1:{public}", old), pool, True, checks, tunnel, reclaim))
    manifest = tmp_path / "pool.json"
    manifest.write_text(json.dumps(pool.projection()))
    def request(method, path, body=None):
        conn = HTTPConnection("127.0.0.1", public, timeout=3)
        try:
            conn.request(method, path, body=body, headers={"Host": "public.test"})
            res = conn.getresponse()
            return res.status, res.read()
        finally:
            conn.close()

    def wait_for(predicate):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except OSError:
                pass
            time.sleep(.1)
        pytest.fail("edge transition timed out")

    edge = subprocess.Popen([binary, "-db", "-f", str(config)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    checker = None
    try:
        wait_for(lambda: request("GET", "/resource")[0] == 503)  # cold start must fail closed
        checker = subprocess.Popen([sys.executable, ROOT / "scripts/gptadmin_failover_proxy.py",
                                    "--quorum-checks", str(manifest), "--check-port", str(checks)])
        wait_for(lambda: request("GET", "/resource")[0] == 200)
        assert json.loads(request("GET", "/resource")[1]) == {"member": "a.test", "public": "public.test", "host": "a.test"}
        status, _ = request("POST", "/once", b"single dispatch")
        assert status == 502
        wait_for(lambda: b"b.test" in request("GET", "/resource")[1])
        assert state["requests"] == [("a.test", b"single dispatch")]
        checker.terminate()
        checker.wait(timeout=5)
        wait_for(lambda: request("GET", "/resource")[0] == 503)
    finally:
        for proc in (checker, edge):
            if proc and proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=5)


def test_haos_private_listener_selects_fixed_lan_by_sni():
    base = base_config("127.0.0.1:12443", 12080)
    pool = pool_for(19103)
    for i, (label, m) in enumerate(zip(("100", "44", "88"), pool.members)):
        anchor = "    acl host_gptadmin_child"
        physical = (f"    acl host_gptadmin{label} hdr(host),field(1,:),lower -i {m.host}\n"
                    f"    use_backend be_gptadmin{label}_http if host_gptadmin{label}\n")
        base = base.replace(anchor, physical + anchor)
        base += f"\nbackend be_gptadmin{label}_http\n    mode http\n    server member 127.0.0.1:{14000 + i} check\n"
    shadow = ingress.render(base, pool, False, 19101, 19102, 9101, 19103)
    assert shadow.startswith(base)
    assert "bind 127.0.0.1:19103 ssl crt /data/certs/" in shadow
    assert "http-request set-header Host" not in shadow
    for i, m in enumerate(pool.members, 1):
        assert f"use_backend gptadmin_quorum_lan{i} if {{ ssl_fc_sni -i {m.host} }}" in shadow
        assert f"server node 127.0.0.1:{13999+i}\n" in shadow
    for bad in (base.replace(":14002 check", ":14001 check"), base.replace("a.test", "wrong.test"),
                base.replace("    server member", "    http-request auth\n    server member")):
        with pytest.raises(ValueError):
            ingress.render(bad, pool, False, 19101, 19102, 9101, 19103)


@pytest.mark.parametrize("router", [False, True], ids=["haos", "openwrt-became"])
def test_guarded_reload_and_rollback_on_real_master(tmp_path, router):
    binary = (os.getenv("OPENWRT_HAPROXY_BIN") if router else None) or haproxy_binary()
    listen = free_port()
    def config(reply):
        return (f"global\n    nbthread 1\ndefaults\n    mode http\n    timeout client 2s\n"
                f"    timeout connect 2s\n    timeout server 2s\nfrontend client\n    bind 127.0.0.1:{listen}\n"
                f"    http-request return status 200 content-type text/plain string {reply}\n").encode()
    def observe(expected):
        for _ in range(100):
            c = HTTPConnection("127.0.0.1", listen, timeout=1)
            try:
                c.request("GET", "/")
                if c.getresponse().read() == expected:
                    return
            except OSError:
                pass
            finally:
                c.close()
            time.sleep(.03)
        pytest.fail("unexpected response from the reloaded worker")

    live_name = "haproxy-became.cfg" if router else "haproxy.cfg"
    live, users, candidate = (tmp_path / name for name in (live_name, "router-users.cfg", "candidate.cfg"))
    live.write_bytes(config("old"))
    users.write_text("# independent recovery user list\n")
    candidate.write_bytes(config("new"))
    before = hashlib.sha256(live.read_bytes()).hexdigest()
    after = hashlib.sha256(candidate.read_bytes()).hexdigest()
    def swap(source, previous, selected):
        mode = ["--openwrt-became"] if router else []
        return subprocess.run(["sh", ROOT / "scripts/gptadmin_haos_ingress_swap.sh", *mode, source, previous, selected],
                              capture_output=True, timeout=20,
                              env={**os.environ, "GPTADMIN_EDGE_DIR": str(tmp_path), "GPTADMIN_EDGE_BIN": binary})
    user_args = [] if router else ["-f", users]
    master = subprocess.Popen([binary, "-W", "-db", "-f", live, *user_args],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        observe(b"old")
        assert swap(candidate, "0"*64, after).returncode != 0
        observe(b"old")
        receipt = swap(candidate, before, after)
        assert receipt.returncode == 0, receipt.stderr
        assert after.encode() in receipt.stdout
        observe(b"new")
        assert swap(candidate, before, after).returncode != 0
        prefix = "gptadmin-wan-before." if router else "gptadmin-ingress-before."
        backup = tmp_path / (prefix + before + ".cfg")
        assert backup.read_bytes() == config("old") and backup.stat().st_mode & 0o777 == 0o600
        assert swap(backup, after, before).returncode == 0
        observe(b"old")
        assert master.poll() is None
    finally:
        master.terminate()
        master.wait(timeout=10)


def test_legacy_tunnel_returns_redirect_to_client():
    from gptadmin_failover_proxy import Handler, NoRedirect
    from urllib.request import build_opener
    seen = []
    class Redirect(BaseHTTPRequestHandler):
        def do_POST(self):
            seen.append("POST")
            self.send_response(302)
            self.send_header("Location", "/redirected")
            self.end_headers()

        def do_GET(self):
            seen.append("GET")
            self.send_response(200)
            self.end_headers()

        def log_message(self, *_args):
            pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Redirect) as remote, ThreadingHTTPServer(("127.0.0.1", 0), Handler) as proxy:
        proxy.upstream = f"http://127.0.0.1:{remote.server_port}"
        proxy.timeout_sec = 3
        proxy.opener = build_opener(NoRedirect())
        for server in (remote, proxy):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        client = HTTPConnection("127.0.0.1", proxy.server_port, timeout=3)
        try:
            client.request("POST", "/operation", body=b"x")
            response = client.getresponse()
            assert (response.status, response.headers.get("Location")) == (302, "/redirected")
            assert seen == ["POST"]
        finally:
            client.close()
            proxy.shutdown()
            remote.shutdown()
