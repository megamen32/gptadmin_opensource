"""Local decision tests only; real provider acceptance is a separate canary."""
import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path
import unittest
from unittest.mock import patch

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


class DNSAppendTests(unittest.TestCase):
    name = 'frp.example.com'
    value = '192.0.2.22'

    def setUp(self):
        self.rows = [
            {'name': 'frp', 'type': 'A', 'value': '192.0.2.21', 'index': 1},
            {'name': '*', 'type': 'A', 'value': '192.0.2.20', 'index': 2},
            {'domain': 'frp', 'type': 'TXT', 'value': 'keep', 'index': 3},
        ]
        self.calls = []

    def rpc(self, method, params):
        self.calls.append((method, params))
        if method == 'info':
            return {'result': [dict(row) for row in self.rows]}
        self.assertEqual((method, params), ('editMain', {
            'domain': 'example.com', 'action': 'add', 'name': 'frp',
            'type': 'A', 'value': self.value, 'prefix': '',
        }))
        self.rows.append({'name': 'frp', 'type': 'A', 'value': self.value, 'index': 4})
        raise TimeoutError('response lost after commit')

    def append(self, rpc=None, enabled=True):
        # The simulated provider owns only documentation addresses.
        with patch.object(adapter, 'public_relay_address', return_value=self.value):
            return adapter.execute({'action': 'append_a', 'name': self.name, 'value': self.value},
                                   'example.com', self.name, rpc or self.rpc, allow_append_a=enabled)

    def test_argument_free_adapter_allows_only_pool_operations(self):
        env = {'PATH': os.defpath, 'GPTADMIN_FRP_DNS_NAME': self.name,
               'GPTADMIN_FRP_DNS_ZONE': 'example.com',
               'GPTADMIN_FRP_DNS_CREDENTIALS_FILE': '/unused'}
        result = subprocess.run([sys.executable, adapter.__file__], env=env, text=True,
                                input=json.dumps({'action': 'compare_and_set', 'name': self.name}),
                                capture_output=True, check=False)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(json.loads(result.stdout), {'ok': False, 'error': 'ValueError'})

    def test_lost_response_is_reconciled_and_existing_pool_is_preserved(self):
        before = [dict(row) for row in self.rows]
        self.assertEqual(self.append(), {'ok': True, 'value': self.value, 'changed': True})
        self.assertEqual(self.rows[:-1], before)
        self.assertEqual([method for method, _ in self.calls], ['info', 'editMain', 'info'])
        self.calls.clear()
        self.assertEqual(self.append(), {'ok': True, 'value': self.value, 'changed': False})
        self.assertEqual([method for method, _ in self.calls], ['info'])

    def test_unexpected_post_write_state_is_unresolved_without_replay(self):
        def rpc(method, params):
            try:
                return self.rpc(method, params)
            except TimeoutError:
                self.rows.pop(0)
                raise
        self.assertEqual(self.append(rpc), {'ok': False, 'error': 'write_unresolved'})
        self.assertEqual([method for method, _ in self.calls], ['info', 'editMain', 'info'])
        self.calls.clear()
        self.assertEqual(adapter.execute({'action': 'read_a_pool', 'name': self.name},
                                         'example.com', self.name, self.rpc, allow_append_a=True),
                         {'ok': True, 'values': [self.value]})
        self.assertEqual([method for method, _ in self.calls], ['info'])

    def test_concurrent_independent_a_addition_is_accepted(self):
        other = {'name': 'frp', 'type': 'A', 'value': '8.8.4.4', 'index': 5}
        calls = []

        def rpc(method, params):
            calls.append((method, params))
            if method == 'info':
                rows = list(self.rows)
                if len(calls) == 3:
                    rows.append(dict(other))
                return {'result': rows}
            self.rows.append({'name': 'frp', 'type': 'A', 'value': self.value, 'index': 4})
            return {'ok': True}

        self.assertEqual(self.append(rpc), {'ok': True, 'value': self.value, 'changed': True})
        self.assertEqual([method for method, _ in calls], ['info', 'editMain', 'info'])

    def test_concurrent_change_to_existing_a_payload_is_unresolved(self):
        calls = []

        def rpc(method, params):
            calls.append(method)
            if method == 'info':
                rows = [dict(row) for row in self.rows]
                if len(calls) == 3:
                    rows[0]['ttl'] = 900
                return {'result': rows}
            self.rows.append({'name': 'frp', 'type': 'A', 'value': self.value, 'index': 4})
            return {'ok': True}

        self.assertEqual(self.append(rpc), {'ok': False, 'error': 'write_unresolved'})

    def test_needs_opt_in_and_public_address(self):
        with self.assertRaises(ValueError):
            self.append(enabled=False)
        with self.assertRaisesRegex(ValueError, 'public_unicast'):
            adapter.execute({'action': 'append_a', 'name': self.name, 'value': '127.0.0.1'},
                            'example.com', self.name, self.rpc, allow_append_a=True)
        self.assertEqual(self.calls, [])

    def test_creates_exact_record_without_replacing_wildcard_or_txt(self):
        self.rows.pop(0)
        before = [dict(row) for row in self.rows]
        self.assertEqual(self.append(), {'ok': True, 'value': self.value, 'changed': True})
        self.assertEqual(self.rows[:-1], before)
        self.assertEqual([method for method, _ in self.calls], ['info', 'editMain', 'info'])

    def test_refuses_exact_alias_or_delegation_without_writing(self):
        for record_type in ('CNAME', 'DNAME', 'NS', 'ALIAS', 'ANAME'):
            with self.subTest(record_type=record_type):
                self.calls.clear()
                self.rows[-1] = {'name': 'FRP.EXAMPLE.COM.', 'type': record_type, 'value': 'other.example.com.'}
                with self.assertRaisesRegex(ValueError, 'incompatible_exact_record'):
                    self.append()
                self.assertEqual([method for method, _ in self.calls], ['info'])


if __name__ == '__main__':
    unittest.main()
