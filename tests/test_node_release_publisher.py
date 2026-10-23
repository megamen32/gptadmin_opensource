"""Fast units: archive integrity/publication logic; expected1/max15s, not release acceptance."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from tools import publish_node_release as release

SOURCE = '1' * 40


def package(root, arch='amd64', **changes):
    path = Path(root) / ('gptadmin-node-linux-' + arch + '.tar.gz')
    blobs = {name: b'local-logic-fixture' for name in release.FILES}
    blobs['bin/gptadmin-node'] = b'\x7fELF-node-fixture'
    blobs['bin/shellmcp'] = b'\x7fELF-shellmcp-fixture'
    blobs['systemd/gptadmin-proxy-route-relay02.service'] = (
        b'[Unit]\nDescription=fixture\n[Service]\nUser=admin\nExecStart=/usr/bin/ssh\n')
    ui_asset = 'public/admin/assets/index-a1b2c3.js'
    ui_css = 'public/admin/assets/index-d4e5f6.css'
    blobs[ui_asset] = b'js-fixture'
    blobs[ui_css] = b'css-fixture'
    blobs[release.UI_INDEX] = (f'<script src="/admin/assets/{ui_asset.rsplit("/", 1)[-1]}"></script>'
                               f'<link href="/admin/assets/{ui_css.rsplit("/", 1)[-1]}" rel="stylesheet">').encode()
    manifest = {'schema': 'gptadmin.node-package/v1', 'platform': 'linux', 'arch': arch,
                'git_commit': SOURCE, 'build_version': 123, 'source_dirty': False,
                'files': {name: {'sha256': hashlib.sha256(raw).hexdigest(), 'size': len(raw)}
                          for name, raw in blobs.items()}}
    manifest.update(changes)
    blobs['manifest.json'] = json.dumps(manifest).encode()
    with tarfile.open(path, 'w:gz') as archive:
        for name, raw in blobs.items():
            info = tarfile.TarInfo(name); info.size = len(raw)
            archive.addfile(info, io.BytesIO(raw))
    checksum(path)
    return path


def checksum(path):
    path.with_name(path.name + '.sha256').write_text(release.digest(path) + '  ' + path.name + '\n')


class NodeReleaseTests(unittest.TestCase):
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
        for contract in ("workflows: ['Public Product CI']", "conclusion == 'success'",
                         "event == 'push'", "head_branch == 'main'", "ref: ${{ github.event.workflow_run.head_sha }}",
                         '--source "$SOURCE_SHA"', 'contents: write', 'cancel-in-progress: false'):
            with self.subTest(contract=contract):self.assertIn(contract,text)

    def test_both_clean_archives_bind_same_exact_source(self):
        with tempfile.TemporaryDirectory() as d:
            for arch in ('amd64', 'arm64'):
                self.assertEqual(release.verify_package(package(d, arch), arch, SOURCE)['build_version'],123)

    def test_release_requires_both_new_fixed_components_in_archive_and_manifest(self):
        for missing in ('bin/shellmcp', 'systemd/gptadmin-proxy-route-relay02.service'):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as d:
                path=package(d)
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
                with self.assertRaises(ValueError):release.verify_package(path,'amd64',SOURCE)

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
            package(d);package(d,'arm64',build_version=124)
            with self.assertRaises(ValueError):release.publish(d,SOURCE,release.REPOSITORY)
            gh.assert_not_called()

    def test_release_is_draft_until_all_server_digests_match_and_never_latest(self):
        with tempfile.TemporaryDirectory() as d:
            packages=[package(d,a) for a in ('amd64','arm64')]
            files=[f for p in packages for f in (p,p.with_name(p.name+'.sha256'))]
            tag='node-v123-'+SOURCE[:12];assets=[];calls=[]
            def gh(*args):
                calls.append(args);endpoint=args[1]
                if '/git/matching-refs/' in endpoint:
                    return json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':SOURCE}}]).encode()
                if 'per_page=' in endpoint:return b'[]'
                if endpoint.endswith('/releases'):
                    self.assertIn('draft=true',args);self.assertIn('make_latest=false',args)
                    return json.dumps({'id':1,'tag_name':tag,'draft':True,'assets':[]}).encode()
                if endpoint.startswith('https://uploads.github.com/'):
                    p=Path(args[args.index('--input')+1]);asset={'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)}
                    assets.append(asset);return json.dumps(asset).encode()
                if endpoint.endswith('/releases/1'):
                    self.assertEqual(len(assets),4);self.assertIn('draft=false',args);self.assertIn('make_latest=false',args)
                    return json.dumps({'id':1,'tag_name':tag,'draft':False,'assets':assets}).encode()
                raise AssertionError(endpoint)
            import contextlib
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):release.publish(d,SOURCE,release.REPOSITORY)
            self.assertIn('PATCH',calls[-1]);self.assertEqual(len(assets),4)

    def test_bad_existing_asset_is_not_overwritten_or_published(self):
        with tempfile.TemporaryDirectory() as d:
            package(d);package(d,'arm64');tag='node-v123-'+SOURCE[:12]
            existing={'id':1,'tag_name':tag,'draft':True,'assets':[{'name':'gptadmin-node-linux-amd64.tar.gz','size':1,'digest':'sha256:'+'0'*64}]}
            replies=[json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':SOURCE}}]).encode(),json.dumps([existing]).encode(),json.dumps([{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':SOURCE}}]).encode()]
            with patch.object(release,'gh',side_effect=replies) as gh:
                with self.assertRaises(ValueError):release.publish(d,SOURCE,release.REPOSITORY)
                self.assertTrue(all(c.args[0]=='api' for c in gh.call_args_list))

    def test_foreign_existing_tag_never_creates_or_publishes_a_release(self):
        with tempfile.TemporaryDirectory() as d:
            package(d);package(d,'arm64');tag='node-v123-'+SOURCE[:12]
            refs=[{'ref':'refs/tags/'+tag,'object':{'type':'commit','sha':'2'*40}}]
            with patch.object(release,'gh',return_value=json.dumps(refs).encode()) as gh:
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
            packages=[package(d,a) for a in ('amd64','arm64')]
            tag='node-v123-'+SOURCE[:12];state={'tag':False,'assets':[]};calls=[]
            def gh(*args):
                calls.append(args);endpoint=args[1]
                if '/releases/tags/' in endpoint:raise RuntimeError('real draft endpoint404')
                if '/git/matching-refs/' in endpoint:return json.dumps([{'ref':'refs/tags/'+tag,'object':{'sha':SOURCE,'type':'commit'}}] if state['tag'] else []).encode()
                if endpoint.endswith('/git/refs'):
                    self.assertIn('sha='+SOURCE,args);state['tag']=True;return b'{}'
                if 'per_page=' in endpoint:return b'[]'  # remains stale after create
                if endpoint.endswith('/releases'):
                    return json.dumps({'id':41,'tag_name':tag,'draft':True,'assets':[]}).encode()
                if endpoint.startswith('https://uploads.github.com/'):
                    p=Path(args[args.index('--input')+1]);asset={'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)}
                    state['assets'].append(asset);return json.dumps(asset).encode()
                if endpoint.endswith('/releases/41'):
                    self.assertIn('PATCH',args);self.assertEqual(len(state['assets']),4)
                    return json.dumps({'id':41,'tag_name':tag,'draft':False,'assets':state['assets']}).encode()
                raise AssertionError(endpoint)
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):release.publish(d,SOURCE,release.REPOSITORY)
            self.assertIn('PATCH',calls[-1]);self.assertTrue(state['tag'])



if __name__=='__main__':unittest.main()
