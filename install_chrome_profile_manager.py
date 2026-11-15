#!/usr/bin/env python3
"""Install a standalone Chrome Profile Manager and print its GPTAdmin registration."""
import argparse
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import venv

SOURCE = Path(__file__).resolve().parent


def prepare_state(path):
    try:
        path.mkdir(parents=True, mode=0o700)
    except FileExistsError:
        pass
    else:
        path.chmod(0o700)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError('state directory must be owned by the current user, not a symlink, and mode 0700')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prefix', required=True, help='New absolute installation directory')
    parser.add_argument('--state-dir', required=True, help='Absolute private state directory for the child')
    parser.add_argument('--devtools-entry', help='Absolute path to chrome-devtools-mcp 1.12.0 entry, if installed separately')
    args = parser.parse_args()
    prefix = Path(args.prefix)
    state = Path(args.state_dir)
    if not prefix.is_absolute() or not state.is_absolute() or (args.devtools_entry and not Path(args.devtools_entry).is_absolute()):
        parser.error('all supplied paths must be absolute')
    if sys.platform not in ('linux', 'darwin'):
        parser.error('only Linux and macOS are supported')
    if state.resolve() == prefix.resolve() or prefix.resolve() in state.resolve().parents:
        parser.error('state directory must not be inside the installation directory')
    # Refuse replacement: native messaging manifests may already refer to this path.
    prefix.mkdir(parents=True, exist_ok=False, mode=0o700)
    try:
        prepare_state(state)
        for name in ('chrome_profile_manager_mcp.py', 'chrome_profile_helper.py'):
            shutil.copy2(SOURCE / name, prefix / name)
        shutil.copytree(SOURCE / 'chrome_profile_manager_bridge', prefix / 'chrome_profile_manager_bridge',
                        ignore=shutil.ignore_patterns('__pycache__', '*.pyc', 'QA_EVIDENCE.md'))
        shutil.copy2(SOURCE / 'chrome_profile_manager_requirements.txt', prefix / 'requirements.txt')
        venv.create(prefix / 'venv', with_pip=True)
        python = prefix / 'venv' / 'bin' / 'python'
        subprocess.run([str(python), '-m', 'pip', 'install', '--require-hashes', '--only-binary=:all:',
                        '-r', str(prefix / 'requirements.txt')], check=True, stdout=sys.stderr)
    except BaseException:
        shutil.rmtree(prefix)
        raise
    env = {'CHROME_PROFILE_MANAGER_STATE': str(state)}
    if args.devtools_entry:
        env['CHROME_DEVTOOLS_MCP_ENTRY'] = args.devtools_entry
    config = {'ref': 'chrome-profile-manager', 'transport': 'stdio', 'command': str(python),
              'args': ['-u', str(prefix / 'chrome_profile_manager_mcp.py')], 'env': env, 'enabled': True}
    print(json.dumps({'action': 'upsert', 'config': config}, indent=2))


if __name__ == '__main__':
    main()
