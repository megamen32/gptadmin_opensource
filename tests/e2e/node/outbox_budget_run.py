#!/usr/bin/env python3
"""Real standalone Hub/Shell delivery outage on a private 128 MiB ext4 mount.

Run with --shell-binary and --hub-binary. Only owned processes are stopped.
Requires sudo/unshare/mkfs.ext4/mount; no production configuration is read.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--shell-binary', type=Path)
    p.add_argument('--hub-binary', type=Path)
    p.add_argument('--node-binary', type=Path)
    p.add_argument('--case', type=Path)
    p.add_argument('--namespace-child', action='store_true', help=argparse.SUPPRESS)
    a = p.parse_args()
    if bool(a.node_binary) == bool(a.shell_binary or a.hub_binary) or (not a.node_binary and not (a.shell_binary and a.hub_binary)):
        p.error('use --node-binary OR both --shell-binary and --hub-binary')
    project = Path(__file__).resolve().parents[3]
    root = a.case or Path(tempfile.mkdtemp(prefix='outbox-budget-', dir=project / '.tmp'))
    root = root.resolve()
    if not a.namespace_child:
        command = ['sudo', 'unshare', '--mount', '--net', '--propagation', 'private', sys.executable,
                   str(Path(__file__).resolve()), '--namespace-child', '--case', str(root)]
        if a.node_binary:
            command += ['--node-binary', str(a.node_binary.resolve())]
        else:
            command += ['--shell-binary', str(a.shell_binary.resolve()), '--hub-binary', str(a.hub_binary.resolve())]
        raise SystemExit(subprocess.run(command, timeout=180).returncode)
    subprocess.run(['ip', 'link', 'set', 'lo', 'up'], check=True, timeout=5)
    mounted = False
    blocked = False
    processes = []
    report = {'evidence_dir': str(root), 'started_at': time.time(), 'events': [],
              'binaries': {name: {'path': str(path.resolve()), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}
                           for name, path in ([('node', a.node_binary)] if a.node_binary else [('hub', a.hub_binary), ('shell', a.shell_binary)])},
              'mode': 'unified-node' if a.node_binary else 'standalone-hub-shell'}
    started = time.monotonic()
    def event(name, **kw):
        row = dict(event=name, elapsed=round(time.monotonic()-started, 3), **kw)
        report['events'].append(row)
        (root / 'report.json').write_text(json.dumps(report, indent=2))
        print(json.dumps(row), flush=True)
    def port():
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0))
            return s.getsockname()[1]
    token, shelltoken = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
    hubport = port()
    origin = f'http://127.0.0.1:{hubport}'
    rule = ['OUTPUT', '-o', 'lo', '-p', 'tcp', '--dport', str(hubport), '-j', 'REJECT', '--reject-with', 'tcp-reset']
    executor_log = root / ('node.log' if a.node_binary else 'shell.log')
    fs = root / 'fs'
    fs.mkdir()
    env = {k: os.environ[k] for k in ('PATH', 'LANG') if k in os.environ}
    env.update(HOME=str(fs), GPTADMIN_CONFIG_DIR=str(fs / 'hub'), GPTADMIN_ROOT=str(fs),
               GPTADMIN_ENV_FILE=str(fs / 'absent.env'), GPTADMIN_HUB_HOST='127.0.0.1',
               GPTADMIN_HUB_PORT=str(hubport), CTL_TOKEN=token, SHELL_TOKEN=shelltoken,
               OAUTH_CLIENT_SECRET=secrets.token_urlsafe(32), PUBLIC_ORIGIN=origin,
               SHELL_NAME='outbox-budget-canary', SHELL_IDENTITY_DIR=str(fs / 'identity'),
               SHELL_SPOOL_DIR=str(fs / 'spool'), SHELL_OUTBOX_DIR=str(fs / 'spool/outbox'),
               SHELL_DEFAULT_CWD=str(fs), SHELL_DEFAULT_HOME=str(fs / 'home'),
               SHELLMCP_MCP_CONFIG=str(fs / 'no-child.json'), SHELLMCP_AUDIT_LOG=str(fs / 'audit.jsonl'),
               HUB_URL=origin, SHELL_HEARTBEAT='1', SHELLMCP_QUEUE='1', HB_INTERVAL_S='2',
               QUEUE_LONG_POLL_TIMEOUT_S='2', SHELLMCP_QUEUE_RETRY_S='1', POLL_INTERVAL_S='1',
               SHELLMCP_OUTBOX_BACKOFF_BASE_S='1', SHELLMCP_OUTBOX_BACKOFF_CAP_S='3',
               SHELLMCP_SPOOL_RETENTION_S='1', SHELLMCP_SELF_REPAIR_DISABLE='1', TMPDIR=str(fs))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    def request(path, body=None):
        req = urllib.request.Request(origin+path, headers={'Authorization': 'Bearer '+token, 'Content-Type': 'application/json'},
                                     data=None if body is None else json.dumps(body).encode())
        with opener.open(req, timeout=3) as r:
            return json.load(r)
    def launch(name, binary):
        log = (root / (name+'.log')).open('w')
        proc = subprocess.Popen([str(binary.resolve())], env=env, cwd=fs, stdout=log, stderr=log, start_new_session=True)
        log.close()
        processes.append(proc)
        return proc
    def stop(proc):
        if proc.poll() is None:
            os.killpg(proc.pid, signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait(timeout=3)
    def wait(predicate, seconds=25):
        deadline = time.monotonic()+seconds
        while time.monotonic() < deadline:
            try:
                value = predicate()
                if value:
                    return value
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(.1)
        raise AssertionError('bounded condition timeout')
    try:
        image = root / 'volume.ext4'
        with image.open('wb') as f:
            f.truncate(128 << 20)
        subprocess.run(['mkfs.ext4', '-q', '-F', str(image)], check=True, timeout=15)
        subprocess.run(['mount', '-o', 'loop', str(image), str(fs)], check=True, timeout=10)
        mounted = True
        for directory in ('hub', 'identity', 'home', 'spool/outbox'):
            (fs / directory).mkdir(parents=True, exist_ok=True)
        v = os.statvfs(fs)
        budget = min(500 << 20, v.f_blocks*v.f_frsize//20)
        event('filesystem', capacity=v.f_blocks*v.f_frsize, automatic_budget=budget)
        hub = launch('node' if a.node_binary else 'hub-before', a.node_binary or a.hub_binary)
        wait(lambda: request('/version'))
        if not a.node_binary:
            launch('shell', a.shell_binary)
        def registered():
            found = [s for s in request('/servers').get('servers', []) if s.get('name') == 'outbox-budget-canary']
            if found and found[0].get('status') == 'awaiting_approval':
                request('/mcp-relay/call', {'target': 'hub', 'tool': 'approve_pending_server', 'args': {'server_id': 'shell:outbox-budget-canary'}})
            return found
        wait(registered)
        nonce = secrets.token_hex(12)
        jobs = []
        for n in (1, 2):
            code = (f"from pathlib import Path; import time,sys,os; Path('pid{n}').write_text(str(os.getpid())); Path('started{n}').touch(); "
                    f"exec(\"while not Path('release{n}').exists(): time.sleep(.05)\"); "
                    f"f=open('effect{n}','a'); f.write('{nonce}\\n'); f.close(); "
                    + ("sys.stdout.write('x'*(8*1024*1024)); " if n == 1 else '')
                    + f"print('{nonce}-job{n}')")
            result = request('/mcp-relay/call', {'target': 'shell:outbox-budget-canary', 'tool': 'shell_exec',
                'background': True, 'idempotency_key': f'outbox-{nonce}-{n}',
                'args': {'run_as_user': 'root', 'cmd': shlex.quote(sys.executable)+' -c '+shlex.quote(code), 'timeout': 45}})
            jid = result.get('job_id') or result.get('task_id')
            assert jid, result
            jobs.append(jid)
            wait(lambda: (fs / f'started{n}').exists())
        event('running', job_ids=jobs)
        if a.node_binary:
            subprocess.run(['iptables', '-I']+rule, check=True, timeout=5)
            blocked = True
            event('private_netns_callback_port_rejected', node_pid=hub.pid, owned_node_process_count=len(processes), port=hubport)
        else:
            stop(hub)
            event('hub_stopped_actual_delivery_unavailable', exit=hub.returncode)
        (fs / 'release1').touch()
        pending = fs / 'spool/outbox' / (jobs[0]+'.json')
        wait(lambda: pending.exists())
        payload = json.loads(pending.read_text())['payload']
        spill = Path(payload['result']['stdout_path'])
        spill_hash = hashlib.sha256(spill.read_bytes()).hexdigest()
        assert spill.stat().st_size > budget
        (root / 'pending-payload.json').write_text(json.dumps(payload, indent=2))
        event('pending_over_budget', job_id=jobs[0], spill_bytes=spill.stat().st_size,
              spill_sha256=spill_hash, payload_sha256=hashlib.sha256(json.dumps(payload, sort_keys=True).encode()).hexdigest())
        os.utime(spill, (1, 1))
        disposable = fs / 'home/.gptadmin/file-backups/pressure/artifact'
        disposable.parent.mkdir(parents=True)
        disposable.write_bytes(b'd'*(8 << 20))
        (fs / 'release2').touch()
        wait(lambda: (fs / 'spool/outbox' / (jobs[1]+'.json')).exists())
        wait(lambda: not disposable.exists())
        assert pending.exists() and hashlib.sha256(spill.read_bytes()).hexdigest() == spill_hash
        assert json.loads(pending.read_text())['payload'] == payload
        event('cleanup_pressure_preserved_pending', disposable_removed=True, pending_count=2, old_spill_preserved=True)
        # Exhaust only this private filesystem while real delivery retries run.
        filler = fs / 'owned-pressure-fill'
        import errno
        with filler.open('wb', buffering=0) as f:
            try:
                while True:
                    f.write(b'z'*(1 << 20))
            except OSError as exc:
                assert exc.errno == errno.ENOSPC
        wait(lambda: 'outbox retry persist failed' in executor_log.read_text(), seconds=12)
        assert json.loads(pending.read_text())['payload'] == payload
        assert hashlib.sha256(spill.read_bytes()).hexdigest() == spill_hash
        filler.unlink()
        event('actual_enospc_retry_preserved_previous_payload', old_spill_preserved=True)
        if a.node_binary:
            assert hub.poll() is None, 'Node died during delivery failure'
            subprocess.run(['iptables', '-D']+rule, check=True, timeout=5)
            blocked = False
            event('same_node_callback_route_restored', node_pid=hub.pid)
        else:
            hub = launch('hub-after', a.hub_binary)
        wait(lambda: request('/version'))
        receipts = []
        for jid in jobs:
            receipt = wait(lambda: (r if (r := request('/mcp-relay/job/'+jid)).get('status') == 'completed' else None))
            assert nonce in receipt.get('stdout', ''), receipt
            receipts.append(receipt)
        wait(lambda: not list((fs / 'spool/outbox').glob('*.json')))
        for jid, receipt in zip(jobs, receipts):
            for _ in range(3):
                assert request('/mcp-relay/job/'+jid) == receipt
        for n in (1, 2):
            assert (fs / f'effect{n}').read_text().splitlines() == [nonce]
        assert hashlib.sha256(spill.read_bytes()).hexdigest() == spill_hash
        assert 'connection refused' in executor_log.read_text()
        assert 'storage mandatory data retained:' in executor_log.read_text()
        (root / 'receipts.json').write_text(json.dumps(receipts, indent=2))
        report['ok'] = True
        event('delivered_once', job_ids=jobs, side_effect_counts=[1, 1], repeated_reads_each=3, outbox_remaining=0, spill_sha256=spill_hash)
    finally:
        if blocked:
            subprocess.run(['iptables', '-D']+rule, check=True, timeout=5)
        for proc in reversed(processes):
            stop(proc)
        if mounted:
            # Callback commands have their own process groups; clean only the
            # groups containing task-recorded Python PIDs before unmounting.
            for path in fs.glob('pid[12]'):
                try:
                    os.killpg(os.getpgid(int(path.read_text())), signal.SIGKILL)
                except ProcessLookupError:
                    pass
            subprocess.run(['umount', str(fs)], check=True, timeout=10)
        report['finished_at'] = time.time()
        event('cleanup', owned_processes_stopped=all(p.poll() is not None for p in processes), private_mount_removed=True)


if __name__ == '__main__':
    main()
