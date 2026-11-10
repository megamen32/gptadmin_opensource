import base64
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest
import chrome_profile_manager_mcp as cpm
from chrome_profile_manager_mcp import ProfileManager, ProfileManagerError, TOOLS, content_hash, extension_identity


@pytest.mark.parametrize('system,expected', [
    ('Linux', ['.config/google-chrome', '.config/chromium']),
    ('Darwin', ['Library/Application Support/Google/Chrome', 'Library/Application Support/Chromium']),
    ('Windows', ['Google/Chrome/User Data', 'Chromium/User Data']),
])
def test_standard_chrome_and_chromium_roots(system, expected, tmp_path, monkeypatch):
    monkeypatch.setattr(cpm.platform, 'system', lambda: system)
    monkeypatch.setattr(cpm.Path, 'home', classmethod(lambda cls: tmp_path))
    monkeypatch.setenv('LOCALAPPDATA', str(tmp_path))
    monkeypatch.delenv('CHROME_PROFILE_ROOTS', raising=False)
    monkeypatch.delenv('XDG_CONFIG_HOME', raising=False)
    roots = ProfileManager(tmp_path / 'state')._default_roots()
    for suffix in expected:
        assert any(str(root).replace('\\', '/').endswith(suffix) for root in roots)


def test_missing_posix_primitives_keeps_discovery_but_refuses_writes(tmp_path, monkeypatch):
    root = tmp_path / 'chromium'; root.mkdir()
    (root / 'Local State').write_text(json.dumps({'profile': {'info_cache': {'Default': {'name':'Fixture'}}}}))
    manager = ProfileManager(tmp_path / 'state', roots=[root])
    monkeypatch.setattr(cpm, 'fcntl', None)
    assert manager.profile_list()[0]['mode'] == 'unmanaged_unavailable'
    modes = manager.capabilities()
    assert modes['modes']['controlled']['available'] is False
    assert modes['modes']['helper']['available'] is False
    assert modes['modes']['policy']['available'] is False
    assert modes['modes']['unmanaged_unavailable']['available'] is False
    assert modes['backend']['runtime'] == 'not_started'
    with pytest.raises(ProfileManagerError, match='platform'):
        manager.stage('input', 'a'*32, '1.0', 'a'*64)
    with pytest.raises(ProfileManagerError, match='platform'):
        manager.install('controlled', 'unused', None)


def test_darwin_gate_requires_posix_primitives_and_windows_stays_closed(monkeypatch):
    monkeypatch.setattr(cpm.platform, 'system', lambda: 'Darwin')
    assert cpm.platform_lifecycle_status()[0] is True
    monkeypatch.setattr(cpm, 'fcntl', None)
    assert cpm.platform_lifecycle_status() == (False, 'interprocess_lock_unavailable')
    monkeypatch.setattr(cpm.platform, 'system', lambda: 'Windows')
    assert cpm.platform_lifecycle_status()[0] is False


def test_module_import_and_mcp_list_without_fcntl(tmp_path):
    code = '''import builtins, runpy, sys
original = builtins.__import__
def blocked(name, *args, **kwargs):
    if name == "fcntl": raise ImportError("not available")
    return original(name, *args, **kwargs)
builtins.__import__ = blocked
sys.argv = [sys.argv[1]]
runpy.run_path(sys.argv[0], run_name="__main__")
'''
    script = Path(__file__).resolve().parents[1] / 'chrome_profile_manager_mcp.py'
    env = {**os.environ, 'CHROME_PROFILE_MANAGER_STATE':str(tmp_path / 'state'), 'CHROME_PROFILE_ROOTS':str(tmp_path / 'empty')}
    result = subprocess.run([sys.executable, '-c', code, str(script)], input=json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'profile_list','arguments':{}}})+'\n', text=True, capture_output=True, env=env, timeout=10, check=True)
    data = json.loads(result.stdout)['result']['structuredContent']
    assert data['capabilities']['modes']['controlled']['available'] is False
    assert data['profiles'] == []


def test_invalid_tool_arguments_preserve_request_id(tmp_path):
    script = Path(__file__).resolve().parents[1] / 'chrome_profile_manager_mcp.py'
    env = {**os.environ, 'CHROME_PROFILE_MANAGER_STATE':str(tmp_path / 'state'), 'CHROME_PROFILE_ROOTS':str(tmp_path / 'empty')}
    request = {'jsonrpc':'2.0', 'id':42, 'method':'tools/call', 'params':{'name':'profile_reload','arguments':{'expected_version':'1'}}}
    completed = subprocess.run([sys.executable, str(script)], input=json.dumps(request)+'\n', text=True, capture_output=True, env=env, timeout=10, check=True)
    response = json.loads(completed.stdout)
    assert response['id'] == 42
    assert 'error' in response


def test_backend_metadata_is_not_runtime_availability(tmp_path, monkeypatch):
    root = tmp_path / 'package'; entry = root / 'build' / 'src' / 'bin' / 'chrome-devtools-mcp.js'
    entry.parent.mkdir(parents=True); entry.write_text('// fixture')
    (root / 'package.json').write_text(json.dumps({'name':'chrome-devtools-mcp', 'version':'1.12.0'}))
    monkeypatch.setenv('CHROME_DEVTOOLS_MCP_ENTRY', str(entry))
    manager = ProfileManager(tmp_path / 'state', roots=[])
    info = manager.capabilities()
    assert info['backend']['package_metadata'] == 'matched_1.12.0'
    assert info['backend']['runtime'] == 'not_started'
    assert not info['modes']['controlled']['available']
    (root / 'package.json').write_text('[]')
    assert manager.capabilities()['backend']['package_metadata'] == 'invalid_or_unavailable'


