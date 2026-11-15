"""Real isolated Chrome chooser QA; opt in with CHROME_CHOOSER_REAL_QA=1.

The test deliberately fails closed until chooser accessibility authorization and
two independently bound profile bridges have been implemented.
"""
import json
import os
import secrets
import shutil
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

import pytest


@pytest.mark.skipif(os.getenv('CHROME_CHOOSER_REAL_QA') != '1', reason='isolated real Chrome QA only')
def test_real_chooser_profile_install(tmp_path):
    if os.getenv('CHOOSER_QA_PRIVATE_BUS') != '1':
        env = {k: v for k, v in os.environ.items() if k not in (
            'DBUS_SESSION_BUS_ADDRESS', 'DISPLAY', 'WAYLAND_DISPLAY', 'CHROME_USER_DATA_DIR')}
        env['CHOOSER_QA_PRIVATE_BUS'] = '1'
        result = subprocess.run(['dbus-run-session', '--', sys.executable, '-m', 'pytest', '-s', '-q',
                                 str(Path(__file__).resolve())], env=env, timeout=90)
        assert result.returncode == 0, 'isolated real chooser probe failed; see output above'
        return

    import websocket
    root = Path(__file__).resolve().parents[1]
    sys.path.insert(0, str(root))
    from chrome_profile_chooser import Binding, load_unpacked
    from chrome_profile_chooser_linux import LinuxChooser, process_identity
    from chrome_profile_manager_bridge.provision import provision, public_key, extension_origin
    from chrome_profile_manager_mcp import bridge_call

    runtime = tmp_path / 'runtime'
    runtime.mkdir(mode=0o700)
    os.environ.update(HOME=str(tmp_path), XDG_CONFIG_HOME=str(tmp_path / 'config'),
                      XDG_CACHE_HOME=str(tmp_path / 'cache'), XDG_DATA_HOME=str(tmp_path / 'data'),
                      XDG_RUNTIME_DIR=str(runtime), XDG_CURRENT_DESKTOP='GNOME', GTK_USE_PORTAL='1')
    processes = []
    try:
        xvfb = subprocess.Popen(['Xvfb', '-displayfd', '1', '-screen', '0', '1920x1200x24', '-nolisten', 'tcp'],
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, start_new_session=True)
        processes.append(xvfb)
        import select
        assert select.select([xvfb.stdout], [], [], 5)[0], 'Xvfb timed out'
        display = xvfb.stdout.readline().strip()
        assert display.isdigit(), 'Xvfb did not start'
        os.environ['DISPLAY'] = ':' + display
        subprocess.run(['dbus-update-activation-environment', 'DISPLAY', 'HOME', 'XDG_CONFIG_HOME',
                        'XDG_DATA_HOME', 'XDG_RUNTIME_DIR', 'XDG_CURRENT_DESKTOP', 'GTK_USE_PORTAL'],
                       check=True, timeout=5)
        processes.append(subprocess.Popen(['fluxbox'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                          start_new_session=True))
        profile = tmp_path / 'chrome'
        profile.mkdir()
        bridge = provision('default', profile)
        extension = tmp_path / 'managed' / 'prepared' / 'extension'
        extension.mkdir(parents=True)
        key = public_key()
        expected_id = extension_origin(key)[19:-1]
        (extension / 'manifest.json').write_text(json.dumps({'manifest_version': 3, 'name': 'Chooser QA',
                                                            'version': '1.2.3', 'key': key}))
        chrome = subprocess.Popen(['/usr/bin/google-chrome', '--no-first-run', '--no-default-browser-check',
                                   '--disable-background-networking', '--password-store=basic',
                                   '--remote-debugging-port=0', '--user-data-dir=' + str(profile), 'about:blank'],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        processes.append(chrome)
        portfile = profile / 'DevToolsActivePort'
        deadline = time.monotonic() + 20
        while not portfile.exists() and time.monotonic() < deadline:
            assert chrome.poll() is None, 'QA Chrome exited'
            time.sleep(.05)
        assert portfile.exists(), 'QA Chrome CDP startup timeout'
        port, path = portfile.read_text().splitlines()[:2]
        endpoint = f'ws://127.0.0.1:{port}{path}'

        session_ws = websocket.create_connection(endpoint, timeout=15, suppress_origin=True)

        def call(method, params=None, session=None, timeout=8):
            persistent = bool(session) or method == 'Target.attachToTarget'
            ws = session_ws if persistent else websocket.create_connection(endpoint, timeout=timeout, suppress_origin=True)
            try:
                ws.settimeout(timeout)
                msg = {'id': 1, 'method': method, 'params': params or {}}
                if session:
                    msg['sessionId'] = session
                ws.send(json.dumps(msg))
                while True:
                    response = json.loads(ws.recv())
                    if response.get('id') == 1:
                        if 'error' in response:
                            raise RuntimeError(str(response['error']))
                        return response['result']
            finally:
                if not persistent:
                    ws.close()

        bridge_id = call('Extensions.loadUnpacked', {'path': str(bridge)})['id']
        deadline = time.monotonic() + 12
        while True:
            targets = call('Target.getTargets')['targetInfos']
            matches = [x for x in targets if x.get('type') == 'service_worker' and
                       x.get('url', '').startswith('chrome-extension://' + bridge_id + '/')]
            if len(matches) == 1:
                break
            assert time.monotonic() < deadline, 'bridge context not unique or unavailable'
            time.sleep(.1)
        context = matches[0]['browserContextId']
        deadline = time.monotonic() + 12
        while True:
            try:
                before = bridge_call('default', 'list')
                break
            except (OSError, RuntimeError):
                assert time.monotonic() < deadline, 'real bridge unavailable'
                time.sleep(.1)
        assert not any(x['id'] == expected_id for x in before)
        target = call('Target.createTarget', {'url': 'chrome://extensions/', 'browserContextId': context,
                                              'newWindow': True})['targetId']
        second_bridge = provision('Profile 2', profile)
        opened = subprocess.Popen(['/usr/bin/google-chrome', '--user-data-dir=' + str(profile),
                                   '--profile-directory=Profile 2', 'about:blank'],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        assert opened.wait(timeout=10) == 0
        deadline = time.monotonic() + 10
        while True:
            pages = call('Target.getTargets')['targetInfos']
            other = [x for x in pages if x.get('type') == 'page' and x.get('browserContextId')
                     and x['browserContextId'] != context]
            if other: break
            assert time.monotonic() < deadline, 'second profile not created'
            time.sleep(.1)
        call('Target.activateTarget', {'targetId': other[0]['targetId']})
        second_id = call('Extensions.loadUnpacked', {'path': str(second_bridge)})['id']
        deadline = time.monotonic() + 12
        while True:
            targets = call('Target.getTargets')['targetInfos']
            second = [x for x in targets if x.get('type') == 'service_worker' and
                      x.get('url', '').startswith('chrome-extension://' + second_id + '/')]
            try:
                second_before = bridge_call('Profile 2', 'list')
            except (OSError, RuntimeError):
                second_before = None
            if len(second) == 1 and second_before is not None: break
            assert time.monotonic() < deadline, 'second profile bridge not ready'
            time.sleep(.1)
        assert second[0]['browserContextId'] == other[0]['browserContextId'] != context
        assert not any(x['id'] == expected_id for x in second_before)
        from chrome_profile_manager_mcp import content_hash
        manager_state = tmp_path / 'mcp-state'
        artifact = manager_state / 'artifacts' / 'qa-stage'
        artifact.parent.mkdir(parents=True)
        shutil.copytree(extension, artifact)
        env = os.environ.copy()
        env.update(CHROME_PROFILE_ROOTS=str(profile), CHROME_PROFILE_MANAGER_STATE=str(manager_state),
                   SHELLMCP_MCP_CONFIG=str(tmp_path / 'mcp.json'), SHELLMCP_SPOOL_DIR=str(tmp_path / 'spool'),
                   SHELLMCP_IDENTITY_DIR=str(tmp_path / 'identity'), SHELLMCP_DEFAULT_HOME=str(tmp_path),
                   SHELLMCP_DEFAULT_CWD=str(tmp_path), SHELLMCP_HEARTBEAT='0', SHELLMCP_QUEUE='0', HUB_URL='')
        shell = subprocess.Popen(['/opt/gptadmin/bin/shellmcp', '--mcp-stdio'], stdin=subprocess.PIPE,
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1,
                                 env=env, start_new_session=True)
        processes.append(shell)
        sequence = 0
        def rpc(method, params):
            nonlocal sequence
            sequence += 1
            shell.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': sequence, 'method': method, 'params': params}) + '\n')
            shell.stdin.flush()
            assert select.select([shell.stdout], [], [], 30)[0], 'shellmcp timeout'
            reply = json.loads(shell.stdout.readline())
            assert 'error' not in reply, reply.get('error')
            return reply['result']
        rpc('initialize', {'protocolVersion': '2025-03-26', 'capabilities': {},
                           'clientInfo': {'name': 'isolated-chooser-qa', 'version': '1'}})
        config = {'ref': 'qa-helper', 'transport': 'stdio', 'command': sys.executable,
                  'args': [str(root / 'chrome_profile_manager_mcp.py')], 'enabled': True}
        assert not rpc('tools/call', {'name': 'mcp_manage', 'arguments': {'action': 'upsert', 'config': config}}).get('isError')
        def child_call(name, arguments):
            result = rpc('tools/call', {'name': 'mcp_call', 'arguments': {'ref': 'qa-helper', 'name': name,
                                                                          'arguments': arguments}})
            assert not result.get('isError'), result
            child = result['structuredContent']['result']
            assert not child.get('isError'), child
            return child['structuredContent']
        staged = child_call('profile_helper_stage', {'profile_id': 'default', 'source_path': 'qa-stage',
                            'extension_id': expected_id, 'version': '1.2.3', 'digest': content_hash(artifact)})
        prepared = child_call('profile_helper_prepare', {'profile_id': 'default', 'stage_id': staged['stage_id']})
        assert prepared['installed'] is False and prepared['extension_id'] == expected_id
        extension = Path(prepared['path'])
        assert extension.is_relative_to(tmp_path) and content_hash(extension) == content_hash(artifact)
        print('shellmcp prepared profile-owned artifact:', prepared['extension_id'], flush=True)
        session = call('Target.attachToTarget', {'targetId': target, 'flatten': True})['sessionId']
        nonce = 'chooserqa' + secrets.token_hex(16)
        deadline = time.monotonic() + 8
        while True:
            call('Runtime.evaluate', {'expression': 'document.title = ' + json.dumps(nonce)}, session)
            search = subprocess.run(['xdotool', 'search', '--all', '--pid', str(chrome.pid), '--name', nonce],
                                    capture_output=True, text=True, timeout=3)
            windows = search.stdout.split()
            assert len(windows) <= 1, 'ambiguous Chrome window'
            if windows:
                break
            assert time.monotonic() < deadline, 'owned Chrome window unavailable'
            time.sleep(.1)
        binding = Binding(chrome.pid, process_identity(chrome.pid), windows[0], target, context, session)
        second_target = call('Target.createTarget', {'url': 'chrome://extensions/',
                             'browserContextId': second[0]['browserContextId'], 'newWindow': True})['targetId']
        second_session = call('Target.attachToTarget', {'targetId': second_target, 'flatten': True})['sessionId']
        second_nonce = 'chooserqa' + secrets.token_hex(16)
        deadline = time.monotonic() + 8
        while True:
            call('Runtime.evaluate', {'expression': 'document.title = ' + json.dumps(second_nonce)}, second_session)
            search = subprocess.run(['xdotool', 'search', '--all', '--pid', str(chrome.pid), '--name', second_nonce],
                                    capture_output=True, text=True, timeout=3)
            second_windows = search.stdout.split()
            assert len(second_windows) <= 1, 'second Chrome window is ambiguous'
            if second_windows: break
            assert time.monotonic() < deadline, 'second Chrome window unavailable'
            time.sleep(.1)
        assert second_windows[0] != binding.chrome_window
        print('two independently bound profile windows are distinct', flush=True)
        call('Target.activateTarget', {'targetId': target})

        def check(b):
            try:
                info = call('Target.getTargetInfo', {'targetId': b.target_id})['targetInfo']
                return (chrome.poll() is None and process_identity(chrome.pid) == b.process_identity and
                        info.get('browserContextId') == b.context_id and
                        info.get('url', '').startswith('chrome://extensions/') and
                        call('Browser.getWindowForTarget', {'targetId': b.target_id}).get('windowId') is not None)
            except Exception:
                return False

        def authorize(b, xid, action):
            import gi
            gi.require_version('Atspi', '2.0')
            from gi.repository import Atspi
            desktop = Atspi.get_desktop(0)
            matches = []
            for i in range(desktop.get_child_count()):
                app = desktop.get_child_at_index(i)
                try:
                    if app.get_name() != 'xdg-desktop-portal-gtk':
                        continue
                    for j in range(app.get_child_count()):
                        dialog = app.get_child_at_index(j)
                        if dialog.get_role_name() == 'file chooser' and dialog.get_name() == 'Выберите каталог расширения':
                            matches.append(dialog.get_process_id())
                except Exception as exc:
                    print('AT-SPI error:', repr(exc), flush=True)
                    return False
            if len(matches) != 1:
                return False
            found = subprocess.run(['xdotool', 'search', '--onlyvisible', '--pid', str(matches[0]),
                                    '--name', '^Выберите каталог расширения$'],
                                   capture_output=True, text=True, timeout=2)
            ok = found.returncode == 0 and found.stdout.split() == [str(xid)]
            print('chooser accessibility and X11 PID match:', ok, 'action:', action, flush=True)
            return ok

        def perform_action(b, xid, action):
            if action == 'location':
                subprocess.run(['xdotool', 'key', 'alt+Home'], check=True, timeout=2)
                return True
            if action == 'path':
                subprocess.run(['xdotool', 'type', '--clearmodifiers', '--', extension.relative_to(tmp_path).parts[0]], check=True, timeout=2)
                return True
            if action == 'navigate':
                commands = []
                for part in extension.relative_to(tmp_path).parts[1:]:
                    commands += [['key', 'Return'], ['type', '--clearmodifiers', '--', part]]
                for command in commands:
                    if not check(b) or not authorize(b, xid, action):
                        raise RuntimeError('chooser binding changed during navigation')
                    focus = subprocess.check_output(['xdotool', 'getwindowfocus'], text=True, timeout=2).strip()
                    if focus != str(xid):
                        raise RuntimeError('chooser focus changed during navigation')
                    subprocess.run(['xdotool', *command], check=True, timeout=2)
                    time.sleep(.35)
                return True
            if action != 'select':
                return False
            from gi.repository import Atspi
            desktop = Atspi.get_desktop(0)
            buttons = []
            def walk(node, depth=0):
                if depth > 8: return
                if node.get_role_name() == 'push button' and node.get_name() == 'Выбрать':
                    state = node.get_state_set()
                    print('select state showing/enabled/sensitive:', state.contains(Atspi.StateType.SHOWING), state.contains(Atspi.StateType.ENABLED), state.contains(Atspi.StateType.SENSITIVE), flush=True)
                    if state.contains(Atspi.StateType.SHOWING) and state.contains(Atspi.StateType.ENABLED):
                        buttons.append(node)
                state = node.get_state_set()
                if state.contains(Atspi.StateType.SELECTED) and state.contains(Atspi.StateType.SHOWING):
                    print('GTK selected:', node.get_role_name(), repr(node.get_name()), flush=True)
                for i in range(node.get_child_count()):
                    walk(node.get_child_at_index(i), depth + 1)
            for i in range(desktop.get_child_count()):
                app = desktop.get_child_at_index(i)
                if app.get_name() == 'xdg-desktop-portal-gtk':
                    for j in range(app.get_child_count()):
                        dialog = app.get_child_at_index(j)
                        if dialog.get_role_name() == 'file chooser' and dialog.get_name() == 'Выберите каталог расширения':
                            walk(dialog)
            if len(buttons) != 1 or not buttons[0].get_action_iface().do_action(0):
                raise RuntimeError('unique enabled chooser select button unavailable')
            print('activated GTK chooser select button', flush=True)
            return True

        driver = LinuxChooser(authorize=authorize, display=os.environ['DISPLAY'], perform_action=perform_action)
        def send(method, params, session_id, timeout):
            try:
                response = call(method, params, session_id, timeout)
                print('WebUI CDP response:', response, flush=True)
                return response
            except Exception as exc:
                print('WebUI CDP failure:', repr(exc), flush=True)
                raise

        refused = load_unpacked(binding, str(extension), 5, check_binding=check, chooser=driver, send=send)
        print('developer mode off:', refused.status, refused.detail, flush=True)
        assert refused.status == 'REJECTED' and 'developer mode' in refused.detail.lower()
        assert not any(x['id'] == expected_id for x in bridge_call('default', 'list'))
        # This opt-in toggle is confined to the disposable QA profile.
        setup = call('Runtime.evaluate', {'expression': "new Promise(resolve => chrome.developerPrivate.updateProfileConfiguration({inDeveloperMode:true}, () => resolve(chrome.runtime.lastError?.message || 'ok')))", 'awaitPromise': True, 'returnByValue': True}, session)
        print('developer mode setup:', setup, flush=True)
        assert setup.get('result', {}).get('value') == 'ok', setup
        result = load_unpacked(binding, str(extension), 30, check_binding=check, chooser=driver, send=send)
        print('real chooser result:', result.status, result.detail, flush=True)
        after = bridge_call('default', 'list')
        print('real bridge target present:', any(x['id'] == expected_id for x in after), flush=True)
        assert result.status == 'ACKNOWLEDGED', 'real chooser did not complete: ' + result.detail
        entries = [x for x in after if x['id'] == expected_id]
        assert len(entries) == 1 and entries[0]['version'] == '1.2.3' and entries[0]['enabled']
        metadata = call('Runtime.evaluate', {'expression':
            'new Promise(resolve => chrome.developerPrivate.getExtensionInfo(' + json.dumps(expected_id) +
            ', x => resolve(x ? {id:x.id,version:x.version,location:x.location,path:x.path} : null)))',
            'awaitPromise': True, 'returnByValue': True}, session)['result']['value']
        assert metadata['id'] == expected_id and metadata['version'] == '1.2.3'
        assert metadata['location'] == 'UNPACKED' and Path(metadata['path']).resolve() == extension.resolve()
        print('Chrome unpacked metadata confirms the exact selected directory', flush=True)
        second_after = bridge_call('Profile 2', 'list')
        assert not any(x['id'] == expected_id for x in second_after), 'extension appeared in second profile'
        summary = lambda items: sorted((x['id'], x.get('version'), x.get('enabled')) for x in items)
        assert summary(second_after) == summary(second_before), 'second profile extension state changed'
        print('PASS isolated profiles: extension', expected_id, 'version', entries[0]['version'],
              'selected profile only; second bridge confirms absence', flush=True)
    finally:
        for proc in reversed(processes):
            if proc.poll() is None:
                try:
                    os.killpg(proc.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
        for proc in reversed(processes):
            try:
                proc.wait(timeout=4)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait(timeout=3)
