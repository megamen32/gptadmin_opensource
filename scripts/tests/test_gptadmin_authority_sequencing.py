"""Narrow lifecycle logic tests. These do not claim a real fleet migration."""
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import gptadmin_authority_controller as controller
import gptadmin_authority_fleet as fleet


class SimulatedFleet:
    def __init__(self, writer='100', persistent_404=False, lost_reader_stop=False):
        self.writer = writer
        self.persistent_404 = persistent_404
        self.lost_reader_stop = lost_reader_stop
        self.events = []
        self.running = {n: True for n in ('100', '44', '88', 'mac_mini', 'mac_intel', 'haos')}
        self.draining = dict.fromkeys(self.running, False)
        self.outbox = {'88': 1}
        self.runner = object.__new__(fleet.Fleet)
        self.runner.m = {'affected_controller_units': []}
        self.runner.nodes = {n: {'operation_dir': '/fixture/' + n} for n in self.running}
        self.runner.state = {'activation_authorized': False, 'forward': {}, 'reverse': {}, 'frozen': [], 'quiescence': {}}
        self.runner.save = lambda phase=None: self.events.append(('phase', phase))
        self.runner.node = self.node
        self.runner.await_launch_gate = lambda: None
        self.runner.parallel = lambda names, fn: {n: fn(n) for n in names}

    def node(self, name, step, action, *args):
        self.events.append((name, action, args))
        if action == 'capture':
            return {'service': {'pid': 1}, 'preflight': {}}
        if action in ('fence', 'fence-applied'):
            if self.draining[name]:
                raise fleet.Refused('another fence owns this node')
            self.draining[name] = True
            return {'listening': True, 'drain': {'draining': True}}
        if action in ('verify-fenced-ledger', 'verify-fenced-ledger-applied'):
            return {'authority_ledger_empty': True, 'read_only': True}
        if action in ('stop-fenced', 'stop-fenced-applied', 'stop-fenced-writer', 'stop-fenced-writer-applied'):
            if name == '88' and self.outbox['88']:
                if self.persistent_404 or not self.running[self.writer]:
                    raise fleet.Refused('outbox remains pending without an acknowledgement')
                self.events.append(('88', 'result_acknowledged_by_live_writer', (self.writer,)))
                self.outbox['88'] = 0
            self.running[name] = False
            if name == '44' and self.lost_reader_stop:
                raise OSError('stop applied but response was lost')
            return {'stopped': {'pid': 0}, 'drain': {'ready_to_stop': True}}
        if action == 'restore-fenced':
            self.running[name] = True
            self.draining[name] = False
            return {'original_runtime_restored': True}
        if action == 'resume-auxiliaries':
            return {'auxiliaries_restored': True}
        raise AssertionError('unexpected downstream phase: ' + action)


