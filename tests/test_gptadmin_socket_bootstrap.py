import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import signal
import socket
import subprocess
import sys
import threading
import time
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/gptadmin_socket_bootstrap.py"
spec = importlib.util.spec_from_file_location("gptadmin_socket_bootstrap", SCRIPT)
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Writer:
    def __init__(self, port, label):
        self.port = port
        self.label = label
        self.stop = threading.Event()
        self.sock = None
        self.thread = None
        self.connections = 0

    def start(self):
        self.sock = socket.socket()
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind(("127.0.0.1", self.port))
        self.sock.listen(64)
        self.sock.settimeout(0.1)
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def run(self):
        while not self.stop.is_set():
            try:
                conn, _ = self.sock.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            self.connections += 1
            threading.Thread(target=self.handle, args=(conn,), daemon=True).start()

    def handle(self, conn):
        with conn:
            conn.settimeout(2)
            data = conn.recv(65536)
            if data.startswith(b"WS "):
                conn.sendall(data)
                while True:
                    block = conn.recv(65536)
                    if not block:
                        return
                    conn.sendall(block)
            body = (self.label + "\n").encode()
            conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: " + str(len(body)).encode()
                         + b"\r\nConnection: close\r\n\r\n" + body)

    def close(self):
        self.stop.set()
        if self.sock:
            self.sock.close()
        if self.thread:
            self.thread.join(2)


def control(path, command, timeout=2):
    deadline = time.monotonic() + timeout
    while True:
        try:
            with socket.socket(socket.AF_UNIX) as sock:
                sock.settimeout(timeout)
                sock.connect(str(path))
                sock.sendall(command.encode())
                return json.loads(sock.recv(65536))
        except (FileNotFoundError, ConnectionRefusedError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.02)


def http_request(port, method="GET", path="/healthz", timeout=5):
    with socket.create_connection(("127.0.0.1", port), timeout=timeout) as sock:
        sock.settimeout(timeout)
        body = b"{}" if method == "POST" else b""
        request = (f"{method} {path} HTTP/1.1\r\nHost: test\r\n"
                   f"Content-Length: {len(body)}\r\nConnection: close\r\n\r\n").encode() + body
        sock.sendall(request)
        chunks = []
        while True:
            block = sock.recv(65536)
            if not block:
                return b"".join(chunks)
            chunks.append(block)


def start_bridge(tmp_path, listen, upstream, **overrides):
    control_path = tmp_path / "control.sock"
    command = [sys.executable, str(SCRIPT), "bridge-serve",
               "--listen", listen, "--upstream", upstream,
               "--control", str(control_path),
               "--deadline", str(overrides.get("deadline", 120)),
               "--max-connections", str(overrides.get("max_connections", 64)),
               "--max-buffer", str(overrides.get("max_buffer", 1048576)),
               "--per-connection-buffer", str(overrides.get("per_connection_buffer", 262144))]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    status = control(control_path, "status")
    return process, control_path, status


def test_bridge_old_hold_new_preserves_get_post_and_duplex(tmp_path):
    writer_port, bridge_port = free_port(), free_port()
    old = Writer(writer_port, "old")
    old.start()
    process, control_path, status = start_bridge(
        tmp_path, f"127.0.0.1:{bridge_port}", f"127.0.0.1:{writer_port}")
    try:
        assert status["mode"] == "forward"
        assert b"old" in http_request(bridge_port)
        duplex = socket.create_connection(("127.0.0.1", bridge_port), timeout=2)
        duplex.sendall(b"WS hello")
        assert duplex.recv(64) == b"WS hello"
        held = control(control_path, "hold")
        assert held["mode"] == "hold"
        pending = []
        threads = [threading.Thread(target=lambda m=method: pending.append(
            http_request(bridge_port, m, "/mcp" if m == "POST" else "/version")), daemon=True)
            for method in (["GET", "POST"] * 8)]
        for thread in threads:
            thread.start()
        duplex.sendall(b" more")
        assert duplex.recv(64) == b" more"
        duplex.shutdown(socket.SHUT_WR)
        duplex.close()
        deadline = time.monotonic() + 3
        state = {}
        while time.monotonic() < deadline:
            state = control(control_path, "status")
            if state["held"] >= len(threads) and state["dialing"] == state["forwarding"] == 0:
                break
            time.sleep(0.02)
        assert state["held"] >= len(threads)
        old_connections = old.connections
        old.close()
        new = Writer(writer_port, "new")
        new.start()
        assert control(control_path, f"upstream 127.0.0.1:{writer_port}")["mode"] == "hold"
        released = control(control_path, "release")
        assert released["mode"] == "forward"
        for thread in threads:
            thread.join(5)
        assert len(pending) == len(threads)
        assert all(b" 200 " in item and b"new" in item for item in pending)
        assert old.connections == old_connections
        assert b"new" in http_request(bridge_port)
        final = control(control_path, "status")
        assert final["errors"] == final["rejected"] == 0
        assert final["buffered"] == 0
        new.close()
    finally:
        process.send_signal(signal.SIGTERM)
        assert process.wait(timeout=5) == 0


