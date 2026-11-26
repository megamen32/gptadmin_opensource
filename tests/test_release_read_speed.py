"""Streaming archive checks and publication barriers around concurrent reads."""
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import threading

import pytest

from tools import publish_full_release as full
from tools import publish_node_release as node

_spec = importlib.util.spec_from_file_location('node_package_fixture', Path(__file__).with_name('test_node_release_publisher.py'))
_fixture = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_fixture)
SOURCE = _fixture.SOURCE
PRIVATE = _fixture.SOURCE_TRAILER
VERSION = 1158
RUN = 123
ATTEMPT = 2


def _repack(path, transform):
    with tarfile.open(path, 'r:gz') as archive:
        rows = [(member, archive.extractfile(member).read()) for member in archive]
    with tarfile.open(path, 'w:gz') as archive:
        for member, data in transform(rows):
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
    _fixture.checksum(path)


@pytest.mark.parametrize('manifest_first', [True, False])
def test_streaming_verifies_payload_even_when_manifest_is_at_either_end(tmp_path, manifest_first):
    path = _fixture.package(tmp_path, build_version=VERSION)
    _repack(path, lambda rows: sorted(rows, key=lambda row: (row[0].name != 'manifest.json') == manifest_first))
    assert node.verify_package(path, 'amd64', SOURCE)['build_version'] == VERSION
    def corrupt(rows):
        return [(member, b'changed payload' if member.name == 'cli.py' else data) for member, data in rows]
    _repack(path, corrupt)
    with pytest.raises(ValueError, match='payload differs'):
        node.verify_package(path, 'amd64', SOURCE)


@pytest.mark.parametrize('platform,arch,header', [
    ('linux', 'amd64', b''), ('linux', 'arm64', b'not ELF!'),
    ('darwin', 'arm64', b'\xcf\xfa\xed\xfe\x07\x00\x00\x01'),
    ('darwin', 'amd64', b'\xcf\xfa\xed\xfe'),
])
def test_streamed_executable_headers_remain_required(tmp_path, platform, arch, header):
    path = _fixture.package(tmp_path, arch, platform=platform)
    def replace(rows):
        data = dict((member.name, payload) for member, payload in rows)
        manifest = json.loads(data['manifest.json'])
        import hashlib
        name = 'bin/gptadmin-node'
        manifest['files'][name] = {'sha256': hashlib.sha256(header).hexdigest(), 'size': len(header)}
        data[name] = header
        data['manifest.json'] = json.dumps(manifest).encode()
        return [(member, data[member.name]) for member, _ in rows]
    _repack(path, replace)
    with pytest.raises(ValueError, match='ELF|Mach-O|architecture'):
        node.verify_package(path, arch, SOURCE, platform)


def test_concurrent_reads_are_bounded_and_complete_before_returning():
    lock = threading.Lock()
    barrier = threading.Barrier(4)
    state = {'active': 0, 'maximum': 0, 'started': 0, 'finished': 0}
    def read(index):
        with lock:
            state['active'] += 1
            state['maximum'] = max(state['maximum'], state['active'])
            first_wave = state['started'] < 4
            state['started'] += 1
        if first_wave:
            barrier.wait(timeout=5)
        with lock:
            state['active'] -= 1
            state['finished'] += 1
        return index
    assert node.parallel_reads({index: lambda index=index: read(index) for index in range(9)}) == dict(enumerate(range(9)))
    assert state['maximum'] == 4 and state['active'] == 0 and state['finished'] == 9


def test_failed_read_settles_in_flight_work_and_propagates_original_error():
    barrier = threading.Barrier(4)
    failed = threading.Event()
    release = threading.Event()
    finished = []
    def read(index):
        barrier.wait(timeout=5)
        if index == 0:
            failed.set()
            raise RuntimeError('read failed')
        assert release.wait(timeout=5)
        finished.append(index)
    with ThreadPoolExecutor(max_workers=1) as caller:
        future = caller.submit(node.parallel_reads, {index: lambda index=index: read(index) for index in range(4)})
        try:
            assert failed.wait(timeout=5)
            assert not future.done()
        finally:
            release.set()
        with pytest.raises(RuntimeError, match='read failed'):
            future.result(timeout=5)
    assert sorted(finished) == [1, 2, 3]


