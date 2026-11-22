"""Preflight the real Darwin Node archives before release, without loading launchd."""
import argparse
import importlib.util
from pathlib import Path
import stat
import sys
import tempfile
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from scripts import gptadmin_node_macos_manage as installer
from tools import publish_node_release as publisher


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('source', help='full build commit')
    parser.add_argument('--previous-installer', type=Path, help='installer from the previous published Node release')
    args = parser.parse_args()
    previous = None
    if args.previous_installer:
        spec = importlib.util.spec_from_file_location('previous_darwin_installer', args.previous_installer)
        previous = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(previous)
    for arch in ('amd64', 'arm64'):
        archive = args.directory / f'gptadmin-node-darwin-{arch}.tar.gz'
        manifest = publisher.verify_package(archive, arch, args.source, 'darwin')
        print(f'{archive.name} verified by publisher')
        if previous:
            # This is an archive check. Validate the target architecture even
            # when the GitHub build machine uses a different CPU.
            with tempfile.TemporaryDirectory() as temp, patch.object(previous, 'arch_name', return_value=arch):
                if previous.validate_package(archive, Path(temp)) != manifest:
                    raise ValueError('previous installer and publisher read different manifests')
            print(f'{archive.name} accepted by the previous published macOS installer')
        with tempfile.TemporaryDirectory() as temp, patch.object(installer, 'arch_name', return_value=arch):
            stage = Path(temp)
            if installer.validate_package(archive, stage) != manifest:
                raise ValueError('installer and publisher read different manifests')
            for name, mode in (('bin/gptadmin-node', 0o755),
                               ('bin/gptadmin-mcp-bridge', 0o755),
                               ('public/secret-input/index.html', 0o644)):
                path = stage / name
                if stat.S_IMODE(path.stat().st_mode) != mode:
                    raise ValueError(f'incorrect staged permissions for {name}')
                if installer.digest(path) != manifest['files'][name]['sha256']:
                    raise ValueError(f'incorrect staged contents for {name}')
        print(f'{archive.name} verified by macOS installer')


if __name__ == '__main__':
    main()
