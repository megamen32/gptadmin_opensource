#!/usr/bin/env python3
"""Two real Nodes; auth reader admission on a real 128 MiB loop-ext4 filesystem.

Run with sudo. Re-executes in a private mount namespace; no production paths.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('--inside', action='store_true', help=argparse.SUPPRESS)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('run with sudo for an isolated real loop filesystem')
    if not args.inside:
        os.execvp('unshare', ['unshare', '--mount', '--propagation', 'private', '--', sys.executable,
                            str(Path(__file__).resolve()), str(args.binary.resolve()), '--inside'])
    project = Path(__file__).resolve().parents[3]
    root = Path(tempfile.mkdtemp(prefix='auth-budget-canary-', dir=project / '.tmp'))
    image, mount = root / 'reader.ext4', root / 'disk'
    mount.mkdir()
    with image.open('wb') as stream:
        stream.truncate(128 << 20)
    subprocess.run(['mkfs.ext4', '-q', '-F', '-m', '0', str(image)], check=True, capture_output=True)
    mounted = False
    nodes = {}
    report = {'evidence_dir': str(root), 'image_bytes': 128 << 20}
    try:
        subprocess.run(['mount', '-o', 'loop', str(image), str(mount)], check=True, capture_output=True)
        mounted = True
        report['filesystem'] = subprocess.check_output(['findmnt', '-n', '-o', 'FSTYPE', '--target', str(mount)], text=True).strip()
        assert report['filesystem'] == 'ext4', report
        shared = secrets.token_urlsafe(32)
        with socket.socket() as a, socket.socket() as b:
            for name, sock in [('nodeA', a), ('nodeB', b)]:
                sock.bind(('127.0.0.1', 0))
                directory = (root if name == 'nodeA' else mount) / name
                directory.mkdir()
                port = sock.getsockname()[1]
                node = nodes[name] = {'directory': directory, 'origin': f'http://127.0.0.1:{port}',
                                      'owner': secrets.token_urlsafe(32)}
                # Do not inherit a production environment/config. All credentials are fixtures.
                node['env'] = dict(PATH=os.environ['PATH'], HOME=str(directory), TMPDIR=str(directory),
                    GPTADMIN_CONFIG_DIR=str(directory), GPTADMIN_ROOT=str(directory),
                    GPTADMIN_ENV_FILE=str(directory / 'absent.env'), GPTADMIN_HUB_HOST='127.0.0.1',
                    GPTADMIN_HUB_PORT=str(port), CTL_TOKEN=node['owner'], SHELL_TOKEN=secrets.token_urlsafe(32),
                    OAUTH_CLIENT_SECRET=shared, MCP_RELAY_AGENT_TOKEN=secrets.token_urlsafe(32),
                    PUBLIC_ORIGIN='https://auth-budget.example', MCP_RESOURCE='https://auth-budget.example/mcp',
                    GPTADMIN_AUTH_MODE='writer' if name == 'nodeA' else 'reader', GPTADMIN_NODE_PEERS='{}',
                    SHELL_NAME=name, SHELL_IDENTITY_DIR=str(directory), SHELL_SPOOL_DIR=str(directory / 'spool'),
                    SHELL_OUTBOX_DIR=str(directory / 'spool/outbox'), SHELL_DEFAULT_CWD=str(directory),
                    SHELL_DEFAULT_HOME=str(directory), SHELLMCP_DEFAULT_USER='root', SHELLMCP_MCP_CONFIG=str(directory / 'none.json'),
                    HUB_URL='http://127.0.0.1:1', SHELLMCP_SELF_REPAIR_DISABLE='1')

        def request(name, path, body=None, token=None, method=None):
            headers = {'Content-Type': 'application/json'}
            if token:
                headers['Authorization'] = 'Bearer ' + token
            req = urllib.request.Request(nodes[name]['origin'] + path,
                data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
            try:
                response = urllib.request.urlopen(req, timeout=15)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                raw = response.read()
                return response.status, json.loads(raw) if raw else {}

        def start(name):
            node = nodes[name]
            node['log'] = (root / f'{name}.log').open('ab')
            node['proc'] = subprocess.Popen([str(args.binary.resolve())], env=node['env'], cwd=node['directory'],
                                           stdout=node['log'], stderr=subprocess.STDOUT)
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                assert node['proc'].poll() is None, f'{name} exited; see runtime log'
                try:
                    if request(name, '/healthz')[0] == 200:
                        return
                except (urllib.error.URLError, TimeoutError):
                    pass
                time.sleep(.1)
            raise AssertionError(f'{name} did not start')

        def stop(name):
            node = nodes[name]
            if node.get('proc') and node['proc'].poll() is None:
                node['proc'].terminate()
                try:
                    code = node['proc'].wait(timeout=15)
                except subprocess.TimeoutExpired:
                    node['proc'].kill(); node['proc'].wait()
                    raise AssertionError(f'{name} shutdown timeout')
                assert code == 0, f'{name} shutdown {code}'
            if node.get('log'):
                node['log'].close()

        def initialize(token):
            return request('nodeB', '/mcp', {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
                'params': {'protocolVersion': '2025-03-26', 'capabilities': {},
                           'clientInfo': {'name': 'auth-budget-canary', 'version': '1'}}}, token)[0]

        def tool(token, name, arguments):
            status, body = request('nodeB', '/mcp', {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call',
                'params': {'name': name, 'arguments': arguments}}, token)
            assert status == 200 and 'result' in body and not body['result'].get('isError'), f'tool {name} HTTP{status}'
            return body['result']['structuredContent']

        def execute(token, marker):
            assert initialize(token) == 200
            result = tool(token, 'execute', {'target': 'shell:nodeB', 'tool': 'shell_exec',
                'args': {'cmd': 'printf ' + marker, 'timeout': 10}, 'background': True, 'idempotency_key': marker})
            job = result.get('job_id') or result.get('task_id')
            deadline = time.monotonic() + 15
            while result.get('status') != 'completed' and time.monotonic() < deadline:
                time.sleep(.1)
                result = tool(token, 'job', {'id': job})
            assert result.get('status') == 'completed' and result.get('stdout') == marker, json.dumps(result)
            return job, result

        def export():
            status, snapshot = request('nodeA', '/admin/api/auth-snapshot/export', {}, nodes['nodeA']['owner'])
            assert status == 200, f'writer export HTTP{status}'
            return snapshot

        def apply(snapshot):
            return request('nodeB', '/admin/api/auth-snapshot/apply', snapshot, nodes['nodeB']['owner'])[0]

        def auth_hashes():
            base = nodes['nodeB']['directory'] / 'auth-continuity'
            return {str(path.relative_to(base)): hashlib.sha256(path.read_bytes()).hexdigest()
                    for path in base.rglob('*.json')}

        start('nodeA')
        writer = json.loads((nodes['nodeA']['directory'] / 'shellmcp_identity.json').read_text())['server_id']
        nodes['nodeB']['env']['GPTADMIN_AUTH_SOURCE_ID'] = writer
        start('nodeB')
        credentials = []
        for label in ('revoke-me', 'survivor'):
            status, credential = request('nodeA', '/admin/api/mcp/issue-token',
                {'client_id': label, 'role': 'client', 'access_mode': 'full', 'ttl_days': 7}, nodes['nodeA']['owner'])
            assert status == 200 and credential['role'] == 'client'
            credentials.append(credential)
        revoked, survivor = credentials
        first = export()
        assert apply(first) == 200
        job, receipt = execute(revoked['access_token'], 'before-fill-' + secrets.token_hex(8))
        baseline = auth_hashes()
        # Generate an actual revocation on A; it must not leak into B on rejected apply.
        assert request('nodeA', '/admin/api/clients/' + revoked['token_id'],
                       token=nodes['nodeA']['owner'], method='DELETE')[0] == 200
        next_snapshot = export()
        filler = mount / 'filler'
        fs = os.statvfs(mount)
        target_available = 512 << 10
        allocation = (fs.f_bavail * fs.f_frsize - target_available) // fs.f_frsize * fs.f_frsize
        with filler.open('wb') as stream:
            os.posix_fallocate(stream.fileno(), 0, allocation)
            os.fsync(stream.fileno())
        assert filler.stat().st_blocks * 512 >= allocation, 'filler was sparse, not allocated'
        fs = os.statvfs(mount)
        report.update(allocated_filler_bytes=allocation, available_before_apply=fs.f_bavail * fs.f_frsize,
                      reserve_bytes=1 << 20, initial_generation=first['generation'], job_id=job)
        assert report['available_before_apply'] < 1 << 20
        status = apply(next_snapshot)
        report['low_space_apply_status'] = status
        assert status == 507, f'low-space apply HTTP{status}, expected507'
        assert auth_hashes() == baseline, 'rejected apply changed auth files'
        manifest = nodes['nodeB']['directory'] / 'auth-continuity/state.json'
        assert not json.loads(manifest.read_text())['pending']
        assert initialize(revoked['access_token']) == 200
        assert tool(revoked['access_token'], 'job', {'id': job}) == receipt
        stop('nodeB'); start('nodeB')
        assert auth_hashes() == baseline and not json.loads(manifest.read_text())['pending']
        assert initialize(revoked['access_token']) == 200
        assert tool(revoked['access_token'], 'job', {'id': job}) == receipt
        report['low_space_restart_old_auth_and_receipt'] = True
        filler.unlink()
        newer = export()
        assert newer['generation'] > next_snapshot['generation'] and apply(newer) == 200
        assert initialize(revoked['access_token']) == 401
        execute(survivor['access_token'], 'after-free-' + secrets.token_hex(8))
        stop('nodeB'); start('nodeB')
        assert initialize(revoked['access_token']) == 401
        assert initialize(survivor['access_token']) == 200
        assert tool(survivor['access_token'], 'job', {'id': job}) == receipt
        report.update(ok=True, final_generation=newer['generation'], revocation_after_free_and_restart=True,
                      slots=len(list(manifest.parent.glob('slot-*'))))
        assert report['slots'] <= 2
    except BaseException as error:
        report.update(ok=False, failure=type(error).__name__)
        raise
    finally:
        for name, node in nodes.items():
            proc = node.get('proc')
            if proc and proc.poll() is None:
                proc.terminate()
                try: proc.wait(timeout=15)
                except subprocess.TimeoutExpired: proc.kill(); proc.wait()
            if node.get('log'): node['log'].close()
        if mounted:
            # Both process groups are joined; detach only this private canary mount.
            subprocess.run(['umount', str(mount)], check=True)
        (root / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report), flush=True)


if __name__ == '__main__':
    main()
