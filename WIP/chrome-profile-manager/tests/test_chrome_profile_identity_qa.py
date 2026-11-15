"""Opt-in Chrome Stable probe; uses only a disposable profile and extension."""
import json
import os
import signal
import subprocess
import time

import pytest


@pytest.mark.skipif(os.environ.get('CHROME_IDENTITY_QA') != '1', reason='opt-in isolated Chrome QA')
def test_stable_unpacked_native_identity(tmp_path):
    websocket = pytest.importorskip('websocket')
    from chrome_profile_manager_bridge.provision import public_key, extension_origin
    chrome = '/usr/bin/google-chrome'
    version = subprocess.check_output([chrome, '--version'], text=True).strip()
    assert version.startswith('Google Chrome 152.'), version
    home = tmp_path / 'home'
    home.mkdir()
    profile = tmp_path / 'user-data'
    profile.mkdir()
    extension = tmp_path / 'extension'
    extension.mkdir()
    host_name = 'com.example.identity_qa'
    key = public_key()
    origin = extension_origin(key)
    (extension / 'manifest.json').write_text(json.dumps({
        'manifest_version': 3, 'name': 'Native identity QA', 'version': '1.0',
        'key': key,
        'permissions': ['nativeMessaging', 'transientBackground'],
        'natively_connectable': [host_name],
        'background': {'service_worker': 'worker.js'},
    }))
    (extension / 'worker.js').write_text(
        'if (chrome.runtime.onConnectNative) chrome.runtime.onConnectNative.addListener(() => {});\n'
        f'chrome.runtime.onInstalled.addListener(() => {{ let p = chrome.runtime.connectNative({json.dumps(host_name)}); p.onDisconnect.addListener(() => void chrome.runtime.lastError); }});\n')
    receipt = tmp_path / 'receipt.json'
    host = tmp_path / 'host.py'
    host.write_text('#!/usr/bin/python3\n'
        'import base64, json, sys\n'
        'args = [x for x in sys.argv[1:] if x.startswith("--reconnect-command=")]\n'
        'parts = json.loads(base64.b64decode(args[0].split("=", 1)[1])) if args else []\n'
        'result = {"reconnect": bool(args), "profile_directory": next((x.split("=", 1)[1] for x in parts if x.startswith("--profile-directory=")), None), "user_data_matches": any(x == ' + repr('--user-data-dir=' + str(profile)) + ' for x in parts)}\n'
        'open(' + repr(str(receipt)) + ', "w").write(json.dumps(result))\n')
    host.chmod(0o700)
    native_dir = profile / 'NativeMessagingHosts'
    native_dir.mkdir(parents=True)
    (native_dir / (host_name + '.json')).write_text(json.dumps({
        'name': host_name, 'description': 'isolated QA', 'path': str(host), 'type': 'stdio',
        'allowed_origins': [origin], 'supports_native_initiated_connections': True,
    }))
    env = dict(os.environ, HOME=str(home), XDG_CONFIG_HOME=str(home / '.config'),
               XDG_CACHE_HOME=str(home / '.cache'), XDG_DATA_HOME=str(home / '.local/share'))
    for name in ('DBUS_SESSION_BUS_ADDRESS', 'DISPLAY', 'WAYLAND_DISPLAY', 'CHROME_USER_DATA_DIR'):
        env.pop(name, None)
    proc = subprocess.Popen([chrome, '--headless=new', '--no-first-run', '--no-default-browser-check',
                             '--disable-background-networking', '--remote-debugging-port=0',
                             '--user-data-dir=' + str(profile), 'about:blank'],
                            env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                            start_new_session=True)
    ws = None
    try:
        port_file = profile / 'DevToolsActivePort'
        deadline = time.monotonic() + 20
        while not port_file.exists() and time.monotonic() < deadline:
            assert proc.poll() is None, f'Chrome exited: {proc.returncode}'
            time.sleep(.05)
        assert port_file.exists(), 'Chrome debugging endpoint did not start'
        port, path = port_file.read_text().splitlines()[:2]
        ws = websocket.create_connection(f'ws://127.0.0.1:{port}{path}', timeout=10, suppress_origin=True)
        ws.send(json.dumps({'id': 1, 'method': 'Extensions.loadUnpacked', 'params': {'path': str(extension)}}))
        while True:
            result = json.loads(ws.recv())
            if result.get('id') == 1:
                break
        if 'error' in result:
            print('Chrome:', version, 'loadUnpacked error:', result['error'])
            pytest.fail('Unpacked extension could not be loaded without the unsafe extension debugging flag')
        assert 'chrome-extension://' + result['result']['id'] + '/' == origin
        deadline = time.monotonic() + 15
        while not receipt.exists() and time.monotonic() < deadline:
            assert proc.poll() is None, 'Chrome exited before native host launched'
            time.sleep(.1)
        assert receipt.exists(), 'native host was not launched by extension'
        observed = json.loads(receipt.read_text())
        print('Chrome:', version, 'native host:', observed)
        assert observed == {'reconnect': False, 'profile_directory': None, 'user_data_matches': False}
    finally:
        if ws:
            ws.close()
        os.killpg(proc.pid, signal.SIGTERM)
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.wait(timeout=5)
