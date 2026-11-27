import json
import os
import pathlib
import tempfile
import time
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from scripts import gptadmin_node_autoupdate as autoupdate
from scripts import gptadmin_node_manage as node


class FakeRunner:
    def __init__(self, active=True, listen='127.0.0.1:9001'):
        self.active = active
        self.listen = listen
        self.calls = []

    def run(self, argv, timeout=None):
        self.calls.append((list(argv), timeout))
        if argv[:2] == ['systemctl', 'is-active']:
            return SimpleNamespace(returncode=0 if self.active else 3,
                                   stdout='active\n' if self.active else 'inactive\n', stderr='')
        if argv[:2] == ['systemctl', 'show'] and '-p' in argv and 'Listen' in argv:
            return SimpleNamespace(returncode=0, stdout=self.listen + '\n', stderr='')
        if argv[:2] == ['systemctl', 'show']:
            return SimpleNamespace(returncode=0, stdout='MainPID=123\nActiveState=active\n', stderr='')
        return SimpleNamespace(returncode=0, stdout='', stderr='')


class ReleaseControllerBehaviorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = pathlib.Path(self.temp.name)
        self.layout = node.Layout(opt_root=root / 'opt', state_root=root / 'state',
                                  config_root=root / 'config')
        self.layout.versions_root().mkdir(parents=True)

    def version(self, commit, build):
        path = self.layout.versions_root() / f'{commit}-1'
        path.mkdir()
        (path / 'bin').mkdir()
        binary = path / 'bin' / 'gptadmin-node'
        binary.write_bytes(b'fixture')
        binary.chmod(0o755)
        (path / 'manifest.json').write_text(json.dumps({
            'schema': node.PACKAGE_SCHEMA, 'git_commit': commit,
            'build_version': build, 'platform': 'linux', 'arch': 'amd64',
            'source_dirty': False, 'files': {},
        }))
        return path

    def staging(self, commit, build):
        path = self.layout.versions_root() / '.staging-test'
        path.mkdir()
        (path / 'bin').mkdir()
        binary = path / 'bin' / 'gptadmin-node'
        binary.write_bytes(b'fixture')
        binary.chmod(0o755)
        (path / 'manifest.json').write_text(json.dumps({
            'schema': node.PACKAGE_SCHEMA, 'git_commit': commit,
            'build_version': build, 'platform': 'linux', 'arch': 'amd64',
            'source_dirty': False, 'files': {},
        }))
        return path

    def base(self):
        commit = 'a' * 40
        current = self.version(commit, 1)
        node.switch_current(self.layout, current)
        return commit, current

    def test_canonical_socket_unit_is_single_writer_and_loopback_only(self):
        text = node.PRIMARY_SOCKET_UNIT_TEXT
        self.assertEqual(node.PRIMARY_SOCKET_UNIT, 'gptadmin-hub.socket')
        self.assertIn('Service=gptadmin-hub.service', text)
        self.assertIn('ListenStream=127.0.0.1:9001', text)
        self.assertIn('Backlog=4096', text)
        self.assertIn('NoDelay=true', text)
        for forbidden in ('ReusePort', 'PartOf=', 'ListenStream=9001', '0.0.0.0'):
            self.assertNotIn(forbidden, text)

    def test_atomic_link_refuses_external_target_and_implicit_remove(self):
        external = pathlib.Path(self.temp.name) / 'outside'
        external.mkdir()
        with self.assertRaises(node.PreflightError):
            node.atomic_link(self.layout.previous_link(), external,
                             versions_root=self.layout.versions_root())
        with self.assertRaises(node.PreflightError):
            node.atomic_link(self.layout.previous_link(), None,
                             versions_root=self.layout.versions_root())

    def test_canary_environment_is_synthetic_and_scrubbed(self):
        root = pathlib.Path(self.temp.name) / 'canary'
        source = {'CTL_TOKEN': 'prod-token', 'GPTADMIN_NODE_PEERS': '{prod}',
                  'GPTADMIN_QUORUM_CONFIG': '/prod/quorum',
                  'USERIO_AGENT_DELIVER_URL': 'https://prod.invalid'}
        env = node.canary_environment(source, root, 19091)
        self.assertNotEqual(env['CTL_TOKEN'], 'prod-token')
        self.assertEqual(env['CTL_TOKEN'], env['SHELL_TOKEN'])
        self.assertEqual(env['GPTADMIN_NODE_PEERS'], '{}')
        self.assertEqual(env['GPTADMIN_HUB_HOST'], '127.0.0.1')
        self.assertEqual(env['GPTADMIN_HUB_PORT'], '19091')
        self.assertNotIn('GPTADMIN_QUORUM_CONFIG', env)
        self.assertNotIn('USERIO_AGENT_DELIVER_URL', env)
        self.assertEqual(env['ROOTD_AUTO_UPDATE'], '0')

    def test_canary_result_never_contains_pid_or_port(self):
        result = node.sanitize_canary_result({'ok': True, 'version': 'abc',
                                              'handoff_capable': True,
                                              'pid': 42, 'port': 19091})
        self.assertEqual(result, {'ok': True, 'version': 'abc',
                                  'handoff_capable': True})

    def test_socket_detection_requires_live_handoff_capability(self):
        runner = FakeRunner()
        with patch.object(node, 'http_json', return_value=(200, {
                'socket_inherited': True, 'state': 'idle'})):
            self.assertTrue(node.socket_activation_enabled(
                runner, 'http://127.0.0.1:9001', 'token'))
        with patch.object(node, 'http_json', return_value=(412, {
                'detail': 'unavailable without activated listener'})):
            self.assertFalse(node.socket_activation_enabled(
                runner, 'http://127.0.0.1:9001', 'token'))

    def test_writer_handoff_requires_exact_operation_and_lease(self):
        operation = 'node-release:' + 'b' * 40
        payload = {'ready': True, 'state': 'ready', 'operation_id': operation,
                   'lease_id': 'c' * 32, 'socket_inherited': True}
        with patch.object(node, 'http_json', return_value=(200, payload)):
            self.assertEqual(node.writer_handoff(
                'http://127.0.0.1:9001', 'token', operation)['lease_id'], 'c' * 32)
        bad = dict(payload, operation_id='other-operation')
        with patch.object(node, 'http_json', return_value=(200, bad)):
            with self.assertRaises(node.RuntimeFailure):
                node.writer_handoff('http://127.0.0.1:9001', 'token', operation)

    def test_writer_handoff_transport_loss_is_unknown_not_replayed(self):
        with patch.object(node, 'http_json', side_effect=OSError('lost response')):
            with self.assertRaisesRegex(node.RuntimeFailure, 'outcome unknown'):
                node.writer_handoff('http://127.0.0.1:9001', 'token',
                                    'node-release:' + 'b' * 40)

    def test_socket_prepare_installs_but_does_not_start(self):
        runner = FakeRunner()
        with patch.object(node, 'require_root'),              patch.object(node, 'read_installed_env', return_value={
                 'CTL_TOKEN': 'token', 'GPTADMIN_HUB_HOST': '127.0.0.1',
                 'GPTADMIN_HUB_PORT': '9001'}),              patch.object(node, 'http_json', return_value=(412, {
                 'detail': 'node handoff is unavailable without an activated listener'})),              patch.object(node, 'install_primary_socket_activation') as install,              patch('builtins.print'):
            node.cmd_socket_prepare(SimpleNamespace(), self.layout, runner)
        install.assert_called_once_with(runner)
        self.assertFalse(any('start' in call[0] for call in runner.calls))

    def test_parser_exposes_socket_prepare(self):
        args = node.build_parser().parse_args(['socket-prepare'])
        self.assertIs(args.func, node.cmd_socket_prepare)

    def test_install_socket_writes_only_canonical_artifacts_and_reload(self):
        root = pathlib.Path(self.temp.name)
        dropin_dir = root / 'gptadmin-hub.service.d'
        dropin_dir.mkdir()
        socket_path = root / 'gptadmin-hub.socket'
        runner = FakeRunner()
        with patch.object(node, 'PRIMARY_DROPIN', dropin_dir / 'zzzzzz-node-autoupdate.conf'),              patch.object(node, 'PRIMARY_SOCKET_DROPIN', dropin_dir / '99-socket-activation.conf'),              patch.object(node, 'SOCKET_UNIT_PATH', socket_path):
            node.install_primary_socket_activation(runner)
        self.assertEqual(socket_path.read_text(), node.PRIMARY_SOCKET_UNIT_TEXT)
        self.assertIn('Requires=gptadmin-hub.socket',
                      (dropin_dir / '99-socket-activation.conf').read_text())
        # The stop may need the whole graceful shutdown (55s long-polls);
        # these lines are load-bearing for the no-client-error switch.
        dropin_text = (dropin_dir / '99-socket-activation.conf').read_text()
        self.assertIn('KillMode=mixed', dropin_text)
        self.assertIn('TimeoutStopSec=75', dropin_text)
        self.assertIn('Environment=GPTADMIN_SHUTDOWN_GRACE_SECS=70', dropin_text)
        self.assertTrue(any(call[0][:2] == ['systemctl', 'daemon-reload']
                            for call in runner.calls))

    def test_successful_update_commits_current_and_retains_previous(self):
        previous_commit, current = self.base()
        target_commit = 'b' * 40
        staging = self.staging(target_commit, 2)
        runner = FakeRunner()
        env = {'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': '9001'}
        with patch.object(node, 'socket_activation_enabled', return_value=True),              patch.object(node, 'reconcile_stale_artifacts', return_value={'removed': []}),              patch.object(node, 'reconcile_release', return_value={'recovered': False}),              patch.object(node, 'canary_candidate', return_value={
                 'ok': True, 'version': target_commit, 'handoff_capable': True}),              patch.object(node, 'writer_handoff', return_value={
                 'ready': True, 'state': 'ready', 'operation_id': 'node-release:' + target_commit,
                 'lease_id': 'c' * 32, 'socket_inherited': True}),              patch.object(node, 'systemctl'),              patch.object(node, 'wait_healthy', return_value={'git_commit': target_commit}),              patch.object(node, 'verify_inherited_socket', return_value={
                 'git_commit': target_commit, 'fd3': 'socket:[1]'}):
            result = node.update_primary_socket_controller(
                self.layout, runner, staging=staging,
                manifest={'git_commit': target_commit}, env=env, token='token',
                expected_revision=previous_commit)
        self.assertTrue(result['ok'])
        self.assertEqual(node.current_target(self.layout).name, target_commit + '-1')
        self.assertEqual(node.release_slots(self.layout)['previous'], current)
        self.assertEqual(json.loads(self.layout.release_receipt('primary').read_text())['phase'],
                         'committed')

    def test_candidate_failure_rolls_back_without_switching_pointer(self):
        previous_commit, current = self.base()
        target_commit = 'b' * 40
        staging = self.staging(target_commit, 2)
        with patch.object(node, 'socket_activation_enabled', return_value=True),              patch.object(node, 'reconcile_stale_artifacts', return_value={'removed': []}),              patch.object(node, 'reconcile_release', return_value={'recovered': False}),              patch.object(node, 'canary_candidate', side_effect=node.PreflightError('bad candidate')),              patch.object(node, 'systemctl'),              patch.object(node, 'wait_healthy', return_value={'git_commit': previous_commit}),              patch.object(node, 'verify_inherited_socket', return_value={
                 'git_commit': previous_commit, 'fd3': 'socket:[1]'}):
            with self.assertRaisesRegex(node.RuntimeFailure, 'rolled back'):
                node.update_primary_socket_controller(
                    self.layout, FakeRunner(), staging=staging,
                    manifest={'git_commit': target_commit},
                    env={'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': '9001'},
                    token='token', expected_revision=previous_commit)
        self.assertEqual(node.current_target(self.layout), current)
        self.assertEqual(json.loads(self.layout.release_receipt('primary').read_text())['phase'],
                         'rolled-back')

    def test_start_failure_switches_back_to_previous(self):
        previous_commit, current = self.base()
        target_commit = 'b' * 40
        staging = self.staging(target_commit, 2)
        calls = {'start': 0}
        def systemctl_side(_runner, *args, **kwargs):
            if args and args[0] == 'start':
                calls['start'] += 1
                if calls['start'] == 1:
                    raise node.RuntimeFailure('candidate start failed')
            return SimpleNamespace(returncode=0, stdout='', stderr='')
        with patch.object(node, 'socket_activation_enabled', return_value=True),              patch.object(node, 'reconcile_stale_artifacts', return_value={'removed': []}),              patch.object(node, 'reconcile_release', return_value={'recovered': False}),              patch.object(node, 'canary_candidate', return_value={
                 'ok': True, 'version': target_commit, 'handoff_capable': True}),              patch.object(node, 'writer_handoff', return_value={
                 'ready': True, 'state': 'ready', 'operation_id': 'node-release:' + target_commit,
                 'lease_id': 'c' * 32, 'socket_inherited': True}),              patch.object(node, 'systemctl', side_effect=systemctl_side),              patch.object(node, 'wait_healthy', return_value={'git_commit': previous_commit}),              patch.object(node, 'verify_inherited_socket', return_value={
                 'git_commit': previous_commit, 'fd3': 'socket:[1]'}):
            with self.assertRaisesRegex(node.RuntimeFailure, 'rolled back'):
                node.update_primary_socket_controller(
                    self.layout, FakeRunner(), staging=staging,
                    manifest={'git_commit': target_commit},
                    env={'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': '9001'},
                    token='token', expected_revision=previous_commit)
        self.assertEqual(node.current_target(self.layout), current)
        self.assertEqual(calls['start'], 2)

    def test_reconcile_finishes_target_only_after_runtime_verification(self):
        previous_commit, previous = self.base()
        target_commit = 'b' * 40
        target = self.version(target_commit, 2)
        node.switch_current(self.layout, target, expected_revision=previous_commit)
        node.write_release_receipt(self.layout, {
            'phase': 'starting', 'previous_dir': previous.name,
            'previous_commit': previous_commit, 'target_dir': target.name,
            'target_commit': target_commit})
        with patch.object(node, '_runtime_snapshot', return_value={'active': True}),              patch.object(node, '_runtime_matches', return_value=True):
            result = node.reconcile_release(self.layout, FakeRunner(),
                                            {'GPTADMIN_HUB_HOST': '127.0.0.1',
                                             'GPTADMIN_HUB_PORT': '9001'}, 'token')
        self.assertEqual(result['action'], 'finish')
        self.assertEqual(json.loads(self.layout.release_receipt('primary').read_text())['phase'],
                         'committed')

    def test_reconcile_marks_ambiguous_pointer_manual_required(self):
        previous_commit, previous = self.base()
        target_commit = 'b' * 40
        target = self.version(target_commit, 2)
        third = self.version('c' * 40, 3)
        node.switch_current(self.layout, third, expected_revision=previous_commit)
        node.write_release_receipt(self.layout, {
            'phase': 'starting', 'previous_dir': previous.name,
            'previous_commit': previous_commit, 'target_dir': target.name,
            'target_commit': target_commit})
        with patch.object(node, '_runtime_snapshot', return_value={}):
            with self.assertRaises(node.RuntimeFailure):
                node.reconcile_release(self.layout, FakeRunner(),
                                       {'GPTADMIN_HUB_HOST': '127.0.0.1',
                                        'GPTADMIN_HUB_PORT': '9001'}, 'token')
        self.assertEqual(json.loads(self.layout.release_receipt('primary').read_text())['phase'],
                         'manual-required')

    def test_active_receipt_blocks_stale_cleanup(self):
        staging = self.layout.versions_root() / '.staging-old'
        staging.mkdir()
        node.write_release_receipt(self.layout, {'phase': 'starting'})
        result = node.reconcile_stale_artifacts(self.layout, max_age_seconds=0)
        self.assertEqual(result['reason'], 'deployment-active')
        self.assertTrue(staging.exists())

    def test_known_old_hold_is_archived_only_after_verified_newer_runtime(self):
        old_commit = 'a' * 40
        new_commit = 'b' * 40
        old = self.version(old_commit, 1)
        current = self.version(new_commit, 2)
        node.switch_current(self.layout, current)
        state = self.layout.state('autoupdate')
        state.mkdir(parents=True)
        hold = state / 'hold.json'
        hold.write_text(json.dumps({
            'complete': False, 'source': old_commit,
            'reason': 'manager_update_failed_review_required', 'instance': 'primary'}))
        old_time = time.time() - 60
        os.utime(hold, (old_time, old_time))
        with patch.object(node, '_runtime_snapshot', return_value={'active': True}),              patch.object(node, '_runtime_matches', return_value=True):
            result = node.reconcile_stale_artifacts(
                self.layout, FakeRunner(), {'GPTADMIN_HUB_HOST': '127.0.0.1',
                                            'GPTADMIN_HUB_PORT': '9001'}, 'token',
                max_age_seconds=0)
        self.assertFalse(hold.exists())
        self.assertTrue(any('hold.archived-' in item for item in result['removed']))
        self.assertTrue(old.exists())

    def test_unknown_hold_is_preserved(self):
        current = self.version('b' * 40, 2)
        node.switch_current(self.layout, current)
        state = self.layout.state('autoupdate')
        state.mkdir(parents=True)
        hold = state / 'hold.json'
        hold.write_text(json.dumps({'source': 'a' * 40, 'reason': 'unknown'}))
        result = node.reconcile_stale_artifacts(self.layout, max_age_seconds=0)
        self.assertTrue(hold.exists())
        self.assertEqual(result['removed'], [])

    def test_autoupdate_selects_socket_only_from_live_capability(self):
        manager = SimpleNamespace(
            Runner=lambda: FakeRunner(), DEFAULT_LAYOUT=self.layout,
            read_installed_env=lambda *_: {'CTL_TOKEN': 'token',
                                           'GPTADMIN_HUB_HOST': '127.0.0.1',
                                           'GPTADMIN_HUB_PORT': '9001'},
            read_admin_token=lambda *_: 'token',
            origin_from_env=lambda *_: 'http://127.0.0.1:9001',
            socket_activation_enabled=lambda *_: True)
        self.assertEqual(autoupdate.select_update_mode(manager, 'primary'),
                         'socket-primary')
        self.assertEqual(autoupdate.select_update_mode(manager, 'node-44'),
                         'legacy-drain')

    def test_autoupdate_source_passes_expected_revision(self):
        source = pathlib.Path(autoupdate.__file__).read_text()
        self.assertIn("'--expected-revision',live_versions[name]['git_commit']", source)
        self.assertIn('reconcile_stale_artifacts', source)


if __name__ == '__main__':
    unittest.main()
