#!/usr/bin/env python3
"""Per-user GPTAdmin Node lifecycle for macOS (launchd)."""
from __future__ import annotations

import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import secrets
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request
import urllib.error

SCHEMA = 'gptadmin.node-package/v1'
DEFAULT_LABEL = 'com.gptadmin.node'
REQUIRED = ('bin/gptadmin-node', 'cli.py', 'scripts/gptadmin_node_manage.py',
            'scripts/gptadmin_node_macos_manage.py', 'scripts/gptadmin_node_probe.py',
            'scripts/gptadmin_node_autoupdate.py', 'scripts/gptadmin_node_macos_autoupdate.py')


class NodeError(RuntimeError):
    pass


def roots():
    home = Path.home()
    label = os.environ.get('GPTADMIN_NODE_MACOS_LABEL', DEFAULT_LABEL)
    if not re.fullmatch(r'com\.[A-Za-z0-9.-]{1,180}', label):
        raise NodeError('invalid launchd label')
    root = Path(os.environ.get('GPTADMIN_NODE_MACOS_ROOT', home / 'Library/Application Support/GPTAdmin/Node'))
    plist = Path(os.environ.get('GPTADMIN_NODE_MACOS_PLIST', home / 'Library/LaunchAgents' / (label + '.plist')))
    return root, plist


def service_label():
    roots()
    return os.environ.get('GPTADMIN_NODE_MACOS_LABEL', DEFAULT_LABEL)


def arch_name():
    arch = platform.machine().lower()
    names = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64', 'amd64': 'amd64'}
    if arch not in names:
        raise NodeError(f'unsupported macOS architecture: {arch}')
    return names[arch]


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def validate_package(path, stage, allow_dirty=False):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 128 * 1024**2:
        raise NodeError('package is missing, symlinked or too large')
    with tarfile.open(path, 'r:gz') as archive:
        members = archive.getmembers()
        if len(members) > 32 or sum(m.size for m in members) > 256 * 1024**2:
            raise NodeError('package exceeds member or expanded-size limit')
        by_name = {m.name: m for m in members}
        if len(by_name) != len(members) or any(m.name.startswith('/') or '..' in Path(m.name).parts or not m.isfile() for m in members):
            raise NodeError('package contains duplicate, unsafe or non-file entries')
        meta = by_name.get('manifest.json')
        if meta is None or meta.size > 65536:
            raise NodeError('package manifest missing or oversized')
        manifest = json.load(archive.extractfile(meta))
        if (manifest.get('schema') != SCHEMA or manifest.get('platform') != 'darwin'
                or manifest.get('arch') != arch_name()
                or (manifest.get('source_dirty') is not False and not allow_dirty)
                or not isinstance(manifest.get('build_version'), int)
                or not re.fullmatch(r'[0-9a-f]{40}', str(manifest.get('git_commit', '')))):
            raise NodeError('package manifest does not match this clean macOS build')
        files = manifest.get('files')
        if not isinstance(files, dict) or set(files) != set(REQUIRED) or set(by_name) != set(files) | {'manifest.json'}:
            raise NodeError('package manifest file closure is invalid')
        for name, expected in files.items():
            raw = archive.extractfile(by_name[name]).read()
            if len(raw) != expected.get('size') or hashlib.sha256(raw).hexdigest() != expected.get('sha256'):
                raise NodeError(f'package digest mismatch: {name}')
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(raw)
            target.chmod(0o755 if name == 'bin/gptadmin-node' else 0o644)
    binary = stage / 'bin/gptadmin-node'
    header = binary.read_bytes()[:8]
    if header[:4] == b'\xcf\xfa\xed\xfe':
        cpu = struct.unpack('<I', header[4:8])[0]
    elif header[:4] == b'\xfe\xed\xfa\xcf':
        cpu = struct.unpack('>I', header[4:8])[0]
    else:
        raise NodeError('Node binary is not a Mach-O executable')
    expected_cpu = {'arm64': 0x0100000C, 'amd64': 0x01000007}[arch_name()]
    if cpu != expected_cpu:
        raise NodeError('Node Mach-O CPU type does not match this Mac')
    return manifest