@pytest.mark.skipif(cpm.fcntl is None, reason='POSIX lock fixture only')
def test_lock_failure_refuses_operation(tmp_path, monkeypatch):
    manager = ProfileManager(tmp_path / 'state', roots=[])
    def fail(*args): raise OSError('fixture lock failure')
    monkeypatch.setattr(cpm.fcntl, 'flock', fail)
    with pytest.raises(ProfileManagerError, match='lock unavailable'):
        manager.install('controlled', 'unused', None)
    assert not (manager._profile_root('controlled') / 'state.json').exists()


def test_discover_two_isolated_browser_roots(tmp_path):
    chrome = tmp_path / 'chrome'; chromium = tmp_path / 'chromium'
    for root, name in ((chrome, 'Chrome Fixture'), (chromium, 'Chromium Fixture')):
        root.mkdir()
        (root / 'Local State').write_text(json.dumps({'profile': {'info_cache': {'Default': {'name':name}}}}))
    manager = ProfileManager(tmp_path / 'state', roots=[chrome, chromium])
    profiles = manager.profile_list()
    assert {item['name'] for item in profiles} == {'Chrome Fixture', 'Chromium Fixture'}
    assert {item['mode'] for item in profiles} == {'unmanaged_unavailable'}


def test_pure_lifecycle_transition_plans_pending_and_completion():
    from chrome_profile_manager_mcp import lifecycle_transition
    target = {'extension_id':'a'*32, 'version':'1.0', 'digest':'d', 'stage_id':'s', 'name':'A'}
    before = {'extensions': {}}
    planned = lifecycle_transition(before, {'operation':'install', 'target':target, 'expected_version':None})
    assert before == {'extensions': {}}
    assert planned['state']['extensions']['a'*32]['pending'] == target
    kinds = [e['kind'] for e in planned['effects']]
    assert kinds.index('persist') < kinds.index('mutate') < kinds.index('verify_readback') < kinds.index('finish')
    pre = lifecycle_transition(planned['state'], {'operation':'set_phase', 'extension_id':'a'*32, 'phase':'before_mutation'})['state']
    unknown = lifecycle_transition(pre, {'operation':'set_phase', 'extension_id':'a'*32, 'phase':'mutation_unknown'})['state']
    complete = lifecycle_transition(unknown, {'operation':'complete', 'extension_id':'a'*32})
    assert complete['state']['extensions']['a'*32]['current'] == target
    assert complete['state']['extensions']['a'*32]['pending'] is None


def test_pure_transitions_reject_stale_cas_and_unknown_reload():
    from chrome_profile_manager_mcp import lifecycle_transition
    item = {'extension_id':'a'*32, 'version':'1.0', 'digest':'d', 'stage_id':'s', 'name':'A'}
    state = {'extensions': {'a'*32: {'current':item, 'pending':None, 'path':'extension'}}}
    with pytest.raises(ProfileManagerError, match='compare-and-swap'):
        lifecycle_transition(state, {'operation':'reload', 'extension_id':'a'*32, 'expected_version':'2.0'})
    plan = lifecycle_transition(state, {'operation':'reload', 'extension_id':'a'*32, 'expected_version':'1.0'})
    assert plan['state']['extensions']['a'*32]['pending']['operation'] == 'reload'
    unknown = lifecycle_transition(plan['state'], {'operation':'set_phase', 'extension_id':'a'*32, 'phase':'mutation_unknown'})['state']
    reconciliation = lifecycle_transition(unknown, {'operation':'reconcile', 'extension_id':'a'*32})
    assert [x['kind'] for x in reconciliation['effects']] == ['verify_readback', 'verify_active', 'unprovable_reload']


def test_pure_backup_update_rollback_reconcile_and_independent_cas():
    from chrome_profile_manager_mcp import lifecycle_transition
    a = {'extension_id':'a'*32, 'version':'1', 'digest':'a', 'stage_id':'s1', 'name':'A'}
    b = {'extension_id':'b'*32, 'version':'5', 'digest':'b', 'stage_id':'s2', 'name':'B'}
    next_a = {**a, 'version':'2', 'stage_id':'s3'}
    original = {'extensions': {'a'*32:{'current':a, 'pending':None, 'path':'extension'},
                               'b'*32:{'current':b, 'pending':None, 'path':'extensions/b/extension'}}}
    backup = lifecycle_transition(original, {'operation':'backup', 'extension_id':'a'*32, 'expected_version':'1', 'backup_id':'test'})
    assert [x['kind'] for x in backup['effects']] == ['verify_readback', 'verify_active', 'verify_stage', 'write_backup']
    update = lifecycle_transition(original, {'operation':'update', 'target':next_a, 'expected_version':'1'})
    assert update['state']['extensions']['b'*32] == original['extensions']['b'*32]
    assert lifecycle_transition(update['state'], {'operation':'backup', 'extension_id':'b'*32, 'expected_version':'5', 'backup_id':'test'})['result'] == b
    with pytest.raises(ProfileManagerError, match='pending'):
        lifecycle_transition(update['state'], {'operation':'update', 'target':next_a, 'expected_version':'1'})
    pre = lifecycle_transition(update['state'], {'operation':'set_phase', 'extension_id':'a'*32, 'phase':'before_mutation'})['state']
    unknown = lifecycle_transition(pre, {'operation':'set_phase', 'extension_id':'a'*32, 'phase':'mutation_unknown'})['state']
    reconcile = lifecycle_transition(unknown, {'operation':'reconcile', 'extension_id':'a'*32})
    assert [x['kind'] for x in reconcile['effects']] == ['verify_readback', 'verify_active', 'finish']
    completed = lifecycle_transition(unknown, {'operation':'complete', 'extension_id':'a'*32})['state']
    rollback = lifecycle_transition(completed, {'operation':'rollback', 'target':a, 'expected_version':'2', 'extension_id':'a'*32})
    assert rollback['state']['extensions']['a'*32]['pending'] == a
    with pytest.raises(ProfileManagerError, match='identity mismatch'):
        lifecycle_transition(completed, {'operation':'rollback', 'target':a, 'expected_version':'2', 'extension_id':'b'*32})
    assert original['extensions']['a'*32]['current'] == a


