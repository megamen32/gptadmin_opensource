"""Real HTTP regression for delayed drain replies; not deployment acceptance."""
import threading
import time
import unittest
from scripts.test_gptadmin_node_manage import QuietHandler, start_server, respond_json, mgr

class SlowDrainHTTP(unittest.TestCase):
    def test_delayed_drain_cancel_is_confirmed(self):
        finished = threading.Event()
        class Handler(QuietHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers.get('Content-Length', 0)))
                time.sleep(6)
                try:
                    respond_json(self, 200, {'draining': False})
                except BrokenPipeError:
                    pass
                finally:
                    finished.set()
        server = start_server(Handler)
        try:
            self.assertTrue(mgr.drain_cancel('http://127.0.0.1:'+str(server.server_port), 'local-test-token'))
        finally:
            finished.wait(8)
            server.shutdown()
            server.server_close()

    def test_status_timeout_retries_within_overall_drain_budget(self):
        from unittest.mock import patch
        class Handler(QuietHandler):
            calls = 0
            def do_POST(self):
                self.rfile.read(int(self.headers.get('Content-Length', 0)))
                Handler.calls += 1
                if Handler.calls == 1:
                    time.sleep(0.3)
                try:
                    respond_json(self, 200, {'draining': True, 'ready_to_stop': True})
                except BrokenPipeError:
                    pass
        server = start_server(Handler)
        try:
            # Tune the real HTTP deadline, not the network boundary.
            with patch.object(mgr, 'DRAIN_HTTP_TIMEOUT_SECS', 0.1):
                result = mgr.drain_wait_ready('http://127.0.0.1:'+str(server.server_port), 'local-test-token', 2, poll_interval=0.01)
            self.assertTrue(result['ready_to_stop'])
            self.assertGreaterEqual(Handler.calls, 2)
        finally:
            server.shutdown()
            server.server_close()
