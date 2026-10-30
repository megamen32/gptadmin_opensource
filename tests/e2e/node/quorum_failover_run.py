#!/usr/bin/env python3
"""Isolated real-process quorum probe; this is not production acceptance.

Usage: quorum_failover_run.py NODE_BINARY PRIVATE_TMP [--immediate-auth]
The optional scenario confirms client registrations, profiles, refresh rotation
and revocation survive a leader stopping immediately after acknowledgment.
"""
import base64
import datetime as dt
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID

BINARY = Path(sys.argv[1]).resolve()
BASE = Path(sys.argv[2]).resolve()
BASE.mkdir(parents=True, exist_ok=True)
ROOT = Path(tempfile.mkdtemp(prefix="real-quorum-", dir=BASE))
os.chmod(ROOT, 0o700)
NOW = dt.datetime.now(dt.timezone.utc)
password = secrets.token_urlsafe(32)
signer = secrets.token_urlsafe(32)
issuer = "https://quorum-test.invalid"
resource = issuer + "/mcp"
nodes = []
proxies = []
used_ports = set()


def private(path, data):
    path.write_bytes(data)
    path.chmod(0o600)


def port():
    while True:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            chosen = sock.getsockname()[1]
        if chosen not in used_ports:
            used_ports.add(chosen)
            return chosen


