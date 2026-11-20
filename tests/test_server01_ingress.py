"""Unit coverage for the WAN overlay and its checked TLS routes."""
import json
from pathlib import Path
import re
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import gptadmin_server01_ingress as gateway


def gateway_base():
    return """global
    nbthread 1
defaults
    mode tcp
    timeout connect 5s
    timeout client 120s
    timeout server 120s
frontend public_https_sni
    bind 192.0.2.1:19443
    tcp-request inspect-delay 4s
    tcp-request content accept if { req.ssl_hello_type 1 }
    acl is_became req.ssl_sni -i became.bezrabotnyi.com
    use_backend public_became if is_became
    default_backend existing_haos_ingress
backend public_became
    server website100 192.0.2.2:443 check inter 3s fall 2 rise 2
backend existing_haos_ingress
    server haos_ingress 192.0.2.5:8443 check inter 3s fall 2 rise 2
"""


MEMBERS = gateway.Enrollment("cluster.1", "n-100", ("n-44", "n-88"))


@pytest.mark.parametrize("mode", [None, "owner_local", "replicated"])
def test_enrollment_requires_direct_voter_addresses_and_owner_local(tmp_path, mode):
    peers = [{"id": n, "internal_hub_url": "https://" + ip + ":9500"}
             for n, ip in zip(("n-100", "n-44", "n-88"), ("192.0.2.2", "192.0.2.3", "192.0.2.4"))]
    info = {"node_id": "n-100", "cluster_id": "cluster.1", "peers": peers}
    if mode is not None:
        info["mode"] = mode
    path = tmp_path / "enrollment"
    path.write_text(json.dumps(info))
    if mode == "replicated":
        with pytest.raises(ValueError):
            gateway.Enrollment.read(path)
        return
    assert gateway.Enrollment.read(path) == MEMBERS
    for invalid in ("https://192.0.2.5:8443", "https://mcp.bezrabotnyi.com",
                    "http://192.0.2.3:9500", "https://192.0.2.2:9501"):
        peers[1]["internal_hub_url"] = invalid
        path.write_text(json.dumps(info))
        with pytest.raises(ValueError):
            gateway.Enrollment.read(path)


def test_original_routes_and_tls_policy():
    base = gateway_base()
    cut = base.index("    default_backend")
    candidate = gateway.render(base, MEMBERS)
    assert candidate.startswith(base[:cut])
    assert base[cut:] in candidate
    for name in gateway.HUB_NAMES:
        assert candidate.count("req.ssl_sni -i " + name + "\n") == 1
        assert candidate.count(f"verify required verifyhost {name} ca-file ") == 3
    assert candidate.count("    retries 0\n") == 9
    assert candidate.count("http-check set-var(proc.") == 9
    assert candidate.count("use_backend gptadmin_wan_closed if ") == len(gateway.HUB_NAMES)
    for unsafe in (candidate, base.replace("is_became req.ssl_sni", "is_became req.ssl_sni -m end"),
                   base.replace("default_backend", "use_backend new if from_lan\n    default_backend"),
                   base.replace("192.0.2.5:8443", "192.0.2.1:19443")):
        with pytest.raises(ValueError):
            gateway.render(unsafe, MEMBERS)


@pytest.mark.parametrize("route,follower,node,leader", [
    ("haos", False, "n-44", "n-44"), ("server100", False, "n-100", "n-100"),
    ("server100", True, "n-100", "n-88"),
])
def test_only_enrolled_owner_local_health_in_any_field_order(route, follower, node, leader):
    health = dict(cluster_id=MEMBERS.cluster, role="follower" if follower else "leader", leader_id=leader,
                  node_id=node, voter_count=3, replica_ready=True, ok=not follower, hub_ready=not follower,
                  quorum_mode="owner_local")
    if follower:
        health["baseline_ready"] = True
    pattern = gateway.health_pattern(MEMBERS, route, follower)
    assert re.fullmatch(pattern, json.dumps(health))
    assert re.fullmatch(pattern, json.dumps(dict(reversed(health.items())), separators=(",", ":")))
    for key in health:
        assert not re.fullmatch(pattern, json.dumps({k: v for k, v in health.items() if k != key}))
    for key, bad in (("node_id", "alien"), ("leader_id", "n-100" if follower else "n-88"),
                     ("cluster_id", "clusterX1"), ("quorum_mode", "replicated"), ("voter_count", 30),
                     ("ok", follower), ("hub_ready", follower), ("replica_ready", False)):
        assert not re.fullmatch(pattern, json.dumps({**health, key: bad}))
    assert not re.fullmatch(pattern, json.dumps({"nested": health}))


