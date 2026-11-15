import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

import pytest

SCRIPT = Path(__file__).resolve().parents[1] / 'chrome_profile_manager_bridge' / 'provision.py'
spec = importlib.util.spec_from_file_location('bridge_provision', SCRIPT)
provision = importlib.util.module_from_spec(spec)
spec.loader.exec_module(provision)


def test_profiles_repeat_and_preserve_neighbors(tmp_path, monkeypatch):
    monkeypatch.setenv('HOME', str(tmp_path))
    monkeypatch.setenv('XDG_CONFIG_HOME', str(tmp_path / 'config'))
    data = tmp_path / 'chrome'
    native_dir = data / 'NativeMessagingHosts'
    native_dir.mkdir(parents=True)
    neighbor = native_dir / 'other.json'
    neighbor.write_text('untouched')
    native_path = native_dir / (provision.NAME + '.json')
    old_origin = 'chrome-extension://' + 'a' * 32 + '/'
    native_path.write_text(json.dumps({'name': provision.NAME, 'type': 'stdio', 'path': str(provision.HOST), 'allowed_origins': [old_origin], 'extra': 'keep'}))
    first = provision.provision('Default', data)
    root = tmp_path / 'config/gptadmin-extension-bridge'
    registry_path = root / 'profiles.json'
    binding_path = root / 'bindings.json'
    original_registry = registry_path.read_bytes()
    assert provision.provision('Default', data) == first
    assert registry_path.read_bytes() == original_registry
    second = provision.provision('Profile 1', data)
    bindings = json.loads(binding_path.read_text())
    registry = json.loads(registry_path.read_text())
    assert first != second and bindings['Default'] != bindings['Profile 1']
    assert set(bindings.values()) == set(registry)
    assert len(set(registry.values())) == 2
    for profile, artifact in [('Default', first), ('Profile 1', second)]:
        assert provision.extension_origin(json.loads((artifact / 'manifest.json').read_text())['key']) == bindings[profile]
        assert 'AUTO_CONNECT = true' in (artifact / 'config.js').read_text()
    assert registry_path.stat().st_mode & 0o777 == 0o600
    assert binding_path.stat().st_mode & 0o777 == 0o600
    native = json.loads(native_path.read_text())
    assert set(native['allowed_origins']) == set(bindings.values()) | {old_origin}
    assert native['extra'] == 'keep' and neighbor.read_text() == 'untouched'


def test_conflicts_and_unsafe_registry_fail_closed(tmp_path, monkeypatch):
    monkeypatch.setenv('XDG_CONFIG_HOME', str(tmp_path / 'config'))
    data = tmp_path / 'chrome'
    data.mkdir()
    provision.provision('one', data)
    root = tmp_path / 'config/gptadmin-extension-bridge'
    registry = root / 'profiles.json'
    before = registry.read_bytes()
    registry.chmod(0o644)
    with pytest.raises(ValueError, match='unsafe file'):
        provision.provision('two', data)
    assert registry.read_bytes() == before
    registry.chmod(0o600)
    native = data / 'NativeMessagingHosts' / (provision.NAME + '.json')
    value = json.loads(native.read_text())
    value['path'] = '/somebody/else'
    native.write_text(json.dumps(value))
    with pytest.raises(ValueError, match='registration conflict'):
        provision.provision('one', data)
    assert registry.read_bytes() == before


def test_cli_only_prints_artifact_path(tmp_path):
    data = tmp_path / 'chrome'
    data.mkdir()
    env = dict(os.environ, HOME=str(tmp_path), XDG_CONFIG_HOME=str(tmp_path / 'config'))
    result = subprocess.run([sys.executable, str(SCRIPT), '--profile-id', 'Default', '--user-data-dir', str(data)], env=env, capture_output=True, text=True, check=True)
    artifact = Path(result.stdout.strip())
    assert artifact.is_dir() and result.stderr == ''
    registry = (tmp_path / 'config/gptadmin-extension-bridge/profiles.json').read_text()
    assert all(secret not in result.stdout for secret in json.loads(registry).values())


def test_same_profile_alias_cannot_move_to_another_user_data_root(tmp_path, monkeypatch):
    monkeypatch.setenv('XDG_CONFIG_HOME', str(tmp_path / 'config'))
    first, second = tmp_path / 'chrome', tmp_path / 'chromium'
    first.mkdir()
    second.mkdir()
    provision.provision('Default', first)
    with pytest.raises(ValueError, match='root conflict'):
        provision.provision('Default', second)
    assert not (second / 'NativeMessagingHosts').exists()
