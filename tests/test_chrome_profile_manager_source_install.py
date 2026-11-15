# LHC:COVERS install_chrome_profile_manager.py chrome_profile_manager_requirements.txt
"""Real source install and child registration against a real ShellMCP process."""
import json
import os
from pathlib import Path
import subprocess
import sys
import stat
import time

ROOT = Path(__file__).resolve().parents[1]


def test_failed_install_can_be_retried_and_unsafe_state_is_rejected(tmp_path):
    prefix = tmp_path / 'install'
    state = tmp_path / 'state'
    command = [sys.executable, str(ROOT / 'install_chrome_profile_manager.py'),
               '--prefix', str(prefix), '--state-dir', str(state)]
    env = os.environ.copy()
    env.update(PIP_NO_INDEX='1', PIP_NO_CACHE_DIR='1')
    failed = subprocess.run(command, env=env, capture_output=True, text=True)
    assert failed.returncode != 0
    assert not prefix.exists()
    state.chmod(0o755)
    rejected = subprocess.run(command, capture_output=True, text=True)
    assert rejected.returncode != 0
    assert not prefix.exists()
    assert stat.S_IMODE(state.stat().st_mode) == 0o755
    state.chmod(0o700)
    symlink = tmp_path / 'state-link'
    symlink.symlink_to(state, target_is_directory=True)
    link_command = command[:-1] + [str(symlink)]
    assert subprocess.run(link_command, capture_output=True).returncode != 0
    assert not prefix.exists()
    subprocess.run(command, check=True, capture_output=True, text=True)
    assert prefix.is_dir()
    marker = prefix / 'keep'
    marker.write_text('owned data')
    assert subprocess.run(command, capture_output=True).returncode != 0
    assert marker.read_text() == 'owned data'


def test_source_install_and_shellmcp(tmp_path):
    prefix = tmp_path / 'installation with spaces'
    state = tmp_path / 'state'
    result = subprocess.run([sys.executable, str(ROOT / 'install_chrome_profile_manager.py'),
                             '--prefix', str(prefix), '--state-dir', str(state)],
                            check=True, capture_output=True, text=True)
    registration = json.loads(result.stdout)
    assert stat.S_IMODE(state.stat().st_mode) == 0o700
    assert registration['config']['ref'] == 'chrome-profile-manager'
    assert (prefix / 'chrome_profile_manager_bridge/host.py').is_file()
    assert (prefix / 'chrome_profile_manager_bridge/extension/manifest.json').is_file()
    subprocess.run([str(prefix / 'venv/bin/python'), '-c',
                    'import websocket, chrome_profile_helper; assert websocket.__version__ == "1.8.0"'],
                   cwd=prefix, check=True)
    binary = tmp_path / 'shellmcp'
    subprocess.run(['go', 'build', '-o', str(binary), './cmd/shellmcp-go'], cwd=ROOT / 'go-shellmcp', check=True)
    env = os.environ.copy()
    env.update(HOME=str(tmp_path), XDG_CONFIG_HOME=str(tmp_path / 'config'),
               CHROME_PROFILE_ROOTS=str(tmp_path / 'empty-roots'),
               SHELLMCP_MCP_CONFIG=str(tmp_path / 'mcp.json'),
               SHELLMCP_SPOOL_DIR=str(tmp_path / 'spool'),
               SHELLMCP_IDENTITY_DIR=str(tmp_path / 'identity'),
               SHELLMCP_DEFAULT_HOME=str(tmp_path), SHELLMCP_DEFAULT_CWD=str(tmp_path),
               SHELLMCP_HEARTBEAT='0', SHELLMCP_QUEUE='0', HUB_URL='')
    proc = subprocess.Popen([str(binary), '--mcp-stdio'], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, text=True, env=env)
    try:
        def rpc(i, method, params):
            proc.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': i, 'method': method, 'params': params}) + '\n')
            proc.stdin.flush()
            response = json.loads(proc.stdout.readline())
            assert 'error' not in response, response
            return response['result']

        rpc(1, 'initialize', {'protocolVersion': '2025-03-26', 'capabilities': {},
                              'clientInfo': {'name': 'source-install-test', 'version': '1'}})
        managed = rpc(2, 'tools/call', {'name': 'mcp_manage', 'arguments': registration})
        assert not managed.get('isError'), managed
        assert json.loads((tmp_path / 'mcp.json').read_text())
        listed = rpc(3, 'tools/call', {'name': 'mcp_tools', 'arguments': {'ref': 'chrome-profile-manager'}})
        assert 'profile_list' in {tool['name'] for tool in listed['structuredContent']['tools']}
        called = rpc(4, 'tools/call', {'name': 'mcp_call', 'arguments':
                     {'ref': 'chrome-profile-manager', 'name': 'profile_list', 'arguments': {}}})
        child = called['structuredContent']['result']
        assert not child.get('isError'), child
        assert child['structuredContent']['profiles'] == []

        # A temporary Local State file makes the environment difference observable
        # through the real child, without launching Chrome or using real profiles.
        roots = tmp_path / 'empty-roots'
        roots.mkdir()
        (roots / 'Local State').write_text(json.dumps({'profile': {'info_cache': {'Default': {'name': 'Test profile'}}}}))
        unscoped = rpc(5, 'tools/call', {'name': 'mcp_call', 'arguments':
                       {'ref': 'chrome-profile-manager', 'name': 'profile_list', 'arguments': {}}})
        assert unscoped['structuredContent']['result']['structuredContent']['profiles'][0]['name'] == 'Test profile'

        listing = rpc(6, 'tools/call', {'name': 'mcp_manage', 'arguments': {'action': 'list'}})
        instance = next(x['connection_instance'] for x in listing['structuredContent']['agents']
                        if x['ref'] == 'chrome-profile-manager')
        args = {'ref': 'chrome-profile-manager', 'name': 'profile_list', 'arguments': {},
                '_connection_scope': 'a' * 64, '_connection_instance': instance,
                '_connection_expires_at': int((time.time() + 30) * 1000)}
        proc.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': 7, 'method': 'tools/call',
                                     'params': {'name': 'mcp_call', 'arguments': args}}) + '\n')
        proc.stdin.flush()
        response = json.loads(proc.stdout.readline())
        assert 'scoped stdio ownership-safe lifecycle unavailable' in response['error']['message']

        registration['config']['env']['CHROME_PROFILE_ROOTS'] = str(tmp_path / 'other-roots')
        updated = rpc(8, 'tools/call', {'name': 'mcp_manage', 'arguments': registration})
        assert not updated.get('isError'), updated
        changed = rpc(9, 'tools/call', {'name': 'mcp_call', 'arguments':
                      {'ref': 'chrome-profile-manager', 'name': 'profile_list', 'arguments': {}}})
        assert changed['structuredContent']['result']['structuredContent']['profiles'] == []
    finally:
        proc.stdin.close()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