def artifact(manager, name, key, version):
    path = manager.artifact_dir / name
    path.mkdir()
    manifest = {'manifest_version': 3, 'name': 'Fixture', 'version': version, 'key': key}
    (path / 'manifest.json').write_text(json.dumps(manifest))
    return manager.profile_stage('managed', name, extension_identity(manifest)[0], version, content_hash(path))


class FakeBackend:
    def __init__(self, root): self.root = root; self.extension = None; self.calls = []
    def tool(self, name, args):
        self.calls.append(name)
        manifest = json.loads((self.root / 'extension' / 'manifest.json').read_text())
        ident, version = extension_identity(manifest)
        self.extension = {'id': ident, 'version': version, 'enabled': True}
        return 'Extension installed. Id: ' + ident
    def call(self, method, args):
        return {'structuredContent': {'extensions': [self.extension] if self.extension else []}}
    def close(self): pass


def test_multiple_extensions_have_separate_cas_and_backup(tmp_path):
    class Many(FakeBackend):
        def __init__(self, root): self.root = root; self.extensions = {}; self.calls = []
        def tool(self, name, args):
            self.calls.append((name, args))
            if name == 'install_extension': path = Path(args['path'])
            else: path = Path(self.extensions[args['id']]['path'])
            manifest = json.loads((path / 'manifest.json').read_text())
            ident, version = extension_identity(manifest)
            self.extensions[ident] = {'id':ident, 'version':version, 'enabled':True, 'path':str(path)}
            return 'Extension installed. Id: ' + ident
        def call(self, method, args): return {'structuredContent': {'extensions': list(self.extensions.values())}}
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=Many)
    key_a = base64.b64encode(b'public-a').decode(); key_b = base64.b64encode(b'public-b').decode()
    a1 = artifact(manager, 'a1', key_a, '1.0'); a2 = artifact(manager, 'a2', key_a, '2.0'); b1 = artifact(manager, 'b1', key_b, '5.0')
    manager.install('managed', a1['stage_id'], None)
    manager.install('managed', b1['stage_id'], None)
    a = a1['extension_id']; b = b1['extension_id']
    backup = manager.backup('managed', '1.0', extension_id=a)
    manager.update('managed', a2['stage_id'], '1.0')
    assert manager.reload('managed', '5.0', extension_id=b)['extension_id'] == b
    assert manager.rollback('managed', backup['backup_id'], '2.0', extension_id=a)['version'] == '1.0'
    assert manager.backends['managed'].extensions[b]['version'] == '5.0'
    with pytest.raises(ProfileManagerError, match='compare-and-swap'): manager.update('managed', a2['stage_id'], '5.0')
    listed = manager.profile_list()
    assert {entry['extension_id'] for entry in listed[0]['extensions']} == {a, b}
    manager.close()


def test_staging_bounded_and_tampering(tmp_path):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public-key').decode()
    stage = artifact(manager, 'a', key, '1.0')
    assert manager._stage(stage['stage_id'])['digest'] == stage['digest']
    with pytest.raises(ProfileManagerError, match='relative'):
        manager.profile_stage('managed', str(tmp_path), stage['extension_id'], '1.0', stage['digest'])
    link = manager.artifact_dir / 'bad'; link.symlink_to(tmp_path, target_is_directory=True)
    with pytest.raises(ProfileManagerError, match='unsafe'):
        manager.profile_stage('managed', 'bad', stage['extension_id'], '1.0', stage['digest'])
    f = manager.stage_dir / stage['stage_id'] / 'content' / 'manifest.json'
    f.chmod(0o600); f.write_text('{}')
    with pytest.raises(ProfileManagerError): manager._stage(stage['stage_id'])


def test_copy_artifact_rejects_many_empty_directories_before_copy(tmp_path, monkeypatch):
    root = tmp_path / 'artifacts'; source = root / 'input'; source.mkdir(parents=True)
    for n in range(6): (source / str(n)).mkdir()
    monkeypatch.setattr(cpm, 'MAX_ARTIFACT_ENTRIES', 5, raising=False)
    destination = tmp_path / 'destination'
    with pytest.raises(ProfileManagerError, match='entries'):
        cpm.copy_artifact(root, 'input', destination)
    assert not destination.exists()


def test_copy_artifact_rejects_excess_depth_before_copy(tmp_path, monkeypatch):
    root = tmp_path / 'artifacts'; source = root / 'input'; source.mkdir(parents=True)
    (source / 'a' / 'b' / 'c').mkdir(parents=True)
    monkeypatch.setattr(cpm, 'MAX_ARTIFACT_DEPTH', 2, raising=False)
    destination = tmp_path / 'destination'
    with pytest.raises(ProfileManagerError, match='depth'):
        cpm.copy_artifact(root, 'input', destination)
    assert not destination.exists()


def test_copy_artifact_checks_limits_again_when_source_changes(tmp_path, monkeypatch):
    root = tmp_path / 'artifacts'; source = root / 'input'; source.mkdir(parents=True)
    (source / 'one').mkdir()
    monkeypatch.setattr(cpm, 'MAX_ARTIFACT_ENTRIES', 1)
    original = cpm.os.scandir
    count = 0
    def changed(fd):
        nonlocal count
        count += 1
        if count == 3: (source / 'new').mkdir()
        return original(fd)
    # The wrapper itself is not listed in os.supports_fd; the actual syscall is unchanged.
    monkeypatch.setattr(cpm, 'require_lifecycle_platform', lambda: None)
    monkeypatch.setattr(cpm.os, 'scandir', changed)
    with pytest.raises(ProfileManagerError, match='entries'):
        cpm.copy_artifact(root, 'input', tmp_path / 'destination')


