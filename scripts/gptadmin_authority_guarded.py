#!/usr/bin/env python3
"""Wrap the unchanged reviewed fleet in a genuine canonical release guard."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import time


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def module(path, expected, name):
    raw = Path(path).read_bytes()
    if sha(raw) != expected:
        raise RuntimeError('reviewed module changed')
    spec = importlib.util.spec_from_loader(name, loader=None, origin=path)
    value = importlib.util.module_from_spec(spec)
    value.__file__ = path
    sys.modules[name] = value
    exec(compile(raw, path, 'exec'), value.__dict__)
    return value


def guarded_class(base, guard):
    class GuardedFleet(base.Fleet):
        def check(self, direction):
            value = guard.check(cleanup=direction != 'forward')
            self.state['canonical_release_guard'] = value
            self.save()

        def quiesce(self, direction, writer):
            self.check(direction)
            return super().quiesce(direction, writer)

        def apply(self, direction):
            self.check(direction)
            return super().apply(direction)

        def authorize_and_start(self, direction, writer):
            self.check(direction)
            return super().authorize_and_start(direction, writer)

        def edge(self, direction, ready):
            self.check(direction)
            return super().edge(direction, ready)

        def finish_readers(self, direction, writer):
            self.check(direction)
            return super().finish_readers(direction, writer)

        def rollback_before_activation(self):
            self.check('cleanup')
            return super().rollback_before_activation()

        def await_launch_gate(self):
            result = super().await_launch_gate()
            guard.check()
            return result

        def node(self, name, step, action, *args):
            if step == 'prelaunch-auxiliary-restore':
                guard.check(cleanup=True)
            return super().node(name, step, action, *args)
    return GuardedFleet


SUCCESS = 'deployed_pending_independent_mcp_acceptance'
RECOVERED = {'restored_before_activation', 'recovered_original_writer_with_fresh_state',
             'prelaunch_aborted_auxiliaries_restored'}


def terminal_fleet(state, expected):
    if state.get('manifest_sha256') != expected:
        return None
    phase = state.get('phase')
    if phase == SUCCESS:
        return {'verified_terminal': True, 'transfer_success': True, 'phase': phase}
    if phase in RECOVERED:
        return {'verified_terminal': True, 'transfer_success': False, 'recovered': True, 'phase': phase}
    return None


def exact_program(path, expected):
    raw = Path(path).read_bytes()
    if sha(raw) != expected:
        raise RuntimeError('reviewed preparation program changed')
    return raw.decode()


def preparation(cfg, guard, api):
    """Existing capture/publication routines retain their separate durable receipts."""
    root = Path(cfg['packet'])
    phase = exact_program(cfg['publication_phase'], cfg['publication_phase_sha256'])
    publish = exact_program(cfg['publish_source'], cfg['publish_source_sha256'])
    ssh = cfg['publication_ssh_argv']
    report = {'operation_id': cfg['operation_id'], 'manifest_sha256': cfg['fleet_manifest_sha256'],
              'guard_id': guard.guard_id, 'fence': cfg['fence'], 'started_at': time.time()}
    api.durable(root / 'guarded-preparation-intent.json', report)
    guard.check()
    capture = subprocess.run(ssh + [shlex.join(['python3', '-c', phase, 'capture-pause'])],
                             capture_output=True, timeout=600)
    captured = json.loads(capture.stdout)
    report['capture'] = {'returncode': capture.returncode, 'result': captured}
    api.durable(root / 'guarded-capture-result.json', report['capture'])
    if capture.returncode:
        no_pause = captured.get('status') == 'capture_failed_no_pause'
        restored = captured.get('status') == 'pause_failed_restoration_attempted' and all_nodes_ok(captured.get('restore_readback'))
        if not (no_pause or restored):
            raise api.GuardRefused('capture outcome requires exact reconciliation')
        report.update(status='preparation_recovered', verified_terminal=True, transfer_success=False)
    else:
        if captured.get('status') != 'captured_and_paused':
            raise api.GuardRefused('capture did not prove a paused preparation')
        try:
            guard.check()
        except Exception:
            guard.check(cleanup=True)
            restored = subprocess.run(ssh + [shlex.join(['python3', '-c', phase, 'restore'])],
                                      capture_output=True, timeout=600)
            value = json.loads(restored.stdout)
            report['restore'] = {'returncode': restored.returncode, 'result': value}
            api.durable(root / 'guarded-prepublication-restore.json', report['restore'])
            if restored.returncode or value.get('status') != 'restored' or not all_nodes_ok(value.get('readback')):
                raise api.GuardRefused('prepublication restore needs reconciliation')
            report.update(status='preparation_recovered', verified_terminal=True, transfer_success=False)
        else:
            published = subprocess.run(['python3', '-c', publish, captured['receipt_sha256'], phase],
                                       capture_output=True, timeout=600)
            value = json.loads(published.stdout)
            report['publication'] = {'returncode': published.returncode, 'result': value}
            api.durable(root / 'guarded-publication-result.json', report['publication'])
            if published.returncode == 0 and value.get('status') == 'published':
                report.update(status='preparation_published', verified_terminal=True, transfer_success=True)
            else:
                restoration = value.get('restore', {})
                state = restoration.get('stdout', {})
                if restoration.get('returncode') != 0 or state.get('status') != 'restored' or not all_nodes_ok(state.get('readback')):
                    raise api.GuardRefused('publication failure requires exact reconciliation')
                report.update(status='preparation_recovered', verified_terminal=True, transfer_success=False)
    report['guard_finished'] = guard.finish(report)
    report['lease_released'] = False
    receipt_path = root / 'guarded-preparation-result.json'
    api.durable(receipt_path, report)
    print(json.dumps({'status': report['status'], 'operation_id': cfg['operation_id'],
                      'manifest_sha256': cfg['fleet_manifest_sha256'], 'receipt_path': str(receipt_path),
                      'receipt_sha256': sha(receipt_path.read_bytes()), 'guard_finished': report['guard_finished'],
                      'capture_receipt_sha256': captured.get('receipt_sha256'),
                      'publication_receipt_sha256': report.get('publication', {}).get('result', {}).get('receipt_sha256'),
                      'main_commit': report.get('publication', {}).get('result', {}).get('main_commit'),
                      'lease_released': False}))
    return 0 if report['transfer_success'] else 1


def all_nodes_ok(value):
    return (isinstance(value, dict) and set(value) == {'100', '44', '88', 'mac_mini', 'mac_intel', 'haos'}
            and all(v.get('returncode') == 0 and v.get('same_runtime') is True for v in value.values()))


def launch(cfg, config_path, config_sha, api):
    """One explicit new wrapper ExecStart; retain original task/PID/root gate."""
    manifest_raw = api.checked(cfg['fleet_manifest'], cfg['fleet_manifest_sha256'], True)
    manifest = json.loads(manifest_raw)
    # Fresh claim check between the two guarded effects. No launcher retry.
    check = api.Channel(api.remote_argv(cfg, config_path, config_sha, 'check', 'prelaunch'))
    try:
        lease = check.call({'action': 'check'})
        if lease.get('owner') != cfg['owner'] or lease.get('fence') != cfg['fence'] or lease.get('expiresAt', 0) <= time.time() * 1000:
            raise api.GuardRefused('prelaunch claim is no longer live and owned')
    finally:
        check.close()
    claim = Path(manifest['launcher_receipt_path'])
    request = {'schema': 'gptadmin.authority-launch-dispatch/v1', 'manifest_sha256': cfg['fleet_manifest_sha256'],
               'operation_id': manifest['operation_id'], 'unit': manifest['external_unit'],
               'wrapper_sha256': cfg['wrapper_sha256'], 'guard_config_sha256': config_sha,
               'status': 'dispatching', 'at': time.time()}
    api.durable(claim, request)
    program = r'''
import pathlib,hashlib,json,subprocess,sys,time
path,expected,config,csha,wrapper,wsha=sys.argv[1:]
raw=pathlib.Path(path).read_bytes();assert hashlib.sha256(raw).hexdigest()==expected
m=json.loads(raw);unit=m['external_unit']
assert hashlib.sha256(pathlib.Path(config).read_bytes()).hexdigest()==csha
assert hashlib.sha256(pathlib.Path(wrapper).read_bytes()).hexdigest()==wsha
prior=subprocess.run(['systemctl','show',unit,'-p','LoadState','--value'],capture_output=True,text=True,check=True).stdout.strip()
assert prior=='not-found','existing external launch must be reconciled'
argv=['sudo','-n','systemd-run','--unit='+unit,'--uid=roomhacker','--property=Type=exec','--property=Restart=no','--property=KillMode=mixed','--property=TimeoutStopSec=20s','--working-directory='+str(pathlib.Path(path).parent),'/usr/bin/python3',wrapper,'--config',config,'--config-sha256',csha,'--fleet']
r=subprocess.run(argv,capture_output=True,text=True,timeout=25);assert r.returncode==0,'external dispatch did not confirm'
deadline=time.monotonic()+5
while time.monotonic()<deadline:
 raw=subprocess.run(['systemctl','show',unit,'-p','MainPID','-p','ActiveState','-p','ControlGroup'],capture_output=True,text=True,check=True).stdout
 values=dict(line.split('=',1) for line in raw.splitlines())
 if values.get('ActiveState')=='active' and int(values.get('MainPID','0'))>0:break
 time.sleep(.1)
assert values.get('ActiveState')=='active' and int(values.get('MainPID','0'))>0
assert values['ControlGroup'].endswith('/'+unit)
print(json.dumps({'operation_id':m['operation_id'],'manifest_sha256':expected,'unit':unit,'main_pid':int(values['MainPID']),'cgroup':values['ControlGroup'],'phase':'waiting_for_operator_gate','wrapper_sha256':wsha,'guard_config_sha256':csha}))
'''
    argv = manifest['launcher_ssh_argv'] + [shlex.join(['python3', '-c', program,
            manifest['manifest_on44'], cfg['fleet_manifest_sha256'], config_path, config_sha,
            cfg['wrapper'], cfg['wrapper_sha256']])]
    result = subprocess.run(argv, capture_output=True, timeout=40)
    if result.returncode:
        raise api.GuardRefused('external launch outcome unknown; retain its one dispatch claim')
    receipt = json.loads(result.stdout)
    if any(receipt.get(k) != request[k] for k in ('operation_id', 'manifest_sha256', 'unit', 'wrapper_sha256', 'guard_config_sha256')) or not receipt.get('main_pid'):
        raise api.GuardRefused('external wrapper binding mismatch')
    request.update(status='dispatched', receipt=receipt)
    api.durable(Path(str(claim) + '.guarded-result.json'), request)
    print(json.dumps(receipt))
    return 0


def run_fleet(cfg, guard, api):
    root = Path(cfg['packet'])
    manifest = json.loads(api.checked(cfg['fleet_manifest'], cfg['fleet_manifest_sha256'], True))
    module(str(root / 'gptadmin_authority_controller.py'), manifest['controller_module_sha256'], 'gptadmin_authority_controller')
    base = module(str(root / 'gptadmin_authority_fleet.py'), manifest['fleet_sha256'], 'gptadmin_authority_fleet')
    runner = guarded_class(base, guard)(cfg['fleet_manifest'], cfg['fleet_manifest_sha256'])
    error = None
    try:
        runner.run()
    except Exception as exc:
        error = type(exc).__name__
    transaction = runner.root / 'transaction.json'
    raw = transaction.read_bytes() if transaction.exists() else None
    state = json.loads(raw) if raw is not None else runner.state
    if raw is not None and state != runner.state:
        raise api.GuardRefused('durable fleet outcome differs from the current runner')
    terminal = terminal_fleet(state, cfg['fleet_manifest_sha256'])
    report = {'operation_id': cfg['operation_id'], 'manifest_sha256': cfg['fleet_manifest_sha256'],
              'guard_id': guard.guard_id, 'fence': cfg['fence'], 'phase': state.get('phase'),
              'error_class': error, 'transaction_sha256': sha(raw) if raw is not None else None}
    if terminal:
        report.update(terminal)
        report['guard_finished'] = guard.finish(terminal)
        # The claim is kept for independent public acceptance and an explicit release.
        report['lease_released'] = False
    else:
        report.update(verified_terminal=False, requires_reconciliation=True, lease_released=False)
    api.durable(root / 'guarded-fleet-result.json', report)
    print(json.dumps(report))
    return 0 if terminal and terminal.get('transfer_success') and not error else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('--config-sha256', required=True)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--preparation', action='store_true')
    mode.add_argument('--launch', action='store_true')
    mode.add_argument('--fleet', action='store_true')
    args = parser.parse_args()
    raw = Path(args.config).read_bytes()
    if sha(raw) != args.config_sha256:
        raise RuntimeError('guard configuration changed')
    initial = json.loads(raw)
    api = module(initial['module'], initial['module_sha256'], 'gptadmin_authority_release_guard')
    cfg = api.configuration(args.config, args.config_sha256)
    if args.launch:
        return launch(cfg, args.config, args.config_sha256, api)
    guard = api.GuardWindow(cfg, args.config, args.config_sha256, 'preparation' if args.preparation else 'fleet')
    try:
        guard.open()
        return preparation(cfg, guard, api) if args.preparation else run_fleet(cfg, guard, api)
    except Exception as error:
        # This is failure evidence, never a substitute for a canonical Finished.
        api.durable(Path(cfg['packet']) / ('guarded-' + guard.effect + '-failure.json'),
                    {'operation_id': cfg['operation_id'], 'manifest_sha256': cfg['fleet_manifest_sha256'],
                     'guard_id': guard.guard_id, 'guard_finished': guard.finished,
                     'error_class': type(error).__name__, 'requires_reconciliation': True,
                     'lease_released': False, 'at': time.time()})
        raise
    finally:
        guard.close()


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except Exception as error:
        print('authority guarded effect: ' + type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