def test_verify_aborts_before_sending_bearer_to_unready_origin(monkeypatch):
    seen = []
    def read(host, route, context, path, bearer=None):
        seen.append((path, bearer is not None))
        return 200, b'{"ok":true}'
    monkeypatch.setattr(gateway, "pinned_read", read)
    with pytest.raises(ValueError, match="quorum identity"):
        gateway.verify(MEMBERS, ["server100"], "local-test")
    assert seen == [("/healthz", False)]


def test_tcp_gateway_health_and_single_post(tmp_path):
    """Exercise the gateway binary with controlled health inputs and lost responses."""
    from contextlib import ExitStack, closing
    import http.client
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
    import os
    import socket
    import ssl
    import subprocess
    import threading
    import time

    binary = os.environ.get("OPENWRT_HAPROXY_BIN") or os.environ.get("HAPROXY_BIN")
    if not binary:
        pytest.skip("set OPENWRT_HAPROXY_BIN to the gateway HAProxy executable")
    cert, key = tmp_path / "ca.pem", tmp_path / "key"
    # The third exact alias deliberately has no valid certificate at either edge.
    names = [*gateway.HUB_NAMES[:2], "other.test", "became.bezrabotnyi.com"]
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                    "-subj", "/CN=test", "-addext", "subjectAltName=" + ",".join("DNS:" + n for n in names),
                    "-keyout", str(key), "-out", str(cert)], capture_output=True, check=True)
    server_tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    server_tls.load_cert_chain(cert, key)
    client_tls = ssl.create_default_context(cafile=cert)

    class Origin(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            status, value = 200, self.server.route
            if self.path == "/healthz":
                if self.server.route == "haos":
                    value = {"ok": True}
                else:
                    status = 503
                    value = {"ok": False, "hub_ready": False, "quorum_mode": "owner_local", "node_id": MEMBERS.local,
                             "leader_id": MEMBERS.others[0], "role": "follower", "replica_ready": True,
                             "baseline_ready": True, "cluster_id": MEMBERS.cluster, "voter_count": 3}
            elif self.path == gateway.DISCOVERY:
                status = self.server.discovery_status
                value = {"issuer": gateway.ISSUER}
            raw = json.dumps(value).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self):
            self.server.received.append(self.rfile.read(int(self.headers["Content-Length"])))
            self.close_connection = True

    with ExitStack() as stack:
        def origin(name):
            server = ThreadingHTTPServer(("127.0.0.1", 0), Origin)
            server.route, server.discovery_status, server.received = name, 503, []
            server.socket = server_tls.wrap_socket(server.socket, server_side=True)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            stack.callback(server.server_close)
            stack.callback(server.shutdown)
            return server

        primary, backup = origin("haos"), origin("server100")
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        text = gateway.render(gateway_base(), MEMBERS, str(cert))
        for original, chosen in (("192.0.2.1:19443", port), ("192.0.2.5:8443", primary.server_port),
                                 ("192.0.2.2:443", backup.server_port)):
            text = text.replace(original, f"127.0.0.1:{chosen}")
        path = tmp_path / "gateway.cfg"
        path.write_text(text.replace("inter 4s", "inter 150ms"))
        with (tmp_path / "haproxy.log").open("wb") as log:
            proc = subprocess.Popen([binary, "-db", "-f", str(path)], stdout=log, stderr=log)
        stack.callback(proc.wait, 5)
        stack.callback(proc.terminate)

        def reach(host, post=False, tls=client_tls):
            try:
                with closing(http.client.HTTPSConnection(host, timeout=2, context=tls)) as conn:
                    with socket.create_connection(("127.0.0.1", port), timeout=2) as sock:
                        conn.sock = tls.wrap_socket(sock, server_hostname=host)
                        conn.auto_open = False
                        conn.request("POST" if post else "GET", "/", headers={"Host": host},
                                     body=b"deliver once" if post else None)
                        return json.loads(conn.getresponse().read())
            except (OSError, http.client.HTTPException):
                return None

        def wait(predicate):
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                assert proc.poll() is None, "gateway exited"
                if predicate():
                    return
                time.sleep(.03)
            pytest.fail("gateway route did not converge")

        wait(lambda: reach("other.test") == "haos")
        assert reach("became.bezrabotnyi.com") == "server100"
        assert reach(names[0]) is None
        backup.discovery_status = 200
        wait(lambda: reach(names[0]) == "server100")
        assert reach(names[0], post=True) is None
        assert not primary.received and backup.received == [b"deliver once"]
        # Let this one test client accept the trusted leaf with a mismatched name.
        # A closed socket then proves the gateway itself rejected this alias.
        chain_only = ssl.create_default_context(cafile=cert)
        chain_only.check_hostname = False
        assert reach(gateway.HUB_NAMES[2], tls=chain_only) is None
        backup.discovery_status = 503
        wait(lambda: reach(names[0]) is None)
        assert reach("other.test") == "haos"