@pytest.mark.parametrize('kind', ['nested_symlink', 'hardlink', 'bytes', 'files'])
def test_copy_artifact_preflight_preserves_safety_limits(tmp_path, monkeypatch, kind):
    root = tmp_path / 'artifacts'; source = root / 'input'; source.mkdir(parents=True)
    if kind == 'nested_symlink':
        (source / 'nested').mkdir()
        (source / 'nested' / 'link').symlink_to(tmp_path, target_is_directory=True)
    elif kind == 'hardlink':
        other = tmp_path / 'other'; other.write_text('fixture')
        os.link(other, source / 'hardlink')
    else:
        (source / 'file').write_text('fixture')
        monkeypatch.setattr(cpm, 'MAX_ARTIFACT_BYTES' if kind == 'bytes' else 'MAX_ARTIFACT_FILES', 0)
    destination = tmp_path / 'destination'
    with pytest.raises(ProfileManagerError):
        cpm.copy_artifact(root, 'input', destination)
    assert not destination.exists()


def test_lifecycle_cas_backup_rollback_and_readback(tmp_path):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public-key').decode()
    one = artifact(manager, 'a', key, '1.0'); two = artifact(manager, 'b', key, '2.0')
    assert manager.install('managed', one['stage_id'], None)['readback'] == 'verified'
    backup = manager.backup('managed', '1.0')
    with pytest.raises(ProfileManagerError, match='compare-and-swap'): manager.update('managed', two['stage_id'], '0')
    assert manager.update('managed', two['stage_id'], '1.0')['version'] == '2.0'
    assert manager.reload('managed', '2.0')['readback'] == 'verified'
    assert manager.rollback('managed', backup['backup_id'], '2.0')['version'] == '1.0'
    assert len(manager.backends) == 1
    manager.close()


def test_structured_readback_ignores_spoofed_names(tmp_path):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    item = {'extension_id':'a'*32, 'version':'1'}
    class Spoof(FakeBackend):
        def call(self, *args): return {'structuredContent': {'extensions': [{'id':'b'*32, 'version':'1', 'enabled':True, 'name':'x\nid='+'a'*32+' "x" v1 Enabled'}]}}
    manager.backend_factory = Spoof
    manager._profile_root('managed').mkdir(parents=True)
    with pytest.raises(ProfileManagerError, match='mismatch'): manager._readback('managed', item)
    manager.close()


def test_unknown_mutation_is_read_back_without_replay(tmp_path):
    class Unknown(FakeBackend):
        def tool(self, name, args):
            result = super().tool(name, args)
            if name == 'install_extension': raise ProfileManagerError('timeout; outcome unknown')
            return result
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=Unknown)
    stage = artifact(manager, 'a', base64.b64encode(b'public').decode(), '1.0')
    assert manager.install('managed', stage['stage_id'], None)['readback'] == 'verified'
    assert manager.backends['managed'].calls == ['install_extension']
    with pytest.raises(ProfileManagerError, match='distinct version'):
        manager.update('managed', stage['stage_id'], '1.0')
    manager.close()


def test_unknown_then_reconcile_reads_back_without_repeating_mutation(tmp_path):
    class Uncertain(FakeBackend):
        def __init__(self, root): super().__init__(root); self.fail_readback = False
        def tool(self, name, args):
            result = super().tool(name, args)
            if name == 'install_extension':
                self.fail_readback = True
                raise ProfileManagerError('outcome unknown')
            return result
        def call(self, method, args):
            if self.fail_readback:
                self.fail_readback = False
                raise ProfileManagerError('readback unavailable')
            return super().call(method, args)
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=Uncertain)
    stage = artifact(manager, 'a', base64.b64encode(b'public').decode(), '1.0')
    with pytest.raises(ProfileManagerError, match='readback unavailable'):
        manager.install('managed', stage['stage_id'], None)
    assert manager._profile_state('managed')['extensions'][stage['extension_id']]['pending']
    assert manager.reconcile('managed', stage['extension_id'])['readback'] == 'verified'
    assert manager.backends['managed'].calls.count('install_extension') == 1
    manager.close()


@pytest.mark.parametrize('failure_point', ['before_activate', 'during_activate', 'before_mutation'])
def test_crash_before_browser_mutation_recovers_previous_artifact(tmp_path, monkeypatch, failure_point):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public').decode()
    one = artifact(manager, 'one', key, '1.0'); two = artifact(manager, 'two', key, '2.0')
    manager.install('managed', one['stage_id'], None)
    backend = manager.backends['managed']; backend.calls.clear()
    active = manager._profile_root('managed') / 'extension'
    original_replace = cpm.os.replace
    original_save = manager._save_profile
    def interrupt_replace(src, dst):
        src, dst = Path(src), Path(dst)
        if failure_point == 'before_activate' and src == active and dst.name.startswith('previous-'):
            raise OSError('crash before activate')
        if failure_point == 'during_activate' and src.name.startswith('candidate-') and dst == active:
            raise OSError('crash during activate')
        return original_replace(src, dst)
    def interrupt_save(profile_id, state):
        item = state['extensions'][one['extension_id']]
        if failure_point == 'before_mutation' and item.get('phase') == 'mutation_unknown':
            raise OSError('crash before browser mutation')
        return original_save(profile_id, state)
    with monkeypatch.context() as patch:
        patch.setattr(cpm.os, 'replace', interrupt_replace)
        patch.setattr(manager, '_save_profile', interrupt_save)
        with pytest.raises(OSError, match='crash'):
            manager.update('managed', two['stage_id'], '1.0')
    entry = manager._profile_state('managed')['extensions'][one['extension_id']]
    assert entry['phase'] == ('before_mutation' if failure_point == 'before_mutation' else 'before_activate')
    recovered = manager.reconcile('managed', one['extension_id'])
    assert recovered['status'] == 'pre_mutation_recovered'
    assert content_hash(active) == one['digest']
    assert manager._profile_state('managed')['extensions'][one['extension_id']]['pending'] is None
    assert backend.calls == []
    assert manager.update('managed', two['stage_id'], '1.0')['version'] == '2.0'
    manager.close()


