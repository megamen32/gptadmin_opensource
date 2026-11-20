"""Preflight the real Darwin Node archives before release, without loading launchd."""
import argparse
from pathlib import Path
import stat
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from scripts import gptadmin_node_macos_manage as installer
from tools import publish_node_release as publisher


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('source', help='full build commit')
    args = parser.parse_args()
    for arch in ('amd64', 'arm64'):
        archive = args.directory / f'gptadmin-node-darwin-{arch}.tar.gz'
        manifest = publisher.verify_package(archive, arch, args.source, 'darwin')
        print(f'{archive.name} verified by publisher')
        if arch == installer.arch_name():
            with tempfile.TemporaryDirectory() as temp:
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