def read_state(root):
    state = root / 'state.json'
    if not state.exists():
        return {}
    try:
        data = json.loads(state.read_text())
    except (OSError, ValueError):
        raise NodeError('existing node state is unreadable; refusing to replace it') from None
    if not isinstance(data, dict):
        raise NodeError('existing node state is invalid; refusing to replace it')
    return data


def loaded(plist):
    result = subprocess.run(['/bin/launchctl', 'print', f'gui/{os.getuid()}/{service_label()}'], capture_output=True)
    return result.returncode == 0


DRAIN_ENDPOINT = '/admin/api/node/drain'
AUTOUPDATE_LABEL = 'com.gptadmin.node.autoupdate'


def launchd_job_loaded(label):
    result = subprocess.run(['/bin/launchctl', 'print', f'gui/{os.getuid()}/{label}'], capture_output=True)
    return result.returncode == 0


def _file_snapshot(path):
    if not path.exists():
        return None
    return {'data': path.read_bytes(), 'mode': path.stat().st_mode & 0o777}


def _restore_file(path, snapshot):
    if snapshot is None:
        path.unlink(missing_ok=True)
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + '.restore-new')
    temp.write_bytes(snapshot['data'])
    temp.chmod(snapshot['mode'])
    os.replace(temp, path)


def capture_autoupdate_state(root):
    config = root / 'autoupdate.json'
    plist = Path.home() / 'Library/LaunchAgents' / (AUTOUPDATE_LABEL + '.plist')
    return {'config': _file_snapshot(config), 'plist': _file_snapshot(plist),
            'loaded': launchd_job_loaded(AUTOUPDATE_LABEL)}


def restore_autoupdate_state(root, snapshot):
    config = root / 'autoupdate.json'
    plist = Path.home() / 'Library/LaunchAgents' / (AUTOUPDATE_LABEL + '.plist')
    _restore_file(config, snapshot['config'])
    _restore_file(plist, snapshot['plist'])
    active = launchd_job_loaded(AUTOUPDATE_LABEL)
    if snapshot['loaded'] and not active:
        subprocess.run(['/bin/launchctl', 'bootstrap', f'gui/{os.getuid()}', str(plist)], check=True)
    elif active and not snapshot['loaded']:
        subprocess.run(['/bin/launchctl', 'bootout', f'gui/{os.getuid()}/{AUTOUPDATE_LABEL}'], check=True)
    if launchd_job_loaded(AUTOUPDATE_LABEL) != snapshot['loaded']:
        raise NodeError('automatic updater launchd state could not be restored')


def restore_node_service(root, plist, was_loaded, state, plist_snapshot):
    if not was_loaded:
        return
    _restore_file(plist, plist_snapshot)
    if not loaded(plist):
        subprocess.run(['/bin/launchctl', 'bootstrap', f'gui/{os.getuid()}', str(plist)], check=True)
    if not loaded(plist):
        raise NodeError('Node launchd service did not return after failed uninstall')
    env = json.loads((root / 'node.env.json').read_text())
    if (not isinstance(state, dict) or type(state.get('build_version')) is not int
            or not re.fullmatch(r'[0-9a-f]{40}', str(state.get('git_commit', '')))):
        raise NodeError('prior Node version/commit state is unavailable for uninstall recovery')
    wait_ready(env, state)


def drain_request(env, action, timeout=5):
    host = env.get('GPTADMIN_HUB_HOST', '127.0.0.1')
    port = env.get('GPTADMIN_HUB_PORT', '9001')
    token = env.get('CTL_TOKEN')
    if not token:
        raise NodeError('Node drain credential is missing; refusing to stop the running Node')
    request = urllib.request.Request(
        f'http://{host}:{port}{DRAIN_ENDPOINT}',
        data=json.dumps({'action': action}).encode(),
        headers={'Authorization': f'Bearer {token}', 'Content-Type': 'application/json'},
        method='POST')
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.loads(response.read(65536))
            return response.status, payload
    except urllib.error.HTTPError as error:
        return error.code, {}