def test_legacy_unknown_pending_never_treated_as_pre_mutation(tmp_path):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public').decode()
    one = artifact(manager, 'one', key, '1.0'); two = artifact(manager, 'two', key, '2.0')
    manager.install('managed', one['stage_id'], None)
    state = manager._profile_state('managed'); entry = state['extensions'][one['extension_id']]
    entry['pending'] = {k: two[k] for k in ('extension_id', 'version', 'digest', 'name', 'stage_id')}
    entry.pop('phase', None)
    manager._save_profile('managed', state)
    with pytest.raises(ProfileManagerError, match='readback mismatch'):
        manager.reconcile('managed', one['extension_id'])
    assert manager._profile_state('managed')['extensions'][one['extension_id']]['pending'] is not None
    manager.close()


def test_mutation_unknown_never_reverted_on_old_readback(tmp_path, monkeypatch):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public').decode()
    one = artifact(manager, 'one', key, '1.0'); two = artifact(manager, 'two', key, '2.0')
    manager.install('managed', one['stage_id'], None)
    backend = manager.backends['managed']; original_tool = backend.tool
    def crash(name, args):
        if name == 'reload_extension': raise OSError('crash at browser mutation')
        return original_tool(name, args)
    with monkeypatch.context() as patch:
        patch.setattr(backend, 'tool', crash)
        with pytest.raises(OSError, match='crash'):
            manager.update('managed', two['stage_id'], '1.0')
    state = manager._profile_state('managed')['extensions'][one['extension_id']]
    assert state['phase'] == 'mutation_unknown'
    with pytest.raises(ProfileManagerError, match='readback mismatch'):
        manager.reconcile('managed', one['extension_id'])
    assert content_hash(manager._profile_root('managed') / 'extension') == two['digest']
    assert manager._profile_state('managed')['extensions'][one['extension_id']]['phase'] == 'mutation_unknown'
    manager.close()


def test_pre_mutation_recovery_of_first_install_removes_pending_entry(tmp_path, monkeypatch):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    stage = artifact(manager, 'one', base64.b64encode(b'public').decode(), '1.0')
    original_save = manager._save_profile
    def interrupt(profile_id, state):
        if state['extensions'][stage['extension_id']].get('phase') == 'mutation_unknown':
            raise OSError('crash before first browser mutation')
        return original_save(profile_id, state)
    with monkeypatch.context() as patch:
        patch.setattr(manager, '_save_profile', interrupt)
        with pytest.raises(OSError, match='crash'): manager.install('managed', stage['stage_id'], None)
    assert manager.reconcile('managed', stage['extension_id'])['status'] == 'pre_mutation_recovered'
    assert not (manager._profile_root('managed') / 'extension').exists()
    assert stage['extension_id'] not in manager._profile_state('managed')['extensions']
    assert manager.install('managed', stage['stage_id'], None)['readback'] == 'verified'
    manager.close()


def test_interrupted_pre_mutation_recovery_is_repeatable(tmp_path, monkeypatch):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public').decode()
    one = artifact(manager, 'one', key, '1.0'); two = artifact(manager, 'two', key, '2.0')
    manager.install('managed', one['stage_id'], None)
    original_save = manager._save_profile
    def interrupt_save(profile_id, state):
        if state['extensions'][one['extension_id']].get('phase') == 'mutation_unknown':
            raise OSError('crash before mutation')
        return original_save(profile_id, state)
    with monkeypatch.context() as patch:
        patch.setattr(manager, '_save_profile', interrupt_save)
        with pytest.raises(OSError, match='crash'): manager.update('managed', two['stage_id'], '1.0')
    original_replace = cpm.os.replace
    def interrupt_recovery(src, dst):
        if Path(src).name.startswith('recovery-'): raise OSError('crash during recovery')
        return original_replace(src, dst)
    with monkeypatch.context() as patch:
        patch.setattr(cpm.os, 'replace', interrupt_recovery)
        with pytest.raises(OSError, match='crash during recovery'):
            manager.reconcile('managed', one['extension_id'])
    assert manager._profile_state('managed')['extensions'][one['extension_id']]['phase'] == 'before_mutation'
    assert manager.reconcile('managed', one['extension_id'])['status'] == 'pre_mutation_recovered'
    assert content_hash(manager._profile_root('managed') / 'extension') == one['digest']
    manager.close()


def test_pre_mutation_recovery_refuses_unknown_active_content(tmp_path, monkeypatch):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    key = base64.b64encode(b'public').decode()
    one = artifact(manager, 'one', key, '1.0'); two = artifact(manager, 'two', key, '2.0')
    manager.install('managed', one['stage_id'], None)
    original_save = manager._save_profile
    def interrupt(profile_id, state):
        if state['extensions'][one['extension_id']].get('phase') == 'mutation_unknown': raise OSError('crash')
        return original_save(profile_id, state)
    with monkeypatch.context() as patch:
        patch.setattr(manager, '_save_profile', interrupt)
        with pytest.raises(OSError, match='crash'): manager.update('managed', two['stage_id'], '1.0')
    manifest = manager._profile_root('managed') / 'extension' / 'manifest.json'
    manifest.chmod(0o600); manifest.write_text('unexpected content')
    with pytest.raises(ProfileManagerError, match='integrity'):
        manager.reconcile('managed', one['extension_id'])
    assert manifest.read_text() == 'unexpected content'
    assert manager._profile_state('managed')['extensions'][one['extension_id']]['phase'] == 'before_mutation'
    manager.close()


