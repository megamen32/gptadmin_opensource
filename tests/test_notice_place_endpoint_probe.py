"""Self-contained check of the Notice Place endpoint probe.

The probe must speak the Streamable HTTP session semantics of a real client:
`initialize`, the `initialized` notification and `tools/list`, carrying the
session id the server handed out. The test uses a local disposable server, so
it needs no network and no credential.
"""

import http.server
import importlib.util
import json
import pathlib
import threading
import unittest

spec = importlib.util.spec_from_file_location(
    'notice_place_endpoint_probe',
    pathlib.Path(__file__).parents[1] / 'scripts' / 'notice_place_endpoint_probe.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class _Server(http.server.BaseHTTPRequestHandler):
    """Minimal Streamable HTTP MCP server with a mandatory session id."""

    session = 'session-under-test'
    require_session = True

    def do_POST(self):
        if not self.path.endswith('/mcp'):
            self._reply({'error': 'not found'}, status=404)
            return
        length = int(self.headers.get('Content-Length') or 0)
        request = json.loads(self.rfile.read(length) or b'{}')
        method = request.get('method')
        if method == 'initialize':
            body = {'jsonrpc': '2.0', 'id': request.get('id'),
                    'result': {'protocolVersion': '2024-11-05',
                               'capabilities': {'tools': {}},
                               'serverInfo': {'name': 'notice-place-test', 'version': '1.3.0'}}}
            self._reply(body, session=self.session)
            return
        if method == 'notifications/initialized':
            self.send_response(202)
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        if method == 'tools/list':
            if self.require_session and self.headers.get('mcp-session-id') != self.session:
                self._reply({'error': {'code': -32000, 'message': 'missing session id'}}, status=400)
                return
            body = {'jsonrpc': '2.0', 'id': request.get('id'),
                    'result': {'tools': [{'name': 'noticeplace_instructions', 'description': '',
                                          'inputSchema': {'type': 'object'}},
                                         {'name': 'noticeplace_ask_human', 'description': '',
                                          'inputSchema': {'type': 'object'}}]}}
            self._reply(body)
            return
        self._reply({'error': {'code': -32601, 'message': 'unknown method'}})

    def _reply(self, body, status=200, session=None):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        if session:
            self.send_header('mcp-session-id', session)
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *args):
        return


class EndpointProbeTests(unittest.TestCase):
    def setUp(self):
        _Server.session = 'session-under-test'
        _Server.require_session = True
        self.server = http.server.HTTPServer(('127.0.0.1', 0), _Server)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.url = f'http://127.0.0.1:{self.server.server_address[1]}/mcp'

    def test_probe_reads_the_tool_list_with_session_semantics(self):
        result = probe.probe_registration(self.url, {})
        self.assertEqual(2, result['tool_count'])
        self.assertEqual(['noticeplace_ask_human', 'noticeplace_instructions'], result['tools'])
        self.assertEqual('notice-place-test', result['server'])
        self.assertTrue(result['session_id'])

    def test_probe_fails_when_the_session_id_is_not_carried(self):
        # the disposable server rejects tools/list without the session id it
        # handed out, so a successful probe proves the id was carried
        self.assertTrue(_Server.require_session)
        result = probe.probe_registration(self.url, {})
        self.assertEqual(2, result['tool_count'])
        self.assertTrue(result['session_id'])

    def test_probe_reports_a_missing_endpoint(self):
        with self.assertRaises(Exception):
            probe.probe_registration(f'http://127.0.0.1:{self.server.server_address[1]}/nope', {})

    def test_stdio_registration_is_reported_as_not_probed(self):
        result = probe.probe_entry({'command': ['/opt/noticeplace/bin/ask-human-mcp']})
        self.assertFalse(result['probed'])
        self.assertEqual('stdio', result['transport'])
        self.assertIn('HTTP endpoint', result['error'])

    def test_parse_payload_reads_json_and_sse_bodies(self):
        self.assertEqual({'a': 1}, probe.parse_payload('{"a": 1}'))
        self.assertEqual({'a': 2}, probe.parse_payload('event: message\ndata: {"a": 2}\n\n'))

    def test_collect_finds_only_notice_place_entries(self):
        self.assertTrue(probe.is_notice_place({'url': 'https://notify.bezrabotnyi.com/mcp'}))
        self.assertTrue(probe.is_notice_place({'command': ['/opt/noticeplace/bin/ask-human-mcp']}))
        self.assertFalse(probe.is_notice_place({'url': 'https://msg.bezrabotnyi.com/mcp'}))
        self.assertFalse(probe.is_notice_place({'url': 'https://pswd.bezrabotnyi.com/mcp'}))


if __name__ == '__main__':
    unittest.main()