def test_hold_is_generation_barrier_for_inflight_dial(tmp_path, monkeypatch):
    bridge = bootstrap.Bridge("127.0.0.1:1", "127.0.0.1:2", str(tmp_path / "c"),
                              1024, 1, 2, 512)
    entered = threading.Event()
    release = threading.Event()

    class Peer:
        def close(self):
            pass

    def slow_dial(_upstream):
        entered.set()
        release.wait(2)
        return Peer()

    monkeypatch.setattr(bridge, "_dial", slow_dial)
    client_a, client_b = socket.socketpair()
    worker = threading.Thread(target=bridge._connection, args=(client_a,))
    assert bridge.slots.acquire(blocking=False)
    worker.start()
    assert entered.wait(1)
    with bridge.lock:
        bridge.mode = "hold"
        bridge.generation += 1
    release.set()
    deadline = time.monotonic() + 1
    while time.monotonic() < deadline:
        with bridge.lock:
            if bridge.held == 1:
                break
        time.sleep(0.02)
    with bridge.lock:
        assert bridge.forwarding == 0
        assert bridge.held == 1
    bridge.close()
    client_b.close()
    worker.join(2)


def test_connection_limit_rejects_without_unbounded_workers(tmp_path):
    writer_port, bridge_port = free_port(), free_port()
    old = Writer(writer_port, "old")
    old.start()
    process, control_path, _ = start_bridge(
        tmp_path, f"127.0.0.1:{bridge_port}", f"127.0.0.1:{writer_port}", max_connections=1)
    first = socket.create_connection(("127.0.0.1", bridge_port), timeout=2)
    first.sendall(b"WS first")
    assert first.recv(64) == b"WS first"
    second = socket.create_connection(("127.0.0.1", bridge_port), timeout=2)
    second.settimeout(2)
    assert second.recv(1) == b""
    state = control(control_path, "status")
    assert state["rejected"] >= 1
    first.close()
    second.close()
    old.close()
    process.terminate()
    assert process.wait(timeout=5) == 0


def test_nginx_render_is_exact_and_rejects_drift():
    original = b"""upstream gptadmin_hub_active {
    zone gptadmin_hub_active 64k;
    server 127.0.0.1:9001 max_fails=1 fail_timeout=1s;
    server 127.0.0.1:19001 backup;
    keepalive 64;
}
"""
    rendered = bootstrap.render_bridge_nginx(original).decode()
    assert "127.0.0.1:19002" in rendered
    assert "127.0.0.1:9001" not in rendered
    assert "127.0.0.1:19001" not in rendered
    with pytest.raises(bootstrap.BootstrapFailure):
        bootstrap.render_bridge_nginx(original.replace(b"19001", b"19009"))


def test_parser_requires_explicit_migration_identity():
    parser = bootstrap.build_parser()
    with pytest.raises(SystemExit):
        parser.parse_args(["migrate"])


def test_global_buffer_budget_is_never_exceeded_under_hold(tmp_path):
    writer_port, bridge_port = free_port(), free_port()
    old = Writer(writer_port, "old")
    old.start()
    process, control_path, _ = start_bridge(
        tmp_path, f"127.0.0.1:{bridge_port}", f"127.0.0.1:{writer_port}",
        max_connections=32, max_buffer=4096, per_connection_buffer=2048)
    clients = []
    try:
        assert control(control_path, "hold")["mode"] == "hold"
        for _ in range(24):
            client = socket.create_connection(("127.0.0.1", bridge_port), timeout=2)
            client.sendall(b"x" * 8192)
            clients.append(client)
        deadline = time.monotonic() + 2
        maximum = 0
        while time.monotonic() < deadline:
            state = control(control_path, "status")
            maximum = max(maximum, state["buffered"])
            assert state["buffered"] <= 4096
            if state["held"] >= 20:
                break
            time.sleep(0.02)
        assert maximum > 0
    finally:
        for client in clients:
            client.close()
        old.close()
        process.terminate()
        assert process.wait(timeout=5) == 0


def test_socket_unit_contract_has_one_listener_and_explicit_green_target():
    bootstrap_unit = bootstrap.bootstrap_primary_socket_text()
    assert bootstrap_unit.count("ListenStream=127.0.0.1:9001") == 1
    assert f"Service={bootstrap.BOOTSTRAP_GREEN_SERVICE}" in bootstrap_unit
    assert "ReusePort" not in bootstrap_unit
    green = bootstrap.green_service_text(Path("/opt/gptadmin-node/versions/candidate-1"))
    assert f"Requires={bootstrap.PRIMARY_SOCKET}" in green
    assert f"Sockets={bootstrap.PRIMARY_SOCKET}" in green
    assert "GPTADMIN_HUB_PORT=9001" in green
    assert "Restart=no" in green


