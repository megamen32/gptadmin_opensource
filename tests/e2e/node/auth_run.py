#!/usr/bin/env python3
"""Real managed-bearer/OAuth continuity with separate node stores and processes."""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve()
    project = Path(__file__).resolve().parents[3]
    root = Path(tempfile.mkdtemp(prefix='auth-canary-', dir=project / '.tmp'))
    signer, password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    issuer, resource = 'https://node-auth.example', 'https://node-auth.example/mcp'
    opener = urllib.request.build_opener(NoRedirect())
    nodes = {}
    with socket.socket() as a, socket.socket() as b:
        for name, sock in [('nodeA', a), ('nodeB', b)]:
            sock.bind(('127.0.0.1', 0))
            directory = root / name
            directory.mkdir()
            port = sock.getsockname()[1]
            nodes[name] = dict(directory=directory, origin=f'http://127.0.0.1:{port}', owner=secrets.token_urlsafe(32))
            node = nodes[name]
            node['env'] = dict(os.environ, GPTADMIN_CONFIG_DIR=str(directory), GPTADMIN_ROOT=str(directory),
                GPTADMIN_ENV_FILE=str(directory / 'absent.env'), GPTADMIN_HUB_HOST='127.0.0.1', GPTADMIN_HUB_PORT=str(port),
                CTL_TOKEN=node['owner'], SHELL_TOKEN=secrets.token_urlsafe(32), OAUTH_CLIENT_SECRET=signer,
                MCP_RELAY_AGENT_TOKEN=secrets.token_urlsafe(32),
                ADMIN_PASSWORD=password, PUBLIC_ORIGIN=issuer, MCP_RESOURCE=resource,
                GPTADMIN_RELAX_AUTH_CHECKS='0', GPTADMIN_AUTH_MODE='writer' if name == 'nodeA' else 'reader',
                GPTADMIN_AUTH_SOURCE_ID='', GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES=str(256 << 10), GPTADMIN_NODE_PEERS='{}',
                SHELL_NAME=name, SHELL_IDENTITY_DIR=str(directory), SHELL_SPOOL_DIR=str(directory / 'spool'),
                SHELL_OUTBOX_DIR=str(directory / 'spool/outbox'), SHELL_DEFAULT_CWD=str(directory), SHELL_DEFAULT_HOME=str(directory),
                SHELLMCP_MCP_CONFIG=str(directory / 'no-child-mcp.json'), HUB_URL='http://127.0.0.1:1', SHELLMCP_SELF_REPAIR_DISABLE='1')

    def request(name, path, body=None, token=None, method=None, form=None, basic=None):
        headers = {'Content-Type': 'application/json'}
        data = None if body is None else json.dumps(body).encode()
        if form is not None:
            data = urllib.parse.urlencode(form).encode()
            headers['Content-Type'] = 'application/x-www-form-urlencoded'
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if basic:
            headers['Authorization'] = 'Basic ' + base64.b64encode((':'.join(basic)).encode()).decode()
        req = urllib.request.Request(nodes[name]['origin'] + path, data=data, headers=headers, method=method)
        try:
            response = opener.open(req, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            result = json.loads(raw) if raw and response.status != 302 else None
            return response.status, result, response.headers

    def start(name):
        node = nodes[name]
        node['log'] = (node['directory'] / 'runtime.log').open('a')
        node['proc'] = subprocess.Popen([str(binary)], cwd=node['directory'], env=node['env'], stdout=node['log'], stderr=node['log'])
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            assert node['proc'].poll() is None, f'{name} exited; see {node["directory"] / "runtime.log"}'
            try:
                if request(name, '/healthz')[0] == 200:
                    return
            except (urllib.error.URLError, TimeoutError):
                pass
            time.sleep(.1)
        raise AssertionError(f'{name} not ready')

    def stop(name):
        node = nodes[name]
        proc = node.get('proc')
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                code = proc.wait(timeout=12)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise AssertionError(f'{name} did not stop')
            assert code == 0, f'{name} exit {code}'
        if node.get('log'):
            node['log'].close()

    def tool(token, name, arguments):
        status, response, _ = request('nodeB', '/mcp', {'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
            'params': {'name': name, 'arguments': arguments}}, token)
        assert status == 200 and 'error' not in response, f'MCP {name} failed HTTP{status}'
        result = response['result']
        assert not result.get('isError'), f'MCP {name} returned tool error'
        return result['structuredContent']

    def execute(token, label):
        status, response, _ = request('nodeB', '/mcp', {'jsonrpc': '2.0', 'id': 0, 'method': 'initialize',
            'params': {'protocolVersion': '2025-03-26', 'capabilities': {}, 'clientInfo': {'name': 'auth-continuity-canary', 'version': '1'}}}, token)
        assert status == 200 and 'result' in response, f'initialize failed HTTP{status}'
        command = f'printf "%s\\n" {label} >> {label}.txt; pwd; cat {label}.txt'
        result = tool(token, 'execute', {'target': 'shell:nodeB', 'tool': 'shell_exec', 'args': {'cmd': command, 'timeout': 10},
            'background': True, 'idempotency_key': label})
        job_id = result.get('job_id') or result.get('task_id')
        assert job_id, f'{label} did not create a job'
        deadline = time.monotonic() + 20
        while result.get('status') != 'completed' and time.monotonic() < deadline:
            time.sleep(.1)
            result = tool(token, 'job', {'job_id': job_id})
        assert result.get('status') == 'completed', f'{label} command did not finish'
        assert (nodes['nodeB']['directory'] / (label + '.txt')).read_text().splitlines() == [label]
        assert not (nodes['nodeA']['directory'] / (label + '.txt')).exists()
        assert str(nodes['nodeB']['directory']) in json.dumps(result)

    def sync():
        status, snapshot, _ = request('nodeA', '/admin/api/auth-snapshot/export', {}, nodes['nodeA']['owner'])
        assert status == 200, f'export failed HTTP{status}'
        encoded = json.dumps(snapshot).encode()
        assert len(encoded) <= 256 << 10
        assert all(node['owner'].encode() not in encoded for node in nodes.values()), 'snapshot contains CTL credential'
        status, _, _ = request('nodeB', '/admin/api/auth-snapshot/apply', snapshot, nodes['nodeB']['owner'])
        assert status == 200, f'apply failed HTTP{status}'
        return snapshot, len(encoded)

    try:
        start('nodeA')
        identity_a = json.loads((nodes['nodeA']['directory'] / 'shellmcp_identity.json').read_text())['server_id']
        nodes['nodeB']['env']['GPTADMIN_AUTH_SOURCE_ID'] = identity_a
        start('nodeB')
        assert request('nodeB', '/mcp')[0] == 503, 'unseeded reader accepted auth'
        assert request('nodeB', '/mcp-relay/pollution')[0] == 503, 'poll exemption crossed path boundary'
        status, managed, _ = request('nodeA', '/admin/api/mcp/issue-token',
            {'client_id': 'ordinary-canary', 'role': 'client', 'access_mode': 'full', 'ttl_days': 7}, nodes['nodeA']['owner'])
        assert status == 200 and managed['role'] == 'client', 'ordinary managed issuance failed'
        # A real authenticated idle relay poll must not retain the auth gate.
        # Export waiting behind it used to also block subsequent ordinary MCP.
        poll_path = '/mcp-relay/poll/idle-auth-canary?timeout=8'
        assert request('nodeA', poll_path, token='incorrect-relay-token')[0] == 401
        address = urllib.parse.urlparse(nodes['nodeA']['origin'])
        with ThreadPoolExecutor(max_workers=2) as pool:
            poll = socket.create_connection((address.hostname, address.port), timeout=2)
            try:
                poll.sendall((f'GET {poll_path} HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer '
                    + nodes['nodeA']['env']['MCP_RELAY_AGENT_TOKEN'] + '\r\n\r\n').encode())
                poll.settimeout(.2)
                try:
                    poll.recv(1, socket.MSG_PEEK)
                    raise AssertionError('idle relay poll returned before export')
                except socket.timeout:
                    pass
                began = time.monotonic()
                export_pending = pool.submit(request, 'nodeA', '/admin/api/auth-snapshot/export', {}, nodes['nodeA']['owner'])
                time.sleep(.05)
                admission_pending = pool.submit(request, 'nodeA', '/mcp', {'jsonrpc': '2.0', 'id': 2,
                    'method': 'initialize', 'params': {'protocolVersion': '2025-03-26', 'capabilities': {},
                    'clientInfo': {'name': 'poll-admission-canary', 'version': '1'}}}, managed['access_token'])
                assert export_pending.result(timeout=2)[0] == 200
                admitted = admission_pending.result(timeout=2)
                assert admitted[0] == 200 and 'result' in admitted[1]
                poll_export_seconds = time.monotonic() - began
                assert poll_export_seconds < 2
                try:
                    poll.recv(1, socket.MSG_PEEK)
                    raise AssertionError('idle relay poll ended before parallel checks')
                except socket.timeout:
                    pass
            finally:
                poll.close()
        # Slow anonymous input must not hold the admission gate or queue an
        # exclusive export ahead of every authenticated request.
        address = urllib.parse.urlparse(nodes['nodeA']['origin'])
        with ThreadPoolExecutor(max_workers=2) as pool:
            slow = socket.create_connection((address.hostname, address.port), timeout=2)
            try:
                slow.sendall(b'POST /register HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 9999\r\n\r\n{')
                time.sleep(.1)
                export_pending = pool.submit(request, 'nodeA', '/admin/api/auth-snapshot/export', {}, nodes['nodeA']['owner'])
                time.sleep(.05)
                admission_pending = pool.submit(request, 'nodeA', '/mcp', None, managed['access_token'])
                assert export_pending.result(timeout=2)[0] == 200
                assert admission_pending.result(timeout=2)[0] == 200
            finally:
                slow.close()
        callback = 'http://127.0.0.1:49173/callback/auth-continuity'
        status, registration, _ = request('nodeA', '/register', {'redirect_uris': [callback], 'client_name': 'auth-continuity-canary'})
        assert status == 201, f'register failed HTTP{status}'
        basic = (registration['client_id'], registration['client_secret'])
        verifier = secrets.token_urlsafe(48)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip('=')
        status, _, headers = request('nodeA', '/oauth/authorize', form={'client_id': basic[0], 'redirect_uri': callback,
            'resource': resource, 'scope': 'gptadmin.read gptadmin.exec offline_access', 'password': password,
            'code_challenge': challenge, 'code_challenge_method': 'S256'})
        assert status == 302, f'authorize failed HTTP{status}'
        code = urllib.parse.parse_qs(urllib.parse.urlparse(headers['Location']).query)['code'][0]
        status, oauth, _ = request('nodeA', '/oauth/token', form={'grant_type': 'authorization_code', 'code': code,
            'redirect_uri': callback, 'resource': resource, 'code_verifier': verifier}, basic=basic)
        assert status == 200 and oauth.get('refresh_token'), f'exchange failed HTTP{status}'
        first, size = sync()
        for credential, label in [(managed['access_token'], 'managed-before'), (oauth['access_token'], 'oauth-before')]:
            execute(credential, label)
        assert request('nodeB', '/admin/api/auth-snapshot/export', {}, managed['access_token'])[0] == 403
        assert request('nodeB', '/register', {'redirect_uris': [callback]})[0] >= 400
        assert request('nodeB', '/admin/api/mcp/issue-token', {'client_id': 'forbidden'}, nodes['nodeB']['owner'])[0] >= 400
        refresh_form = {'grant_type': 'refresh_token', 'refresh_token': oauth['refresh_token'], 'resource': resource}
        assert request('nodeB', '/oauth/token', form=refresh_form, basic=basic)[0] >= 400
        stop('nodeB'); start('nodeB')
        execute(managed['access_token'], 'managed-restart')
        execute(oauth['access_token'], 'oauth-restart')
        # An accepted synchronous command must not hold the auth generation gate.
        started = nodes['nodeB']['directory'] / 'inflight.started'
        finished = nodes['nodeB']['directory'] / 'inflight.finished'
        with ThreadPoolExecutor(max_workers=1) as pool:
            running = pool.submit(tool, managed['access_token'], 'execute', {
                'target': 'shell:nodeB', 'tool': 'shell_exec',
                'args': {'cmd': 'printf started > inflight.started; sleep 5; printf done > inflight.finished', 'timeout': 10},
                'background': False, 'idempotency_key': 'inflight-revocation'})
            deadline = time.monotonic() + 10
            while not started.exists() and not running.done() and time.monotonic() < deadline:
                time.sleep(.02)
            assert started.exists() and not finished.exists(), 'long command did not start'
            assert request('nodeA', '/admin/api/clients/' + managed['token_id'], token=nodes['nodeA']['owner'], method='DELETE')[0] == 200
            admission_start = time.monotonic()
            second, size = sync()
            assert request('nodeB', '/mcp', token=managed['access_token'])[0] == 401
            admission_seconds = time.monotonic() - admission_start
            assert admission_seconds < 2 and not finished.exists() and not running.done(), 'auth apply/admission waited for active command'
            result = running.result(timeout=12)
            assert result.get('status') == 'completed' and finished.read_text() == 'done'

        assert second['generation'] > first['generation']
        assert request('nodeB', '/mcp', token=managed['access_token'])[0] == 401
        assert request('nodeB', '/admin/api/auth-snapshot/apply', first, nodes['nodeB']['owner'])[0] == 409
        stop('nodeB'); start('nodeB')
        assert request('nodeB', '/mcp', token=managed['access_token'])[0] == 401
        execute(oauth['access_token'], 'oauth-after-revoke-sync')
        stop('nodeA')
        stop('nodeB')
        nodes['nodeB']['env']['GPTADMIN_AUTH_MODE'] = 'writer'
        start('nodeB')
        status, rotated, _ = request('nodeB', '/oauth/token', form=refresh_form, basic=basic)
        assert status == 200 and rotated.get('refresh_token') != oauth['refresh_token'], f'Basic refresh failed HTTP{status}'
        execute(rotated['access_token'], 'refreshed-after-handoff')
        status, replay, _ = request('nodeB', '/oauth/token', form=refresh_form, basic=basic)
        assert status == 400 and replay['error'] == 'invalid_grant', 'old refresh credential replayed'
        stop('nodeB'); start('nodeB')
        assert request('nodeB', '/oauth/token', form=refresh_form, basic=basic)[0] == 400
        execute(rotated['access_token'], 'refreshed-restart')
        assert request('nodeB', '/mcp', token=managed['access_token'])[0] == 401
        slots = list((nodes['nodeB']['directory'] / 'auth-continuity').glob('slot-*'))
        assert len(slots) <= 2
        print(json.dumps({'ok': True, 'ordinary_role': 'client', 'oauth_pkce_basic_offline_access': True,
            'reader_restart': True, 'idle_relay_poll_export_admission_seconds': round(poll_export_seconds, 3),
            'incomplete_anonymous_body_isolated': True, 'inflight_revocation_seconds': round(admission_seconds, 3), 'managed_revocation_after_sync_restart': True,
            'explicit_writer_handoff': True, 'basic_refresh_after_handoff': True, 'refresh_single_use_after_restart': True,
            'snapshot_bytes': size, 'generation_slots': len(slots), 'evidence_dir': str(root)}))
    finally:
        for name in nodes:
            stop(name)


if __name__ == '__main__':
    main()