def test_recovery_after_other_extension_started_new_backend(tmp_path, monkeypatch):
    class Many(FakeBackend):
        def __init__(self, root): self.root = root; self.extensions = {}; self.calls = []
        def tool(self, name, args):
            self.calls.append(name)
            path = Path(args['path']) if name == 'install_extension' else Path(self.extensions[args['id']]['path'])
            manifest = json.loads((path / 'manifest.json').read_text())
            ident, version = extension_identity(manifest)
            self.extensions[ident] = {'id':ident, 'version':version, 'enabled':True, 'path':str(path)}
            return 'Extension installed. Id: ' + ident
        def call(self, method, args): return {'structuredContent': {'extensions':list(self.extensions.values())}}
    state_dir = tmp_path / 'state'
    manager = ProfileManager(state_dir, roots=[], backend_factory=Many)
    key_a = base64.b64encode(b'a').decode(); key_b = base64.b64encode(b'b').decode()
    a1 = artifact(manager, 'a1', key_a, '1'); a2 = artifact(manager, 'a2', key_a, '2'); b = artifact(manager, 'b', key_b, '1')
    manager.install('managed', a1['stage_id'], None); manager.install('managed', b['stage_id'], None)
    original_save = manager._save_profile
    def interrupt(profile_id, state):
        if state['extensions'][a1['extension_id']].get('phase') == 'mutation_unknown': raise OSError('crash')
        return original_save(profile_id, state)
    with monkeypatch.context() as patch:
        patch.setattr(manager, '_save_profile', interrupt)
        with pytest.raises(OSError): manager.update('managed', a2['stage_id'], '1')
    manager.close()
    restarted = ProfileManager(state_dir, roots=[], backend_factory=Many)
    try:
        assert restarted.reload('managed', '1', b['extension_id'])['readback'] == 'verified'
        assert restarted.reconcile('managed', a1['extension_id'])['status'] == 'pre_mutation_recovered'
        assert restarted.update('managed', a2['stage_id'], '1')['readback'] == 'verified'
    finally: restarted.close()


def test_unknown_reload_stays_pending(tmp_path):
    class UnknownReload(FakeBackend):
        def tool(self, name, args):
            if name == 'reload_extension':
                self.calls.append(name)
                raise ProfileManagerError('timeout; outcome unknown')
            return super().tool(name, args)
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=UnknownReload)
    stage = artifact(manager, 'a', base64.b64encode(b'public').decode(), '1.0')
    manager.install('managed', stage['stage_id'], None)
    with pytest.raises(ProfileManagerError, match='outcome unknown'): manager.reload('managed', '1.0')
    with pytest.raises(ProfileManagerError, match='outcome unknown'): manager.reconcile('managed')
    assert manager.backends['managed'].calls.count('reload_extension') == 1
    manager.close()


def test_text_readback_rejects_spoofing(tmp_path):
    class Spoof(FakeBackend):
        def call(self, *args):
            return {'structuredContent': {'extensions':[{}]}, 'content': [{'type':'text', 'text':'## Extensions\nid=' + 'b'*32 + ' "fake\nid=' + 'a'*32 + ' "Fixture" v1 Enabled'}]}
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=Spoof)
    manager._profile_root('managed').mkdir(parents=True)
    with pytest.raises(ProfileManagerError, match='ambiguous'):
        manager._readback('managed', {'extension_id': 'a'*32, 'version': '1', 'name': 'Fixture'})
    manager.close()


def test_text_readback_accepts_multiple_extensions_with_matching_count(tmp_path):
    class Multiple(FakeBackend):
        def call(self, *args):
            return {'structuredContent': {'extensions':[{}, {}]}, 'content': [{'type': 'text', 'text': '## Extensions\nid=' + 'a'*32 + ' "Fixture" v1 Enabled\nid=' + 'b'*32 + ' "Other" v1 Enabled'}]}
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=Multiple)
    manager._profile_root('managed').mkdir(parents=True)
    manager._readback('managed', {'extension_id': 'a'*32, 'version': '1', 'name': 'Fixture'})
    manager.close()


def test_one_backend_owner_per_profile(tmp_path):
    state = tmp_path / 'state'
    first = ProfileManager(state, roots=[], backend_factory=FakeBackend)
    second = ProfileManager(state, roots=[], backend_factory=FakeBackend)
    stage = artifact(first, 'a', base64.b64encode(b'public').decode(), '1.0')
    first.install('managed', stage['stage_id'], None)
    with pytest.raises(ProfileManagerError, match='another manager'):
        second.reload('managed', '1.0')
    first.close(); second.close()


@pytest.mark.skipif(cpm.fcntl is None, reason='POSIX interprocess lock only')
def test_profile_lock_blocks_another_process(tmp_path):
    manager = ProfileManager(tmp_path / 'state', roots=[])
    script = '''import fcntl, sys
with open(sys.argv[1], "a") as handle:
    try: fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError: sys.exit(75)
'''
    with manager._locked('managed'):
        lock = manager._profile_root('managed') / '.lock'
        blocked = subprocess.run([sys.executable, '-c', script, str(lock)], timeout=10, check=False)
        assert blocked.returncode == 75
    released = subprocess.run([sys.executable, '-c', script, str(lock)], timeout=10, check=False)
    assert released.returncode == 0


def test_legacy_single_extension_state_is_preserved(tmp_path):
    manager = ProfileManager(tmp_path / 'state', roots=[], backend_factory=FakeBackend)
    stage = artifact(manager, 'a', base64.b64encode(b'public').decode(), '1.0')
    manager.install('managed', stage['stage_id'], None)
    state_file = manager._profile_root('managed') / 'state.json'
    entry = json.loads(state_file.read_text())['extensions'][stage['extension_id']]
    state_file.write_text(json.dumps({'current':entry['current'], 'pending':None}))
    assert manager.reload('managed', '1.0')['extension_id'] == stage['extension_id']
    assert manager._profile_state('managed')['extensions'][stage['extension_id']]['path'] == 'extension'
    manager.close()


def test_normal_profile_is_unmanaged(tmp_path):
    root = tmp_path / 'chrome'; root.mkdir()
    (root / 'Local State').write_text(json.dumps({'profile': {'info_cache': {'Default': {'name': 'Alice'}}}}))
    manager = ProfileManager(tmp_path / 'state', roots=[root])
    assert manager.profile_list()[0]['mode'] == 'unmanaged_unavailable'
    with pytest.raises(ProfileManagerError, match='unmanaged_unavailable'):
        manager.install('default', 'invalid', None)
    tool = next(x for x in TOOLS if x['name'] == 'profile_reload')
    assert 'backend_command' not in tool['inputSchema']['properties']


