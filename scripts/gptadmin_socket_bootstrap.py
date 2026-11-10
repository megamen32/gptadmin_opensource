#!/usr/bin/env python3
"""Controlled ingress bridge for the legacy-to-canonical GPTAdmin handover.

The bridge never binds the legacy writer's port.  nginx points at its separate
listener (19002 by default).  FORWARD sends new streams to the legacy TCP
writer; HOLD accepts and bounds new streams while existing streams drain;
RELEASE sends held streams to the now-ready canonical TCP writer.
"""
from __future__ import annotations

import argparse
import contextlib
import errno
import fcntl
import hashlib
import importlib.util
import json
import os
import pwd
from pathlib import Path
import re
import secrets
import select
import selectors
import shutil
import signal
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone


def valid_host_port(value):
    match = re.fullmatch(r"(127\.0\.0\.1):([1-9][0-9]{0,4})", value)
    return bool(match and int(match.group(2)) <= 65535)


def address_port(value):
    if not valid_host_port(value):
        raise BootstrapFailure(f"invalid loopback address: {value}")
    return int(value.rsplit(":", 1)[1])


def canonical_origin():
    return "http://" + CANONICAL_ADDR


class Bridge:
    def __init__(self, listen, upstream, control, max_buffer, deadline, max_connections, per_connection_buffer):
        self.listen = listen
        self.upstream = upstream
        self.control = control
        self.max_buffer = max_buffer
        self.max_connections = max_connections
        self.per_connection_buffer = per_connection_buffer
        self.deadline = deadline
        self.mode = "forward"
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.active = 0
        self.forwarding = 0
        self.dialing = 0
        self.held = 0
        self.total_buffer = 0
        self.generation = 0
        self.slots = threading.BoundedSemaphore(max_connections)
        self.accepted = 0
        self.rejected = 0
        self.errors = 0
        self.partial_write_errors = 0
        self.partial_write_bytes = 0
        self.listener = None
        self.connections = set()
        self.control_thread = None

    def _status(self):
        with self.lock:
            return {"schema": "gptadmin.bootstrap-bridge/v1",
                    "mode": self.mode, "upstream": self.upstream,
                    "held": self.held, "active": self.active,
                    "forwarding": self.forwarding, "dialing": self.dialing,
                    "buffered": self.total_buffer, "accepted": self.accepted,
                    "rejected": self.rejected, "errors": self.errors,
                    "partial_write_errors": self.partial_write_errors,
                    "partial_write_bytes": self.partial_write_bytes,
                    "generation": self.generation}

    def _send_once(self, sock, data):
        """Send data once. Bytes already accepted are never resent.

        A later hard error is terminal for this buffer: callers must drop the
        connection instead of redialing or replaying the original payload.
        A timeout continues only with the unsent tail.
        """
        view = memoryview(data)
        sent = 0
        while sent < len(view):
            try:
                count = sock.send(view[sent:])
            except socket.timeout:
                continue
            except OSError as error:
                if sent:
                    with self.lock:
                        self.partial_write_errors += 1
                        self.partial_write_bytes += sent
                    raise PartialWrite(sent) from error
                raise
            if count <= 0:
                if sent:
                    with self.lock:
                        self.partial_write_errors += 1
                        self.partial_write_bytes += sent
                    raise PartialWrite(sent)
                raise ConnectionError("socket closed before any byte was accepted")
            sent += count
        return sent

    def _dial(self, upstream):
        host, port = upstream.rsplit(":", 1)
        return socket.create_connection((host, int(port)), min(0.5, self.deadline))

    def _finish_dial(self, peer, upstream, generation):
        """Atomically finish dialing and cross (or reject at) the HOLD barrier."""
        with self.lock:
            self.dialing -= 1
            if (peer is None or self.mode != "forward"
                    or self.generation != generation or self.upstream != upstream):
                return False
            self.connections.add(peer)
            self.forwarding += 1
            return True

    def _copy(self, client, peer, *, client_eof=False):
        sel = selectors.DefaultSelector()
        if not client_eof:
            sel.register(client, selectors.EVENT_READ, peer)
        sel.register(peer, selectors.EVENT_READ, client)
        try:
            while sel.get_map() and not self.stop.is_set():
                for key, _ in sel.select(0.25):
                    source, target = key.fileobj, key.data
                    try:
                        data = source.recv(65536)
                    except socket.timeout:
                        continue
                    if not data:
                        with contextlib.suppress(OSError):
                            target.shutdown(socket.SHUT_WR)
                        sel.unregister(source)
                    else:
                        try:
                            self._send_once(target, data)
                        except PartialWrite:
                            return
        finally:
            sel.close()

    def _connection(self, client):
        peer = None
        buffered = bytearray()
        held = False
        client_eof = False
        forwarding_accounted = False
        buffered_accounted = 0
        with self.lock:
            self.active += 1
            self.connections.add(client)
        try:
            client.settimeout(0.25)
            deadline = time.monotonic() + self.deadline
            while not self.stop.is_set():
                with self.lock:
                    ready = self.mode == "forward"
                    upstream = self.upstream
                    generation = self.generation
                    if ready:
                        # Reserving dialing under the same lock as HOLD means a
                        # HOLD response with dialing=forwarding=0 is a barrier:
                        # no later old-writer dial can appear behind it.
                        self.dialing += 1
                    if ready and held:
                        held = False
                        self.held -= 1
                    if not ready and not held:
                        held = True
                        self.held += 1
                if ready:
                    try:
                        peer = self._dial(upstream)
                    except OSError:
                        peer = None
                    if self._finish_dial(peer, upstream, generation):
                        forwarding_accounted = True
                        break
                    if peer is not None:
                        with contextlib.suppress(OSError):
                            peer.close()
                        peer = None
                    time.sleep(0.05)

                # Backpressure: reserve buffer budget before recv. The global
                # value can never exceed max_buffer even under concurrent reads.
                room = 0
                if not client_eof:
                    with self.lock:
                        room = min(self.max_buffer - self.total_buffer,
                                   self.per_connection_buffer - len(buffered), 65536)
                        if room > 0:
                            self.total_buffer += room
                    if room > 0:
                        part = None
                        try:
                            part = client.recv(room)
                            if part == b"":
                                client_eof = True
                        except socket.timeout:
                            part = None
                        finally:
                            with self.lock:
                                self.total_buffer -= room
                                if part:
                                    self.total_buffer += len(part)
                                    buffered_accounted += len(part)
                        if part:
                            buffered += part
                    else:
                        time.sleep(0.01)
                elif not buffered:
                    return
                else:
                    # A write-half-closed client may still wait for a response.
                    # Keep its buffered request until RELEASE and propagate the
                    # half-close only after the request reaches the new writer.
                    time.sleep(0.01)

                if time.monotonic() >= deadline:
                    with self.lock:
                        self.errors += 1
                    return
            if peer is None:
                return
            with self.lock:
                self.total_buffer -= buffered_accounted
                buffered_accounted = 0
            peer.settimeout(0.25)
            if buffered:
                try:
                    self._send_once(peer, buffered)
                except PartialWrite:
                    return
            if client_eof:
                with contextlib.suppress(OSError):
                    peer.shutdown(socket.SHUT_WR)
            self._copy(client, peer, client_eof=client_eof)
        except (OSError, TimeoutError):
            with self.lock:
                self.errors += 1
        finally:
            for sock in (peer, client):
                if sock:
                    with contextlib.suppress(OSError):
                        sock.close()
            with self.lock:
                self.connections.discard(client)
                if peer is not None:
                    self.connections.discard(peer)
                self.active -= 1
                self.held -= int(held)
                self.total_buffer -= buffered_accounted
                if forwarding_accounted:
                    self.forwarding -= 1
            self.slots.release()

    def _control(self):
        if os.path.exists(self.control):
            os.unlink(self.control)
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(self.control)
        os.chmod(self.control, 0o660)
        server.listen(8)
        server.settimeout(0.25)
        while not self.stop.is_set():
            try:
                conn, _ = server.accept()
            except socket.timeout:
                continue
            with conn:
                conn.settimeout(1)
                command = conn.recv(64).decode("ascii", "strict").strip().lower()
                with self.lock:
                    if command == "hold":
                        self.mode = "hold"
                        self.generation += 1
                    elif command == "release":
                        probe = None
                        try:
                            probe = self._dial(self.upstream)
                        except OSError:
                            self.errors += 1
                        finally:
                            if probe:
                                probe.close()
                        if probe:
                            self.mode = "forward"
                            self.generation += 1
                    elif command.startswith("upstream "):
                        value = command.split(None, 1)[1]
                        if (self.mode != "hold" or self.forwarding or self.dialing
                                or not valid_host_port(value)):
                            self.errors += 1
                        else:
                            self.upstream = value
                            self.generation += 1
                    elif command not in ("status", ""):
                        self.errors += 1
                conn.sendall((json.dumps(self._status(), sort_keys=True) + "\n").encode())
        server.close()
        try:
            os.unlink(self.control)
        except FileNotFoundError:
            pass

    def run(self):
        host, port = self.listen.rsplit(":", 1)
        self.listener = socket.create_server((host, int(port)), backlog=self.max_connections)
        self.listener.settimeout(0.25)
        executor = ThreadPoolExecutor(max_workers=self.max_connections, thread_name_prefix="bootstrap-bridge")
        self.control_thread = threading.Thread(target=self._control, name="bootstrap-control")
        self.control_thread.start()
        while not self.stop.is_set():
            try:
                client, _ = self.listener.accept()
            except socket.timeout:
                continue
            except OSError:
                if self.stop.is_set():
                    break
                raise
            if not self.slots.acquire(blocking=False):
                with self.lock:
                    self.errors += 1
                    self.rejected += 1
                client.close()
            else:
                with self.lock:
                    self.accepted += 1
                executor.submit(self._connection, client)
        with contextlib.suppress(OSError):
            self.listener.close()
        executor.shutdown(wait=True, cancel_futures=True)
        if self.control_thread:
            self.control_thread.join(timeout=2)

    def close(self, *_args):
        self.stop.set()
        if self.listener:
            with contextlib.suppress(OSError):
                self.listener.close()
        with self.lock:
            connections = list(self.connections)
        for connection in connections:
            with contextlib.suppress(OSError):
                connection.shutdown(socket.SHUT_RDWR)
            with contextlib.suppress(OSError):
                connection.close()


