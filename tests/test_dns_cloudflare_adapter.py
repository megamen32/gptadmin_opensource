"""Cloudflare adapter contract tests with an in-memory provider."""

import importlib.util
import json
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location(
    'cloudflare_adapter', Path(__file__).parents[1] / 'scripts/gptadmin_dns_cloudflare.py')
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)

NAME = 'relay.example.com'


def row(value, kind='A', proxied=False, name=NAME):
    return {'name': name, 'type': kind, 'content': value, 'proxied': proxied}


class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.rows = [row('1.1.1.1'), row('8.8.8.8', name='else.example.com')]
        self.writes = []
        self.lose_response = False

    def provider(self, method, *, query=None, record=None):
        if method == 'GET':
            self.assertEqual(query['name.exact'], NAME)
            page = query['page']
            # One record per page exercises mixed A/AAAA and concurrent additions.
            return {'result': self.rows[page - 1:page], 'result_info': {'total_pages': len(self.rows)}}
        self.assertEqual(method, 'POST')
        self.writes.append(record)
        self.rows.append(dict(record))
        if self.lose_response:
            self.rows.append(row('9.9.9.9'))
            raise TimeoutError('provider response lost')
        return {'result': record}

    def execute(self, action, **kwargs):
        return adapter.execute({'name': NAME, 'action': action, **kwargs},
                               'example.com', NAME, self.provider)

    def test_creates_one_dns_only_row_and_retry_is_idempotent(self):
        self.assertTrue(self.execute('append_a', value='8.8.4.4')['changed'])
        self.assertFalse(self.execute('append_a', value='8.8.4.4')['changed'])
        self.assertEqual(self.writes, [dict(row('8.8.4.4'), ttl=1)])
        self.assertEqual(self.execute('read_a_pool'), {'ok': True, 'values': ['1.1.1.1', '8.8.4.4']})

    def test_ambiguous_create_is_reconciled_without_replay(self):
        self.lose_response = True
        self.assertTrue(self.execute('append_a', value='8.8.4.4')['ok'])
        self.assertEqual(self.execute('read_a_pool')['values'], ['1.1.1.1', '8.8.4.4', '9.9.9.9'])
        self.assertEqual(len(self.writes), 1)

    def test_unresolved_and_unapplied_write_is_not_retried(self):
        def provider(method, **kwargs):
            if method == 'POST':
                self.writes.append(kwargs['record'])
                raise TimeoutError()
            return self.provider(method, **kwargs)
        result = adapter.execute({'action': 'append_a', 'name': NAME, 'value': '8.8.4.4'},
                                 'example.com', NAME, provider)
        self.assertEqual(result, {'ok': False, 'error': 'write_unresolved'})
        self.assertEqual(len(self.writes), 1)

    def test_mixed_proxy_and_alias_modes_block_before_write(self):
        for incompatible in (row('1.0.0.1', proxied=True), row('2001:db8::1', 'AAAA', True),
                             row('service.example.com', 'CNAME'), row('ns.example.com', 'NS')):
            with self.subTest(kind=incompatible['type']):
                self.rows = [row('1.1.1.1'), incompatible]
                with self.assertRaises(adapter.AdapterError):
                    self.execute('append_a', value='8.8.4.4')
        self.assertFalse(self.writes)

    def test_out_of_scope_and_invalid_inputs_do_not_reach_provider(self):
        rpc = Mock(side_effect=AssertionError('unexpected provider call'))
        for fields in ({'name': 'other.example.com'}, {'action': 'compare_and_set'},
                       {'value': '203.0.113.10'}, {'value': 1234}, {'value': '224.0.0.1'}):
            msg = {'action': 'append_a', 'name': NAME, 'value': '8.8.4.4', **fields}
            with self.subTest(fields=fields), self.assertRaises(adapter.AdapterError):
                adapter.execute(msg, 'example.com', NAME, rpc)
        rpc.assert_not_called()

    @patch.object(adapter.http.client, 'HTTPSConnection')
    def test_http_uses_exact_filtered_path_and_never_follows_redirect(self, connection):
        token = 'test-token'
        cf = adapter.Cloudflare({'zone_id': 'a' * 32, 'api_token': token})
        client = connection.return_value
        response = client.getresponse.return_value
        response.status = 200
        response.read.return_value = json.dumps({
            'success': True, 'result': [], 'result_info': {'total_pages': 0}}).encode()
        self.assertEqual(adapter.read_pool(NAME, cf.request), set())
        path = '/client/v4/zones/' + 'a' * 32 + '/dns_records'
        self.assertEqual(client.request.call_args.args, ('GET', path + '?name.exact=relay.example.com&page=1&per_page=100'))
        self.assertEqual(client.request.call_args.kwargs['headers']['Authorization'], 'Bearer ' + token)
        response.status = 307
        response.getheader.return_value = 'https://external.example/redirect'
        response.read.return_value = token.encode()
        with self.assertRaisesRegex(adapter.AdapterError, '^provider_request_failed$'):
            cf.request('POST', record=row('8.8.4.4'))
        self.assertEqual(client.request.call_count, 2)
        self.assertEqual([call.args[0] for call in connection.call_args_list],
                         ['api.cloudflare.com', 'api.cloudflare.com'])


if __name__ == '__main__':
    unittest.main()
