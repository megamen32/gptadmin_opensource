"""CLI rejection regressions; these tests do not claim Herder integration."""
import json
import contextlib
import importlib.util
import io
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parents[1]


class AdapterValidationTests(unittest.TestCase):
    def run_adapter(self, name, args, response=None):
        spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / (name + '.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        stderr = io.StringIO()
        stdout = io.StringIO()
        network = ({'side_effect': AssertionError('invalid input reached network')} if response is None
                   else {'return_value': io.BytesIO(response.encode())})
        with patch.object(sys, 'argv', [name, *args]), contextlib.redirect_stderr(stderr), \
                contextlib.redirect_stdout(stdout), patch.object(module.urllib.request, 'urlopen', **network):
            code = module.main()
        return SimpleNamespace(returncode=code, stderr=stderr.getvalue())

    def deliver(self, event):
        return self.run_adapter('agent_deliver', [json.dumps(event)])

    def event(self):
        return {'schema': 'gptadmin.agent-deliver.v1', 'event_id': 'test',
                'target': {'harness': 'codex', 'name': 'test',
                           'cwd': '/home/roomhacker/gptadmin'}, 'payload': {}}

    def assert_rejected(self, result):
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertFalse(json.loads(result.stderr)['ok'])
        self.assertNotIn('Traceback', result.stderr)

    def test_nonobject_event_and_nested_fields(self):
        for value in ([], None, 1, 'event'):
            with self.subTest(value=value):
                self.assert_rejected(self.deliver(value))
        for field in ('target', 'delivery'):
            for value in ([], ['invalid'], None, 1):
                event = self.event()
                event[field] = value
                with self.subTest(field=field, value=value):
                    self.assert_rejected(self.deliver(event))

    def test_deliver_rejects_path_escape(self):
        event = self.event()
        for cwd in ('/home/roomhacker/../../etc', '', '.', '/home/roomhacker'):
            event['target']['cwd'] = cwd
            self.assert_rejected(self.deliver(event))

    def test_deliver_rejects_invalid_policy_and_oversized_message(self):
        event = self.event()
        for value in ('unsupported', [], {}):
            event['delivery'] = {'mode': value}
            self.assert_rejected(self.deliver(event))
        event.pop('delivery')
        event['payload'] = 'x' * 32001
        self.assert_rejected(self.deliver(event))

    def test_wake_rejects_path_escape(self):
        args = ['--schema', 'gptadmin.agent-wake.v1', '--harness', 'codex',
                '--name', 'test', '--cwd', '/home/roomhacker/../../etc',
                '--event-id', 'test', '--source', '{}', '--subject', 'test',
                '--payload', '{}']
        self.assert_rejected(self.run_adapter('agent_wake', args))

    def test_mcp_errors_cannot_be_success(self):
        for response in ({'error': {'code': -32603, 'message': 'failure'}},
                         {'result': {'isError': True, 'structuredContent': {'ok': True}}}):
            result = self.run_adapter('agent_deliver', [json.dumps(self.event())], json.dumps(response))
            self.assertEqual(result.returncode, 4)
            self.assertFalse(json.loads(result.stderr)['ok'])

    def test_malformed_response_is_structured_failure(self):
        result = self.run_adapter('agent_deliver', [json.dumps(self.event())], 'unavailable')
        self.assertEqual(result.returncode, 3)
        self.assertFalse(json.loads(result.stderr)['ok'])


if __name__ == '__main__':
    unittest.main()