def test_cmd_migrate_holds_shared_target_lock_through_controller_completion(monkeypatch, tmp_path, capsys):
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", tmp_path / "leases.json")
    monkeypatch.setattr(bootstrap, "VERSIONS_ROOT", tmp_path / "opt" / "versions")
    args = argparse.Namespace(release_lease_target_unit=bootstrap.PRIMARY_SERVICE,
                              candidate_commit="a" * 40, operation_id="operation-123456")

    class FakeController:
        def __init__(self, _args):
            pass

        def migrate(self):
            with pytest.raises(bootstrap.BootstrapFailure, match="deployment target"):
                bootstrap.deployment_target_lock(bootstrap.PRIMARY_SERVICE)
            return {"phase": "committed"}

    monkeypatch.setattr(bootstrap, "BootstrapController", FakeController)
    assert bootstrap.cmd_migrate(args) == 0
    assert json.loads(capsys.readouterr().out)["phase"] == "committed"
    fd = bootstrap.deployment_target_lock(bootstrap.PRIMARY_SERVICE)
    os.close(fd)


def test_migrate_calls_rollback_on_intermediate_failure(monkeypatch, tmp_path):
    args = type("Args", (), {"operation_id": "operation-123456", "previous_dir": "old"})()
    monkeypatch.setattr(bootstrap, "control_token", lambda: "synthetic")
    controller = bootstrap.BootstrapController(args)
    events = []
    monkeypatch.setattr(controller, "preflight", lambda: events.append("preflight") or tmp_path / "pkg")
    monkeypatch.setattr(controller, "extract_candidate", lambda package: events.append("extract") or tmp_path / "ctl")
    monkeypatch.setattr(controller, "start_bridge", lambda ctl: events.append("bridge"))
    monkeypatch.setattr(controller, "route_public_to_bridge", lambda: events.append("nginx"))
    monkeypatch.setattr(controller, "switch_old_to_green", lambda: events.append("green"))
    def fail():
        events.append("canonical")
        raise bootstrap.BootstrapFailure("synthetic")
    monkeypatch.setattr(controller, "install_canonical", fail)
    monkeypatch.setattr(controller, "rollback", lambda reason: events.append("rollback:" + reason))
    with pytest.raises(bootstrap.BootstrapFailure):
        controller.migrate()
    assert events[:6] == ["preflight", "extract", "bridge", "nginx", "green", "canonical"]
    assert events[-1].startswith("rollback:BootstrapFailure")


def test_verify_extracted_tree_rejects_unmanifested_file(tmp_path):
    root = tmp_path / "candidate"
    (root / "bin").mkdir(parents=True)
    binary = root / "bin/gptadmin-node"
    binary.write_bytes(b"\x7fELFpayload")
    binary.chmod(0o755)
    commit = "a" * 40
    files = {"bin/gptadmin-node": {"size": binary.stat().st_size,
                                     "sha256": bootstrap.sha256_file(binary)}}
    (root / "manifest.json").write_text(json.dumps({
        "schema": "gptadmin.node-package/v1", "git_commit": commit,
        "source_dirty": False, "platform": "linux", "arch": bootstrap.host_arch(),
        "build_version": 1, "files": files}))
    assert bootstrap.verify_extracted_tree(root, commit)["git_commit"] == commit
    (root / "untracked").write_text("x")
    with pytest.raises(bootstrap.BootstrapFailure):
        bootstrap.verify_extracted_tree(root, commit)


def test_explicit_recovery_rolls_back_incomplete_receipt(monkeypatch, tmp_path, capsys):
    receipt = {"schema": bootstrap.BOOTSTRAP_RECEIPT_SCHEMA, "phase": "green-active",
               "release_lease_id": "lease-1"}
    class Recovery:
        def rollback(self, reason):
            with pytest.raises(bootstrap.BootstrapFailure, match="deployment target"):
                bootstrap.deployment_target_lock(bootstrap.PRIMARY_SERVICE)
            self.reason = reason

    fake = Recovery()
    monkeypatch.setattr(bootstrap.os, "geteuid", lambda: 0)
    monkeypatch.setattr(bootstrap, "VERSIONS_ROOT", tmp_path / "opt" / "versions")
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", tmp_path / "leases.json")
    monkeypatch.setattr(bootstrap, "read_receipt", lambda: receipt)
    monkeypatch.setattr(bootstrap, "recovery_controller", lambda value, lease: fake)
    final = {**receipt, "phase": "rolled-back"}
    calls = iter([receipt, final])
    monkeypatch.setattr(bootstrap, "read_receipt", lambda: next(calls))
    args = type("Args", (), {"release_lease_id": "lease-1",
                               "release_lease_target_unit": bootstrap.PRIMARY_SERVICE})()
    assert bootstrap.cmd_recover(args) == 0
    assert fake.reason == "explicit recovery rollback"
    assert json.loads(capsys.readouterr().out)["phase"] == "rolled-back"