class FleetSequencingTests(unittest.TestCase):
    def test_both_directions_keep_writer_for_pending_reader_result(self):
        for direction, writer in [('forward', '100'), ('reverse', '44')]:
            with self.subTest(direction=direction):
                sim = SimulatedFleet(writer)
                if direction == 'reverse':
                    sim.runner.state['activation_authorized'] = True
                    sim.runner.state['forward']['prepared'] = {n: {'plan_digest': 'a' * 64} for n in sim.running}
                sim.runner.quiesce(direction, writer)
                actions = [x for x in sim.events if x[0] != 'phase']
                fence_action = 'fence' if direction == 'forward' else 'fence-applied'
                stop_action = 'stop-fenced' if direction == 'forward' else 'stop-fenced-applied'
                self.assertEqual(actions[0][:2], (writer, fence_action))
                writer_stop = 'stop-fenced-writer' if direction == 'forward' else 'stop-fenced-writer-applied'
                stops = [x for x in actions if x[1] in (stop_action, writer_stop)]
                self.assertEqual(actions[1][1], 'verify-fenced-ledger' if direction == 'forward' else 'verify-fenced-ledger-applied')
                self.assertEqual(stops[-1][1], writer_stop)
                self.assertEqual(stops[-1][0], writer)
                self.assertEqual({x[0] for x in stops[:-1]}, set(sim.running) - {writer})
                self.assertIn(('88', 'result_acknowledged_by_live_writer', (writer,)), actions)
                self.assertFalse(any(sim.running.values()))
                quiet = sim.runner.state['quiescence'][direction]
                self.assertEqual(set(quiet['fenced']), set(sim.running))
                self.assertEqual(set(quiet['stopped']), set(sim.running))
                if direction == 'reverse':
                    self.assertTrue(sim.runner.state['activation_authorized'])
                    self.assertEqual(stops[-1][2], ('/fixture/44/forward-backup', 'a' * 64))

    def test_persistent_404_never_stops_writer_or_exports_and_restores_readers(self):
        sim = SimulatedFleet(persistent_404=True)
        with self.assertRaises(fleet.Refused):
            sim.runner.run_locked()
        self.assertNotIn(('100', 'stop-fenced-writer', ()), sim.events)
        self.assertFalse(any(len(x) == 3 and x[1].startswith('helper') for x in sim.events))
        quiet = sim.runner.state['quiescence']['forward']
        self.assertEqual(quiet['stop_attempted'], ['44', '88'])
        self.assertEqual(list(quiet['stopped']), ['44'])
        self.assertEqual(sim.runner.state['frozen'], ['44'])
        self.assertEqual(quiet['recovery']['attempted'], ['44', '88', '100'])
        self.assertTrue(all(sim.running.values()))
        self.assertFalse(any(sim.draining.values()))
        self.assertEqual(sim.outbox['88'], 1)
        self.assertEqual({x[0] for x in sim.events if len(x) == 3 and x[1] == 'resume-auxiliaries'}, set(sim.running))

    def test_lost_reader_stop_is_attempted_not_completed_and_is_not_replayed(self):
        sim = SimulatedFleet(lost_reader_stop=True)
        with self.assertRaises(fleet.Refused):
            sim.runner.run_locked()
        quiet = sim.runner.state['quiescence']['forward']
        self.assertEqual(quiet['stop_attempted'], ['44'])
        self.assertEqual(quiet['stopped'], {})
        self.assertEqual(sim.runner.state['frozen'], [])
        self.assertEqual(sim.events.count(('44', 'stop-fenced', ())), 1)
        self.assertEqual(quiet['recovery']['attempted'], ['44', '100'])
        self.assertTrue(all(sim.running.values()))
        self.assertNotIn(('100', 'stop-fenced-writer', ()), sim.events)

    def test_one_failed_reader_recovery_does_not_strand_other_nodes(self):
        sim = SimulatedFleet(persistent_404=True)
        original = sim.runner.node
        def node(name, step, action, *args):
            if name == '44' and action == 'restore-fenced':
                raise OSError('reader restoration response is unknown')
            return original(name, step, action, *args)
        sim.runner.node = node
        with self.assertRaises(fleet.Refused):
            sim.runner.run_locked()
        self.assertFalse(sim.draining['100'])
        self.assertFalse(sim.draining['88'])
        self.assertEqual(sim.runner.state['quiescence']['forward']['recovery']['errors'], {'44': 'OSError'})
        self.assertIn(('phase', 'preactivation_recovery_incomplete'), sim.events)
        self.assertEqual({x[0] for x in sim.events if len(x) == 3 and x[1] == 'resume-auxiliaries'}, set(sim.running))


class FenceControllerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.events = []
        self.runtime = {'pid': 77, 'generation': 'original-invocation', 'unit_metadata_sha256': 'original-unit'}
        self.drain = {'draining': False, 'ready_to_stop': False, 'reason': '', 'since': '',
                      'counters': {'active_submissions': 0, 'active_callbacks': 0,
                                   'unfinished_executions': 0, 'outbox_pending': 0}}
        self.lose_begin = False
        self.lose_cancel = False
        self.calls = []
        runner = object.__new__(controller.Controller)
        runner.work = Path(self.tmp.name)
        runner.manifest_hash = '1' * 64
        runner.m = {'adapter': 'systemd', 'local_origin': 'http://fixture.invalid',
                    'drain_timeout': 1, 'recovery_ready_timeout': 1}
        saved = {'service': dict(self.runtime), 'environment_file_sha256': 'original-env',
                 'environment': {'CTL_TOKEN': 'synthetic-only'}, 'auxiliaries': {}}
        runner.snapshot = lambda: saved
        runner.runtime_guards = lambda: None
        runner.launcher_digest = lambda: 'original-env'
        runner.check_binary = lambda info: None
        test = self
        class Lifecycle:
            def info(self): return dict(test.runtime)
            def has_node(self, value): return bool(value['pid'])
            def stopped(self, value): return value['pid'] == 0
            def pause(self, states): test.events.append('pause')
            def resume(self, states): test.events.append('resume')
            def stop(self):
                test.events.append('stop')
                test.runtime['pid'] = 0
                return dict(test.runtime)
            def start(self):
                test.events.append('start')
                test.runtime.update(pid=123, generation='recovery-invocation', unit_metadata_sha256='recovered-unit')
                test.drain.update(draining=False, reason='', since='')
        runner.lifecycle = Lifecycle()
        self.runner = runner

    def http(self, origin, token, action=None, path='/admin/api/node/drain'):
        self.calls.append(action)
        if path == '/healthz':
            return 200, {'ok': True}
        if isinstance(action, dict) and action['action'] == 'begin':
            self.drain.update(draining=True, ready_to_stop=True, reason=action['reason'], since='fixture-time')
            if self.lose_begin:
                raise controller.urllib.error.URLError(TimeoutError('begin response lost'))
        elif action == 'cancel':
            self.drain.update(draining=False, reason='', since='')
            if self.lose_cancel:
                raise controller.urllib.error.URLError(TimeoutError('cancel response lost'))
        elif action is not None:
            raise AssertionError('status polling must be GET')
        return 200, json.loads(json.dumps(self.drain))

    def fence(self):
        return self.runner.execute('forward-fence', 'fence', [])

    def test_lost_begin_is_not_repeated_and_only_own_fence_is_cancelled(self):
        self.lose_begin = True
        with patch.object(controller, 'http_json', side_effect=self.http):
            with self.assertRaises(controller.urllib.error.URLError): self.fence()
            with self.assertRaises(controller.Refused): self.fence()
            self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertEqual(sum(isinstance(x, dict) for x in self.calls), 1)
        self.assertEqual(self.calls.count('cancel'), 1)
        self.assertNotIn('stop', self.events)
        self.assertNotIn('start', self.events)
        self.assertEqual(controller.read_json(self.runner.work / 'step-forward-fence.json')['status'], 'started')

    def test_lost_cancel_is_not_repeated_even_under_a_different_step(self):
        with patch.object(controller, 'http_json', side_effect=self.http):
            self.fence()
            self.lose_cancel = True
            with self.assertRaises(controller.urllib.error.URLError):
                self.runner.execute('recovery-fence', 'restore-fenced', [])
            with self.assertRaises(controller.Refused):
                self.runner.execute('recovery-fence', 'restore-fenced', [])
            self.runner.execute('reconcile-cancel', 'restore-fenced', [])
        self.assertEqual(self.calls.count('cancel'), 1)
        self.assertEqual(self.runner.read_fence()['phase'], 'recovered')

    def test_same_pid_new_generation_is_not_stopped_or_cancelled(self):
        with patch.object(controller, 'http_json', side_effect=self.http):
            self.fence()
            self.runtime['generation'] = 'unrelated-invocation'
            with self.assertRaises(controller.Refused):
                self.runner.execute('forward-stop', 'stop-fenced', [])
            with self.assertRaises(controller.Refused):
                self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertNotIn('stop', self.events)
        self.assertNotIn('cancel', self.calls)

    def test_foreign_reason_or_since_is_never_cancelled(self):
        with patch.object(controller, 'http_json', side_effect=self.http):
            self.fence()
            self.drain['since'] = 'a-later-foreign-fence'
            with self.assertRaises(controller.Refused):
                self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertNotIn('cancel', self.calls)

    def test_nonzero_outbox_overrides_ready_flag_and_never_stops(self):
        with patch.object(controller, 'http_json', side_effect=self.http):
            self.fence()
            self.drain['counters']['outbox_pending'] = 1
            with patch.object(controller.time, 'monotonic', side_effect=[0, 0, 2]), patch.object(controller.time, 'sleep'):
                with self.assertRaisesRegex(controller.Refused, 'deadline'):
                    self.runner.execute('forward-stop', 'stop-fenced', [])
        self.assertNotIn('stop', self.events)
        self.assertEqual(self.runner.read_fence()['phase'], 'fenced')

    def test_recovery_binds_new_generation_before_waiting_for_http(self):
        ready_calls = []
        original_http = self.http
        def delayed_http(*args, **kwargs):
            if self.runtime['pid'] == 123 and not ready_calls:
                ready_calls.append(True)
                proof = self.runner.read_fence()
                self.assertEqual(proof['phase'], 'recovery_started')
                self.assertEqual(proof['recovery_service']['generation'], 'recovery-invocation')
                raise controller.urllib.error.URLError(ConnectionRefusedError())
            return original_http(*args, **kwargs)
        with patch.object(controller, 'http_json', side_effect=delayed_http), patch.object(controller.time, 'sleep'):
            self.fence()
            self.runner.execute('forward-stop', 'stop-fenced', [])
            result = self.runner.execute('recovery-fence', 'restore-fenced', [])
        self.assertEqual(result['status'], 'completed')
        self.assertEqual(self.events.count('start'), 1)
        self.assertNotIn('cancel', self.calls)


if __name__ == '__main__':
    unittest.main()
