#!/usr/bin/env python3
"""Rotate explicitly inventoried FRP files; keys enter through stdin only.

The JSON plan contains paths, SHA256 pins and formats, never credentials.
This tool does not restart services or change networking. Run as the file owner
or root, validate native FRP configs, then restart only the inventoried units.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import tempfile


def digest(data):
    return hashlib.sha256(data).hexdigest()


def replace_key(raw, kind, previous_digest, key):
    if kind == 'token-file':
        if digest(raw.strip()) != previous_digest:
            raise ValueError('key drift')
        return key + b'\n'
    field = 'FRP_TOKEN' if kind == 'env' else r'(?:auth\.)?token'
    pattern = re.compile(rb'(?m)^(\s*(?:export\s+)?' + field.encode() +
                         rb'\s*=\s*)([^\r\n]*)(\r?)$')
    count = 0

    def change(match):
        nonlocal count
        value = match[2].strip().strip(b'\"').strip(b"'")
        if digest(value) != previous_digest:
            return match[0]
        count += 1
        # Env values and TOML strings are deliberately always quoted.
        return match[1] + b'"' + key + b'"' + match[3]

    result = pattern.sub(change, raw)
    if count != 1:
        raise ValueError('expected exactly one old key')
    return result


def atomic_write(path, raw, mode, uid, gid):
    fd, name = tempfile.mkstemp(prefix='.frp-rotation-', dir=str(path.parent))
    try:
        os.fchmod(fd, mode)
        os.fchown(fd, uid, gid)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        dir_fd = os.open(str(path.parent), os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(dir_fd)
        finally:
            os.close(dir_fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def apply(plan, key, backup):
    if not re.fullmatch(rb'[A-Za-z0-9_-]{43,128}', key):
        raise ValueError('invalid key format')
    entries = plan['files']
    if not 1 <= len(entries) <= 64:
        raise ValueError('invalid file count')
    if len({r['path'] for r in entries}) != len(entries):
        raise ValueError('duplicate path')
    prepared = []
    total = 0
    for row in entries:
        path = Path(row['path'])
        if not path.is_absolute() or '..' in path.parts:
            raise ValueError('invalid path')
        # Refuse symlinks in the entire existing path, including parents.
        if any(p.is_symlink() for p in [path, *path.parents]):
            raise ValueError('symlink refused')
        parent = path.parent.stat()
        if parent.st_uid not in (0, os.getuid()) or parent.st_mode & 0o022:
            raise ValueError('untrusted writable parent')
        exists = path.exists()
        if exists:
            info = path.stat()
            if not stat.S_ISREG(info.st_mode) or info.st_size > 2 * 1024 * 1024:
                raise ValueError('invalid file')
            raw = path.read_bytes()
        else:
            raw = b''
            info = None
        if row.get('sha256') != (digest(raw) if exists else None):
            raise ValueError('file drift')
        kind = row['kind']
        if kind not in ('token-file', 'env', 'toml', 'bezhook-env', 'unit-reference'):
            raise ValueError('unknown format')
        if kind == 'bezhook-env':
            if exists:
                raise ValueError('existing credential file refused')
            changed = b'BEZHOOK_TOKEN=' + key + b'\n'
        elif kind == 'unit-reference':
            if exists:
                raise ValueError('existing drop-in refused')
            changed = b'[Service]\nEnvironmentFile=/etc/frp/client-key.env\n'
        else:
            changed = replace_key(raw, kind, plan['previous_key_sha256'], key)
        total += len(raw)
        if total > 64 * 1024 * 1024:
            raise ValueError('backup budget exceeded')
        prepared.append((row, path, raw, changed, info))
    backup = Path(backup)
    if backup.exists():
        raise ValueError('backup already exists; reconcile instead of replay')
    backup.mkdir(mode=0o700, parents=False)
    records = []
    # Save ALL original bytes before mutating any target.
    for index, (row, path, raw, changed, info) in enumerate(prepared):
        target = backup / str(index)
        atomic_write(target, raw, 0o600, os.getuid(), os.getgid())
        records.append({'path': str(path), 'before_sha256': row['sha256'],
                        'after_sha256': digest(changed), 'backup': str(target),
                        'existed': info is not None})
    atomic_write(backup / 'receipt.json', json.dumps(records).encode(),
                 0o600, os.getuid(), os.getgid())
    for row, path, raw, changed, info in prepared:
        # Recheck immediately before each replace, including inode generation.
        if info:
            now = path.lstat()
            if (now.st_dev, now.st_ino) != (info.st_dev, info.st_ino) or digest(path.read_bytes()) != digest(raw):
                raise ValueError('late file drift; inspect protected backup')
            if (now.st_uid, now.st_gid, now.st_mode) != (info.st_uid, info.st_gid, info.st_mode):
                raise ValueError('late permission drift')
        elif path.exists() or path.is_symlink():
            raise ValueError('new path collision')
        mode = stat.S_IMODE(info.st_mode) if info else 0o600
        # Keep existing service-user/group readability; new secret files are 600.
        atomic_write(path, changed, mode, info.st_uid if info else os.getuid(),
                     info.st_gid if info else os.getgid())
    return records


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plan', required=True)
    parser.add_argument('--backup', required=True)
    args = parser.parse_args()
    try:
        key = sys.stdin.buffer.read(129).strip()
        result = apply(json.loads(Path(args.plan).read_text()), key, args.backup)
        print(json.dumps({'status': 'files_rotated', 'files': result}))
        return 0
    except Exception as error:
        # Exception text, config bytes and credentials never go to stdout/stderr.
        print(json.dumps({'status': 'refused', 'error_class': type(error).__name__}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
