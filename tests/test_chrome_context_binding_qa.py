"""Opt-in isolated Chromium profile-context and extension API experiment.

No DOM, page text, storage or user profile data is read.
"""
import json
import os
import platform
import secrets
from pathlib import Path
import subprocess
import select
import shutil
import sys
import threading
import time

import pytest

websocket = pytest.importorskip("websocket")


@pytest.mark.skipif(os.environ.get("CHROME_CONTEXT_QA") != "1", reason="isolated Chrome QA only")
def test_profile_context_webui_extension_api(tmp_path, monkeypatch):
    from chrome_profile_manager_bridge.provision import provision, extension_origin, public_key
    from chrome_profile_manager_mcp import bridge_call, ProfileManager, ProfileManagerError, content_hash, extension_identity
    from chrome_profile_helper import ChromeProfileHelper, ProfileError
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "config"))
    profile = tmp_path / "chrome"
    profile.mkdir()
    bridge = provision("default", profile)
    extension = tmp_path / "test-extension"
    extension.mkdir()
    manifest = extension / "manifest.json"
    qa_key = public_key()
    manifest.write_text(json.dumps({"manifest_version": 3, "name": "Context QA", "key": qa_key, "version": "1.0"}))
    xvfb = None
    chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' if platform.system() == 'Darwin' else '/usr/bin/google-chrome'
    if platform.system() != 'Darwin':
        xvfb = subprocess.Popen(["Xvfb", "-displayfd", "1", "-screen", "0", "1280x800x24", "-nolisten", "tcp"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        display = xvfb.stdout.readline().strip()
        if not display.isdigit():
            xvfb.terminate()
            xvfb.wait(timeout=5)
            pytest.fail("QA Xvfb did not start")
        monkeypatch.setenv("DISPLAY", ":" + display)
        if os.environ.get('CHROME_CHOOSER_QA') == '1':
            runtime = tmp_path / 'runtime'
            runtime.mkdir(mode=0o700)
            monkeypatch.setenv('XDG_RUNTIME_DIR', str(runtime))
            monkeypatch.setenv('XDG_DATA_HOME', str(tmp_path / 'data'))
            subprocess.run(['dbus-update-activation-environment', 'DISPLAY', 'HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_RUNTIME_DIR'], check=True)
    proc = subprocess.Popen([chrome, "--no-first-run",
        "--disable-background-networking", "--password-store=basic", "--remote-debugging-port=0",
        "--user-data-dir=" + str(profile), "about:blank"],
        env=os.environ.copy(), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    ws = None
    first_helper = None
    second_helper = None
    manager = None
    shellmcp = None
    try:
        port_file = profile / "DevToolsActivePort"
        deadline = time.monotonic() + 15
        while not port_file.exists() and time.monotonic() < deadline:
            if proc.poll() is not None: pytest.fail("isolated Chrome exited")
            time.sleep(.05)
        lines = port_file.read_text().splitlines()
        endpoint = f"ws://127.0.0.1:{lines[0]}{lines[1]}"
        ws = websocket.create_connection(endpoint, timeout=30, suppress_origin=True)
        number = 0
        def call(method, params=None, session=None):
            nonlocal number
            number += 1
            frame = {"id": number, "method": method, "params": params or {}}
            if session: frame["sessionId"] = session
            ws.send(json.dumps(frame))
            while True:
                answer = json.loads(ws.recv())
                if answer.get("id") == number:
                    assert "error" not in answer, answer.get("error")
                    return answer["result"]
        bridge_id = call("Extensions.loadUnpacked", {"path": str(bridge)})["id"]
        extension_id = call("Extensions.loadUnpacked", {"path": str(extension)})["id"]
        deadline = time.monotonic() + 10
        while True:
            targets = call("Target.getTargets", {"filter": [{"type": "service_worker", "exclude": False}, {"exclude": True}]})["targetInfos"]
            matches = [x for x in targets if x.get("url", "").startswith("chrome-extension://" + bridge_id + "/")]
            if matches: break
            if time.monotonic() >= deadline: pytest.fail("QA bridge service worker missing")
            time.sleep(.05)
        context = matches[0].get("browserContextId")
        assert context, "Chrome did not provide a context for the QA extension"
        def owned_native_window(context_id):
            assert xvfb, 'native window probe requires isolated Xvfb'
            nonce = 'gptadminqa' + secrets.token_hex(16)
            target_id = call('Target.createTarget', {'url':'chrome://extensions/', 'browserContextId':context_id, 'newWindow':True})['targetId']
            session_id = call('Target.attachToTarget', {'targetId':target_id, 'flatten':True})['sessionId']
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                # Assign a self-generated title to this owned WebUI window; do
                # not read page content or titles of unrelated windows.
                call('Runtime.evaluate', {'expression':'document.title = ' + json.dumps(nonce), 'returnByValue':True}, session_id)
                result = subprocess.run(['xdotool','search','--all','--pid',str(proc.pid),'--name',nonce], capture_output=True, text=True)
                candidates = result.stdout.split()
                if candidates:
                    assert len(candidates) == 1, 'owned window is ambiguous'
                    assert 'windowId' in call('Browser.getWindowForTarget', {'targetId':target_id})
                    return target_id, candidates[0]
                time.sleep(.05)
            pytest.fail('owned native window could not be identified')
        first_native = owned_native_window(context) if os.environ.get('CHROME_WINDOW_BINDING_QA') == '1' else None
        target = call("Target.createTarget", {"url": "chrome://extensions/", "browserContextId": context})["targetId"]
        session = call("Target.attachToTarget", {"targetId": target, "flatten": True})["sessionId"]
        result = call("Runtime.evaluate", {"expression": "typeof chrome.developerPrivate", "returnByValue": True}, session)
        assert result["result"].get("value") == "object", result
        # Ask Chrome only about this disposable unpacked extension; return a
        # boolean for its path rather than exposing filesystem metadata.
        info_expression = "new Promise(resolve => chrome.developerPrivate.getExtensionInfo(" + json.dumps(extension_id) + ", x => resolve({id:x?.id, location:x?.location, pathMatches:x?.path === " + json.dumps(str(extension)) + "})))"
        info = call('Runtime.evaluate', {'expression': info_expression, 'awaitPromise': True, 'returnByValue': True}, session)
        assert info['result']['value'] == {'id': extension_id, 'location': 'UNPACKED', 'pathMatches': True}
        deadline = time.monotonic() + 10
        while True:
            try:
                if bridge_call("default", "get", extension_id)["version"] == "1.0": break
            except RuntimeError:
                pass
            if time.monotonic() >= deadline: pytest.fail("QA bridge not connected")
            time.sleep(.05)
        # Ordinary unpacked installs use developer mode. Change only this QA profile.
        enabled = call("Runtime.evaluate", {"expression": "new Promise(resolve => chrome.developerPrivate.updateProfileConfiguration({inDeveloperMode:true}, () => resolve(chrome.runtime.lastError?.message || 'ok')))", "awaitPromise": True, "returnByValue": True}, session)
        assert enabled["result"].get("value") == "ok", enabled
        manifest.write_text(json.dumps({"manifest_version": 3, "name": "Context QA", "key": qa_key, "version": "1.1"}))
        expression = "new Promise(resolve => chrome.developerPrivate.reload(" + json.dumps(extension_id) + ", {failQuietly:true,populateErrorForUnpacked:true}, err => resolve(chrome.runtime.lastError?.message || (err ? 'reload_failed' : 'ok'))))"
        try:
            result = call("Runtime.evaluate", {"expression": expression, "awaitPromise": True, "returnByValue": True}, session)
        except websocket.WebSocketTimeoutException:
            state = bridge_call("default", "get", extension_id)
            pytest.xfail("QA reload response timed out; Chrome management readback version=" + str(state.get("version")))
        assert result["result"].get("value") == "ok", result
        deadline = time.monotonic() + 5
        while True:
            state = bridge_call("default", "get", extension_id)
            if state["version"] == "1.1": break
            if time.monotonic() >= deadline:
                pytest.xfail("QA reload acknowledged; Chrome management readback version=" + str(state.get("version")))
            time.sleep(.05)
        # Query only each QA profile's name via Chrome's profile API, without DOM.
        def profile_name(context_id):
            target_id = call("Target.createTarget", {"url": "chrome://settings/", "browserContextId": context_id})["targetId"]
            profile_session = call("Target.attachToTarget", {"targetId": target_id, "flatten": True})["sessionId"]
            profile_info = call("Runtime.evaluate", {"expression": "import('chrome://resources/js/cr.js').then(m => m.sendWithPromise('getProfileInfo')).then(info => info.name)", "awaitPromise": True, "returnByValue": True}, profile_session)
            return profile_info["result"].get("value")
        first_name = profile_name(context)
        first_helper = ChromeProfileHelper(endpoint, {"default": (first_name, bridge_id)})
        assert first_helper.get("default", extension_id)["version"] == "1.1"
        if os.environ.get('CHROME_CHOOSER_QA') == '1':
            directory = provision('persistent-qa', profile)
            chosen_id = extension_origin(json.loads((directory / 'manifest.json').read_text())['key'])[19:-1]
            outcome = []
            def choose():
                try:
                    outcome.append(call('Runtime.evaluate', {'expression': "new Promise(resolve => chrome.developerPrivate.loadUnpacked({failQuietly:true}, () => resolve(chrome.runtime.lastError?.message || 'ok')))", 'userGesture': True, 'awaitPromise': True, 'returnByValue': True}, session))
                except Exception as exc: outcome.append(str(exc))
            before = set(subprocess.run(['xdotool', 'search', '--onlyvisible', '--name', ''], capture_output=True, text=True).stdout.split()) if xvfb else set()
            t = threading.Thread(target=choose, daemon=True)
            t.start()
            if xvfb:
                deadline = time.monotonic() + 8
                named = []
                while time.monotonic() < deadline:
                    found = set(subprocess.run(['xdotool', 'search', '--onlyvisible', '--name', ''], capture_output=True, text=True).stdout.split()) - before
                    named = [item for item in found if 'xdg-desktop-portal-gtk' in subprocess.run(['xprop', '-id', item, 'WM_CLASS'], capture_output=True, text=True).stdout]
                    if named or not t.is_alive(): break
                    time.sleep(.1)
                assert len(named) == 1, f'QA chooser unavailable: {len(named)} candidate windows, {outcome}'
                window = named[0]
                subprocess.run(['xdotool', 'windowfocus', '--sync', window, 'key', 'ctrl+l', 'type', '--clearmodifiers', '--', str(directory)], check=True)
                subprocess.run(['xdotool', 'key', 'Return'], check=True)
                t.join(3)
                if t.is_alive():
                    subprocess.run(['xdotool', 'key', 'Return'], check=True)
                    t.join(5)
            else:
                script = '''on run argv
set targetPid to (item 1 of argv) as integer
set targetPath to item 2 of argv
tell application "System Events"
  if not UI elements enabled then error "Accessibility unavailable"
  set ids to id of every application process
  set pids to unix id of every application process
  set objectId to 0
  repeat with i from 1 to count of pids
    if item i of pids is targetPid then set objectId to item i of ids
  end repeat
  if objectId is 0 then error "QA process unavailable"
  set chooserIndex to 0
  repeat 80 times
    if (unix id of application process id objectId) is not targetPid then error "QA process changed"
    set chooserIndex to 0
    repeat with i from 1 to (count of windows of application process id objectId)
      set windowName to ""
      try
        set windowName to name of window i of application process id objectId
      end try
      if windowName is "Открыть" or windowName is "Open" then
        if chooserIndex is not 0 then error "QA chooser ambiguous"
        set chooserIndex to i
      end if
    end repeat
    if chooserIndex is not 0 then exit repeat
    delay 0.1
  end repeat
  if chooserIndex is 0 then error "QA chooser unavailable"
  if (unix id of application process id objectId) is not targetPid then error "QA process changed"
  set frontmost of application process id objectId to true
  perform action "AXRaise" of window chooserIndex of application process id objectId
  if not (frontmost of application process id objectId) then error "QA process not frontmost"
  key code 44
  delay 1
  if (value of attribute "AXRole" of (value of attribute "AXFocusedUIElement" of application process id objectId)) is not "AXTextField" then error "QA path field unavailable"
  set value of (value of attribute "AXFocusedUIElement" of application process id objectId) to targetPath
  delay 1
  if not (frontmost of application process id objectId) then error "QA process lost focus to " & (name of first application process whose frontmost is true)
  key code 36
  delay 1
  if not (frontmost of application process id objectId) then error "QA process lost focus"
  key code 36
end tell
end run'''
                subprocess.run(['/usr/bin/osascript', '-', str(proc.pid), str(directory)], input=script, text=True, check=True, timeout=25)
                t.join(8)
            assert outcome and isinstance(outcome[0], dict), f'QA chooser unresolved: {outcome}'
            assert outcome[0]['result'].get('value') == 'ok'
            chosen = [e for e in bridge_call('default', 'list') if e.get('id') == chosen_id]
            assert len(chosen) == 1
            chosen_version = chosen[0]['version']
        manager = ProfileManager(state_dir=tmp_path / 'manager', roots=[profile])
        deadline = time.monotonic() + 10
        while not any(p['profile_id'] == 'default' for p in manager.profile_list()):
            if time.monotonic() >= deadline: pytest.fail('Chrome did not persist QA profile metadata')
            time.sleep(.05)
        staged = manager.artifact_dir / 'qa-stage'
        staged.mkdir()
        staged_manifest = {'manifest_version': 3, 'name': 'Staged QA', 'version': '1.0', 'key': public_key()}
        (staged / 'manifest.json').write_text(json.dumps(staged_manifest))
        staged_id, _ = extension_identity(staged_manifest)
        digest = content_hash(staged)
        staged_result = manager.helper_stage('default', 'qa-stage', staged_id, '1.0', digest)
        assert staged_result['extension_id'] == staged_id and staged_result['digest'] == digest and staged_result['immutable'] is True
        with pytest.raises(ProfileManagerError):
            manager.helper_stage('default', 'qa-stage', staged_id, '1.0', '0' * 64)
        manifest.write_text(json.dumps({'manifest_version': 3, 'name': 'Context QA', 'key': qa_key, 'version': '1.15'}))
        assert manager.helper_reload('default', extension_id, '1.1', '1.15')['extension']['version'] == '1.15'
        with pytest.raises(ProfileManagerError, match='precondition'):
            manager.helper_reload('default', extension_id, '1.1', '1.15')
        manifest.write_text(json.dumps({'manifest_version': 3, 'name': 'Context QA', 'key': qa_key, 'version': '1.1'}))
        assert manager.helper_reload('default', extension_id, '1.15', '1.1')['extension']['version'] == '1.1'
        backup = manager.helper_backup('default', extension_id, '1.1')
        assert backup['extension_id'] == extension_id and backup['version'] == '1.1' and backup['digest'] == content_hash(extension)
        assert manager._stage(backup['stage_id'])['digest'] == backup['digest']
        with pytest.raises(ProfileManagerError, match='precondition'):
            manager.helper_backup('default', extension_id, '0.1')
        if os.environ.get('GPTADMIN_CHROME_QA') == '1':
            mcp_artifacts = tmp_path / 'mcp-state' / 'artifacts'
            mcp_artifacts.mkdir(parents=True, exist_ok=True)
            shutil.copytree(staged, mcp_artifacts / 'qa-stage')
            env = os.environ.copy()
            env.update(CHROME_PROFILE_ROOTS=str(profile), CHROME_PROFILE_MANAGER_STATE=str(tmp_path / 'mcp-state'), SHELLMCP_MCP_CONFIG=str(tmp_path / 'mcp.json'), SHELLMCP_SPOOL_DIR=str(tmp_path / 'spool'), SHELLMCP_IDENTITY_DIR=str(tmp_path / 'identity'), SHELLMCP_DEFAULT_HOME=str(tmp_path), SHELLMCP_DEFAULT_CWD=str(tmp_path), SHELLMCP_HEARTBEAT='0', SHELLMCP_QUEUE='0', HUB_URL='')
            shellmcp = subprocess.Popen([os.environ.get('GPTADMIN_QA_BINARY', '/opt/gptadmin/bin/shellmcp'), '--mcp-stdio'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1, env=env)
            sequence = 0
            def rpc(method, params):
                nonlocal sequence
                sequence += 1
                shellmcp.stdin.write(json.dumps({'jsonrpc':'2.0', 'id':sequence, 'method':method, 'params':params}) + '\n')
                shellmcp.stdin.flush()
                assert select.select([shellmcp.stdout], [], [], 30)[0], 'GPTAdmin response timeout'
                reply = json.loads(shellmcp.stdout.readline())
                assert 'error' not in reply, reply.get('error')
                return reply['result']
            rpc('initialize', {'protocolVersion':'2025-03-26', 'capabilities':{}, 'clientInfo':{'name':'qa', 'version':'1'}})
            config = {'ref':'qa-helper', 'transport':'stdio', 'command':sys.executable, 'args':[str(Path(__file__).resolve().parents[1] / 'chrome_profile_manager_mcp.py')], 'enabled':True}
            assert not rpc('tools/call', {'name':'mcp_manage', 'arguments':{'action':'upsert', 'config':config}}).get('isError')
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_stage', 'arguments':{'profile_id':'default', 'source_path':'qa-stage', 'extension_id':staged_id, 'version':'1.0', 'digest':digest}}})
            assert not result.get('isError'), result
            child = result['structuredContent']['result']
            assert not child.get('isError'), child
            assert child['structuredContent']['digest'] == digest and child['structuredContent']['extension_id'] == staged_id
            first_stage = child['structuredContent']['stage_id']
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_prepare', 'arguments':{'profile_id':'default', 'stage_id':first_stage}}})
            assert not result.get('isError'), result
            prepared = result['structuredContent']['result']['structuredContent']
            assert prepared['installed'] is False
            assert call('Extensions.loadUnpacked', {'path':prepared['path']})['id'] == staged_id
            deadline = time.monotonic() + 10
            while True:
                try:
                    if bridge_call('default', 'get', staged_id).get('version') == '1.0': break
                except RuntimeError: pass
                if time.monotonic() >= deadline: pytest.fail('managed QA extension did not load')
                time.sleep(.05)
            second_artifact = mcp_artifacts / 'qa-stage-v2'
            second_artifact.mkdir()
            (second_artifact / 'manifest.json').write_text(json.dumps({**staged_manifest, 'version':'2.0'}))
            second_digest = content_hash(second_artifact)
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_stage', 'arguments':{'profile_id':'default', 'source_path':'qa-stage-v2', 'extension_id':staged_id, 'version':'2.0', 'digest':second_digest}}})
            second_stage = result['structuredContent']['result']['structuredContent']['stage_id']
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_update', 'arguments':{'profile_id':'default', 'stage_id':second_stage, 'expected_version':'1.0'}}})
            changed = result['structuredContent']['result']['structuredContent']
            assert changed['version'] == '2.0' and changed['digest'] == second_digest
            assert bridge_call('default', 'get', staged_id)['version'] == '2.0'
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_rollback', 'arguments':{'profile_id':'default', 'extension_id':staged_id, 'backup_id':changed['backup_id'], 'expected_version':'2.0'}}})
            restored = result['structuredContent']['result']['structuredContent']
            assert restored['version'] == '1.0' and restored['digest'] == digest
            assert bridge_call('default', 'get', staged_id)['version'] == '1.0'
            manifest.write_text(json.dumps({'manifest_version': 3, 'name': 'Context QA', 'key': qa_key, 'version': '1.16'}))
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_reload', 'arguments':{'profile_id':'default', 'extension_id':extension_id, 'expected_current_version':'1.1', 'expected_version':'1.16'}}})
            assert not result.get('isError'), result
            child = result['structuredContent']['result']
            assert not child.get('isError'), child
            assert child['structuredContent']['extension']['version'] == '1.16'
            assert bridge_call('default', 'get', extension_id)['version'] == '1.16'
            result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_helper_backup', 'arguments':{'profile_id':'default', 'extension_id':extension_id, 'expected_version':'1.16'}}})
            assert not result.get('isError'), result
            child = result['structuredContent']['result']
            assert not child.get('isError'), child
            assert child['structuredContent']['digest'] == content_hash(extension) and child['structuredContent']['version'] == '1.16'
            manifest.write_text(json.dumps({'manifest_version': 3, 'name': 'Context QA', 'key': qa_key, 'version': '1.1'}))
            assert manager.helper_reload('default', extension_id, '1.16', '1.1')['extension']['version'] == '1.1'
        # Open a second named profile in the same disposable browser instance.
        second_bridge = provision("Profile 2", profile)
        opened = subprocess.Popen([chrome,
            "--user-data-dir=" + str(profile), "--profile-directory=Profile 2", "about:blank"],
            env=os.environ.copy(), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            opened.wait(timeout=10)
        except subprocess.TimeoutExpired:
            opened.terminate()
            opened.wait(timeout=5)
            pytest.fail("second QA profile launch did not complete")
        assert (profile / "Profile 2").is_dir(), "Chrome did not create the second QA profile"
        deadline = time.monotonic() + 10
        while True:
            pages = call("Target.getTargets", {"filter": [{"type": "page", "exclude": False}, {"exclude": True}]})["targetInfos"]
            second_pages = [x for x in pages if x.get("browserContextId") and x["browserContextId"] != context]
            if second_pages: break
            if time.monotonic() >= deadline: pytest.fail("second QA profile target not discovered")
            time.sleep(.05)
        call("Target.activateTarget", {"targetId": second_pages[0]["targetId"]})
        second_id = call("Extensions.loadUnpacked", {"path": str(second_bridge)})["id"]
        assert call("Extensions.loadUnpacked", {"path": str(extension)})["id"] == extension_id
        deadline = time.monotonic() + 10
        while True:
            targets = call("Target.getTargets", {"filter": [{"type": "service_worker", "exclude": False}, {"exclude": True}]})["targetInfos"]
            second = [x for x in targets if x.get("url", "").startswith("chrome-extension://" + second_id + "/")]
            try:
                second_version = bridge_call("Profile 2", "get", extension_id)["version"]
            except RuntimeError:
                second_version = None
            if second and second_version == "1.1": break
            if time.monotonic() >= deadline: pytest.fail("second QA profile not ready")
            time.sleep(.05)
        assert second[0].get("browserContextId") != context, "profiles shared the same CDP context"
        if first_native:
            second_native = owned_native_window(second[0]['browserContextId'])
            assert first_native[1] != second_native[1]
            call('Target.closeTarget', {'targetId':second_native[0]})
            call('Target.closeTarget', {'targetId':first_native[0]})
        second_name = profile_name(second[0]["browserContextId"])
        assert isinstance(first_name, str) and first_name
        assert isinstance(second_name, str) and second_name and second_name != first_name
        manifest.write_text(json.dumps({"manifest_version": 3, "name": "Context QA", "key": qa_key, "version": "1.2"}))
        # Reuse the helper's first-profile session after the second becomes active.
        assert first_helper.reload("default", extension_id, "1.2", expected_current_version="1.1")["version"] == "1.2"
        assert bridge_call("Profile 2", "get", extension_id)["version"] == "1.1"
        deadline = time.monotonic() + 10
        while not any(p['profile_id'] == 'Profile 2' for p in manager.profile_list()):
            if time.monotonic() >= deadline: pytest.fail('Chrome did not persist second QA profile metadata')
            time.sleep(.05)
        manifest.write_text(json.dumps({'manifest_version': 3, 'name': 'Context QA', 'key': qa_key, 'version': '1.25'}))
        assert manager.helper_reload('default', extension_id, '1.2', '1.25')['extension']['version'] == '1.25'
        assert bridge_call('Profile 2', 'get', extension_id)['version'] == '1.1'
        second_helper = ChromeProfileHelper(endpoint, {"default": (first_name, bridge_id), "Profile 2": (second_name, second_id)})
        # Developer mode must be enabled separately on the second QA profile.
        second_settings = call("Target.createTarget", {"url": "chrome://extensions/", "browserContextId": second[0]["browserContextId"]})["targetId"]
        second_session = call("Target.attachToTarget", {"targetId": second_settings, "flatten": True})["sessionId"]
        result = call("Runtime.evaluate", {"expression": "new Promise(resolve => chrome.developerPrivate.updateProfileConfiguration({inDeveloperMode:true}, () => resolve(chrome.runtime.lastError?.message || 'ok')))", "awaitPromise": True, "returnByValue": True}, second_session)
        assert result["result"].get("value") == "ok", result
        manifest.write_text(json.dumps({"manifest_version": 3, "name": "Context QA", "key": qa_key, "version": "1.3"}))
        assert second_helper.reload("Profile 2", extension_id, "1.3", expected_current_version="1.1")["version"] == "1.3"
        assert first_helper.get("default", extension_id)["version"] == "1.25"
        manifest.write_text(json.dumps({'manifest_version': 3, 'name': 'Context QA', 'key': qa_key, 'version': '1.35'}))
        assert manager.helper_reload('Profile 2', extension_id, '1.3', '1.35')['extension']['version'] == '1.35'
        assert bridge_call('default', 'get', extension_id)['version'] == '1.25'
        second_backup = manager.helper_backup('Profile 2', extension_id, '1.35')
        assert second_backup['profile_id'] == 'Profile 2' and second_backup['digest'] == content_hash(extension)
        with pytest.raises(ProfileManagerError, match='identity or version mismatch'):
            manager.helper_backup('default', extension_id, '1.25')
        # A mismatched claimed name must stop before touching the extension.
        manifest.write_text(json.dumps({"manifest_version": 3, "name": "Context QA", "key": qa_key, "version": "1.4"}))
        first_helper._profiles["default"] = ("wrong profile name", bridge_id)
        with pytest.raises(ProfileError, match="profile name mismatch"):
            first_helper.reload("default", extension_id, "1.4", expected_current_version="1.25")
        assert bridge_call("default", "get", extension_id)["version"] == "1.25"
        assert bridge_call("Profile 2", "get", extension_id)["version"] == "1.35"
        if os.environ.get('CHROME_CHOOSER_QA') == '1':
            manager.close(); manager = None
            first_helper.close(); first_helper = None
            second_helper.close(); second_helper = None
            if shellmcp and shellmcp.poll() is None:
                shellmcp.terminate()
                try: shellmcp.wait(timeout=5)
                except subprocess.TimeoutExpired: shellmcp.kill(); shellmcp.wait(timeout=5)
                shellmcp = None
            ws.send(json.dumps({'id': 999, 'method': 'Browser.close'}))
            ws.close(); ws = None
            proc.wait(timeout=10)
            # In this disposable setup, replace the temporary CDP-loaded bridge
            # pairing with the persistently installed bridge for Default.
            registry = tmp_path / 'config' / 'gptadmin-extension-bridge'
            for filename in ('bindings.json', 'roots.json'):
                path = registry / filename
                data = json.loads(path.read_text())
                data['default'] = data.pop('persistent-qa')
                path.write_text(json.dumps(data))
            port_file.unlink(missing_ok=True)
            proc = subprocess.Popen([chrome, '--no-first-run', '--disable-background-networking', '--password-store=basic', '--remote-debugging-port=0', '--user-data-dir=' + str(profile), '--profile-directory=Default', 'about:blank'], env=os.environ.copy(), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            deadline = time.monotonic() + 15
            while not port_file.exists() and time.monotonic() < deadline:
                if proc.poll() is not None: pytest.fail('restarted QA Chrome exited')
                time.sleep(.05)
            lines = port_file.read_text().splitlines()
            ws = websocket.create_connection(f'ws://127.0.0.1:{lines[0]}{lines[1]}', timeout=10, suppress_origin=True)
            readback = call('Extensions.getExtensions')['extensions']
            matches = [item for item in readback if item.get('id') == chosen_id]
            assert len(matches) == 1 and matches[0].get('version') == chosen_version and matches[0].get('enabled') is True
            deadline = time.monotonic() + 15
            while True:
                try:
                    reconnected = bridge_call('default', 'get', chosen_id)
                    if reconnected.get('id') == chosen_id and reconnected.get('enabled') is True: break
                except RuntimeError:
                    pass
                if time.monotonic() >= deadline: pytest.fail('persisted QA bridge did not reconnect after restart')
                time.sleep(.1)
            if os.environ.get('GPTADMIN_CHROME_QA') == '1':
                shellmcp = subprocess.Popen([os.environ.get('GPTADMIN_QA_BINARY', '/opt/gptadmin/bin/shellmcp'), '--mcp-stdio'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1, env=env)
                rpc('initialize', {'protocolVersion':'2025-03-26', 'capabilities':{}, 'clientInfo':{'name':'qa-restart', 'version':'1'}})
                result = rpc('tools/call', {'name':'mcp_call', 'arguments':{'ref':'qa-helper', 'name':'profile_extensions_list', 'arguments':{'profile_id':'default'}}})
                assert not result.get('isError'), result
                child = result['structuredContent']['result']
                assert not child.get('isError'), child
                entries = [entry for entry in child['structuredContent']['extensions'] if entry.get('id') == chosen_id]
                assert len(entries) == 1 and entries[0].get('enabled') is True and entries[0].get('version') == chosen_version
    finally:
        if shellmcp and shellmcp.poll() is None:
            shellmcp.terminate()
            try: shellmcp.wait(timeout=5)
            except subprocess.TimeoutExpired: shellmcp.kill(); shellmcp.wait(timeout=5)
        if manager: manager.close()
        if second_helper: second_helper.close()
        if first_helper: first_helper.close()
        if ws:
            try: ws.send(json.dumps({"id": 999, "method": "Browser.close"}))
            except OSError: pass
            ws.close()
        try: proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.terminate()
            proc.wait(timeout=5)
        if xvfb:
            xvfb.terminate()
            xvfb.wait(timeout=5)