def test_recovery_refuses_foreign_release_lease():
    owner = {"hostId": "admin-node-100", "harness": "opencode", "nativeSessionId": "ses-a"}
    receipt = {"release_lease": {
        "project": bootstrap.DEFAULT_RELEASE_PROJECT, "owner": owner, "fence": 7,
        "receipt_id": "lease-old", "deployment_target": {"unit": bootstrap.PRIMARY_SERVICE}}}
    foreign = dict(receipt["release_lease"], owner={**owner, "nativeSessionId": "ses-b"},
                   receipt_id="lease-new", fence=8)
    with pytest.raises(bootstrap.BootstrapFailure, match="does not match"):
        bootstrap.assert_lease_continuity(receipt, foreign)


def test_hold_status_cannot_report_zero_before_inflight_dial_resolves(tmp_path, monkeypatch):
    bridge = bootstrap.Bridge("127.0.0.1:1", "127.0.0.1:2", str(tmp_path / "c"),
                              4096, 2, 2, 2048)
    entered = threading.Event()
    release = threading.Event()
    closed = threading.Event()

    class Peer:
        def close(self):
            closed.set()

    def blocked_dial(_upstream):
        entered.set()
        assert release.wait(2)
        return Peer()

    monkeypatch.setattr(bridge, "_dial", blocked_dial)
    client_a, client_b = socket.socketpair()
    assert bridge.slots.acquire(blocking=False)
    worker = threading.Thread(target=bridge._connection, args=(client_a,))
    worker.start()
    assert entered.wait(1)
    with bridge.lock:
        bridge.mode = "hold"
        bridge.generation += 1
        # HOLD cannot claim the barrier while a pre-HOLD dial is unresolved.
        assert bridge.dialing == 1
        assert bridge.forwarding == 0
    release.set()
    deadline = time.monotonic() + 1
    while time.monotonic() < deadline:
        state = bridge._status()
        if state["dialing"] == state["forwarding"] == 0 and state["held"] == 1:
            break
        time.sleep(0.01)
    assert state["dialing"] == state["forwarding"] == 0
    assert state["held"] == 1
    assert closed.is_set()
    bridge.close()
    client_b.close()
    worker.join(2)


def test_half_closed_held_request_reaches_new_writer(tmp_path):
    writer_port, bridge_port = free_port(), free_port()
    old = Writer(writer_port, "old")
    old.start()
    process, control_path, _ = start_bridge(
        tmp_path, f"127.0.0.1:{bridge_port}", f"127.0.0.1:{writer_port}")
    client = None
    new = None
    try:
        assert control(control_path, "hold")["mode"] == "hold"
        client = socket.create_connection(("127.0.0.1", bridge_port), timeout=2)
        client.settimeout(5)
        request = (b"POST /mcp HTTP/1.1\r\nHost: test\r\nContent-Length: 2\r\n"
                   b"Connection: close\r\n\r\n{}")
        client.sendall(request)
        client.shutdown(socket.SHUT_WR)
        deadline = time.monotonic() + 2
        state = {}
        while time.monotonic() < deadline:
            state = control(control_path, "status")
            if state["held"] == 1 and state["buffered"] >= len(request):
                break
            time.sleep(0.01)
        assert state["held"] == 1
        assert state["buffered"] >= len(request)
        old.close()
        new = Writer(writer_port, "new")
        new.start()
        assert control(control_path, "release")["mode"] == "forward"
        chunks = []
        while True:
            block = client.recv(65536)
            if not block:
                break
            chunks.append(block)
        response = b"".join(chunks)
        assert b"HTTP/1.1 200 OK" in response
        assert b"new\n" in response
        final = control(control_path, "status")
        assert final["errors"] == 0
    finally:
        if client:
            client.close()
        old.close()
        if new:
            new.close()
        process.terminate()
        assert process.wait(timeout=5) == 0


def test_atomic_receipt_write_has_no_missing_runtime_dependency(tmp_path):
    target = tmp_path / "receipt.json"
    bootstrap.atomic_write_text(target, '{"ok":true}\n', 0o600)
    assert target.read_text() == '{"ok":true}\n'
    assert target.stat().st_mode & 0o777 == 0o600


def test_socket_and_reconcile_units_are_reboot_safe():
    bridge = bootstrap.bridge_unit_text("/usr/local/lib/gptadmin/bootstrap/x/gptadmin_socket_bootstrap.py")
    assert "Before=nginx.service" in bridge
    assert "Restart=no" in bridge
    assert "WantedBy=multi-user.target" in bridge
    reconcile = bootstrap.reconcile_unit_text("/usr/local/lib/gptadmin/bootstrap/x/gptadmin_socket_bootstrap.py")
    assert "Before=nginx.service" in reconcile
    assert "Restart=no" in reconcile
    assert "reconcile-boot" in reconcile
    assert "WantedBy=multi-user.target" in reconcile


