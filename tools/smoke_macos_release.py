#!/usr/bin/env python3
"""Run the exact native macOS client bundle in an isolated loopback process."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

try:
    from . import verify_macos_release
except ImportError:
    import verify_macos_release


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--build-dir', required=True, type=Path)
    parser.add_argument('--version', required=True, type=int)
    parser.add_argument('--commit', required=True)
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        raise SystemExit('native macOS is required for this acceptance check')
    arch = {'arm64': 'arm64', 'x86_64': 'x64'}.get(platform.machine())
    if arch is None:
        raise SystemExit('unsupported native macOS architecture')
    verified = verify_macos_release.verify(args.build_dir, 1074)['verified']
    if any(row['build_version'] != args.version or row['git_commit'] != args.commit for row in verified):
        raise SystemExit('package metadata does not match the checked-out source')
    package = args.build_dir / f'gptadmin-macos-{arch}-client.tar.gz'
    with tempfile.TemporaryDirectory(prefix='gptadmin-macos-smoke-') as directory:
        root = Path(directory)
        binary = root / 'shellmcp'
        with tarfile.open(package, 'r:gz') as archive:
            member = next(m for m in archive.getmembers() if m.name in ('bin/shellmcp', './bin/shellmcp'))
            with archive.extractfile(member) as src, binary.open('wb') as dst:
                while chunk := src.read(1024 * 1024):
                    dst.write(chunk)
        binary.chmod(0o755)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        env = {'PATH': '/usr/bin:/bin', 'HOME': directory, 'TMPDIR': directory,
               'SHELL_HOST': '127.0.0.1', 'SHELL_PORT': str(port), 'SHELL_TOKEN': 'ci-local-only',
               'SHELLMCP_MODE': 'webhook', 'SHELLMCP_QUEUE': '0', 'SHELLMCP_HEARTBEAT': '0',
               'SHELL_IDENTITY_DIR': directory, 'SHELL_SPOOL_DIR': str(root / 'spool'),
               'SHELLMCP_SELF_REPAIR_DISABLE': '1'}
        process = subprocess.Popen([str(binary)], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            base = f'http://127.0.0.1:{port}'
            deadline = time.monotonic() + 15
            while True:
                if process.poll() is not None:
                    raise RuntimeError('packaged ShellMCP exited before /version')
                try:
                    with urllib.request.urlopen(base + '/version', timeout=1) as response:
                        version = json.load(response)
                    break
                except (OSError, urllib.error.URLError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError('packaged ShellMCP did not start') from None
                    time.sleep(0.1)
            if version.get('build_version') != args.version or version.get('git_commit') != args.commit:
                raise RuntimeError('running executable version differs from packaged provenance')
            payload = 'macOS stdin canary: проверка\n'
            request = urllib.request.Request(base + '/exec',
                data=json.dumps({'argv': ['/bin/cat'], 'stdin': payload}).encode(),
                headers={'Authorization': 'Bearer ci-local-only', 'Content-Type': 'application/json'})
            with urllib.request.urlopen(request, timeout=10) as response:
                result = json.load(response)
            if result.get('returncode') != 0 or result.get('stdout') != payload:
                raise RuntimeError('packaged ShellMCP failed native argv/stdin execution')
            print(json.dumps({'native_arch': arch, 'build_version': args.version,
                              'git_commit': args.commit, 'argv_stdin': 'passed'}))
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


if __name__ == '__main__':
    main()
