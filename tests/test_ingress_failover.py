"""Logic-only regressions; real provider/outage acceptance belongs to the live canary."""
import json
import os
import time
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
import gptadmin_ingress_failover as controller


class IngressTransitions(unittest.TestCase):
    def setUp(self):
        self.config = {'failure_threshold': 2, 'name': 'canary.example',
                       'expected': '192.0.2.1', 'value': '192.0.2.2'}
        self.state = {'version': 1, 'binding': 'test', 'phase': 'watching', 'failures': 0}
        self.saved = []

    def step(self, probe_results=(False, True), fresh=True, provider=None):
        with patch.object(controller, 'probe', side_effect=probe_results), \
                patch.object(controller, 'fresh', return_value=fresh), \
                patch.object(controller, 'provider', side_effect=provider or (lambda cfg, action: cfg['value'])):
            return controller.step(self.config, self.state, lambda value: self.saved.append(dict(value)))

    def test_threshold_then_primary_recovery_clears_failures(self):
        self.state = self.step((False,))
        self.assertEqual(self.state['failures'], 1)
        self.assertEqual(self.state['phase'], 'watching')
        self.state = self.step((True,))
        self.assertEqual(self.state['failures'], 0)

    def test_stale_or_failed_candidate_never_mutates_dns(self):
        self.state['failures'] = 1
        for probes, fresh in [((False, False), True), ((False, True), False)]:
            with patch.object(controller, 'provider') as dns:
                with patch.object(controller, 'probe', side_effect=probes), patch.object(controller, 'fresh', return_value=fresh):
                    result = controller.step(self.config, self.state, lambda value: None)
                dns.assert_not_called()
                self.assertEqual(result['phase'], 'watching')

    def test_pending_is_durable_before_single_mutation(self):
        self.state['failures'] = 1
        calls = []
        def dns(cfg, action):
            calls.append(action)
            if action == 'read':
                return cfg['expected']
            self.assertEqual(self.saved[-1]['phase'], 'pending')
            return cfg['value']
        result = self.step(provider=dns)
        self.assertEqual(calls, ['read', 'compare_and_set'])
        self.assertEqual(result['phase'], 'promoted')

    def test_uncertain_restart_reconciles_reads_only(self):
        self.state['failures'] = 1
        def dns(cfg, action):
            if action == 'read':
                return cfg['expected']
            raise TimeoutError()
        self.state = self.step(provider=dns)
        self.assertEqual(self.state['phase'], 'pending')
        for current, phase in [(self.config['expected'], 'pending'), (self.config['value'], 'promoted')]:
            with patch.object(controller, 'probe') as p, patch.object(controller, 'fresh') as f, \
                    patch.object(controller, 'provider', return_value=current) as d:
                self.state = controller.step(self.config, self.state, lambda value: None)
                d.assert_called_once_with(self.config, 'read')
                p.assert_not_called(); f.assert_not_called()
                self.assertEqual(self.state['phase'], phase)

    def test_conflicting_dns_blocks_without_mutation(self):
        self.state['failures'] = 1
        calls = []
        result = self.step(provider=lambda cfg, action: calls.append(action) or '192.0.2.99')
        self.assertEqual(calls, ['read'])
        self.assertEqual(result['phase'], 'blocked')

    def test_promoted_never_fails_back(self):
        self.state['phase'] = 'promoted'
        with patch.object(controller, 'provider') as d, patch.object(controller, 'probe') as p:
            self.assertEqual(controller.step(self.config, self.state, lambda value: None)['phase'], 'promoted')
            d.assert_not_called(); p.assert_not_called()

    def test_failed_pending_save_prevents_provider_mutation(self):
        self.state['failures'] = 1
        with patch.object(controller, 'probe', side_effect=[False, True]), patch.object(controller, 'fresh', return_value=True), \
                patch.object(controller, 'provider', return_value=self.config['expected']) as dns:
            with self.assertRaises(OSError):
                controller.step(self.config, self.state, lambda value: (_ for _ in ()).throw(OSError()))
            dns.assert_called_once_with(self.config, 'read')

    def test_freshness_expiring_during_dns_read_prevents_write(self):
        self.state['failures'] = 1
        with patch.object(controller, 'probe', side_effect=[False, True]), \
                patch.object(controller, 'fresh', side_effect=[True, False]), \
                patch.object(controller, 'provider', return_value=self.config['expected']) as dns:
            result = controller.step(self.config, self.state, lambda value: None)
        dns.assert_called_once_with(self.config, 'read')
        self.assertEqual(result['phase'], 'watching')
        self.assertEqual(result['last_error'], 'candidate_auth_not_fresh')

    def test_adapter_ok_false_after_write_stays_pending(self):
        self.state['failures'] = 1
        config = dict(self.config, provider_argv=['provider'], provider_timeout=95)
        with patch.object(controller, 'probe', side_effect=[False, True]), \
                patch.object(controller, 'fresh', return_value=True), \
                patch.object(controller, 'child_json', side_effect=[
                    {'ok': True, 'value': config['expected']},
                    {'ok': False, 'value': config['value'], 'error': 'write_unresolved'}]):
            result = controller.step(config, self.state, lambda value: None)
        self.assertEqual(result['phase'], 'pending')
        self.assertEqual(result['last_error'], 'pending_write_unconfirmed')

    def test_semantic_write_conflict_stays_blocked_after_restart(self):
        self.state['failures'] = 1
        config = dict(self.config, provider_argv=['provider'], provider_timeout=95)
        for code in ('other_records_changed', 'record_conflict'):
            with patch.object(controller, 'probe', side_effect=[False, True]), \
                    patch.object(controller, 'fresh', return_value=True), \
                    patch.object(controller, 'child_json', side_effect=[
                        {'ok': True, 'value': config['expected']},
                        {'ok': False, 'value': config['value'], 'error': code}]):
                result = controller.step(config, self.state, lambda value: self.saved.append(dict(value)))
            self.assertEqual(result['phase'], 'blocked')
            self.assertEqual(result['last_error'], code)
            with tempfile.TemporaryDirectory(dir=ROOT / '.tmp', prefix='ingress-conflict-') as directory:
                path = Path(directory) / 'state.json'
                controller.save_state(path, result)
                restarted = controller.load_state(path, result['binding'])
            with patch.object(controller, 'provider', return_value=config['value']) as dns:
                self.assertEqual(controller.step(config, restarted, lambda value: None), result)
                dns.assert_not_called()