def test_send_once_preserves_bytes_and_does_not_replay_after_partial(tmp_path):
    bridge = bootstrap.Bridge("127.0.0.1:1", "127.0.0.1:2", str(tmp_path / "c"),
                              1024, 1, 2, 512)

    class Slow:
        def __init__(self):
            self.chunks = []

        def send(self, data):
            self.chunks.append(bytes(data[:1]))
            return 1

    slow = Slow()
    assert bridge._send_once(slow, b"xyz") == 3
    assert b"".join(slow.chunks) == b"xyz"

    class Partial:
        def __init__(self):
            self.calls = []

        def send(self, data):
            self.calls.append(bytes(data))
            if len(self.calls) == 1:
                return 2
            raise OSError("reset")

    partial = Partial()
    with pytest.raises(bootstrap.PartialWrite) as caught:
        bridge._send_once(partial, b"abcdef")
    assert caught.value.sent == 2
    assert partial.calls == [b"abcdef", b"cdef"]
    assert bridge.partial_write_errors == 1
    assert bridge.partial_write_bytes == 2
    assert len(partial.calls) == 2


def _lease_fixture(tmp_path, **overrides):
    project = overrides.get("project", bootstrap.DEFAULT_RELEASE_PROJECT)
    owner = overrides.get("owner", {
        "hostId": "admin-node-100", "harness": "opencode", "nativeSessionId": "ses-lease"})
    fence = overrides.get("fence", 7)
    token = overrides.get("token", "lease-token-value")
    expires_at = overrides.get("expires_at", int(time.time() * 1000) + 3_600_000)
    unit = overrides.get("unit", bootstrap.PRIMARY_SERVICE)
    version = overrides.get("version", 1)
    include_lease = overrides.get("include_lease", True)
    token_hash = overrides.get("token_hash", hashlib.sha256(token.encode()).hexdigest())
    proof_owner = overrides.get("proof_owner", owner)
    proof_fence = overrides.get("proof_fence", fence)
    proof = tmp_path / "proof.json"
    proof.write_text(json.dumps({
        "project": project, "fence": proof_fence, "owner": proof_owner, "token": token}))
    proof.chmod(0o600)
    entry = {"fence": fence, "sessions": {}}
    if include_lease:
        entry["lease"] = {
            "project": project, "fence": fence, "owner": owner, "expiresAt": expires_at,
            "tokenHash": token_hash, "deploymentTarget": {"unit": unit},
        }
    document = {"projects": {project: entry}}
    if version is not None:
        document["version"] = version
    if overrides.get("schema"):
        document["schema"] = overrides["schema"]
    state = tmp_path / "leases.json"
    state.write_text(json.dumps(document))
    state.chmod(0o600)
    args = argparse.Namespace(
        release_lease_project=project, release_lease_fence=overrides.get("args_fence", fence),
        release_lease_host=proof_owner["hostId"],
        release_lease_owner_harness=proof_owner["harness"],
        release_lease_owner_session=proof_owner["nativeSessionId"],
        release_lease_target_unit=overrides.get("args_unit", unit),
        release_lease_fleet_dir="", release_lease_id=overrides.get("receipt_id", "lease-receipt-1"),
        release_lease_proof=proof)
    return args, state


@pytest.fixture
def local_release_lease_owner(monkeypatch):
    # The deployment account is host-specific. Unit fixtures use the real
    # owner of their temporary files; secure_regular still checks UID/mode.
    current_user = pwd.getpwuid(os.getuid())
    monkeypatch.setattr(bootstrap, "pwd", SimpleNamespace(getpwnam=lambda _name: current_user))


