#!/usr/bin/env python3
# LHC:COVERS go-shellmcp/internal/shell/run.go go-shellmcp/internal/shell/self_service_control.go go-shellmcp/internal/shell/self_service_control_linux.go
"""Exercise a real ShellMCP executable in disposable systemd services.

Requires root and the host systemd manager. Never targets the installed Hub.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import socket
import subprocess
import time
import urllib.error
import urllib.request


def run(*argv, check=True):
    return subprocess.run(argv, check=check, text=True, capture_output=True, timeout=30)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--shellmcp-binary', required=True, type=Path)
    parser.add_argument('--expected-commit', required=True)
    parser.add_argument('--workdir', required=True, type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('run as root to create only the disposable test services')
    binary = args.shellmcp_binary.resolve(strict=True)
    root = args.workdir.resolve()
    root.mkdir(parents=True, mode=0o700, exist_ok=False)
    suffix = secrets.token_hex(6)
    unit = 'gptadmin-selfstop-qa-' + suffix + '.service'
    other = 'gptadmin-selfstop-other-' + suffix + '.service'
    maintenance = 'gptadmin-selfstop-maintenance-' + suffix + '.service'
    token = secrets.token_hex(32)
    with socket.socket() as probe:
        probe.bind(('127.0.0.1', 0))
        port = probe.getsockname()[1]
    origin = 'http://127.0.0.1:' + str(port)
    env = {
        'SHELL_HOST': '127.0.0.1', 'SHELL_PORT': str(port), 'SHELL_TOKEN': token,
        'SHELL_IDENTITY_DIR': str(root / 'identity'),
        'SHELL_SPOOL_DIR': str(root / 'spool'),
        'SHELL_OUTBOX_DIR': str(root / 'outbox'),
        'SHELL_DEFAULT_USER': 'root', 'SHELL_DEFAULT_HOME': str(root),
        'SHELL_DEFAULT_CWD': str(root), 'SHELLMCP_QUEUE': '0',
        'SHELL_HEARTBEAT': '0', 'SHELLMCP_SELF_REPAIR_DISABLE': '1',
    }
    env_file = root / 'environment'
    env_file.write_text(''.join(k + '=' + shlex.quote(v) + '\n' for k, v in env.items()))
    env_file.chmod(0o600)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(path, body=None):
        payload = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(origin + path, data=payload, headers={
            'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json',
        })
        with opener.open(req, timeout=10) as response:
            return json.load(response)

    def pid():
        return int(run('systemctl', 'show', unit, '-p', 'MainPID', '--value').stdout.strip())

    def version():
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                value = request('/version')
                assert value.get('git_commit') == args.expected_commit, value
                return value
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        raise AssertionError('real ShellMCP did not become ready')

    evidence = {'unit': unit, 'git_commit': args.expected_commit,
                'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
                'checks': []}
    try:
        run('systemd-run', '--unit=' + unit, '--collect', '--service-type=exec',
            '--property=EnvironmentFile=' + str(env_file),
            '--property=KillMode=mixed', '--property=TimeoutStopSec=3', str(binary))
        evidence['version'] = version()
        original_pid = pid()
        assert original_pid > 0
        prefix = root / 'before-stop'
        suffix_file = root / 'after-stop'
        requests = [
            {'cmd': 'touch ' + shlex.quote(str(prefix)) + '; sudo /usr/bin/systemctl stop '
             + unit + '; touch ' + shlex.quote(str(suffix_file))},
            {'argv': ['/usr/bin/systemctl', 'stop', unit]},
        ]
        for spec in requests:
            value = request('/exec', {**spec, 'run_as_user': 'root', 'timeout': 5})
            assert value.get('returncode') == -1 and value.get('error'), value
            assert 'systemd-run' in value['error'], value
            assert not prefix.exists() and not suffix_file.exists(), 'command ran before refusal'
            assert pid() == original_pid, 'guard changed the real service generation'
        evidence['checks'].append('cmd and argv self-stop refused before any shell effect')
        result = request('/exec', {'argv': ['/usr/bin/systemctl', 'is-active', unit],
                                   'run_as_user': 'root', 'timeout': 5})
        assert result.get('returncode') == 0 and result.get('stdout', '').strip() == 'active', result
        evidence['checks'].append('read-only own-service inspection allowed')
        run('systemd-run', '--unit=' + other, '--collect', '/usr/bin/sleep', '60')
        result = request('/exec', {'argv': ['/usr/bin/systemctl', 'stop', other],
                                   'run_as_user': 'root', 'timeout': 5})
        assert result.get('returncode') == 0 and pid() == original_pid, result
        evidence['checks'].append('other-service stop allowed without touching own service')
        done = root / 'maintenance-completed'
        script = root / 'maintenance.sh'
        script.write_text('set -eu\nsleep 1\nsystemctl restart ' + shlex.quote(unit)
                          + '\nprintf completed > ' + shlex.quote(str(done)) + '\n')
        script.chmod(0o700)
        result = request('/exec', {'argv': ['/usr/bin/systemd-run', '--unit=' + maintenance,
                                           '--collect', '/bin/sh', str(script)],
                                   'run_as_user': 'root', 'timeout': 5})
        assert result.get('returncode') == 0, result
        deadline = time.monotonic() + 20
        while not done.exists() and time.monotonic() < deadline:
            time.sleep(0.1)
        assert done.read_text() == 'completed', 'detached maintenance lost control at restart'
        version()
        assert pid() > 0 and pid() != original_pid, 'detached restart did not create a new process'
        result = request('/exec', {'argv': ['/usr/bin/printf', 'SELF_SERVICE_REAL_GREEN\n'],
                                   'run_as_user': 'root', 'timeout': 5})
        assert result.get('returncode') == 0 and result.get('stdout') == 'SELF_SERVICE_REAL_GREEN\n', result
        evidence['checks'].append('detached maintenance survives real restart; new executor works')
        evidence['result'] = 'PASS'
    finally:
        for name in (maintenance, other, unit):
            run('systemctl', 'stop', name, check=False)
        evidence['cleanup'] = {name: run('systemctl', 'is-active', name, check=False).stdout.strip()
                               for name in (maintenance, other, unit)}
        (root / 'evidence.json').write_text(json.dumps(evidence, indent=2) + '\n')
        env_file.unlink(missing_ok=True)
        print(json.dumps(evidence, ensure_ascii=False))


if __name__ == '__main__':
    main()
