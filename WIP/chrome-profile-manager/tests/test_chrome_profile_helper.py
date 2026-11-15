"""Local logic tests; no real browser or integration claim."""
import json

import pytest
import chrome_profile_helper as helper

A, B, EXT = 'a' * 32, 'b' * 32, 'c' * 32


class Socket:
    def __init__(self):
        self.frames = []
        self.targets = {}
        self.missing = None
        self.names = {'one': 'First', 'two': 'Second'}
        self.fail_reload = False

    def send(self, data):
        self.frame = json.loads(data)
        self.frames.append(self.frame)
        if self.fail_reload and 'developerPrivate.reload' in self.frame.get('params', {}).get('expression', ''):
            raise TimeoutError('secret endpoint')

    def recv(self):
        m, p = self.frame['method'], self.frame['params']
        if m == 'Target.getTargets':
            r = {'targetInfos': [{'type': 'service_worker', 'url': 'chrome-extension://' + bridge + '/worker.js', 'browserContextId': context} for bridge, context in [(A, 'one'), (B, 'two')] if self.missing != context]}
        elif m == 'Target.createTarget':
            target = 'target' + str(len(self.targets))
            self.targets[target] = p['browserContextId']
            r = {'targetId': target}
        elif m == 'Target.attachToTarget':
            r = {'sessionId': p['targetId']}
        elif m == 'Runtime.evaluate':
            expression = p['expression']
            value = self.names[self.targets[self.frame['sessionId']]] if 'getProfileInfo' in expression else 'ok'
            r = {'result': {'value': value}}
        else:
            r = {}
        return json.dumps({'id': self.frame['id'], 'result': r})

    def close(self):
        pass


@pytest.fixture
def setup(tmp_path, monkeypatch):
    monkeypatch.setenv('XDG_CONFIG_HOME', str(tmp_path))
    folder = tmp_path / 'gptadmin-extension-bridge'
    folder.mkdir()
    path = folder / 'bindings.json'
    path.write_text(json.dumps({'p1': 'chrome-extension://' + A + '/', 'p2': 'chrome-extension://' + B + '/'}))
    path.chmod(0o600)
    sock = Socket()
    monkeypatch.setattr(helper.websocket, 'create_connection', lambda *a, **k: sock)
    state = {'p1': '1.1', 'p2': '1.0'}
    def bridge(profile, method, extension):
        return {'id': extension, 'version': state[profile], 'enabled': True}
    monkeypatch.setattr(helper, 'bridge_call', bridge)
    client = helper.ChromeProfileHelper('ws://localhost/devtools/browser/secret', {'p1': ('First', A), 'p2': ('Second', B)}, timeout=.01)
    yield client, sock, state
    client.close()


def test_reload_uses_named_context_and_readback(setup):
    client, sock, state = setup
    assert client.reload('p1', EXT, '1.1', expected_current_version='1.1')['version'] == '1.1'
    creates = [x['params'] for x in sock.frames if x['method'] == 'Target.createTarget']
    assert creates[-1] == {'url': 'chrome://extensions/', 'browserContextId': 'one'}
    assert state['p2'] == '1.0'
    assert 'secret' not in repr(client)


@pytest.mark.parametrize('failure', ['missing', 'name', 'auth'])
def test_binding_failure_prevents_reload(setup, monkeypatch, failure):
    client, sock, _ = setup
    if failure == 'missing': sock.missing = 'one'
    if failure == 'name': sock.names['one'] = 'Wrong'
    if failure == 'auth': monkeypatch.setattr(helper, 'bridge_call', lambda *args: (_ for _ in ()).throw(RuntimeError('auth')))
    with pytest.raises(helper.ProfileError): client.reload('p1', EXT, '1.1', expected_current_version='1.1')
    assert not any('developerPrivate.reload' in x['params'].get('expression', '') for x in sock.frames)


def test_timeout_is_unknown_and_never_retried(setup):
    client, sock, _ = setup
    sock.fail_reload = True
    with pytest.raises(helper.ReloadUnknown) as e: client.reload('p1', EXT, '1.1', expected_current_version='1.1')
    assert 'secret endpoint' not in str(e.value)
    assert sum('developerPrivate.reload' in x['params'].get('expression', '') for x in sock.frames) == 1


def test_unconfirmed_version_is_unknown(setup):
    client, sock, _ = setup
    with pytest.raises(helper.ReloadUnknown): client.reload('p1', EXT, '2.0', expected_current_version='1.1')
    assert sum('developerPrivate.reload' in x['params'].get('expression', '') for x in sock.frames) == 1


def test_duplicate_names_rejected(monkeypatch):
    monkeypatch.setattr(helper.websocket, 'create_connection', lambda *a, **k: pytest.fail('must not connect'))
    with pytest.raises(helper.ProfileError):
        helper.ChromeProfileHelper('ws://localhost/devtools/browser/secret', {'p1': ('Same', A), 'p2': ('Same', B)})


def test_version_precondition_prevents_reload(setup):
    client, sock, _ = setup
    with pytest.raises(helper.ProfileError, match='precondition'):
        client.reload('p1', EXT, '2.0', expected_current_version='0.9')
    assert not any('developerPrivate.reload' in x['params'].get('expression', '') for x in sock.frames)


def test_bridge_cannot_reload_itself(setup):
    client, sock, _ = setup
    with pytest.raises(helper.ProfileError, match='cannot reload'):
        client.reload('p1', A, '1.0', expected_current_version='1.0')
    assert not sock.frames


def test_inactive_other_profile_does_not_block_target(setup):
    client, sock, _ = setup
    sock.missing = 'two'
    assert client.reload('p1', EXT, '1.1', expected_current_version='1.1')['version'] == '1.1'
