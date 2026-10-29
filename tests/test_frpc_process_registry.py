"""Fast units: local ownership/duplication logic only; expected <1s/max10s.

Process/network doubles here never constitute HAOS runtime acceptance.
"""
from pathlib import Path
import json
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace
import importlib
from pathlib import Path
from scripts import gptadmin_frpc_processes as owned
from scripts import gptadmin_failover_watchdog as watchdog

ROOT = Path(__file__).resolve().parents[1]
DEPLOY_TARGET = "home" + "assistant"
if not (ROOT / "deploy" / DEPLOY_TARGET).is_dir():
    DEPLOY_TARGET = "server01"
runtime = importlib.import_module(
    f"deploy.{DEPLOY_TARGET}.gptadmin_hub_standby.gptadmin_failover_runtime"
)


class FRPCRegistryTests(unittest.TestCase):
    def test_paths_are_bounded_and_one_config_per_endpoint(self):
        self.assertEqual(owned.config_paths('/lease/frpc.toml', 1), [Path('/lease/frpc.toml')])
        self.assertEqual(len(owned.config_paths('/lease/frpc.toml', 3)), 3)
        for n in (0, 17):
            with self.assertRaises(ValueError):
                owned.config_paths('/lease/frpc.toml', n)

    def test_legacy_multiple_pids_all_observed_and_dead_skipped(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d)/'owners';p.write_text('71\n72\n73\n')
            def inspect(pid, *unused):
                return None if pid == 72 else {'pid':pid,'started':'10','config':'/lease/a'}
            with patch.object(owned, 'inspect', inspect):
                self.assertEqual([r['pid'] for r in owned.read_records(p,'/bin/frpc',[])], [71,73])

    def test_fresh_registry_keeps_generation_and_private_mode(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d)/'owners';r={'pid':71,'started':'10','config':'/lease/a'}
            owned.write_records(p,[r])
            self.assertEqual(p.stat().st_mode & 0o777,0o600)
            with patch.object(owned,'inspect',return_value=r):
                self.assertEqual(owned.read_records(p,'/bin/frpc',[]),[r])
            with patch.object(owned,'inspect',return_value={**r,'started':'11'}):
                with self.assertRaises(ValueError):owned.read_records(p,'/bin/frpc',[])

    def test_reused_pid_with_an_unrelated_generation_is_ignored(self):
        import os
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners'
            owned.write_records(p,[{'pid':os.getpid(),'started':'0','config':'/lease/frpc.toml'}])
            self.assertEqual(owned.read_records(p,'/bin/frpc',[Path('/lease/frpc.toml')]),[])

    def test_reused_pid_still_running_another_frpc_generation_fails_closed(self):
        import os, sys
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners'
            owned.write_records(p,[{'pid':os.getpid(),'started':'0','config':'/lease/frpc.toml'}])
            with self.assertRaisesRegex(ValueError,'generation changed'):
                owned.read_records(p,sys.executable,[Path('/lease/frpc.toml')])

    def test_duplicate_malformed_and_large_registry_fail_closed(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners'
            for raw in ['71\n71\n','private-string-not-a-pid',json.dumps({'version':1,'clients':[{}]}),'x'*8193]:
                p.write_text(raw)
                with self.assertRaises(ValueError):owned.read_records(p,'/bin/frpc',[])

    def test_pending_or_symlink_registry_is_held_before_any_inspection(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners';owned.write_records(p,[],pending=True)
            with patch.object(owned,'inspect') as inspect:
                with self.assertRaises(ValueError):owned.read_records(p,'/bin/frpc',[])
                inspect.assert_not_called()
            link=Path(d)/'link';link.symlink_to(p)
            with self.assertRaises(ValueError):owned.read_records(link,'/bin/frpc',[])

    def test_stop_binds_fd_and_checks_generation_before_signal(self):
        r={'pid':71,'started':'10','config':'/lease/a'}
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners';owned.write_records(p,[r])
            with patch.object(owned,'read_records',return_value=[r]), patch.object(owned.os,'pidfd_open',return_value=901), patch.object(owned,'inspect',return_value={**r,'started':'11'}), patch.object(owned.signal,'pidfd_send_signal') as send, patch.object(owned.os,'close') as close:
                with self.assertRaises(ValueError):owned.stop_records(p,'/bin/frpc',[])
                send.assert_not_called();close.assert_called_once_with(901)
                self.assertTrue(p.exists())

    def test_stop_all_owners_and_remove_only_after_terminal(self):
        rows=[{'pid':n,'started':'10','config':'/lease/a'} for n in (71,72)]
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners';owned.write_records(p,rows)
            with patch.object(owned,'read_records',return_value=rows), patch.object(owned.os,'pidfd_open',side_effect=[901,902]), patch.object(owned,'inspect',side_effect=rows), patch.object(owned.signal,'pidfd_send_signal') as send, patch.object(owned.select,'select',side_effect=[([901],[],[]),([902],[],[])]), patch.object(owned.os,'close'):
                owned.stop_records(p,'/bin/frpc',[])
                self.assertEqual(send.call_count,2);self.assertFalse(p.exists())

    def test_stop_timeout_retains_known_owners(self):
        r={'pid':71,'started':'10','config':'/lease/a'}
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'owners';owned.write_records(p,[r])
            with patch.object(owned,'read_records',return_value=[r]), patch.object(owned.os,'pidfd_open',return_value=901), patch.object(owned,'inspect',return_value=r), patch.object(owned.signal,'pidfd_send_signal'), patch.object(owned.select,'select',return_value=([],[],[])), patch.object(owned.os,'close'):
                with self.assertRaises(ValueError):owned.stop_records(p,'/bin/frpc',[])
                self.assertTrue(p.exists())

    def test_long_outage_does_not_launch_a_second_complete_client_set(self):
        bundle={'tunnel':{'frp':{'token':'unit-only','subdomain':'unit','domain':'example.test','endpoints':['a.example:7000','b.example:7000']}}}
        with tempfile.TemporaryDirectory() as d:
            base=Path(d)/'frpc.toml';args=SimpleNamespace(frpc_config=str(base),frpc_pid_file=str(Path(d)/'owners'),frpc_bin='/bin/frpc',node_id='unit',hub_service='none',frpc_service='none')
            rows=[{'pid':n,'started':'10','config':str(p)} for n,p in zip((71,72),owned.config_paths(base,2))]
            with patch.object(watchdog,'read_records',return_value=rows), patch.object(watchdog.subprocess,'Popen') as launch:
                watchdog.promote(args,{},bundle,{'local_hub_port':9101},False)
                launch.assert_not_called()

    def test_failed_durable_registration_stops_only_new_child_and_keeps_previous_owner(self):
        bundle={'tunnel':{'frp':{'token':'unit-only','subdomain':'unit','domain':'example.test','endpoints':['a.example:7000','b.example:7000']}}}
        for fail_at in (2,4):
            with self.subTest(fail_at=fail_at), tempfile.TemporaryDirectory() as d:
                base=Path(d)/'frpc.toml';pidfile=Path(d)/'owners'
                args=SimpleNamespace(frpc_config=str(base),frpc_pid_file=str(pidfile),frpc_bin='/bin/frpc',node_id='unit',hub_service='none',frpc_service='none')
                from unittest.mock import Mock
                children=[Mock(pid=71),Mock(pid=72)]
                writes=[]
                def persist(path,records,pending=False):
                    writes.append(list(records))
                    if len(writes)==fail_at:raise OSError('unit disk full')
                    owned.write_records(path,records,pending=pending)
                def inspect(pid,*unused):return {'pid':pid,'started':'10','config':str(base)}
                with patch.object(watchdog,'read_records',return_value=[]),patch.object(watchdog.subprocess,'Popen',side_effect=children) as launch,patch.object(watchdog,'inspect',side_effect=inspect),patch.object(watchdog,'write_records',side_effect=persist):
                    with self.assertRaises(OSError):watchdog.promote(args,{},bundle,{'local_hub_port':9101},False)
                child_index=fail_at//2-1
                self.assertEqual(launch.call_count,child_index+1)
                children[child_index].terminate.assert_called_once()
                children[child_index].wait.assert_called_once_with(timeout=5)
                if fail_at==4:
                    children[0].terminate.assert_not_called()
                    self.assertEqual(len(json.loads(pidfile.read_text())['clients']),1)

    def test_unreaped_child_is_killed_or_keeps_durable_hold(self):
        from unittest.mock import Mock
        import subprocess
        bundle={'tunnel':{'frp':{'token':'unit-only','subdomain':'unit','domain':'example.test','endpoints':['a.example:7000']}}}
        for reaped in (True,False):
            with self.subTest(reaped=reaped),tempfile.TemporaryDirectory() as d:
                base=Path(d)/'frpc.toml';pidfile=Path(d)/'owners'
                args=SimpleNamespace(frpc_config=str(base),frpc_pid_file=str(pidfile),frpc_bin='/bin/frpc',node_id='unit',hub_service='none',frpc_service='none')
                child=Mock(pid=71);expired=subprocess.TimeoutExpired('unit',5)
                child.wait.side_effect=[expired,0 if reaped else expired]
                with patch.object(watchdog,'read_records',return_value=[]),patch.object(watchdog.subprocess,'Popen',return_value=child) as launch,patch.object(watchdog,'inspect',side_effect=ValueError('unit inspect unavailable')):
                    with self.assertRaises((ValueError,subprocess.TimeoutExpired)):watchdog.promote(args,{},bundle,{'local_hub_port':9101},False)
                self.assertEqual(launch.call_count,1);child.kill.assert_called_once()
                if reaped:self.assertFalse(json.loads(pidfile.read_text())['pending'])
                else:
                    with self.assertRaises(ValueError):owned.read_records(pidfile,'/bin/frpc',[])

    def test_proxy_cleanup_runs_when_owned_frpc_cleanup_refuses(self):
        from unittest.mock import Mock
        proxy=Mock();proxy.poll.return_value=None
        with patch.object(runtime,'load_options',return_value={}),patch.object(runtime.Path,'is_file',return_value=True),patch.object(runtime.signal,'signal'),patch.object(runtime,'start_proxy',return_value=proxy),patch.object(runtime,'reset_dead_frpc_cooldown',side_effect=ValueError('unit pending')),patch.object(runtime,'stop_frpc',side_effect=ValueError('unit ownership refused')):
            with self.assertRaises(ValueError):runtime.main()
        proxy.terminate.assert_called_once();proxy.wait.assert_called_once_with(timeout=5)

    def test_native_controller_fixture_keeps_exact_process_identity_and_reuses_owner(self):
        # Local ownership integration only; the fixture is not a FRP tunnel.
        import os,shutil,subprocess,time
        if not shutil.which('cc'):self.skipTest('C compiler unavailable for local fixture')
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);binary=root/'frpc';base=root/'frpc.toml';registry=root/'owners';route=root/'route'
            subprocess.run(['cc','-Wall','-Wextra','-Werror','-O2',str(Path(__file__).parent/'e2e/failover/fake-frpc.c'),'-o',str(binary)],check=True,timeout=10)
            args=SimpleNamespace(frpc_config=str(base),frpc_pid_file=str(registry),frpc_bin=str(binary),node_id='unit',hub_service='none',frpc_service='none')
            bundle={'tunnel':{'frp':{'token':'unit-only','subdomain':'unit','domain':'example.test','endpoints':['a.example:7000']}}}
            pids=[]
            try:
                with patch.dict(os.environ,{'E2E_ROUTE_FILE':str(route),'E2E_RUNNER_PID':str(os.getpid())}):
                    watchdog.promote(args,{},bundle,{'local_hub_port':9101},False)
                    first=owned.read_records(registry,str(binary),[base]);pids=[r['pid'] for r in first]
                    watchdog.promote(args,{},bundle,{'local_hub_port':9101},False)
                    self.assertEqual(owned.read_records(registry,str(binary),[base]),first)
                    deadline=time.monotonic()+2
                    while not route.exists() and time.monotonic()<deadline:time.sleep(.01)
                    self.assertTrue(route.exists())
            finally:
                if registry.exists():owned.stop_records(registry,str(binary),[base])
                for pid in pids:
                    try:os.waitpid(pid,0)
                    except ChildProcessError:pass
            self.assertFalse(route.exists());self.assertFalse(registry.exists())


if __name__=='__main__':unittest.main()
