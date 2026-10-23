#!/usr/bin/env python3
"""Build a portable Linux Node package; no release service or owner host needed."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = 'gptadmin.node-package/v1'
PAYLOAD = ('cli.py', 'scripts/gptadmin_node_manage.py', 'scripts/gptadmin_node_probe.py',
           'scripts/gptadmin_node_autoupdate.py')
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


def build(arch: str, output: Path, commit: str, version: int, dirty: bool) -> dict:
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
        env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=arch,
                   GOTMPDIR=str(gotmp), TMPDIR=str(gotmp))
        subprocess.run(['go', 'build', '-trimpath', '-ldflags', flags,
                        '-o', str(binary), './cmd/gptadmin-node'],
                       cwd=ROOT / 'go-hub', env=env, check=True)
        subprocess.run(['go', 'build', '-trimpath', '-ldflags', flags,
                        '-o', str(shellmcp_binary), SHELLMCP_MAIN],
                       cwd=ROOT / SHELLMCP_SOURCE, env=env, check=True)
        proxy_unit = ROOT / PROXY_ROUTE_UNIT_SOURCE
        if proxy_unit.is_symlink() or not proxy_unit.is_file():
            raise RuntimeError(f'proxy route systemd unit is missing or unsafe: {PROXY_ROUTE_UNIT_SOURCE}')
        if proxy_unit.stat().st_size > 256 * 1024:
            raise RuntimeError('proxy route systemd unit exceeds its package size limit')
        unit_text = proxy_unit.read_text()
        if '[Unit]' not in unit_text or '[Service]' not in unit_text or \
                not any(line.strip() == 'User=admin' for line in unit_text.splitlines()):
            raise RuntimeError('proxy route systemd unit does not match the fixed admin service contract')
        files = {
            'bin/gptadmin-node': binary,
            'bin/shellmcp': shellmcp_binary,
            PROXY_ROUTE_UNIT_PACKAGE_PATH: proxy_unit,
        }
        files.update({name: ROOT / name for name in PAYLOAD})
        files.update(build_admin_ui())
        manifest = {
            'schema': SCHEMA, 'git_commit': commit, 'build_version': version,
            'platform': 'linux', 'arch': arch, 'source_dirty': dirty,
            'files': {name: {'sha256': digest(path), 'size': path.stat().st_size}
                      for name, path in files.items()},
        }
        manifest_path = stage / 'manifest.json'
        manifest_path.write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
        output.mkdir(parents=True, exist_ok=True)
        archive = output / f'gptadmin-node-linux-{arch}.tar.gz'
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
    for arch in dict.fromkeys(args.arch or ['amd64', 'arm64']):
        print(json.dumps(build(arch, output, commit, version, dirty)), flush=True)


if __name__ == '__main__':
    main()