def test_real_recovery_after_manager_restart(tmp_path, monkeypatch):
    if not os.environ.get('CHROME_MANAGER_REAL_TEST'): pytest.skip('explicit isolated Chrome test only')
    private = tmp_path / 'key.pem'; public = tmp_path / 'public.der'
    subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048', '-out', str(private)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['openssl', 'pkey', '-in', str(private), '-pubout', '-outform', 'DER', '-out', str(public)], check=True)
    manager = ProfileManager(tmp_path / 'state', roots=[])
    key = base64.b64encode(public.read_bytes()).decode()
    one = artifact(manager, 'one', key, '1.0'); two = artifact(manager, 'two', key, '2.0')
    try:
        manager.install('managed', one['stage_id'], None)
        original_save = manager._save_profile
        def interrupt(profile_id, state):
            if state['extensions'][one['extension_id']].get('phase') == 'mutation_unknown':
                raise OSError('simulated process crash')
            return original_save(profile_id, state)
        with monkeypatch.context() as patch:
            patch.setattr(manager, '_save_profile', interrupt)
            with pytest.raises(OSError, match='crash'): manager.update('managed', two['stage_id'], '1.0')
    finally: manager.close()
    restarted = ProfileManager(tmp_path / 'state', roots=[])
    try:
        assert restarted.reconcile('managed', one['extension_id'])['status'] == 'pre_mutation_recovered'
        assert restarted.update('managed', two['stage_id'], '1.0')['readback'] == 'verified'
    finally: restarted.close()


def test_real_chrome_isolated(tmp_path, monkeypatch):
    if not os.environ.get('CHROME_MANAGER_REAL_TEST'): pytest.skip('explicit isolated Chrome test only')
    private = tmp_path / 'key.pem'; public = tmp_path / 'public.der'
    subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048', '-out', str(private)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['openssl', 'pkey', '-in', str(private), '-pubout', '-outform', 'DER', '-out', str(public)], check=True)
    key = base64.b64encode(public.read_bytes()).decode()
    foreign_private = tmp_path / 'foreign-key.pem'; foreign_public = tmp_path / 'foreign-public.der'
    subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048', '-out', str(foreign_private)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['openssl', 'pkey', '-in', str(foreign_private), '-pubout', '-outform', 'DER', '-out', str(foreign_public)], check=True)
    foreign_manifest = {'manifest_version':3, 'name':'Independent Neighbor', 'version':'3.0', 'key':base64.b64encode(foreign_public.read_bytes()).decode()}
    foreign_id = extension_identity(foreign_manifest)[0]
    manager = ProfileManager(tmp_path / 'state', roots=[])
    try:
        one = artifact(manager, 'a', key, '1.0'); two = artifact(manager, 'b', key, '2.0')
        assert manager.install('managed', one['stage_id'], None)['readback'] == 'verified'
        foreign_dir = manager._profile_root('managed') / 'foreign-fixture'; foreign_dir.mkdir()
        (foreign_dir / 'manifest.json').write_text(json.dumps(foreign_manifest))
        manager._backend('managed').tool('install_extension', {'path':str(foreign_dir)})
        manager._readback('managed', {'extension_id':foreign_id, 'version':'3.0'})
        backup = manager.backup('managed', '1.0')
        original_save = manager._save_profile
        def interrupt_before_mutation(profile_id, state):
            entry = state['extensions'][one['extension_id']]
            if entry.get('phase') == 'mutation_unknown' and entry['pending']['version'] == '2.0':
                raise OSError('isolated crash before browser mutation')
            return original_save(profile_id, state)
        with monkeypatch.context() as patch:
            patch.setattr(manager, '_save_profile', interrupt_before_mutation)
            with pytest.raises(OSError, match='isolated crash'):
                manager.update('managed', two['stage_id'], '1.0')
        assert manager.reconcile('managed', one['extension_id'])['status'] == 'pre_mutation_recovered'
        manager._readback('managed', one)
        manager._readback('managed', {'extension_id':foreign_id, 'version':'3.0'})
        assert manager.update('managed', two['stage_id'], '1.0')['version'] == '2.0'
        assert manager.reload('managed', '2.0')['readback'] == 'verified'
        assert manager.rollback('managed', backup['backup_id'], '2.0')['version'] == '1.0'
        manager._readback('managed', {'extension_id':foreign_id, 'version':'3.0'})
    finally: manager.close()
    restarted = ProfileManager(tmp_path / 'state', roots=[])
    try:
        assert restarted.reload('managed', '1.0')['readback'] == 'verified'
    finally: restarted.close()


