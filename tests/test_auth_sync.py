"""Narrow local validation tests; real node acceptance lives in e2e/node."""
import importlib.util
import pathlib
import unittest


PATH = pathlib.Path(__file__).resolve().parents[1] / 'scripts/gptadmin_auth_sync.py'
spec = importlib.util.spec_from_file_location('auth_sync', PATH)
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class AuthSyncTests(unittest.TestCase):
    def test_remote_plaintext_and_redirect_destinations_rejected(self):
        for url in ('http://example.com', 'https://user:secret@example.com',
                    'https://example.com/path', 'https://example.com?token=secret'):
            with self.subTest(url=url), self.assertRaises(ValueError):
                sync.validate_origin(url, True)
        with self.assertRaises(ValueError):
            sync.validate_origin('http://127.0.0.1:9001', False)
        self.assertEqual(sync.validate_origin('http://127.0.0.1:9001', True).hostname, '127.0.0.1')

    def test_failure_does_not_reset_age(self):
        binding = {'source_id': 'writer', 'writer_url': 'https://writer.example',
                   'reader_url': 'http://127.0.0.1:19002', 'writer_connect_to': ''}
        state = {'version': 1, 'snapshot_bytes': 100, 'binding': binding,
                 'last_success': 100, 'generation': 3, 'writer_id': 'writer',
                 'last_error': 'writer_http_503'}
        self.assertTrue(sync.freshness(state, 120, now=150, expected=binding)['promotion_eligible'])
        self.assertFalse(sync.freshness(state, 120, now=221, expected=binding)['promotion_eligible'])
        self.assertFalse(sync.freshness(state, 120, now=99, expected=binding)['promotion_eligible'])
        self.assertFalse(sync.freshness({}, 120, now=100, expected=binding)['promotion_eligible'])
        self.assertFalse(sync.freshness(state, 120, now=150)['promotion_eligible'])
        for field in ('version', 'snapshot_bytes', 'generation', 'writer_id', 'binding'):
            missing = {k: v for k, v in state.items() if k != field}
            self.assertFalse(sync.freshness(missing, 120, now=150, expected=binding)['promotion_eligible'])
        for field, value in [('generation', True), ('version', True), ('snapshot_bytes', -1),
                             ('last_success', float('nan')), ('last_success', float('inf'))]:
            self.assertFalse(sync.freshness({**state, field: value}, 120, now=150, expected=binding)['promotion_eligible'])
        for field in binding:
            self.assertFalse(sync.freshness(state, 120, now=150,
                expected={**binding, field: 'other'})['promotion_eligible'])

    def test_identity_and_generation_must_match(self):
        valid = {'writer_id': 'writer', 'generation': 4}
        sync.validate_ack({'ok': True, **valid}, valid)
        for ack in ({'ok': True, 'writer_id': 'other', 'generation': 4},
                    {'ok': True, 'writer_id': 'writer', 'generation': 3},
                    {'ok': False, **valid}):
            with self.assertRaises(ValueError):
                sync.validate_ack(ack, valid)


if __name__ == '__main__':
    unittest.main()
