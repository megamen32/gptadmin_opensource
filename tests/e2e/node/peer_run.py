#!/usr/bin/env python3
"""Real static peer routing: B owns the job even when ingress A disappears."""
import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import select
import ssl
import threading
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('--protocol', choices=('rest', 'mcp', 'both'), default='both')
    parser.add_argument('--legacy-probe', type=Path, help='optional preserved probe expected to fail peer receipt reads')
    args = parser.parse_args()
    for protocol in (('rest', 'mcp') if args.protocol == 'both' else (args.protocol,)):
        run_canary(args.binary.resolve(), protocol, args.legacy_probe)


def run_canary(binary, protocol, legacy_probe=None):
    project = Path(__file__).resolve().parents[3]
    root = Path(tempfile.mkdtemp(prefix='node-peer-canary-', dir=project / '.tmp'))
    token = secrets.token_urlsafe(32)
    owner_token = token
    signer = secrets.token_urlsafe(32)
    nodes = {}
    # Reserve both ports together so they cannot accidentally be identical.
    with socket.socket() as a, socket.socket() as b:
        for name, sock in [('nodeA', a), ('nodeB', b)]:
            sock.bind(('127.0.0.1', 0))
            directory = root / name
            directory.mkdir()
            nodes[name] = dict(directory=directory, port=sock.getsockname()[1])
    for node in nodes.values():
        node['origin'] = f'http://127.0.0.1:{node["port"]}'

    versions = {}
    proxy = None
    peer_route = nodes['nodeB']['origin']
    peer_origin = peer_route

    def request(name, path, body=None, authenticated=True):
        headers = {'Content-Type': 'application/json'}
        if path == '/mcp' and name in versions:
            headers['MCP-Protocol-Version'] = versions[name]
            headers['Mcp-Method'] = body['method']
            if body['method'] == 'tools/call':
                headers['Mcp-Name'] = body['params']['name']
        if authenticated:
            headers['Authorization'] = 'Bearer ' + token
        req = urllib.request.Request(nodes[name]['origin'] + path, headers=headers,
                                     data=None if body is None else json.dumps(body).encode())
        with urllib.request.urlopen(req, timeout=20) as response:
            return None if response.status == 204 else json.load(response)

    def initialize(name):
        response = request(name, '/mcp', {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
            'params': {'protocolVersion': '2025-03-26', 'capabilities': {},
                       'clientInfo': {'name': 'node-peer-canary', 'version': '1'}}})
        assert 'result' in response, response
        versions[name] = response['result']['protocolVersion']
        request(name, '/mcp', {'jsonrpc': '2.0', 'method': 'notifications/initialized'})

    def mcp_tool(name, tool, arguments, authenticated=True):
        response = request(name, '/mcp', {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call',
            'params': {'name': tool, 'arguments': arguments}}, authenticated)
        assert 'error' not in response, response
        result = response['result']
        assert not result.get('isError'), result
        payload = dict(result['structuredContent'])
        payload.update({key: value for key, value in result.get('_meta', {}).items()
                        if key in ('owner_target', 'owner_endpoint')})
        return payload

    def call(name, body, authenticated=True):
        if protocol == 'mcp':
            return mcp_tool(name, 'execute', body, authenticated)
        return request(name, '/mcp-relay/call', body, authenticated)

    def get_receipt(name, job_id, owner_target=None):
        if protocol == 'mcp':
            arguments = {'id': job_id}
            if owner_target:
                arguments['owner_target'] = owner_target
            return mcp_tool(name, 'job', arguments)
        return request(name, '/mcp-relay/job/' + job_id)

    def stop(node):
        proc = node.get('proc')
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                code = proc.wait(timeout=12)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise AssertionError('node did not stop after SIGTERM')
            assert code == 0, f'node shutdown exit {code}'

    try:
        if protocol == 'mcp':
            certificate, key = root / 'peer.crt', root / 'peer.key'
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                '-subj', '/CN=peer-route.test', '-addext', 'subjectAltName=DNS:peer-route.test',
                '-keyout', str(key), '-out', str(certificate)], check=True, capture_output=True)
            tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls.load_cert_chain(certificate, key)
            class TLSForward(socketserver.BaseRequestHandler):
                def handle(self):
                    try:
                        with tls.wrap_socket(self.request, server_side=True) as client, \
                                socket.create_connection(('127.0.0.1', nodes['nodeB']['port']), timeout=3) as upstream:
                            while True:
                                ready, _, _ = select.select([client, upstream], [], [], 10)
                                if client.pending() and client not in ready:
                                    ready.append(client)
                                if not ready:
                                    return
                                for source in ready:
                                    data = source.recv(65536)
                                    if not data:
                                        return
                                    (upstream if source is client else client).sendall(data)
                    except (OSError, ssl.SSLError):
                        pass
            proxy = socketserver.ThreadingTCPServer(('127.0.0.1', 0), TLSForward)
            proxy.daemon_threads = True
            threading.Thread(target=proxy.serve_forever, daemon=True).start()
            peer_origin = f'https://peer-route.test:{proxy.server_address[1]}'
            peer_route = {'url': peer_origin, 'connect_to': '127.0.0.1'}
        for name, node in nodes.items():
            directory = node['directory']
            env = dict(os.environ, GPTADMIN_CONFIG_DIR=str(directory), GPTADMIN_ROOT=str(directory),
                       GPTADMIN_ENV_FILE=str(directory / 'absent.env'), GPTADMIN_HUB_HOST='127.0.0.1',
                       GPTADMIN_HUB_PORT=str(node['port']), CTL_TOKEN=token,
                       SHELL_TOKEN=secrets.token_urlsafe(32), OAUTH_CLIENT_SECRET=secrets.token_urlsafe(32),
                       PUBLIC_ORIGIN=node['origin'], SHELL_NAME=name, SHELL_IDENTITY_DIR=str(directory),
                       SHELL_SPOOL_DIR=str(directory / 'spool'),
                       SHELL_OUTBOX_DIR=str(directory / 'spool/outbox'),
                       SHELL_DEFAULT_CWD=str(directory), SHELL_DEFAULT_HOME=str(directory),
                       SHELLMCP_MCP_CONFIG=str(directory / 'no-child-mcp.json'),
                       HUB_URL='http://127.0.0.1:1', SHELLMCP_SELF_REPAIR_DISABLE='1',
                       GPTADMIN_NODE_PEERS=json.dumps({'shell:nodeB': peer_route} if name == 'nodeA' else {}))
            if protocol == 'mcp':
                source_id = ''
                if name == 'nodeB':
                    identity_file = nodes['nodeA']['directory'] / 'shellmcp_identity.json'
                    deadline = time.monotonic() + 15
                    while not identity_file.exists() and time.monotonic() < deadline:
                        time.sleep(.05)
                    source_id = json.loads(identity_file.read_text())['server_id']
                env.update(SSL_CERT_FILE=str(certificate), GPTADMIN_AUTH_MODE='writer' if name == 'nodeA' else 'reader',
                           GPTADMIN_AUTH_SOURCE_ID=source_id, OAUTH_CLIENT_SECRET=signer,
                           PUBLIC_ORIGIN='https://peer-auth.example', MCP_RESOURCE='https://peer-auth.example/mcp')
            node['log'] = (directory / 'runtime.log').open('w')
            node['proc'] = subprocess.Popen([str(binary)], cwd=directory, env=env,
                                            stdout=node['log'], stderr=node['log'])
        if protocol == 'mcp':
            for name in nodes:
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    try:
                        request(name, '/healthz')
                        break
                    except (urllib.error.URLError, TimeoutError):
                        time.sleep(.05)
                else:
                    raise AssertionError('node health unavailable before auth sync')
            managed = request('nodeA', '/admin/api/mcp/issue-token',
                {'client_id': 'peer-receipt-canary', 'role': 'client', 'access_mode': 'full', 'ttl_days': 7})
            assert managed['role'] == 'client'
            snapshot = request('nodeA', '/admin/api/auth-snapshot/export', {})
            ack = request('nodeB', '/admin/api/auth-snapshot/apply', snapshot)
            assert ack['generation'] == snapshot['generation']
        for name, node in nodes.items():
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if node['proc'].poll() is not None:
                    raise AssertionError(f'{name} exited; see {node["directory"] / "runtime.log"}')
                try:
                    inventory = request(name, '/servers')
                    if any(x['name'] == name and x['status'] == 'online' for x in inventory['servers']):
                        break
                except (urllib.error.URLError, TimeoutError):
                    pass
                time.sleep(.1)
            else:
                raise AssertionError(f'{name} executor not ready')
        identities = [json.loads((node['directory'] / 'shellmcp_identity.json').read_text()) for node in nodes.values()]
        assert identities[0]['server_id'] != identities[1]['server_id'], 'nodes share an executor identity'
        assert identities[0]['public_key'] != identities[1]['public_key'], 'nodes share an executor key'
        if protocol == 'mcp':
            token = managed['access_token']
            assert token != owner_token
            initialize('nodeA')
            initialize('nodeB')
            tools = request('nodeA', '/mcp', {'jsonrpc': '2.0', 'id': 3, 'method': 'tools/list'})['result']['tools']
            job_schema = next(tool['inputSchema'] for tool in tools if tool['name'] == 'job')
            assert 'owner_target' in job_schema['properties']
        marker = secrets.token_hex(16)
        body = {'target': 'shell:nodeB', 'tool': 'shell_exec', 'background': True,
                'idempotency_key': 'peer-' + marker,
                'args': {'cmd': f'printf "%s\\n" {marker} >> peer-marker.txt; pwd; cat peer-marker.txt', 'timeout': 10}}
        for name in nodes:
            try:
                call(name, body, authenticated=False)
                raise AssertionError(f'{name} allowed anonymous execution')
            except urllib.error.HTTPError as exc:
                assert exc.code == 401, exc.code
        result = call('nodeA', body)
        job_id = result.get('job_id') or result.get('task_id')
        assert job_id, result
        assert result.get('owner_target') == 'shell:nodeB', result
        assert result.get('owner_endpoint') == peer_origin, result
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            receipt = get_receipt('nodeA', job_id, result['owner_target']) if protocol == 'mcp' else get_receipt('nodeB', job_id)
            if receipt.get('status') == 'completed':
                break
            time.sleep(.1)
        else:
            raise AssertionError(f'B job did not finish: {receipt}')
        output = nodes['nodeB']['directory'] / 'peer-marker.txt'
        assert output.read_text().splitlines() == [marker], receipt
        assert str(nodes['nodeB']['directory']) in json.dumps(receipt), receipt
        assert not (nodes['nodeA']['directory'] / 'peer-marker.txt').exists(), 'A executed B command'
        if protocol == 'mcp':
            assert get_receipt('nodeA', job_id, result['owner_target']) == receipt, 'repeated same-ingress read changed receipt'
            for unknown_id, owner in [(secrets.token_hex(16), 'shell:nodeB'), (job_id, 'shell:nodeA'),
                                      (job_id, 'shell:unconfigured')]:
                try:
                    failure = mcp_tool('nodeA', 'job', {'id': unknown_id, 'owner_target': owner})
                except urllib.error.HTTPError as error:
                    assert error.code in (400, 403, 404, 508)
                else:
                    assert failure.get('status') == 'failed', 'unknown/nonowned job returned data'
        if protocol == 'mcp':
            probe_env = dict(os.environ, NODE_PEER_CANARY_TOKEN=token)
            probes = [('current', project / 'scripts/gptadmin_node_probe.py', 0)]
            if legacy_probe:
                probes.insert(0, ('legacy', legacy_probe.resolve(), 1))
            for label, script, expected in probes:
                probe = subprocess.run(['python3', str(script), '--url', nodes['nodeA']['origin'],
                    '--target', 'shell:nodeB', '--token-env', 'NODE_PEER_CANARY_TOKEN', '--timeout', '10'],
                    env=probe_env, capture_output=True, text=True, timeout=15)
                (root / f'probe-{label}.json').write_text(probe.stdout or probe.stderr)
                assert probe.returncode == expected, f'{label} probe unexpected exit: {probe.stdout} {probe.stderr}'
                if expected == 0:
                    assert json.loads(probe.stdout)['ok']
                else:
                    assert 'probe job did not complete successfully' in probe.stderr, probe.stderr
        stop(nodes['nodeA'])
        if protocol == 'mcp':
            initialize('nodeB')
        try:
            call('nodeB', body, authenticated=False)
            raise AssertionError('B allowed anonymous replay after A stopped')
        except urllib.error.HTTPError as exc:
            assert exc.code == 401, exc.code
        replay = call('nodeB', body)
        assert (replay.get('job_id') or replay.get('task_id')) == job_id, replay
        assert replay.get('status') == 'completed', replay
        assert marker in json.dumps(replay), replay
        direct = get_receipt('nodeB', job_id)
        without_hints = lambda value: {k: v for k, v in value.items() if k not in ('owner_target', 'owner_endpoint')}
        assert without_hints(direct) == without_hints(receipt)
        assert output.read_text().splitlines() == [marker], 'duplicate side effect after ingress loss'
        fresh = call('nodeB', {
            'target': 'shell:nodeB', 'tool': 'shell_exec', 'idempotency_key': 'fresh-' + marker,
            'args': {'cmd': 'printf alive > after-a.txt; cat after-a.txt', 'timeout': 10}})
        fresh_id = fresh.get('job_id') or fresh.get('task_id')
        deadline = time.monotonic() + 20
        while fresh.get('status') != 'completed' and time.monotonic() < deadline:
            time.sleep(.1)
            fresh = get_receipt('nodeB', fresh_id)
        assert fresh.get('status') == 'completed', fresh
        assert (nodes['nodeB']['directory'] / 'after-a.txt').read_text() == 'alive', fresh
        assert not (nodes['nodeA']['directory'] / 'after-a.txt').exists()
        print(json.dumps({'ok': True, 'protocol': protocol, 'owner': 'shell:nodeB', 'job_id': job_id,
                          'ingress_a_stopped': True, 'replayed_receipt': True,
                          'distinct_executor_identities': True, 'fresh_command_after_a_loss': True,
                          'side_effect_count': 1, 'unauthorized_status': 401,
                          'ordinary_managed_client': protocol == 'mcp', 'same_entrypoint_repeated_receipt': protocol == 'mcp',
                          'unknown_nonowned_receipts_rejected': protocol == 'mcp', 'peer_forced_https': protocol == 'mcp',
                          'evidence_dir': str(root)}))
    finally:
        if proxy:
            proxy.shutdown(); proxy.server_close()
        for node in nodes.values():
            try:
                stop(node)
            finally:
                if 'log' in node:
                    node['log'].close()


if __name__ == '__main__':
    main()
