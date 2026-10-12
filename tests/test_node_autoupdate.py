"""Fast units for updater selection/security; expected1/max15s, no runtime claims."""
import json
import io
from pathlib import Path
import tarfile
import tempfile
import time
import unittest
from types import SimpleNamespace
from contextlib import ExitStack
from unittest.mock import patch
from scripts import gptadmin_node_autoupdate as update
from scripts import gptadmin_node_manage as manager
from test_node_release_publisher import package

TAG='node-v123-'+'1'*12


def index(**changes):
    name='gptadmin-node-linux-amd64.tar.gz'
    value={'tag_name':TAG,'published_at':'2026-10-12T00:00:00Z','draft':False,'prerelease':False,
           'assets':[{'name':name,'size':100,'digest':'sha256:'+'1'*64,
                      'browser_download_url':'https://github.com/'+update.REPOSITORY+'/releases/download/'+TAG+'/'+name}]}
    value.update(changes);return value


class NodeAutoupdateTests(unittest.TestCase):
    def test_intent_record_is_durable_protected_and_replaces_atomically(self):
        import os,stat
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)/'hold.json'
            update.record(path,{'reason':'manager_in_progress'})
            self.assertEqual(json.loads(path.read_text())['reason'],'manager_in_progress')
            self.assertEqual(stat.S_IMODE(path.stat().st_mode),0o600)
            update.record(path,{'reason':'failed_review_required'})
            self.assertEqual(json.loads(path.read_text())['reason'],'failed_review_required')
            self.assertEqual(list(Path(d).iterdir()),[path])
            with patch.object(update,'STATE',Path(d)):update.clear_hold()
            self.assertFalse(path.exists())

    def test_selects_node_channel_without_consuming_legacy_latest_or_drafts(self):
        release,asset,source=update.select_release([index(tag_name='v999'),index(),index(tag_name='node-v124-'+'2'*12,draft=True)],'amd64')
        self.assertEqual(release['tag_name'],TAG);self.assertEqual(source,'1'*12)

    def test_missing_architecture_wrong_url_digest_or_asset_size_refuses(self):
        for field,value in [('name','gptadmin-node-linux-arm64.tar.gz'),('size',129*1024**2),
                            ('browser_download_url','https://github.com/other/repo/release'),('digest','sha256:no')]:
            with self.subTest(field=field):
                row=index();row['assets'][0][field]=value
                with self.assertRaises(ValueError):update.select_release([row],'amd64')

    def test_public_downloads_never_redirect_to_plaintext_or_credentialed_hosts(self):
        for url in ['http://github.com/x','https://github.com:444/x','https://user:secret@github.com/x',
                    'https://untrusted.test/x','https://github.com/x#fragment']:
            with self.subTest(url=url):
                with self.assertRaises(ValueError):update.public_url(url)
        update.public_url('https://release-assets.githubusercontent.com/path?one=signed-public-url')

    def test_explicit_config_rejects_missing_disabled_duplicate_or_external_instance(self):
        for value in [{},{'enabled':False,'names':['a']},{'enabled':True,'names':[]},
                      {'enabled':True,'names':['a','a']},{'enabled':True,'names':['../outside']}]:
            with self.subTest(value=value):
                with self.assertRaises(ValueError):update.validate_config(value)
        self.assertEqual(update.validate_config({'enabled':True,'names':['one','two']}),['one','two'])

    def test_tag_resolution_is_exact_commit_not_only_short_prefix(self):
        expected='1'*12+'2'*28
        with patch.object(update,'get',return_value=json.dumps({'object':{'sha':expected,'type':'commit'}}).encode()):
            self.assertEqual(update.resolve_tag(TAG,time.monotonic()+5),expected)

    def test_annotated_tag_chain_is_bounded_and_non_commit_refuses(self):
        row={'object':{'sha':'2'*40,'type':'tag'}}
        with patch.object(update,'get',return_value=json.dumps(row).encode()) as get:
            with self.assertRaises(ValueError):update.resolve_tag(TAG,time.monotonic()+5)
            self.assertEqual(get.call_count,5)
        with patch.object(update,'get',return_value=json.dumps({'object':{'sha':'2'*40,'type':'blob'}}).encode()):
            with self.assertRaises(ValueError):update.resolve_tag(TAG,time.monotonic()+5)

    def test_expired_read_budget_never_opens_network(self):
        with patch.object(update.urllib.request,'build_opener') as opener:
            with self.assertRaises(RuntimeError):update.get(update.API,100,time.monotonic()-1)
            opener.assert_not_called()

    def test_native_health_has_no_commit_version_is_authoritative(self):
        with patch.object(manager,'read_installed_env',return_value={}),patch.object(manager,'origin_from_env',return_value='http://unit.invalid'):
            for commit,expected in [('1'*40,True),('2'*40,False)]:
                with patch.object(manager,'http_json',side_effect=[(200,{'ok':True}),(200,{'git_commit':commit})]) as http:
                    self.assertEqual(update.live_matches(manager,'one','1'*40),expected)
                    self.assertTrue(http.call_args_list[-1].args[1].endswith('/version'))

    def test_manager_rejects_duplicate_or_unbounded_member_count_before_extraction(self):
        for duplicate in (True,False):
            with self.subTest(duplicate=duplicate),tempfile.TemporaryDirectory() as d:
                path=package(d)
                with tarfile.open(path,'r:gz') as archive:rows=[(m,archive.extractfile(m).read()) for m in archive]
                with tarfile.open(path,'w:gz') as archive:
                    for m,raw in rows:archive.addfile(m,io.BytesIO(raw))
                    for n in range(1 if duplicate else 64):
                        m=tarfile.TarInfo('bin/gptadmin-node' if duplicate else 'extra-'+str(n));archive.addfile(m,io.BytesIO(b''))
                target=Path(d)/'extract'
                with self.assertRaises(manager.PreflightError):manager.verify_and_extract(path,'amd64',target)
                self.assertFalse(target.exists())

    def test_require_drain_is_an_explicit_update_flag(self):
        parsed=manager.build_parser().parse_args(['update','--name','one','--package','unit.tar.gz','--require-drain'])
        self.assertTrue(parsed.require_drain)

    def test_catchable_deadline_restores_previous_code_and_cancels_drain(self):
        # Narrow local lifecycle proof: no native manager/runtime claim.
        with tempfile.TemporaryDirectory() as d, ExitStack() as stack:
            root=Path(d);layout=manager.Layout(opt_root=root/'opt')
            old=layout.versions_root()/'old-1';old.mkdir(parents=True)
            manager.switch_current(layout,old)
            def extract(package,arch,target):
                target.mkdir();return {'git_commit':'2'*40,'build_version':123}
            for name,value in [('require_root',None),('resolve_instance_name','one'),
                               ('read_installed_env',{}),('read_admin_token','unit-only'),
                               ('origin_from_env','http://unit.invalid'),('host_arch','amd64'),
                               ('check_disk_floor',None),('verify_elf_binary',None),
                               ('manifest_summary',{'git_commit':'1'*40}),('drain_begin',True),
                               ('drain_wait_ready',{'ready_to_stop':True}),('wait_healthy',None)]:
                stack.enter_context(patch.object(manager,name,return_value=value))
            stack.enter_context(patch.object(manager,'verify_and_extract',side_effect=extract))
            cancel=stack.enter_context(patch.object(manager,'drain_cancel',return_value=True))
            states=[]
            def restart(runner,*args):
                states.append(manager.current_target(layout))
                if len(states)==1:manager.UpdateBudget.expired(0,None)
            stack.enter_context(patch.object(manager,'systemctl',side_effect=restart))
            budget=SimpleNamespace(cleanup=unittest.mock.Mock())
            args=SimpleNamespace(name='one',package='unit.tar.gz',wait_secs=20,require_drain=True)
            with self.assertRaisesRegex(manager.RuntimeFailure,'rolled back'):
                manager.update_with_budget(args,layout,manager.Runner(),budget)
            self.assertNotEqual(states[0],old);self.assertEqual(states[1],old)
            self.assertEqual(manager.current_target(layout),old)
            budget.cleanup.assert_called_once();cancel.assert_called_once()

    def test_lost_drain_begin_response_is_cancelled_before_any_switch(self):
        with tempfile.TemporaryDirectory() as d, ExitStack() as stack:
            root=Path(d);layout=manager.Layout(opt_root=root/'opt')
            old=layout.versions_root()/'old-1';old.mkdir(parents=True);manager.switch_current(layout,old)
            def extract(package,arch,target):target.mkdir();return {'git_commit':'2'*40}
            for name,value in [('require_root',None),('resolve_instance_name','one'),
                ('read_installed_env',{}),('read_admin_token','unit-only'),('origin_from_env','http://unit.invalid'),
                ('host_arch','amd64'),('check_disk_floor',None),('verify_elf_binary',None),
                ('manifest_summary',{'git_commit':'1'*40})]:
                stack.enter_context(patch.object(manager,name,return_value=value))
            stack.enter_context(patch.object(manager,'verify_and_extract',side_effect=extract))
            stack.enter_context(patch.object(manager,'drain_begin',side_effect=manager.RuntimeFailure('lost begin response')))
            cancel=stack.enter_context(patch.object(manager,'drain_cancel',return_value=True))
            restart=stack.enter_context(patch.object(manager,'systemctl'))
            budget=SimpleNamespace(cleanup=unittest.mock.Mock())
            with self.assertRaisesRegex(manager.RuntimeFailure,'lost begin'):
                manager.update_with_budget(SimpleNamespace(name='one',package='unit',wait_secs=20,require_drain=True),layout,manager.Runner(),budget)
            self.assertEqual(manager.current_target(layout),old);restart.assert_not_called()
            cancel.assert_called_once();budget.cleanup.assert_called_once()

    def test_real_alarm_is_catchable_and_cleanup_has_separate_reserve(self):
        import signal
        previous=signal.getsignal(signal.SIGALRM)
        with manager.UpdateBudget(1) as budget:
            with self.assertRaisesRegex(manager.RuntimeFailure,'deadline'):
                time.sleep(1.2)
            budget.cleanup()
            self.assertGreater(signal.getitimer(signal.ITIMER_REAL)[0],79)
        self.assertEqual(signal.getsignal(signal.SIGALRM),previous)
        self.assertEqual(signal.getitimer(signal.ITIMER_REAL)[0],0)

    def test_systemctl_timeout_is_catchable_not_uncaught_subprocess_error(self):
        import subprocess
        runner=SimpleNamespace(run=unittest.mock.Mock(side_effect=subprocess.TimeoutExpired('systemctl',45)))
        with self.assertRaises(manager.RuntimeFailure):manager.systemctl(runner,'restart','unit-only')


if __name__=='__main__':unittest.main()