def drain_begin(env):
    status, _ = drain_request(env, 'begin')
    if not 200 <= status < 300:
        raise NodeError(f'Node drain begin failed: HTTP {status}')


def drain_wait_ready(env, wait_secs=300, poll_interval=1):
    deadline = time.monotonic() + wait_secs
    while time.monotonic() < deadline:
        status, payload = drain_request(env, 'status')
        if not 200 <= status < 300:
            raise NodeError(f'Node drain status failed: HTTP {status}')
        if isinstance(payload, dict) and payload.get('ready_to_stop') is True:
            return
        time.sleep(min(poll_interval, max(0, deadline - time.monotonic())))
    raise NodeError(f'Node drain did not reach ready_to_stop within {wait_secs:.0f}s')


def drain_cancel(env):
    try:
        status, _ = drain_request(env, 'cancel')
        return 200 <= status < 300
    except (OSError, urllib.error.URLError, NodeError, ValueError):
        return False


def write_plist(root, plist):
    version = root / 'current'
    binary = version / 'bin/gptadmin-node'
    envfile = root / 'node.env.json'
    env = json.loads(envfile.read_text())
    values = dict(env)
    values.update({'GPTADMIN_CONFIG_DIR': str(root / 'config'), 'GPTADMIN_ROOT': str(root),
                   'GPTADMIN_NODE_IDENTITY': str(root / 'config/node-identity.json'),
                   'GPTADMIN_NODE_ID': env.get('GPTADMIN_NODE_ID', 'macos-node')})
    values.setdefault('CTL_TOKEN', secrets.token_hex(32))
    values.setdefault('SHELL_TOKEN', secrets.token_hex(32))
    envfile = root / 'node.env.json'
    if values != env:
        pending = envfile.with_suffix('.json.new')
        pending.write_text(json.dumps(values, sort_keys=True, indent=2) + '\n')
        pending.chmod(0o600)
        os.replace(pending, envfile)
    root.joinpath('config').mkdir(parents=True, exist_ok=True)
    root.joinpath('logs').mkdir(parents=True, exist_ok=True)
    service = {'Label': service_label(), 'ProgramArguments': [str(binary)], 'WorkingDirectory': str(root),
               'EnvironmentVariables': values, 'RunAtLoad': True, 'KeepAlive': {'SuccessfulExit': False},
               'ThrottleInterval': 5, 'StandardOutPath': str(root / 'logs/node.log'),
               'StandardErrorPath': str(root / 'logs/node.log')}
    plist.parent.mkdir(parents=True, exist_ok=True)
    temp = plist.with_suffix('.plist.new')
    temp.write_bytes(plistlib.dumps(service, fmt=plistlib.FMT_XML, sort_keys=True))
    temp.chmod(0o600)
    os.replace(temp, plist)


