"""Authority-only ledger and single-start recovery regressions."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import gptadmin_authority_controller as C
import gptadmin_authority_fleet as F


class ControllerFixture(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.work = Path(self.tmp.name)
        self.good = self.work / 'reviewed-node'
        self.bad = self.work / 'pre-exec-launcher'
        for path, body in [(self.good, b'reviewed binary'), (self.bad, b'launcher')]:
            path.write_bytes(body)
            path.chmod(0o700)
        self.binary = self.good
        self.info = {'pid': 77, 'generation': 'original', 'unit_metadata_sha256': 'original-unit'}
        self.original = dict(self.info)
        self.events = []
        self.clock = 0
        self.drain = {'draining': True, 'reason': 'owned', 'since': 'original-fence',
                      'ready_to_stop': True, 'counters': dict.fromkeys(
                          ('active_submissions', 'active_callbacks', 'unfinished_executions', 'outbox_pending'), 0)}
        self.view = {'auth_mode': 'writer', 'auth_pending': False, 'auth_state_stable_during_read': True,
                     'local_id': 'writer-id', 'target': 'shell:writer', 'config_dir': '/fixture/config',
                     'pending_task_records': 0, 'pending_task_controls': 0}
        runner = object.__new__(C.Controller)
        runner.work = self.work
        runner.manifest_hash = 'a' * 64
        runner.m = {'adapter': 'systemd', 'local_origin': 'http://fixture.invalid',
                    'config_dir': '/fixture/config', 'target': 'shell:writer', 'local_id': 'writer-id',
                    'recovery_ready_timeout': 2, 'drain_timeout': 2,
                    'binary_sha256': hashlib.sha256(self.good.read_bytes()).hexdigest()}
        self.saved = {'service': self.original, 'environment': {'CTL_TOKEN': 'synthetic-only'},
                      'environment_file_sha256': 'original-env', 'auxiliaries': {'updater': 'active'}}
        runner.snapshot = lambda: self.saved
        runner.runtime_guards = lambda: None
        runner.launcher_digest = lambda: 'original-env'
        runner.helper = self.helper
        test = self

        class Lifecycle:
            def info(self): return dict(test.info)
            def has_node(self, value): return bool(value['pid'])
            def stopped(self, value): return not value['pid']
            def binary(self, value): return str(test.binary)
            def start(self):
                test.events.append('start')
                test.info.update(pid=123, generation='new-invocation', unit_metadata_sha256='new-unit')
                test.binary = test.bad
                test.drain['draining'] = False
            def stop(self):
                test.events.append('stop')
                test.info['pid'] = 0
                return dict(test.info)
            def resume(self, states): test.events.append('resume')
        runner.lifecycle = Lifecycle()
        self.runner = runner
        C.private_json(runner.fence_path(), {'manifest_sha256': runner.manifest_hash, 'plan_digest': None,
            'service': runner.service_identity(self.original), 'phase': 'fenced', 'reason': 'owned',
            'drain': json.loads(json.dumps(self.drain))}, True)

    def helper(self, args, env):
        self.events.append('helper-read')
        self.assertEqual(args[0], 'preflight')
        return dict(self.view)

    def http(self, origin, token, action=None, path='/admin/api/node/drain'):
        self.assertIsNone(action, 'these success paths must not replay a mutation')
        self.events.append('http-read')
        return (200, {'ok': True}) if path == '/healthz' else (200, json.loads(json.dumps(self.drain)))

    def sleep(self, _): self.clock += .5

    def stopped_original(self):
        self.info['pid'] = 0
        row = self.runner.read_fence()
        row['phase'] = 'stopped'
        C.private_json(self.runner.fence_path(), row)

    def patches(self, sleep=None, http=None):
        from contextlib import ExitStack
        stack = ExitStack()
        stack.enter_context(patch.object(C.time, 'monotonic', side_effect=lambda: self.clock))
        stack.enter_context(patch.object(C.time, 'sleep', side_effect=sleep or self.sleep))
        stack.enter_context(patch.object(C, 'http_json', side_effect=http or self.http))
        return stack

    def test_owned_start_waits_for_same_process_exec_then_records_verified_generation(self):
        self.stopped_original()
        def finish_exec(delay):
            self.sleep(delay)
            self.binary = self.good
        with self.patches(sleep=finish_exec):
            result = self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertEqual(self.events.count('start'), 1)
        self.assertEqual(result['result']['service']['pid'], 123)
        self.assertEqual(self.runner.read_fence()['recovery_service']['generation'], 'new-invocation')
        self.assertEqual(self.runner.read_fence()['phase'], 'recovered')

    def test_wrong_binary_deadline_is_durable_and_never_restarts(self):
        self.stopped_original()
        with self.patches(), self.assertRaises(C.RuntimeReadinessFailure):
            self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertEqual(self.events, ['start'])
        failure = C.read_json(self.work / 'step-recovery-fence.json')
        self.assertEqual(failure['status'], 'started')
        self.assertEqual(failure['failure']['readiness'], {'stage': 'original_runtime_readiness',
                         'reason': 'reviewed_binary', 'pid': 123, 'generation': 'new-invocation'})
        with self.assertRaises(C.Refused): self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertEqual(self.events.count('start'), 1)

    def test_existing_runtime_wrong_binary_is_not_a_startup_retry(self):
        self.binary = self.bad
        with self.patches(), self.assertRaises(C.BinaryDigestMismatch):
            self.runner.wait_original_ready()
        self.assertEqual(self.clock, 0)

    def test_startup_process_or_generation_change_is_refused(self):
        for change in ({'pid': 124}, {'generation': 'foreign-invocation'}):
            with self.subTest(change=change):
                self.info = dict(self.original)
                self.binary = self.bad
                self.clock = 0
                def replace(delay):
                    self.sleep(delay)
                    self.info.update(change)
                    self.binary = self.good
                with self.patches(sleep=replace), self.assertRaisesRegex(C.Refused, 'runtime changed'):
                    self.runner.wait_original_ready(after_start=True)

    def test_launcher_drift_and_unsafe_executable_are_not_retried(self):
        self.runner.launcher_digest = lambda: 'changed'
        with self.patches(), self.assertRaisesRegex(C.Refused, 'launcher changed'):
            self.runner.wait_original_ready(after_start=True)
        self.runner.launcher_digest = lambda: 'original-env'
        self.bad.chmod(0o777)
        self.binary = self.bad
        with self.patches(), self.assertRaisesRegex(C.Refused, 'unsafe file mode'):
            self.runner.wait_original_ready(after_start=True)
        self.assertEqual(self.clock, 0)

    def test_http_success_from_replaced_generation_is_not_accepted(self):
        self.drain['draining'] = False
        def replace_on_http(*args, **kwargs):
            result = self.http(*args, **kwargs)
            self.info['generation'] = 'foreign'
            return result
        with self.patches(http=replace_on_http), self.assertRaisesRegex(C.Refused, 'across recovery HTTP'):
            self.runner.wait_original_ready(after_start=True)

    def test_already_verified_binary_cannot_regress_during_http_wait(self):
        self.drain['draining'] = False
        def unavailable(*args, **kwargs):
            self.binary = self.bad
            raise C.urllib.error.URLError(ConnectionRefusedError())
        with self.patches(http=unavailable), self.assertRaises(C.BinaryDigestMismatch):
            self.runner.wait_original_ready(after_start=True)

    def test_live_callback_finishes_before_early_ledger_is_checked(self):
        self.drain['ready_to_stop'] = False
        self.drain['counters']['active_callbacks'] = 1
        self.view['pending_task_records'] = 1
        def finish_callback(delay):
            self.assertNotIn('helper-read', self.events)
            self.sleep(delay)
            self.drain['ready_to_stop'] = True
            self.drain['counters']['active_callbacks'] = 0
            self.view['pending_task_records'] = 0
        with self.patches(sleep=finish_callback):
            result = self.runner.verify_fenced_ledger()
        self.assertTrue(result['authority_ledger_empty'])
        self.assertEqual(self.events.count('helper-read'), 1)
        self.assertNotIn('stop', self.events)

    def test_durable_pending_and_control_records_refuse_despite_generic_ready(self):
        for key, value in [('pending_task_records', 10), ('pending_task_controls', 1),
                           ('pending_task_controls', None), ('pending_task_records', False)]:
            with self.subTest(key=key, value=value):
                self.view.update(pending_task_records=0, pending_task_controls=0)
                self.view[key] = value
                with self.patches(), self.assertRaises(C.RuntimeReadinessFailure):
                    self.runner.verify_fenced_ledger()
                self.assertEqual(self.runner.read_fence()['phase'], 'fenced')
        self.assertNotIn('stop', self.events)

    def test_final_writer_stop_rechecks_ledger_and_leaves_owned_fence(self):
        with self.patches():
            self.runner.verify_fenced_ledger()
            self.view['pending_task_records'] = 1
            with self.assertRaises(C.RuntimeReadinessFailure):
                self.runner.stop_fenced(authority_writer=True)
        self.assertEqual(self.events.count('helper-read'), 2)
        self.assertNotIn('stop', self.events)
        self.assertEqual(self.runner.read_fence()['phase'], 'fenced')

    def test_ordinary_stop_keeps_generic_durable_queue_contract(self):
        self.view['pending_task_records'] = 10
        with self.patches(): self.runner.stop_fenced()
        self.assertEqual(self.events.count('stop'), 1)
        self.assertNotIn('helper-read', self.events)

    def test_foreign_fence_and_changed_generation_cannot_supply_ledger_proof(self):
        for kind in ('reason', 'generation'):
            with self.subTest(kind=kind):
                self.drain['reason'] = 'foreign' if kind == 'reason' else 'owned'
                self.info['generation'] = 'foreign' if kind == 'generation' else 'original'
                with self.patches(), self.assertRaises(C.Refused): self.runner.verify_fenced_ledger()
        self.assertNotIn('helper-read', self.events)

    @unittest.skipUnless(Path('/proc/self/exe').exists() and Path('/bin/sh').exists(), 'Linux exec observation')
    def test_real_delayed_exec_keeps_pid_and_reaches_reviewed_binary(self):
        # A private process demonstrates the Type=simple observation class.
        # It is not a production service restart or a claim about the old failure.
        child = subprocess.Popen(['/bin/sh', '-c', 'sleep 0.15; exec /bin/cat'],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        def cleanup():
            if child.poll() is None:
                child.terminate()
                child.wait(timeout=3)
            for stream in (child.stdin, child.stdout, child.stderr):
                if not stream.closed: stream.close()
        self.addCleanup(cleanup)
        self.info.update(pid=child.pid, generation='synthetic-systemd-invocation')
        self.runner.lifecycle.binary = lambda value: str(Path('/proc/%d/exe' % value['pid']).resolve())
        self.runner.m['binary_sha256'] = hashlib.sha256(Path('/bin/cat').read_bytes()).hexdigest()
        self.drain['draining'] = False
        with patch.object(C, 'http_json', side_effect=self.http):
            result = self.runner.wait_original_ready(after_start=True)
        self.assertEqual(result['service']['pid'], child.pid)
        self.assertEqual(Path('/proc/%d/exe' % child.pid).resolve(), Path('/bin/cat').resolve())
        child.terminate()
        child.wait(timeout=3)
        child.stdin.close(); child.stdout.close(); child.stderr.close()


class FleetLedgerTests(unittest.TestCase):
    def test_pending_early_ledger_recovers_without_stopping_a_reader(self):
        runner = object.__new__(F.Fleet)
        runner.nodes = dict.fromkeys(['100', '44', '88', 'mac_mini', 'mac_intel', 'haos'], {})
        runner.state = {'forward': {}, 'reverse': {}, 'frozen': [], 'quiescence': {}}
        events = []
        runner.save = lambda phase=None: None
        runner.parallel = lambda names, fn: {n: fn(n) for n in names}
        def node(name, step, action, *args):
            events.append((name, action))
            if action == 'verify-fenced-ledger': raise F.Refused('durable queued work remains')
            return {'listening': True}
        runner.node = node
        with self.assertRaises(F.Refused): runner.quiesce('forward', '100')
        quiet = runner.state['quiescence']['forward']
        self.assertEqual(quiet['fence_attempted'], ['100'])
        self.assertEqual(quiet['stop_attempted'], [])
        runner.restore_fences_before_activation(quiet)
        self.assertEqual(quiet['recovery']['attempted'], ['100'])
        self.assertFalse(any('stop' in action for _, action in events))
        self.assertEqual({n for n, a in events if a == 'resume-auxiliaries'}, set(runner.nodes))

    def test_both_directions_check_early_ledger_and_use_strict_writer_stop(self):
        for direction, writer in [('forward', '100'), ('reverse', '44')]:
            with self.subTest(direction=direction):
                runner = object.__new__(F.Fleet)
                runner.nodes = {n: {'operation_dir': '/fixture/' + n} for n in ['100', '44', '88', 'mac_mini', 'mac_intel', 'haos']}
                runner.state = {'forward': {'prepared': {n: {'plan_digest': 'b' * 64} for n in runner.nodes}},
                                'reverse': {}, 'frozen': [], 'quiescence': {}}
                events = []
                runner.save = lambda phase=None: None
                runner.node = lambda name, step, action, *args: events.append((name, action, args)) or {'listening': True}
                runner.quiesce(direction, writer)
                suffix = '' if direction == 'forward' else '-applied'
                self.assertEqual(events[1][:2], (writer, 'verify-fenced-ledger' + suffix))
                self.assertEqual(events[-1][:2], (writer, 'stop-fenced-writer' + suffix))
                self.assertEqual(sum('stop-fenced' in a for _, a, _ in events), 6)
                self.assertEqual(len(runner.state['quiescence'][direction]['stopped']), 6)


if __name__ == '__main__': unittest.main()
