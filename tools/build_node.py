#!/usr/bin/env python3
"""Build a portable Linux Node package; no release service or owner host needed."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform as platform_module
import pwd
import re
import secrets
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = 'gptadmin.node-package/v1'
PAYLOAD = ('cli.py', 'scripts/gptadmin_node_manage.py', 'scripts/gptadmin_node_probe.py',
           'scripts/gptadmin_node_autoupdate.py')
DARWIN_PAYLOAD = (*PAYLOAD, 'scripts/gptadmin_node_macos_manage.py',
                  'scripts/gptadmin_node_macos_autoupdate.py')
SHELLMCP_MAIN = './cmd/shellmcp-go'
SHELLMCP_SOURCE = 'go-shellmcp'
PROXY_ROUTE_UNIT_SOURCE = 'scripts/systemd/gptadmin-proxy-route-relay02.service'
PROXY_ROUTE_UNIT_PACKAGE_PATH = 'systemd/gptadmin-proxy-route-relay02.service'


def build_admin_ui() -> dict[str, Path]:
    """Build the served React UI and return its package-relative files."""
    ui_root = ROOT / 'admin-ui'
    env = dict(os.environ)
    if env.get('GPTADMIN_ADMIN_UI_DEPS_READY') != '1':
        subprocess.run(['npm', 'ci', '--prefer-offline', '--no-audit', '--no-fund'],
                       cwd=ui_root, env=env, check=True)
    subprocess.run(['npm', 'run', 'build', '--', '--base=/admin/'],
                   cwd=ui_root, env=env, check=True)
    dist = ui_root / 'dist'
    index = dist / 'index.html'
    if not index.is_file() or '/admin/assets/' not in index.read_text():
        raise RuntimeError('admin-ui build is missing index.html or the /admin/ asset base')
    files = {}
    for path in sorted(dist.rglob('*')):
        if path.is_symlink():
            raise RuntimeError(f'admin-ui build contains a symlink: {path.relative_to(dist)}')
        if path.is_file():
            name = (Path('public/admin') / path.relative_to(dist)).as_posix()
            files[name] = path
    if 'public/admin/index.html' not in files or len(files) < 2:
        raise RuntimeError('admin-ui build has no index or asset files')
    return files


def digest(path: Path) -> str:
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def native_arch() -> str | None:
    return {'x86_64': 'amd64', 'amd64': 'amd64',
            'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform_module.machine().lower())


def verify_go_binary_metadata(binary: Path, *, command_path: str,
                              goos: str, goarch: str) -> dict:
    """Require deterministic Go metadata before a binary enters a manifest."""
    result = subprocess.run(['go', 'version', '-m', str(binary)], capture_output=True,
                            text=True, check=False, timeout=15)
    if result.returncode:
        raise RuntimeError(f'go version -m failed for {binary.name}')
    command = None
    settings = {}
    for line in result.stdout.splitlines():
        fields = line.split('\t')
        if len(fields) >= 3 and fields[1] == 'path':
            command = fields[2]
        elif len(fields) >= 3 and fields[1] == 'build' and '=' in fields[2]:
            key, value = fields[2].split('=', 1)
            settings[key] = value
    if command != command_path:
        raise RuntimeError(f'{binary.name} command path mismatch: {command!r}')
    if settings.get('GOOS') != goos or settings.get('GOARCH') != goarch:
        raise RuntimeError(f'{binary.name} target metadata mismatch')
    if settings.get('vcs.modified') == 'true' or 'vcs.revision' in settings:
        raise RuntimeError(f'{binary.name} contains ambient VCS metadata; explicit provenance required')
    return {'command': command, 'goos': settings.get('GOOS'),
            'goarch': settings.get('GOARCH'), 'buildvcs': 'disabled'}


def probe_node_binary(binary: Path, *, commit: str, version: int) -> dict:
    """Boot the native Node in a synthetic root and bind /version to the manifest."""
    base = ROOT / '.tmp' / 'node-provenance'
    base.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='probe-', dir=base) as temp:
        root = Path(temp)
        for name in ('config', 'state', 'spool', 'outbox', 'home'):
            (root / name).mkdir(mode=0o700)
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            probe.bind(('127.0.0.1', 0))
            port = probe.getsockname()[1]
        token = secrets.token_hex(32)
        origin = f'http://127.0.0.1:{port}'
        env = {
            'PATH': '/usr/bin:/bin', 'HOME': str(root / 'home'), 'LANG': 'C.UTF-8',
            'GPTADMIN_HUB_HOST': '127.0.0.1', 'GPTADMIN_HUB_PORT': str(port),
            'GPTADMIN_ROOT': str(root), 'GPTADMIN_CONFIG_DIR': str(root / 'config'),
            'CTL_TOKEN': token, 'SHELL_TOKEN': token, 'PUBLIC_ORIGIN': origin,
            'MCP_RESOURCE': origin, 'GPTADMIN_AUTH_MODE': 'writer',
            'GPTADMIN_NODE_PEERS': '{}', 'SHELL_NAME': 'build-provenance-probe',
            'SHELL_IDENTITY_DIR': str(root / 'state'),
            'SHELL_SPOOL_DIR': str(root / 'spool'),
            'SHELL_OUTBOX_DIR': str(root / 'outbox'),
            'SHELL_DEFAULT_USER': pwd.getpwuid(os.getuid()).pw_name,
            'SHELL_DEFAULT_HOME': str(root / 'home'),
            'SHELL_DEFAULT_CWD': str(root / 'home'),
            'SHELLMCP_SELF_REPAIR_DISABLE': '1', 'ROOTD_AUTO_UPDATE': '0',
            'GOMAXPROCS': '1',
        }
        process = subprocess.Popen([str(binary)], env=env, stdout=subprocess.DEVNULL,
                                   stderr=subprocess.PIPE, text=True, start_new_session=True)
        deadline = time.monotonic() + 20
        observed = None
        try:
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    break
                try:
                    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
                            origin + '/healthz', timeout=1) as response:
                        health = json.load(response)
                    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
                            origin + '/version', timeout=1) as response:
                        observed = json.load(response)
                    if health.get('ok') is True:
                        break
                except (OSError, ValueError, urllib.error.URLError):
                    time.sleep(0.05)
            if observed is None:
                detail = ''
                if process.poll() is not None and process.stderr is not None:
                    detail = process.stderr.read()[-1000:]
                raise RuntimeError(f'native Node provenance probe failed: {detail}')
            if (observed.get('git_commit') != commit
                    or str(observed.get('build_version')) != str(version)):
                raise RuntimeError(
                    f'native Node reports {observed!r}; expected commit={commit} build={version}')
            request = urllib.request.Request(origin + '/admin/api/node/handoff',
                                             headers={'Authorization': 'Bearer ' + token})
            try:
                urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=2)
                raise RuntimeError('non-activated Node unexpectedly accepted handoff')
            except urllib.error.HTTPError as error:
                payload = json.loads(error.read(65536))
                if error.code != 412 or 'activated listener' not in str(payload.get('detail', '')):
                    raise RuntimeError(f'handoff capability probe failed: HTTP {error.code} {payload!r}')
            return {'git_commit': observed['git_commit'],
                    'build_version': str(observed['build_version']),
                    'handoff_capable': True}
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=2)


def build(arch: str, output: Path, commit: str, version: int, dirty: bool, platform: str = 'linux') -> dict:
    temp_root = ROOT / '.tmp' / 'node-build'
    temp_root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=f'{arch}-', dir=temp_root) as td:
        stage = Path(td)
        binary = stage / 'bin' / 'gptadmin-node'
        binary.parent.mkdir()
        shellmcp_binary = stage / 'bin' / 'shellmcp'
        gotmp = stage / 'go-tmp'
        gotmp.mkdir()
        flags = '-s -w'
        for package in ('go-hub/internal/hub', 'go-shellmcp/internal/server'):
            flags += f' -X github.com/megamen32/gptadmin/{package}.BuildVersion={version}'
            flags += f' -X github.com/megamen32/gptadmin/{package}.GitCommit={commit}'
        env = dict(os.environ, CGO_ENABLED='0', GOOS=platform, GOARCH=arch,
                   GOTMPDIR=str(gotmp), TMPDIR=str(gotmp))
        subprocess.run(['go', 'build', '-buildvcs=false', '-trimpath', '-ldflags', flags,
                        '-o', str(binary), './cmd/gptadmin-node'],
                       cwd=ROOT / 'go-hub', env=env, check=True)
        verify_go_binary_metadata(binary,
                                  command_path='github.com/megamen32/gptadmin/go-hub/cmd/gptadmin-node',
                                  goos=platform, goarch=arch)
        if platform == 'linux' and arch == native_arch():
            probe_node_binary(binary, commit=commit, version=version)
        files = {'bin/gptadmin-node': binary}
        if platform == 'linux':
            subprocess.run(['go', 'build', '-buildvcs=false', '-trimpath', '-ldflags', flags,
                            '-o', str(shellmcp_binary), SHELLMCP_MAIN],
                           cwd=ROOT / SHELLMCP_SOURCE, env=env, check=True)
            verify_go_binary_metadata(shellmcp_binary,
                                      command_path='github.com/megamen32/gptadmin/go-shellmcp/cmd/shellmcp-go',
                                      goos=platform, goarch=arch)
            proxy_unit = ROOT / PROXY_ROUTE_UNIT_SOURCE
            if proxy_unit.is_symlink() or not proxy_unit.is_file():
                raise RuntimeError(f'proxy route systemd unit is missing or unsafe: {PROXY_ROUTE_UNIT_SOURCE}')
            if proxy_unit.stat().st_size > 256 * 1024:
                raise RuntimeError('proxy route systemd unit exceeds its package size limit')
            unit_text = proxy_unit.read_text()
            if '[Unit]' not in unit_text or '[Service]' not in unit_text or \
                    not any(line.strip() == 'User=admin' for line in unit_text.splitlines()):
                raise RuntimeError('proxy route systemd unit does not match the fixed admin service contract')
            files.update({'bin/shellmcp': shellmcp_binary,
                          PROXY_ROUTE_UNIT_PACKAGE_PATH: proxy_unit})
        payload = PAYLOAD if platform == 'linux' else DARWIN_PAYLOAD
        files.update({name: ROOT / name for name in payload})
        if platform == 'linux':
            files.update(build_admin_ui())
        manifest = {
            'schema': SCHEMA, 'git_commit': commit, 'build_version': version,
            'platform': platform, 'arch': arch, 'source_dirty': dirty,
            'files': {name: {'sha256': digest(path), 'size': path.stat().st_size}
                      for name, path in files.items()},
        }
        manifest_path = stage / 'manifest.json'
        manifest_path.write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
        output.mkdir(parents=True, exist_ok=True)
        archive = output / f'gptadmin-node-{platform}-{arch}.tar.gz'
        pending = stage / archive.name
        with tarfile.open(pending, 'w:gz') as tar:
            for name, path in {'manifest.json': manifest_path, **files}.items():
                info = tar.gettarinfo(str(path), arcname=name)
                info.uid = info.gid = 0
                info.uname = info.gname = ''
                info.mode = 0o755 if name in ('bin/gptadmin-node', 'bin/shellmcp') else 0o644
                with path.open('rb') as stream:
                    tar.addfile(info, stream)
        # Output is constrained to the project .tmp tree, on the same filesystem
        # for the default path. Replace only the named package, never its parent.
        import shutil
        staged_output = archive.with_suffix(archive.suffix + '.new')
        shutil.copyfile(pending, staged_output)
        os.replace(staged_output, archive)
        checksum = digest(archive)
        archive.with_name(archive.name + '.sha256').write_text(f'{checksum}  {archive.name}\n')
        return {'path': str(archive), 'sha256': checksum, 'git_commit': commit,
                'arch': arch, 'source_dirty': dirty}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--arch', action='append', choices=['amd64', 'arm64'],
                        help='Repeat for multiple targets; default both')
    parser.add_argument('--platform', choices=['linux', 'darwin'], default='linux')
    parser.add_argument('--build-version', type=int, default=None,
                        help='explicit package version for controlled local QA; release callers use VERSION')
    parser.add_argument('--output', type=Path, default=ROOT / '.tmp' / 'node-packages')
    parser.add_argument('--allow-dirty', action='store_true',
                        help='Development canary only; manifest records source_dirty=true')
    args = parser.parse_args()
    output = args.output.resolve()
    if not output.is_relative_to(ROOT / '.tmp'):
        parser.error('--output must be inside this project\'s .tmp directory')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=normal'],
                                        cwd=ROOT, text=True).strip())
    if dirty and not args.allow_dirty:
        parser.error('working tree is dirty; commit first or explicitly build a development canary')
    version = int((ROOT / 'VERSION').read_text().strip())
    if args.build_version is not None:
        if args.build_version < 1:
            parser.error('--build-version must be positive')
        version = args.build_version
    default_arch = 'arm64' if args.platform == 'darwin' else 'amd64'
    for arch in dict.fromkeys(args.arch or [default_arch]):
        print(json.dumps(build(arch, output, commit, version, dirty, args.platform)), flush=True)


if __name__ == '__main__':
    main()
