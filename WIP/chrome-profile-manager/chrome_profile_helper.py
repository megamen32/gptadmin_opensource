"""Profile-scoped CDP operations over an explicitly authorized browser socket."""
import json
import os
from pathlib import Path
import re
import stat
import time
from urllib.parse import urlsplit

import websocket
from chrome_profile_manager_mcp import bridge_call


class ProfileError(RuntimeError):
    pass


class ReloadUnknown(ProfileError):
    """Reload may have occurred. Inspect the original operation before retrying."""


class ChromeProfileHelper:
    def __init__(self, endpoint, profiles, *, timeout=10):
        """profiles maps every paired profile ID to (expected Chrome name, bridge ID)."""
        try:
            url = urlsplit(endpoint)
            self._profiles = dict(profiles)
            if url.scheme not in ('ws', 'wss') or url.hostname not in ('127.0.0.1', '::1', 'localhost') or not url.path.startswith('/devtools/browser/') or url.fragment or timeout <= 0 or not self._profiles:
                raise ValueError()
            for key, value in self._profiles.items():
                if not isinstance(key, str) or not key or not isinstance(value, (list, tuple)) or len(value) != 2 or not isinstance(value[0], str) or not value[0] or not isinstance(value[1], str) or not re.fullmatch(r'[a-p]{32}', value[1]):
                    raise ValueError()
            if len({v[0] for v in self._profiles.values()}) != len(self._profiles) or len({v[1] for v in self._profiles.values()}) != len(self._profiles):
                raise ValueError()
            self._ws = websocket.create_connection(endpoint, timeout=timeout, suppress_origin=True)
        except Exception:
            raise ProfileError('invalid configuration or browser connection failed') from None
        self._id = 0
        self._timeout = timeout
        self._targets = []
        self._sessions = {}

    def __repr__(self):
        return '<ChromeProfileHelper>'

    def close(self):
        for target in self._targets:
            try:
                self._call('Target.closeTarget', {'targetId': target})
            except Exception:
                pass
        self._targets.clear()
        self._sessions.clear()
        try:
            self._ws.close()
        except Exception:
            pass

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def _call(self, method, params=None, session=None):
        self._id += 1
        frame = {'id': self._id, 'method': method, 'params': params or {}}
        if session:
            frame['sessionId'] = session
        try:
            self._ws.send(json.dumps(frame))
            while True:
                reply = json.loads(self._ws.recv())
                if reply.get('id') != self._id:
                    continue
                if 'error' in reply or not isinstance(reply.get('result'), dict):
                    raise ProfileError('CDP request rejected')
                return reply['result']
        except ProfileError:
            raise
        except Exception:
            raise ProfileError('CDP transport failed') from None

    def _target(self, context, url):
        target = self._call('Target.createTarget', {'url': url, 'browserContextId': context}).get('targetId')
        if not isinstance(target, str) or not target:
            raise ProfileError('target unavailable')
        self._targets.append(target)
        session = self._call('Target.attachToTarget', {'targetId': target, 'flatten': True}).get('sessionId')
        if not isinstance(session, str) or not session:
            raise ProfileError('session unavailable')
        return session

    def _eval(self, session, expression):
        result = self._call('Runtime.evaluate', {'expression': expression, 'awaitPromise': True, 'returnByValue': True}, session)
        if result.get('exceptionDetails') or not isinstance(result.get('result'), dict) or 'value' not in result['result']:
            raise ProfileError('profile API failed')
        return result['result']['value']

    def _bind(self, profile_id):
        if profile_id not in self._profiles:
            raise ProfileError('unknown profile')
        try:
            path = Path(os.environ.get('XDG_CONFIG_HOME', str(Path.home() / '.config'))) / 'gptadmin-extension-bridge' / 'bindings.json'
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, 'r', encoding='utf-8') as f:
                st = os.fstat(f.fileno())
                if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
                    raise ValueError()
                bindings = json.load(f)
            if not isinstance(bindings, dict) or not set(self._profiles).issubset(bindings):
                raise ValueError()
            for key, (_, bridge_id) in self._profiles.items():
                if bindings[key] != 'chrome-extension://' + bridge_id + '/':
                    raise ValueError()
            info = bridge_call(profile_id, 'get', self._profiles[profile_id][1])
            if not isinstance(info, dict) or info.get('id') != self._profiles[profile_id][1]:
                raise ValueError()
        except Exception:
            raise ProfileError('bridge binding or authorization failed') from None
        targets = self._call('Target.getTargets', {'filter': [{'type': 'service_worker', 'exclude': False}, {'exclude': True}]}).get('targetInfos')
        if not isinstance(targets, list):
            raise ProfileError('bridge contexts unavailable')
        expected, bridge_id = self._profiles[profile_id]
        prefix = 'chrome-extension://' + bridge_id + '/'
        matches = [t for t in targets if isinstance(t, dict) and t.get('type') == 'service_worker' and isinstance(t.get('url'), str) and t['url'].startswith(prefix)]
        if len(matches) != 1 or not matches[0].get('browserContextId'):
            raise ProfileError('bridge context missing or ambiguous')
        context = matches[0]['browserContextId']
        for key, (_, other_id) in self._profiles.items():
            if key != profile_id and any(t.get('type') == 'service_worker' and isinstance(t.get('url'), str) and t['url'].startswith('chrome-extension://' + other_id + '/') and t.get('browserContextId') == context for t in targets if isinstance(t, dict)):
                raise ProfileError('profile context conflict')
        cached = self._sessions.get(profile_id)
        if cached and cached[0] != context:
            raise ProfileError('profile context changed; reconnect required')
        if not cached:
            cached = (context, self._target(context, 'chrome://settings/'), self._target(context, 'chrome://extensions/'))
            self._sessions[profile_id] = cached
        name = self._eval(cached[1], "import('chrome://resources/js/cr.js').then(m => m.sendWithPromise('getProfileInfo')).then(info => info.name)")
        if name != expected:
            raise ProfileError('profile name mismatch or conflict')
        return context

    @staticmethod
    def _validate_id(extension_id):
        if not isinstance(extension_id, str) or not re.fullmatch(r'[a-p]{32}', extension_id):
            raise ProfileError('invalid extension id')

    def get(self, profile_id, extension_id):
        self._validate_id(extension_id)
        self._bind(profile_id)
        try:
            result = bridge_call(profile_id, 'get', extension_id)
            if not isinstance(result, dict) or result.get('id') != extension_id:
                raise ValueError()
            return result
        except Exception:
            raise ProfileError('bridge readback failed') from None

    def list_extensions(self, profile_id):
        self._bind(profile_id)
        try:
            result = bridge_call(profile_id, "list")
            if not isinstance(result, list):
                raise ValueError()
            return result
        except Exception:
            raise ProfileError("bridge readback failed") from None

    def unpacked_details(self, profile_id, extension_id):
        """Read only the unpacked artifact metadata for a bound profile."""
        self._validate_id(extension_id)
        self._bind(profile_id)
        session = self._sessions[profile_id][2]
        expression = 'new Promise(resolve => chrome.developerPrivate.getExtensionInfo(' + json.dumps(extension_id) + ', x => resolve(x ? {id:x.id, version:x.version, location:x.location, path:x.path} : null)))'
        info = self._eval(session, expression)
        if not isinstance(info, dict) or info.get('id') != extension_id or info.get('location') != 'UNPACKED' or not isinstance(info.get('path'), str) or not isinstance(info.get('version'), str):
            raise ProfileError('unpacked extension metadata unavailable')
        return info

    def reload(self, profile_id, extension_id, expected_version, *, expected_current_version):
        self._validate_id(extension_id)
        if not isinstance(expected_version, str) or not expected_version or not isinstance(expected_current_version, str) or not expected_current_version:
            raise ProfileError('expected version required')
        if profile_id in self._profiles and extension_id == self._profiles[profile_id][1]:
            raise ProfileError('cannot reload the management bridge')
        context = self._bind(profile_id)
        session = self._sessions[profile_id][2]
        try:
            before = bridge_call(profile_id, 'get', extension_id)
            if not isinstance(before, dict) or before.get('id') != extension_id:
                raise ValueError()
        except Exception:
            raise ProfileError('bridge authorization or extension read failed') from None
        if before.get('version') != expected_current_version or before.get('enabled') is not True:
            raise ProfileError('extension version or enabled-state precondition failed')
        expression = 'new Promise(resolve => chrome.developerPrivate.reload(' + json.dumps(extension_id) + ", {failQuietly:true,populateErrorForUnpacked:true}, err => resolve(chrome.runtime.lastError?.message || (err ? 'reload_failed' : 'ok'))))"
        try:
            outcome = self._eval(session, expression)
        except Exception:
            raise ReloadUnknown('reload outcome unknown; do not retry automatically') from None
        if outcome != 'ok':
            raise ReloadUnknown('reload failed; inspect state before retry')
        deadline = time.monotonic() + self._timeout
        while True:
            try:
                state = bridge_call(profile_id, 'get', extension_id)
                if isinstance(state, dict) and state.get('id') == extension_id and state.get('version') == expected_version and state.get('enabled') is True:
                    return state
            except Exception:
                pass
            if time.monotonic() >= deadline:
                raise ReloadUnknown('reload readback unconfirmed; do not retry automatically')
            time.sleep(min(.05, max(0, deadline - time.monotonic())))
