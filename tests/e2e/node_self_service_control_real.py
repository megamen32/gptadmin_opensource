#!/usr/bin/env python3
"""Real disposable Node -> Hub MCP -> executor -> PtyMcp self-stop canary.

Run as root. Only randomly named services created by this invocation are ever
controlled. A failed guard may stop its QA Node; the native driver survives,
records RED and restores that same fixture for the remaining independent checks.
terminal_write is input, never a command: its allow probe writes only to an
owned /bin/cat PTY, after verifying the reported PID/executable and QA cgroup.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import secrets
import shlex
import socket
import subprocess
import time
import urllib.request


def run(*argv, check=True):
    return subprocess.run(argv, check=check, capture_output=True, text=True, timeout=35)


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def values(value, key):
    if isinstance(value, dict):
        if key in value:
            yield value[key]
        for item in value.values():
            yield from values(item, key)
    elif isinstance(value, list):
        for item in value:
            yield from values(item, key)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--node-binary', type=Path, required=True)
    parser.add_argument('--expected-commit', required=True)
    parser.add_argument('--pty-mcp-executable', type=Path, required=True)
    parser.add_argument('--workdir', type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('root is required for disposable systemd service creation')
    binary = args.node_binary.resolve(strict=True)
    pty = args.pty_mcp_executable.absolute()
    pty.resolve(strict=True)
    account = pwd.getpwnam('admin')
    root = args.workdir.resolve()
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.chown(root, account.pw_uid, account.pw_gid)
    for name in ('config', 'state', 'spool', 'outbox', 'home', 'pty-state'):
        directory = root / name
        directory.mkdir(mode=0o700)
        os.chown(directory, account.pw_uid, account.pw_gid)
    suffix = secrets.token_hex(6)
    qa_name = 'qa-node-selfstop-' + suffix
    unit = qa_name + '.service'
    other = qa_name + '-other.service'
    maintenance = qa_name + '-maintenance.service'
    token = secrets.token_hex(32)
    secret_sentinel = 'qa-audit-secret-' + secrets.token_hex(24)
    qa_client_id = qa_name + '-mcp-client'
    private_values = [token, secret_sentinel]
    mcp_token = None
    registration_attempted = False
    registration_token_id = None
    port, shell_port = free_port(), free_port()
    while shell_port == port:
        shell_port = free_port()
    origin = 'http://127.0.0.1:' + str(port)
    shell_target = 'shell:' + qa_name
    pty_target = 'mcp:' + shell_target + ':PtyMcp'
    supervisor = root / 'config' / 'supervisor.json'
    supervisor.write_text(json.dumps({'agents': [{
        'ref': 'PtyMcp', 'name': 'pty-mcp', 'transport': 'stdio',
        'command': str(pty), 'args': [], 'user': account.pw_name, 'cwd': '/',
        'enabled': True, 'env': {'PTY_MCP_STATE_DIR': str(root / 'pty-state')},
    }]}))
    supervisor.chmod(0o600)
    os.chown(supervisor, account.pw_uid, account.pw_gid)
    env = {
        'PATH': '/home/admin/bin:/home/admin/.bun/bin:/home/admin/.npm-global/bin:/usr/local/bin:/usr/bin:/bin',
        'HOME': str(root / 'home'), 'LANG': 'C.UTF-8',
        'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': str(port),
        'GPTADMIN_ROOT': str(root), 'GPTADMIN_CONFIG_DIR': str(root / 'config'),
        'CTL_TOKEN': token, 'SHELL_TOKEN': token, 'PUBLIC_ORIGIN': origin,
        'MCP_RESOURCE': origin, 'GPTADMIN_AUTH_MODE': 'writer',
        'GPTADMIN_NODE_PEERS': '{}', 'SHELL_NAME': qa_name,
        'SHELL_HOST': '127.0.0.1', 'SHELL_PORT': str(shell_port),
        'SHELL_IDENTITY_DIR': str(root / 'state'),
        'SHELL_SPOOL_DIR': str(root / 'spool'), 'SHELL_OUTBOX_DIR': str(root / 'outbox'),
        'SHELL_DEFAULT_USER': account.pw_name, 'SHELL_DEFAULT_HOME': str(root / 'home'),
        'SHELL_DEFAULT_CWD': str(root / 'home'), 'SHELLMCP_MCP_CONFIG': str(supervisor),
        'SHELLMCP_AUDIT_LOG': str(root / 'executor-audit.jsonl'),
        'GPTADMIN_AUDIT_STATE_FILE': str(root / 'hub-audit.jsonl'),
        'SHELLMCP_SELF_REPAIR_DISABLE': '1', 'ROOTD_AUTO_UPDATE': '0',
        'MCP_RELAY_POLL_MAX_TIMEOUT': '2', 'GOMAXPROCS': '2',
    }
    env_file = root / 'environment'
    env_file.write_text(''.join(k + '=' + shlex.quote(v) + '\n' for k, v in env.items()))
    env_file.chmod(0o600)
    spec = importlib.util.spec_from_file_location(
        'qa_bootstrap', Path(__file__).resolve().parents[2] / 'go-hub/internal/hub/bootstrap_assets/gptadmin.py')
    api = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(api)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    evidence = {'unit': unit, 'other_unit': other, 'maintenance_unit': maintenance,
                'binary': str(binary), 'expected_commit': args.expected_commit,
                'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
                'pty_executable': str(pty), 'protocol': api.LEGACY, 'checks': []}
    client = None
    responses = []

    def redact(text):
        for value in private_values:
            text = text.replace(value, '<redacted>')
        return text

    def save_json(path, value):
        path.write_text(redact(json.dumps(value, ensure_ascii=False, indent=2)) + '\n')
        path.chmod(0o600)

    def pid():
        result = run('systemctl', 'show', unit, '-p', 'MainPID', '--value', check=False)
        return int(result.stdout.strip() or '0')

    def start_node():
        run('systemd-run', '--unit=' + unit, '--service-type=exec',
            '--property=User=' + account.pw_name, '--property=EnvironmentFile=' + str(env_file),
            '--property=KillMode=control-group', '--property=TimeoutStopSec=3', str(binary))

    def connect():
        nonlocal client, mcp_token, registration_attempted, registration_token_id
        deadline = time.monotonic() + 25
        last_error = None
        while time.monotonic() < deadline:
            try:
                with opener.open(origin + '/version', timeout=2) as response:
                    version = json.load(response)
                if version.get('git_commit') != args.expected_commit:
                    raise AssertionError('QA binary commit mismatch: ' + str(version.get('git_commit')))
                if mcp_token is None:
                    # Use the real credential manager; CTL auth deliberately
                    # retains nil-caller semantics and cannot prove MCP origin.
                    assert not registration_attempted, 'QA credential registration outcome unknown; not replaying issuance'
                    registration_attempted = True
                    registration = urllib.request.Request(origin + '/admin/api/mcp/issue-token',
                        headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
                        data=json.dumps({'client_id': qa_client_id, 'ttl_days': 1,
                                         'access_mode': 'full', 'role': 'client'}).encode())
                    with opener.open(registration, timeout=5) as response:
                        issued = json.load(response)
                    mcp_token = issued.get('access_token')
                    assert isinstance(mcp_token, str) and mcp_token, 'QA registration did not issue a credential'
                    private_values.append(mcp_token)
                    assert issued.get('client_id') == qa_client_id and issued.get('token_id'), 'QA credential identity mismatch'
                    registration_token_id = issued['token_id']
                    # Values stay protected; reports contain identity only.
                    credential = root / 'mcp-credential.json'
                    with open(credential, 'x', opener=lambda path, flags: os.open(path, flags, 0o600)) as file:
                        json.dump(issued, file)
                    evidence['authentication'] = {'method': 'native_managed_mcp_registration',
                        'client_id': qa_client_id, 'token_id': registration_token_id}
                client = api.Client({'mcp_url': origin + '/mcp', 'access_token': mcp_token,
                                     'expires_at': time.time() + 3600}, timeout=12, legacy=True)
                # The bundled HTTP helper only accepts notification HTTP 202;
                # the real legacy Hub returns 204. Send that notification over
                # the real wire here, retaining the actual negotiated response.
                initialized = client.rpc('initialize', {
                    'protocolVersion': api.LEGACY, 'capabilities': {},
                    'clientInfo': {'name': 'node-self-service-real', 'version': '1'},
                })
                assert initialized.get('protocolVersion') == api.LEGACY, initialized
                headers = {'Authorization': 'Bearer ' + mcp_token, 'Content-Type': 'application/json',
                           'MCP-Protocol-Version': api.LEGACY}
                if client.session_id:
                    headers['Mcp-Session-Id'] = client.session_id
                notification = urllib.request.Request(origin + '/mcp', headers=headers, data=json.dumps({
                    'jsonrpc': '2.0', 'method': 'notifications/initialized', 'params': {},
                }).encode())
                with opener.open(notification, timeout=3) as response:
                    assert response.status in (202, 204), response.status
                client.initialization = initialized
                client.load_tools()
                evidence['version'] = version
                return
            except AssertionError:
                raise
            except Exception as exc:
                last_error = type(exc).__name__ + ': ' + str(exc)
                time.sleep(0.15)
        raise AssertionError('isolated Node/MCP startup failed: ' + str(last_error))

    def schema(target, tool):
        value = api.result_data(client.call_tool('schema', {'target': target, 'tool': tool, 'detail': 'full'}))
        if isinstance(value, dict) and value.get('context'):
            client.instruction_context = value['context']
        return value

    def call(target, tool, arguments):
        schema(target, tool)
        value = api.result_data(client.call_target(target, tool, arguments))
        if isinstance(value, dict) and value.get('executed') is False and value.get('context'):
            # This explicit pre-execution rejection is safe to repair once.
            client.instruction_context = value['context']
            value = api.result_data(client.call_target(target, tool, arguments))
        deadline = time.monotonic() + 20
        while isinstance(value, dict) and time.monotonic() < deadline:
            receipt = value.get('error', {}).get('receipt', {}) if isinstance(value.get('error'), dict) else {}
            job_id = receipt.get('job_id')
            if not job_id:
                break
            # Both public targets execute on this one QA shell. A child MCP
            # target is not a valid receipt owner in the ordinary Hub API.
            value = api.result_data(client.call_tool('job', {'id': job_id, 'owner_target': shell_target, 'detail': 'full'}))
            if value.get('status') in ('completed', 'failed', 'cancelled', 'error'):
                break
            time.sleep(0.1)
        responses.append({'target': target, 'tool': tool, 'response': value})
        return value

    def exec_shell(command, **extra):
        return call(shell_target, 'shell_exec', {'cmd': command, 'timeout': 6, **extra})

    def spawn(command, **extra):
        return call(pty_target, 'terminal_spawn', {'shell': '/bin/bash', 'args': ['-lc', command],
                                                 'cols': 100, 'rows': 30, 'waitMs': 400, **extra})

    def pty_payload(value):
        for text in values(value, 'text'):
            if isinstance(text, str):
                try:
                    decoded = json.loads(text)
                except ValueError:
                    continue
                if isinstance(decoded, dict) and decoded.get('sessionId'):
                    return decoded
        raise AssertionError('real PtyMcp did not return a sessionId: ' + json.dumps(value)[:1000])

    def succeeded(value):
        return 0 in list(values(value, 'returncode')) or 0 in list(values(value, 'return_code'))

    def refusal(value, before, markers):
        text = json.dumps(value, ensure_ascii=False).lower()
        assert unit in text and ('отклон' in text or 'собствен' in text or 'self-service' in text), text[:1200]
        assert not any(marker.exists() for marker in markers), 'command effects occurred before refusal'
        assert pid() == before and before > 0, 'self-stop changed QA Node PID'

    def check(name, operation):
        before = pid()
        try:
            detail = operation()
            evidence['checks'].append({'name': name, 'result': 'PASS', 'detail': detail})
        except Exception as exc:
            evidence['checks'].append({'name': name, 'result': 'FAIL',
                                       'pid_before': before, 'pid_after': pid(),
                                       'prefix_effects': sorted(path.name for path in root.glob('*-prefix')),
                                       'suffix_effects': sorted(path.name for path in root.glob('*-suffix')),
                                       'error': redact(type(exc).__name__ + ': ' + str(exc))[:2000]})
        finally:
            if pid() == 0:
                # Native driver, never the dying executor, owns fixture recovery.
                loaded = run('systemctl', 'show', unit, '-p', 'LoadState', '--value', check=False).stdout.strip()
                if loaded in ('', 'not-found'):
                    start_node()
                else:
                    run('systemctl', 'restart', unit)
                connect()

    def guard_probe(tool):
        assert tool in ('shell_exec', 'terminal_spawn'), 'unsupported executable guard probe'
        before = pid()
        audit_path = root / 'executor-audit.jsonl'
        audit_offset = audit_path.stat().st_size if audit_path.exists() else 0
        prefix, after = root / (tool + '-prefix'), root / (tool + '-suffix')
        command = 'touch ' + shlex.quote(str(prefix)) + '; sudo -n systemctl stop ' + unit + '; touch ' + shlex.quote(str(after))
        if tool == 'shell_exec':
            result = exec_shell(command)
        else:
            result = spawn(command)
        refusal(result, before, (prefix, after))
        if tool != 'shell_exec' and audit_path.exists():
            new_rows = [json.loads(line) for line in audit_path.read_bytes()[audit_offset:].splitlines()]
            assert not any(row.get('type') == 'child_invocation_intent' and row.get('child_tool') == tool
                           for row in new_rows), 'blocked PTY request reached child invocation'
        return {'pid_before': before, 'pid_after': pid(), 'no_prefix_or_suffix': True}

    def write_input_probe():
        before = pid()
        terminal = pty_payload(call(pty_target, 'terminal_spawn', {
            'shell': '/bin/cat', 'args': [], 'cols': 120, 'rows': 30, 'waitMs': 100}))
        session_id = terminal['sessionId']
        try:
            terminal_pid = terminal.get('pid')
            assert isinstance(terminal_pid, int) and terminal_pid > 0, 'owned cat PTY has no PID'
            process = Path('/proc') / str(terminal_pid)
            assert (process / 'exe').resolve(strict=True) == Path('/bin/cat').resolve(strict=True), \
                'refusing to send command-shaped input to a PTY that is not /bin/cat'
            assert unit in (process / 'cgroup').read_text(), 'cat PTY is outside the owned QA unit'
            assert before > 0 and pid() == before, 'QA Node changed before the data-only write'
            audit_path = root / 'executor-audit.jsonl'
            audit_offset = audit_path.stat().st_size if audit_path.exists() else 0
            data = 'systemctl stop ' + unit + '\n'
            arguments = {'sessionId': session_id, 'data': data}
            # Match the canonical Go json.Marshal(map) used by the real audit.
            encoded = json.dumps(arguments, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()
            input_hash = hashlib.sha256(encoded).hexdigest()
            written = call(pty_target, 'terminal_write', arguments)
            assert 'Wrote ' + str(len(data)) + ' bytes to ' + session_id in json.dumps(written), \
                'ordinary terminal_write input was rejected or not acknowledged'
            deadline = time.monotonic() + 3
            readback = ''
            while time.monotonic() < deadline:
                screen = call(pty_target, 'terminal_screenshot', {'sessionId': session_id, 'format': 'text'})
                readback = '\n'.join(text for text in values(screen, 'text') if isinstance(text, str))
                if data.strip() in readback:
                    break
                time.sleep(0.1)
            assert data.strip() in readback, 'cat PTY did not return the exact command-shaped input'
            assert pid() == before, 'data-only PTY write changed the QA Node PID'
            assert audit_path.exists(), 'terminal_write child audit is missing'
            rows = [json.loads(line) for line in audit_path.read_bytes()[audit_offset:].splitlines()]
            matched = [row for row in rows if row.get('type') == 'child_invocation_intent'
                       and row.get('child_ref') == 'PtyMcp' and row.get('child_tool') == 'terminal_write'
                       and row.get('inputs_sha256') == input_hash
                       and row.get('source') == 'authenticated_mcp' and row.get('client_id') == qa_client_id
                       and row.get('job_id') and row.get('trace_id')
                       and unit in json.dumps(row.get('executor_process', {}))
                       and row.get('native_session_status') in ('not_supplied', 'client_declared', 'not_recorded')]
            assert matched, 'no authenticated child audit intent for the exact terminal_write input'
            return {'input_readback': data.strip(), 'pid_before': before, 'pid_after': pid(),
                    'owned_cat_pid': terminal_pid, 'audit_job_id': matched[0]['job_id'],
                    'audit_trace_id': matched[0]['trace_id']}
        finally:
            if pid() == before:
                killed = call(pty_target, 'terminal_kill', {'sessionId': session_id})
                assert 'Killed session ' + session_id in json.dumps(killed), 'owned cat PTY kill did not succeed'

    def allow_probe():
        before = pid()
        read = exec_shell('sudo -n systemctl is-active ' + unit)
        assert succeeded(read) and 'active' in json.dumps(read), read
        run('systemd-run', '--unit=' + other, '--collect', '--property=User=' + account.pw_name, '/usr/bin/sleep', '120')
        stopped = exec_shell('sudo -n systemctl stop ' + other)
        assert succeeded(stopped) and pid() == before, stopped
        assert run('systemctl', 'is-active', other, check=False).stdout.strip() == 'inactive'
        return {'readonly': 'allowed', 'foreign_owned_stop': 'allowed', 'pid': before}

    def audit_secrets_clean():
        # Read raw complete files, never redacted report copies. Recheck after
        # service cleanup so shutdown records are included in the final gate.
        evidence['audit_secrets'] = []
        for path in sorted(root.rglob('*audit*')):
            if path.is_file():
                content = path.read_bytes()
                clean = all(value.encode() not in content for value in private_values)
                evidence['audit_secrets'].append({'path': str(path.relative_to(root)),
                                                  'bytes': len(content), 'no_secrets': clean})
        return bool(evidence['audit_secrets']) and all(row['no_secrets'] for row in evidence['audit_secrets'])

    def maintenance_probe():
        before = pid()
        audit_path = root / 'executor-audit.jsonl'
        audit_before = audit_path.read_bytes() if audit_path.exists() else b''
        script, done = root / 'maintenance.sh', root / 'maintenance-completed'
        script.write_text('set -eu\nsleep 1\nsystemctl restart ' + shlex.quote(unit)
                          + '\nprintf completed > ' + shlex.quote(str(done)) + '\n')
        script.chmod(0o700)
        launched = spawn('sudo -n systemd-run --collect --unit=' + maintenance + ' /bin/bash ' + shlex.quote(str(script)),
                         env={'QA_AUDIT_SENTINEL': secret_sentinel})
        payload = pty_payload(launched)
        assert payload.get('exitCode') == 0, launched
        deadline = time.monotonic() + 25
        while not done.exists() and time.monotonic() < deadline:
            time.sleep(0.1)
        assert done.exists() and done.read_text() == 'completed', 'maintenance did not survive QA Node restart'
        connect()
        after = pid()
        assert after > 0 and after != before, 'no new QA Node generation'
        result = exec_shell('printf NODE_PTY_AFTER_RESTART')
        assert succeeded(result) and 'NODE_PTY_AFTER_RESTART' in json.dumps(result), result
        evidence['maintenance'] = {'old_pid': before, 'new_pid': after, 'sentinel': str(done)}
        audit_after = audit_path.read_bytes() if audit_path.exists() else b''
        assert audit_before and audit_after.startswith(audit_before), 'durable executor audit missing or lost at restart'
        # Scan complete Hub and executor audit files (including any rotations),
        # not just the subset of intent rows selected for correlation below.
        assert audit_secrets_clean(), 'audit contains a QA credential or secret sentinel'
        rows = [json.loads(line) for line in audit_after.splitlines()]
        valid = [row for row in rows if row.get('job_id') and row.get('trace_id')
                 and unit in json.dumps(row.get('executor_process', {}))
                 and row.get('source') == 'authenticated_mcp'
                 and row.get('client_id') == qa_client_id
                 and row.get('native_session_status') in ('not_supplied', 'client_declared', 'not_recorded')]
        assert any(row.get('type') == 'execution_intent' for row in valid), \
            'no authenticated_mcp execution intent with validated QA client_id and task/trace/cgroup/caller status'
        assert any(row.get('type') == 'child_invocation_intent' and row.get('child_ref') == 'PtyMcp'
                   for row in valid), 'no real PtyMcp task/trace/cgroup/caller invocation intent'
        return {'fresh_MCP_execution': True, 'durable_audit_rows': len(rows), 'correlated_intents': len(valid),
                'source': 'authenticated_mcp', 'client_id': qa_client_id}

    def prove_authenticated_caller():
        marker = 'NODE_QA_AUTHENTICATED_' + suffix
        result = exec_shell('test -n "$QA_AUDIT_SENTINEL" && printf ' + marker,
                            env={'QA_AUDIT_SENTINEL': secret_sentinel})
        assert succeeded(result) and marker in json.dumps(result), 'authenticated QA shell probe failed'
        auth_audit = root / 'hub-audit.jsonl'
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            for line in auth_audit.read_bytes().splitlines() if auth_audit.exists() else []:
                try:
                    row = json.loads(line)
                except ValueError:
                    continue
                fields = row.get('fields') or {}
                claims = fields.get('jwt_claims') or {}
                if row.get('name') == 'mcp_auth_ok' and claims.get('client_id') == qa_client_id:
                    assert claims.get('jti') == registration_token_id, 'validated claims differ from registered QA credential'
                    assert claims.get('sub'), 'validated QA credential has no subject'
                    evidence['authentication']['validated_claims'] = {
                        'client_id': claims['client_id'], 'token_id': claims['jti'],
                        'subject': claims['sub'], 'auth_kind': fields.get('auth_kind'),
                    }
                    return
            time.sleep(0.1)
        raise AssertionError('Hub did not confirm validated QA client claims')

    try:
        start_node()
        connect()
        evidence['initial_pid'] = pid()
        deadline = time.monotonic() + 20
        while True:
            schemas = {tool: schema(target, tool) for target, tool in ((shell_target, 'shell_exec'), (pty_target, 'terminal_spawn'))}
            if all('inputSchema' in json.dumps(value) for value in schemas.values()):
                break
            if time.monotonic() > deadline:
                raise AssertionError('real shell/PtyMcp schemas unavailable: ' + json.dumps(schemas)[:1500])
            time.sleep(0.2)
        save_json(root / 'schemas.json', schemas)
        prove_authenticated_caller()
        for tool in ('shell_exec', 'terminal_spawn'):
            check(tool + '_own_stop_refused', lambda tool=tool: guard_probe(tool))
        check('terminal_write_owned_cat_input_allowed', write_input_probe)
        check('readonly_and_foreign_owned_stop_allowed', allow_probe)
        check('detached_restart_fresh_MCP_and_durable_audit', maintenance_probe)
    except Exception as exc:
        evidence['setup_error'] = redact(type(exc).__name__ + ': ' + str(exc))[:2500]
    finally:
        for name in (maintenance, other, unit):
            run('systemctl', 'stop', name, check=False)
            run('systemctl', 'reset-failed', name, check=False)
        evidence['cleanup'] = {name: run('systemctl', 'is-active', name, check=False).stdout.strip()
                               for name in (maintenance, other, unit)}
        journal = run('journalctl', '--unit=' + unit, '--no-pager', '-n', '100', '-o', 'cat', check=False).stdout
        (root / 'journal.log').write_text(redact(journal))
        env_file.unlink(missing_ok=True)
        no_audit_secrets = audit_secrets_clean()
        evidence['result'] = 'PASS' if (not evidence.get('setup_error') and len(evidence['checks']) == 5
                                       and all(row['result'] == 'PASS' for row in evidence['checks'])
                                       and no_audit_secrets
                                       and all(state == 'inactive' for state in evidence['cleanup'].values())) else 'FAIL'
        save_json(root / 'responses.json', responses)
        save_json(root / 'evidence.json', evidence)
        print(redact(json.dumps(evidence, ensure_ascii=False)))
    return 0 if evidence['result'] == 'PASS' else 1


if __name__ == '__main__':
    raise SystemExit(main())
