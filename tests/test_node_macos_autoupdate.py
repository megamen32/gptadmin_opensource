"""stdlib-only safety checks for the macOS Node updater URL boundary."""
import sys
import unittest
import fcntl
import os
import subprocess
from pathlib import Path
import json
import tempfile
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
import gptadmin_node_macos_autoupdate as updater
import gptadmin_node_macos_manage as manager


class MacNodeUpdaterTests(unittest.TestCase):
    def test_install_reports_prior_node_restore_failure_with_version_readback_context(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            versions = root / 'versions'
            versions.mkdir()
            previous = versions / ('a' * 40 + '-1')
            previous.mkdir()
            current = root / 'current'
            current.symlink_to(previous.relative_to(root))
            (root / 'node.env.json').write_text('{}\n')
            args = SimpleNamespace(package='candidate.tar.gz', allow_dirty=False)
            with patch.object(manager, 'roots', return_value=(root, root / 'node.plist')), \
                 patch.object(manager, 'validate_package', return_value={'git_commit':'b'*40,'build_version':2}), \
                 patch.object(manager, 'loaded', return_value=False), \
                 patch.object(manager, 'read_state', return_value={'build_version':1,'git_commit':'a'*40}), \
                 patch.object(manager, 'write_plist'), patch.object(manager, 'load_service'), \
                 patch.object(manager, 'wait_ready', side_effect=[manager.NodeError('candidate unhealthy'), manager.NodeError('prior version/commit mismatch')]) as readback:
                with self.assertRaisesRegex(manager.NodeError, 'prior Node restore failed verification.*version/commit mismatch'):
                    manager.cmd_install(args)
            self.assertEqual(readback.call_args_list[1].args[1], {'build_version':1,'git_commit':'a'*40})
            self.assertEqual(current.resolve(), previous)

    def test_uninstall_refuses_when_drain_cancel_cannot_be_verified(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plist = root / 'node.plist'
            plist.write_text('service plist')
            (root / 'node.env.json').write_text('{"CTL_TOKEN":"test"}\n')
            with patch.object(manager, 'roots', return_value=(root, plist)), \
                 patch.object(manager, 'loaded', return_value=True), \
                 patch.object(manager, 'capture_autoupdate_state', return_value={'config':None,'plist':None,'loaded':False}), \
                 patch.object(manager, 'drain_begin'), \
                 patch.object(manager, 'drain_wait_ready', side_effect=manager.NodeError('drain busy')), \
                 patch.object(manager, 'drain_cancel', return_value=False) as cancel, \
                 patch.object(manager, 'cmd_autoupdate') as updater_state, \
                 patch.object(manager.subprocess, 'run') as launchctl:
                with self.assertRaisesRegex(manager.NodeError, 'cancellation is unverified; service left running'):
                    manager.cmd_uninstall(SimpleNamespace(purge=True))
            cancel.assert_called_once()
            updater_state.assert_not_called()
            launchctl.assert_not_called()
            self.assertTrue(plist.exists())

    def test_uninstall_drain_failure_cancels_and_preserves_service_and_updater_state(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plist = root / 'node.plist'
            plist.write_text('service plist')
            env = root / 'node.env.json'
            env.write_text('{"CTL_TOKEN":"test"}\n')
            autoupdate = root / 'autoupdate.json'
            autoupdate.write_text('{"enabled":true}\n')
            events = []
            with patch.object(manager, 'roots', return_value=(root, plist)), \
                 patch.object(manager, 'service_label', return_value='com.example.node'), \
                 patch.object(manager, 'loaded', return_value=True), \
                 patch.object(manager, 'capture_autoupdate_state', return_value={'config':None,'plist':None,'loaded':False}), \
                 patch.object(manager, 'drain_begin', side_effect=lambda _env: events.append('begin')), \
                 patch.object(manager, 'drain_wait_ready', side_effect=manager.NodeError('drain busy')), \
                 patch.object(manager, 'drain_cancel', side_effect=lambda _env: events.append('cancel') or True), \
                 patch.object(manager, 'cmd_autoupdate', side_effect=lambda _args: events.append('disable')), \
                 patch.object(manager.subprocess, 'run', side_effect=lambda *a, **k: events.append('bootout')):
                with self.assertRaisesRegex(manager.NodeError, 'drain busy'):
                    manager.cmd_uninstall(SimpleNamespace(purge=False))
            self.assertEqual(events, ['begin', 'cancel'])
            self.assertTrue(plist.exists())
            self.assertEqual(autoupdate.read_text(), '{"enabled":true}\n')

    def test_uninstall_completes_drain_before_changing_updater_or_stopping_service(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plist = root / 'node.plist'
            plist.write_text('service plist')
            (root / 'node.env.json').write_text('{"CTL_TOKEN":"test"}\n')
            events = []
            with patch.object(manager, 'roots', return_value=(root, plist)), \
                 patch.object(manager, 'service_label', return_value='com.example.node'), \
                 patch.object(manager, 'loaded', side_effect=[True, False]), \
                 patch.object(manager, 'capture_autoupdate_state', return_value={'config':None,'plist':None,'loaded':False}), \
                 patch.object(manager, 'drain_begin', side_effect=lambda _env: events.append('begin')), \
                 patch.object(manager, 'drain_wait_ready', side_effect=lambda _env: events.append('ready')) as ready, \
                 patch.object(manager, 'cmd_autoupdate', side_effect=lambda _args: events.append('disable')), \
                 patch.object(manager.subprocess, 'run', side_effect=lambda *a, **k: events.append('bootout')):
                manager.cmd_uninstall(SimpleNamespace(purge=False))
            self.assertEqual(events, ['begin', 'ready', 'disable', 'bootout'])
            self.assertFalse(plist.exists())

    def test_uninstall_disable_failure_restores_updater_verifies_node_and_cancels_drain(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plist = root / 'node.plist'
            plist.write_text('service plist')
            (root / 'node.env.json').write_text('{"CTL_TOKEN":"test"}\n')
            events = []
            snapshot = {'config':b'{"enabled":true}\n','config_mode':0o600,
                        'plist':b'prior updater plist','plist_mode':0o600,'loaded':True}
            with patch.object(manager, 'roots', return_value=(root, plist)), \
                 patch.object(manager, 'service_label', return_value='com.example.node'), \
                 patch.object(manager, 'loaded', return_value=True), \
                 patch.object(manager, 'drain_begin', side_effect=lambda _env: events.append('begin')), \
                 patch.object(manager, 'drain_wait_ready', side_effect=lambda _env: events.append('ready')), \
                 patch.object(manager, 'capture_autoupdate_state', return_value=snapshot, create=True), \
                 patch.object(manager, 'cmd_autoupdate', side_effect=manager.NodeError('updater disable failed')), \
                 patch.object(manager, 'restore_autoupdate_state', side_effect=lambda _root, _snap: events.append('restore updater'), create=True) as restore, \
                 patch.object(manager, 'read_state', return_value={'build_version':9,'git_commit':'a'*40}), \
                 patch.object(manager, 'wait_ready', side_effect=lambda _env, _state: events.append('verify node')) as ready, \
                 patch.object(manager, 'drain_cancel', side_effect=lambda _env: events.append('cancel drain') or True), \
                 patch.object(manager.subprocess, 'run') as launchctl:
                with self.assertRaisesRegex(manager.NodeError, 'updater disable failed.*updater restored.*drain cancelled'):
                    manager.cmd_uninstall(SimpleNamespace(purge=False))
            self.assertEqual(events, ['begin','ready','restore updater','verify node','cancel drain'])
            restore.assert_called_once_with(root, snapshot)
            ready.assert_called_once()
            launchctl.assert_not_called()
            self.assertTrue(plist.exists())

    def test_updater_compensation_restores_exact_config_and_launchagent_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            home = base / 'home'
            updater_plist = home / 'Library/LaunchAgents/com.gptadmin.node.autoupdate.plist'
            root = base / 'node-root'
            config = root / 'autoupdate.json'
            snapshot = {'config':{'data':b'{"enabled":true}\n','mode':0o600},
                        'plist':{'data':b'previous-launchagent','mode':0o600},'loaded':False}
            with patch.object(Path, 'home', return_value=home), \
                 patch.object(manager, 'launchd_job_loaded', return_value=False):
                manager.restore_autoupdate_state(root, snapshot)
            self.assertEqual(config.read_bytes(), snapshot['config']['data'])
            self.assertEqual(updater_plist.read_bytes(), snapshot['plist']['data'])
            self.assertEqual(config.stat().st_mode & 0o777, 0o600)
            self.assertEqual(updater_plist.stat().st_mode & 0o777, 0o600)

    def test_uninstall_bootout_failure_restores_updater_and_node_then_cancels_drain(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plist = root / 'node.plist'
            plist.write_text('service plist')
            (root / 'node.env.json').write_text('{"CTL_TOKEN":"test"}\n')
            events = []
            snapshot = {'config':b'{"enabled":true}\n','config_mode':0o600,
                        'plist':b'prior updater plist','plist_mode':0o600,'loaded':True}
            with patch.object(manager, 'roots', return_value=(root, plist)), \
                 patch.object(manager, 'service_label', return_value='com.example.node'), \
                 patch.object(manager, 'loaded', return_value=True), \
                 patch.object(manager, 'drain_begin', side_effect=lambda _env: events.append('begin')), \
                 patch.object(manager, 'drain_wait_ready', side_effect=lambda _env: events.append('ready')), \
                 patch.object(manager, 'capture_autoupdate_state', return_value=snapshot, create=True), \
                 patch.object(manager, 'cmd_autoupdate', side_effect=lambda _args: events.append('disable updater')), \
                 patch.object(manager, 'restore_autoupdate_state', side_effect=lambda _root, _snap: events.append('restore updater'), create=True) as restore, \
                 patch.object(manager, 'read_state', return_value={'build_version':9,'git_commit':'a'*40}), \
                 patch.object(manager, 'wait_ready', side_effect=lambda _env, _state: events.append('verify node')) as ready, \
                 patch.object(manager, 'drain_cancel', side_effect=lambda _env: events.append('cancel drain') or True), \
                 patch.object(manager.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'launchctl bootout')):
                with self.assertRaisesRegex(manager.NodeError, 'uninstall failed.*updater restored.*drain cancelled'):
                    manager.cmd_uninstall(SimpleNamespace(purge=False))
            self.assertEqual(events, ['begin','ready','disable updater','restore updater','verify node','cancel drain'])
            restore.assert_called_once_with(root, snapshot)
            ready.assert_called_once()
            self.assertTrue(plist.exists())

    def test_install_rejects_stale_or_reused_build_number_before_switch(self):
        installed = {'build_version': 1079, 'git_commit': 'b' * 40}
        with self.assertRaisesRegex(manager.NodeError, 'older than the installed'):
            manager.ensure_not_downgrade({'build_version': 1078, 'git_commit': 'a' * 40}, installed)
        with self.assertRaisesRegex(manager.NodeError, 'different source'):
            manager.ensure_not_downgrade({'build_version': 1079, 'git_commit': 'a' * 40}, installed)
        manager.ensure_not_downgrade({'build_version': 1080, 'git_commit': 'c' * 40}, installed)

    def test_concurrent_install_fails_before_package_validation_or_lifecycle(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            fd = None
            with patch.object(manager, 'roots', return_value=(root, root / 'node.plist')):
                lock = root / 'update.lock'
                fd = os.open(lock, os.O_RDWR | os.O_CREAT, 0o600)
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                with patch.object(manager, 'validate_package') as validate:
                    with self.assertRaisesRegex(manager.NodeError, 'already running'):
                        manager.cmd_install(SimpleNamespace(package='candidate.tar.gz', allow_dirty=False))
                    validate.assert_not_called()
                os.close(fd)

    def test_drain_requires_verified_readiness_before_install_switch_or_launchd_bootout(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            versions = root / 'versions'
            versions.mkdir()
            previous = versions / 'old-1'
            previous.mkdir()
            current = root / 'current'
            current.symlink_to(previous.relative_to(root))
            (root / 'node.env.json').write_text(json.dumps({'CTL_TOKEN':'test-token'}))
            plist = root / 'node.plist'
            args = SimpleNamespace(package='candidate.tar.gz', allow_dirty=False)
            with patch.object(manager, 'roots', return_value=(root, plist)), \
                 patch.object(manager, 'validate_package', return_value={'git_commit':'a'*40,'build_version':2}), \
                 patch.object(manager, 'loaded', return_value=True), \
                 patch.object(manager, 'drain_begin') as begin, \
                 patch.object(manager, 'drain_wait_ready', side_effect=manager.NodeError('busy')), \
                 patch.object(manager, 'drain_cancel', return_value=True) as cancel, \
                 patch.object(manager, 'load_service') as load:
                with self.assertRaisesRegex(manager.NodeError, 'busy'):
                    manager.cmd_install(args)
            self.assertEqual(current.resolve(), previous)
            begin.assert_called_once()
            cancel.assert_called_once()
            load.assert_not_called()

    def test_missing_drain_credential_fails_closed(self):
        with self.assertRaisesRegex(manager.NodeError, 'credential is missing'):
            manager.drain_request({'GPTADMIN_HUB_PORT':'1'}, 'begin')

    def test_launchd_environment_persists_ctl_token_for_future_drains(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'current/bin').mkdir(parents=True)
            (root / 'node.env.json').write_text('{}\n')
            with patch.object(manager, 'service_label', return_value='com.example.node'):
                manager.write_plist(root, root / 'node.plist')
            env = json.loads((root / 'node.env.json').read_text())
            self.assertTrue(env['CTL_TOKEN'])
            self.assertEqual(env['GPTADMIN_ROOT'], str(root))

    def test_only_exact_https_release_hosts_are_allowed(self):
        for url in (
            'https://api.github.com/repos/megamen32/gptadmin_opensource/releases',
            'https://github.com/megamen32/gptadmin_opensource/releases/download/tag/file',
            'https://release-assets.githubusercontent.com/object',
        ):
            with self.subTest(url=url):
                updater.public_url(url)
        for url in (
            'http://api.github.com/releases',
            'https://github.com.evil.example/releases',
            'https://user@github.com/releases',
            'https://github.com:444/releases',
        ):
            with self.subTest(url=url), self.assertRaises(ValueError):
                updater.public_url(url)

    def test_redirect_handler_rejects_non_public_destination(self):
        handler = updater.PublicRedirect()
        with self.assertRaises(ValueError):
            handler.redirect_request(None, None, 302, 'Found', {}, 'https://attacker.example/package')


if __name__ == '__main__':
    unittest.main()
