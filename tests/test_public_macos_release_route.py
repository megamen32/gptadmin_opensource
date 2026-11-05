"""Local release contract checks; public CI and publication remain separate gates."""
import hashlib
import io
import json
from pathlib import Path
import struct
import tarfile

import pytest
import yaml

from tools import publish_full_release as release


ROOT = Path(__file__).resolve().parents[1]
OVERLAY = ROOT / '.gitpublic/public/.github/workflows'
WORKFLOWS = OVERLAY if OVERLAY.is_dir() else ROOT / '.github/workflows'
SOURCE = 'a' * 40
PRIVATE = 'b' * 40


def _full_matrix(root: Path) -> None:
    entries, sums = [], []
    for platform, extension in (('windows', 'zip'), ('macos', 'tar.gz'),
                                ('ubuntu', 'tar.gz'), ('android', 'tar.gz')):
        for arch in ('x64', 'arm64'):
            for edition in ('full', 'client'):
                name = f'gptadmin-{platform}-{arch}-{edition}.{extension}'
                path = root / name
                if platform == 'macos' and edition == 'client':
                    cpu = 0x01000007 if arch == 'x64' else 0x0100000C
                    with tarfile.open(path, 'w:gz') as archive:
                        for entry, raw, mode in (
                            ('./bin/shellmcp', b'\xcf\xfa\xed\xfe' + struct.pack('<I', cpu), 0o755),
                            ('./README.txt', f'GPTAdmin 1078 for macos/{arch} (client)\n'.encode(), 0o644),
                        ):
                            info = tarfile.TarInfo(entry); info.size = len(raw); info.mode = mode
                            archive.addfile(info, io.BytesIO(raw))
                else:
                    path.write_bytes(b'non-macos-bundle-fixture')
                digest = hashlib.sha256(path.read_bytes()).hexdigest()
                entries.append({'platform': platform, 'arch': arch, 'edition': edition,
                                'file': name, 'size': path.stat().st_size, 'sha256': digest})
                sums.append(f'{digest}  build/{name}')
    (root / 'gptadmin-checksums.txt').write_text('\n'.join(sums) + '\n')
    (root / 'gptadmin-release-matrix.json').write_text(json.dumps({
        'schema': 'gptadmin.release-matrix/v2', 'build_version': 1078,
        'git_commit': SOURCE, 'artifacts': entries,
    }))


def _node_release_packages(root: Path, source: str = SOURCE,
                           manifest_arch_override: dict[tuple[str, str], str] | None = None) -> list[Path]:
    packages = []
    manifest_arch_override = manifest_arch_override or {}
    for platform in ('linux', 'darwin'):
        for arch in ('amd64', 'arm64'):
            name = f'gptadmin-node-{platform}-{arch}.tar.gz'
            path = root / name
            required = release.node.LINUX_FILES if platform == 'linux' else release.node.DARWIN_FILES
            files = {member: b'node-release-fixture' for member in required}
            if platform == 'linux':
                js = 'public/admin/assets/app.fixture.js'
                css = 'public/admin/assets/app.fixture.css'
                files[release.node.UI_INDEX] = (js.rsplit('/', 1)[-1] + ' '
                                                + css.rsplit('/', 1)[-1]).encode()
                files[js] = b'js-fixture'
                files[css] = b'css-fixture'
                files['bin/gptadmin-node'] = b'\x7fELF-node-fixture'
            else:
                cpu = 0x0100000C if arch == 'arm64' else 0x01000007
                files['bin/gptadmin-node'] = b'\xcf\xfa\xed\xfe' + struct.pack('<I', cpu)
            manifest = {
                'schema': 'gptadmin.node-package/v1', 'platform': platform,
                'arch': manifest_arch_override.get((platform, arch), arch),
                'git_commit': source, 'build_version': 1078, 'source_dirty': False,
                'files': {member: {'sha256': hashlib.sha256(raw).hexdigest(), 'size': len(raw)}
                          for member, raw in files.items()},
            }
            with tarfile.open(path, 'w:gz') as archive:
                for member, raw in [*files.items(), ('manifest.json', json.dumps(manifest).encode())]:
                    info = tarfile.TarInfo(member); info.size = len(raw)
                    archive.addfile(info, io.BytesIO(raw))
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            path.with_name(path.name + '.sha256').write_text(digest + '  ' + path.name + '\n')
            packages.append(path)
    return packages