BOOTSTRAP_RECEIPT_SCHEMA = "gptadmin.socket-bootstrap/v1"
BOOTSTRAP_STATE_ROOT = Path("/var/lib/gptadmin-node/primary")
BOOTSTRAP_RECEIPT = BOOTSTRAP_STATE_ROOT / "socket-bootstrap-receipt.json"
BOOTSTRAP_NGINX_BACKUP = BOOTSTRAP_STATE_ROOT / "nginx-upstream.pre-bootstrap.conf"
BOOTSTRAP_CONTROLLER_ROOT = Path("/usr/local/lib/gptadmin/bootstrap")
BOOTSTRAP_BRIDGE_UNIT = "gptadmin-bootstrap-bridge.service"
BOOTSTRAP_BRIDGE_UNIT_PATH = Path("/etc/systemd/system") / BOOTSTRAP_BRIDGE_UNIT
BOOTSTRAP_RECONCILE_UNIT = "gptadmin-bootstrap-reconcile.service"
BOOTSTRAP_RECONCILE_UNIT_PATH = Path("/etc/systemd/system") / BOOTSTRAP_RECONCILE_UNIT
BOOTSTRAP_GREEN_SERVICE = "gptadmin-bootstrap-green.service"
BOOTSTRAP_GREEN_SERVICE_PATH = Path("/etc/systemd/system") / BOOTSTRAP_GREEN_SERVICE
BOOTSTRAP_CONTROL = Path("/run/gptadmin-bootstrap/control.sock")
BOOTSTRAP_BRIDGE_ADDR = "127.0.0.1:19002"
CANONICAL_ADDR = "127.0.0.1:9001"
DEFAULT_NGINX_UPSTREAM = Path("/etc/nginx/conf.d/gptadmin-hub-upstream.conf")
TRUSTED_MANAGER = Path("/usr/local/lib/gptadmin/gptadmin_node_manage.py")
VERSIONS_ROOT = Path("/opt/gptadmin-node/versions")
CURRENT_LINK = Path("/opt/gptadmin-node/current")
PREVIOUS_LINK = Path("/opt/gptadmin-node/previous")
PRIMARY_SERVICE = "gptadmin-hub.service"
PRIMARY_SOCKET = "gptadmin-hub.socket"
PRIMARY_SOCKET_PATH = Path("/etc/systemd/system/gptadmin-hub.socket")
PRIMARY_SOCKET_DROPIN = Path("/etc/systemd/system/gptadmin-hub.service.d/zzzzzz-primary-socket.conf")
IPTABLES = "/usr/sbin/iptables"
RELEASE_LEASE_STATE = Path("/home/admin/.local/state/agent-herder/project-release-leases.json")
DEFAULT_RELEASE_PROJECT = "/home/admin/gptadmin/.git"


class BootstrapFailure(RuntimeError):
    pass


class PartialWrite(OSError):
    def __init__(self, sent):
        super().__init__(errno.EIO, f"partial write of {sent} bytes; refusing replay")
        self.sent = int(sent)


WRITER_DOWN_PHASES = frozenset({
    "old-stop-intent", "old-stopped", "slot-switch-intent", "slot-switched",
    "green-active", "canonical-socket-retargeted", "canonical-active",
    "nginx-restored",
})
BRIDGE_REQUIRED_PHASES = frozenset({
    "bridge-forward-old", "nginx-switching", "nginx-bridge", "public-via-bridge",
    "old-quiesced", "cutover-prepared", *WRITER_DOWN_PHASES,
})
SECRET_FIELD = re.compile(r"(token|secret|password|authorization|cookie|bearer)", re.I)
FS_IMMUTABLE_FL = 0x00000010
FS_IOC_GETFLAGS = 0x80086601
FS_IOC_SETFLAGS = 0x40086602


def utc_now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def run(argv, *, timeout=30, check=True, input_text=None):
    result = subprocess.run(argv, input=input_text, text=True, capture_output=True,
                            timeout=timeout, check=False)
    if check and result.returncode:
        detail = (result.stderr or result.stdout or "").strip().splitlines()
        raise BootstrapFailure(f"command failed ({result.returncode}): {' '.join(argv)}: "
                               f"{detail[-1] if detail else 'no output'}")
    return result


def fsync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def atomic_write_bytes(path, data, mode):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + f".new-{os.getpid()}-{secrets.token_hex(4)}")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(tmp, mode)
        os.replace(tmp, path)
        fsync_dir(path.parent)
    finally:
        with contextlib.suppress(FileNotFoundError):
            tmp.unlink()


def atomic_write_text(path, text, mode):
    atomic_write_bytes(path, text.encode("utf-8"), mode)


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()



def read_release_proof(path):
    """Read a Herder proof file. The token never leaves this function's return."""
    path = Path(path)
    secure_regular(path, owner=os.geteuid(), max_bytes=65536, writable_mask=0o077)
    try:
        proof = json.loads(path.read_text())
    except (OSError, ValueError) as error:
        raise BootstrapFailure("release lease proof is unreadable") from error
    token = proof.get("token") if isinstance(proof, dict) else None
    if not isinstance(token, str) or not token or token != token.strip():
        raise BootstrapFailure("release lease proof token is missing")
    return proof, token


def expected_deployment_target(args):
    target = {"unit": args.release_lease_target_unit}
    fleet_dir = getattr(args, "release_lease_fleet_dir", "") or ""
    if fleet_dir:
        target["fleetDir"] = fleet_dir
    return target


def validate_release_lease(args, *, minimum_remaining_seconds=0, now_ms=None):
    """Match Agent Herder release_gate.ReleaseGate.check, not an invented schema.

    The live ledger is ``{"version": 1, "projects": {<canonical .git path>:
    {"fence": N, "lease": {...}}}}``. Admission requires that nested lease's
    project path, owner, fence, expiresAt, tokenHash of the proof token, and
    the full deploymentTarget. A receipt id by itself is not a lease.
    """
    expected_uid = pwd.getpwnam("admin").pw_uid
    info = secure_regular(RELEASE_LEASE_STATE, owner=expected_uid,
                          max_bytes=1024 * 1024, writable_mask=0o077)
    try:
        state = json.loads(RELEASE_LEASE_STATE.read_text())
    except (OSError, ValueError) as error:
        raise BootstrapFailure("release lease state is unreadable") from error
    if not isinstance(state, dict) or state.get("version") != 1:
        raise BootstrapFailure("unsupported release lease state version")
    project = args.release_lease_project
    entry = (state.get("projects") or {}).get(project)
    lease = entry.get("lease") if isinstance(entry, dict) else None
    if not isinstance(lease, dict):
        raise BootstrapFailure("no active GPTAdmin project release lease")
    if lease.get("project", project) != project:
        raise BootstrapFailure("project release lease canonical project changed")
    try:
        expires_at = int(lease["expiresAt"])
        fence = int(lease["fence"])
    except (KeyError, TypeError, ValueError) as error:
        raise BootstrapFailure("project release lease is missing expiry or fence") from error
    now_ms = int(time.time() * 1000 if now_ms is None else now_ms)
    remaining_ms = expires_at - now_ms
    if expires_at <= now_ms or remaining_ms < int(minimum_remaining_seconds * 1000):
        raise BootstrapFailure("project release lease is expired or too close to expiry")
    expected_owner = {
        "hostId": args.release_lease_host,
        "harness": args.release_lease_owner_harness,
        "nativeSessionId": args.release_lease_owner_session,
    }
    if lease.get("owner") != expected_owner:
        raise BootstrapFailure("project release lease owner changed")
    if fence != int(args.release_lease_fence):
        raise BootstrapFailure("project release lease fence changed")
    proof, token = read_release_proof(args.release_lease_proof)
    if (proof.get("project") != project or proof.get("owner") != expected_owner
            or int(proof.get("fence") or -1) != fence):
        raise BootstrapFailure("release proof does not match the requested lease identity")
    digest = hashlib.sha256(token.encode()).hexdigest()
    token = None
    if not isinstance(lease.get("tokenHash"), str) or lease.get("tokenHash") != digest:
        raise BootstrapFailure("release lease token continuity failed")
    target = expected_deployment_target(args)
    if lease.get("deploymentTarget") != target:
        raise BootstrapFailure("project release lease deployment target changed")
    return {
        "project": project, "owner": expected_owner, "fence": fence,
        "expires_at": expires_at, "remaining_seconds": remaining_ms // 1000,
        "receipt_id": args.release_lease_id,
        "deployment_target": target, "state_inode": info.st_ino,
        "continuity": "matched",
    }


def _fs_ioc(direction, number):
    size = struct.calcsize("@l")
    return (direction << 30) | (size << 16) | (ord("f") << 8) | number


def try_set_immutable(fd):
    """Best-effort root immutable flag. Unsupported filesystems stay mode 0444."""
    if os.geteuid() != 0:
        return False
    try:
        current = struct.unpack("@l", fcntl.ioctl(fd, _fs_ioc(2, 1), struct.pack("@l", 0)))[0]
        fcntl.ioctl(fd, _fs_ioc(1, 2), struct.pack("@l", current | FS_IMMUTABLE_FL))
        seen = struct.unpack("@l", fcntl.ioctl(fd, _fs_ioc(2, 1), struct.pack("@l", 0)))[0]
    except OSError as error:
        if error.errno in (errno.EOPNOTSUPP, errno.ENOTTY, errno.EINVAL, errno.EPERM):
            return False
        raise
    return bool(seen & FS_IMMUTABLE_FL)


def snapshot_identity(path, expected_sha256):
    """Re-open a snapshot by O_NOFOLLOW and require digest, size, and mode."""
    path = Path(path)
    fd = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise BootstrapFailure("package snapshot is not a regular file")
        if stat.S_IMODE(info.st_mode) & 0o222:
            raise BootstrapFailure("package snapshot is writable")
        if info.st_uid != os.geteuid():
            raise BootstrapFailure("package snapshot owner changed")
        digest = hashlib.sha256()
        total = 0
        while True:
            block = os.read(fd, 1024 * 1024)
            if not block:
                break
            total += len(block)
            digest.update(block)
        if total != info.st_size or digest.hexdigest() != expected_sha256:
            raise BootstrapFailure("package snapshot digest mismatch")
        return {"dev": int(info.st_dev), "ino": int(info.st_ino), "size": int(info.st_size),
                "mode": stat.S_IMODE(info.st_mode), "sha256": expected_sha256}
    finally:
        os.close(fd)


def require_snapshot(path, expected_sha256, identity=None):
    current = snapshot_identity(path, expected_sha256)
    if identity:
        for key in ("dev", "ino", "size", "sha256"):
            if str(current[key]) != str(identity[key]):
                raise BootstrapFailure("package snapshot identity changed before extraction")
    return current