def test_real_mcp_stdio_consumer(tmp_path):
    if not os.environ.get('CHROME_MANAGER_REAL_TEST'): pytest.skip('explicit isolated Chrome test only')
    private = tmp_path / 'key.pem'; public = tmp_path / 'public.der'
    subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048', '-out', str(private)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['openssl', 'pkey', '-in', str(private), '-pubout', '-outform', 'DER', '-out', str(public)], check=True)
    key = base64.b64encode(public.read_bytes()).decode()
    private_b = tmp_path / 'key-b.pem'; public_b = tmp_path / 'public-b.der'
    subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048', '-out', str(private_b)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['openssl', 'pkey', '-in', str(private_b), '-pubout', '-outform', 'DER', '-out', str(public_b)], check=True)
    key_b = base64.b64encode(public_b.read_bytes()).decode()
    state = tmp_path / 'state'
    producer = ProfileManager(state, roots=[])
    source = producer.artifact_dir / 'input'; source.mkdir()
    manifest = {'manifest_version': 3, 'name': 'Fixture', 'version': '1.0', 'key': key}
    (source / 'manifest.json').write_text(json.dumps(manifest))
    source2 = producer.artifact_dir / 'input2'; source2.mkdir()
    (source2 / 'manifest.json').write_text(json.dumps({**manifest, 'version':'2.0'}))
    source_b = producer.artifact_dir / 'input-b'; source_b.mkdir()
    manifest_b = {**manifest, 'name':'Neighbor', 'version':'5.0', 'key':key_b}
    (source_b / 'manifest.json').write_text(json.dumps(manifest_b))
    ident, _ = extension_identity(manifest)
    ident_b, _ = extension_identity(manifest_b)
    env = {**os.environ, 'CHROME_PROFILE_MANAGER_STATE': str(state), 'CHROME_PROFILE_ROOTS': str(tmp_path / 'empty')}
    server = subprocess.Popen([sys.executable, str(Path(__file__).resolve().parents[1] / 'chrome_profile_manager_mcp.py')], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, env=env)
    def request(number, method, params):
        server.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': number, 'method': method, 'params': params}) + '\n'); server.stdin.flush()
        result = json.loads(server.stdout.readline())
        assert result['id'] == number and 'error' not in result, result
        return result['result']
    try:
        assert request(1, 'initialize', {'protocolVersion':'2026-07-28'})['protocolVersion'] == '2025-06-18'
        initial_caps = request(101, 'tools/call', {'name':'profile_list', 'arguments':{}})['structuredContent']['capabilities']
        assert initial_caps['backend']['runtime'] == 'not_started'
        assert initial_caps['modes']['controlled']['available'] is False
        stage = request(2, 'tools/call', {'name':'profile_stage', 'arguments':{'profile_id':'controlled', 'source_path':'input', 'extension_id':ident, 'version':'1.0', 'digest':content_hash(source)}})['structuredContent']
        installed = request(3, 'tools/call', {'name':'profile_install', 'arguments':{'profile_id':'controlled', 'stage_id':stage['stage_id']}})['structuredContent']
        assert installed['extension_id'] == ident and installed['readback'] == 'verified'
        active_caps = request(102, 'tools/call', {'name':'profile_list', 'arguments':{}})['structuredContent']['capabilities']
        assert active_caps['backend']['runtime'] == 'verified_current_session'
        assert active_caps['modes']['controlled']['available'] is True
        backup = request(4, 'tools/call', {'name':'profile_backup', 'arguments':{'profile_id':'controlled', 'expected_version':'1.0'}})['structuredContent']
        stage2 = request(5, 'tools/call', {'name':'profile_stage', 'arguments':{'profile_id':'controlled', 'source_path':'input2', 'extension_id':ident, 'version':'2.0', 'digest':content_hash(source2)}})['structuredContent']
        updated = request(6, 'tools/call', {'name':'profile_update', 'arguments':{'profile_id':'controlled', 'stage_id':stage2['stage_id'], 'expected_version':'1.0'}})['structuredContent']
        assert updated['version'] == '2.0' and updated['readback'] == 'verified'
        result = request(7, 'tools/call', {'name':'profile_reload', 'arguments':{'profile_id':'controlled', 'expected_version':'2.0'}})
        assert result['structuredContent']['readback'] == 'verified'
        rollback = request(8, 'tools/call', {'name':'profile_rollback', 'arguments':{'profile_id':'controlled', 'backup_id':backup['backup_id'], 'expected_version':'2.0'}})['structuredContent']
        assert rollback['version'] == '1.0' and rollback['extension_id'] == ident and rollback['readback'] == 'verified'
        stage_b = request(9, 'tools/call', {'name':'profile_stage', 'arguments':{'profile_id':'controlled', 'source_path':'input-b', 'extension_id':ident_b, 'version':'5.0', 'digest':content_hash(source_b)}})['structuredContent']
        assert request(10, 'tools/call', {'name':'profile_install', 'arguments':{'profile_id':'controlled', 'stage_id':stage_b['stage_id']}})['structuredContent']['extension_id'] == ident_b
        profiles = request(11, 'tools/call', {'name':'profile_list', 'arguments':{}})['structuredContent']['profiles']
        assert {x['extension_id'] for x in profiles[0]['extensions']} == {ident, ident_b}
        assert request(12, 'tools/call', {'name':'profile_update', 'arguments':{'profile_id':'controlled', 'stage_id':stage2['stage_id'], 'expected_version':'1.0'}})['structuredContent']['version'] == '2.0'
        assert request(13, 'tools/call', {'name':'profile_backup', 'arguments':{'profile_id':'controlled', 'extension_id':ident_b, 'expected_version':'5.0'}})['structuredContent']['extension_id'] == ident_b
        assert request(14, 'tools/call', {'name':'profile_reload', 'arguments':{'profile_id':'controlled', 'extension_id':ident, 'expected_version':'2.0'}})['structuredContent']['readback'] == 'verified'
        assert request(15, 'tools/call', {'name':'profile_rollback', 'arguments':{'profile_id':'controlled', 'extension_id':ident, 'backup_id':backup['backup_id'], 'expected_version':'2.0'}})['structuredContent']['version'] == '1.0'
        assert request(16, 'tools/call', {'name':'profile_reload', 'arguments':{'profile_id':'controlled', 'extension_id':ident_b, 'expected_version':'5.0'}})['structuredContent']['readback'] == 'verified'
        server.stdin.close(); server.wait(timeout=10)
        server = subprocess.Popen([sys.executable, str(Path(__file__).resolve().parents[1] / 'chrome_profile_manager_mcp.py')], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, env=env)
        request(17, 'initialize', {'protocolVersion':'2025-06-18'})
        assert request(18, 'tools/call', {'name':'profile_backup', 'arguments':{'profile_id':'controlled', 'extension_id':ident, 'expected_version':'1.0'}})['structuredContent']['extension_id'] == ident
        assert request(19, 'tools/call', {'name':'profile_backup', 'arguments':{'profile_id':'controlled', 'extension_id':ident_b, 'expected_version':'5.0'}})['structuredContent']['extension_id'] == ident_b
    finally:
        server.stdin.close(); server.wait(timeout=10)
