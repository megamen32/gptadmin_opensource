"""Fast units for updater selection/security; expected1/max15s, no runtime claims."""
import json
import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import time
import unittest
import urllib.error
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
    def test_public_node_url_is_separate_https_origin_for_install_and_update(self):
        public_origin='https://app.example.test'
        node_url='https://node-44.example.test'
        install=manager.build_parser().parse_args([
            'install','--name','node44','--package','node.tar.gz',
            '--public-origin',public_origin,'--node-public-url',node_url,'--yes'])
        self.assertEqual(install.public_origin,public_origin)
        self.assertEqual(install.node_public_url,node_url)
        update_args=manager.build_parser().parse_args([
            'update','--name','node44','--package','node.tar.gz',
            '--node-public-url',node_url,'--require-drain'])
        self.assertEqual(update_args.node_public_url,node_url)
        values=manager.build_env_values(
            name='node44',layout=manager.Layout(),bind_host='127.0.0.1',bind_port=9001,
            token='x'*32,admin_password=None,public_origin=public_origin,node_public_url=node_url,
            seed=None,peers=None,default_user='root',default_home='/root')
        self.assertEqual(values['PUBLIC_ORIGIN'],public_origin)
        self.assertEqual(values['GPTADMIN_NODE_PUBLIC_URL'],node_url)
        with self.assertRaises(manager.UsageError):
            manager.validate_node_public_url('http://node-44.example.test')

    def test_install_reads_one_time_join_grant_from_stdin(self):
        parsed=manager.build_parser().parse_args([
            'install','--name','node44','--package','node.tar.gz','--join-url',
            'https://mcp.example.test','--join-grant-stdin','--yes'])
        self.assertTrue(parsed.join_grant_stdin)
        valid='ab'*32
        with patch.object(manager.sys,'stdin',io.StringIO(valid+'\n')):
            self.assertEqual(manager.read_join_grant_stdin(),valid)
        for payload in ('','not-a-grant\n',valid+'\nextra'):
            with self.subTest(payload=payload),patch.object(manager.sys,'stdin',io.StringIO(payload)):
                with self.assertRaises(manager.UsageError):manager.read_join_grant_stdin()

    def test_generated_node_unit_has_the_measured_finite_service_budget(self):
        unit=manager.render_unit_template(manager.Layout())
        for line in ('MemoryHigh=128M','MemoryMax=256M','MemorySwapMax=0',
                     'CPUQuota=100%','TasksMax=128','IOWeight=10','Environment=GOMAXPROCS=1'):
            with self.subTest(line=line):self.assertIn(line,unit)

    def test_autoupdate_service_can_refresh_helpers_under_protect_system(self):
        unit=manager.AUTOUPDATE_SERVICE_TEXT
        self.assertIn('ProtectSystem=full',unit)
        self.assertIn('ReadWritePaths=/usr/local/lib/gptadmin',unit)

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

    def test_latest_node_release_cannot_regress_live_build_or_reuse_build_for_other_source(self):
        with self.assertRaisesRegex(ValueError,'older than an active Node'):
            update.validate_release_freshness(1023,'2'*40,1024,['1'*40],time.monotonic()+5)
        with self.assertRaisesRegex(ValueError,'same build version has a different source'):
            update.validate_release_freshness(1024,'2'*40,1024,['1'*40],time.monotonic()+5)
        update.validate_release_freshness(1024,'1'*40,1024,['1'*40])
        update.validate_release_freshness(1025,'2'*40,1024,['1'*40])

    def test_higher_live_build_retires_the_older_configured_source_baseline(self):
        config={'minimum_build_version':1027,'minimum_sources':['f'*40]}
        live={'primary':{'build_version':1028,'git_commit':'9'*40}}
        minimum,sources=update.freshness_baseline(config,live)
        self.assertEqual(minimum,1028)
        self.assertEqual(sources,['9'*40])
        update.validate_release_freshness(1028,'9'*40,minimum,sources)

    def test_same_highest_live_build_still_rejects_multiple_sources(self):
        config={'minimum_build_version':1027,'minimum_sources':['f'*40]}
        live={
            'primary':{'build_version':1028,'git_commit':'9'*40},
            'replica':{'build_version':1028,'git_commit':'8'*40},
        }
        minimum,sources=update.freshness_baseline(config,live)
        self.assertEqual(minimum,1028)
        with self.assertRaisesRegex(ValueError,'same build version has a different source'):
            update.validate_release_freshness(1028,'9'*40,minimum,sources)

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
                      {'enabled':True,'names':['a','a']},{'enabled':True,'names':['../outside']},
                      {'enabled':True,'names':['a'],'minimum_sources':[{}]},
                      {'enabled':True,'names':['a'],'minimum_build_version':True}]:
            with self.subTest(value=value):
                with self.assertRaises(ValueError):update.validate_config(value)
        self.assertEqual(update.validate_config({'enabled':True,'names':['one','two']}),['one','two'])

    def test_tag_resolution_is_exact_commit_not_only_short_prefix(self):
        expected='1'*12+'2'*28
        with patch.object(update,'get',return_value=json.dumps({'object':{'sha':expected,'type':'commit'}}).encode()):
            self.assertEqual(update.resolve_tag(TAG,time.monotonic()+5),expected)

    def test_rate_limited_api_falls_back_to_immutable_public_feed_tag_and_checksum(self):
        from io import BytesIO
        latest='node-v124-'+'2'*12
        source='2'*12+'3'*28
        api_error=urllib.error.HTTPError(update.API,403,'rate limit exceeded',{},BytesIO(b'API rate limit exceeded'))
        checksum=b'a'*64+b'  gptadmin-node-linux-amd64.tar.gz\n'
        with patch.object(update,'get',side_effect=[api_error,checksum]) as get, \
             patch.object(update,'latest_public_tag',return_value=(latest,source,124)):
            release,asset,got_source,fallback=update.select_release_with_fallback('amd64',time.monotonic()+30)
        self.assertTrue(fallback);self.assertEqual(got_source,source)
        self.assertEqual(release['tag_name'],latest)
        self.assertEqual(asset['digest'],'sha256:'+'a'*64)
        self.assertIsNone(asset['size'])
        self.assertTrue(get.call_args_list[1].args[0].endswith('/gptadmin-node-linux-amd64.tar.gz.sha256'))

    def test_public_feed_selects_latest_node_tag_and_binds_exact_git_ref(self):
        latest='node-v124-'+'2'*12
        source='2'*12+'3'*28
        ns='http://www.w3.org/2005/Atom'
        feed=f'''<feed xmlns="{ns}">
          <entry><id>tag:github.com,2008:Repository/1/node-v123-{'1'*12}</id><updated>2026-10-01T00:00:00Z</updated></entry>
          <entry><id>tag:github.com,2008:Repository/1/{latest}</id><updated>2026-10-02T00:00:00Z</updated></entry>
        </feed>'''.encode()
        result=SimpleNamespace(returncode=0,stdout=f'{source}\trefs/tags/{latest}\n'.encode(),stderr=b'')
        with patch.object(update,'get',return_value=feed),patch.object(update.subprocess,'run',return_value=result) as run:
            self.assertEqual(update.latest_public_tag(time.monotonic()+30),(latest,source,124))
        self.assertEqual(run.call_args.args[0][0:3],['git','-c','credential.helper='])

    def test_public_feed_refuses_unbound_tag_and_xml_entity_declarations(self):
        latest='node-v124-'+'2'*12
        ns='http://www.w3.org/2005/Atom'
        feed=f'<feed xmlns="{ns}"><entry><id>tag:github.com,2008:Repository/1/{latest}</id><updated>2026-10-02T00:00:00Z</updated></entry></feed>'.encode()
        result=SimpleNamespace(returncode=0,stdout=f"{'3'*40}\trefs/tags/{latest}\n".encode(),stderr=b'')
        with patch.object(update,'get',return_value=feed),patch.object(update.subprocess,'run',return_value=result):
            with self.assertRaisesRegex(ValueError,'source prefix'):
                update.latest_public_tag(time.monotonic()+30)
        with patch.object(update,'get',return_value=b'<!DOCTYPE feed [<!ENTITY x "boom">]><feed/>'):
            with self.assertRaisesRegex(ValueError,'forbidden declaration'):
                update.latest_public_tag(time.monotonic()+30)

    def test_only_verified_drain_timeout_is_retryable_without_manual_hold(self):
        busy=SimpleNamespace(returncode=4,stdout=b'',stderr=b'drain did not reach ready_to_stop')
        unknown=SimpleNamespace(returncode=4,stdout=b'',stderr=b'drain admission failed; cancel is unverified')
        rollback=SimpleNamespace(returncode=4,stdout=b'',stderr=b'ROLLBACK ALSO FAILED')
        self.assertTrue(update.safely_deferred_for_busy_node(busy))
        self.assertFalse(update.safely_deferred_for_busy_node(unknown))
        self.assertFalse(update.safely_deferred_for_busy_node(rollback))

    def test_verified_package_refreshes_all_three_protected_runtime_helpers(self):
        with tempfile.TemporaryDirectory() as source_dir, tempfile.TemporaryDirectory() as install_dir:
            source=Path(source_dir)/'scripts';source.mkdir()
            names=('gptadmin_node_manage.py','gptadmin_node_probe.py','gptadmin_node_autoupdate.py')
            for name in names:(source/name).write_text('verified:'+name)
            with patch.object(update,'HELPER_INSTALL_DIR',Path(install_dir)):
                update.refresh_installed_helpers(source_dir)
            for name in names:
                self.assertEqual((Path(install_dir)/name).read_text(),'verified:'+name)

    def test_primary_is_enrolled_only_when_live_service_and_version_identify_unified_node(self):
        class PrimaryRunner:
            def __init__(self, exec_path):self.exec_path=exec_path
            def run(self,argv,timeout=None):
                if argv[:3]==['systemctl','list-units','gptadmin-node@*.service']:
                    return SimpleNamespace(returncode=0,stdout='')
                if argv[:3]==['systemctl','is-active','gptadmin-hub.service']:
                    return SimpleNamespace(returncode=0,stdout='active')
                if argv[:3]==['systemctl','show','gptadmin-hub.service']:
                    return SimpleNamespace(returncode=0,stdout=self.exec_path)
                raise AssertionError(argv)
        with tempfile.TemporaryDirectory() as d:
            env=Path(d)/'primary.env';env.write_text('GPTADMIN_HUB_HOST=127.0.0.1\nGPTADMIN_HUB_PORT=9001\nCTL_TOKEN=unit-only\n')
            base=Path(d)/'gptadmin.env';base.write_text('')
            with patch.object(manager,'PRIMARY_ENV_FILE',env),patch.object(manager,'PRIMARY_BASE_ENV_FILE',base), \
                 patch.object(manager,'http_json',return_value=(200,{
                    'name':'gptadmin-go-hub','git_commit':'1'*40,'build_version':1024})):
                for exec_path in ('/opt/gptadmin/bin/gptadmin-node',
                                  '/opt/gptadmin-node/current/bin/gptadmin-node'):
                    with self.subTest(exec_path=exec_path):
                        self.assertTrue(manager.primary_unified_node(PrimaryRunner(exec_path)))

    def test_active_node_versions_accepts_numeric_build_version_string_from_live_api(self):
        live_version = {
            'name': 'gptadmin-go-hub',
            'git_commit': '1' * 40,
            'build_version': '1024',
        }
        with patch.object(manager, 'read_primary_effective_env', return_value={
            'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': '9001',
        }), patch.object(manager, 'http_json', return_value=(200, live_version)):
            versions = manager.active_node_versions(manager.Layout(), ['primary'])
        self.assertEqual(versions, {
            'primary': {'git_commit': '1' * 40, 'build_version': 1024},
        })
        self.assertEqual(manager.parse_build_version(1024), 1024)
        for invalid in (True, 1.5, '0', '01024', 'latest', 0):
            with self.subTest(invalid=invalid):
                self.assertIsNone(manager.parse_build_version(invalid))

    def test_primary_adoption_reads_drain_credential_from_effective_systemd_environment(self):
        import stat
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)
            primary_env=root/'primary.env'
            primary_env.write_text('GPTADMIN_HUB_HOST=127.0.0.1\nGPTADMIN_HUB_PORT=9001\n')
            base_env=root/'gptadmin.env'
            base_env.write_text('CTL_TOKEN=unit-only\n')
            binary=root/'gptadmin-node'
            binary.write_bytes(b'unit-primary-binary')
            layout=manager.Layout(opt_root=root/'opt',state_root=root/'state',config_root=root/'config')
            dropin=root/'systemd'/'gptadmin-hub.service.d'/'zzzzzz-node-autoupdate.conf'
            original_stat=Path.stat
            def stat_with_protected_systemd_files(path,*args,**kwargs):
                if path in (primary_env,binary):
                    return SimpleNamespace(st_uid=0,st_mode=stat.S_IFREG|0o600)
                return original_stat(path,*args,**kwargs)
            with patch.object(manager,'PRIMARY_ENV_FILE',primary_env), \
                 patch.object(manager,'PRIMARY_BASE_ENV_FILE',base_env), \
                 patch.object(manager,'PRIMARY_BINARY',binary), \
                 patch.object(manager,'PRIMARY_DROPIN',dropin), \
                 patch.object(manager,'primary_unified_node',return_value=True), \
                 patch.object(manager,'http_json',return_value=(200,{
                    'name':'gptadmin-go-hub','git_commit':'1'*40,'build_version':'1024'})), \
                 patch.object(manager,'current_target',return_value=None), \
                 patch.object(manager,'host_arch',return_value='amd64'), \
                 patch.object(Path,'stat',stat_with_protected_systemd_files):
                manager.prepare_primary_node(layout,manager.Runner())
            self.assertTrue(layout.current_link().is_symlink())
            version_dir=next(layout.versions_root().iterdir())
            manifest=json.loads((version_dir/'manifest.json').read_text())
            self.assertEqual(manifest['build_version'],1024)

    def test_existing_primary_current_must_match_live_version_and_manifest_digest(self):
        with tempfile.TemporaryDirectory() as d:
            version_dir=Path(d)/'version';binary=version_dir/'bin'/'gptadmin-node'
            binary.parent.mkdir(parents=True);data=b'\x7fELFverified-test-binary';binary.write_bytes(data)
            live={'git_commit':'1'*40,'build_version':1024}
            manifest={'schema':manager.PACKAGE_SCHEMA,'git_commit':live['git_commit'],
                      'build_version':live['build_version'],'source_dirty':False,
                      'platform':'linux','arch':'amd64',
                      'files':{'bin/gptadmin-node':{'sha256':hashlib.sha256(data).hexdigest(),
                                                    'size':len(data)}}}
            (version_dir/'manifest.json').write_text(json.dumps(manifest))
            with patch.object(manager,'host_arch',return_value='amd64'), \
                 patch.object(manager,'verify_elf_binary'):
                manager.validate_primary_current(version_dir,live)
                with self.assertRaisesRegex(manager.PreflightError,'does not match the live primary'):
                    manager.validate_primary_current(version_dir,{'git_commit':'2'*40,'build_version':1024})
                binary.write_bytes(b'\x7fELFtampered')
                with self.assertRaisesRegex(manager.PreflightError,'does not match its manifest'):
                    manager.validate_primary_current(version_dir,live)

    def test_primary_update_reuses_systemd_environment_files_without_copying_credentials(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);base=root/'gptadmin.env';base.write_text('CTL_TOKEN=unit-only\nGPTADMIN_HUB_PORT=9000\n')
            layout=manager.Layout(config_root=root/'node-config')
            env_path=layout.env_file('primary');env_path.parent.mkdir(parents=True)
            env_path.write_text('GPTADMIN_HUB_PORT=9001\nGPTADMIN_HUB_HOST=127.0.0.1\n')
            with patch.object(manager,'PRIMARY_BASE_ENV_FILE',base):
                env=manager.read_installed_env(layout,'primary')
            self.assertEqual(env['CTL_TOKEN'],'unit-only')
            self.assertEqual(env['GPTADMIN_HUB_PORT'],'9001')

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

    def test_program_has_explicit_node_auto_update_opt_in_and_enumerates_every_active_instance(self):
        parsed=manager.build_parser().parse_args(['autoupdate','enable'])
        self.assertEqual(parsed.action,'enable')
        self.assertIs(parsed.func,manager.cmd_autoupdate)
        self.assertEqual(manager.Layout().unit_instance('primary'),'gptadmin-hub.service')
        class ActiveNodes:
            def run(self,argv,timeout=None):
                self.assertion = argv
                return SimpleNamespace(returncode=0,stdout=(
                    'gptadmin-node@node-a.service loaded active running Node A\n'
                    'gptadmin-node@node-b.service loaded active running Node B\n'))
        with patch.object(manager,'primary_unified_node',return_value=False):
            self.assertEqual(manager.active_node_names(ActiveNodes()),['node-a','node-b'])

    def test_enable_writes_opt_in_and_verifies_public_package_before_timer_activation(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);config=root/'etc'/'autoupdate.json';script=root/'lib'/'updater.py'
            installed_manager=root/'lib'/'manager.py';installed_probe=root/'lib'/'probe.py'
            service=root/'systemd'/'service';timer=root/'systemd'/'timer';calls=[]
            order=[]
            args=manager.build_parser().parse_args(['autoupdate','enable'])
            runner=manager.Runner()
            def record_systemctl(runner,*argv,**kwargs):calls.append(argv)
            def record_prepare(*args,**kwargs):order.append('adopt')
            def record_names(*args,**kwargs):order.append('enumerate');return ['node-a','node-b']
            live={'node-a':{'git_commit':'1'*40,'build_version':123},
                  'node-b':{'git_commit':'2'*40,'build_version':124}}
            def record_versions(layout,names):order.append('versions');return live
            def record_check(*args,**kwargs):order.append('check');return SimpleNamespace(returncode=0,stdout=b'',stderr=b'')
            with patch.object(manager,'require_root'), \
                 patch.object(manager,'prepare_primary_node',side_effect=record_prepare), \
                 patch.object(manager,'active_node_names',side_effect=record_names), \
                 patch.object(manager,'active_node_versions',side_effect=record_versions), \
                 patch.object(manager,'AUTOUPDATE_CONFIG',config),patch.object(manager,'AUTOUPDATE_SCRIPT',script), \
                 patch.object(manager,'AUTOUPDATE_MANAGER',installed_manager),patch.object(manager,'AUTOUPDATE_PROBE',installed_probe), \
                 patch.object(manager,'AUTOUPDATE_SERVICE_PATH',service),patch.object(manager,'AUTOUPDATE_TIMER_PATH',timer), \
                 patch.object(manager,'systemctl',side_effect=record_systemctl), \
                 patch.object(manager.subprocess,'run',side_effect=record_check) as check:
                manager.cmd_autoupdate(args,manager.Layout(),runner)
            self.assertEqual(json.loads(config.read_text()),{
                'enabled':True,'names':['node-a','node-b'],'minimum_build_version':124,
                'minimum_sources':['1'*40,'2'*40],
            })
            self.assertTrue(script.is_file());self.assertTrue(installed_manager.is_file());self.assertTrue(installed_probe.is_file())
            self.assertTrue(service.is_file());self.assertTrue(timer.is_file())
            self.assertEqual(check.call_args.args[0][1],str(script))
            self.assertEqual(order,['enumerate','versions','check','adopt'])
            self.assertEqual(calls,[('daemon-reload',),('enable','--now',manager.AUTOUPDATE_TIMER)])

    def test_enable_failure_restores_opt_in_and_does_not_adopt_or_install_units(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);config=root/'etc'/'autoupdate.json';script=root/'lib'/'updater.py'
            installed_manager=root/'lib'/'manager.py';installed_probe=root/'lib'/'probe.py'
            service=root/'systemd'/'service';timer=root/'systemd'/'timer'
            args=manager.build_parser().parse_args(['autoupdate','enable'])
            live={'node-a':{'git_commit':'1'*40,'build_version':124}}
            with patch.object(manager,'require_root'),patch.object(manager,'prepare_primary_node') as adopt, \
                 patch.object(manager,'active_node_names',return_value=['node-a']), \
                 patch.object(manager,'active_node_versions',return_value=live), \
                 patch.object(manager,'AUTOUPDATE_CONFIG',config),patch.object(manager,'AUTOUPDATE_SCRIPT',script), \
                 patch.object(manager,'AUTOUPDATE_MANAGER',installed_manager),patch.object(manager,'AUTOUPDATE_PROBE',installed_probe), \
                 patch.object(manager,'AUTOUPDATE_SERVICE_PATH',service),patch.object(manager,'AUTOUPDATE_TIMER_PATH',timer), \
                 patch.object(manager,'systemctl') as systemctl, \
                 patch.object(manager.subprocess,'run',return_value=SimpleNamespace(returncode=1,stdout=b'',stderr=b'outdated')):
                with self.assertRaisesRegex(manager.PreflightError,'package verification failed'):
                    manager.cmd_autoupdate(args,manager.Layout(),manager.Runner())
            self.assertEqual(json.loads(config.read_text()),{'enabled':False,'names':['node-a']})
            self.assertTrue(all(not path.exists() for path in (script,installed_manager,installed_probe,service,timer)))
            adopt.assert_not_called();systemctl.assert_not_called()

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

    def test_drained_node_public_url_update_restores_previous_env_on_rollback(self):
        with tempfile.TemporaryDirectory() as d, ExitStack() as stack:
            root=Path(d);layout=manager.Layout(opt_root=root/'opt',config_root=root/'config')
            old=layout.versions_root()/'old-1';old.mkdir(parents=True)
            manager.switch_current(layout,old)
            manager.write_env_file(layout.env_file('one'),{
                'CTL_TOKEN':'unit-only','PUBLIC_ORIGIN':'https://app.example.test',
                'GPTADMIN_NODE_PUBLIC_URL':'https://old.example.test'})
            def extract(package,arch,target):
                target.mkdir();return {'git_commit':'2'*40,'build_version':123}
            for name,value in [('require_root',None),('resolve_instance_name','one'),
                ('read_admin_token','unit-only'),('origin_from_env','https://app.example.test'),
                ('host_arch','amd64'),('check_disk_floor',None),('verify_elf_binary',None),
                ('manifest_summary',{'git_commit':'1'*40}),('drain_begin',True),
                ('drain_wait_ready',{'ready_to_stop':True})]:
                stack.enter_context(patch.object(manager,name,return_value=value))
            stack.enter_context(patch.object(manager,'verify_and_extract',side_effect=extract))
            stack.enter_context(patch.object(manager,'systemctl'))
            healthy=stack.enter_context(patch.object(manager,'wait_healthy',side_effect=[
                manager.RuntimeFailure('new node URL failed'),None]))
            cancel=stack.enter_context(patch.object(manager,'drain_cancel',return_value=True))
            budget=SimpleNamespace(cleanup=unittest.mock.Mock())
            args=SimpleNamespace(name='one',package='unit.tar.gz',wait_secs=20,require_drain=True,
                                 node_public_url='https://new.example.test')
            with self.assertRaisesRegex(manager.RuntimeFailure,'rolled back'):
                manager.update_with_budget(args,layout,manager.Runner(),budget)
            restored=manager.parse_env_file(layout.env_file('one'))
            self.assertEqual(restored['GPTADMIN_NODE_PUBLIC_URL'],'https://old.example.test')
            self.assertEqual(restored['PUBLIC_ORIGIN'],'https://app.example.test')
            self.assertEqual(manager.current_target(layout),old)
            self.assertEqual(healthy.call_count,2);cancel.assert_called_once()

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
