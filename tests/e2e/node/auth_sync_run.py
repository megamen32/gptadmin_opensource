#!/usr/bin/env python3
"""Real periodic snapshot sync, revocation, outage and reader restart canary."""
import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import select
import ssl
import stat
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    args = parser.parse_args()
    project = Path(__file__).resolve().parents[3]
    root = Path(tempfile.mkdtemp(prefix='auth-sync-canary-', dir=project / '.tmp'))
    script = project / 'scripts/gptadmin_auth_sync.py'
    signer = secrets.token_urlsafe(32)
    nodes = {}
    for name in ('nodeA', 'nodeB'):
        directory = root / name
        directory.mkdir()
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        owner = secrets.token_urlsafe(32)
        env = dict(os.environ, GPTADMIN_CONFIG_DIR=str(directory), GPTADMIN_ROOT=str(directory),
            GPTADMIN_ENV_FILE=str(directory / 'absent.env'), GPTADMIN_HUB_HOST='127.0.0.1',
            GPTADMIN_HUB_PORT=str(port), CTL_TOKEN=owner, SHELL_TOKEN=secrets.token_urlsafe(32),
            OAUTH_CLIENT_SECRET=signer, PUBLIC_ORIGIN='https://auth-sync.example',
            MCP_RESOURCE='https://auth-sync.example/mcp', GPTADMIN_AUTH_SOURCE_ID='',
            GPTADMIN_AUTH_MODE='writer' if name == 'nodeA' else 'reader',
            GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES=str(256 << 10), GPTADMIN_NODE_PEERS='{}',
            SHELL_NAME=name, SHELL_IDENTITY_DIR=str(directory), SHELL_SPOOL_DIR=str(directory / 'spool'),
            SHELL_OUTBOX_DIR=str(directory / 'spool/outbox'), SHELL_DEFAULT_CWD=str(directory),
            SHELL_DEFAULT_HOME=str(directory), SHELLMCP_MCP_CONFIG=str(directory / 'no-mcp.json'),
            SHELLMCP_SELF_REPAIR_DISABLE='1', HUB_URL='http://127.0.0.1:1', GPTADMIN_RELAX_AUTH_CHECKS='0')
        nodes[name] = {'dir': directory, 'env': env, 'owner': owner, 'url': f'http://127.0.0.1:{port}'}

    def request(name, path, body=None, token=None, method=None):
        headers = {'Content-Type': 'application/json', 'Accept': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        req = urllib.request.Request(nodes[name]['url'] + path, headers=headers,
            data=None if body is None else json.dumps(body).encode(), method=method)
        try:
            response = urllib.request.urlopen(req, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else None

    def wait_for(fn, timeout=15):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            try:
                value = fn()
                if value:
                    return value
            except (OSError, urllib.error.URLError, json.JSONDecodeError):
                pass
            time.sleep(.1)
        raise AssertionError('condition timed out; evidence ' + str(root))

    def start(name):
        node = nodes[name]
        node['log'] = (node['dir'] / 'runtime.log').open('a')
        node['process'] = subprocess.Popen([str(args.binary.resolve())], cwd=node['dir'], env=node['env'],
            stdout=node['log'], stderr=node['log'])
        wait_for(lambda: request(name, '/healthz')[0] == 200)

    def stop(name):
        node = nodes[name]
        process = node.get('process')
        if process and process.poll() is None:
            process.terminate()
            try:
                assert process.wait(timeout=12) == 0
            except subprocess.TimeoutExpired:
                process.kill(); process.wait()
                raise
        if node.get('log'):
            node['log'].close()

    def issue(label):
        code, body = request('nodeA', '/admin/api/mcp/issue-token',
            {'client_id': label, 'role': 'client', 'access_mode': 'full', 'ttl_days': 7}, nodes['nodeA']['owner'])
        assert code == 200
        return body

    def execute(token, label):
        code, response = request('nodeB', '/mcp', {'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
            'params': {'name': 'execute', 'arguments': {'target': 'shell:nodeB', 'tool': 'shell_exec',
                'args': {'cmd': f'printf "%s\\n" {label} > {label}.txt', 'timeout': 10},
                'idempotency_key': label}}}, token)
        assert code == 200 and 'error' not in response and not response['result'].get('isError')
        wait_for(lambda: (nodes['nodeB']['dir'] / (label + '.txt')).exists())
        assert (nodes['nodeB']['dir'] / (label + '.txt')).read_text() == label + '\n'
        assert not (nodes['nodeA']['dir'] / (label + '.txt')).exists()

    sync = None
    log = None
    proxy = None
    try:
        start('nodeA')
        source = json.loads((nodes['nodeA']['dir'] / 'shellmcp_identity.json').read_text())['server_id']
        nodes['nodeB']['env']['GPTADMIN_AUTH_SOURCE_ID'] = source
        start('nodeB')
        revoked, retained = issue('sync-revoked'), issue('sync-retained')
        status_dir = root / 'sync'
        status_dir.mkdir()
        status = status_dir / 'status.json'
        # Actual TLS termination forwarding bytes to the real writer process.
        # The URL hostname deliberately needs connect-to; TLS still validates it.
        certificate, key = root / 'writer.crt', root / 'writer.key'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
            '-subj', '/CN=sync-writer.test', '-addext', 'subjectAltName=DNS:sync-writer.test',
            '-keyout', str(key), '-out', str(certificate)], check=True, capture_output=True)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        writer_port = int(nodes['nodeA']['url'].rsplit(':', 1)[1])
        class TLSForward(socketserver.BaseRequestHandler):
            def handle(self):
                try:
                    with context.wrap_socket(self.request, server_side=True) as client, \
                            socket.create_connection(('127.0.0.1', writer_port), timeout=3) as upstream:
                        while True:
                            ready, _, _ = select.select([client, upstream], [], [], 3)
                            if not ready:
                                return
                            for source_socket in ready:
                                data = source_socket.recv(65536)
                                if not data:
                                    return
                                (upstream if source_socket is client else client).sendall(data)
                except (OSError, ssl.SSLError):
                    pass
        proxy = socketserver.ThreadingTCPServer(('127.0.0.1', 0), TLSForward)
        proxy.daemon_threads = True
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        writer_url = f'https://sync-writer.test:{proxy.server_address[1]}'
        env = dict(os.environ, GPTADMIN_AUTH_SYNC_WRITER_TOKEN=nodes['nodeA']['owner'],
            CTL_TOKEN=nodes['nodeB']['owner'], SSL_CERT_FILE=str(certificate))
        base = [sys.executable, str(script)]
        options = ['--writer-url', writer_url, '--writer-connect-to', '127.0.0.1', '--reader-url', nodes['nodeB']['url'],
            '--source-id', source, '--status-file', str(status), '--interval', '1', '--max-age', '3',
            '--timeout', '2', '--allow-loopback-http']
        log = (root / 'sync.log').open('w')
        invalid_options = [s.replace('sync-writer.test', 'wrong-host.test')
                           if s != str(status) else str(root / 'wrong-host-status.json') for s in options]
        invalid_tls = subprocess.run(base + ['once'] + invalid_options, env=env,
            capture_output=True, text=True, timeout=5)
        assert invalid_tls.returncode == 1
        assert not json.loads((root / 'wrong-host-status.json').read_text()).get('last_success')
        assert 'SSLCertVerificationError' in invalid_tls.stdout
        sync = subprocess.Popen(base + ['run'] + options, env=env, stdout=log, stderr=log)
        def state():
            return json.loads(status.read_text())
        wait_for(lambda: state().get('last_success'))
        assert stat.S_IMODE(status.stat().st_mode) == 0o600
        execute(revoked['access_token'], 'before-revoke')
        assert request('nodeB', '/mcp', token='unauthorized')[0] == 401
        initial = state()['generation']
        assert request('nodeA', '/admin/api/clients/' + revoked['token_id'],
            token=nodes['nodeA']['owner'], method='DELETE')[0] == 200
        wait_for(lambda: request('nodeB', '/mcp', token=revoked['access_token'])[0] == 401)
        # Reader admission changes at apply commit, before the helper can persist
        # its received acknowledgment. Wait for both independent observations.
        wait_for(lambda: state()['generation'] > initial)
        stop('nodeB'); start('nodeB')
        assert request('nodeB', '/mcp', token=revoked['access_token'])[0] == 401
        execute(retained['access_token'], 'after-reader-restart')
        stop('nodeA')
        wait_for(lambda: state().get('last_error'))
        failed = state()
        time.sleep(3.2)
        assert state()['last_success'] == failed['last_success']
        checked = subprocess.run(base + ['check'] + options,
            capture_output=True, text=True, timeout=5)
        assert checked.returncode == 1 and not json.loads(checked.stdout)['promotion_eligible']
        sync.terminate(); assert sync.wait(timeout=5) == 0
        previous_attempt = state()['last_attempt']
        sync = subprocess.Popen(base + ['run'] + options, env=env, stdout=log, stderr=log)
        wait_for(lambda: state()['last_attempt'] > previous_attempt)
        assert state()['last_success'] == failed['last_success'], 'runner restart renewed freshness'
        stop('nodeB'); start('nodeB')
        assert request('nodeB', '/mcp', token=revoked['access_token'])[0] == 401
        execute(retained['access_token'], 'writer-down-reader-restart')
        assert request('nodeB', '/register', {'redirect_uris': ['http://127.0.0.1/callback']})[0] == 503
        start('nodeA')
        wait_for(lambda: state().get('last_error') is None and state()['generation'] > failed['generation'])
        assert subprocess.run(base + ['check'] + options,
            capture_output=True, timeout=5).returncode == 0
        assert subprocess.run(base + ['check', '--status-file', str(status), '--max-age', '3'],
            capture_output=True, timeout=5).returncode == 1, 'unbound status qualified for promotion'
        mismatched = list(options)
        mismatched[mismatched.index('--reader-url') + 1] = 'http://127.0.0.1:1'
        assert subprocess.run(base + ['check'] + mismatched,
            capture_output=True, timeout=5).returncode == 1, 'different reader qualified for promotion'
        sync.terminate(); assert sync.wait(timeout=5) == 0
        names = sorted(p.name for p in status_dir.iterdir())
        assert names == ['status.json', 'status.json.lock'], names
        assert status.stat().st_size <= 4096
        for node in nodes.values():
            assert len(list((node['dir'] / 'auth-continuity').glob('slot-*'))) <= 2
        raw = status.read_bytes() + (root / 'sync.log').read_bytes()
        assert all(value.encode() not in raw for value in (nodes['nodeA']['owner'], nodes['nodeB']['owner'],
            revoked['access_token'], retained['access_token'], signer))
        report = {'ok': True, 'revocation_converged': True, 'reader_restart': True,
            'forced_source_https_verified': True, 'wrong_hostname_rejected': True,
            'writer_down_no_freshness_extension': True, 'stale_promotion_excluded': True,
            'runner_restart_no_freshness_extension': True, 'deployment_binding_checked': True,
            'stale_reader_command_after_restart': True, 'writer_recovery': True,
            'status_files': names, 'status_bytes': status.stat().st_size, 'evidence_dir': str(root)}
        (root / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report))
    finally:
        if sync and sync.poll() is None:
            sync.terminate()
            try:
                sync.wait(timeout=5)
            except subprocess.TimeoutExpired:
                sync.kill(); sync.wait()
        if log:
            log.close()
        if proxy:
            proxy.shutdown(); proxy.server_close()
        for name in nodes:
            stop(name)


if __name__ == '__main__':
    main()
