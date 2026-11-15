"""Isolated Chrome lifecycle probe; never reads page or profile contents."""
import http.server
import json
import os
import shutil
import subprocess
import threading
import time

import pytest

websocket = pytest.importorskip("websocket")


@pytest.mark.skipif(os.environ.get('CHROME_PERSISTENCE_QA') != '1', reason='opt-in isolated Chrome QA')
def test_cdp_extension_restart(tmp_path):
    events = []

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            events.append(self.path)
            self.send_response(204)
            self.end_headers()

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    extension = tmp_path / 'extension'
    extension.mkdir()
    (extension / 'manifest.json').write_text(json.dumps({
        'manifest_version': 3, 'name': 'Isolated lifecycle QA', 'version': '1.0',
        'background': {'service_worker': 'worker.js'},
        'host_permissions': [f'http://127.0.0.1:{server.server_port}/*'],
    }))
    (extension / 'worker.js').write_text(
        f"const report = s => fetch('http://127.0.0.1:{server.server_port}/' + s, {{method: 'POST'}});\n"
        "chrome.runtime.onInstalled.addListener(() => report('installed'));\n"
        "chrome.runtime.onStartup.addListener(() => report('startup'));\n")
    profile = tmp_path / 'profile'
    profile.mkdir()
    process = None
    ws = None

    def start():
        nonlocal process, ws
        active = profile / 'DevToolsActivePort'
        active.unlink(missing_ok=True)
        env = dict(os.environ, HOME=str(tmp_path), XDG_CONFIG_HOME=str(tmp_path / 'config'),
                   XDG_CACHE_HOME=str(tmp_path / 'cache'))
        process = subprocess.Popen([
            '/usr/bin/google-chrome', '--headless=new', '--no-first-run',
            '--no-default-browser-check', '--disable-background-networking',
            '--remote-debugging-port=0', '--enable-unsafe-extension-debugging',
            '--user-data-dir=' + str(profile), 'about:blank'],
            env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        deadline = time.monotonic() + 15
        while not active.exists() and time.monotonic() < deadline:
            assert process.poll() is None, f'Chrome exited {process.returncode}'
            time.sleep(.05)
        lines = active.read_text().splitlines()
        ws = websocket.create_connection(f'ws://127.0.0.1:{lines[0]}{lines[1]}', timeout=10, suppress_origin=True)

    def call(method, params=None):
        ws.send(json.dumps({'id': 1, 'method': method, 'params': params or {}}))
        while True:
            message = json.loads(ws.recv())
            if message.get('id') == 1:
                assert 'error' not in message, message
                return message['result']

    def stop():
        nonlocal process, ws
        if ws:
            try:
                ws.send(json.dumps({'id': 99, 'method': 'Browser.close'}))
                ws.close()
            finally:
                ws = None
        if process:
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.terminate()
                process.wait(timeout=5)
            process = None

    try:
        start()
        result = call('Extensions.loadUnpacked', {'path': str(extension)})
        extension_id = result['id']
        assert any(x['id'] == extension_id for x in call('Extensions.getExtensions')['extensions'])
        time.sleep(2)
        assert '/installed' in events, events
        print('before restart:', extension_id, 'events:', events)
        stop()
        start()
        time.sleep(3)
        retained = any(x['id'] == extension_id for x in call('Extensions.getExtensions')['extensions'])
        print('after restart: retained=', retained, 'events=', events)
        assert not retained
        assert '/startup' not in events
    finally:
        stop()
        server.shutdown()
        server.server_close()
        shutil.rmtree(tmp_path, ignore_errors=True)
