"""Local contract tests only: no SSH, canonical bridge, systemd, or MCP is run."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import queue
import shlex
import subprocess
import tempfile
import threading
import types
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


api = load('guard_contract_under_test', 'gptadmin_authority_release_guard.py')
wrapper = load('guarded_contract_under_test', 'gptadmin_authority_guarded.py')
NODES = {'100', '44', '88', 'mac_mini', 'mac_intel', 'haos'}
OWNER = {'hostId': 'roomhacker-server-100', 'harness': 'chatgpt',
         'nativeSessionId': '/root/close_gptadmin'}


def config():
    return {'project': '/review/project/.git', 'owner': dict(OWNER), 'fence': 82,
            'module': '/review/release_guard.py', 'module_sha256': 'a' * 64,
            'bridge_ssh_argv': ['ssh', '-o', 'StrictHostKeyChecking=yes', 'fleet-100'],
            'renew_seconds': 120, 'forward_window_seconds': 1800,
            'operation_id': 'review-operation', 'fleet_manifest_sha256': 'b' * 64}


def lease(cfg=None, **changes):
    cfg = cfg or config()
    return {'project': cfg['project'], 'owner': dict(cfg['owner']),
            'fence': cfg['fence'], 'expiresAt': 4_000_000_000_000, **changes}


class DormantThread:
    """The scheduler is controlled explicitly by each test."""
    def __init__(self, *args, **kwargs):
        self.started = False

    def start(self):
        self.started = True

    def join(self, timeout=None):
        pass

    def is_alive(self):
        return False


class ScriptedChannel:
    def __init__(self, answers):
        self.answers = list(answers)
        self.requests = []
        self.closed = False

    def call(self, request, timeout=15):
        self.requests.append(dict(request))
        answer = self.answers.pop(0)
        if isinstance(answer, BaseException):
            raise answer
        return answer

    def close(self):
        self.closed = True


class ChannelContractTests(unittest.TestCase):
    def test_installed_settings_default_does_not_enable_an_absent_switch(self):
        self.assertTrue(api.occupancy_enabled({'version': 1, 'releaseOccupancyEnabled': True}))
        for settings in ({'version': 1}, {'releaseOccupancyEnabled': False},
                         {'releaseOccupancyEnabled': 1}, {'releaseOccupancyEnabled': 'true'}, None):
            self.assertFalse(api.occupancy_enabled(settings))

    def channel(self, replies):
        value = object.__new__(api.Channel)
        value.process = types.SimpleNamespace(stdin=io.StringIO(), poll=lambda: None)
        value.lines = queue.Queue()
        for reply in replies:
            value.lines.put(reply if reply is None else json.dumps(reply) + '\n')
        value.lock = threading.Lock()
        value.failed = False
        return value

    def test_semantic_expiry_keeps_original_channel_for_cleanup(self):
        channel = self.channel([
            {'ok': False, 'error': 'lease_expired'},
            {'ok': True, 'result': lease(expiresAt=1)},
            {'ok': True, 'result': {'finished': True, 'guardId': 'guard-one'}},
        ])
        with self.assertRaises(api.BridgeRejected):
            channel.call({'action': 'check'})
        self.assertFalse(channel.failed)
        self.assertEqual(channel.call({'action': 'check', 'cleanup': True})['expiresAt'], 1)
        self.assertTrue(channel.call({'action': 'finished'})['finished'])
        sent = [json.loads(x) for x in channel.process.stdin.getvalue().splitlines()]
        self.assertEqual(sent, [{'action': 'check'}, {'action': 'check', 'cleanup': True},
                                {'action': 'finished'}])

    def test_transport_eof_is_latched_and_cannot_be_replayed_as_cleanup(self):
        channel = self.channel([None])
        with self.assertRaises(api.GuardRefused):
            channel.call({'action': 'check'})
        self.assertTrue(channel.failed)
        written = channel.process.stdin.getvalue()
        with self.assertRaises(api.GuardRefused):
            channel.call({'action': 'check', 'cleanup': True})
        self.assertEqual(channel.process.stdin.getvalue(), written)

    def test_timeout_is_unknown_and_latches_channel(self):
        channel = self.channel([])
        with self.assertRaises(queue.Empty):
            channel.call({'action': 'check'}, timeout=0)
        self.assertTrue(channel.failed)
        with self.assertRaises(api.GuardRefused):
            channel.call({'action': 'finished'})


class GuardWindowTests(unittest.TestCase):
    def opened(self, remaining=(), renewal=None):
        cfg = config()
        main = ScriptedChannel([{'guarded': True, 'guardId': 'guard-one', 'lease': lease(cfg)},
                                lease(cfg), *remaining])
        channels = [main] + ([] if renewal is None else [renewal])
        created = []

        def factory(argv):
            created.append(argv)
            return channels[len(created) - 1]

        guard = api.GuardWindow(cfg, '/review/config.json', 'c' * 64, 'fleet', factory)
        with mock.patch.object(api.threading, 'Thread', DormantThread):
            guard.open()
        self.addCleanup(guard.close)
        return guard, main, created

    def test_separate_renewal_preserves_original_guard_channel(self):
        renewed = ScriptedChannel([lease()])
        guard, original, created = self.opened([lease()], renewed)
        guard.renew()
        self.assertIs(guard.channel, original)
        self.assertTrue(renewed.closed)
        self.assertEqual(renewed.requests, [{'action': 'renew'}])
        self.assertEqual(len(created), 2)
        self.assertIn('--bridge renew', created[1][-1])
        guard.check()
        self.assertEqual(original.requests[-1], {'action': 'check'})

    def test_renew_failure_blocks_forward_but_same_guard_cleanup_can_finish(self):
        renewal = ScriptedChannel([api.BridgeRejected('lease_expired')])
        guard, original, _ = self.opened([
            lease(expiresAt=1), lease(expiresAt=1),
            {'finished': True, 'guardId': 'guard-one'},
        ], renewal)
        guard.stop = mock.Mock(wait=mock.Mock(return_value=False), set=mock.Mock())
        guard._renew_loop()
        self.assertEqual(guard.renewal_error, 'BridgeRejected')
        before = len(original.requests)
        with self.assertRaises(api.GuardRefused):
            guard.check()
        self.assertEqual(len(original.requests), before)
        self.assertTrue(guard.check(cleanup=True)['cleanup'])
        result = guard.finish({'verified_terminal': True, 'transfer_success': False,
                               'recovered': True})
        self.assertTrue(result['finished'])
        self.assertTrue(guard.finished)
        self.assertEqual(original.requests[-3:], [
            {'action': 'check', 'cleanup': True},
            {'action': 'check', 'cleanup': True}, {'action': 'finished'}])

    def test_local_forward_window_expiry_does_not_recreate_guard(self):
        guard, original, created = self.opened([lease(expiresAt=1)])
        with mock.patch.object(api.time, 'monotonic', return_value=guard.started + 1801):
            with self.assertRaises(api.GuardRefused):
                guard.check()
            guard.check(cleanup=True)
        self.assertEqual(len(created), 1)
        self.assertEqual(original.requests[-1], {'action': 'check', 'cleanup': True})

    def test_identity_mismatch_is_not_accepted_as_renewal(self):
        for changed in ({'fence': 83}, {'project': '/other/.git'},
                        {'owner': {**OWNER, 'nativeSessionId': 'another-owner'}}):
            with self.subTest(changed=changed):
                renewal = ScriptedChannel([lease(**changed)])
                guard, _, _ = self.opened(renewal=renewal)
                with self.assertRaises(api.GuardRefused):
                    guard.renew()
                self.assertTrue(renewal.closed)

    def test_unknown_effect_cannot_send_finished(self):
        guard, original, _ = self.opened()
        with self.assertRaises(api.GuardRefused):
            guard.finish({'verified_terminal': False, 'requires_reconciliation': True})
        self.assertNotIn({'action': 'finished'}, original.requests)

    def test_finished_receipt_for_another_guard_is_rejected(self):
        guard, _, _ = self.opened([lease(), {'finished': True, 'guardId': 'different-guard'}])
        with self.assertRaises(api.GuardRefused):
            guard.finish({'verified_terminal': True})
        self.assertFalse(guard.finished)


class RecordingFleet:
    def __init__(self, *args):
        self.state = {}
        self.events = []

    def save(self):
        pass

    def quiesce(self, *args): self.events.append(('quiesce', args))
    def apply(self, *args): self.events.append(('apply', args))
    def authorize_and_start(self, *args): self.events.append(('authorize', args))
    def edge(self, *args): self.events.append(('edge', args))
    def finish_readers(self, *args): self.events.append(('readers', args))
    def rollback_before_activation(self): self.events.append(('rollback', ()))
    def await_launch_gate(self): self.events.append(('native-task-gate', ()))
    def node(self, *args): self.events.append(('node', args))


class FleetBoundaryTests(unittest.TestCase):
    def test_each_forward_boundary_checks_before_base_mutation(self):
        for name, args in [('quiesce', ('forward', '100')), ('apply', ('forward',)),
                           ('authorize_and_start', ('forward', '44')),
                           ('edge', ('forward', {})), ('finish_readers', ('forward', '44'))]:
            with self.subTest(name=name):
                guard = mock.Mock(check=mock.Mock(side_effect=api.GuardRefused('lost')))
                runner = wrapper.guarded_class(types.SimpleNamespace(Fleet=RecordingFleet), guard)()
                with self.assertRaises(api.GuardRefused):
                    getattr(runner, name)(*args)
                self.assertEqual(runner.events, [])
                guard.check.assert_called_once_with(cleanup=False)

    def test_reverse_and_pre_activation_recovery_use_original_cleanup_guard(self):
        guard = mock.Mock(check=mock.Mock(return_value={'cleanup': True}))
        runner = wrapper.guarded_class(types.SimpleNamespace(Fleet=RecordingFleet), guard)()
        runner.apply('reverse')
        runner.authorize_and_start('reverse', '100')
        runner.edge('reverse', {})
        runner.rollback_before_activation()
        self.assertEqual(guard.check.call_args_list, [mock.call(cleanup=True)] * 4)
        self.assertEqual([e[0] for e in runner.events], ['apply', 'authorize', 'edge', 'rollback'])

    def test_existing_native_launcher_gate_is_still_called_before_guard_check(self):
        guard = mock.Mock()
        runner = wrapper.guarded_class(types.SimpleNamespace(Fleet=RecordingFleet), guard)()
        guard.check.side_effect = lambda: runner.events.append(('guard-check', ()))
        runner.await_launch_gate()
        self.assertEqual([e[0] for e in runner.events], ['native-task-gate', 'guard-check'])

    def test_terminal_classification_retains_failure_and_manifest_binding(self):
        expected = 'b' * 64
        for phase in wrapper.RECOVERED:
            value = wrapper.terminal_fleet({'manifest_sha256': expected, 'phase': phase}, expected)
            self.assertTrue(value['verified_terminal'])
            self.assertFalse(value['transfer_success'])
            self.assertTrue(value['recovered'])
        for phase in ['recovering', 'edge_outcome_unknown_reconciliation_required',
                      'preactivation_recovery_incomplete', 'forward_applying']:
            self.assertIsNone(wrapper.terminal_fleet({'manifest_sha256': expected, 'phase': phase}, expected))
        self.assertIsNone(wrapper.terminal_fleet({'manifest_sha256': 'wrong', 'phase': wrapper.SUCCESS}, expected))

    def test_auxiliary_restore_requires_all_six_exact_successful_nodes(self):
        good = {n: {'returncode': 0, 'same_runtime': True} for n in NODES}
        self.assertTrue(wrapper.all_nodes_ok(good))
        for bad in [None, {}, {n: row for n, row in good.items() if n != 'haos'},
                    {**good, 'haos': {'returncode': 1, 'same_runtime': True}},
                    {**good, 'haos': {'returncode': 0, 'same_runtime': False}}]:
            self.assertFalse(wrapper.all_nodes_ok(bad))

    def test_run_fleet_finishes_verified_recovery_but_never_unknown(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for phase, should_finish in [('restored_before_activation', True),
                                         ('edge_outcome_unknown_reconciliation_required', False)]:
                with self.subTest(phase=phase):
                    class LocalFleet:
                        def __init__(self, *args):
                            self.root = root
                            self.state = {'manifest_sha256': 'b' * 64, 'phase': phase}
                            (root / 'transaction.json').write_text(json.dumps(self.state))
                        def run(self):
                            raise RuntimeError('synthetic failed transfer')
                    cfg = {**config(), 'packet': str(root), 'fleet_manifest': str(root / 'manifest')}
                    guard = mock.Mock(guard_id='guard-one', finish=mock.Mock(return_value={'finished': True}))
                    fake_api = mock.Mock(checked=mock.Mock(return_value=b'{"controller_module_sha256":"c","fleet_sha256":"f"}'),
                                         GuardRefused=api.GuardRefused)
                    base = types.SimpleNamespace(Fleet=LocalFleet)
                    with mock.patch.object(wrapper, 'module', return_value=base), contextlib.redirect_stdout(io.StringIO()):
                        self.assertEqual(wrapper.run_fleet(cfg, guard, fake_api), 1)
                    self.assertEqual(guard.finish.call_count, int(should_finish))
                    report = fake_api.durable.call_args.args[1]
                    self.assertFalse(report['lease_released'])
                    if should_finish:
                        self.assertTrue(report['recovered'])
                        self.assertFalse(report['transfer_success'])
                    else:
                        self.assertTrue(report['requires_reconciliation'])

    def test_durable_success_cannot_overwrite_current_unknown_runner_state(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            (root / 'transaction.json').write_text(json.dumps({
                'manifest_sha256': 'b' * 64, 'phase': wrapper.SUCCESS}))
            class LocalFleet:
                def __init__(self, *args):
                    self.root = root
                    self.state = {'manifest_sha256': 'b' * 64,
                                  'phase': 'edge_outcome_unknown_reconciliation_required'}
                def run(self):
                    raise RuntimeError('synthetic unresolved outcome')
            cfg = {**config(), 'packet': str(root), 'fleet_manifest': str(root / 'manifest')}
            guard = mock.Mock(guard_id='guard-one')
            fake_api = mock.Mock(checked=mock.Mock(return_value=b'{"controller_module_sha256":"c","fleet_sha256":"f"}'),
                                 GuardRefused=api.GuardRefused)
            with mock.patch.object(wrapper, 'module', return_value=types.SimpleNamespace(Fleet=LocalFleet)):
                with self.assertRaises(api.GuardRefused):
                    wrapper.run_fleet(cfg, guard, fake_api)
            guard.finish.assert_not_called()
            fake_api.durable.assert_not_called()


class LauncherContractTests(unittest.TestCase):
    def setup_launch(self, td):
        root = Path(td)
        cfg = {**config(), 'packet': str(root), 'fleet_manifest': str(root / 'manifest.json'),
               'wrapper': '/review/guarded.py', 'wrapper_sha256': 'd' * 64}
        manifest = {'operation_id': cfg['operation_id'], 'external_unit': 'review-transfer.service',
                    'launcher_receipt_path': str(root / 'dispatch.json'),
                    'manifest_on44': '/review/manifest.json',
                    'launcher_ssh_argv': ['ssh', '-o', 'StrictHostKeyChecking=yes', 'fleet-44']}
        raw = json.dumps(manifest).encode()
        cfg['fleet_manifest_sha256'] = wrapper.sha(raw)
        Path(cfg['fleet_manifest']).write_bytes(raw)
        Path(cfg['fleet_manifest']).chmod(0o600)
        channel = ScriptedChannel([lease(cfg)])
        fake_api = types.SimpleNamespace(checked=api.checked, durable=api.durable,
            GuardRefused=api.GuardRefused, Channel=mock.Mock(return_value=channel),
            remote_argv=api.remote_argv)
        receipt = {'operation_id': cfg['operation_id'], 'manifest_sha256': cfg['fleet_manifest_sha256'],
                   'unit': manifest['external_unit'], 'wrapper_sha256': cfg['wrapper_sha256'],
                   'guard_config_sha256': 'e' * 64, 'main_pid': 56789,
                   'phase': 'waiting_for_operator_gate',
                   'cgroup': '/system.slice/' + manifest['external_unit']}
        return cfg, manifest, fake_api, channel, receipt

    def test_launcher_preserves_original_unit_pid_and_manifest_task_gate_contract(self):
        with tempfile.TemporaryDirectory() as td:
            cfg, manifest, fake_api, channel, receipt = self.setup_launch(td)
            response = subprocess.CompletedProcess([], 0, json.dumps(receipt).encode(), b'')
            with mock.patch.object(wrapper.subprocess, 'run', return_value=response) as run, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(wrapper.launch(cfg, '/review/config.json', 'e' * 64, fake_api), 0)
            run.assert_called_once()
            argv = run.call_args.args[0]
            self.assertEqual(argv[:-1], manifest['launcher_ssh_argv'])
            remote = shlex.split(argv[-1])
            self.assertEqual(remote[-6:], [manifest['manifest_on44'], cfg['fleet_manifest_sha256'],
                '/review/config.json', 'e' * 64, cfg['wrapper'], cfg['wrapper_sha256']])
            self.assertIn("'--fleet'", remote[2])
            self.assertIn("'MainPID'", remote[2])
            self.assertTrue(channel.closed)
            saved = json.loads(Path(manifest['launcher_receipt_path'] + '.guarded-result.json').read_bytes())
            self.assertEqual(saved['receipt']['main_pid'], 56789)
            self.assertEqual(saved['receipt']['phase'], 'waiting_for_operator_gate')
            self.assertNotIn('token', json.dumps(saved))

    def test_unknown_dispatch_keeps_claim_and_cannot_dispatch_again(self):
        with tempfile.TemporaryDirectory() as td:
            cfg, manifest, fake_api, _, _ = self.setup_launch(td)
            fake_api.Channel.side_effect = lambda argv: ScriptedChannel([lease(cfg)])
            with mock.patch.object(wrapper.subprocess, 'run', side_effect=subprocess.TimeoutExpired('mock SSH', 40)) as run:
                with self.assertRaises(subprocess.TimeoutExpired):
                    wrapper.launch(cfg, '/review/config.json', 'e' * 64, fake_api)
                self.assertTrue(Path(manifest['launcher_receipt_path']).exists())
                with self.assertRaises(FileExistsError):
                    wrapper.launch(cfg, '/review/config.json', 'e' * 64, fake_api)
            run.assert_called_once()

    def test_mismatched_external_unit_receipt_is_not_accepted(self):
        with tempfile.TemporaryDirectory() as td:
            cfg, manifest, fake_api, _, receipt = self.setup_launch(td)
            receipt['unit'] = 'foreign.service'
            response = subprocess.CompletedProcess([], 0, json.dumps(receipt).encode(), b'')
            with mock.patch.object(wrapper.subprocess, 'run', return_value=response) as run:
                with self.assertRaises(api.GuardRefused):
                    wrapper.launch(cfg, '/review/config.json', 'e' * 64, fake_api)
            run.assert_called_once()
            self.assertTrue(Path(manifest['launcher_receipt_path']).exists())
            self.assertFalse(Path(manifest['launcher_receipt_path'] + '.guarded-result.json').exists())


if __name__ == '__main__':
    unittest.main()