def _publication_fixture(tmp_path, monkeypatch, fault=None, fault_phase=0):
    node_tag = f'node-v{VERSION}-{SOURCE[:12]}'
    full_tag = f'v{VERSION}'
    assets = []
    for platform in ('linux', 'darwin'):
        for arch in ('amd64', 'arm64'):
            path = _fixture.package(tmp_path, arch, platform=platform, build_version=VERSION)
            assets += [path, path.with_name(path.name + '.sha256')]
    def descriptor(path):
        return {'name': path.name, 'size': path.stat().st_size, 'digest': 'sha256:' + node.digest(path)}
    node_release = {'tag_name': node_tag, 'draft': False, 'prerelease': False,
                    'assets': [descriptor(path) for path in assets]}
    bundle = tmp_path / 'full.tar.gz'
    bundle.write_bytes(b'full package fixture')
    monkeypatch.setattr(full, 'checked_assets', lambda *args: [bundle])
    calls = []
    state = {'tag': False, 'uploads': [], 'phase': 0}
    lock = threading.Lock()
    def gh(*args):
        endpoint = args[1]
        mutation = '--method' in args and args[args.index('--method') + 1] in ('POST', 'PATCH')
        with lock:
            calls.append((state['phase'], args))
            phase = state['phase']
        if not mutation and fault and fault in endpoint and phase == fault_phase:
            raise RuntimeError('preflight read failed')
        if endpoint.endswith('/git/commits/' + SOURCE):
            return json.dumps({'sha': SOURCE, 'message': 'snapshot\n\nSource-Commit: ' + PRIVATE}).encode()
        if endpoint.endswith('/git/ref/heads/main'):
            return json.dumps({'object': {'sha': SOURCE}}).encode()
        if endpoint.endswith('/actions/runs/' + str(RUN)):
            return json.dumps({'name': 'Public Product CI', 'head_sha': SOURCE, 'path': '.github/workflows/public-ci.yml',
                               'event': 'push', 'head_branch': 'main', 'status': 'completed',
                               'conclusion': 'success', 'run_attempt': ATTEMPT}).encode()
        if '/jobs?' in endpoint:
            jobs = [{'name': f'Release bundles ({pair})', 'run_id': RUN, 'status': 'completed',
                     'conclusion': 'success', 'steps': []} for pair in full.CI_RELEASE_PAIRS]
            jobs[0]['steps'] = [{'name': full.CI_RELEASE_LIFECYCLE, 'status': 'completed', 'conclusion': 'success'}]
            jobs += [{'name': name, 'conclusion': 'success'} for name in
                     ('Public product CI gate', 'Verify both macOS ShellMCP release packages')]
            return json.dumps({'jobs': jobs}).encode()
        if '/git/matching-refs/tags/' in endpoint:
            tag = endpoint.rsplit('/', 1)[1]
            return json.dumps([{'ref': 'refs/tags/' + tag, 'object': {'type': 'commit', 'sha': SOURCE}}]
                              if tag == node_tag or state['tag'] else []).encode()
        if endpoint.endswith('/releases/tags/' + node_tag):
            return json.dumps(node_release).encode()
        if endpoint == 'graphql':
            return json.dumps({'data': {'repository': {'latestRelease': {'tagName': 'v1021'}}}}).encode()
        if endpoint.endswith('/releases') and not mutation:
            return (node_tag + '\n').encode()
        if endpoint.endswith('/git/refs'):
            state['tag'] = True
            return b'{}'
        if endpoint.endswith('/releases') and mutation:
            return json.dumps({'id': 8, 'tag_name': full_tag, 'draft': True}).encode()
        if endpoint.startswith('https://uploads.github.com/'):
            asset = descriptor(Path(args[args.index('--input') + 1]))
            state['uploads'].append(asset)
            state['phase'] = 1
            return json.dumps(asset).encode()
        if endpoint.endswith('/releases/8') and mutation:
            return json.dumps({'id': 8, 'tag_name': full_tag, 'draft': False, 'assets': state['uploads']}).encode()
        raise AssertionError(args)
    monkeypatch.setattr(node, 'gh', gh)
    return calls


def test_publication_reads_fresh_state_before_and_after_upload_and_reuses_only_within_phase(tmp_path, monkeypatch):
    calls = _publication_fixture(tmp_path, monkeypatch)
    full.publish(tmp_path, SOURCE, PRIVATE, VERSION, RUN, tmp_path, ci_artifact_attempt=ATTEMPT)
    for phase in (0, 1):
        reads = [args[1] for current, args in calls if current == phase and '--method' not in args]
        for suffix in (f'/git/commits/{SOURCE}', '/git/ref/heads/main', f'/actions/runs/{RUN}', f'/actions/runs/{RUN}/jobs?per_page=100'):
            assert sum(endpoint.endswith(suffix) for endpoint in reads) == 1
    assert any('PATCH' in args for _, args in calls)


@pytest.mark.parametrize('fault', ['/git/commits/', '/git/ref/heads/main', '/actions/runs/123',
                                   '/jobs?', '/git/matching-refs/tags/node-', '/releases/tags/node-'])
@pytest.mark.parametrize('phase', [0, 1])
def test_read_failure_prevents_initial_mutation_or_final_publication(tmp_path, monkeypatch, fault, phase):
    calls = _publication_fixture(tmp_path, monkeypatch, fault, phase)
    with pytest.raises(RuntimeError, match='preflight read failed'):
        full.publish(tmp_path, SOURCE, PRIVATE, VERSION, RUN, tmp_path, ci_artifact_attempt=ATTEMPT)
    mutations = [args for _, args in calls if '--method' in args]
    if phase == 0:
        assert mutations == []
    else:
        assert mutations and all('PATCH' not in args for args in mutations)
        assert sum(args[1].startswith('https://uploads.github.com/') for args in mutations) == 1