def test_full_release_rejects_wrong_source_or_tampered_macos_asset(tmp_path: Path) -> None:
    _full_matrix(tmp_path)
    assert len(release.checked_assets(tmp_path, SOURCE, 1078)) == 18
    with pytest.raises(ValueError, match='not bound'):
        release.checked_assets(tmp_path, 'c' * 40, 1078)
    (tmp_path / 'gptadmin-macos-arm64-client.tar.gz').write_bytes(b'tampered')
    with pytest.raises(ValueError, match='digest or size'):
        release.checked_assets(tmp_path, SOURCE, 1078)


def test_full_release_requires_a_published_matching_node_release(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.setattr(release, 'checked_assets', lambda *args: [tmp_path / 'asset'])
    monkeypatch.setattr(release, 'verify_ci_and_node',
                        lambda *args: (_ for _ in ()).throw(ValueError('matching Node release missing')))
    with pytest.raises(ValueError, match='matching Node release missing'):
        release.publish(tmp_path, SOURCE, PRIVATE, 1078, 123, tmp_path)


def test_source_commit_and_ci_must_bind_exact_public_snapshot(monkeypatch, tmp_path: Path) -> None:
    monkeypatch.setattr(release.node, 'public_mirror_commit', lambda *args: SOURCE)
    _node_release_packages(tmp_path)
    node_assets = [path for archive in sorted(tmp_path.glob('gptadmin-node-*.tar.gz'))
                   for path in (archive, archive.with_name(archive.name + '.sha256'))]
    state = {
        'private': PRIVATE,
        'ci_source': SOURCE,
        'node_assets': [{'name': path.name, 'size': path.stat().st_size,
                         'digest': 'sha256:' + release.node.digest(path)} for path in node_assets],
    }
    def gh(*args):
        endpoint = args[1]
        if '/git/commits/' in endpoint:
            value = {'message': 'sanitized\n\nSource-Commit: ' + state['private']}
        elif '/git/ref/heads/main' in endpoint:
            value = {'object': {'sha': SOURCE}}
        elif endpoint.endswith('/actions/runs/123'):
            value = {'name': 'Public Product CI', 'head_sha': state['ci_source'], 'event': 'push',
                     'head_branch': 'main', 'status': 'completed', 'conclusion': 'success'}
        elif '/actions/runs/123/jobs?' in endpoint:
            value = {'jobs': [{'name': name, 'conclusion': 'success'} for name in
                              ('Public product CI gate', 'Verify both macOS ShellMCP release packages')]}
        elif '/releases/tags/' in endpoint:
            tag = f'node-v1078-{SOURCE[:12]}'
            value = {'tag_name': tag, 'draft': False, 'prerelease': False,
                     'assets': state['node_assets']}
        else:
            raise AssertionError(endpoint)
        return json.dumps(value).encode()
    monkeypatch.setattr(release.node, 'gh', gh)
    monkeypatch.setattr(release.node, 'tag_commit', lambda *args, **kwargs: SOURCE)
    release.verify_ci_and_node(release.REPOSITORY, SOURCE, PRIVATE, 1078, 123, tmp_path)
    assert len(state['node_assets']) == 8
    # The complete remote closure includes every archive and checksum, including Darwin.
    state['node_assets'] = state['node_assets'][:-1]
    with pytest.raises(ValueError, match='published digests'):
        release.verify_ci_and_node(release.REPOSITORY, SOURCE, PRIVATE, 1078, 123, tmp_path)
    state['node_assets'] = [{'name': path.name, 'size': path.stat().st_size,
                             'digest': 'sha256:' + release.node.digest(path)} for path in node_assets]
    bad_arch_dir = tmp_path / 'bad-arch'
    bad_arch_dir.mkdir()
    _node_release_packages(bad_arch_dir, manifest_arch_override={('darwin', 'arm64'): 'amd64'})
    bad_arch_assets = [path for archive in sorted(bad_arch_dir.glob('gptadmin-node-*.tar.gz'))
                       for path in (archive, archive.with_name(archive.name + '.sha256'))]
    state['node_assets'] = [{'name': path.name, 'size': path.stat().st_size,
                             'digest': 'sha256:' + release.node.digest(path)} for path in bad_arch_assets]
    with pytest.raises(ValueError, match='manifest does not bind accepted source'):
        release.verify_ci_and_node(release.REPOSITORY, SOURCE, PRIVATE, 1078, 123, bad_arch_dir)
    bad_source_dir = tmp_path / 'bad-source'
    bad_source_dir.mkdir()
    _node_release_packages(bad_source_dir, source='c' * 40)
    bad_source_assets = [path for archive in sorted(bad_source_dir.glob('gptadmin-node-*.tar.gz'))
                         for path in (archive, archive.with_name(archive.name + '.sha256'))]
    state['node_assets'] = [{'name': path.name, 'size': path.stat().st_size,
                             'digest': 'sha256:' + release.node.digest(path)} for path in bad_source_assets]
    with pytest.raises(ValueError, match='manifest does not bind accepted source'):
        release.verify_ci_and_node(release.REPOSITORY, SOURCE, PRIVATE, 1078, 123, bad_source_dir)
    state['node_assets'] = [{'name': path.name, 'size': path.stat().st_size,
                             'digest': 'sha256:' + release.node.digest(path)} for path in node_assets]
    state['private'] = 'd' * 40
    with pytest.raises(ValueError, match='approved private Source-Commit'):
        release.verify_ci_and_node(release.REPOSITORY, SOURCE, PRIVATE, 1078, 123, tmp_path)
    state['private'] = PRIVATE
    state['ci_source'] = 'c' * 40
    with pytest.raises(ValueError, match='exact source push'):
        release.verify_ci_and_node(release.REPOSITORY, SOURCE, PRIVATE, 1078, 123, tmp_path)


def test_public_workflows_run_macos_gate_then_auto_publish_node_and_manual_full_release() -> None:
    ci = yaml.safe_load((WORKFLOWS / 'public-ci.yml').read_text())
    assert 'macos-release-packages' in ci['jobs']['ci-gate']['needs']
    command = next(step['run'] for step in ci['jobs']['macos-release-packages']['steps']
                   if step.get('name') == 'Build and verify both macOS client bundles')
    assert './tools/build.sh macos-client' in command and 'verify_macos_release.py' in command
    assert ci['jobs']['macos-build']['needs'] == 'macos-release-packages'
    mac_steps = ci['jobs']['macos-build']['steps']
    assert any('actions/download-artifact@' in step.get('uses', '') for step in mac_steps)
    assert any('smoke_macos_release.py' in step.get('run', '') for step in mac_steps)
    workflow = yaml.safe_load((WORKFLOWS / 'publish-full-release.yml').read_text())
    trigger = workflow.get('on', workflow.get(True))
    assert set(trigger) == {'workflow_dispatch'}
    assert workflow['permissions']['contents'] == 'read'
    assert workflow['jobs']['publish']['if'] == "github.ref == 'refs/heads/main'"
    assert workflow['jobs']['publish']['permissions']['contents'] == 'write'
    steps = workflow['jobs']['publish']['steps']
    names = [step.get('name') for step in steps]
    assert names.index('Verify public source, CI, Source-Commit and matching Node release') < names.index('Build complete release from the verified public snapshot')
    assert names.index('Build complete release from the verified public snapshot') < names.index('Publish verified assets without changing latest')

    node_workflow = yaml.safe_load((WORKFLOWS / 'publish-node-release.yml').read_text())
    node_trigger = node_workflow.get('on', node_workflow.get(True))
    assert set(node_trigger) == {'workflow_run'}
    assert node_trigger['workflow_run']['workflows'] == ['Public Product CI']
    assert node_workflow['jobs']['build-test-publish']['if'].strip().startswith("github.event.workflow_run.conclusion == 'success'")
    node_steps = node_workflow['jobs']['build-test-publish']['steps']
    assert any('exact source that passed' in step.get('name', '') for step in node_steps)


def test_latest_release_readback_fails_closed_and_returns_stable_tag(monkeypatch) -> None:
    monkeypatch.setattr(release.node, 'gh', lambda *args: json.dumps({
        'data': {'repository': {'latestRelease': {'tagName': 'v1077'}}}
    }).encode())
    assert release.latest_release_tag(release.REPOSITORY) == 'v1077'
    monkeypatch.setattr(release.node, 'gh', lambda *args: json.dumps(
        {'data': {'repository': {'latestRelease': None}}}
    ).encode())
    assert release.latest_release_tag(release.REPOSITORY) is None
    monkeypatch.setattr(release.node, 'gh', lambda *args: (_ for _ in ()).throw(RuntimeError('API unavailable')))
    with pytest.raises(ValueError, match='UNKNOWN'):
        release.latest_release_tag(release.REPOSITORY)
    monkeypatch.setattr(release.node, 'gh', lambda *args: json.dumps({'errors': ['denied']}).encode())
    with pytest.raises(ValueError, match='UNKNOWN'):
        release.latest_release_tag(release.REPOSITORY)
