"""Fast units: archive integrity/publication logic; expected1/max15s, not release acceptance."""
import contextlib
from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch
from tools import publish_node_release as release

SOURCE = '1' * 40
MIRROR = SOURCE
SOURCE_TRAILER = '3' * 40


def package(root, arch='amd64', platform='linux', **changes):
    path = Path(root) / f'gptadmin-node-{platform}-{arch}.tar.gz'
    required = release.LINUX_FILES if platform == 'linux' else release.DARWIN_FILES
    blobs = {name: b'local-logic-fixture' for name in required}
    blobs['bin/gptadmin-node'] = b'\x7fELF-node-fixture'
    if platform == 'linux':
        blobs['bin/shellmcp'] = b'\x7fELF-shellmcp-fixture'
        blobs['systemd/gptadmin-proxy-route-relay02.service'] = b'[Unit]\nDescription=fixture\n[Service]\nUser=admin\nExecStart=/usr/bin/ssh\n'
        ui_asset = 'public/admin/assets/index-a1b2c3.js'; ui_css = 'public/admin/assets/index-d4e5f6.css'
        blobs[ui_asset] = b'js-fixture'; blobs[ui_css] = b'css-fixture'
        blobs[release.UI_INDEX] = (f'<script src="/admin/assets/{ui_asset.rsplit("/", 1)[-1]}"></script>'
                                   f'<link href="/admin/assets/{ui_css.rsplit("/", 1)[-1]}" rel="stylesheet">').encode()
    else:
        blobs['bin/gptadmin-node'] = b'\xcf\xfa\xed\xfe\x0c\x00\x00\x01' if arch == 'arm64' else b'\xcf\xfa\xed\xfe\x07\x00\x00\x01'
    blobs['bin/gptadmin-mcp-bridge'] = blobs['bin/gptadmin-node'] + b'-bridge'
    manifest = {'schema': 'gptadmin.node-package/v1', 'platform': platform, 'arch': arch,
                'git_commit': SOURCE, 'build_version': 123, 'source_dirty': False,
                'files': {name: {'sha256': hashlib.sha256(raw).hexdigest(), 'size': len(raw)}
                          for name, raw in blobs.items()}}
    manifest.update(changes)
    blobs['manifest.json'] = json.dumps(manifest).encode()
    with tarfile.open(path, 'w:gz') as archive:
        for name, raw in blobs.items():
            info = tarfile.TarInfo(name); info.size = len(raw)
            info.mode = 0o755 if name.startswith('bin/') else 0o644
            archive.addfile(info, io.BytesIO(raw))
    checksum(path)
    return path


def checksum(path):
    path.with_name(path.name + '.sha256').write_text(release.digest(path) + '  ' + path.name + '\n')


