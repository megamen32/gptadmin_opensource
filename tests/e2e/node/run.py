#!/usr/bin/env python3
"""Exercise a real combined-node executable and its real local shell queue."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shlex
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('binary', type=Path)
    args = parser.parse_args()
    project = Path(__file__).resolve().parents[3]
    root = Path(tempfile.mkdtemp(prefix='node-canary-', dir=project / '.tmp'))
    token = secrets.token_urlsafe(32)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    env = dict(os.environ, GPTADMIN_CONFIG_DIR=str(root), GPTADMIN_ROOT=str(root),
               GPTADMIN_ENV_FILE=str(root / 'absent.env'), GPTADMIN_HUB_HOST='127.0.0.1',
               GPTADMIN_HUB_PORT=str(port), CTL_TOKEN=token, SHELL_TOKEN=secrets.token_urlsafe(32),
               OAUTH_CLIENT_SECRET=secrets.token_urlsafe(32), PUBLIC_ORIGIN=origin,
               SHELL_NAME='node-canary', SHELL_IDENTITY_DIR=str(root),
               SHELL_SPOOL_DIR=str(root / 'spool'), SHELL_OUTBOX_DIR=str(root / 'spool/outbox'),
               SHELL_DEFAULT_CWD=str(root), SHELL_DEFAULT_HOME=str(root),
               SHELLMCP_MCP_CONFIG=str(root / 'no-child-mcp.json'),
               HUB_URL='http://127.0.0.1:1', SSH_HOST='127.0.0.1', SSH_PORT='1',
               SHELLMCP_SELF_REPAIR_DISABLE='1')

    def request(path, body=None, authenticated=True, credential=None):
        headers = {'Content-Type': 'application/json'}
        if authenticated:
            headers['Authorization'] = 'Bearer ' + (credential or token)
        req = urllib.request.Request(origin + path, headers=headers,
                                     data=None if body is None else json.dumps(body).encode())
        with urllib.request.urlopen(req, timeout=20) as response:
            return json.load(response)

    with (root / 'runtime.log').open('w') as log:
        proc = subprocess.Popen([str(args.binary.resolve())], env=env, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    raise RuntimeError(f'node exited {proc.returncode}; see {root / "runtime.log"}')
                try:
                    inventory = request('/servers')
                    if any(x['name'] == 'node-canary' and x['status'] == 'online' for x in inventory['servers']):
                        break
                except (urllib.error.URLError, TimeoutError):
                    pass
                time.sleep(.1)
            else:
                raise RuntimeError('local executor did not register')
            marker = secrets.token_hex(16)
            output = root / 'executed.txt'
            command = f'printf %s {shlex.quote(marker)} > {shlex.quote(str(output))} && cat {shlex.quote(str(output))}'
            body = {'target': 'shell:node-canary', 'cmd': command, 'cwd': str(root), 'timeout': 10}
            try:
                request('/mcp-relay/shell_exec', body, authenticated=False)
                raise AssertionError('unauthenticated execution accepted')
            except urllib.error.HTTPError as exc:
                assert exc.code == 401, exc.code
            assert not output.exists(), 'unauthenticated call executed command'
            result = request('/mcp-relay/shell_exec', body)
            assert result.get('status') == 'completed', result
            assert output.read_text() == marker, result
            assert marker in json.dumps(result), result
            assert result.get('server_id') == 'shell:node-canary', result
            job_id = result.get('job_id') or result.get('task_id')
            assert job_id, result
            for queue in ('node-canary', '%20node-canary%20', '%256eode-canary'):
                try:
                    request('/queue/' + queue + '/result',
                            {'id': job_id, 'result': {'stdout': 'forged'}}, credential=env['SHELL_TOKEN'])
                    raise AssertionError('fleet token overwrote local job result')
                except urllib.error.HTTPError as exc:
                    assert exc.code in (401, 403), exc.code
            receipt = request('/mcp-relay/job/' + job_id)
            assert marker in json.dumps(receipt) and 'forged' not in json.dumps(receipt), receipt
            active_pid = root / 'active.pid'
            late_output = root / 'must-not-run.txt'
            active = request('/mcp-relay/shell_exec', {
                'target': 'shell:node-canary', 'background': True, 'timeout': 60,
                'cmd': f'echo $$ > {shlex.quote(str(active_pid))}; sleep 30; echo orphan > {shlex.quote(str(late_output))}',
                'cwd': str(root)})
            deadline = time.monotonic() + 10
            while not active_pid.exists() and time.monotonic() < deadline:
                time.sleep(.05)
            assert active_pid.exists(), active
            print(json.dumps({'ok': True, 'target': result['server_id'], 'status': result['status'],
                              'unreachable_remote_primary': env['HUB_URL'], 'evidence_dir': str(root)}))
        finally:
            if proc.poll() is None:
                started = time.monotonic()
                proc.terminate()
                try:
                    code = proc.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                    raise AssertionError('node did not stop after SIGTERM')
                assert code == 0, f'node shutdown exit {code}; see runtime.log'
                if 'active_pid' in locals() and active_pid.exists():
                    pid = int(active_pid.read_text())
                    try:
                        os.killpg(pid, 0)
                    except ProcessLookupError:
                        pass
                    else:
                        raise AssertionError(f'command process group {pid} survived node shutdown')
                    assert not late_output.exists(), 'cancelled command continued execution'
                print(json.dumps({'shutdown_exit': code, 'shutdown_seconds': round(time.monotonic() - started, 3)}))


if __name__ == '__main__':
    main()