def load_service(plist):
    subprocess.run(['/bin/launchctl', 'bootout', f'gui/{os.getuid()}/{service_label()}'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(30):
        if not loaded(plist):
            break
        time.sleep(0.25)
    else:
        raise NodeError('launchd did not stop the prior Node process')
    last_error = ''
    for attempt in range(8):
        result = subprocess.run(['/bin/launchctl', 'bootstrap', f'gui/{os.getuid()}', str(plist)], capture_output=True, text=True)
        if result.returncode == 0:
            return
        last_error = result.stderr.strip()[:300]
        time.sleep(min(0.25 * (attempt + 1), 1.0))
    raise NodeError('launchd bootstrap failed: ' + last_error)


def wait_ready(env, manifest):
    host = env.get('GPTADMIN_HUB_HOST', '127.0.0.1')
    port = env.get('GPTADMIN_HUB_PORT', '9001')
    url = f'http://{host}:{port}/version'
    for _ in range(30):
        try:
            with urllib.request.urlopen(url, timeout=1) as response:
                version = json.loads(response.read(65536))
            if (response.status == 200 and str(version.get('build_version')) == str(manifest['build_version'])
                    and version.get('git_commit') == manifest['git_commit']):
                return
        except Exception:
            pass
        time.sleep(0.5)
    raise NodeError('launchd Node failed version and commit readback')


def ensure_not_downgrade(candidate, installed):
    if not installed:
        return
    try:
        current_version = installed['build_version']
        current_commit = installed['git_commit']
        candidate_version = candidate['build_version']
        candidate_commit = candidate['git_commit']
    except (KeyError, TypeError):
        raise NodeError('installed Node version state is incomplete; refusing automatic replacement') from None
    if not isinstance(current_version, int) or not isinstance(candidate_version, int):
        raise NodeError('installed or candidate Node version is invalid')
    if candidate_version < current_version:
        raise NodeError('candidate Node release is older than the installed version')
    if candidate_version == current_version and candidate_commit != current_commit:
        raise NodeError('candidate Node release reuses the installed version with different source')


@contextmanager
def update_lock(root):
    root.mkdir(parents=True, exist_ok=True)
    lock_path = root / 'update.lock'
    fd = os.open(lock_path, os.O_RDWR | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        os.close(fd)
        raise NodeError('another Node install or update is already running') from None
    try:
        yield
    finally:
        os.close(fd)


def cmd_install(args):
    root, _ = roots()
    with update_lock(root):
        _cmd_install_locked(args)


def _cmd_install_locked(args):
    root, plist = roots()
    root.mkdir(parents=True, exist_ok=True)
    versions = root / 'versions'
    versions.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.stage-', dir=versions) as temp:
        stage = Path(temp)
        manifest = validate_package(Path(args.package), stage, args.allow_dirty)
        state = read_state(root)
        ensure_not_downgrade(manifest, state)
        version = versions / f"{manifest['git_commit']}-{manifest['build_version']}"
        if version.exists():
            shutil.rmtree(stage)
        else:
            os.rename(stage, version)
    old = root / 'current'
    prior = old.resolve() if old.is_symlink() else None
    existing_env = json.loads((root / 'node.env.json').read_text()) if (root / 'node.env.json').is_file() else {}
    drain_attempted = False
    if prior is not None and loaded(plist):
        try:
            drain_attempted = True
            drain_begin(existing_env)
            drain_wait_ready(existing_env)
        except Exception:
            if drain_attempted and not drain_cancel(existing_env):
                raise NodeError('Node drain failed and cancellation could not be verified; manual recovery required') from None
            raise
    try:
        link = root / 'current.new'
        link.unlink(missing_ok=True)
        link.symlink_to(version.relative_to(root))
        os.replace(link, old)
        state = read_state(root)
        env_path = root / 'node.env.json'
        if not env_path.exists():
            args_env = {'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': '9001',
                        'PUBLIC_ORIGIN': 'http://127.0.0.1:9001',
                        'CTL_TOKEN': secrets.token_hex(32)}
            env_path.write_text(json.dumps(args_env, sort_keys=True, indent=2) + '\n')
            env_path.chmod(0o600)
        write_plist(root, plist)
        load_service(plist)
        wait_ready(json.loads((root / 'node.env.json').read_text()), manifest)
    except Exception as install_error:
        rollback_error = None
        if prior is not None:
            try:
                rollback = root / 'current.rollback'
                rollback.unlink(missing_ok=True)
                rollback.symlink_to(prior)
                os.replace(rollback, old)
                if old.resolve() != prior:
                    raise NodeError('current symlink does not point to the prior version')
                if (not isinstance(state, dict)
                        or type(state.get('build_version')) is not int
                        or not re.fullmatch(r'[0-9a-f]{40}', str(state.get('git_commit', '')))):
                    raise NodeError('prior version/commit is missing from installed state')
                write_plist(root, plist)
                load_service(plist)
                wait_ready(json.loads((root / 'node.env.json').read_text()), state)
            except Exception as error:
                rollback_error = error
        elif old.is_symlink():
            rollback_error = NodeError('prior current target could not be established')
        if rollback_error is not None:
            raise NodeError(
                f'Node install failed ({install_error}); prior Node restore failed verification '
                f'(version/commit): {rollback_error}; manual recovery required') from None
        if drain_attempted and not drain_cancel(existing_env):
            raise NodeError('Node install failed and drain cancellation is unverified; manual recovery required') from None
        raise
    if drain_attempted and not drain_cancel(existing_env):
        raise NodeError('new Node is healthy but drain cancellation is unverified; manual recovery required')
    state.setdefault('schema', 'gptadmin.node-macos-state/v1')
    state['build_version'] = manifest['build_version']
    state['git_commit'] = manifest['git_commit']
    state.setdefault('installed_at', __import__('datetime').datetime.now().astimezone().isoformat())
    state_path = root / 'state.json'
    tmp = state_path.with_suffix('.json.new')
    tmp.write_text(json.dumps(state, sort_keys=True, indent=2) + '\n')
    tmp.chmod(0o600)
    os.replace(tmp, state_path)
    print(json.dumps({'ok': True, 'build_version': state['build_version'], 'git_commit': state['git_commit'],
                      'identity_preserved': (root / 'config/node-identity.json').exists(), 'service': service_label()}))


def cmd_status(_args):
    root, plist = roots()
    state = read_state(root) if root.exists() else {}
    print(json.dumps({'installed': bool(state), 'build_version': state.get('build_version'),
                      'git_commit': state.get('git_commit'), 'launchd_loaded': loaded(plist),
                      'identity_present': (root / 'config/node-identity.json').is_file()}))


def cmd_uninstall(args):
    root, plist = roots()
    with update_lock(root):
        _cmd_uninstall_locked(args, root, plist)


def _cmd_uninstall_locked(args, root, plist):
    node_was_loaded = loaded(plist)
    env = {}
    prior_state = {}
    node_plist_snapshot = _file_snapshot(plist)
    updater_snapshot = capture_autoupdate_state(root)
    if node_was_loaded:
        drain_attempted = False
        try:
            env = json.loads((root / 'node.env.json').read_text())
            if not isinstance(env, dict):
                raise NodeError('Node environment is invalid; refusing to stop the running Node')
            prior_state = read_state(root)
            drain_attempted = True
            drain_begin(env)
            drain_wait_ready(env)
        except Exception as error:
            if drain_attempted and not drain_cancel(env):
                raise NodeError('Node uninstall drain failed and cancellation is unverified; service left running; manual recovery required') from None
            raise error
    try:
        # Do not alter updater state until an active Node has reached drain readiness.
        cmd_autoupdate(argparse.Namespace(action='disable'))
        if node_was_loaded:
            subprocess.run(['/bin/launchctl', 'bootout', f'gui/{os.getuid()}/{service_label()}'], check=True)
            if loaded(plist):
                raise NodeError('launchd still reports the Node service loaded after bootout')
        plist.unlink(missing_ok=True)
        if args.purge:
            shutil.rmtree(root, ignore_errors=True)
    except Exception as uninstall_error:
        compensation_errors = []
        try:
            restore_autoupdate_state(root, updater_snapshot)
        except Exception as error:
            compensation_errors.append(f'updater restore failed: {error}')
        try:
            restore_node_service(root, plist, node_was_loaded, prior_state, node_plist_snapshot)
        except Exception as error:
            compensation_errors.append(f'Node restore failed: {error}')
        if node_was_loaded and not drain_cancel(env):
            compensation_errors.append('drain cancellation could not be verified')
        if compensation_errors:
            raise NodeError(f'uninstall failed ({uninstall_error}); compensation incomplete: '
                            + '; '.join(compensation_errors) + '; manual recovery required') from None
        raise NodeError(f'uninstall failed ({uninstall_error}); updater restored, Node availability verified, '
                        'drain cancelled') from None
    print(json.dumps({'ok': True, 'state_preserved': not args.purge}))


def cmd_autoupdate(args):
    root, _ = roots()
    config = root / 'autoupdate.json'
    plist = Path.home() / 'Library/LaunchAgents/com.gptadmin.node.autoupdate.plist'
    label = 'com.gptadmin.node.autoupdate'
    if args.action == 'status':
        result = subprocess.run(['/bin/launchctl','print',f'gui/{os.getuid()}/{label}'],capture_output=True)
        data = json.loads(config.read_text()) if config.is_file() else {'enabled':False}
        print(json.dumps({'enabled':data.get('enabled') is True,'launchd_loaded':result.returncode==0})); return
    if args.action == 'disable':
        if config.exists(): config.write_text('{"enabled":false}\n'); config.chmod(0o600)
        subprocess.run(['/bin/launchctl','bootout',f'gui/{os.getuid()}/{label}'],capture_output=True)
        if launchd_job_loaded(label):
            raise NodeError('launchd automatic updater remains loaded after disable')
        plist.unlink(missing_ok=True)
        print(json.dumps({'enabled':False})); return
    if args.action == 'run':
        subprocess.run([sys.executable,str(Path(__file__).with_name('gptadmin_node_macos_autoupdate.py'))],check=True,timeout=360); return
    if not (root/'state.json').is_file(): raise NodeError('install Node before enabling automatic updates')
    config.write_text('{"enabled":true}\n'); config.chmod(0o600)
    plist.parent.mkdir(parents=True,exist_ok=True)
    job={'Label':label,'ProgramArguments':[sys.executable,str(Path(__file__).with_name('gptadmin_node_macos_autoupdate.py'))],
         'StartInterval':600,'RunAtLoad':False,'StandardOutPath':str(root/'logs/autoupdate.log'),
         'StandardErrorPath':str(root/'logs/autoupdate.log'),
         'EnvironmentVariables':{'GPTADMIN_NODE_MACOS_ROOT':str(root)}}
    temp=plist.with_suffix('.plist.new');temp.write_bytes(plistlib.dumps(job));temp.chmod(0o600);os.replace(temp,plist)
    subprocess.run(['/bin/launchctl','bootout',f'gui/{os.getuid()}/{label}'],capture_output=True)
    subprocess.run(['/bin/launchctl','bootstrap',f'gui/{os.getuid()}',str(plist)],check=True)
    active=subprocess.run(['/bin/launchctl','print',f'gui/{os.getuid()}/{label}'],capture_output=True)
    if active.returncode: raise NodeError('launchd did not load the automatic updater')
    print(json.dumps({'enabled':True,'interval_seconds':600,'launchd_loaded':True}))


def main():
    if sys.platform != 'darwin':
        raise NodeError('macOS Node lifecycle can only run on macOS')
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    install = sub.add_parser('install', help='install or update a verified macOS package')
    install.add_argument('--package', required=True)
    install.add_argument('--allow-dirty', action='store_true', help='accept an explicitly dirty local QA package')
    install.set_defaults(func=cmd_install)
    status = sub.add_parser('status', help='show installed build and launchd state')
    status.set_defaults(func=cmd_status)
    uninstall = sub.add_parser('uninstall', help='remove service and code; preserve identity by default')
    uninstall.add_argument('--purge', action='store_true')
    uninstall.set_defaults(func=cmd_uninstall)
    automatic = sub.add_parser('autoupdate', help='manage the opt-in daily verified updater')
    automatic.add_argument('action',choices=('status','enable','disable','run'),nargs='?',default='status')
    automatic.set_defaults(func=cmd_autoupdate)
    args = parser.parse_args()
    args.func(args)


if __name__ == '__main__':
    try:
        main()
    except (NodeError, OSError, subprocess.SubprocessError, tarfile.TarError, ValueError) as exc:
        print(f'gptadmin-node-macos: {exc}', file=sys.stderr)
        sys.exit(3)