def test_release_lease_accepts_live_v1_document_and_refuses_the_gates(
        tmp_path, monkeypatch, local_release_lease_owner):
    args, state = _lease_fixture(tmp_path)
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", state)
    accepted = bootstrap.validate_release_lease(args, minimum_remaining_seconds=600)
    assert accepted["continuity"] == "matched"
    assert accepted["fence"] == 7
    assert "token" not in json.dumps(accepted)
    assert accepted["receipt_id"] == "lease-receipt-1"

    (tmp_path / "missing").mkdir()
    missing, missing_state = _lease_fixture(tmp_path / "missing", include_lease=False)
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", missing_state)
    with pytest.raises(bootstrap.BootstrapFailure, match="no active"):
        bootstrap.validate_release_lease(missing, minimum_remaining_seconds=0)

    (tmp_path / "expired").mkdir()
    expired, expired_state = _lease_fixture(tmp_path / "expired", expires_at=int(time.time() * 1000) - 1)
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", expired_state)
    with pytest.raises(bootstrap.BootstrapFailure, match="expired"):
        bootstrap.validate_release_lease(expired, minimum_remaining_seconds=0)

    (tmp_path / "owner").mkdir()
    wrong_owner, owner_state = _lease_fixture(
        tmp_path / "owner",
        owner={"hostId": "admin-node-100", "harness": "opencode", "nativeSessionId": "other"},
        proof_owner={"hostId": "admin-node-100", "harness": "opencode", "nativeSessionId": "ses-lease"})
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", owner_state)
    with pytest.raises(bootstrap.BootstrapFailure, match="owner"):
        bootstrap.validate_release_lease(wrong_owner, minimum_remaining_seconds=0)

    (tmp_path / "fence").mkdir()
    wrong_fence, fence_state = _lease_fixture(tmp_path / "fence", args_fence=8)
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", fence_state)
    with pytest.raises(bootstrap.BootstrapFailure, match="fence"):
        bootstrap.validate_release_lease(wrong_fence, minimum_remaining_seconds=0)

    (tmp_path / "hash").mkdir()
    bad_hash, hash_state = _lease_fixture(tmp_path / "hash", token_hash="0" * 64)
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", hash_state)
    with pytest.raises(bootstrap.BootstrapFailure, match="token continuity"):
        bootstrap.validate_release_lease(bad_hash, minimum_remaining_seconds=0)

    (tmp_path / "schema").mkdir()
    invented, invented_state = _lease_fixture(
        tmp_path / "schema", version=None, schema="agent-herder-project-release-leases/v1")
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", invented_state)
    with pytest.raises(bootstrap.BootstrapFailure, match="version"):
        bootstrap.validate_release_lease(invented, minimum_remaining_seconds=0)


def test_renewed_recovery_lease_is_recorded_without_token(
        tmp_path, monkeypatch, local_release_lease_owner):
    original_owner = {"hostId": "admin-node-100", "harness": "opencode",
                      "nativeSessionId": "ses-lease"}
    renewed, state = _lease_fixture(tmp_path, fence=8, token="renewed-lease-token", receipt_id="lease-new")
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", state)
    checked = bootstrap.validate_release_lease(renewed, minimum_remaining_seconds=120)
    receipt = {"release_lease": {
        "project": bootstrap.DEFAULT_RELEASE_PROJECT, "owner": original_owner, "fence": 7,
        "receipt_id": "lease-old", "deployment_target": {"unit": bootstrap.PRIMARY_SERVICE}}}
    continued = bootstrap.assert_lease_continuity(receipt, checked)
    assert continued["receipt_id"] == "lease-new"
    assert continued["fence"] == 8
    encoded = json.dumps(continued)
    assert "renewed-lease-token" not in encoded
    assert "tokenHash" not in encoded
    regressed = dict(checked, fence=6)
    with pytest.raises(bootstrap.BootstrapFailure, match="regressed"):
        bootstrap.assert_lease_continuity(receipt, regressed)


def test_snapshot_rejects_symlink_mutation_and_digest(tmp_path, monkeypatch):
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_STATE_ROOT", tmp_path / "state")
    payload = b"package-bytes"
    source = tmp_path / "pkg.tar.gz"
    source.write_bytes(payload)
    digest = hashlib.sha256(payload).hexdigest()
    link = tmp_path / "linked.tar.gz"
    link.symlink_to(source)
    with pytest.raises(bootstrap.BootstrapFailure, match="symlink"):
        bootstrap.snapshot_package(link, digest)
    with pytest.raises(bootstrap.BootstrapFailure, match="SHA-256"):
        bootstrap.snapshot_package(source, "a" * 64)

    real_read = os.read

    def mutating_read(fd, size):
        data = real_read(fd, size)
        if data and not getattr(mutating_read, "done", False):
            mutating_read.done = True
            with open(source, "ab") as handle:
                handle.write(b"mutated")
        return data

    monkeypatch.setattr(bootstrap.os, "read", mutating_read)
    with pytest.raises(bootstrap.BootstrapFailure, match="changed while snapshotting"):
        bootstrap.snapshot_package(source, digest)
    monkeypatch.undo()
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_STATE_ROOT", tmp_path / "state-ok")
    copied = bootstrap.snapshot_package(source, hashlib.sha256(source.read_bytes()).hexdigest())
    info = copied.stat()
    assert stat_mode_is_readonly(info.st_mode)
    assert copied.read_bytes() == source.read_bytes()


def stat_mode_is_readonly(mode):
    return mode & 0o222 == 0