def snapshot_package(source, expected_sha256):
    """Copy the candidate through an O_NOFOLLOW fd into a root-owned snapshot.

    Later extraction must use the snapshot path recorded in the receipt, never
    the caller-supplied path. The source inode/size must stay unchanged for
    the whole copy; a symlink, digest mismatch, or mutation is rejected.
    """
    source = Path(source)
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha256):
        raise BootstrapFailure("package sha256 must be 64 lowercase hex characters")
    BOOTSTRAP_STATE_ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    snapshots = BOOTSTRAP_STATE_ROOT / "packages"
    snapshots.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(snapshots, 0o700)
    target = snapshots / (expected_sha256 + ".tar.gz")
    if target.exists() or target.is_symlink():
        snapshot_identity(target, expected_sha256)
        return target
    tmp = target.with_name(target.name + f".new-{os.getpid()}-{secrets.token_hex(4)}")
    digest = hashlib.sha256()
    total = 0
    source_fd = None
    out_fd = None
    try:
        source_fd = os.open(source, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
        before = os.fstat(source_fd)
        if not stat.S_ISREG(before.st_mode) or before.st_size > 128 * 1024 * 1024:
            raise BootstrapFailure("candidate package is not a bounded regular file")
        if stat.S_ISLNK(before.st_mode):
            raise BootstrapFailure("candidate package is a symlink")
        out_fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        while True:
            block = os.read(source_fd, 1024 * 1024)
            if not block:
                break
            total += len(block)
            if total > 128 * 1024 * 1024:
                raise BootstrapFailure("candidate package exceeds byte limit")
            digest.update(block)
            offset = 0
            view = memoryview(block)
            while offset < len(view):
                wrote = os.write(out_fd, view[offset:])
                if wrote <= 0:
                    raise BootstrapFailure("package snapshot write stalled")
                offset += wrote
        os.fsync(out_fd)
        after = os.fstat(source_fd)
        if ((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns)
                != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns)):
            raise BootstrapFailure("candidate package changed while snapshotting")
        if total != before.st_size or digest.hexdigest() != expected_sha256:
            raise BootstrapFailure("candidate package SHA-256 mismatch")
        os.fchmod(out_fd, 0o444)
        os.fsync(out_fd)
        os.close(out_fd)
        out_fd = None
        os.replace(tmp, target)
        fsync_dir(target.parent)
        final = os.open(target, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
        try:
            try_set_immutable(final)
            os.fsync(final)
        finally:
            os.close(final)
        snapshot_identity(target, expected_sha256)
        return target
    except OSError as error:
        if isinstance(error, BootstrapFailure):
            raise
        if error.errno == errno.ELOOP:
            raise BootstrapFailure("candidate package is a symlink") from error
        raise BootstrapFailure(f"candidate package snapshot failed: {error.strerror}") from error
    finally:
        if source_fd is not None:
            os.close(source_fd)
        if out_fd is not None:
            os.close(out_fd)
        with contextlib.suppress(FileNotFoundError):
            tmp.unlink()


def secure_regular(path, *, owner=0, max_bytes=None, writable_mask=0o022):
    path = Path(path)
    info = os.lstat(path)
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise BootstrapFailure(f"unsafe non-regular path: {path}")
    if owner is not None and info.st_uid != owner:
        raise BootstrapFailure(f"unexpected owner for {path}")
    if info.st_mode & writable_mask:
        raise BootstrapFailure(f"unsafe writable mode for {path}")
    if max_bytes is not None and info.st_size > max_bytes:
        raise BootstrapFailure(f"oversized file: {path}")
    return info


def load_module(path, name):
    secure_regular(path, owner=0, max_bytes=4 * 1024 * 1024)
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise BootstrapFailure(f"cannot import {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def host_arch():
    machine = os.uname().machine.lower()
    mapping = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
    if machine not in mapping:
        raise BootstrapFailure(f"unsupported architecture {machine}")
    return mapping[machine]


def read_manifest(root):
    root = Path(root)
    raw = (root / "manifest.json").read_bytes()
    if len(raw) > 65536:
        raise BootstrapFailure("manifest exceeds byte limit")
    try:
        manifest = json.loads(raw)
    except ValueError as error:
        raise BootstrapFailure("manifest is invalid JSON") from error
    return manifest


def verify_extracted_tree(root, expected_commit):
    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise BootstrapFailure("candidate root is not a real directory")
    manifest = read_manifest(root)
    if (manifest.get("schema") != "gptadmin.node-package/v1"
            or manifest.get("git_commit") != expected_commit
            or manifest.get("source_dirty") is not False
            or manifest.get("platform") != "linux"
            or manifest.get("arch") != host_arch()):
        raise BootstrapFailure("candidate manifest identity mismatch")
    files = manifest.get("files")
    if not isinstance(files, dict) or not files:
        raise BootstrapFailure("candidate manifest has no files")
    actual = set()
    for path in root.rglob("*"):
        rel = path.relative_to(root).as_posix()
        if path.is_symlink():
            raise BootstrapFailure(f"candidate contains symlink: {rel}")
        if path.is_file():
            actual.add(rel)
    expected = set(files) | {"manifest.json"}
    if actual != expected:
        raise BootstrapFailure("candidate extracted closure differs from manifest")
    for rel, metadata in files.items():
        path = root / rel
        if not path.is_file() or path.stat().st_size != metadata.get("size"):
            raise BootstrapFailure(f"candidate size mismatch: {rel}")
        if sha256_file(path) != metadata.get("sha256"):
            raise BootstrapFailure(f"candidate digest mismatch: {rel}")
    binary = root / "bin/gptadmin-node"
    if not os.access(binary, os.X_OK) or binary.read_bytes()[:4] != b"\x7fELF":
        raise BootstrapFailure("candidate binary is not executable ELF")
    return manifest


def parse_env_file(path):
    values = {}
    path = Path(path)
    if not path.is_file():
        return values
    for raw in path.read_text().splitlines():
        raw = raw.strip()
        if not raw or raw.startswith("#") or "=" not in raw:
            continue
        key, value = raw.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        values[key] = value
    return values


def primary_env():
    values = parse_env_file("/etc/gptadmin/gptadmin.env")
    values.update(parse_env_file("/etc/gptadmin/nodes/primary.env"))
    return values


def control_token():
    env = primary_env()
    token = env.get("CTL_TOKEN") or env.get("GPTADMIN_CTL_TOKEN") or env.get("ADMIN_PASSWORD")
    if not token:
        raise BootstrapFailure("primary control token is unavailable")
    return token


def http_json(method, url, *, token=None, body=None, timeout=5):
    headers = {"Accept": "application/json", "Connection": "close"}
    data = None
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        data = json.dumps(body, separators=(",", ":")).encode()
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=timeout) as response:
            raw = response.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024:
                raise BootstrapFailure("HTTP response exceeds 2 MiB")
            payload = json.loads(raw) if raw else None
            return response.status, payload
    except urllib.error.HTTPError as error:
        raw = error.read(65536)
        try:
            payload = json.loads(raw) if raw else None
        except ValueError:
            payload = None
        return error.code, payload


def wait_version(origin, expected, *, timeout=30):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            hstatus, health = http_json("GET", origin.rstrip("/") + "/healthz", timeout=2)
            vstatus, version = http_json("GET", origin.rstrip("/") + "/version", timeout=2)
            last = (hstatus, health, vstatus, version)
            if (hstatus == 200 and isinstance(health, dict) and health.get("ok") is True
                    and vstatus == 200 and isinstance(version, dict)
                    and version.get("git_commit") == expected):
                return version
        except (OSError, ValueError, BootstrapFailure) as error:
            last = type(error).__name__
        time.sleep(0.1)
    raise BootstrapFailure(f"{origin} did not report expected version {expected}: {last!r}")


def service_property(unit, prop):
    result = run(["systemctl", "show", unit, "-p", prop, "--value"], timeout=5, check=False)
    return result.stdout.strip() if result.returncode == 0 else ""


def service_main_pid(unit):
    try:
        return int(service_property(unit, "MainPID") or "0")
    except ValueError:
        return 0


def wait_main_pid_zero(unit, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if service_main_pid(unit) == 0:
            return
        time.sleep(0.05)
    raise BootstrapFailure(f"{unit} main process did not exit")


def wait_unit_state(unit, state, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if run(["systemctl", "is-active", unit], check=False, timeout=5).stdout.strip() == state:
            return
        time.sleep(0.1)
    raise BootstrapFailure(f"{unit} did not become {state}")


def wait_unit_stopped(unit, timeout):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = run(["systemctl", "is-active", unit], check=False, timeout=5).stdout.strip()
        if last in ("inactive", "failed", "unknown"):
            return last
        time.sleep(0.1)
    raise BootstrapFailure(f"{unit} did not stop (state={last!r})")


def parse_show(text):
    fields = {}
    for line in text.splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            fields[key] = value
    return fields


def cgroup_proc_ids(control_group):
    if not control_group or control_group in {"n/a", "/"}:
        return []
    relative = control_group.lstrip("/")
    for root in (Path("/sys/fs/cgroup"), Path("/sys/fs/cgroup/systemd")):
        procs = root / relative / "cgroup.procs"
        try:
            text = procs.read_text()
        except OSError:
            continue
        return [int(item) for item in text.split() if item.isdigit()]
    return []


def prove_writer_stopped(unit, timeout):
    """Stop one writer and require MainPID 0, inactive state, and an empty cgroup.

    systemctl's return code alone is not proof. A failed proof must not be
    followed by starting another writer.
    """
    result = run(["systemctl", "stop", unit], timeout=max(timeout, 20), check=False)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip().splitlines()
        raise BootstrapFailure(f"stop failed for {unit} ({result.returncode}): "
                               f"{detail[-1] if detail else 'no output'}")
    deadline = time.monotonic() + timeout
    last = {}
    while True:
        show = run(["systemctl", "show", unit, "-p", "MainPID", "-p", "ActiveState",
                    "-p", "SubState", "-p", "ControlGroup", "-p", "Result"],
                   timeout=10, check=False)
        if show.returncode != 0:
            raise BootstrapFailure(f"cannot read stop state for {unit}")
        fields = parse_show(show.stdout)
        pid = int(fields.get("MainPID") or "0")
        state = fields.get("ActiveState", "")
        procs = cgroup_proc_ids(fields.get("ControlGroup", ""))
        if pid == 0 and state in {"inactive", "failed", "dead"} and not procs:
            return {"unit": unit, "main_pid": 0, "active_state": state,
                    "cgroup": fields.get("ControlGroup", ""), "cgroup_procs": []}
        last = {"main_pid": pid, "active_state": state, "cgroup_procs": procs,
                "result": fields.get("Result", "")}
        if time.monotonic() >= deadline:
            break
        time.sleep(0.1)
    raise BootstrapFailure(f"writer stop unproven for {unit}: {last}")


def prove_socket_stopped(unit, timeout):
    result = run(["systemctl", "stop", unit], timeout=max(timeout, 20), check=False)
    if result.returncode != 0:
        raise BootstrapFailure(f"socket stop failed for {unit} ({result.returncode})")
    deadline = time.monotonic() + timeout
    last = ""
    while True:
        show = run(["systemctl", "show", unit, "-p", "ActiveState", "-p", "SubState", "-p", "Result"],
                   timeout=10, check=False)
        if show.returncode != 0:
            raise BootstrapFailure(f"cannot read socket state for {unit}")
        fields = parse_show(show.stdout)
        state = fields.get("ActiveState", "")
        if state in {"inactive", "failed", "dead"}:
            return {"unit": unit, "active_state": state, "stop_rc": 0}
        last = state
        if time.monotonic() >= deadline:
            break
        time.sleep(0.1)
    raise BootstrapFailure(f"socket stop unproven for {unit}: {last}")


def public_lease(lease):
    target = lease.get("deployment_target") or {}
    return scrub_secrets({
        "project": lease.get("project"),
        "owner": lease.get("owner"),
        "fence": lease.get("fence"),
        "expires_at": lease.get("expires_at"),
        "remaining_seconds": lease.get("remaining_seconds"),
        "receipt_id": lease.get("receipt_id"),
        "deployment_target": {"unit": target.get("unit")},
    })


def assert_lease_continuity(receipt, recovery_lease):
    """A recovery lease may be new, but project, owner, and target must continue.

    The recovery receipt id does not have to equal the migration receipt id.
    A lower fence or a different owner/project/unit is foreign and refused.
    """
    original = receipt.get("release_lease")
    if not isinstance(original, dict):
        raise BootstrapFailure("receipt has no original release lease")
    recovery = public_lease(recovery_lease)
    if recovery.get("project") != original.get("project"):
        raise BootstrapFailure("recovery lease project does not match original lease")
    if recovery.get("owner") != original.get("owner"):
        raise BootstrapFailure("recovery lease owner does not match original lease")
    original_unit = (original.get("deployment_target") or {}).get("unit")
    recovery_unit = (recovery.get("deployment_target") or {}).get("unit")
    if not original_unit or recovery_unit != original_unit:
        raise BootstrapFailure("recovery lease target unit does not match original lease")
    if int(recovery.get("fence") or -1) < int(original.get("fence") or -1):
        raise BootstrapFailure("recovery lease fence regressed")
    if (recovery.get("receipt_id") == original.get("receipt_id")
            and int(recovery.get("fence") or -1) != int(original.get("fence") or -1)):
        raise BootstrapFailure("same lease id cannot change fence")
    return recovery


def bridge_control(command, *, timeout=3):
    deadline = time.monotonic() + timeout
    while True:
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
                sock.settimeout(max(0.1, deadline - time.monotonic()))
                sock.connect(str(BOOTSTRAP_CONTROL))
                sock.sendall(command.encode("ascii"))
                raw = sock.recv(65536)
            value = json.loads(raw)
            if not isinstance(value, dict):
                raise BootstrapFailure("bridge control returned non-object")
            return value
        except (FileNotFoundError, ConnectionRefusedError, socket.timeout):
            if time.monotonic() >= deadline:
                raise BootstrapFailure("bridge control is unavailable")
            time.sleep(0.05)


def wait_bridge(predicate, timeout, description):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = bridge_control("status")
        if predicate(last):
            return last
        time.sleep(0.05)
    raise BootstrapFailure(f"bridge did not reach {description}: {last!r}")


def bridge_identity():
    pid = service_main_pid(BOOTSTRAP_BRIDGE_UNIT)
    if not pid:
        raise BootstrapFailure("bridge service has no MainPID")
    values = {}
    for line in Path(f"/proc/{pid}/status").read_text().splitlines():
        if ":" in line:
            key, value = line.split(":", 1)
            values[key] = value.strip()
    uid = int(values["Uid"].split()[0])
    gid = int(values["Gid"].split()[0])
    return pid, uid, gid


def http_json_as_uid(uid, gid, method, url, *, token=None, body=None, timeout=5):
    read_fd, write_fd = os.pipe()
    pid = os.fork()
    if pid == 0:
        try:
            os.close(read_fd)
            os.setgroups([])
            os.setgid(gid)
            os.setuid(uid)
            result = http_json(method, url, token=token, body=body, timeout=timeout)
            payload = json.dumps({"ok": True, "result": result}, separators=(",", ":")).encode()
        except BaseException as error:
            payload = json.dumps({"ok": False, "error": type(error).__name__}, separators=(",", ":")).encode()
        with os.fdopen(write_fd, "wb", closefd=True) as stream:
            stream.write(payload)
        os._exit(0)
    os.close(write_fd)
    deadline = time.monotonic() + timeout + 2
    chunks = []
    try:
        while time.monotonic() < deadline:
            ready, _, _ = select_select([read_fd], [], [], min(0.2, deadline - time.monotonic()))
            if ready:
                block = os.read(read_fd, 65536)
                if not block:
                    break
                chunks.append(block)
            done, _ = os.waitpid(pid, os.WNOHANG)
            if done == pid and not ready:
                block = os.read(read_fd, 65536)
                if block:
                    chunks.append(block)
                break
        else:
            os.kill(pid, signal.SIGKILL)
            raise BootstrapFailure("identity-scoped HTTP request timed out")
    finally:
        os.close(read_fd)
        with contextlib.suppress(ChildProcessError):
            os.waitpid(pid, 0)
    value = json.loads(b"".join(chunks) or b"{}")
    if value.get("ok") is not True:
        raise BootstrapFailure(f"identity-scoped HTTP request failed: {value.get('error')}")
    status, payload = value["result"]
    return status, payload


def select_select(readers, writers, errors, timeout):
    return __import__("select").select(readers, writers, errors, timeout)



def wait_version_as_uid(uid, gid, origin, expected, timeout=30):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            hstatus, health = http_json_as_uid(uid, gid, "GET", origin + "/healthz", timeout=2)
            vstatus, version = http_json_as_uid(uid, gid, "GET", origin + "/version", timeout=2)
            last = (hstatus, health, vstatus, version)
            if (hstatus == 200 and isinstance(health, dict) and health.get("ok") is True
                    and vstatus == 200 and isinstance(version, dict)
                    and version.get("git_commit") == expected):
                return version
        except Exception as error:
            last = type(error).__name__
        time.sleep(0.1)
    raise BootstrapFailure(f"identity-scoped {origin} did not report {expected}: {last!r}")


def verify_socket_handoff_as_uid(uid, gid, origin, token, expected_commit):
    status, handoff = http_json_as_uid(uid, gid, "GET", origin + "/admin/api/node/handoff",
                                       token=token, timeout=5)
    if (status != 200 or not isinstance(handoff, dict)
            or handoff.get("socket_inherited") is not True
            or handoff.get("state") != "idle"):
        raise BootstrapFailure(f"writer did not confirm inherited socket: {handoff!r}")
    wait_version_as_uid(uid, gid, origin, expected_commit, timeout=30)
    return handoff


def begin_handoff_as_uid(uid, gid, origin, token, operation_id):
    status, payload = http_json_as_uid(uid, gid, "POST", origin + "/admin/api/node/handoff",
                                       token=token,
                                       body={"action": "begin", "operation_id": operation_id,
                                             "wait_seconds": 30, "lease_seconds": 120},
                                       timeout=40)
    if (status != 200 or not isinstance(payload, dict)
            or payload.get("ready") is not True
            or payload.get("state") != "ready"
            or payload.get("operation_id") != operation_id
            or payload.get("socket_inherited") is not True):
        raise BootstrapFailure(f"green handoff failed: HTTP {status} {payload!r}")
    return payload

def scrub_secrets(value):
    if isinstance(value, dict):
        scrubbed = {}
        for key, item in value.items():
            scrubbed[key] = "[redacted]" if SECRET_FIELD.search(str(key)) else scrub_secrets(item)
        return scrubbed
    if isinstance(value, list):
        return [scrub_secrets(item) for item in value]
    if isinstance(value, str) and value.lower().startswith("bearer "):
        return "[redacted]"
    return value


def write_receipt(receipt):
    value = scrub_secrets(dict(receipt))
    value["schema"] = BOOTSTRAP_RECEIPT_SCHEMA
    value["updated_at"] = utc_now()
    atomic_write_text(BOOTSTRAP_RECEIPT, json.dumps(value, sort_keys=True, indent=2) + "\n", 0o600)
    return value


def read_receipt():
    if not BOOTSTRAP_RECEIPT.is_file():
        return None
    value = json.loads(BOOTSTRAP_RECEIPT.read_text())
    if value.get("schema") != BOOTSTRAP_RECEIPT_SCHEMA:
        raise BootstrapFailure("unsupported bootstrap receipt schema")
    return value


def transition(receipt, phase, **fields):
    value = dict(receipt)
    value.update(fields)
    value["phase"] = phase
    return write_receipt(value)


def atomic_symlink(path, target):
    path = Path(path)
    target = Path(target).resolve()
    if target.parent != VERSIONS_ROOT.resolve() or not target.is_dir():
        raise BootstrapFailure("release slot target is outside versions root")
    tmp = path.with_name(path.name + f".new-{os.getpid()}-{secrets.token_hex(4)}")
    with contextlib.suppress(FileNotFoundError):
        tmp.unlink()
    os.symlink(str(target), tmp)
    os.replace(tmp, path)
    fsync_dir(path.parent)


def nginx_worker_pids():
    try:
        master = int(Path("/run/nginx.pid").read_text().strip())
        raw = Path(f"/proc/{master}/task/{master}/children").read_text().strip()
        return {int(value) for value in raw.split() if value.isdigit()}
    except (OSError, ValueError):
        return set()


def wait_pids_gone(pids, timeout):
    deadline = time.monotonic() + timeout
    remaining = set(pids)
    while remaining and time.monotonic() < deadline:
        remaining = {pid for pid in remaining if Path(f"/proc/{pid}").exists()}
        if remaining:
            time.sleep(0.1)
    if remaining:
        raise BootstrapFailure(f"old nginx workers did not exit: {sorted(remaining)}")


def read_secure_nginx(path):
    info = secure_regular(path, owner=0, max_bytes=65536)
    return Path(path).read_bytes(), stat.S_IMODE(info.st_mode)


def upstream_block(text):
    pattern = re.compile(r"(?ms)^upstream\s+gptadmin_hub_active\s*\{.*?^\}\s*\n?")
    matches = list(pattern.finditer(text))
    if len(matches) != 1:
        raise BootstrapFailure("managed nginx upstream block is missing or ambiguous")
    block = matches[0].group(0)
    required = (
        "zone gptadmin_hub_active 64k;",
        "server 127.0.0.1:9001 max_fails=1 fail_timeout=1s;",
        "server 127.0.0.1:19001 backup;",
        "keepalive 64;",
    )
    if any(block.count(item) != 1 for item in required):
        raise BootstrapFailure("managed nginx upstream shape drifted")
    return matches[0]


def render_bridge_nginx(original):
    text = original.decode("utf-8")
    match = upstream_block(text)
    block = ("upstream gptadmin_hub_active {\n"
             "    zone gptadmin_hub_active 64k;\n"
             "    server 127.0.0.1:19002 max_fails=1 fail_timeout=1s;\n"
             "    keepalive 64;\n"
             "}\n")
    return (text[:match.start()] + block + text[match.end():]).encode("utf-8")


def switch_nginx_to_bridge(path, receipt):
    path = Path(path)
    original, mode = read_secure_nginx(path)
    upstream_block(original.decode("utf-8"))
    atomic_write_bytes(BOOTSTRAP_NGINX_BACKUP, original, 0o600)
    receipt = transition(receipt, "nginx-switching",
                         nginx_path=str(path), nginx_mode=mode,
                         nginx_original_sha256=sha256_bytes(original),
                         nginx_backup=str(BOOTSTRAP_NGINX_BACKUP))
    candidate = render_bridge_nginx(original)
    old_workers = nginx_worker_pids()
    atomic_write_bytes(path, candidate, mode)
    try:
        run(["nginx", "-t", "-q"], timeout=15)
        run(["systemctl", "reload", "nginx"], timeout=20)
        wait_pids_gone(old_workers, 30)
    except Exception:
        atomic_write_bytes(path, original, mode)
        run(["nginx", "-t", "-q"], timeout=15, check=False)
        run(["systemctl", "reload", "nginx"], timeout=20, check=False)
        raise
    return transition(receipt, "nginx-bridge", nginx_bridge_sha256=sha256_bytes(candidate))


def restore_nginx(receipt):
    path = Path(receipt["nginx_path"])
    backup = Path(receipt["nginx_backup"])
    original = backup.read_bytes()
    if sha256_bytes(original) != receipt["nginx_original_sha256"]:
        raise BootstrapFailure("nginx rollback backup digest mismatch")
    old_workers = nginx_worker_pids()
    atomic_write_bytes(path, original, int(receipt["nginx_mode"]))
    run(["nginx", "-t", "-q"], timeout=15)
    run(["systemctl", "reload", "nginx"], timeout=20)
    wait_pids_gone(old_workers, 30)


def iptables_rule_specs(operation_id, bridge_uid):
    prefix = [IPTABLES, "-t", "nat"]
    canonical_port = str(address_port(CANONICAL_ADDR))
    bridge_port = str(address_port(BOOTSTRAP_BRIDGE_ADDR))
    exempt = ["OUTPUT", "-p", "tcp", "-d", "127.0.0.1/32", "--dport", canonical_port,
              "-m", "owner", "--uid-owner", str(bridge_uid),
              "-m", "comment", "--comment", f"gptadmin-bootstrap:{operation_id}:bridge", "-j", "RETURN"]
    redirect = ["OUTPUT", "-p", "tcp", "-d", "127.0.0.1/32", "--dport", canonical_port,
                "-m", "comment", "--comment", f"gptadmin-bootstrap:{operation_id}:redirect",
                "-j", "REDIRECT", "--to-ports", bridge_port]
    return prefix, exempt, redirect


def add_local_redirect(operation_id, bridge_uid):
    prefix, exempt, redirect = iptables_rule_specs(operation_id, bridge_uid)
    if run(prefix + ["-C"] + exempt, check=False).returncode == 0:
        raise BootstrapFailure("bootstrap bridge exemption rule already exists")
    if run(prefix + ["-C"] + redirect, check=False).returncode == 0:
        raise BootstrapFailure("bootstrap redirect rule already exists")
    run(prefix + ["-I", "OUTPUT", "1"] + exempt[1:])
    try:
        run(prefix + ["-I", "OUTPUT", "2"] + redirect[1:])
    except Exception:
        run(prefix + ["-D"] + exempt, check=False)
        raise


def remove_local_redirect(operation_id, bridge_uid):
    prefix, exempt, redirect = iptables_rule_specs(operation_id, bridge_uid)
    while run(prefix + ["-C"] + redirect, check=False).returncode == 0:
        run(prefix + ["-D"] + redirect, check=False)
    while run(prefix + ["-C"] + exempt, check=False).returncode == 0:
        run(prefix + ["-D"] + exempt, check=False)


def bridge_unit_text(controller_path):
    return f'''[Unit]
Description=GPTAdmin one-time socket bootstrap bridge
After=network-online.target
Wants=network-online.target
Before=nginx.service

[Service]
Type=simple
DynamicUser=yes
ExecStart=/usr/bin/python3 {controller_path} bridge-serve --listen {BOOTSTRAP_BRIDGE_ADDR} --upstream {CANONICAL_ADDR} --control {BOOTSTRAP_CONTROL} --deadline 180 --max-connections 512 --max-buffer 8388608 --per-connection-buffer 524288
Restart=no
UMask=0077
RuntimeDirectory=gptadmin-bootstrap
RuntimeDirectoryMode=0700
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/run/gptadmin-bootstrap
LimitNOFILE=4096
TasksMax=768
MemoryMax=256M
TimeoutStopSec=190
KillMode=mixed

[Install]
WantedBy=multi-user.target
'''


def bootstrap_primary_socket_text():
    return f'''[Unit]
Description=Canonical GPTAdmin Hub primary listener (bootstrap target)

[Socket]
Service={BOOTSTRAP_GREEN_SERVICE}
ListenStream={CANONICAL_ADDR}
Backlog=4096
NoDelay=true

[Install]
WantedBy=sockets.target
'''


def green_service_text(target):
    binary = Path(target) / "bin/gptadmin-node"
    canonical_port = address_port(CANONICAL_ADDR)
    return f'''[Unit]
Description=GPTAdmin temporary green writer
Requires={PRIMARY_SOCKET}
After={PRIMARY_SOCKET} network-online.target

[Service]
Type=simple
Sockets={PRIMARY_SOCKET}
User=root
EnvironmentFile=/etc/gptadmin/gptadmin.env
EnvironmentFile=-/etc/gptadmin/nodes/primary.env
Environment=GPTADMIN_HUB_HOST=127.0.0.1
Environment=GPTADMIN_HUB_PORT={canonical_port}
Environment=GPTADMIN_ROOT={target}
Environment=GPTADMIN_NODE_DIAGNOSTICS_SOCKET=/run/gptadmin-bootstrap-green/node.sock
Environment=SHELL_SERVICE_NAME={BOOTSTRAP_GREEN_SERVICE}
Environment=ROOTD_SERVICE_NAME={BOOTSTRAP_GREEN_SERVICE}
ExecStart={binary}
Restart=no
RuntimeDirectory=gptadmin-bootstrap-green
RuntimeDirectoryMode=0700
MemoryHigh=768M
MemoryMax=1536M
MemorySwapMax=0
CPUQuota=100%
TasksMax=512
Environment=GOMAXPROCS=1
Environment=GOMEMLIMIT=640MiB
TimeoutStartSec=30
TimeoutStopSec=15
KillMode=mixed
'''

def next_version_dir(commit):
    used = set()
    VERSIONS_ROOT.mkdir(parents=True, exist_ok=True)
    for entry in VERSIONS_ROOT.iterdir():
        if entry.is_dir() and entry.name.startswith(commit + "-"):
            suffix = entry.name[len(commit) + 1:]
            if suffix.isdigit():
                used.add(int(suffix))
    index = 1
    while index in used:
        index += 1
    return VERSIONS_ROOT / f"{commit}-{index}"


def file_backup(path, state_dir, name):
    path = Path(path)
    if path.exists() or path.is_symlink():
        data, mode = read_secure_nginx(path)
        backup = state_dir / name
        atomic_write_bytes(backup, data, 0o600)
        return {"path": str(path), "existed": True, "backup": str(backup),
                "sha256": sha256_bytes(data), "mode": mode}
    return {"path": str(path), "existed": False}


def restore_file_backup(record):
    path = Path(record["path"])
    if record.get("existed"):
        backup = Path(record["backup"])
        data = backup.read_bytes()
        if sha256_bytes(data) != record["sha256"]:
            raise BootstrapFailure(f"backup digest mismatch for {path}")
        atomic_write_bytes(path, data, int(record["mode"]))
    else:
        with contextlib.suppress(FileNotFoundError):
            path.unlink()
            fsync_dir(path.parent)


def wait_quiet(origin, token, timeout):
    deadline = time.monotonic() + timeout
    stable = 0
    last = None
    while time.monotonic() < deadline:
        status, payload = http_json("POST", origin + "/admin/api/node/drain",
                                    token=token, body={"action": "status"}, timeout=5)
        if status != 200 or not isinstance(payload, dict):
            raise BootstrapFailure(f"drain status failed: HTTP {status}")
        counters = payload.get("counters") or {}
        last = counters
        quiet = (int(counters.get("unfinished_executions", 0) or 0) == 0
                 and int(counters.get("outbox_pending", 0) or 0) == 0)
        stable = stable + 1 if quiet else 0
        if stable >= 3:
            return counters
        time.sleep(0.25)
    raise BootstrapFailure(f"writer did not reach quiet preflight: {last!r}")


def wait_drain_ready_as_uid(uid, gid, token, timeout):
    origin = canonical_origin()
    status, payload = http_json_as_uid(uid, gid, "POST", origin + "/admin/api/node/drain",
                                       token=token, body={"action": "begin"}, timeout=5)
    if status != 200:
        raise BootstrapFailure(f"old writer drain begin failed: HTTP {status}")
    deadline = time.monotonic() + timeout
    last = payload
    while time.monotonic() < deadline:
        status, payload = http_json_as_uid(uid, gid, "POST", origin + "/admin/api/node/drain",
                                           token=token, body={"action": "status"}, timeout=5)
        last = payload
        if status == 200 and isinstance(payload, dict) and payload.get("ready_to_stop") is True:
            return payload
        time.sleep(0.1)
    with contextlib.suppress(Exception):
        http_json_as_uid(uid, gid, "POST", origin + "/admin/api/node/drain",
                         token=token, body={"action": "cancel"}, timeout=5)
    raise BootstrapFailure(f"old writer drain did not become ready: {last!r}")


def verify_socket_handoff(origin, token, expected_commit):
    status, handoff = http_json("GET", origin + "/admin/api/node/handoff", token=token, timeout=5)
    if (status != 200 or not isinstance(handoff, dict)
            or handoff.get("socket_inherited") is not True
            or handoff.get("state") != "idle"):
        raise BootstrapFailure(f"writer did not confirm inherited socket: {handoff!r}")
    wait_version(origin, expected_commit, timeout=30)
    return handoff


def begin_handoff(origin, token, operation_id):
    status, payload = http_json("POST", origin + "/admin/api/node/handoff", token=token,
                                body={"action": "begin", "operation_id": operation_id,
                                      "wait_seconds": 30, "lease_seconds": 120}, timeout=40)
    if (status != 200 or not isinstance(payload, dict)
            or payload.get("ready") is not True
            or payload.get("state") != "ready"
            or payload.get("operation_id") != operation_id
            or payload.get("socket_inherited") is not True):
        raise BootstrapFailure(f"green handoff failed: HTTP {status} {payload!r}")
    return payload


def reconcile_unit_text(controller_path):
    return f'''[Unit]
Description=GPTAdmin bootstrap reboot reconciliation
Before=nginx.service
After=network-online.target
Wants=network-online.target
ConditionPathExists={BOOTSTRAP_RECEIPT}

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/python3 {controller_path} reconcile-boot
Restart=no

[Install]
WantedBy=multi-user.target
'''


def install_temp_units(controller_path, target):
    atomic_write_text(BOOTSTRAP_BRIDGE_UNIT_PATH, bridge_unit_text(controller_path), 0o644)
    atomic_write_text(BOOTSTRAP_RECONCILE_UNIT_PATH, reconcile_unit_text(controller_path), 0o644)
    atomic_write_text(BOOTSTRAP_GREEN_SERVICE_PATH, green_service_text(target), 0o644)
    run(["systemctl", "daemon-reload"], timeout=20)


def remove_temp_units():
    run(["systemctl", "disable", "--now", BOOTSTRAP_BRIDGE_UNIT],
        timeout=30, check=False)
    run(["systemctl", "disable", BOOTSTRAP_RECONCILE_UNIT], timeout=20, check=False)
    for unit in (BOOTSTRAP_GREEN_SERVICE,):
        run(["systemctl", "stop", unit], timeout=20, check=False)
    for path in (BOOTSTRAP_GREEN_SERVICE_PATH, BOOTSTRAP_BRIDGE_UNIT_PATH, BOOTSTRAP_RECONCILE_UNIT_PATH):
        with contextlib.suppress(FileNotFoundError):
            path.unlink()
    run(["systemctl", "daemon-reload"], timeout=20, check=False)


def refresh_helpers(target):
    source_dir = Path(target) / "scripts"
    destination = Path("/usr/local/lib/gptadmin")
    for name in ("gptadmin_node_manage.py", "gptadmin_node_probe.py", "gptadmin_node_autoupdate.py"):
        source = source_dir / name
        data = source.read_bytes()
        atomic_write_bytes(destination / name, data, 0o644)


class BootstrapController:
    def __init__(self, args):
        self.args = args
        self.token = control_token()
        self.operation_id = args.operation_id
        self.previous = (VERSIONS_ROOT / args.previous_dir).resolve()
        self.target = None
        self.candidate_manager = None
        self.bridge_uid = None
        self.bridge_gid = None
        self.receipt = None
        self.old_drain_started = False
        self.redirect_added = False
        self.nginx_switched = False
        self.bridge_started = False
        self.old_stopped = False
        self.green_started = False
        self.canonical_started = False
        self.primary_backups = None

    def preflight(self):
        if os.geteuid() != 0:
            raise BootstrapFailure("root is required")
        for value, label in ((self.args.expected_revision, "expected revision"),
                             (self.args.candidate_commit, "candidate commit"),
                             (self.args.controller_commit, "controller commit")):
            if not re.fullmatch(r"[0-9a-f]{40}", value):
                raise BootstrapFailure(f"{label} must be a full lowercase SHA")
        if self.args.candidate_commit != self.args.controller_commit:
            raise BootstrapFailure("candidate and controller commits must be identical")
        repo = Path(__file__).resolve().parents[1]
        observed = run(["git", "-c", f"safe.directory={repo}",
                        "-C", str(repo), "rev-parse", "HEAD"], timeout=10).stdout.strip()
        if observed != self.args.controller_commit:
            raise BootstrapFailure(f"controller checkout changed: {observed}")
        dirty = run(["git", "-c", f"safe.directory={repo}", "-C", str(repo),
                     "status", "--porcelain", "--untracked-files=normal"], timeout=10).stdout
        if dirty.strip():
            raise BootstrapFailure("controller checkout is dirty")
        lease = validate_release_lease(self.args, minimum_remaining_seconds=600)
        package_source = Path(self.args.package).resolve()
        package = snapshot_package(package_source, self.args.package_sha256)
        current = CURRENT_LINK.resolve()
        current_manifest = verify_extracted_tree(current, self.args.expected_revision)
        if self.previous.parent != VERSIONS_ROOT.resolve():
            raise BootstrapFailure("previous slot is outside versions root")
        verify_extracted_tree(self.previous, self.args.expected_revision)
        if run(["systemctl", "is-active", PRIMARY_SERVICE], check=False).stdout.strip() != "active":
            raise BootstrapFailure("primary service is not active")
        if run(["systemctl", "is-active", PRIMARY_SOCKET], check=False).stdout.strip() == "active":
            raise BootstrapFailure("canonical socket is already active; bootstrap is unnecessary")
        live = wait_version(canonical_origin(), self.args.expected_live_version, timeout=10)
        existing = read_receipt()
        if existing and existing.get("phase") not in ("committed", "rolled-back"):
            raise BootstrapFailure(f"previous bootstrap is incomplete: {existing.get('phase')}")
        for address in (BOOTSTRAP_BRIDGE_ADDR,):
            host, port = address.rsplit(":", 1)
            with socket.socket() as probe:
                try:
                    probe.bind((host, int(port)))
                except OSError as error:
                    raise BootstrapFailure(f"bootstrap address is occupied: {address}") from error
        original, _ = read_secure_nginx(self.args.nginx_upstream)
        upstream_block(original.decode("utf-8"))
        if not self.args.release_lease_id:
            raise BootstrapFailure("release lease receipt id is required")
        self.receipt = write_receipt({
            "phase": "preflight",
            "operation_id": self.operation_id,
            "release_lease_id": self.args.release_lease_id,
            "release_lease": lease,
            "controller_commit": self.args.controller_commit,
            "candidate_commit": self.args.candidate_commit,
            "expected_revision": self.args.expected_revision,
            "expected_live_version": self.args.expected_live_version,
            "package": str(package_source),
            "package_snapshot": str(package),
            "package_snapshot_identity": snapshot_identity(package, self.args.package_sha256),
            "package_sha256": self.args.package_sha256,
            "public_origin": self.args.public_origin,
            "nginx_upstream": str(self.args.nginx_upstream),
            "quiet_wait": self.args.quiet_wait,
            "drain_wait": self.args.drain_wait,
            "main_exit_wait": self.args.main_exit_wait,
            "current_before": str(current),
            "current_manifest": {"git_commit": current_manifest.get("git_commit"),
                                 "build_version": current_manifest.get("build_version")},
            "previous_dir": self.previous.name,
            "live_before": live,
        })
        return package

    def extract_candidate(self, package):
        staging = VERSIONS_ROOT / f".bootstrap-staging-{self.operation_id}"
        if staging.exists() or staging.is_symlink():
            raise BootstrapFailure(f"bootstrap staging already exists: {staging}")
        require_snapshot(package, self.args.package_sha256,
                         (self.receipt or {}).get("package_snapshot_identity"))
        trusted = load_module(TRUSTED_MANAGER, "gptadmin_trusted_manager")
        manifest = trusted.verify_and_extract(package, trusted.host_arch(), staging)
        if manifest.get("git_commit") != self.args.candidate_commit:
            raise BootstrapFailure("trusted extraction returned a different candidate commit")
        verify_extracted_tree(staging, self.args.candidate_commit)
        candidate_module = load_module(staging / "scripts/gptadmin_node_manage.py", "gptadmin_candidate_manager_pre")
        canary = candidate_module.canary_candidate(staging, self.args.candidate_commit, primary_env())
        target = next_version_dir(self.args.candidate_commit)
        os.rename(staging, target)
        fsync_dir(VERSIONS_ROOT)
        verify_extracted_tree(target, self.args.candidate_commit)
        self.target = target
        self.candidate_manager = load_module(target / "scripts/gptadmin_node_manage.py", "gptadmin_candidate_manager")
        controller_dir = BOOTSTRAP_CONTROLLER_ROOT / self.args.candidate_commit
        controller_dir.mkdir(mode=0o755, parents=True, exist_ok=True)
        controller_path = controller_dir / "gptadmin_socket_bootstrap.py"
        controller_data = Path(__file__).read_bytes()
        atomic_write_bytes(controller_path, controller_data, 0o755)
        if sha256_file(controller_path) != sha256_bytes(controller_data):
            raise BootstrapFailure("installed bootstrap controller readback mismatch")
        self.receipt = transition(self.receipt, "candidate-installed",
                                  target_dir=target.name, target=str(target),
                                  candidate_manifest={"git_commit": manifest.get("git_commit"),
                                                      "build_version": manifest.get("build_version")},
                                  canary=canary, controller_path=str(controller_path),
                                  controller_sha256=sha256_file(controller_path))
        return controller_path

    def start_bridge(self, controller_path):
        install_temp_units(controller_path, self.target)
        run(["systemctl", "enable", "--now", BOOTSTRAP_BRIDGE_UNIT], timeout=30)
        run(["systemctl", "enable", BOOTSTRAP_RECONCILE_UNIT], timeout=20)
        wait_unit_state(BOOTSTRAP_BRIDGE_UNIT, "active", 10)
        status = wait_bridge(lambda value: value.get("mode") == "forward"
                             and value.get("upstream") == CANONICAL_ADDR, 10, "forward-old")
        _, self.bridge_uid, self.bridge_gid = bridge_identity()
        wait_version("http://127.0.0.1:19002", self.args.expected_live_version, timeout=10)
        self.bridge_started = True
        self.receipt = transition(self.receipt, "bridge-forward-old",
                                  bridge_status=status, bridge_uid=self.bridge_uid,
                                  bridge_gid=self.bridge_gid, bridge_enabled=True)

    def route_public_to_bridge(self):
        validate_release_lease(self.args, minimum_remaining_seconds=300)
        self.receipt = switch_nginx_to_bridge(self.args.nginx_upstream, self.receipt)
        self.nginx_switched = True
        wait_version(self.args.public_origin, self.args.expected_live_version, timeout=15)
        self.receipt = transition(self.receipt, "public-via-bridge")

    def switch_old_to_green(self):
        validate_release_lease(self.args, minimum_remaining_seconds=480)
        quiet = wait_quiet(canonical_origin(), self.token, self.args.quiet_wait)
        started = time.monotonic()
        status = bridge_control("hold")
        if status.get("mode") != "hold":
            raise BootstrapFailure("bridge refused HOLD")
        wait_bridge(lambda value: value.get("dialing") == 0 and value.get("forwarding") == 0,
                    10, "hold barrier")
        add_local_redirect(self.operation_id, self.bridge_uid)
        self.redirect_added = True
        drain = wait_drain_ready_as_uid(self.bridge_uid, self.bridge_gid, self.token,
                                        self.args.drain_wait)
        self.old_drain_started = True
        self.receipt = transition(self.receipt, "old-quiesced", quiet=quiet, drain=drain)
        self._persist_cutover()
        self._stop_old_writer()
        self._switch_slots()
        self._start_green(started)

    def _persist_cutover(self):
        """Durably record backups and the intended pointers before any mutation."""
        validate_release_lease(self.args, minimum_remaining_seconds=420)
        state = BOOTSTRAP_STATE_ROOT / f"unit-backups-{self.operation_id}"
        state.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.primary_backups = {
            "socket": file_backup(PRIMARY_SOCKET_PATH, state, "gptadmin-hub.socket"),
            "dropin": file_backup(PRIMARY_SOCKET_DROPIN, state, "zzzzzz-primary-socket.conf"),
        }
        socket_text = bootstrap_primary_socket_text()
        self.receipt = transition(
            self.receipt, "cutover-prepared",
            primary_unit_backups=self.primary_backups,
            intended_current=self.target.name,
            intended_previous=self.previous.name,
            restore_current=self.receipt.get("current_before"),
            restore_previous=self.previous.name,
            intended_socket_sha256=sha256_bytes(socket_text.encode()),
            recovery={"start_unit": PRIMARY_SERVICE, "hold_until_previous_proven": True})

    def _stop_old_writer(self):
        validate_release_lease(self.args, minimum_remaining_seconds=360)
        self.receipt = transition(self.receipt, "old-stop-intent",
                                  stop_units=[PRIMARY_SERVICE, BOOTSTRAP_GREEN_SERVICE])
        primary = prove_writer_stopped(PRIMARY_SERVICE, self.args.main_exit_wait)
        green = prove_writer_stopped(BOOTSTRAP_GREEN_SERVICE, self.args.main_exit_wait)
        self.old_stopped = True
        self.receipt = transition(self.receipt, "old-stopped",
                                  old_stop_proof=primary, green_stop_proof=green)

    def _switch_slots(self):
        validate_release_lease(self.args, minimum_remaining_seconds=300)
        self.receipt = transition(self.receipt, "slot-switch-intent",
                                  symlink_current=self.target.name,
                                  symlink_previous=self.previous.name)
        atomic_symlink(PREVIOUS_LINK, self.previous)
        atomic_symlink(CURRENT_LINK, self.target)
        atomic_write_text(PRIMARY_SOCKET_PATH, bootstrap_primary_socket_text(), 0o644)
        run(["systemctl", "daemon-reload"], timeout=20)
        self.receipt = transition(self.receipt, "slot-switched",
                                  current_dir=self.target.name,
                                  previous_dir=self.previous.name)

    def _start_green(self, started):
        validate_release_lease(self.args, minimum_remaining_seconds=240)
        prove_writer_stopped(PRIMARY_SERVICE, 20)
        prove_writer_stopped(BOOTSTRAP_GREEN_SERVICE, 20)
        run(["systemctl", "start", PRIMARY_SOCKET], timeout=20)
        run(["systemctl", "start", BOOTSTRAP_GREEN_SERVICE], timeout=30)
        green_version = wait_version_as_uid(self.bridge_uid, self.bridge_gid,
                                            canonical_origin(),
                                            self.args.candidate_commit, timeout=30)
        inherited = verify_socket_handoff_as_uid(self.bridge_uid, self.bridge_gid,
                                                 canonical_origin(), self.token,
                                                 self.args.candidate_commit)
        self.green_started = True
        released = bridge_control("release")
        if released.get("mode") != "forward":
            raise BootstrapFailure("bridge refused RELEASE to green")
        wait_version(self.args.public_origin, self.args.candidate_commit, timeout=20)
        elapsed = round(time.monotonic() - started, 3)
        self.receipt = transition(self.receipt, "green-active", first_hold_seconds=elapsed,
                                  green_version=green_version, green_inherited=inherited,
                                  primary_unit_backups=self.primary_backups,
                                  current_dir=self.target.name, previous_dir=self.previous.name,
                                  bridge_status=bridge_control("status"))

    def install_canonical(self):
        validate_release_lease(self.args, minimum_remaining_seconds=120)
        current_socket, mode = read_secure_nginx(PRIMARY_SOCKET_PATH)
        expected_bootstrap = bootstrap_primary_socket_text().encode("utf-8")
        if current_socket != expected_bootstrap:
            raise BootstrapFailure("bootstrap socket unit changed before canonical retarget")
        final_socket = self.candidate_manager.PRIMARY_SOCKET_UNIT_TEXT.encode("utf-8")
        final_dropin = (
            "[Unit]\nRequires=gptadmin-hub.socket\nAfter=gptadmin-hub.socket\n\n"
            "[Service]\nSockets=gptadmin-hub.socket\n").encode()
        atomic_write_bytes(PRIMARY_SOCKET_PATH, final_socket, mode)
        atomic_write_bytes(PRIMARY_SOCKET_DROPIN, final_dropin, 0o644)
        run(["systemctl", "daemon-reload"], timeout=20)
        if run(["systemctl", "is-active", PRIMARY_SOCKET], check=False).stdout.strip() != "active":
            raise BootstrapFailure("canonical socket stopped during retarget")
        listen = service_property(PRIMARY_SOCKET, "Listen")
        triggers = service_property(PRIMARY_SOCKET, "Triggers")
        if "127.0.0.1:9001" not in listen or PRIMARY_SERVICE not in triggers:
            raise BootstrapFailure(f"canonical socket retarget mismatch: listen={listen!r} triggers={triggers!r}")
        self.receipt = transition(self.receipt, "canonical-socket-retargeted",
                                  socket_listen=listen, socket_triggers=triggers,
                                  socket_sha256=sha256_bytes(final_socket),
                                  dropin_sha256=sha256_bytes(final_dropin))

    def switch_green_to_canonical(self):
        validate_release_lease(self.args, minimum_remaining_seconds=90)
        started = time.monotonic()
        status = bridge_control("hold")
        if status.get("mode") != "hold":
            raise BootstrapFailure("bridge refused second HOLD")
        wait_bridge(lambda value: value.get("dialing") == 0 and value.get("forwarding") == 0,
                    10, "second hold barrier")
        handoff = begin_handoff_as_uid(self.bridge_uid, self.bridge_gid,
                                       canonical_origin(), self.token,
                                       "bootstrap-green:" + self.args.candidate_commit)
        prove_writer_stopped(BOOTSTRAP_GREEN_SERVICE, self.args.main_exit_wait)
        prove_writer_stopped(PRIMARY_SERVICE, 20)
        run(["systemctl", "start", PRIMARY_SERVICE], timeout=30)
        candidate_version = wait_version_as_uid(self.bridge_uid, self.bridge_gid,
                                                canonical_origin(),
                                                self.args.candidate_commit, timeout=40)
        inherited = verify_socket_handoff_as_uid(self.bridge_uid, self.bridge_gid,
                                                 canonical_origin(), self.token,
                                                 self.args.candidate_commit)
        self.canonical_started = True
        remove_local_redirect(self.operation_id, self.bridge_uid)
        self.redirect_added = False
        wait_version(canonical_origin(), self.args.candidate_commit, timeout=20)
        released = bridge_control("release")
        if released.get("mode") != "forward":
            raise BootstrapFailure("bridge refused RELEASE to canonical")
        wait_version(self.args.public_origin, self.args.candidate_commit, timeout=20)
        elapsed = round(time.monotonic() - started, 3)
        self.receipt = transition(self.receipt, "canonical-active",
                                  green_handoff=handoff, inherited=inherited,
                                  candidate_version=candidate_version,
                                  second_hold_seconds=elapsed)

    def commit(self):
        validate_release_lease(self.args, minimum_remaining_seconds=30)
        restore_nginx(self.receipt)
        self.nginx_switched = False
        wait_version(self.args.public_origin, self.args.candidate_commit, timeout=20)
        wait_bridge(lambda value: value.get("active") == 0 and value.get("buffered") == 0,
                    20, "empty bridge")
        run(["systemctl", "stop", BOOTSTRAP_BRIDGE_UNIT], timeout=30, check=False)
        self.bridge_started = False
        run(["systemctl", "stop", BOOTSTRAP_GREEN_SERVICE], timeout=20, check=False)
        remove_temp_units()
        refresh_helpers(self.target)
        layout = self.candidate_manager.Layout()
        self.candidate_manager.write_release_receipt(layout, {
            "phase": "committed",
            "previous_dir": self.previous.name,
            "previous_commit": self.args.expected_revision,
            "target_dir": self.target.name,
            "target_commit": self.args.candidate_commit,
            "bootstrap_operation_id": self.operation_id,
            "runtime": {"version": wait_version(canonical_origin(),
                                                 self.args.candidate_commit, timeout=10),
                        "socket_inherited": True},
        })
        self.receipt = transition(self.receipt, "committed", completed_at=utc_now())
        return self.receipt

    def _bridge_is_active(self):
        return run(["systemctl", "is-active", BOOTSTRAP_BRIDGE_UNIT],
                   check=False).stdout.strip() == "active"

    def _hold_bridge_for_rollback(self):
        if not self.bridge_started and not self._bridge_is_active():
            self.bridge_started = False
            return
        self.bridge_started = True
        status = bridge_control("hold")
        if status.get("mode") != "hold":
            raise BootstrapFailure("bridge refused rollback HOLD")
        wait_bridge(lambda value: value.get("dialing") == 0 and value.get("forwarding") == 0,
                    10, "rollback hold barrier")

    def _prove_writers_and_socket_stopped(self):
        return {
            "primary": prove_writer_stopped(PRIMARY_SERVICE, self.args.main_exit_wait),
            "green": prove_writer_stopped(BOOTSTRAP_GREEN_SERVICE, self.args.main_exit_wait),
            "socket": prove_socket_stopped(PRIMARY_SOCKET, self.args.main_exit_wait),
        }

    def _restore_previous_writer(self):
        backups = self.primary_backups or (self.receipt or {}).get("primary_unit_backups") or {}
        if backups.get("socket"):
            restore_file_backup(backups["socket"])
        if backups.get("dropin"):
            restore_file_backup(backups["dropin"])
        run(["systemctl", "daemon-reload"], timeout=20)
        restore_current = (self.receipt or {}).get("restore_current") or (self.receipt or {}).get("current_before")
        if restore_current:
            atomic_symlink(CURRENT_LINK, Path(restore_current))
        else:
            atomic_symlink(CURRENT_LINK, self.previous)
        if self.target and Path(self.target).is_dir():
            atomic_symlink(PREVIOUS_LINK, self.target)
        validate_release_lease(self.args, minimum_remaining_seconds=60)
        run(["systemctl", "start", PRIMARY_SERVICE], timeout=40)

    def rollback(self, reason):
        errors = []
        phase = (self.receipt or {}).get("phase")
        writer_down = bool(self.old_stopped or phase in WRITER_DOWN_PHASES)
        started_previous = False
        try:
            try:
                self._hold_bridge_for_rollback()
            except BootstrapFailure as error:
                if writer_down:
                    errors.append(f"{type(error).__name__}: {error}")
                else:
                    raise
            if writer_down:
                proofs = self._prove_writers_and_socket_stopped()
                if self.receipt:
                    self.receipt = transition(self.receipt, phase or "old-stopped",
                                              rollback_stop_proof=proofs)
                self._restore_previous_writer()
                started_previous = True
                rollback_version = self.args.expected_revision
            else:
                if self.old_drain_started and self.bridge_uid is not None:
                    status, _payload = http_json_as_uid(
                        self.bridge_uid, self.bridge_gid, "POST",
                        canonical_origin() + "/admin/api/node/drain",
                        token=self.token, body={"action": "cancel"}, timeout=5)
                    if status != 200:
                        raise BootstrapFailure(f"old writer drain cancel failed: HTTP {status}")
                rollback_version = self.args.expected_live_version
            if self.bridge_started:
                bridge_control("upstream " + CANONICAL_ADDR)
                released = bridge_control("release")
                if released.get("mode") != "forward":
                    raise BootstrapFailure("bridge refused release to the restored writer")
                wait_version("http://" + BOOTSTRAP_BRIDGE_ADDR, rollback_version, timeout=30)
            elif not writer_down or started_previous:
                wait_version(canonical_origin(), rollback_version, timeout=30)
            if self.redirect_added and self.bridge_uid is not None:
                remove_local_redirect(self.operation_id, self.bridge_uid)
                self.redirect_added = False
            if self.nginx_switched and self.receipt and self.receipt.get("nginx_backup"):
                restore_nginx(self.receipt)
                self.nginx_switched = False
            if self.bridge_started and not errors:
                run(["systemctl", "stop", BOOTSTRAP_BRIDGE_UNIT], timeout=30)
                self.bridge_started = False
                remove_temp_units()
        except Exception as error:
            errors.append(f"{type(error).__name__}: {error}")
        if writer_down and not started_previous:
            if self.receipt:
                self.receipt = transition(self.receipt, "manual-required",
                                          rollback_reason=reason, rollback_errors=errors,
                                          hold_retained=True)
            raise BootstrapFailure("rollback left HOLD; previous writer was not started: "
                                   + "; ".join(errors))
        if self.receipt:
            self.receipt = transition(self.receipt, "rolled-back" if not errors else "manual-required",
                                      rollback_reason=reason, rollback_errors=errors)
        if errors:
            raise BootstrapFailure("rollback incomplete: " + "; ".join(errors))

    def migrate(self):
        package = self.preflight()
        try:
            controller_path = self.extract_candidate(package)
            self.start_bridge(controller_path)
            self.route_public_to_bridge()
            self.switch_old_to_green()
            self.install_canonical()
            self.switch_green_to_canonical()
            return self.commit()
        except BaseException as error:
            with contextlib.suppress(BaseException):
                self.rollback(f"{type(error).__name__}: {error}")
            raise


def lifecycle_lock():
    VERSIONS_ROOT.parent.mkdir(parents=True, exist_ok=True)
    path = VERSIONS_ROOT.parent / ".lifecycle.lock"
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_CLOEXEC | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        os.close(fd)
        raise BootstrapFailure("another socket bootstrap operation is active")
    return fd


def deployment_target_lock(unit, lock_dir=None):
    """Acquire ReleaseGate's shared per-deployment-unit flock.

    This intentionally mirrors agent-herder/deploy/fleet/release_gate.py:
    hash the exact unit string with SHA-256, name the file
    ``deploy-unit-<hex>.lock`` in the lease-state directory, open with
    O_CREAT|O_RDWR mode 0600, and take LOCK_EX|LOCK_NB. flock is released by
    closing the fd or by kernel process-exit cleanup.
    """
    if not isinstance(unit, str) or not unit:
        raise BootstrapFailure("deployment target unit is required for shared lock")
    lock_dir = Path(lock_dir) if lock_dir is not None else RELEASE_LEASE_STATE.parent
    target_key = hashlib.sha256(unit.encode()).hexdigest()
    path = lock_dir / ("deploy-unit-" + target_key + ".lock")
    try:
        fd = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
    except OSError as error:
        raise BootstrapFailure("cannot open shared deployment-target lock") from error
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as error:
        os.close(fd)
        raise BootstrapFailure("another release holds this deployment target") from error
    except BaseException:
        os.close(fd)
        raise
    return fd


@contextlib.contextmanager
def operation_locks(target_unit):
    """Hold lifecycle and Agent Herder target locks across commit/rollback."""
    lifecycle_fd = lifecycle_lock()
    target_fd = None
    try:
        target_fd = deployment_target_lock(target_unit)
        yield
    finally:
        if target_fd is not None:
            os.close(target_fd)
        os.close(lifecycle_fd)


def cmd_migrate(args):
    with operation_locks(args.release_lease_target_unit):
        previous = {sig: signal.getsignal(sig) for sig in (signal.SIGTERM, signal.SIGINT)}
        def interrupted(signum, _frame):
            raise BootstrapFailure(f"migration interrupted by {signal.Signals(signum).name}")
        for sig in previous:
            signal.signal(sig, interrupted)
        try:
            receipt = BootstrapController(args).migrate()
            print(json.dumps({"ok": True, "phase": receipt["phase"],
                              "candidate_commit": args.candidate_commit,
                              "operation_id": args.operation_id}, sort_keys=True))
            return 0
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)



def iptables_rule_present(operation_id, bridge_uid):
    prefix, exempt, redirect = iptables_rule_specs(operation_id, bridge_uid)
    return (run(prefix + ["-C"] + exempt, check=False).returncode == 0
            or run(prefix + ["-C"] + redirect, check=False).returncode == 0)


def recovery_controller(receipt, args):
    incoming = args
    recovery_lease = validate_release_lease(incoming, minimum_remaining_seconds=120)
    continued = assert_lease_continuity(receipt, recovery_lease)
    required = ("operation_id", "previous_dir", "expected_revision",
                "expected_live_version", "candidate_commit", "controller_commit",
                "package", "package_sha256", "public_origin", "nginx_upstream")
    missing = [key for key in required if key not in receipt]
    if missing:
        raise BootstrapFailure(f"bootstrap receipt lacks recovery fields: {missing}")
    args = argparse.Namespace(
        operation_id=receipt["operation_id"], previous_dir=receipt["previous_dir"],
        expected_revision=receipt["expected_revision"],
        expected_live_version=receipt["expected_live_version"],
        candidate_commit=receipt["candidate_commit"],
        controller_commit=receipt["controller_commit"],
        package=receipt["package"], package_sha256=receipt["package_sha256"],
        release_lease_id=incoming.release_lease_id,
        release_lease_fence=incoming.release_lease_fence,
        release_lease_owner_harness=incoming.release_lease_owner_harness,
        release_lease_owner_session=incoming.release_lease_owner_session,
        release_lease_host=incoming.release_lease_host,
        release_lease_project=incoming.release_lease_project,
        release_lease_target_unit=incoming.release_lease_target_unit,
        release_lease_fleet_dir=getattr(incoming, "release_lease_fleet_dir", ""),
        release_lease_proof=incoming.release_lease_proof,
        public_origin=receipt["public_origin"],
        nginx_upstream=Path(receipt["nginx_upstream"]),
        quiet_wait=float(receipt.get("quiet_wait", 300)),
        drain_wait=float(receipt.get("drain_wait", 5)),
        main_exit_wait=float(receipt.get("main_exit_wait", 130)))
    controller = BootstrapController(args)
    controller.receipt = transition(receipt, receipt.get("phase", "unknown"),
                                    recovery_lease=continued,
                                    original_release_lease=receipt.get("release_lease"),
                                    original_release_lease_id=receipt.get("release_lease_id"))
    target_dir = receipt.get("target_dir")
    if target_dir:
        target = VERSIONS_ROOT / target_dir
        if target.is_dir():
            controller.target = target
            controller.candidate_manager = load_module(
                target / "scripts/gptadmin_node_manage.py", "gptadmin_candidate_manager_recovery")
    controller.bridge_uid = receipt.get("bridge_uid")
    controller.bridge_gid = receipt.get("bridge_gid")
    controller.bridge_started = (run(["systemctl", "is-active", BOOTSTRAP_BRIDGE_UNIT],
                                     check=False).stdout.strip() == "active")
    controller.green_started = (run(["systemctl", "is-active", BOOTSTRAP_GREEN_SERVICE],
                                    check=False).stdout.strip() == "active")
    controller.canonical_started = (
        run(["systemctl", "is-active", PRIMARY_SOCKET], check=False).stdout.strip() == "active"
        and run(["systemctl", "is-active", PRIMARY_SERVICE], check=False).stdout.strip() == "active")
    controller.old_stopped = receipt.get("phase") in WRITER_DOWN_PHASES
    controller.old_drain_started = receipt.get("phase") in (
        {"old-quiesced", "cutover-prepared"} | set(WRITER_DOWN_PHASES))
    backups = receipt.get("primary_unit_backups")
    if isinstance(backups, dict):
        controller.primary_backups = backups
    controller.nginx_switched = bool(receipt.get("nginx_backup")
                                     and receipt.get("phase") not in {"committed", "rolled-back"})
    if controller.bridge_uid is not None:
        controller.redirect_added = iptables_rule_present(controller.operation_id,
                                                           int(controller.bridge_uid))
    return controller


def cmd_reconcile_boot(_args):
    """Start the bridge before nginx if a migration still depends on it.

    An already-active bridge is left untouched. Restart=no stays in force so a
    reboot cannot clear HOLD by recycling the process. A missing unit is not a
    failure: recovery must still be able to run with the bridge absent.
    """
    if os.geteuid() != 0:
        raise BootstrapFailure("root is required")
    receipt = read_receipt()
    phase = receipt.get("phase") if receipt else None
    needs_bridge = phase in BRIDGE_REQUIRED_PHASES or bool(receipt and receipt.get("nginx_backup")
                                                           and phase not in {"committed", "rolled-back"})
    if not needs_bridge:
        print(json.dumps({"ok": True, "action": "noop"}, sort_keys=True))
        return 0
    if run(["systemctl", "is-active", BOOTSTRAP_BRIDGE_UNIT], check=False).stdout.strip() == "active":
        print(json.dumps({"ok": True, "action": "already-active"}, sort_keys=True))
        return 0
    if not BOOTSTRAP_BRIDGE_UNIT_PATH.is_file():
        print(json.dumps({"ok": True, "action": "bridge-absent"}, sort_keys=True))
        return 0
    run(["systemctl", "start", BOOTSTRAP_BRIDGE_UNIT], timeout=30)
    print(json.dumps({"ok": True, "action": "started"}, sort_keys=True))
    return 0


def cmd_recover(args):
    if os.geteuid() != 0:
        raise BootstrapFailure("root is required")
    with operation_locks(args.release_lease_target_unit):
        receipt = read_receipt()
        if not receipt:
            raise BootstrapFailure("no bootstrap receipt exists")
        if receipt.get("phase") in ("committed", "rolled-back"):
            print(json.dumps({"ok": True, "phase": receipt["phase"], "changed": False},
                             sort_keys=True))
            return 0
        controller = recovery_controller(receipt, args)
        controller.rollback("explicit recovery rollback")
        final = read_receipt()
        print(json.dumps({"ok": True, "phase": final.get("phase"), "changed": True},
                         sort_keys=True))
        return 0


def cmd_status(_args):
    if os.geteuid() != 0:
        raise BootstrapFailure("root is required to read protected bootstrap state")
    receipt = read_receipt()
    result = {
        "receipt": receipt,
        "service": run(["systemctl", "is-active", PRIMARY_SERVICE], check=False).stdout.strip(),
        "socket": run(["systemctl", "is-active", PRIMARY_SOCKET], check=False).stdout.strip(),
        "current": str(CURRENT_LINK.resolve()) if CURRENT_LINK.exists() else None,
        "previous": str(PREVIOUS_LINK.resolve()) if PREVIOUS_LINK.exists() else None,
    }
    with contextlib.suppress(Exception):
        result["bridge"] = bridge_control("status", timeout=0.5)
    print(json.dumps(result, sort_keys=True, indent=2))
    return 0

def cmd_bridge_serve(args):
    if not valid_host_port(args.listen) or not valid_host_port(args.upstream):
        raise BootstrapFailure("bridge listen/upstream must be loopback host:port")
    if not (0 < args.per_connection_buffer <= args.max_buffer <= 16 * 1024 * 1024
            and 120 <= args.deadline <= 300 and 0 < args.max_connections <= 4096):
        raise BootstrapFailure("bridge limits are outside safe bounds")
    bridge = Bridge(args.listen, args.upstream, args.control, args.max_buffer,
                    args.deadline, args.max_connections, args.per_connection_buffer)
    signal.signal(signal.SIGTERM, bridge.close)
    signal.signal(signal.SIGINT, bridge.close)
    bridge.run()
    return 0


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    bridge = sub.add_parser("bridge-serve", help=argparse.SUPPRESS)
    bridge.add_argument("--listen", default=BOOTSTRAP_BRIDGE_ADDR)
    bridge.add_argument("--upstream", default=CANONICAL_ADDR)
    bridge.add_argument("--control", default=str(BOOTSTRAP_CONTROL))
    bridge.add_argument("--max-buffer", type=int, default=8 * 1024 * 1024)
    bridge.add_argument("--deadline", type=float, default=180)
    bridge.add_argument("--max-connections", type=int, default=512)
    bridge.add_argument("--per-connection-buffer", type=int, default=512 * 1024)
    bridge.set_defaults(func=cmd_bridge_serve)

    migrate = sub.add_parser("migrate", help="perform the one-time legacy-to-socket migration")
    migrate.add_argument("--package", required=True)
    migrate.add_argument("--package-sha256", required=True)
    migrate.add_argument("--expected-revision", required=True)
    migrate.add_argument("--expected-live-version", required=True)
    migrate.add_argument("--candidate-commit", required=True)
    migrate.add_argument("--controller-commit", required=True)
    migrate.add_argument("--previous-dir", required=True)
    migrate.add_argument("--release-lease-id", required=True)
    migrate.add_argument("--release-lease-proof", type=Path, required=True)
    migrate.add_argument("--release-lease-fence", type=int, required=True)
    migrate.add_argument("--release-lease-owner-harness", required=True)
    migrate.add_argument("--release-lease-owner-session", required=True)
    migrate.add_argument("--release-lease-host", default="admin-node-100")
    migrate.add_argument("--release-lease-project", default=DEFAULT_RELEASE_PROJECT)
    migrate.add_argument("--release-lease-target-unit", default=PRIMARY_SERVICE)
    migrate.add_argument("--release-lease-fleet-dir", default="")
    migrate.add_argument("--operation-id", required=True)
    migrate.add_argument("--public-origin", default="https://became.bezrabotnyi.com")
    migrate.add_argument("--nginx-upstream", type=Path, default=DEFAULT_NGINX_UPSTREAM)
    migrate.add_argument("--quiet-wait", type=float, default=300)
    migrate.add_argument("--drain-wait", type=float, default=5)
    migrate.add_argument("--main-exit-wait", type=float, default=130)
    migrate.set_defaults(func=cmd_migrate)

    recover = sub.add_parser("recover", help="roll back an incomplete bootstrap receipt")
    recover.add_argument("--release-lease-id", required=True)
    recover.add_argument("--release-lease-proof", type=Path, required=True)
    recover.add_argument("--release-lease-fence", type=int, required=True)
    recover.add_argument("--release-lease-owner-harness", required=True)
    recover.add_argument("--release-lease-owner-session", required=True)
    recover.add_argument("--release-lease-host", default="admin-node-100")
    recover.add_argument("--release-lease-project", default=DEFAULT_RELEASE_PROJECT)
    recover.add_argument("--release-lease-target-unit", default=PRIMARY_SERVICE)
    recover.add_argument("--release-lease-fleet-dir", default="")
    recover.set_defaults(func=cmd_recover)

    reconcile = sub.add_parser("reconcile-boot", help="keep the bridge ahead of nginx across reboot")
    reconcile.set_defaults(func=cmd_reconcile_boot)

    status = sub.add_parser("status", help="read bootstrap and production state")
    status.set_defaults(func=cmd_status)
    return parser


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    if getattr(args, "operation_id", None) and not re.fullmatch(r"[A-Za-z0-9_.:-]{8,128}", args.operation_id):
        parser.error("operation-id must contain 8..128 safe characters")
    try:
        return args.func(args)
    except (BootstrapFailure, OSError, ValueError) as error:
        print(f"gptadmin-socket-bootstrap: {type(error).__name__}: {error}", file=sys.stderr)
        return 4
    except KeyboardInterrupt:
        print("gptadmin-socket-bootstrap: interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
