#!/usr/bin/env python3
import importlib
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CLIENT = ROOT / "client"
if str(CLIENT) not in sys.path:
    sys.path.insert(0, str(CLIENT))


def test_truncate_accepts_none_and_bytes():
    for module_name in ("rootd_win", "rootd_linux", "rootd_mac", "rootd_pure"):
        mod = importlib.import_module(module_name)
        assert mod._truncate(None) == ""
        assert "�" in mod._truncate(b"ok\xff") or mod._truncate(b"ok\xff").startswith("ok")


def test_linux_run_handles_non_utf8_stdout():
    import rootd_linux
    res = rootd_linux.run("python3 -c 'import sys; sys.stdout.buffer.write(bytes([0x9d, 0xff, 0x41]))'", timeout=10)
    assert res["returncode"] == 0
    assert "stdout" in res
    assert "A" in res["stdout"]


def test_windows_truncate_regression_none_result():
    import rootd_win
    # Regression for subprocess implementations that may return None stdout/stderr.
    assert rootd_win._truncate(None) == ""
    assert rootd_win._truncate(b"abc") == "abc"

def test_rootd_pure_declares_long_poll_queue_vars():
    import rootd_pure

    assert isinstance(rootd_pure.QUEUE_TRANSPORT, str)
    assert isinstance(rootd_pure.QUEUE_LONG_POLL_TIMEOUT_S, int)
    assert isinstance(rootd_pure.QUEUE_HTTP_TIMEOUT_S, int)
    assert rootd_pure.QUEUE_IS_LONG_POLL == (
        rootd_pure.QUEUE_TRANSPORT in {"long_poll", "long-poll", "longpoll"}
    )

