# LHC:COVERS chrome_profile_manager_bridge/provision.py chrome_profile_manager_bridge/host.py chrome_profile_manager_bridge/extension/background.js
"""Opt-in real Chrome test; --load-extension is repeated, not a persistent install."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import time

import pytest

ROOT = Path(__file__).resolve().parents[1]
CHROME = Path("/home/admin/.cache/puppeteer/chrome/linux-148.0.7778.97/chrome-linux64/chrome")


@pytest.mark.skipif(os.environ.get("CHROME_STARTUP_QA") != "1", reason="opt-in isolated real Chrome QA")
def test_auto_bridge_after_chrome_restart_with_explicit_extension_load(tmp_path):
    if not CHROME.is_file():
        pytest.skip("Chrome for Testing binary unavailable")
    sys.path.insert(0, str(ROOT))
    from chrome_profile_manager_bridge.host import receive, send, socket_path

    home = tmp_path / "home"
    home.mkdir(mode=0o700)
    profile = tmp_path / "chrome"
    profile.mkdir(mode=0o700)
    env = dict(os.environ, HOME=str(home), XDG_CONFIG_HOME=str(home / ".config"),
               XDG_CACHE_HOME=str(home / ".cache"), XDG_DATA_HOME=str(home / ".local/share"))
    for name in ("DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY", "CHROME_USER_DATA_DIR"):
        env.pop(name, None)
    result = subprocess.run([sys.executable, str(ROOT / "chrome_profile_manager_bridge/provision.py"),
                             "--profile-id", "startup-qa", "--user-data-dir", str(profile)],
                            env=env, text=True, capture_output=True, check=True, timeout=30)
    extension = result.stdout.strip()
    # Instrument only the provisioned QA copy. The native host logs a strict
    # event-name allowlist, never the capability or the native message.
    background = Path(extension) / "background.js"
    source = background.read_text()
    replacements = {
        "function connect(secret, retry = true) {": "function connect(secret, retry = true, reason) {",
        "{type:\"auto\"}": "{type:\"auto\", reason}",
        "if (AUTO_CONNECT) connect(); });\nchrome.runtime.onInstalled":
            "if (AUTO_CONNECT) connect(undefined, true, \"onStartup\"); });\nchrome.runtime.onInstalled",
        "if (AUTO_CONNECT) connect(); });\nchrome.runtime.onMessage":
            "if (AUTO_CONNECT) connect(undefined, true, \"onInstalled\"); });\nchrome.runtime.onMessage",
    }
    for before, after in replacements.items():
        assert source.count(before) == 1, before
        source = source.replace(before, after)
    background.write_text(source)
    event_log = home / "events.log"
    original_host = (ROOT / "chrome_profile_manager_bridge/host.py").read_text()
    marker = "    hello = receive(sys.stdin.buffer.raw)\n"
    assert original_host.count(marker) == 1
    instrument = ("    reason = hello.get(\"reason\") if isinstance(hello, dict) else None\n"
                  "    if reason in (\"onInstalled\", \"onStartup\"):\n"
                  f"        with open({str(event_log)!r}, \"a\", encoding=\"utf-8\") as log:\n"
                  "            log.write(reason + \"\\n\")\n")
    qa_host = home / "qa-host.py"
    qa_host.write_text(original_host.replace(marker, marker + instrument))
    qa_host.chmod(0o700)
    native_manifest = profile / "NativeMessagingHosts/com.gptadmin.extension_bridge.json"
    native = json.loads(native_manifest.read_text())
    native["path"] = str(qa_host)
    native_manifest.write_text(json.dumps(native))
    registry = home / ".config/gptadmin-extension-bridge"
    bindings = json.loads((registry / "bindings.json").read_text())
    origin = bindings["startup-qa"]
    secret = json.loads((registry / "profiles.json").read_text())[origin]
    bridge_id = origin.split("/")[2]
    endpoint = Path(socket_path(secret))
    lock = Path(str(endpoint) + ".lock")

    def wait_for_list(proc):
        deadline = time.monotonic() + 20
        last = None
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                pytest.fail(f"QA Chrome exited unexpectedly: {proc.returncode}")
            try:
                with socket.socket(socket.AF_UNIX) as conn:
                    conn.settimeout(3)
                    conn.connect(str(endpoint))
                    with conn.makefile("rwb") as stream:
                        send(stream, {"secret": secret, "method": "list"})
                        response = receive(stream)
                assert "error" not in response, response
                assert any(item["id"] == bridge_id and item["enabled"] for item in response["result"])
                return
            except (OSError, EOFError) as exc:
                last = exc
                time.sleep(0.1)
        pytest.fail(f"automatic native bridge unavailable: {last}")

    def run_once():
        previous = event_log.read_text().splitlines() if event_log.exists() else []
        proc = subprocess.Popen([str(CHROME), "--headless=new", "--no-sandbox", "--disable-gpu",
                                 "--no-first-run", "--no-default-browser-check",
                                 "--disable-background-networking", "--user-data-dir=" + str(profile),
                                 "--load-extension=" + extension, "about:blank"],
                                env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                start_new_session=True)
        try:
            wait_for_list(proc)
            # Give a second startup callback time to be delivered if both fire.
            time.sleep(2)
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait(timeout=5)
            deadline = time.monotonic() + 5
            while endpoint.exists() and time.monotonic() < deadline:
                time.sleep(0.05)
        assert not endpoint.exists(), "native host endpoint remained after Chrome stopped"
        events = event_log.read_text().splitlines() if event_log.exists() else []
        return events[len(previous):]

    try:
        first = run_once()
        second = run_once()
        print(f"QA connect events: first={first}, second={second}", flush=True)
        if "onStartup" not in second:
            pytest.xfail(f"repeated --load-extension fired no onStartup: first={first}, second={second}; persistent installation remains unverified")
    finally:
        if lock.exists():
            lock.unlink()