class NodeReleaseTests(unittest.TestCase):
    def test_platform_file_closures_keep_macos_helpers_out_of_linux(self):
        self.assertNotIn('scripts/gptadmin_node_macos_manage.py', release.LINUX_FILES)
        self.assertNotIn('scripts/gptadmin_node_macos_autoupdate.py', release.LINUX_FILES)
        self.assertIn('scripts/gptadmin_node_macos_manage.py', release.DARWIN_FILES)
        self.assertIn('scripts/gptadmin_node_macos_autoupdate.py', release.DARWIN_FILES)

    def test_product_ci_does_not_duplicate_the_workflow_run_release_publisher(self):
        root=Path(__file__).resolve().parents[1]
        candidates=(root/'.gitpublic/public/.github/workflows/public-ci.yml',
                    root/'.github/workflows/public-ci.yml')
        workflow=next((path for path in candidates if path.is_file()),None)
        self.assertIsNotNone(workflow)
        self.assertNotRegex(workflow.read_text(),r'(?m)^  node-release:')

    def test_public_node_release_waits_for_full_product_ci_and_uses_exact_tested_commit(self):
        root=Path(__file__).resolve().parents[1]
        candidates=(root/'.gitpublic/public/.github/workflows/publish-node-release.yml',
                    root/'.github/workflows/publish-node-release.yml')
        workflow=next((path for path in candidates if path.is_file()),None)
        self.assertIsNotNone(workflow)
        text=workflow.read_text()
        for contract in ("workflows: ['Public Product CI']", "tools/wait_ci_release.py success",
                         "event == 'push'", "head_branch == 'main'", "ref: ${{ github.event.workflow_run.head_sha }}",
                         '--source "$SOURCE_SHA"', 'contents: write', 'cancel-in-progress: false'):
            with self.subTest(contract=contract):self.assertIn(contract,text)
        self.assertNotIn('python3 tools/build_node.py', text)
        for artifact_contract in ('actions: read',
                                  'artifact-ids: ${{ steps.artifacts.outputs.node_ids }}',
                                  '--attempt "$CI_RUN_ATTEMPT"',
                                  'run-id: ${{ github.event.workflow_run.id }}',
                                  'verify_package(directory / name, arch, os.environ[\'SOURCE_SHA\'], platform)',
                                  'exactly four packages and their checksums'):
            with self.subTest(artifact_contract=artifact_contract):
                self.assertIn(artifact_contract, text)
        self.assertLess(text.index('python3 tests/e2e/node/darwin_package.py'),
                        text.index('python3 tools/publish_node_release.py'))
        self.assertLess(text.index('verify_ci_release_artifacts'),
                        text.index('git tag -f'))
        self.assertLess(text.index('git tag -f'),
                        text.index('python3 tools/publish_node_release.py'))

    def test_both_clean_archives_bind_same_exact_source(self):
        with tempfile.TemporaryDirectory() as d:
            for arch in ('amd64', 'arm64'):
                self.assertEqual(release.verify_package(package(d, arch), arch, SOURCE)['build_version'],123)

    def test_darwin_archives_are_verified_as_a_distinct_platform_closure(self):
        with tempfile.TemporaryDirectory() as d:
            for arch in ('amd64','arm64'):
                self.assertEqual(release.verify_package(package(d,arch,platform='darwin'),arch,SOURCE,'darwin')['platform'],'darwin')

    def test_release_requires_fixed_components_in_archive_and_manifest(self):
        cases = [('linux', 'bin/shellmcp'), ('linux', 'systemd/gptadmin-proxy-route-relay02.service')]
        cases += [(platform, name) for platform in ('linux', 'darwin')
                  for name in ('bin/gptadmin-mcp-bridge', 'public/secret-input/index.html')]
        for platform, missing in cases:
            with self.subTest(platform=platform, missing=missing), tempfile.TemporaryDirectory() as d:
                path=package(d, platform=platform)
                with tarfile.open(path,'r:gz') as archive:
                    rows=[(member,archive.extractfile(member).read()) for member in archive
                          if member.name != missing]
                manifest=json.loads(next(raw for member,raw in rows if member.name=='manifest.json'))
                manifest['files'].pop(missing)
                rows=[(member,raw) for member,raw in rows if member.name!='manifest.json']
                manifest_raw=json.dumps(manifest).encode()
                info=tarfile.TarInfo('manifest.json');info.size=len(manifest_raw)
                rows.append((info,manifest_raw))
                with tarfile.open(path,'w:gz') as archive:
                    for info,raw in rows:archive.addfile(info,io.BytesIO(raw))
                checksum(path)
                with self.assertRaises(ValueError):release.verify_package(path,'amd64',SOURCE,platform)

    def test_dirty_foreign_source_wrong_arch_and_bad_version_are_rejected(self):
        cases=[{'source_dirty':True},{'source_dirty':None},{'git_commit':'2'*40},
               {'arch':'arm64'},{'build_version':True},{'build_version':0}]
        for changes in cases:
            with self.subTest(changes=changes),tempfile.TemporaryDirectory() as d:
                with self.assertRaises(ValueError):release.verify_package(package(d,**changes),'amd64',SOURCE)

    def test_traversal_symlink_duplicate_and_unlisted_member_are_rejected(self):
        for name,kind in [('.. /bad',tarfile.REGTYPE),('../bad',tarfile.REGTYPE),
                          ('bin/gptadmin-node',tarfile.REGTYPE),('extra',tarfile.SYMTYPE)]:
            with self.subTest(name=name),tempfile.TemporaryDirectory() as d:
                p=package(d)
                with tarfile.open(p,'r:gz') as archive:
                    rows=[(i,archive.extractfile(i).read()) for i in archive]
                with tarfile.open(p,'w:gz') as archive:
                    for i,raw in rows:archive.addfile(i,io.BytesIO(raw))
                    i=tarfile.TarInfo(name);i.type=kind;i.linkname='bin/gptadmin-node';archive.addfile(i,io.BytesIO(b''))
                checksum(p)
                with self.assertRaises(ValueError):release.verify_package(p,'amd64',SOURCE)

    def test_ui_manifest_closure_rejects_unreferenced_assets_and_non_asset_extras(self):
        for extra in ('public/admin/extra.txt', 'public/admin/assets/unreferenced-123456.js'):
            with self.subTest(extra=extra), tempfile.TemporaryDirectory() as d:
                p=package(d)
                with tarfile.open(p,'r:gz') as archive:
                    rows=[(m,archive.extractfile(m).read()) for m in archive]
                with tarfile.open(p,'w:gz') as archive:
                    for info,raw in rows:
                        archive.addfile(info,io.BytesIO(raw))
                    info=tarfile.TarInfo(extra);raw=b'extra';info.size=len(raw)
                    archive.addfile(info,io.BytesIO(raw))
                checksum(p)
                with self.assertRaises(ValueError):release.verify_package(p,'amd64',SOURCE)

    def test_payload_digest_and_archive_checksum_are_independently_enforced(self):
        with tempfile.TemporaryDirectory() as d:
            p=package(d,files={name:{'sha256':'0'*64,'size':19} for name in release.FILES})
            with self.assertRaises(ValueError):release.verify_package(p,'amd64',SOURCE)
            p=package(d);p.with_name(p.name+'.sha256').write_text('0'*64+'  '+p.name)
            with self.assertRaises(ValueError):release.verify_package(p,'amd64',SOURCE)

    def test_wrong_repository_or_arch_version_skew_never_calls_github(self):
        with tempfile.TemporaryDirectory() as d,patch.object(release,'gh') as gh:
            with self.assertRaises(ValueError):release.publish(d,SOURCE,'private/other')
            package(d);package(d,'arm64',build_version=124);package(d,'amd64',platform='darwin');package(d,'arm64',platform='darwin')
            with self.assertRaises(ValueError):release.publish(d,SOURCE,release.REPOSITORY)
            gh.assert_not_called()

    def test_release_is_draft_until_all_server_digests_match_and_never_latest(self):
        with tempfile.TemporaryDirectory() as d:
            packages=[package(d,a) for platform in ('linux','darwin') for a in ('amd64','arm64')]
            for a in ('amd64','arm64'): package(d,a,platform='darwin')
            tag='node-v123-'+SOURCE[:12];assets=[];calls=[];state={'tag':False}
            def gh(*args):
                calls.append(args);endpoint=args[1]
                if endpoint.endswith('/git/ref/heads/main'):
                    return json.dumps({'ref':'refs/heads/main','object':{'type':'commit','sha':MIRROR}}).encode()
                if endpoint.endswith('/git/commits/'+MIRROR):
                    return json.dumps({'sha':MIRROR,'message':'ci: publish sanitized source\n\nSource-Commit: '+SOURCE_TRAILER}).encode()
                if '/git/matching-refs/' in endpoint:
                    return json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':MIRROR}}] if state['tag'] else []).encode()
                if endpoint.endswith('/git/refs'):
                    self.assertIn('sha='+MIRROR,args);state['tag']=True;return b'{}'
                if 'per_page=' in endpoint:return b'[]'
                if endpoint.endswith('/releases'):
                    self.assertIn('draft=true',args);self.assertIn('make_latest=false',args)
                    self.assertIn('target_commitish='+MIRROR,args)
                    return json.dumps({'id':1,'tag_name':tag,'draft':True,'assets':[]}).encode()
                if endpoint.startswith('https://uploads.github.com/'):
                    p=Path(args[args.index('--input')+1]);asset={'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)}
                    assets.append(asset);return json.dumps(asset).encode()
                if endpoint.endswith('/releases/1'):
                    self.assertEqual(len(assets),8);self.assertIn('draft=false',args);self.assertIn('make_latest=false',args)
                    return json.dumps({'id':1,'tag_name':tag,'draft':False,'assets':assets}).encode()
                raise AssertionError(endpoint)
            import contextlib
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):release.publish(d,SOURCE,release.REPOSITORY)
            self.assertIn('PATCH',calls[-1]);self.assertEqual(len(assets),8)

    def test_release_tag_uses_public_mirror_commit_bound_to_exact_source(self):
        import contextlib
        with tempfile.TemporaryDirectory() as d:
            package(d);package(d,'arm64');package(d,'amd64',platform='darwin');package(d,'arm64',platform='darwin')
            tag='node-v123-'+SOURCE[:12];state={'tag':False,'assets':[]}
            def gh(*args):
                endpoint=args[1]
                if endpoint.endswith('/git/ref/heads/main'):
                    return json.dumps({'ref':'refs/heads/main','object':{'type':'commit','sha':MIRROR}}).encode()
                if endpoint.endswith('/git/commits/'+MIRROR):
                    return json.dumps({'sha':MIRROR,'message':'ci: publish sanitized source\n\nSource-Commit: '+SOURCE_TRAILER}).encode()
                if '/git/matching-refs/' in endpoint:
                    return json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':MIRROR}}] if state['tag'] else []).encode()
                if endpoint.endswith('/git/refs'):
                    self.assertIn('sha='+MIRROR,args)
                    state['tag']=True
                    return b'{}'
                if 'per_page=' in endpoint:return b'[]'
                if endpoint.endswith('/releases'):
                    self.assertIn('target_commitish='+MIRROR,args)
                    self.assertIn('Source: '+SOURCE,args[-1])
                    return json.dumps({'id':1,'tag_name':tag,'draft':True,'assets':[]}).encode()
                if endpoint.startswith('https://uploads.github.com/'):
                    p=Path(args[args.index('--input')+1])
                    asset={'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)}
                    state['assets'].append(asset)
                    return json.dumps(asset).encode()
                if endpoint.endswith('/releases/1'):
                    self.assertIn('make_latest=false',args)
                    return json.dumps({'id':1,'tag_name':tag,'draft':False,'assets':state['assets']}).encode()
                raise AssertionError(endpoint)
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):
                release.publish(d,SOURCE,release.REPOSITORY)
            self.assertTrue(state['tag'])

    def test_public_mirror_commit_must_match_tested_sha_and_have_one_valid_source_trailer(self):
        ref={'ref':'refs/heads/main','object':{'type':'commit','sha':MIRROR}}
        wrong={'sha':MIRROR,'message':'ci: publish sanitized source\n\nSource-Commit: not-a-sha'}
        with patch.object(release,'gh',side_effect=[json.dumps(ref).encode(),json.dumps(wrong).encode()]):
            with self.assertRaises(ValueError):release.public_mirror_commit(release.REPOSITORY,SOURCE)
        duplicate={'sha':MIRROR,'message':'ci: publish sanitized source\n\nSource-Commit: '+SOURCE_TRAILER+'\nSource-Commit: '+SOURCE_TRAILER}
        with patch.object(release,'gh',side_effect=[json.dumps(ref).encode(),json.dumps(duplicate).encode()]):
            with self.assertRaises(ValueError):release.public_mirror_commit(release.REPOSITORY,SOURCE)
        with patch.object(release,'gh',return_value=json.dumps(ref).encode()):
            with self.assertRaisesRegex(ValueError,'differs from the exact tested source'):
                release.public_mirror_commit(release.REPOSITORY,SOURCE,commit='4'*40)

    def test_bad_existing_asset_is_not_overwritten_or_published(self):
        with tempfile.TemporaryDirectory() as d:
            package(d);package(d,'arm64');package(d,'amd64',platform='darwin');package(d,'arm64',platform='darwin');tag='node-v123-'+SOURCE[:12]
            existing={'id':1,'tag_name':tag,'draft':True,'assets':[{'name':'gptadmin-node-linux-amd64.tar.gz','size':1,'digest':'sha256:'+'0'*64}]}
            replies=[json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':MIRROR}}]).encode(),
                     json.dumps({'sha':MIRROR,'message':'ci: publish sanitized source\n\nSource-Commit: '+SOURCE_TRAILER}).encode(),
                     json.dumps([existing]).encode(),
                     json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':MIRROR}}]).encode()]
            with patch.object(release,'gh',side_effect=replies) as gh:
                with self.assertRaises(ValueError):release.publish(d,SOURCE,release.REPOSITORY)
                self.assertTrue(all(c.args[0]=='api' for c in gh.call_args_list))

    def test_foreign_existing_tag_never_creates_or_publishes_a_release(self):
        with tempfile.TemporaryDirectory() as d:
            package(d);package(d,'arm64');package(d,'amd64',platform='darwin');package(d,'arm64',platform='darwin');tag='node-v123-'+SOURCE[:12]
            foreign='3'*40
            refs=[{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':foreign}}]
            replies=[json.dumps(refs).encode()]
            with patch.object(release,'gh',side_effect=replies) as gh:
                with self.assertRaises(ValueError):release.publish(d,SOURCE,release.REPOSITORY)
                self.assertEqual(gh.call_count,1)

    def test_annotated_tag_is_bounded_and_must_resolve_to_exact_commit(self):
        tag='node-v123-'+SOURCE[:12];refs=[{'ref':'refs/tags/'+tag,'object':{'type':'tag','sha':'2'*40}}]
        with patch.object(release,'gh',side_effect=[json.dumps(refs).encode(),json.dumps({'object':{'type':'commit','sha':SOURCE}}).encode()]):
            release.verify_tag(release.REPOSITORY,tag,SOURCE)
        with patch.object(release,'gh',side_effect=[json.dumps(refs).encode(),json.dumps({'object':{'type':'commit','sha':'3'*40}}).encode()]):
            with self.assertRaises(ValueError):release.verify_tag(release.REPOSITORY,tag,SOURCE)

    def test_stale_release_list_does_not_hide_created_draft_or_uploaded_assets(self):
        # Both real CI failures: missing draft tag route and stale list after POST.
        import contextlib
        with tempfile.TemporaryDirectory() as d:
            packages=[package(d,a) for a in ('amd64','arm64')]+[package(d,a,platform='darwin') for a in ('amd64','arm64')]
            tag='node-v123-'+SOURCE[:12];state={'tag':False,'assets':[]};calls=[]
            def gh(*args):
                calls.append(args);endpoint=args[1]
                if '/releases/tags/' in endpoint:raise RuntimeError('real draft endpoint404')
                if endpoint.endswith('/git/ref/heads/main'):
                    return json.dumps({'ref':'refs/heads/main','object':{'type':'commit','sha':MIRROR}}).encode()
                if endpoint.endswith('/git/commits/'+MIRROR):
                    return json.dumps({'sha':MIRROR,'message':'ci: publish sanitized source\n\nSource-Commit: '+SOURCE_TRAILER}).encode()
                if '/git/matching-refs/' in endpoint:return json.dumps([{'ref':'refs/tags/'+tag,'object':{'sha':MIRROR,'type':'commit'}}] if state['tag'] else []).encode()
                if endpoint.endswith('/git/refs'):
                    self.assertIn('sha='+MIRROR,args);state['tag']=True;return b'{}'
                if 'per_page=' in endpoint:return b'[]'  # remains stale after create
                if endpoint.endswith('/releases'):
                    self.assertIn('target_commitish='+MIRROR,args)
                    return json.dumps({'id':41,'tag_name':tag,'draft':True,'assets':[]}).encode()
                if endpoint.startswith('https://uploads.github.com/'):
                    p=Path(args[args.index('--input')+1]);asset={'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)}
                    state['assets'].append(asset);return json.dumps(asset).encode()
                if endpoint.endswith('/releases/41'):
                    self.assertIn('PATCH',args);self.assertEqual(len(state['assets']),8)
                    return json.dumps({'id':41,'tag_name':tag,'draft':False,'assets':state['assets']}).encode()
                raise AssertionError(endpoint)
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):release.publish(d,SOURCE,release.REPOSITORY)
            self.assertIn('PATCH',calls[-1]);self.assertTrue(state['tag'])




class ParallelNodePublicationTests(unittest.TestCase):
    def packages(self, directory):
        packages = [package(directory, arch, platform=platform)
                    for platform in ('linux', 'darwin') for arch in ('amd64', 'arm64')]
        return [asset for path in packages for asset in (path, path.with_name(path.name + '.sha256'))]

    def asset(self, path):
        return {'name': path.name, 'size': path.stat().st_size, 'digest': 'sha256:' + release.digest(path)}

    def transport(self, existing, upload=None):
        lock = threading.Lock()
        def gh(*args):
            endpoint = args[1]
            if '/git/matching-refs/' in endpoint:
                return json.dumps([{'ref': 'refs/tags/' + existing['tag_name'],
                                    'object': {'type': 'commit', 'sha': MIRROR}}]).encode()
            if endpoint.endswith('/git/commits/' + MIRROR):
                return json.dumps({'sha': MIRROR, 'message': 'public source\n\nSource-Commit: ' + SOURCE_TRAILER}).encode()
            if 'per_page=' in endpoint:
                return json.dumps([existing]).encode()
            if endpoint.startswith('https://uploads.github.com/'):
                self.assertIn('POST', args)
                path = Path(args[args.index('--input') + 1])
                uploaded = upload(path)
                with lock:
                    existing['assets'].append(uploaded)
                return json.dumps(uploaded).encode()
            if endpoint.endswith('/releases/1'):
                self.assertIn('PATCH', args)
                self.assertIn('draft=false', args)
                self.assertIn('make_latest=false', args)
                self.assertEqual(len(existing['assets']), 8)
                return json.dumps({**existing, 'draft': False}).encode()
            raise AssertionError(endpoint)
        return gh

    def draft(self, assets=(), published=False):
        return {'id': 1, 'tag_name': 'node-v123-' + SOURCE[:12],
                'draft': not published, 'assets': list(assets)}

    def test_uploads_overlap_with_at_most_four_in_flight_and_publish_after_all_finish(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self.packages(directory)
            existing = self.draft()
            lock = threading.Lock()
            batch = threading.Barrier(4, timeout=3)
            active = peak = 0
            started = []
            def upload(path):
                nonlocal active, peak
                with lock:
                    active += 1
                    peak = max(peak, active)
                    started.append(path.name)
                try:
                    batch.wait()
                    return self.asset(path)
                finally:
                    with lock:
                        active -= 1
            with patch.object(release, 'gh', side_effect=self.transport(existing, upload)) as gh, \
                    contextlib.redirect_stdout(io.StringIO()):
                release.publish(directory, SOURCE, release.REPOSITORY)
            self.assertEqual(peak, 4)
            self.assertEqual(active, 0)
            self.assertCountEqual(started, [path.name for path in paths])
            self.assertIn('PATCH', gh.call_args_list[-1].args)

    def test_every_existing_digest_is_checked_before_any_missing_asset_upload(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self.packages(directory)
            existing = self.draft([{**self.asset(paths[-1]), 'digest': 'sha256:' + '0' * 64}])
            upload = unittest.mock.Mock(side_effect=AssertionError('must validate existing assets first'))
            with patch.object(release, 'gh', side_effect=self.transport(existing, upload)) as gh:
                with self.assertRaisesRegex(ValueError, 'existing Node release asset differs'):
                    release.publish(directory, SOURCE, release.REPOSITORY)
            upload.assert_not_called()
            self.assertFalse(any('--method' in call.args for call in gh.call_args_list))

    def test_matching_draft_uploads_only_missing_assets_and_published_release_is_read_only(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self.packages(directory)
            existing = self.draft([self.asset(path) for path in paths[::2]])
            uploaded = []
            def upload(path):
                uploaded.append(path.name)
                return self.asset(path)
            with patch.object(release, 'gh', side_effect=self.transport(existing, upload)), \
                    contextlib.redirect_stdout(io.StringIO()):
                release.publish(directory, SOURCE, release.REPOSITORY)
            self.assertCountEqual(uploaded, [path.name for path in paths[1::2]])
            existing['draft'] = False
            with patch.object(release, 'gh', side_effect=self.transport(existing)) as gh, \
                    contextlib.redirect_stdout(io.StringIO()):
                release.publish(directory, SOURCE, release.REPOSITORY)
            self.assertFalse(any('--method' in call.args for call in gh.call_args_list))

    def test_incomplete_published_release_rejects_all_uploads(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self.packages(directory)
            existing = self.draft([self.asset(paths[-1])], published=True)
            with patch.object(release, 'gh', side_effect=self.transport(existing)) as gh:
                with self.assertRaisesRegex(ValueError, 'published Node release is incomplete'):
                    release.publish(directory, SOURCE, release.REPOSITORY)
            self.assertFalse(any('--method' in call.args for call in gh.call_args_list))

    def test_failed_upload_cancels_pending_waits_for_running_and_never_retries_or_publishes(self):
        for failure in ('transport', 'digest'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                self.packages(directory)
                existing = self.draft()
                first_batch = threading.Barrier(4, timeout=3)
                finish_uploads = threading.Event()
                shutdown_started = threading.Event()
                pending_cancelled = threading.Event()
                publication_finished = threading.Event()
                lock = threading.Lock()
                started = []
                errors = []
                pools = []

                class ObservedPool(ThreadPoolExecutor):
                    def __init__(self, *args, **kwargs):
                        super().__init__(*args, **kwargs)
                        self.futures = []
                        pools.append(self)

                    def submit(self, *args, **kwargs):
                        future = super().submit(*args, **kwargs)
                        self.futures.append(future)
                        future.add_done_callback(lambda result: pending_cancelled.set() if result.cancelled() else None)
                        return future

                    def shutdown(self, *args, **kwargs):
                        shutdown_started.set()
                        return super().shutdown(*args, **kwargs)

                def upload(path):
                    with lock:
                        index = len(started)
                        started.append(path.name)
                    if index < 4:
                        first_batch.wait()
                    if index == 0:
                        if failure == 'transport':
                            raise RuntimeError('fixture upload failed')
                        return {**self.asset(path), 'digest': 'sha256:' + '0' * 64}
                    if not finish_uploads.wait(timeout=5):
                        raise AssertionError('in-flight fixture was not released')
                    return self.asset(path)

                def publish():
                    try:
                        release.publish(directory, SOURCE, release.REPOSITORY)
                    except Exception as error:
                        errors.append(error)
                    finally:
                        publication_finished.set()

                with patch.object(release, 'gh', side_effect=self.transport(existing, upload)) as gh, \
                        patch.object(release, 'ThreadPoolExecutor', ObservedPool, create=True), \
                        contextlib.redirect_stdout(io.StringIO()):
                    publisher = threading.Thread(target=publish)
                    publisher.start()
                    try:
                        self.assertTrue(shutdown_started.wait(timeout=3))
                        self.assertTrue(pending_cancelled.wait(timeout=3))
                        self.assertFalse(publication_finished.is_set())
                    finally:
                        finish_uploads.set()
                        publisher.join(timeout=5)
                    self.assertFalse(publisher.is_alive())
                self.assertEqual(len(errors), 1)
                self.assertRegex(str(errors[0]), 'fixture upload failed|uploaded Node asset digest differs')
                self.assertEqual(len(started), len(set(started)))
                self.assertTrue(any(future.cancelled() for future in pools[0].futures))
                self.assertTrue(all(future.done() for future in pools[0].futures))
                self.assertFalse(any('PATCH' in call.args for call in gh.call_args_list))


if __name__=='__main__':unittest.main()