class ConfigAndPersistence(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=ROOT / '.tmp', prefix='ingress-unit-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = {
            'primary': {'url': 'https://primary.example', 'connect_to': '192.0.2.1',
                        'target': 'shell:primary', 'token_env': 'TEST_TOKEN'},
            'candidate': {'url': 'https://candidate.example', 'connect_to': '192.0.2.2',
                          'target': 'shell:candidate', 'token_env': 'TEST_TOKEN'},
            'auth_sync': {'writer_url': 'https://primary.example', 'writer_connect_to': '192.0.2.1',
                          'reader_url': 'http://127.0.0.1:19002', 'source_id': 'writer-id',
                          'status_file': str(self.root / 'sync.json'), 'max_age': 120,
                          'allow_loopback_http': True},
            'provider_argv': [sys.executable, 'provider.py'], 'name': 'canary.example',
            'expected': '192.0.2.1', 'value': '192.0.2.2', 'state_file': str(self.root / 'state.json')}

    def load(self):
        path = self.root / 'config.json'
        path.write_text(json.dumps(self.config))
        return controller.load_config(path)

    def test_config_binds_source_and_pin_and_rejects_plaintext(self):
        self.load()
        self.config['auth_sync']['writer_connect_to'] = '192.0.2.9'
        with self.assertRaises(ValueError): self.load()
        self.config['auth_sync']['writer_connect_to'] = '192.0.2.1'
        self.config['candidate']['connect_to'] = '192.0.2.9'
        with self.assertRaises(ValueError): self.load()
        self.config['candidate']['connect_to'] = ''
        self.config['candidate']['url'] = 'http://candidate.example'
        with self.assertRaises(ValueError): self.load()

    def test_pending_state_restart_private_bounded_and_config_bound(self):
        c = self.load()
        path = Path(c['state_file'])
        state = {'version': 1, 'binding': controller.config_digest(c), 'phase': 'pending',
                 'failures': 3, 'decision_at': time.time()}
        controller.save_state(path, state)
        self.assertEqual(controller.load_state(path, state['binding']), state)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(sorted(p.name for p in self.root.iterdir()), ['config.json', 'state.json'])
        with self.assertRaises(ValueError): controller.load_state(path, 'other-config')
        path.write_text('x' * (controller.LIMIT + 1))
        with self.assertRaises(ValueError): controller.load_state(path, state['binding'])
        path.unlink(); path.symlink_to(self.root / 'config.json')
        with self.assertRaises(OSError): controller.load_state(path, state['binding'])

    def test_pinned_https_alias_preserves_original_sync_binding(self):
        original_sync = dict(self.config['auth_sync'])
        self.config['primary']['url'] = 'https://canary.example:443'
        loaded = self.load()
        self.assertEqual(loaded['auth_sync'], original_sync)
        self.config['primary']['url'] = 'https://canary.example:8443'
        with self.assertRaises(ValueError): self.load()
        self.config['auth_sync']['writer_url'] = 'https://primary.example:8443'
        self.load()
        self.config['auth_sync']['writer_connect_to'] = '192.0.2.9'
        with self.assertRaises(ValueError): self.load()

    def test_unpinned_alias_rejected_exact_origin_allowed(self):
        self.config['primary']['connect_to'] = ''
        self.config['auth_sync']['writer_connect_to'] = ''
        self.load()
        self.config['primary']['url'] = 'https://canary.example'
        with self.assertRaises(ValueError): self.load()
        self.config['primary']['connect_to'] = '192.0.2.1'
        with self.assertRaises(ValueError): self.load()

    def test_existing_freshness_rejects_wrong_source_old_and_incomplete(self):
        c = self.load()
        _, binding = controller.auth_sync.configuration(controller.SimpleNamespace(**c['auth_sync']))
        status = {'version': 1, 'writer_id': 'writer-id', 'generation': 1, 'snapshot_bytes': 42000,
                  'binding': binding, 'last_success': time.time()}
        path = Path(c['auth_sync']['status_file'])
        path.write_text(json.dumps(status))
        self.assertTrue(controller.fresh(c))
        for invalid in [dict(status, writer_id='other'), dict(status, last_success=time.time()-121),
                        {'last_success': time.time()}]:
            path.write_text(json.dumps(invalid))
            self.assertFalse(controller.fresh(c))

    def test_real_subprocess_output_and_deadline_bounds_only(self):
        # Actual Python subprocess checks OS I/O bounds, not DNS or Node behavior.
        self.assertEqual(controller.child_json([sys.executable, '-c', 'import json; print(json.dumps(dict(ok=True)))'], 2), {'ok': True})
        with self.assertRaises(ValueError):
            controller.child_json([sys.executable, '-c', 'print("x" * 20000)'], 2)
        started = time.monotonic()
        with self.assertRaises(TimeoutError):
            controller.child_json([sys.executable, '-c', 'import time; time.sleep(30)'], .1)
        self.assertLess(time.monotonic() - started, 2)


if __name__ == '__main__':
    unittest.main()