def test_cutover_receipt_precedes_stop_and_symlink(tmp_path, monkeypatch):
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_RECEIPT", tmp_path / "receipt.json")
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_STATE_ROOT", tmp_path / "state")
    monkeypatch.setattr(bootstrap, "control_token", lambda: "secret-token")
    events = []

    def fake_backup(path, state_dir, name):
        events.append(("backup", bootstrap.read_receipt()["phase"]))
        return {"path": str(path), "existed": False}

    def fake_stop(unit, timeout):
        events.append(("stop", unit, bootstrap.read_receipt()["phase"]))
        return {"unit": unit, "main_pid": 0, "active_state": "inactive", "cgroup_procs": []}

    def fake_link(path, target):
        events.append(("symlink", bootstrap.read_receipt()["phase"], Path(path).name))

    monkeypatch.setattr(bootstrap, "file_backup", fake_backup)
    monkeypatch.setattr(bootstrap, "prove_writer_stopped", fake_stop)
    monkeypatch.setattr(bootstrap, "atomic_symlink", fake_link)
    real_write = bootstrap.atomic_write_text

    def wrapped_write(path, text, mode):
        if Path(path) == bootstrap.PRIMARY_SOCKET_PATH:
            events.append(("unit-write", bootstrap.read_receipt()["phase"]))
            return
        return real_write(path, text, mode)

    monkeypatch.setattr(bootstrap, "atomic_write_text", wrapped_write)
    monkeypatch.setattr(bootstrap, "run", lambda *args, **kwargs: SimpleNamespace(returncode=0, stdout="", stderr=""))
    monkeypatch.setattr(bootstrap, "validate_release_lease", lambda *args, **kwargs: {
        "project": "p", "owner": {}, "fence": 1, "receipt_id": "r",
        "deployment_target": {"unit": bootstrap.PRIMARY_SERVICE},
        "expires_at": 1, "remaining_seconds": 999, "continuity": "matched"})
    controller = bootstrap.BootstrapController(argparse.Namespace(
        operation_id="operation-123456", previous_dir="olddir", main_exit_wait=1,
        release_lease_id="r"))
    controller.target = tmp_path / "candidate-1"
    controller.previous = tmp_path / "olddir"
    controller.receipt = bootstrap.write_receipt({
        "phase": "old-quiesced", "operation_id": "operation-123456",
        "current_before": str(tmp_path / "olddir"), "release_lease_id": "r"})
    controller._persist_cutover()
    assert bootstrap.read_receipt()["phase"] == "cutover-prepared"
    controller._stop_old_writer()
    controller._switch_slots()
    assert [item[0] for item in events].index("backup") < [item[0] for item in events].index("stop")
    assert any(item[:2] == ("stop", bootstrap.PRIMARY_SERVICE) and item[2] == "old-stop-intent" for item in events)
    assert any(item[0] == "symlink" and item[1] == "slot-switch-intent" for item in events)
    assert any(item[0] == "unit-write" and item[1] == "slot-switch-intent" for item in events)
    assert "secret-token" not in (tmp_path / "receipt.json").read_text()


def test_rollback_does_not_start_previous_when_stop_proof_fails(tmp_path, monkeypatch):
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_RECEIPT", tmp_path / "receipt.json")
    monkeypatch.setattr(bootstrap, "control_token", lambda: "secret-token")
    starts = []

    def fake_run(argv, **kwargs):
        if list(argv[:2]) == ["systemctl", "start"]:
            starts.append(list(argv))
        stdout = "inactive\n" if "is-active" in argv else ""
        return SimpleNamespace(returncode=0, stdout=stdout, stderr="")

    monkeypatch.setattr(bootstrap, "run", fake_run)
    monkeypatch.setattr(bootstrap, "prove_writer_stopped",
                        lambda unit, timeout: (_ for _ in ()).throw(bootstrap.BootstrapFailure(f"cgroup busy {unit}")))
    monkeypatch.setattr(bootstrap, "prove_socket_stopped",
                        lambda unit, timeout: (_ for _ in ()).throw(bootstrap.BootstrapFailure(f"socket busy {unit}")))
    controller = bootstrap.BootstrapController(argparse.Namespace(
        operation_id="operation-123456", previous_dir="old", main_exit_wait=1,
        expected_revision="a" * 40, expected_live_version="b" * 40, release_lease_id="id"))
    controller.old_stopped = True
    controller.bridge_started = False
    controller.receipt = bootstrap.write_receipt({
        "phase": "green-active", "operation_id": "operation-123456", "release_lease_id": "id",
        "restore_current": str(tmp_path / "old"), "current_before": str(tmp_path / "old")})
    with pytest.raises(bootstrap.BootstrapFailure, match="not started"):
        controller.rollback("proof failed")
    assert starts == []
    assert bootstrap.read_receipt()["phase"] == "manual-required"
    assert "secret-token" not in (tmp_path / "receipt.json").read_text()


