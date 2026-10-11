#!/usr/bin/env python3
"""Read admission or delegate tiny Go checks to the canonical foreground guard."""
from __future__ import annotations

import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
HELPER = Path('/home/admin/ServersAdministartion/templates/server100-resource-guard/bounded-foreground.py')
TEMP = ROOT / '.tmp' / 'mcp-relay-check'
MIB, GIB = 1024**2, 1024**3
BUDGET = {'high': 384*MIB, 'max': 512*MIB, 'tasks': 128, 'cpu': 1,
          'temp': 256*MIB, 'files': 20000, 'io_weight': 10, 'wall': 120,
          'uid_reserve': 2*GIB, 'shared_reserve': 128*MIB,
          'host_reserve': 20*GIB, 'psi_max': 0}
SELECTIONS = {'client': ('go-shellmcp', './internal/mcpclient', 'TestScoped|TestMatchingStdio'),
              'hub': ('go-hub', './internal/hub', 'TestConnection|TestChildScope'),
              'server': ('go-shellmcp', './internal/server', 'TestChildConnectionInstance')}


def official_guard():
    spec = importlib.util.spec_from_file_location('gptadmin_official_foreground', HELPER)
    if spec is None or spec.loader is None:
        raise OSError('canonical foreground guard is unavailable')
    helper = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(helper)
    return helper


def storage_usage():
    total = count = 0
    if TEMP.exists():
        if TEMP.is_symlink():
            raise ValueError('project check storage must not be a symlink')
        for directory, folders, names in os.walk(TEMP, followlinks=False):
            for name in folders + names:
                path = Path(directory) / name
                if path.is_symlink():
                    raise ValueError('project check storage contains a symlink')
                count += 1
                if path.is_file():
                    total += path.stat().st_size
    if total >= BUDGET['temp'] or count >= BUDGET['files']:
        raise ValueError('retained project storage leaves no reviewed job allowance')
    return total, count


def snapshot_gate(helper):
    original = helper.current_path()
    uid, native, _, available = helper.snapshot(original)
    used, files = storage_usage()
    budget = {**BUDGET, 'temp': BUDGET['temp']-used, 'files': BUDGET['files']-files}
    # Use the actual canonical policy. In particular, never adopt its newer
    # AutoSeller/Exmanager profiles or relax obsolete default pins ourselves.
    helper.budget(budget)
    result = {'status': 'blocked', 'canary_only': True, 'official_guard': str(HELPER),
              'original_cgroup': original, 'uid': uid, 'native': native,
              'host_available_bytes': available, 'budget': budget,
              'retained_temp_bytes': used, 'retained_temp_files': files}
    try:
        helper.validate(budget, uid, native, available=available)
        # Read-only atomic slot probe. Execution takes only the canonical OFD.
        if not helper.LOCK.is_file():
            raise ValueError('existing canonical host lock is unavailable')
        with helper.LOCK.open('r') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        result['status'] = 'gate_passed'
    except (helper.Held, OSError, ValueError) as error:
        result['reason'] = str(error)
    return result, budget


def command(mode):
    module, package, pattern = SELECTIONS[mode]
    # Fixed payload; its cache, temp and log all stay under the guard's private
    # monitored TMPDIR lease. No own scope, daemon, session relocation or flock.
    script = '\n'.join([
        'set -euo pipefail', 'test -n "${TMPDIR:-}"', 'unset GOFLAGS',
        'mkdir -p "$TMPDIR/cache" "$TMPDIR/go-tmp"',
        'export GOCACHE="$TMPDIR/cache" GOTMPDIR="$TMPDIR/go-tmp"',
        'export GOMAXPROCS=1 GOMEMLIMIT=384MiB GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off',
        'cd ' + shlex.quote(str(ROOT / module)),
        'go test -p 1 -parallel 1 -mod=readonly -count=1 -timeout=90s -run ' +
        shlex.quote(pattern) + ' ' + shlex.quote(package) + ' >"$TMPDIR/go-check.log" 2>&1',
    ])
    return ['/bin/bash', '-c', script]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=SELECTIONS, nargs='?', default='client')
    parser.add_argument('--check-gate', action='store_true', help='read-only actual-policy/pressure/lock probe')
    parser.add_argument('--plan', action='store_true', help='print fixed command and reviewed budget; no payload')
    args = parser.parse_args()
    try:
        helper = official_guard()
        result, budget = snapshot_gate(helper)
        if args.plan:
            result['payload'] = command(args.mode)
        print(json.dumps(result, sort_keys=True))
        if result['status'] != 'gate_passed':
            return 75
        if args.check_gate or args.plan:
            return 0
        # A passed observational gate is not a grant: the canonical helper
        # rechecks, reserves the one OFD, verifies the waiting child, and owns
        # same-generation cleanup before any Go process can start.
        ROOT.joinpath('.tmp').mkdir(exist_ok=True, mode=0o700)
        TEMP.mkdir(exist_ok=True, mode=0o700)
        path = ROOT / '.tmp' / ('mcp-check-budget-' + str(os.getpid()) + '.json')
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(descriptor, 'w') as target:
                json.dump(budget, target)
            return subprocess.call([sys.executable, '-B', str(HELPER), '--budget', str(path),
                                    '--temp-root', str(TEMP), '--', *command(args.mode)])
        finally:
            path.unlink(missing_ok=True)
    except Exception as error:
        # API/auth contents never enter this runner. Unknown policy fails closed.
        print(json.dumps({'status': 'blocked', 'canary_only': True,
                          'reason': 'canonical admission unavailable: ' + type(error).__name__}))
        return 75


if __name__ == '__main__':
    sys.exit(main())
