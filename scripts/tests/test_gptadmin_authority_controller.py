import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SOURCE = HERE / 'gptadmin_authority_controller.py'
if not SOURCE.exists():
    SOURCE = HERE.parent / 'gptadmin_authority_controller.py'
spec = importlib.util.spec_from_file_location('authority_controller', SOURCE)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class DurableControllerTests(unittest.TestCase):
    def runner(self):
        root = Path(self.temp.name)
        controller = object.__new__(module.Controller)
        controller.manifest_hash = 'reviewed-manifest'
        controller.work = root / 'operation'
        controller.m = {}
        return controller

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)

    def test_completed_step_does_not_repeat_mutation(self):
        runner = self.runner()
        calls = []
        runner.capture = lambda: calls.append('capture') or {'safe': True}
        first = runner.execute('capture', 'capture', [])
        second = runner.execute('capture', 'capture', [])
        self.assertEqual(calls, ['capture'])
        self.assertEqual(first, second)
        self.assertEqual((runner.work / 'step-capture.json').stat().st_mode & 0o777, 0o600)
        with self.assertRaises(module.Refused):
            runner.execute('capture', 'helper', ['apply'])

    def test_uncertain_action_is_not_repeated(self):
        runner = self.runner()
        calls = []
        def uncertain():
            calls.append('mutation-may-have-run')
            raise module.Refused('lost response')
        runner.capture = uncertain
        for _ in range(2):
            with self.assertRaises(module.Refused):
                runner.execute('capture', 'capture', [])
        self.assertEqual(calls, ['mutation-may-have-run'])
        receipt = module.read_json(runner.work / 'step-capture.json')
        self.assertEqual(receipt['status'], 'started')

    def test_manifest_rebinding_rejected_on_cached_receipt(self):
        runner = self.runner()
        runner.capture = lambda: {'safe': True}
        runner.execute('capture', 'capture', [])
        runner.manifest_hash = 'different-reviewed-manifest'
        with self.assertRaises(module.Refused):
            runner.execute('capture', 'capture', [])

    def test_private_exclusive_receipt_is_not_overwritten(self):
        path = Path(self.temp.name) / 'receipt.json'
        module.private_json(path, {'accepted': 'original'}, True)
        with self.assertRaises(FileExistsError):
            module.private_json(path, {'accepted': 'different'}, True)
        self.assertEqual(module.read_json(path), {'accepted': 'original'})

    def test_unreviewed_helper_mutation_is_refused(self):
        runner = self.runner()
        with self.assertRaises(module.Refused):
            runner.run_offline(['clear-queue'])

    def original_recovery(self, events):
        runner = self.runner()
        runner.work.mkdir(mode=0o700)
        runner.m = {'local_origin': 'http://127.0.0.1:19001', 'recovery_ready_timeout': 2, 'adapter': 'systemd'}
        runner.snapshot = lambda: {'service': {'pid': 77, 'generation': 'original'}, 'environment_file_sha256': 'original',
                                   'environment': {'CTL_TOKEN': 'fixture-only'},
                                   'auxiliaries': {'updater': True}}
        runner.launcher_digest = lambda: 'original'
        runner.runtime_guards = lambda: events.append('guards')
        runner.check_binary = lambda info: events.append(('binary', info['pid']))
        class Lifecycle:
            pid = 0
            def info(self): return {'pid': self.pid, 'generation': 'original' if self.pid == 77 else 'recovered'}
            def stopped(self, value): return value['pid'] == 0
            def has_node(self, value): return value['pid'] != 0
            def start(self):
                events.append('start')
                self.pid = 123
            def resume(self, states): events.append(('resume', states))
        runner.lifecycle = Lifecycle()
        return runner

    def test_recovery_waits_for_listener_before_single_drain_cancel(self):
        events = []
        runner = self.original_recovery(events)
        calls = []
        def http(origin, token, action=None, path='/admin/api/node/drain'):
            calls.append((action, path))
            if len(calls) == 1:
                raise module.urllib.error.URLError(ConnectionRefusedError())
            if path == '/healthz': return 200, {'ok': True}
            if action == 'cancel':
                events.append('cancel')
                return 200, {'draining': False}
            return 200, {'draining': True}
        with patch.object(module, 'http_json', side_effect=http), patch.object(module.time, 'sleep'):
            result = runner.execute('recover', 'restore-unapplied', [])
        self.assertEqual(result['status'], 'completed')
        self.assertEqual(events.count('start'), 1)
        self.assertEqual([call for call in calls if call[0]], [('cancel', '/admin/api/node/drain')])
        self.assertLess(events.index('cancel'), events.index(('resume', {'updater': True})))

    def test_lost_recovery_cancel_response_remains_uncertain_without_retry(self):
        events = []
        runner = self.original_recovery(events)
        mutations = []
        def http(origin, token, action=None, path='/admin/api/node/drain'):
            if path == '/healthz': return 200, {'ok': True}
            if action:
                mutations.append(action)
                raise module.urllib.error.URLError(TimeoutError())
            return 200, {'draining': True}
        with patch.object(module, 'http_json', side_effect=http):
            with self.assertRaises(module.urllib.error.URLError):
                runner.execute('recover', 'restore-unapplied', [])
            with self.assertRaises(module.Refused):
                runner.execute('recover', 'restore-unapplied', [])
        self.assertEqual(events.count('start'), 1)
        self.assertEqual(mutations, ['cancel'])
        self.assertNotIn(('resume', {'updater': True}), events)
        self.assertEqual(module.read_json(runner.work / 'step-recover.json')['status'], 'started')

    def test_recovery_readiness_deadline_does_not_restart_or_resume_updaters(self):
        events = []
        runner = self.original_recovery(events)
        with patch.object(module.time, 'monotonic', side_effect=[0, 0, 3]), \
                patch.object(module.time, 'sleep'), \
                patch.object(module, 'http_json', side_effect=module.urllib.error.URLError(ConnectionRefusedError())):
            with self.assertRaisesRegex(module.Refused, 'readiness deadline'):
                runner.execute('recover', 'restore-unapplied', [])
        self.assertEqual(events.count('start'), 1)
        self.assertNotIn(('resume', {'updater': True}), events)

    def test_recovery_of_ready_original_skips_unneeded_cancel(self):
        events = []
        runner = self.original_recovery(events)
        runner.lifecycle.pid = 77
        calls = []
        def http(origin, token, action=None, path='/admin/api/node/drain'):
            calls.append(action)
            return 200, {'draining': False}
        with patch.object(module, 'http_json', side_effect=http):
            result = runner.execute('recover', 'restore-unapplied', [])
        self.assertFalse(result['result']['drain_cancel_issued'])
        self.assertNotIn('start', events)
        self.assertEqual(calls, [None, None])

    def test_reverse_repauses_restored_auxiliaries_even_if_node_stopped(self):
        runner = self.runner()
        events = []
        class Lifecycle:
            def pause(self, saved):
                events.append(('pause', saved))
            def info(self):
                events.append(('inspect', None))
                return {'pid': 0}
            def stopped(self, value):
                return value['pid'] == 0
            def has_node(self, value):
                return value['pid'] != 0
        runner.lifecycle = Lifecycle()
        runner.runtime_guards = lambda: events.append(('guards', None))
        runner.snapshot = lambda: {'auxiliaries': {'updater': True}}
        result = runner.freeze_applied('unused', 'unused')
        self.assertTrue(result['already_stopped'])
        self.assertEqual(events, [('guards', None), ('pause', {'updater': True}), ('inspect', None)])

    def test_zero_pid_activating_job_is_not_stopped_and_uses_normal_stop(self):
        service = module.Systemd({})
        activating = {'pid': 0, 'control_pid': 77, 'job': '123', 'active': 'activating',
                      'sub_state': 'start-pre', 'cgroup_populated': True}
        stopped = {'pid': 0, 'control_pid': 0, 'job': '', 'active': 'inactive',
                   'sub_state': 'dead', 'cgroup_populated': False}
        self.assertFalse(service.stopped(activating))
        self.assertTrue(service.stopped(stopped))
        events = []
        class Lifecycle:
            def pause(self, states): events.append('pause')
            def info(self): return activating
            def stopped(self, value): return service.stopped(value)
            def has_node(self, value): return service.has_node(value)
            def stop(self): events.append('normal-stop'); return stopped
            def start(self): self.fail('unknown start must never be repeated')
        runner = self.runner()
        runner.lifecycle = Lifecycle()
        runner.runtime_guards = lambda: None
        runner.snapshot = lambda: {'auxiliaries': {}}
        with patch.object(module.time, 'monotonic', side_effect=[0, 16]):
            result = runner.freeze_applied('unused', 'unused')
        self.assertTrue(result['normal_stop_settled_unknown_start'])
        self.assertEqual(events, ['pause', 'normal-stop'])

    def test_launchd_loaded_without_pid_is_not_stopped(self):
        runner = object.__new__(module.Launchd)
        self.assertFalse(runner.stopped({'loaded': True, 'pid': 0}))
        self.assertTrue(runner.stopped({'loaded': False, 'pid': 0}))

    def test_haos_publication_pause_restores_original_update_flag(self):
        runner = object.__new__(module.HAOS)
        state = {'watchdog': False, 'boot': 'auto', 'auto_update': True}
        original = dict(state)
        events = []
        runner.info = lambda: {'pid': 77, 'container_running': True, 'inner_pid': 139}
        runner.auxiliaries = lambda: dict(state)
        def set_flag(value):
            events.append(('auto_update', value))
            state['auto_update'] = value
        runner.set_automatic_updates = set_flag
        runner.wait_updates_idle = lambda: events.append(('wait', None))
        runner.pause(original)
        runner.pause(original)
        runner.resume(original)
        self.assertEqual(events, [('auto_update', False), ('wait', None), ('wait', None), ('auto_update', True)])
        self.assertEqual(state, original)

    def test_prelaunch_failure_restores_paused_updaters_without_freeze(self):
        import sys
        sys.path.insert(0, str(SOURCE.parent))
        import gptadmin_authority_fleet as fleet
        runner = object.__new__(fleet.Fleet)
        runner.m = {'affected_controller_units': []}
        runner.nodes = {'100': {}, '44': {}, '88': {}, 'mac_mini': {}, 'mac_intel': {}, 'haos': {}}
        runner.state = {}
        events = []
        def reject_gate(): raise fleet.Refused('launch task was not terminal')
        runner.await_launch_gate = reject_gate
        runner.node = lambda n, step, action: events.append((n, step, action))
        runner.parallel = lambda names, function: [function(n) for n in names]
        runner.save = lambda phase: events.append(phase)
        with self.assertRaises(fleet.Refused):
            runner.run_locked()
        self.assertEqual(events[:-1], [(n, 'prelaunch-auxiliary-restore', 'resume-auxiliaries') for n in runner.nodes])
        self.assertEqual(events[-1], 'prelaunch_aborted_auxiliaries_restored')

    def test_capture_receipt_failure_restores_all_prepaused_nodes(self):
        import sys
        sys.path.insert(0, str(SOURCE.parent))
        import gptadmin_authority_fleet as fleet
        runner = object.__new__(fleet.Fleet)
        runner.m = {'affected_controller_units': []}
        runner.nodes = {'100': {}, '44': {}, '88': {}, 'mac_mini': {}, 'mac_intel': {}, 'haos': {}}
        runner.state = {}
        events = []
        runner.await_launch_gate = lambda: None
        def node(name, step, action):
            if action == 'capture': raise fleet.Refused('capture receipt transport unavailable')
            events.append((name, action))
        runner.node = node
        runner.parallel = lambda names, function: [function(n) for n in names]
        runner.save = lambda phase: None
        with self.assertRaises(fleet.Refused): runner.run_locked()
        self.assertEqual(events, [(n, 'resume-auxiliaries') for n in runner.nodes])

    def test_partial_freeze_recovery_restores_unfrozen_updaters_too(self):
        import sys
        sys.path.insert(0, str(SOURCE.parent))
        import gptadmin_authority_fleet as fleet
        runner = object.__new__(fleet.Fleet)
        runner.nodes = {'100': {}, '44': {}, '88': {}, 'mac_mini': {}, 'mac_intel': {}, 'haos': {}}
        runner.state = {'activation_authorized': False, 'forward': {}, 'frozen': ['100']}
        events = []
        runner.node = lambda n, step, action: events.append((n, action))
        runner.parallel = lambda names, function: [function(n) for n in names]
        runner.save = lambda phase: None
        runner.rollback_before_activation()
        self.assertEqual(events, [('100', 'restore-unapplied')] + [(n, 'resume-auxiliaries') for n in runner.nodes])

    def test_launchd_pause_waits_for_running_updater(self):
        runner = object.__new__(module.Launchd)
        runner.domain = 'gui/501'
        events = []
        states = iter([{'loaded': True, 'pid': 77}, {'loaded': True, 'pid': 0}, {'loaded': False, 'pid': 0}])
        runner.job = lambda label: next(states)
        with patch.object(module.time, 'sleep', side_effect=lambda seconds: events.append('wait')):
            with patch.object(module, 'command', side_effect=lambda argv: events.append(argv)):
                runner.pause({'com.gptadmin.node.autoupdate': True})
        self.assertEqual(events, ['wait', ['/bin/launchctl', 'bootout', 'gui/501/com.gptadmin.node.autoupdate']])

    def test_installed_edge_file_with_failed_reload_is_not_success(self):
        import sys
        sys.path.insert(0, str(SOURCE.parent))
        import gptadmin_authority_fleet as fleet
        runner = object.__new__(fleet.Fleet)
        runner.m = {'edge': {'forward': {'config_sha256': 'reviewed-forward'}}}
        runner.state = {'forward': {}}
        runner.save = lambda phase: None
        runner.edge_status = lambda: {'forwardResolution': {'outcome': 'installed'}, 'remote': {
            'lockPresent': False, 'liveSha256': 'reviewed-forward',
            'transaction.json': {'phase': 'failed_after_swap'}}}
        with self.assertRaises(fleet.Refused):
            runner.resolve_forward_edge()

    def test_launch_gate_requires_exact_successfully_terminal_native_receipt(self):
        import sys
        sys.path.insert(0, str(SOURCE.parent))
        import gptadmin_authority_fleet as fleet
        gate = Path(self.temp.name) / 'approval.json'
        module.private_json(gate, {'schema': 'gptadmin.authority-launch-gate/v1', 'operation_id': 'reviewed',
                                  'manifest_sha256': 'manifest', 'launcher_owner_target': 'shell:admin-node-100',
                                  'launcher_task_id': 'existing-task'}, True)
        runner = object.__new__(fleet.Fleet)
        runner.m = {'launch_gate_path': str(gate), 'operation_id': 'reviewed', 'external_unit': 'reviewed.service'}
        runner.manifest_hash = 'manifest'
        runner.state = {}
        runner.save = lambda phase: None
        runner.remote = lambda *args, **kwargs: b'{"found":true,"terminal_verified":false,"status":"working"}'
        with self.assertRaises(fleet.Refused):
            runner.await_launch_gate()
        self.assertNotIn('launch_acceptance', runner.state)
        runner.remote = lambda *args, **kwargs: b'{"found":true,"terminal_verified":true,"status":"completed","task_id":"existing-task","returncode":0}'
        runner.await_launch_gate()
        self.assertEqual(runner.state['launch_acceptance']['launcher_task']['task_id'], 'existing-task')


if __name__ == '__main__':
    unittest.main()