def cert(name, ca_key, ca):
    key = ec.generate_private_key(ec.SECP256R1())
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, name)])
    certificate = (x509.CertificateBuilder().subject_name(subject).issuer_name(ca.subject)
        .public_key(key.public_key()).serial_number(x509.random_serial_number())
        .not_valid_before(NOW - dt.timedelta(minutes=2)).not_valid_after(NOW + dt.timedelta(hours=2))
        .add_extension(x509.SubjectAlternativeName([x509.DNSName(name), x509.DNSName("localhost")]), critical=False)
        .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH, ExtendedKeyUsageOID.CLIENT_AUTH]), critical=False)
        .sign(ca_key, hashes.SHA256()))
    crt, pem = ROOT / (name + ".crt"), ROOT / (name + ".key")
    private(crt, certificate.public_bytes(serialization.Encoding.PEM))
    private(pem, key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    return crt, pem


def request(index, path, body=None, token=None, form=None, basic=None):
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    data = None if body is None else json.dumps(body).encode()
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    if token:
        headers["Authorization"] = "Bearer " + token
    if basic:
        headers["Authorization"] = "Basic " + base64.b64encode((':'.join(basic)).encode()).decode()
    connection = http.client.HTTPConnection("127.0.0.1", nodes[index]["http"], timeout=45)
    connection.request("POST" if data is not None else "GET", path, body=data, headers=headers)
    response = connection.getresponse()
    raw = response.read()
    headers = dict(response.getheaders())
    try:
        value = json.loads(raw) if raw else None
    except json.JSONDecodeError:
        value = None
    connection.close()
    return response.status, value, headers


def start(i):
    node = nodes[i]
    node["log"] = (node["dir"] / "runtime.log").open("ab")
    node["proc"] = subprocess.Popen([str(BINARY)], cwd=node["dir"], env=node["env"], stdout=node["log"], stderr=node["log"])


def stop(i):
    node = nodes[i]
    process = node.get("proc")
    if process and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
    if node.get("log"):
        node["log"].close()
        node["log"] = None


def wait_for(label, check, seconds=45):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        for node in nodes:
            process = node.get("proc")
            if process and node.get("log") and process.poll() is not None:
                raise RuntimeError(f"{node['dir'].name} exited with code {process.returncode}")
        try:
            last = check()
            if last:
                print(label, "ok", flush=True)
                return last
        except (OSError, ValueError, KeyError, http.client.HTTPException):
            pass
        time.sleep(.25)
    raise RuntimeError(label + " did not become ready")


def health(i):
    status, data, _ = request(i, "/healthz")
    return data if status == 200 else None


def rpc(i, token, method, params):
    return request(i, "/mcp", {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, token)


def start_tls_ingress(i):
    node = nodes[i]
    class Proxy(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def forward(self):
            try:
                size = int(self.headers.get("Content-Length", "0"))
                data = self.rfile.read(size) if size else None
                headers = {k: v for k, v in self.headers.items() if k.lower() not in {"connection", "host", "transfer-encoding"}}
                headers["Host"] = f"localhost:{node['public']}"
                conn = http.client.HTTPConnection("127.0.0.1", node["http"], timeout=8)
                conn.request(self.command, self.path, body=data, headers=headers)
                response = conn.getresponse()
                body = response.read()
                self.send_response(response.status)
                for key, value in response.getheaders():
                    if key.lower() not in {"connection", "transfer-encoding", "content-length"}:
                        self.send_header(key, value)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                conn.close()
            except (OSError, http.client.HTTPException):
                self.send_error(502)

        do_GET = forward
        do_POST = forward
    server = http.server.ThreadingHTTPServer(("127.0.0.1", node["public"]), Proxy)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(node["cert"], node["cert_key"])
    server.socket = context.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    proxies.append(server)


def main():
    ca_key = ec.generate_private_key(ec.SECP256R1())
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "GPTAdmin isolated quorum CA")])
    ca = (x509.CertificateBuilder().subject_name(subject).issuer_name(subject).public_key(ca_key.public_key())
        .serial_number(x509.random_serial_number()).not_valid_before(NOW - dt.timedelta(minutes=2))
        .not_valid_after(NOW + dt.timedelta(hours=2)).add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .add_extension(x509.KeyUsage(False, False, False, False, False, True, True, False, False), critical=True)
        .sign(ca_key, hashes.SHA256()))
    ca_path = ROOT / "ca.pem"
    private(ca_path, ca.public_bytes(serialization.Encoding.PEM))
    state_key = ROOT / "state.key"
    private(state_key, secrets.token_bytes(32))
    for i in range(3):
        directory = ROOT / f"node{i}"
        directory.mkdir(mode=0o700)
        crt, key = cert(f"node{i}.test", ca_key, ca)
        env = {k: v for k, v in os.environ.items() if not (k.startswith("GPTADMIN_") or k.startswith("SHELL") or k in {"CTL_TOKEN", "OAUTH_CLIENT_SECRET", "ADMIN_PASSWORD", "PUBLIC_ORIGIN", "MCP_RESOURCE", "MCP_RELAY_AGENT_TOKEN"})}
        env.update(GPTADMIN_CONFIG_DIR=str(directory), GPTADMIN_ROOT=str(directory), GPTADMIN_ENV_FILE=str(directory / "missing.env"),
            GPTADMIN_HUB_HOST="127.0.0.1", GPTADMIN_HUB_PORT=str(port()), CTL_TOKEN=secrets.token_urlsafe(32),
            SHELL_TOKEN=secrets.token_urlsafe(32), OAUTH_CLIENT_SECRET=signer, ADMIN_PASSWORD=password,
            MCP_RELAY_AGENT_TOKEN=secrets.token_urlsafe(32), PUBLIC_ORIGIN=issuer, MCP_RESOURCE=resource,
            GPTADMIN_AUTH_MODE="writer" if i == 0 else "reader", SHELL_NAME=f"node{i}",
            SHELL_IDENTITY_DIR=str(directory), SHELL_SPOOL_DIR=str(directory / "spool"),
            SHELL_OUTBOX_DIR=str(directory / "spool/outbox"), SHELL_DEFAULT_CWD=str(directory),
            SHELL_DEFAULT_HOME=str(directory), SHELLMCP_MCP_CONFIG=str(directory / "no-child.json"),
            HUB_URL="http://127.0.0.1:1", SHELLMCP_SELF_REPAIR_DISABLE="1", NO_PROXY="*", SSL_CERT_FILE=str(ca_path))
        nodes.append({"dir": directory, "http": int(env["GPTADMIN_HUB_PORT"]), "raft": port(), "private": port(), "public": port(),
            "key": secrets.token_hex(32), "cert": crt, "cert_key": key, "env": env})
    start(0)
    wait_for("ordinary writer", lambda: health(0))
    writer_id = json.loads((nodes[0]["dir"] / "shellmcp_identity.json").read_text())["server_id"]
    nodes[0]["id"] = writer_id
    callback = "http://127.0.0.1:49173/callback/quorum"
    status, registration, _ = request(0, "/register", {"redirect_uris": [callback], "client_name": "quorum-probe"})
    assert status == 201, f"register HTTP {status}"
    basic = (registration["client_id"], registration["client_secret"])
    verifier = secrets.token_urlsafe(48)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
    status, _, headers = request(0, "/oauth/authorize", form={"client_id": basic[0], "redirect_uri": callback,
        "resource": resource, "scope": "gptadmin.read gptadmin.exec offline_access", "password": password,
        "code_challenge": challenge, "code_challenge_method": "S256"})
    assert status == 302, f"authorize HTTP {status}"
    code = urllib.parse.parse_qs(urllib.parse.urlparse(headers["Location"]).query)["code"][0]
    status, oauth, _ = request(0, "/oauth/token", form={"grant_type": "authorization_code", "code": code,
        "redirect_uri": callback, "resource": resource, "code_verifier": verifier}, basic=basic)
    assert status == 200 and oauth.get("refresh_token"), f"OAuth exchange HTTP {status}"
    for i in (1, 2):
        node = nodes[i]
        node["id"] = secrets.token_hex(16)
        name = f"shell:node{i}"
        status, grant, _ = request(0, "/admin/api/nodes/grant", {"name": name, "ttl_minutes": 30}, nodes[0]["env"]["CTL_TOKEN"])
        assert status == 200, f"grant HTTP {status}"
        status, join, _ = request(0, "/admin/api/nodes/join", {"node_id": node["id"], "node_key": node["key"],
            "name": name, "endpoints": [f"http://127.0.0.1:{node['http']}"], "capabilities": ["ingress", "executor", "replica"]}, grant["grant"])
        assert status == 200, f"join HTTP {status}"
        identity = {"schema": "gptadmin.node-identity/v1", "node_id": node["id"], "node_key": node["key"],
            "created": NOW.isoformat(), "writer_url": join["writer_url"], "writer_id": writer_id}
        private(node["dir"] / "node-identity.json", json.dumps(identity).encode())
        node["env"]["GPTADMIN_AUTH_SOURCE_ID"] = writer_id
        start(i)
        wait_for(f"reader {i} health", lambda i=i: health(i))
    def members_ready():
        status, data, _ = request(0, "/admin/api/nodes", token=nodes[0]["env"]["CTL_TOKEN"])
        return status == 200 and all(any(n.get("node_id") == nodes[i]["id"] and n.get("state") == "ready" for n in data.get("nodes", [])) for i in (1, 2))
    wait_for("readers enrolled and synced", members_ready, 90)
    for i in range(3):
        stop(i)
        start_tls_ingress(i)
    peers = [{"id": n["id"], "address": f"127.0.0.1:{n['raft']}", "server_name": f"node{i}.test",
        "hub_url": f"https://localhost:{n['public']}", "internal_hub_url": f"https://127.0.0.1:{n['private']}",
        "node_key_sha256": hashlib.sha256(n["key"].encode()).hexdigest(), "failure_domain": f"host{i}"} for i, n in enumerate(nodes)]
    for i, n in enumerate(nodes):
        config = {"cluster_id": "isolated-probe", "node_id": n["id"], "bootstrapper_id": writer_id,
            "node_key": n["key"], "bind_address": peers[i]["address"], "advertise": peers[i]["address"],
            "data_dir": str(n["dir"] / "raft"), "tls_cert_file": str(n["cert"]), "tls_key_file": str(n["cert_key"]),
            "tls_ca_file": str(ca_path), "state_key_file": str(state_key), "external_snapshot_max_bytes": 67108864,
            "external_snapshot_free_reserve_bytes": 1048576, "peers": peers, "bootstrap": i == 0}
        config_path = n["dir"] / "quorum.json"
        private(config_path, json.dumps(config).encode())
        n["env"]["GPTADMIN_QUORUM_CONFIG_FILE"] = str(config_path)
        n["env"]["GPTADMIN_NODE_PUBLIC_URL"] = peers[i]["hub_url"]
    start(0)
    def bootstrap_ready():
        status, data, _ = request(0, "/healthz")
        return status == 503 and data and data.get("replica_ready") and data.get("voter_count") == 1
    wait_for("bootstrap authority without other voters", bootstrap_ready, 60)
    invalid_status, _, _ = request(0, "/admin/api/nodes/heartbeat", {"state": "ready", "snapshot_revision": 1}, "invalid-member-key")
    assert invalid_status in (401, 403, 503), f"unregistered heartbeat accepted HTTP {invalid_status}"
    registration_status, _, _ = request(0, "/register", {"redirect_uris": [callback]})
    assert registration_status == 503, f"client registration bypassed the voter gate HTTP {registration_status}"
    print("bootstrap rejects unregistered heartbeat and client registration", flush=True)
    for i in (1, 2):
        start(i)
    wait_for("three-voter leader ready", lambda: health(0), 90)
    # These are synthetic executor identities generated by this isolated run.
    # The production fleet already uses explicit approval for its executors.
    for i in (1, 2):
        target = f"shell:node{i}"
        def pending():
            status, response, _ = request(0, "/servers", token=nodes[0]["env"]["CTL_TOKEN"])
            return status == 200 and any(x.get("server_id") == target and x.get("status") == "awaiting_approval" for x in response.get("servers", []))
        wait_for(f"synthetic {target} pending", pending, 60)
        approval = {"target": "hub", "tool": "approve_pending_server", "args": {"server_id": target}}
        status, response, _ = request(0, "/mcp-relay/call", approval, nodes[0]["env"]["CTL_TOKEN"])
        if status == 428 and response and response.get("executed") is False and response.get("context"):
            approval["context"] = response["context"]
            status, response, _ = request(0, "/mcp-relay/call", approval, nodes[0]["env"]["CTL_TOKEN"])
        assert status == 200 and response and not response.get("error"), f"approval failed for {target}: HTTP {status}, error {response.get('error') if response else None}"
    init = {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "quorum-probe", "version": "1"}}
    status, response, _ = rpc(0, oauth["access_token"], "initialize", init)
    assert status == 200 and response and "result" in response, f"initial OAuth initialize HTTP {status}"
    print("OAuth client before leader loss ok", flush=True)
    status, found, _ = rpc(0, oauth["access_token"], "tools/call", {"name": "search", "arguments": {"query": "shell_exec", "target": "shell:node0", "limit": 5}})
    assert status == 200 and found and "result" in found, "initial command discovery failed"
    search = found["result"].get("structuredContent", {})
    before_args = {"code": "return await call('shell:node0', 'shell_exec', {cmd:\"printf 'once\\n' >> once.txt; printf quorum-before\"});"}
    if search.get("context"):
        before_args["context"] = search["context"]
    status, before, _ = rpc(0, oauth["access_token"], "tools/call", {"name": "exec", "arguments": before_args})
    assert status == 200 and before and "result" in before, "initial command request failed"
    before_detail = before["result"].get("structuredContent", {})
    if before_detail.get("status") == "failed" and before_detail.get("executed") is False and before_detail.get("context"):
        before_args["context"] = before_detail["context"]
        status, before, _ = rpc(0, oauth["access_token"], "tools/call", {"name": "exec", "arguments": before_args})
        assert status == 200 and before and "result" in before, "initial unexecuted command retry failed"
        before_detail = before["result"].get("structuredContent", {})
    before_job = before_detail.get("error", {}).get("receipt", {}).get("job_id")
    if before_detail.get("status") != "completed" and before_job:
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            status, response, _ = rpc(0, oauth["access_token"], "tools/call", {"name": "job", "arguments": {"id": before_job, "owner_target": "shell:node0"}})
            if status == 200 and response and "result" in response:
                item = response["result"].get("structuredContent", {})
                if item.get("status") == "completed":
                    before_detail = item
                    break
                if item.get("status") in ("failed", "cancelled"):
                    raise RuntimeError("the initial command ended with " + item["status"])
            time.sleep(1)
    assert before_detail.get("status") == "completed" and "quorum-before" in json.dumps(before_detail), "initial command was not confirmed"
    database = sqlite3.connect("file:" + str(nodes[0]["dir"] / "tasks_state.sqlite") + "?mode=ro", uri=True)
    before_row = database.execute("SELECT key FROM task_records WHERE kind='shell' AND json_extract(payload,'$.tool_name')='shell_exec' ORDER BY json_extract(payload,'$.created_at') DESC LIMIT 1").fetchone()
    database.close()
    assert before_row and (nodes[0]["dir"] / "once.txt").read_text().splitlines() == ["once"], "initial command receipt or side effect missing"
    stop(0)
    leader = wait_for("surviving leader ready", lambda: next((i for i in (1, 2) if health(i)), None), 90)
    if "--immediate-auth" in sys.argv[3:]:
        start(0)
        def old_follower_ready():
            status, data, _ = request(0, "/healthz")
            return status == 503 and data and data.get("role") == "follower" and data.get("leader_id")
        wait_for("old node rejoins before refresh", old_follower_ready, 90)
        # A client and an access policy created on the successor must survive its loss.
        status, registration2, _ = request(leader, "/register", {"redirect_uris":[callback], "client_name":"second-leader-client"})
        assert status == 201, f"registration on second leader HTTP {status}"
        basic2 = (registration2["client_id"], registration2["client_secret"])
        connection = http.client.HTTPConnection("127.0.0.1", nodes[leader]["http"], timeout=45)
        profile = {"id":"replicated-policy", "access_mode":"full", "approval_mode":"ask_before_write", "allowed_targets":["hub"], "allowed_tools":["discover"]}
        connection.request("PUT", "/admin/api/access-profiles/replicated-policy", body=json.dumps(profile).encode(), headers={"Authorization":"Bearer "+nodes[leader]["env"]["CTL_TOKEN"], "Content-Type":"application/json", "If-Match":"*"})
        profile_response = connection.getresponse(); profile_status=profile_response.status; profile_response.read(); connection.close()
        assert profile_status == 200, f"profile create on second leader HTTP {profile_status}"
        print("client and policy created on successor", flush=True)
        status, response, _ = rpc(leader, oauth["access_token"], "initialize", init)
        assert status == 200 and response and "result" in response, f"surviving OAuth initialize HTTP {status}"
        status, rotated, _ = request(leader, "/oauth/token", form={"grant_type": "refresh_token", "refresh_token": oauth["refresh_token"], "resource": resource}, basic=basic)
        assert status == 200 and rotated.get("refresh_token"), f"surviving OAuth refresh HTTP {status}"
        print("OAuth after leader loss and refresh ok", flush=True)
        stop(leader)
        second = wait_for("immediate second leader", lambda: next((i + 1 for i in range(3) if i != leader and health(i)), None), 90) - 1
        status, final, _ = request(second, "/oauth/token", form={"grant_type":"refresh_token", "refresh_token":rotated["refresh_token"], "resource":resource}, basic=basic)
        assert status == 200 and final.get("refresh_token"), f"new refresh lost immediately after acknowledgment HTTP {status}"
        status, _, _ = request(second, "/oauth/token", form={"grant_type":"refresh_token", "refresh_token":oauth["refresh_token"], "resource":resource}, basic=basic)
        assert status == 400, f"old revoked refresh restored HTTP {status}"
        print("immediate second failover preserves new refresh and old revocation", flush=True)
        status, restored_profile, _ = request(second, "/admin/api/access-profiles/replicated-policy", token=nodes[second]["env"]["CTL_TOKEN"])
        assert status == 200 and restored_profile.get("id") == "replicated-policy", f"access policy lost HTTP {status}"
        verifier2=secrets.token_urlsafe(48)
        challenge2=base64.urlsafe_b64encode(hashlib.sha256(verifier2.encode()).digest()).decode().rstrip("=")
        status, _, headers = request(second, "/oauth/authorize", form={"client_id":basic2[0], "redirect_uri":callback, "resource":resource, "scope":"gptadmin.read gptadmin.exec offline_access", "password":password, "code_challenge":challenge2, "code_challenge_method":"S256"})
        assert status == 302, f"replicated client authorization HTTP {status}"
        code2=urllib.parse.parse_qs(urllib.parse.urlparse(headers["Location"]).query)["code"][0]
        status, issued2, _ = request(second, "/oauth/token", form={"grant_type":"authorization_code", "code":code2, "redirect_uri":callback, "resource":resource, "code_verifier":verifier2}, basic=basic2)
        assert status == 200 and issued2.get("refresh_token"), f"replicated client exchange HTTP {status}"
        print("client registration and access policy survive two leader changes", flush=True)
        return
    status, response, _ = rpc(leader, oauth["access_token"], "initialize", init)
    assert status == 200 and response and "result" in response, f"surviving OAuth initialize HTTP {status}"
    status, rotated, _ = request(leader, "/oauth/token", form={"grant_type": "refresh_token", "refresh_token": oauth["refresh_token"], "resource": resource}, basic=basic)
    assert status == 200 and rotated.get("refresh_token"), f"surviving OAuth refresh HTTP {status}"
    print("OAuth after leader loss and refresh ok", flush=True)
    token = rotated["access_token"]
    status, old_receipt, _ = rpc(leader, token, "tools/call", {"name": "job", "arguments": {"id": before_row[0], "owner_target": "shell:node0"}})
    assert status == 200 and old_receipt and "quorum-before" in json.dumps(old_receipt), "previous owner's receipt lost after leader failure"
    assert (nodes[0]["dir"] / "once.txt").read_text().splitlines() == ["once"], "initial action was repeated"
    print("old receipt survives leader loss without replay", flush=True)
    target = f"shell:node{leader}"
    status, found, _ = rpc(leader, token, "tools/call", {"name": "search", "arguments": {"query": "shell_exec", "target": target, "limit": 5}})
    assert status == 200 and found and "result" in found, f"tool discovery HTTP {status}"
    search = found["result"].get("structuredContent", {})
    args = {"code": "return await call(" + json.dumps(target) + ", 'shell_exec', {cmd:'printf quorum-probe-result'});"}
    if search.get("context"):
        args["context"] = search["context"]
    status, executed, _ = rpc(leader, token, "tools/call", {"name": "exec", "arguments": args})
    assert status == 200 and executed and "result" in executed, f"command call HTTP {status}"
    detail = executed["result"].get("structuredContent", {})
    if detail.get("status") == "failed" and detail.get("executed") is False and detail.get("context"):
        args["context"] = detail["context"]
        status, executed, _ = rpc(leader, token, "tools/call", {"name": "exec", "arguments": args})
        assert status == 200 and executed and "result" in executed, f"unexecuted command retry HTTP {status}"
        detail = executed["result"].get("structuredContent", {})
    print("command result status", detail.get("status"), "keys", sorted(detail), flush=True)
    if detail.get("status") != "completed":
        print("command error", str(detail.get("error", ""))[:400], "executed", detail.get("executed"), flush=True)
    receipt = detail.get("error", {}).get("receipt", {})
    job_id = receipt.get("job_id")
    if detail.get("status") != "completed" and job_id:
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            status, response, _ = rpc(leader, token, "tools/call", {"name": "job", "arguments": {"id": job_id, "owner_target": target}})
            if status == 200 and response and "result" in response:
                item = response["result"].get("structuredContent", {})
                if item.get("status") == "completed":
                    detail = item
                    break
                if item.get("status") in ("failed", "cancelled"):
                    raise RuntimeError("the original command ended with " + item["status"])
            time.sleep(1)
    assert detail.get("status") == "completed" and "quorum-probe-result" in json.dumps(detail), "surviving command not verified"
    print("command payload", json.dumps(detail.get("result"), ensure_ascii=False)[:1200], flush=True)
    database = sqlite3.connect("file:" + str(nodes[leader]["dir"] / "tasks_state.sqlite") + "?mode=ro", uri=True)
    row = database.execute("SELECT key FROM task_records WHERE kind='shell' AND json_extract(payload,'$.tool_name')='shell_exec' ORDER BY json_extract(payload,'$.created_at') DESC LIMIT 1").fetchone()
    database.close()
    assert row, "command receipt not persisted"
    status, response, _ = rpc(leader, token, "tools/call", {"name": "job", "arguments": {"id": row[0], "owner_target": target}})
    assert status == 200 and response and "quorum-probe-result" in json.dumps(response), "independent job receipt read failed"
    print("real shell command and independent receipt after leader loss ok", flush=True)
    start(0)
    def original_rejoined():
        status, data, _ = request(0, "/healthz")
        return status == 503 and data and data.get("role") == "follower" and data.get("leader_id")
    wait_for("original node rejoins as follower", original_rejoined, 60)
    status, response, _ = rpc(0, token, "initialize", init)
    assert status == 200 and response and "result" in response, "client through returned node failed"
    status, response, _ = rpc(0, token, "tools/call", {"name": "job", "arguments": {"id": before_row[0], "owner_target": "shell:node0"}})
    assert status == 200 and response and "quorum-before" in json.dumps(response), "old receipt unavailable through rejoined node"
    assert (nodes[0]["dir"] / "once.txt").read_text().splitlines() == ["once"], "initial action replayed on rejoin"
    print("rejoined node forwards client and original receipt without replay", flush=True)
    remote_target = "shell:node0"
    remote_args = {"code": "return await call('shell:node0', 'shell_exec', {cmd:'printf quorum-remote-result'});", "context": args.get("context", "")}
    status, response, _ = rpc(leader, token, "tools/call", {"name": "exec", "arguments": remote_args})
    assert status == 200 and response and "result" in response, "remote executor call failed"
    remote = response["result"].get("structuredContent", {})
    if remote.get("status") == "failed" and remote.get("executed") is False and remote.get("context"):
        remote_args["context"] = remote["context"]
        status, response, _ = rpc(leader, token, "tools/call", {"name": "exec", "arguments": remote_args})
        assert status == 200 and response and "result" in response, "unexecuted remote command retry failed"
        remote = response["result"].get("structuredContent", {})
    remote_job = remote.get("error", {}).get("receipt", {}).get("job_id")
    if remote.get("status") != "completed" and remote_job:
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            status, response, _ = rpc(leader, token, "tools/call", {"name": "job", "arguments": {"id": remote_job, "owner_target": remote_target}})
            item = response.get("result", {}).get("structuredContent", {}) if response else {}
            if status == 200 and item.get("status") == "completed":
                remote = item
                break
            if item.get("status") in ("failed", "cancelled"):
                raise RuntimeError("remote command ended with " + item["status"])
            time.sleep(1)
    assert remote.get("status") == "completed" and "quorum-remote-result" in json.dumps(remote), "remote executor result not confirmed"
    print("rejoined follower executes a new command through the leader", flush=True)


try:
    main()
except Exception as exc:
    print("probe failed:", type(exc).__name__, str(exc), "evidence:", ROOT, file=sys.stderr, flush=True)
    sys.exit(1)
finally:
    for index in range(len(nodes)):
        stop(index)
    for proxy in proxies:
        proxy.shutdown()
        proxy.server_close()
