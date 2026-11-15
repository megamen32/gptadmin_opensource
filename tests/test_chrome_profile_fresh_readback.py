"""Fresh CDP extension readback; real test uses only disposable Chrome state."""
import json
import hashlib
import os
import re
import subprocess
import time

import pytest


def test_rejects_changed_bundle_before_launch(tmp_path, monkeypatch):
    from chrome_profile_manager_mcp import Backend, ProfileManagerError
    package = tmp_path / 'node_modules/chrome-devtools-mcp'
    entry = package / 'build/src/bin/chrome-devtools-mcp.js'
    entry.parent.mkdir(parents=True)
    entry.write_text('')
    (package / 'package.json').write_text(json.dumps({'name': 'chrome-devtools-mcp', 'version': '1.12.0'}))
    bundle = package / 'build/src/third_party/index.js'
    bundle.parent.mkdir()
    bundle.write_text('unexpected')
    monkeypatch.setenv('CHROME_DEVTOOLS_MCP_ENTRY', str(entry))
    with pytest.raises(ProfileManagerError, match='bundle mismatch'):
        Backend(tmp_path / 'profile')
    assert bundle.read_text() == 'unexpected'
    assert not (tmp_path / 'profile').exists()


def test_real_bridge_toggle_readback(tmp_path, monkeypatch):
    if not os.environ.get('CHROME_FRESH_REAL_TEST'):
        pytest.skip('requires disposable Chrome 152 and pinned MCP 1.12')
    from chrome_profile_manager_mcp import Backend, ProfileManager, bridge_call
    from chrome_profile_manager_bridge import provision
    assert subprocess.check_output(['/usr/bin/google-chrome', '--version'], text=True).startswith('Google Chrome 152.')
    monkeypatch.setenv('HOME', str(tmp_path))
    monkeypatch.setenv('XDG_CONFIG_HOME', str(tmp_path / 'config'))
    (tmp_path / 'chrome').mkdir()
    artifact = provision.provision('qa', tmp_path / 'chrome')
    qa = tmp_path / 'qa-extension'
    qa.mkdir()
    (qa / 'manifest.json').write_text(json.dumps({'manifest_version': 3, 'name': 'Fresh QA', 'version': '1.0'}))
    original_bundle = __import__('pathlib').Path(os.environ['CHROME_DEVTOOLS_MCP_ENTRY']).resolve().parents[1] / 'third_party/index.js'
    original_hash = hashlib.sha256(original_bundle.read_bytes()).hexdigest()
    backend = Backend(tmp_path)
    stage = __import__('pathlib').Path(backend.package_stage.name)
    assert stage.is_dir()
    try:
        backend.tool('install_extension', {'path': str(artifact)})
        out = backend.tool('install_extension', {'path': str(qa)})
        qa_id = re.search(r'Extension installed. Id: ([a-p]{32})', out).group(1)
        manager = ProfileManager(tmp_path / 'manager', roots=[])
        monkeypatch.setattr(manager, '_backend', lambda _: backend)
        monkeypatch.setattr(manager, '_mark_runtime_verified', lambda _: None)
        deadline = time.monotonic() + 15
        while True:
            try:
                bridge_call('qa', 'get', qa_id)
                break
            except Exception:
                if time.monotonic() > deadline:
                    raise
                time.sleep(.1)
        def observed():
            return next(x for x in manager._listing('qa') if x['id'] == qa_id)['enabled']
        assert observed() is True
        assert bridge_call('qa', 'disable', qa_id)['enabled'] is False
        assert observed() is False
        assert bridge_call('qa', 'enable', qa_id)['enabled'] is True
        assert observed() is True
    finally:
        backend.close()
    assert not stage.exists()
    assert hashlib.sha256(original_bundle.read_bytes()).hexdigest() == original_hash
