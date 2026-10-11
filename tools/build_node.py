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
PAYLOAD = ('cli.py', 'scripts/gptadmin_node_manage.py', 'scripts/gptadmin_node_probe.py')


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
        files = {'bin/gptadmin-node': binary}
        files.update({name: ROOT / name for name in PAYLOAD})
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
                info.mode = 0o755 if name == 'bin/gptadmin-node' else 0o644
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
