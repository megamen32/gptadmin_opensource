"""Local decision tests only; real provider acceptance is a separate canary."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('adapter', Path(__file__).parents[1] / 'scripts/gptadmin_dns_sweb.py')
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class DNSDecisionTests(unittest.TestCase):
    def test_uncertain_accepted_write_is_reconciled_without_replay(self):
        rows = [{'name': 'node', 'type': 'A', 'value': '192.0.2.1', 'index': 9}]
        calls = []
        def rpc(method, params):
            calls.append((method, dict(params)))
            if method == 'info':
                return {'result': [dict(r) for r in rows]}
            rows[0]['value'] = params['value']
            raise TimeoutError('response lost')
        result = adapter.execute({'action': 'compare_and_set', 'name': 'node.example.com',
                                  'expected': '192.0.2.1', 'value': '192.0.2.2'},
                                 'example.com', 'node.example.com', rpc)
        self.assertEqual(result['value'], '192.0.2.2')
        self.assertTrue(result['ok'])
        writes = [p for m, p in calls if m != 'info']
        self.assertEqual(len(writes), 1)
        self.assertEqual(writes[0]['action'], 'edit')
        self.assertEqual(writes[0]['index'], 9)

    def test_conflict_does_not_write(self):
        calls = []
        def rpc(method, params):
            calls.append(method)
            return {'result': [{'name': 'node', 'type': 'A', 'value': '192.0.2.3', 'index': 4}]}
        result = adapter.execute({'action': 'compare_and_set', 'name': 'node.example.com',
                                  'expected': '192.0.2.1', 'value': '192.0.2.2'},
                                 'example.com', 'node.example.com', rpc)
        self.assertFalse(result['ok'])
        self.assertEqual(calls, ['info'])

    def test_unresolved_write_is_not_repeated(self):
        calls = []
        def rpc(method, params):
            calls.append(method)
            if method != 'info':
                raise TimeoutError()
            return {'result': [{'name': 'node', 'type': 'A', 'value': '192.0.2.1', 'index': 4}]}
        result = adapter.execute({'action': 'compare_and_set', 'name': 'node.example.com',
                                  'expected': '192.0.2.1', 'value': '192.0.2.2'},
                                 'example.com', 'node.example.com', rpc)
        self.assertFalse(result['ok'])
        self.assertEqual(calls.count('editMain'), 1)

    def test_another_hostname_is_rejected_before_rpc(self):
        def rpc(*args):
            self.fail('RPC must not run')
        with self.assertRaises(ValueError):
            adapter.execute({'action': 'read', 'name': 'other.example.com'},
                            'example.com', 'node.example.com', rpc)

    def test_multiple_records_do_not_get_deleted_or_replaced(self):
        def rpc(method, params):
            self.assertEqual(method, 'info')
            return {'result': [{'name': 'node', 'type': 'A', 'value': '192.0.2.1', 'index': i} for i in (1, 2)]}
        with self.assertRaises(ValueError):
            adapter.execute({'action': 'read', 'name': 'node.example.com'},
                            'example.com', 'node.example.com', rpc)


if __name__ == '__main__':
    unittest.main()
