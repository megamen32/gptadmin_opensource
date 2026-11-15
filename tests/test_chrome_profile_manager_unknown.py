import json
import os
from pathlib import Path
import secrets
import socket
import struct
import subprocess
import sys
import time

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from chrome_profile_manager_bridge import host


@pytest.fixture
def bridge():
    secret = secrets.token_hex(32)
    path = host.socket_path(secret)
    code = ('from chrome_profile_manager_bridge import host; '
            'host.RESPONSE_TIMEOUT = 0.3; host.serve()')
    proc = subprocess.Popen([sys.executable, '-c', code], cwd=ROOT,
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    host.send(proc.stdin, {'type': 'hello', 'secret': secret})
    deadline = time.monotonic() + 3
    while not os.path.exists(path) and time.monotonic() < deadline:
        time.sleep(0.01)
    assert os.path.exists(path)
    try:
        yield proc, path, secret
    finally:
        if proc.poll() is None:
            proc.terminate()
        proc.wait(timeout=3)
        if not proc.stdin.closed:
            proc.stdin.close()
        proc.stdout.close()
        proc.stderr.close()
        # This lock belongs to the unique test capability and the host has exited.
        Path(path + '.lock').unlink(missing_ok=True)


def request(bridge):
    proc, path, secret = bridge
    conn = socket.socket(socket.AF_UNIX)
    conn.settimeout(2)
    conn.connect(path)
    io = conn.makefile('rwb')
    host.send(io, {'secret': secret, 'method': 'list'})
    assert host.receive(proc.stdout)['method'] == 'list'
    return conn, io


@pytest.mark.parametrize('partial', [b'', b'\x10', struct.pack('=I', 16) + b'{'])
def test_timeout_is_unknown_and_host_removes_endpoint(bridge, partial):
    proc, path, _ = bridge
    conn, io = request(bridge)
    try:
        proc.stdin.write(partial)
        proc.stdin.flush()
        result = host.receive(io)
        assert result['outcome'] == 'unknown'
        proc.wait(timeout=2)
        assert not os.path.exists(path)
    finally:
        io.close()
        conn.close()


@pytest.mark.parametrize('payload', [b'not json', json.dumps({'id': 2}).encode()])
def test_invalid_response_is_unknown_and_host_stops(bridge, payload):
    proc, path, _ = bridge
    conn, io = request(bridge)
    try:
        proc.stdin.write(struct.pack('=I', len(payload)) + payload)
        proc.stdin.flush()
        assert host.receive(io)['outcome'] == 'unknown'
        proc.wait(timeout=2)
        assert not os.path.exists(path)
    finally:
        io.close()
        conn.close()


def test_closed_chrome_input_is_unknown(bridge):
    proc, path, _ = bridge
    conn, io = request(bridge)
    proc.stdin.close()
    assert host.receive(io)['outcome'] == 'unknown'
    proc.wait(timeout=2)
    assert not os.path.exists(path)
    io.close()
    conn.close()


def test_rejected_request_before_dispatch_keeps_host(bridge):
    proc, path, secret = bridge
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(2)
        conn.connect(path)
        with conn.makefile('rwb') as io:
            host.send(io, {'secret': secret, 'method': 'invalid'})
            result = host.receive(io)
            assert result == {'error': 'unsupported method'}
    assert proc.poll() is None
    conn, io = request(bridge)
    host.send(proc.stdin, {'id': 1, 'result': []})
    assert host.receive(io)['result'] == []
    io.close()
    conn.close()


def test_fragmented_valid_response_and_next_command(bridge):
    proc, _, _ = bridge
    conn, io = request(bridge)
    payload = json.dumps({'id': 1, 'result': ['ok']}).encode()
    frame = struct.pack('=I', len(payload)) + payload
    for part in (frame[:1], frame[1:4], frame[4:7], frame[7:]):
        proc.stdin.write(part)
        proc.stdin.flush()
        time.sleep(0.02)
    assert host.receive(io) == {'id': 1, 'result': ['ok']}
    io.close()
    conn.close()
    conn, io = request(bridge)
    host.send(proc.stdin, {'id': 1, 'result': []})
    assert host.receive(io)['result'] == []
    io.close()
    conn.close()
