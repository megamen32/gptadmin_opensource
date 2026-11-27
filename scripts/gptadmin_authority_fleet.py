#!/usr/bin/env python3
"""Execute one reviewed ordinary authority transfer from an external controller.

The manifest pins all node runners, helpers, direct SSH hops and edge scripts.
No GPTAdmin/MCP call or GitHub operation is used after the first admission fence.
Only a fresh complete reverse authority image can recover after activation.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess
import sys
import threading
import time

from gptadmin_authority_controller import Refused, checked_file, private_json, read_json, sync_dir


class EdgeOutcomeUnknown(Refused):
    pass


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def routed_argv(hops, argv):
    """Quote each remote argv once for its own SSH shell boundary."""
    current = list(argv)
    for hop in reversed(hops):
        current = list(hop) + [shlex.join(current)]
    return current


class Fleet:
    def __init__(self, path, expected):
        checked_file(path, expected)
        self.m = read_json(path)
        if self.m.get('schema') != 'gptadmin.authority-fleet/v1':
            raise Refused('unknown fleet manifest')
        checked_file(str(Path(__file__).resolve()), self.m['fleet_sha256'])
        checked_file(str(Path(__file__).resolve().with_name('gptadmin_authority_controller.py')),
                     self.m['controller_module_sha256'])
        self.manifest_hash = expected
        self.root = Path(self.m['operation_dir'])
        self.nodes = self.m['nodes']
        if set(self.nodes) != {'100', '44', '88', 'mac_mini', 'mac_intel', 'haos'}:
            raise Refused('reviewed active-reader scope is incomplete or enlarged')
        if self.m['source'] != '100' or self.m['destination'] != '44':
            raise Refused('manifest does not name the reviewed transfer')
        self.lock = threading.Lock()
        self.state = {'schema': 'gptadmin.authority-fleet-result/v1', 'manifest_sha256': expected,
                      'phase': 'starting', 'capture': {}, 'frozen': [], 'forward': {},
                      'reverse': {}, 'quiescence': {}, 'activation_authorized': False, 'started_at': time.time()}

    def save(self, phase=None):
        with self.lock:
            if phase:
                self.state['phase'] = phase
            private_json(self.root / 'transaction.json', self.state)

    def remote(self, node, argv, data=None, timeout=180, sudo=True):
        cfg = self.nodes[node]
        prefix = list(cfg.get('sudo', [])) if sudo else []
        result = subprocess.run(routed_argv(cfg['ssh_hops'], prefix + list(argv)),
                                input=data, capture_output=True, timeout=timeout)
        if result.returncode:
            raise Refused('external node command failed on %s, rc=%s' % (node, result.returncode))
        return result.stdout

    def node(self, name, step, action, *args):
        cfg = self.nodes[name]
        # Compile the exact pinned bytes rather than hash-then-exec a pathname.
        shim = ('import sys,pathlib,hashlib; p=pathlib.Path(sys.argv[1]); b=p.read_bytes(); '
                'assert hashlib.sha256(b).hexdigest()==sys.argv[2],"runner digest changed"; '
                'sys.argv=[str(p)]+sys.argv[3:]; exec(compile(b,str(p),"exec"))')
        argv = [cfg.get('python', 'python3'), '-c', shim, cfg['runner'], cfg['runner_sha256'],
                '--manifest', cfg['manifest'], '--manifest-sha256', cfg['manifest_sha256'],
                '--step', step, action] + list(args)
        raw = self.remote(name, argv)
        receipt = json.loads(raw)
        if receipt.get('status') != 'completed':
            raise Refused('node action has no completed durable receipt')
        private_json(self.root / ('receipt-' + name + '-' + step + '.json'), receipt, True)
        return receipt['result']

    def transfer_file(self, source, source_path, target, target_path):
        raw = self.remote(source, ['cat', source_path], timeout=60)
        if len(raw) > 256 * 1024 * 1024:
            raise Refused('private state transfer exceeds reviewed budget')
        program = ('import sys,pathlib,os,hashlib\np=pathlib.Path(sys.argv[1]); b=sys.stdin.buffer.read()\n'
                   'assert hashlib.sha256(b).hexdigest()==sys.argv[2]\n'
                   'fd=os.open(str(p),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)\n'
                   'with os.fdopen(fd,"wb") as f:\n f.write(b); f.flush(); os.fsync(f.fileno())\n'
                   'd=os.open(str(p.parent),os.O_RDONLY); os.fsync(d); os.close(d)')
        self.remote(target, [self.nodes[target].get('python', 'python3'), '-c', program, target_path, sha(raw)], raw, 60)
        return sha(raw)

    def mkdir(self, name, path):
        program = ('import os,sys,pathlib; p=pathlib.Path(sys.argv[1]); p.mkdir(mode=0o700); '
                   'd=os.open(str(p.parent),os.O_RDONLY); os.fsync(d); os.close(d)')
        self.remote(name, [self.nodes[name].get('python', 'python3'), '-c', program, path])

    def parallel(self, names, function):
        results = {}
        errors = []
        with ThreadPoolExecutor(max_workers=len(names)) as pool:
            pending = {name: pool.submit(function, name) for name in names}
            for name, future in pending.items():
                try:
                    results[name] = future.result()
                except Exception as error:
                    errors.append((name, type(error).__name__))
        if errors:
            self.state['parallel_errors'] = errors
            self.save()
            raise Refused('a bounded fleet phase failed; exact node receipts retained')
        return results

    def path(self, node, suffix):
        return str(Path(self.nodes[node]['operation_dir']) / suffix)

    def helper(self, node, direction, step, args):
        if direction == 'forward':
            return self.node(node, direction + '-' + step, 'helper', *args)
        prior = self.state['forward']['prepared'][node]
        return self.node(node, direction + '-' + step, 'helper-from-applied',
                         self.path(node, 'forward-backup'), prior['plan_digest'], *args)

    def prepare(self, direction, source, writer):
        data = self.state[direction]
        source_id = self.nodes[source]['local_id']
        source_image = self.path(source, direction + '-image')
        export_args = ['export', '--config-dir', self.nodes[source]['config_dir'], '--target', self.nodes[source]['target'],
                       '--expect-writer', source_id, '--output', source_image]
        if direction == 'reverse':
            baseline = self.state['forward']['retained_legacy_baseline']
            if baseline['physical_owner'] != self.nodes[source]['target'] or not baseline['sha256']:
                raise Refused('pre-forward physical history baseline is not frozen')
            export_args += ['--retained-legacy-baseline', baseline['file'],
                            '--retained-legacy-baseline-sha256', baseline['sha256']]
        data['export'] = self.helper(source, direction, 'export', export_args)
        self.save(direction + '_exported')
        image_at_writer = self.path(writer, direction + '-source-image')
        self.mkdir(writer, image_at_writer)
        actual = self.transfer_file(source, source_image + '/bundle.json', writer, image_at_writer + '/bundle.json')
        if actual != data['export']['bundle_digest']:
            raise Refused('transferred source image digest differs from export receipt')
        if data['export']['content_copied']:
            self.transfer_file(source, source_image + '/exec-content.sqlite', writer, image_at_writer + '/exec-content.sqlite')
        prepared = self.helper(writer, direction, 'prepare',
            ['prepare-writer', '--config-dir', self.nodes[writer]['config_dir'], '--target', self.nodes[writer]['target'],
             '--expect-local-id', self.nodes[writer]['local_id'], '--bundle', image_at_writer,
             '--bundle-sha256', data['export']['bundle_digest'], '--source-peer-url', self.nodes[source]['physical_origin'],
             '--environment-file', self.nodes[writer]['environment_file'], '--output', self.path(writer, direction + '-stage')])
        data['prepared'] = {writer: prepared}
        self.save(direction + '_writer_prepared')

        def prepare_reader(name):
            local_transition = self.path(name, direction + '-reader-transition.json')
            self.transfer_file(writer, prepared['directory'] + '/reader-transition.json', name, local_transition)
            args = ['prepare-reader', '--config-dir', self.nodes[name]['config_dir'], '--target', self.nodes[name]['target'],
                    '--expect-local-id', self.nodes[name]['local_id'], '--transition', local_transition,
                    '--transition-sha256', prepared['transition_digest'], '--output', self.path(name, direction + '-stage')]
            if name == 'haos':
                args += ['--identity-derived-entrypoint', '/run.sh', '--entrypoint-sha256', self.nodes[name]['entrypoint_sha256']]
                args += ['--identity-derived-seed-alias', self.nodes[name]['identity_seed_alias'],
                         '--identity-derived-options', '/data/options.json',
                         '--options-sha256', self.nodes[name]['identity_options_sha256']]
            else:
                args += ['--environment-file', self.nodes[name]['environment_file']]
                if name == '88':
                    args += ['--listen-host', '0.0.0.0']
            return self.helper(name, direction, 'prepare', args)

        data['prepared'].update(self.parallel([x for x in self.nodes if x != writer], prepare_reader))
        self.save(direction + '_all_prepared')

    def apply(self, direction):
        data = self.state[direction]
        data['apply_attempted'] = list(self.nodes)
        self.save(direction + '_applying')
        def apply_node(name):
            row = data['prepared'][name]
            return self.helper(name, direction, 'apply',
                ['apply', '--config-dir', self.nodes[name]['config_dir'], '--prepared', row['directory'],
                 '--plan-sha256', row['plan_digest'], '--backup', self.path(name, direction + '-backup')])
        data['applied'] = self.parallel(list(self.nodes), apply_node)
        self.save(direction + '_all_applied')

    def authorize_and_start(self, direction, writer):
        data = self.state[direction]
        if direction == 'forward' and not self.state['forward'].get('retained_legacy_baseline', {}).get('sha256'):
            raise Refused('the physical reverse history baseline must be frozen before activation')
        self.state['activation_authorized'] = True
        self.save(direction + '_global_activation_authorized')
        # Durably retire recovery on every node before starting the one writer.
        def authorize(name):
            row = data['prepared'][name]
            # For reverse the effective environment has now changed again;
            # authorization only needs canonical config paths from capture.
            return self.node(name, direction + '-authorize', 'helper', 'authorize-activation',
                             '--config-dir', self.nodes[name]['config_dir'], '--backup', self.path(name, direction + '-backup'),
                             '--plan-sha256', row['plan_digest'])
        data['authorized'] = self.parallel(list(self.nodes), authorize)
        data['runtime'] = {}
        row = data['prepared'][writer]
        data['runtime'][writer] = self.node(writer, direction + '-start', 'start-applied',
                                           self.path(writer, direction + '-backup'), row['plan_digest'])
        # Offline-signer readers can prove coherent local startup before the
        # public origin changes. Their real writer heartbeat is checked after it.
        local_readers = ['88'] if direction == 'forward' else ['88', '44']
        for name in local_readers:
            row = data['prepared'][name]
            data['runtime'][name] = self.node(name, direction + '-start', 'start-applied',
                                             self.path(name, direction + '-backup'), row['plan_digest'])
        self.save(direction + '_local_ready')

    def readiness(self, direction, source, writer):
        data = self.state[direction]
        source_preflight = self.state['capture'][source]['preflight']
        if direction == 'reverse':
            # This is the actual fresh image after all new admissions, not the
            # old startup generation saved before those operations happened.
            source_preflight = data['stopped_source_preflight']
        else:
            source_preflight = data['stopped_source_preflight']
        result = {'schema': 'gptadmin.authority-cutover-ready/v1' if direction == 'forward' else 'gptadmin.authority-cutover-reverse-ready/v1',
                  'phase': 'writer44_ready_edge_pending' if direction == 'forward' else 'writer100_ready_reverse_edge_pending',
                  'operation_id': self.m['operation_id'], 'helper': self.m['helper'],
                  'source_export': data['export'], 'source_preflight': source_preflight,
                  'writer_prepare': data['prepared'][writer], 'writer_runtime': data['runtime'][writer],
                  'reader88_prepare': data['prepared']['88'], 'reader88_runtime': data['runtime']['88']}
        if direction == 'reverse':
            result['reader44_prepare'] = data['prepared']['44']
            result['reader44_runtime'] = data['runtime']['44']
        path = self.root / (direction + '-ready.json')
        private_json(path, result, True)
        return path

    def edge(self, direction, ready):
        edge = self.m['edge'][direction]
        raw = ready.read_bytes()
        shim = ('import sys,pathlib,hashlib; p=pathlib.Path(sys.argv[1]); b=p.read_bytes(); '
                'assert hashlib.sha256(b).hexdigest()==sys.argv[2],"edge dispatcher changed"; '
                'sys.argv=[str(p)]+sys.argv[3:]; exec(compile(b,str(p),"exec"))')
        flag = '--ready-sha256' if direction == 'forward' else '--reverse-ready-sha256'
        argv = ['python3', '-c', shim, edge['dispatcher'], edge['sha256'], flag, sha(raw)]
        self.state[direction]['edge_dispatch_attempted'] = True
        self.save(direction + '_edge_dispatching')
        try:
            result = self.remote('100', argv, raw, 120, sudo=False)
            receipt = json.loads(result)
            private_json(self.root / (direction + '-edge-receipt.json'), receipt, True)
            self.state[direction]['edge_receipt'] = receipt
        except (Refused, OSError, ValueError, subprocess.TimeoutExpired):
            # Never replay a possibly admitted dispatch. Its sole next action
            # is read-only inspection of the dispatcher's durable transaction.
            self.state[direction]['edge_response_unavailable'] = True
            self.save(direction + '_edge_resolving')
        if direction == 'forward':
            self.resolve_forward_edge()
        else:
            status = self.edge_status(direction)
            remote = status.get('remote', {})
            transaction = remote.get('reverse-transaction.json', {})
            if remote.get('lockPresent') or remote.get('liveSha256') != edge['config_sha256'] or transaction.get('phase') != 'edge_active_pending_public_validation':
                raise EdgeOutcomeUnknown('reverse ingress outcome requires exact out-of-band reconciliation')
        self.save(direction + '_edge_applied')

    def edge_status(self, direction='forward'):
        edge = self.m['edge'][direction]
        shim = ('import sys,pathlib,hashlib; p=pathlib.Path(sys.argv[1]); b=p.read_bytes(); '
                'assert hashlib.sha256(b).hexdigest()==sys.argv[2],"edge dispatcher changed"; '
                'sys.argv=[str(p)]+sys.argv[3:]; exec(compile(b,str(p),"exec"))')
        raw = self.remote('100', ['python3', '-c', shim, edge['dispatcher'], edge['sha256'], '--status'], timeout=30, sudo=False)
        result = json.loads(raw)
        private_json(self.root / ('edge-status-' + direction + '.json'), result)
        return result

    def resolve_forward_edge(self):
        deadline = time.monotonic() + 55
        while time.monotonic() < deadline:
            try:
                status = self.edge_status()
            except (Refused, OSError, ValueError, subprocess.TimeoutExpired):
                status = {}
            outcome = status.get('forwardResolution', {}).get('outcome', 'unknown')
            self.state['forward']['edge_resolution'] = outcome
            self.save('forward_edge_resolved_' + outcome)
            remote = status.get('remote', {})
            if outcome == 'installed' and not remote.get('lockPresent') and remote.get('liveSha256') == self.m['edge']['forward']['config_sha256']:
                phase = remote.get('transaction.json', {}).get('phase')
                if phase == 'edge_active_pending_public_validation':
                    return
                if phase == 'failed_after_swap':
                    raise Refused('forward ingress reload failed after swap; fresh reverse authority is required')
            if outcome in {'failed_before_swap', 'not_dispatched'}:
                raise Refused('forward ingress was proven not installed; fresh reverse authority is required')
            time.sleep(1)
        raise EdgeOutcomeUnknown('forward ingress remains uncertain; keep the one writer and inspect the exact durable transaction')

    def finish_readers(self, direction, writer):
        data = self.state[direction]
        for name in self.nodes:
            if name in data['runtime']:
                continue
            row = data['prepared'][name]
            data['runtime'][name] = self.node(name, direction + '-start', 'start-applied',
                                             self.path(name, direction + '-backup'), row['plan_digest'])
        self.save(direction + '_all_running')
        # Exercise the real public writer's node-auth handler and wait for all
        # retained active readers' genuine heartbeat/snapshot convergence.
        expected = {'writer_id': self.nodes[writer]['local_id'],
                    'auth_generation': data['runtime'][writer]['preflight']['auth_generation'], 'members': {}}
        for name, node in self.nodes.items():
            before = self.state['capture'][name]['preflight']
            member_id = node['local_id'] if name == writer else before.get('node_id', node['local_id'])
            expected['members'][node['target']] = {'node_id': member_id, 'local_id': node['local_id'],
                                                 'endpoint': node['physical_origin']}
        prepared = data['prepared'][writer]
        result = self.node(writer, direction + '-membership-acceptance', 'verify-membership',
                           prepared['directory'] + '/reader-transition.json', prepared['transition_digest'],
                           json.dumps(expected, separators=(',', ':')))
        data['acceptance'] = result
        self.save(direction + '_authenticated_membership_verified')
        for name in self.nodes:
            self.node(name, direction + '-auxiliary-restore', 'resume-auxiliaries')
        self.save('deployed_pending_independent_mcp_acceptance' if direction == 'forward' else 'recovered_original_writer_with_fresh_state')

    def rollback_before_activation(self):
        if self.state['activation_authorized']:
            raise Refused('old snapshots are globally retired')
        data = self.state['forward']
        quiet = self.state.get('quiescence', {}).get('forward')
        if quiet is not None and not data.get('apply_attempted'):
            return self.restore_fences_before_activation(quiet)
        # Restore every attempted apply, including calls whose response was
        # lost. Missing journal is acceptable only with exact all-before proof.
        for name in data.get('apply_attempted', []):
            row = data['prepared'][name]
            backup = self.path(name, 'forward-backup')
            probe = self.remote(name, [self.nodes[name].get('python', 'python3'), '-c',
                'import pathlib,sys,json;print(json.dumps({"journal":(pathlib.Path(sys.argv[1])/"rollback.json").exists()}))', backup])
            if json.loads(probe)['journal']:
                self.node(name, 'recovery-restore', 'helper', 'restore-before-activation',
                          '--config-dir', self.nodes[name]['config_dir'], '--backup', backup,
                          '--plan-sha256', row['plan_digest'])
                self.node(name, 'recovery-start', 'start-restored', backup, row['plan_digest'])
            else:
                self.node(name, 'recovery-start', 'start-unchanged-plan', row['directory'], row['plan_digest'], backup)
        for name in self.state['frozen']:
            if name not in data.get('apply_attempted', []):
                self.node(name, 'recovery-original', 'restore-unapplied')
        self.parallel(list(self.nodes), lambda n: self.node(n, 'preactivation-auxiliary-restore', 'resume-auxiliaries'))
        self.save('restored_before_activation')

    def quiesce(self, direction, writer):
        """Close writer admission first, keep callbacks live, stop writer last."""
        phases = self.state.setdefault('quiescence', {})
        if direction in phases:
            raise Refused('quiescence was already attempted; inspect its exact receipts')
        quiet = {'writer': writer, 'fence_attempted': [], 'fenced': {},
                 'stop_attempted': [], 'stopped': {}}
        phases[direction] = quiet
        self.save(direction + '_fencing_writer')

        def arguments(name):
            if direction == 'forward':
                return []
            row = self.state['forward']['prepared'][name]
            return [self.path(name, 'forward-backup'), row['plan_digest']]

        def fence(name):
            quiet['fence_attempted'].append(name)
            self.save(direction + '_fencing_' + name)
            action = 'fence' if direction == 'forward' else 'fence-applied'
            result = self.node(name, direction + '-fence', action, *arguments(name))
            quiet['fenced'][name] = result
            if 'stopped' in result:
                quiet['stopped'][name] = result
            self.save(direction + '_fenced_' + name)

        def stop(name):
            if name not in quiet['stopped']:
                quiet['stop_attempted'].append(name)
                self.save(direction + '_stopping_' + name)
                action = ('stop-fenced-writer' if direction == 'forward' else 'stop-fenced-writer-applied') if name == writer else ('stop-fenced' if direction == 'forward' else 'stop-fenced-applied')
                quiet['stopped'][name] = self.node(name, direction + '-stop', action, *arguments(name))
            if direction == 'forward':
                self.state['frozen'].append(name)
            self.save(direction + '_stopped_' + name)

        fence(writer)
        self.save(direction + '_checking_writer_ledger')
        action = 'verify-fenced-ledger' if direction == 'forward' else 'verify-fenced-ledger-applied'
        quiet['early_authority_ledger'] = self.node(writer, direction + '-early-ledger', action, *arguments(writer))
        self.save(direction + '_writer_ledger_empty')
        for name in self.nodes:
            if name != writer:
                fence(name)
                stop(name)
        stop(writer)

    def restore_fences_before_activation(self, quiet):
        # No authority apply has occurred. A writer whose stop was not
        # attempted stays listening while independently recovering readers.
        writer = quiet['writer']
        readers = [name for name in quiet['fence_attempted'] if name != writer]
        order = ([writer] + readers if writer in quiet['stop_attempted'] else
                 readers + ([writer] if writer in quiet['fence_attempted'] else []))
        recovery = {'attempted': [], 'completed': {}, 'errors': {}}
        quiet['recovery'] = recovery
        for name in order:
            recovery['attempted'].append(name)
            self.save('recovering_original_' + name)
            try:
                recovery['completed'][name] = self.node(name, 'recovery-fence', 'restore-fenced')
            except Exception as error:
                # A failed or uncertain reader recovery must not strand the
                # other unchanged readers or the still-listening writer.
                recovery['errors'][name] = type(error).__name__
            self.save()
        try:
            self.parallel(list(self.nodes), lambda n: self.node(n, 'preactivation-auxiliary-restore', 'resume-auxiliaries'))
        except Exception as error:
            recovery['errors']['auxiliaries'] = type(error).__name__
        if recovery['errors']:
            self.save('preactivation_recovery_incomplete')
            raise Refused('some original runtimes or auxiliaries require receipt reconciliation')
        self.save('restored_before_activation')

    def reverse(self):
        # No old backup is read as a source of authority after this point.
        self.quiesce('reverse', '44')
        self.save('reverse_all_stopped')
        old = self.state['forward']['prepared']['44']
        self.state['reverse']['stopped_source_preflight'] = self.node('44', 'reverse-source-preflight', 'helper-from-applied',
            self.path('44', 'forward-backup'), old['plan_digest'], 'preflight',
            '--config-dir', self.nodes['44']['config_dir'], '--target', self.nodes['44']['target'],
            '--expect-local-id', self.nodes['44']['local_id'])
        self.prepare('reverse', '44', '100')
        self.apply('reverse')
        self.authorize_and_start('reverse', '100')
        ready = self.readiness('reverse', '44', '100')
        self.edge('reverse', ready)
        self.finish_readers('reverse', '100')

    def await_launch_gate(self):
        """Admit the reviewed operation only after its MCP launcher completed.

        The human-authorized root operator writes this private gate after
        reading the real launch task receipt. We independently read that exact
        canonical ledger row over SSH; the launcher's stdout must identify this
        already running external unit/PID and this exact fleet manifest.
        """
        gate_path = Path(self.m['launch_gate_path'])
        deadline = time.monotonic() + 300
        self.save('awaiting_external_launch_acceptance')
        while time.monotonic() < deadline:
            if gate_path.exists():
                info = gate_path.lstat()
                if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
                    raise Refused('launch acceptance is not a private operator-owned regular file')
                gate = read_json(gate_path)
                if (gate.get('schema') != 'gptadmin.authority-launch-gate/v1'
                        or gate.get('operation_id') != self.m['operation_id']
                        or gate.get('manifest_sha256') != self.manifest_hash
                        or gate.get('launcher_owner_target') != 'shell:admin-node-100'):
                    raise Refused('launch acceptance does not bind this reviewed operation')
                task_id = gate.get('launcher_task_id', '')
                if not isinstance(task_id, str) or not task_id or len(task_id) > 128:
                    raise Refused('launch acceptance lacks an exact native task ID')
                expected = {'operation_id': self.m['operation_id'], 'manifest_sha256': self.manifest_hash,
                            'unit': self.m['external_unit'], 'main_pid': os.getpid()}
                program = ('import sqlite3,json,sys\n'
                           'd=sqlite3.connect("file:/etc/gptadmin/tasks_state.sqlite?mode=ro",uri=True)\n'
                           'r=d.execute("SELECT kind,done,payload FROM task_records WHERE key=?",(sys.argv[1],)).fetchone()\n'
                           'out={"found":bool(r),"terminal_verified":False}\n'
                           'if r:\n'
                           ' v=json.loads(r[2]); z=v.get("result",{}); expected=json.loads(sys.argv[2])\n'
                           ' try: payload=json.loads(z.get("stdout",""))\n'
                           ' except (ValueError,TypeError): payload={}\n'
                           ' out.update(status=v.get("status"),returncode=z.get("returncode"),task_id=v.get("id"))\n'
                           ' out["terminal_verified"]=(r[0]=="shell" and r[1]>0 and v.get("id")==sys.argv[1] '
                           'and v.get("server")=="admin-node-100" and v.get("tool_name")=="shell_exec" '
                           'and v.get("status")=="completed" and z.get("returncode")==0 '
                           'and all(payload.get(k)==value for k,value in expected.items()))\n'
                           'print(json.dumps(out))')
                proof = json.loads(self.remote('100', ['python3', '-c', program, task_id,
                                                       json.dumps(expected, separators=(',', ':'))], timeout=20))
                if not proof.get('terminal_verified'):
                    raise Refused('the exact MCP launcher is not successfully terminal for this external unit')
                self.state['launch_acceptance'] = {'gate_sha256': sha(gate_path.read_bytes()),
                                                   'launcher_task': proof, 'external_unit': self.m['external_unit']}
                self.save('external_launch_accepted')
                return
            time.sleep(.5)
        raise Refused('external launch acceptance deadline reached before any admission fence')

    def run(self):
        self.root.mkdir(mode=0o700)
        sync_dir(self.root.parent)
        lock = os.open(str(self.root / 'fleet.lock'), os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            return self.run_locked()
        finally:
            os.close(lock)

    def run_locked(self):
        own_cgroup = Path('/proc/self/cgroup').read_text()
        if any(unit in own_cgroup for unit in self.m['affected_controller_units']):
            raise Refused('fleet runner is inside an affected controller cgroup')
        self.state['external_controller'] = {'pid': os.getpid(), 'cgroup': own_cgroup,
                                             'hostname': os.uname().nodename}
        try:
            self.await_launch_gate()
            self.save('capturing')
            self.state['capture'] = self.parallel(list(self.nodes), lambda n: self.node(n, 'capture', 'capture'))
            self.save('captured')
        except Exception:
            # Publication paused the original updater states after capture.
            # A pre-fence launcher failure must restore those same states.
            self.parallel(list(self.nodes), lambda n: self.node(n, 'prelaunch-auxiliary-restore', 'resume-auxiliaries'))
            self.save('prelaunch_aborted_auxiliaries_restored')
            raise
        try:
            self.quiesce('forward', '100')
            self.save('all_stopped')
            self.state['forward']['retained_legacy_baseline'] = self.node('44', 'forward-baseline', 'helper',
                'capture-legacy-baseline', '--config-dir', self.nodes['44']['config_dir'], '--target', self.nodes['44']['target'],
                '--expect-local-id', self.nodes['44']['local_id'], '--output', self.path('44', 'retained-before-forward.json'))
            self.state['forward']['stopped_source_preflight'] = self.node('100', 'forward-source-preflight', 'helper',
                'preflight', '--config-dir', self.nodes['100']['config_dir'], '--target', self.nodes['100']['target'],
                '--expect-local-id', self.nodes['100']['local_id'])
            self.save('all_stopped_baseline_frozen')
            self.prepare('forward', '100', '44')
            self.apply('forward')
            self.authorize_and_start('forward', '44')
            ready = self.readiness('forward', '100', '44')
            self.edge('forward', ready)
            self.finish_readers('forward', '44')
        except Exception as error:
            self.state['failure_class'] = type(error).__name__
            if isinstance(error, EdgeOutcomeUnknown):
                self.save('edge_outcome_unknown_reconciliation_required')
                raise
            self.save('recovering')
            if self.state['activation_authorized']:
                self.reverse()
            else:
                self.rollback_before_activation()
            raise Refused('transfer did not pass; see the exact durable recovery outcome') from None
        return self.state


def launch_external(manifest_path, manifest_hash):
    """Dispatch one independently managed44 unit; never wait for its cutover."""
    checked_file(manifest_path, manifest_hash)
    m = read_json(manifest_path)
    if m.get('schema') != 'gptadmin.authority-fleet/v1' or m.get('source') != '100' or m.get('destination') != '44':
        raise Refused('launcher manifest is outside the reviewed transfer')
    checked_file(str(Path(__file__).resolve()), m['fleet_sha256'])
    checked_file(str(Path(__file__).resolve().with_name('gptadmin_authority_controller.py')), m['controller_module_sha256'])
    claim = Path(m['launcher_receipt_path'])
    if claim.exists():
        raise Refused('this external launch was already dispatched; inspect its exact task/unit instead of repeating it')
    request = {'schema': 'gptadmin.authority-launch-dispatch/v1', 'manifest_sha256': manifest_hash,
               'operation_id': m['operation_id'], 'unit': m['external_unit'], 'status': 'dispatching', 'at': time.time()}
    private_json(claim, request, True)
    program = (
        'import pathlib,subprocess,json,hashlib,sys,time\n'
        'p=pathlib.Path(sys.argv[1]); raw=p.read_bytes(); assert hashlib.sha256(raw).hexdigest()==sys.argv[2]\n'
        'm=json.loads(raw); unit=m["external_unit"]\n'
        'prior=subprocess.run(["systemctl","show",unit,"-p","LoadState","--value"],capture_output=True,text=True,check=True).stdout.strip()\n'
        'assert prior=="not-found","operation unit already exists; reconcile without repeating admission"\n'
        'argv=["sudo","-n","systemd-run","--unit="+unit,"--uid=admin","--property=Type=exec",'
        '"--property=Restart=no","--property=KillMode=mixed","--property=TimeoutStopSec=20s",'
        '"--working-directory="+str(p.parent),"/usr/bin/python3",str(p.parent/"gptadmin_authority_fleet.py"),'
        '"--manifest",str(p),"--manifest-sha256",sys.argv[2]]\n'
        'r=subprocess.run(argv,capture_output=True,text=True,timeout=25); assert r.returncode==0,"systemd external dispatch did not confirm"\n'
        'deadline=time.monotonic()+5; values={}\n'
        'while time.monotonic()<deadline:\n'
        ' raw=subprocess.run(["systemctl","show",unit,"-p","MainPID","-p","ActiveState","-p","ControlGroup"],capture_output=True,text=True,check=True).stdout\n'
        ' values=dict(line.split("=",1)for line in raw.splitlines())\n'
        ' if values.get("ActiveState")=="active" and int(values.get("MainPID","0"))>0:break\n'
        ' time.sleep(.1)\n'
        'assert values.get("ActiveState")=="active" and int(values.get("MainPID","0"))>0,"external process not active"\n'
        'assert values["ControlGroup"].endswith("/"+unit),"external unit cgroup mismatch"\n'
        'print(json.dumps({"operation_id":m["operation_id"],"manifest_sha256":sys.argv[2],"unit":unit,'
        '"main_pid":int(values["MainPID"]),"cgroup":values["ControlGroup"],"phase":"waiting_for_operator_gate"}))')
    argv = list(m['launcher_ssh_argv']) + [shlex.join(['python3', '-c', program,
                                                    m['manifest_on44'], manifest_hash])]
    result = subprocess.run(argv, capture_output=True, timeout=40)
    if result.returncode:
        raise Refused('external launch outcome needs exact unit/task reconciliation; dispatch claim retained')
    receipt = json.loads(result.stdout)
    if any(receipt.get(k) != request[k] for k in ('operation_id', 'manifest_sha256', 'unit')) or not receipt.get('main_pid'):
        raise Refused('external launch receipt did not bind the reviewed unit')
    request.update({'status': 'dispatched', 'receipt': receipt})
    private_json(claim, request)
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', required=True)
    parser.add_argument('--manifest-sha256', required=True)
    parser.add_argument('--launch', action='store_true', help='dispatch the independent44 unit once from100')
    args = parser.parse_args()
    if args.launch:
        print(json.dumps(launch_external(args.manifest, args.manifest_sha256)))
        return
    result = Fleet(args.manifest, args.manifest_sha256).run()
    print(json.dumps({'phase': result['phase'], 'manifest_sha256': result['manifest_sha256']}))


if __name__ == '__main__':
    try:
        main()
    except (Refused, OSError, ValueError, subprocess.TimeoutExpired) as error:
        print('authority fleet: ' + (str(error) if isinstance(error, Refused) else type(error).__name__), file=sys.stderr)
        raise SystemExit(1)