def test_reconcile_boot_never_restarts_and_tolerates_missing_bridge(tmp_path, monkeypatch):
    monkeypatch.setattr(bootstrap.os, "geteuid", lambda: 0)
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_RECEIPT", tmp_path / "receipt.json")
    calls = []

    def fake_run(argv, **kwargs):
        calls.append(list(argv))
        if list(argv[:2]) == ["systemctl", "is-active"]:
            return SimpleNamespace(returncode=0, stdout="active\n", stderr="")
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    monkeypatch.setattr(bootstrap, "run", fake_run)
    bootstrap.write_receipt({"phase": "public-via-bridge", "operation_id": "operation-123456"})
    assert bootstrap.cmd_reconcile_boot(argparse.Namespace()) == 0
    assert not any(item[1] in {"restart", "start"} for item in calls if item[0] == "systemctl")

    calls.clear()
    monkeypatch.setattr(bootstrap, "BOOTSTRAP_BRIDGE_UNIT_PATH", tmp_path / "missing.service")

    def inactive(argv, **kwargs):
        calls.append(list(argv))
        return SimpleNamespace(returncode=0, stdout="inactive\n", stderr="")

    monkeypatch.setattr(bootstrap, "run", inactive)
    assert bootstrap.cmd_reconcile_boot(argparse.Namespace()) == 0
    assert not any(len(item) > 1 and item[1] == "start" for item in calls)


def test_sandbox_guard_refuses_production_units_and_ports():
    path = ROOT / "scripts" / "gptadmin_bootstrap_sandbox.py"
    spec = importlib.util.spec_from_file_location("gptadmin_bootstrap_sandbox", path)
    sandbox = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(sandbox)
    with pytest.raises(sandbox.SandboxFailure, match="production"):
        sandbox.require_safe_unit("gptadmin-hub.service")
    with pytest.raises(sandbox.SandboxFailure, match="production"):
        sandbox.require_safe_port(9001)
    with pytest.raises(sandbox.SandboxFailure, match="production"):
        sandbox.run(["systemctl", "stop", "nginx.service"], check=False)


def test_lifecycle_lock_is_the_shared_opt_root_lock(tmp_path, monkeypatch):
    root = tmp_path / "opt" / "gptadmin-node"
    (root / "versions").mkdir(parents=True)
    monkeypatch.setattr(bootstrap, "VERSIONS_ROOT", root / "versions")
    held = bootstrap.lifecycle_lock()
    try:
        with pytest.raises(bootstrap.BootstrapFailure, match="active"):
            bootstrap.lifecycle_lock()
    finally:
        os.close(held)
    assert (root / ".lifecycle.lock").is_file()


def test_deployment_target_lock_matches_herder_shared_unit_lock(tmp_path, monkeypatch):
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", tmp_path / "leases.json")
    unit = bootstrap.PRIMARY_SERVICE
    key = hashlib.sha256(unit.encode()).hexdigest()
    expected_path = tmp_path / f"deploy-unit-{key}.lock"
    held = bootstrap.deployment_target_lock(unit)
    try:
        assert expected_path.is_file()
        assert expected_path.stat().st_mode & 0o777 == 0o600
        # A separate project alias naming the same unit contends on the exact
        # same Herder target lock, not a project-specific lock.
        with pytest.raises(bootstrap.BootstrapFailure, match="deployment target"):
            bootstrap.deployment_target_lock(unit)
    finally:
        os.close(held)
    available = bootstrap.deployment_target_lock(unit)
    os.close(available)


@pytest.mark.parametrize("outcome", ["commit", "rollback", "exception"])
def test_deployment_target_lock_released_after_operation_outcome(tmp_path, monkeypatch, outcome):
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", tmp_path / "leases.json")
    monkeypatch.setattr(bootstrap, "VERSIONS_ROOT", tmp_path / "opt" / "versions")
    unit = bootstrap.PRIMARY_SERVICE
    try:
        with bootstrap.operation_locks(unit):
            with pytest.raises(bootstrap.BootstrapFailure, match="deployment target"):
                bootstrap.deployment_target_lock(unit)
            if outcome == "rollback":
                raise bootstrap.BootstrapFailure("rollback outcome")
            if outcome == "exception":
                raise RuntimeError("operation exit")
    except (bootstrap.BootstrapFailure, RuntimeError):
        if outcome == "commit":
            raise
    available = bootstrap.deployment_target_lock(unit)
    os.close(available)


def test_deployment_target_lock_released_by_process_exit(tmp_path, monkeypatch):
    monkeypatch.setattr(bootstrap, "RELEASE_LEASE_STATE", tmp_path / "leases.json")
    unit = bootstrap.PRIMARY_SERVICE
    script = (
        "import fcntl,hashlib,os,sys; from pathlib import Path; "
        "p=Path(sys.argv[1]); u=sys.argv[2]; "
        "fd=os.open(p/('deploy-unit-'+hashlib.sha256(u.encode()).hexdigest()+'.lock'), "
        "os.O_CREAT|os.O_RDWR,0o600); "
        "fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB); "
        "print('held',flush=True); sys.stdin.buffer.read(1)"
    )
    proc = subprocess.Popen([sys.executable, "-c", script, str(tmp_path), unit],
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    try:
        assert proc.stdout.readline().strip() == "held"
        with pytest.raises(bootstrap.BootstrapFailure, match="deployment target"):
            bootstrap.deployment_target_lock(unit)
    finally:
        proc.kill()
        proc.wait(timeout=5)
    released = bootstrap.deployment_target_lock(unit)
    os.close(released)
