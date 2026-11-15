"""Bounded local logic tests; real chooser evidence is in the opt-in QA."""
import threading
import time

from chrome_profile_chooser import Binding, load_unpacked


def test_early_chrome_developer_mode_rejection(tmp_path):
    binding = Binding(1, 'process', '1', 'target', 'context', 'session')
    marker = threading.Event()
    class Chooser:
        input_sent = False
        def prepare(self, value): pass
        def mark_dispatched(self, value): marker.set()
        def select(self, value, path, deadline, check):
            while time.monotonic() < deadline:
                check()
                time.sleep(.01)
    def send(*args):
        assert marker.is_set()
        return {'result': {'type': 'string', 'value': 'Must be in developer mode to load unpacked extensions.'}}
    result = load_unpacked(binding, str(tmp_path), 2, check_binding=lambda _: True,
                           chooser=Chooser(), send=send)
    assert result.status == 'REJECTED'
    assert 'developer mode' in result.detail


def test_missing_binding_is_not_sent(tmp_path):
    binding = Binding(1, 'process', '1', 'target', 'context', 'session')
    sent = []
    result = load_unpacked(binding, str(tmp_path), 2, check_binding=lambda _: False,
                           chooser=object(), send=lambda *args: sent.append(args))
    assert result.status == 'NOT_SENT'
    assert not sent
