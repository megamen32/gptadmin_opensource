"""Exact Linux FRP child ownership for the existing systemd-free add-on.

Only recorded clients with the configured executable/config and a fresh
PID generation may be observed or stopped. No host-wide process adoption.
"""
from pathlib import Path
import json
import os
import signal
import select
import stat
import time


def config_paths(base, count):
    base = Path(base).absolute()
    if not 1 <= count <= 16:
        raise ValueError('bounded endpoint count required')
    return [base] if count == 1 else [
        base.with_name(f'{base.stem}-{n}{base.suffix}') for n in range(1, count + 1)]


def inspect(pid, binary, configs):
    if type(pid) is not int or pid <= 1:
        raise ValueError('invalid client pid')
    root = Path('/proc') / str(pid)
    try:
        stat = (root / 'stat').read_text().rsplit(')', 1)[1].split()
        if stat[0] == 'Z':
            return None
        args = (root / 'cmdline').read_bytes().rstrip(b'\0').split(b'\0')
        expected = str(Path(binary).resolve())
        allowed = {str(p.absolute()).encode() for p in configs}
        if (root.stat().st_uid != os.getuid() or os.getpgid(pid) != pid
                or str((root / 'exe').resolve()) != expected or len(args) != 3
                or args[0] != str(binary).encode() or args[1] != b'-c' or args[2] not in allowed):
            raise ValueError('recorded pid does not own this FRP client')
        return {'pid': pid, 'started': stat[19], 'config': args[2].decode()}
    except (FileNotFoundError, ProcessLookupError):
        return None


def read_records(path, binary, configs):
    path = Path(path)
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return []
    except OSError:
        raise ValueError('invalid client registry') from None
    try:
        metadata = os.fstat(fd)
        if (not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.getuid()
                or metadata.st_size > 8192):
            raise ValueError('invalid client registry')
        raw = os.read(fd, 8193)
        if len(raw) > 8192:
            raise ValueError('invalid client registry')
    finally:
        os.close(fd)
    # Migration reads legacy lists, but never adopts arbitrary live PIDs.
    if raw.lstrip().startswith(b'{'):
        value = json.loads(raw)
        if not isinstance(value, dict) or value.get('version') != 1 or not isinstance(value.get('clients'), list):
            raise ValueError('invalid client generation registry')
        if value.get('pending'):
            raise ValueError('previous FRP launch requires owner recovery')
        records = value['clients']
    else:
        try:
            records = [{'pid': int(n)} for n in raw.split()]
        except ValueError:
            raise ValueError('invalid client pid registry') from None
    if (any(not isinstance(r, dict) or type(r.get('pid')) is not int for r in records)
            or len(records) > 16 or len({r['pid'] for r in records}) != len(records)):
        raise ValueError('unbounded or duplicate client registry')
    live = []
    for record in records:
        found = inspect(record['pid'], binary, configs)
        if found is None:
            continue
        if record.get('started') and record['started'] != found['started']:
            raise ValueError('recorded client generation changed')
        if record.get('config') and record['config'] != found['config']:
            raise ValueError('recorded client config changed')
        live.append(found)
    return live


def write_records(path, records, pending=False):
    path = Path(path)
    raw = json.dumps({'version': 1, 'clients': records, 'pending': pending}).encode() + b'\n'
    if len(records) > 16 or len(raw) > 8192:
        raise ValueError('bounded client registry required')
    tmp = path.with_name(path.name + '.new')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, 'wb') as f:
            f.write(raw); f.flush(); os.fsync(f.fileno())
        os.replace(tmp, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        tmp.unlink(missing_ok=True)


def stop_records(path, binary, configs, deadline_seconds=5):
    records = read_records(path, binary, configs)
    deadline = time.monotonic() + deadline_seconds
    descriptors = []
    try:
        for record in records:
            try:
                fd = os.pidfd_open(record['pid'])
            except ProcessLookupError:
                continue
            descriptors.append(fd)
            # Bind the descriptor before the second generation observation.
            found = inspect(record['pid'], binary, configs)
            if found is None:
                continue
            if found != record:
                raise ValueError('client changed before stop')
            signal.pidfd_send_signal(fd, signal.SIGTERM)
        for fd in descriptors:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([fd], [], [], remaining)[0]:
                raise ValueError('owned client stop deadline')
        Path(path).unlink(missing_ok=True)
    finally:
        for fd in descriptors:
            os.close(fd)
