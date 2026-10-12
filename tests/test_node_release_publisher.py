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
    def test_both_clean_archives_bind_same_exact_source(self):
        with tempfile.TemporaryDirectory() as d:
            for arch in ('amd64', 'arm64'):
                self.assertEqual(release.verify_package(package(d, arch), arch, SOURCE)['build_version'],123)

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
            assets=[{'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)} for p in files]
            calls=[]
            def gh(*args):
                calls.append(args)
                if args[0]=='api':
                    if '/git/matching-refs/' in args[1]:
                        return json.dumps([{'ref':'refs/tags/node-v123-'+SOURCE[:12],
                                            'object':{'type':'commit','sha':SOURCE}}]).encode()
                    if 'per_page=' in args[1]:
                        created=any(c[:2]==('release','create') for c in calls)
                        return json.dumps([{'id':1,'tag_name':'node-v123-'+SOURCE[:12],'draft':True,'assets':[]}] if created else []).encode()
                    count=sum(1 for c in calls if c[:2]==('release','upload'))
                    return json.dumps({'id':1,'draft':True,'assets':assets if count==4 else []}).encode()
                return b''
            import contextlib
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):release.publish(d,SOURCE,release.REPOSITORY)
            create=next(c for c in calls if c[:2]==('release','create'))
            self.assertIn('--draft',create);self.assertIn('--latest=false',create)
            self.assertEqual(calls[-1][:2],('release','edit'));self.assertIn('--latest=false',calls[-1])

    def test_bad_existing_asset_is_not_overwritten_or_published(self):
        with tempfile.TemporaryDirectory() as d:
            package(d);package(d,'arm64');tag='node-v123-'+SOURCE[:12]
            existing={'tag_name':tag,'draft':True,'assets':[{'name':'gptadmin-node-linux-amd64.tar.gz','size':1,'digest':'sha256:'+'0'*64}]}
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

    def test_draft_lookup_uses_id_and_explicit_ref_before_publication(self):
        # Reproduces the real GitHub 404: drafts have no published tag route.
        import contextlib
        with tempfile.TemporaryDirectory() as d:
            packages=[package(d,a) for a in ('amd64','arm64')]
            files=[f for p in packages for f in (p,p.with_name(p.name+'.sha256'))]
            tag='node-v123-'+SOURCE[:12];state={'tag':False,'created':False,'assets':[]};calls=[]
            def gh(*args):
                calls.append(args)
                if args[0]=='api':
                    endpoint=args[1]
                    if '/releases/tags/' in endpoint:raise RuntimeError('real draft endpoint404')
                    if '/git/matching-refs/' in endpoint:return json.dumps([{'ref':'refs/tags/'+tag,'object':{'sha':SOURCE,'type':'commit'}}] if state['tag'] else []).encode()
                    if endpoint.endswith('/git/refs'):
                        self.assertIn('--method',args);self.assertIn('sha='+SOURCE,args);state['tag']=True;return b'{}'
                    if 'per_page=' in endpoint:return json.dumps([{'id':41,'tag_name':tag,'draft':True,'assets':state['assets']}] if state['created'] else []).encode()
                    if endpoint.endswith('/releases/41'):return json.dumps({'id':41,'draft':True,'assets':state['assets']}).encode()
                if args[:2]==('release','create'):state['created']=True
                if args[:2]==('release','upload'):
                    p=Path(args[3]);state['assets'].append({'name':p.name,'size':p.stat().st_size,'digest':'sha256:'+release.digest(p)})
                if args[:2]==('release','edit'):self.assertEqual(len(state['assets']),4)
                return b''
            with patch.object(release,'gh',side_effect=gh),contextlib.redirect_stdout(io.StringIO()):release.publish(d,SOURCE,release.REPOSITORY)
            self.assertEqual(calls[-1][:2],('release','edit'));self.assertTrue(state['tag'])


if __name__=='__main__':unittest.main()
