#!/usr/bin/env python3
"""Use the installed canonical release bridge; never create another lease authority."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import shlex
import socket
import stat
import subprocess
import sys
import threading
import time


class GuardRefused(RuntimeError):
    pass


class BridgeRejected(GuardRefused):
    pass


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def occupancy_enabled(settings):
    # Installed c069 defaults this switch to false; an absent key is not consent.
    return isinstance(settings, dict) and settings.get('releaseOccupancyEnabled') is True


def checked(path, expected, private=False):
    path = Path(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or path.resolve() != path or info.st_nlink != 1:
        raise GuardRefused('guard input is not an exact regular file')
    if private and (info.st_uid != os.getuid() or info.st_mode & 0o077):
        raise GuardRefused('guard input is not private to its actual operator')
    raw = path.read_bytes()
    if digest(raw) != expected:
        raise GuardRefused('guard input digest changed')
    return raw


def durable(path, value, exclusive=True):
    path = Path(path)
    flags = os.O_WRONLY | os.O_CREAT | (os.O_EXCL if exclusive else os.O_TRUNC)
    fd = os.open(str(path), flags | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write((json.dumps(value, sort_keys=True, indent=2) + '\n').encode())
        stream.flush()
        os.fsync(stream.fileno())
    fd = os.open(str(path.parent), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def configuration(path, expected):
    cfg = json.loads(checked(path, expected, True))
    if cfg.get('schema') != 'gptadmin.canonical-guard-window/v1':
        raise GuardRefused('unknown guard contract')
    checked(cfg['fleet_manifest'], cfg['fleet_manifest_sha256'], True)
    checked(cfg['module'], cfg['module_sha256'])
    checked(cfg['wrapper'], cfg['wrapper_sha256'])
    if os.getuid() != 1000 or cfg['owner'] != {
            'hostId': 'roomhacker-server-100', 'harness': 'chatgpt',
            'nativeSessionId': '/root/close_gptadmin'}:
        raise GuardRefused('wrong operator identity')
    return cfg


class Channel:
    """One serialized JSON-lines session. No request, stderr or token is logged."""
    def __init__(self, argv):
        self.process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, text=True, bufsize=1)
        self.lines = queue.Queue()
        self.lock = threading.Lock()
        self.failed = False
        def read():
            try:
                for line in self.process.stdout:
                    self.lines.put(line)
            finally:
                self.lines.put(None)
        threading.Thread(target=read, daemon=True).start()

    def call(self, request, timeout=15):
        with self.lock:
            if self.failed or self.process.poll() is not None:
                raise GuardRefused('canonical guard channel unavailable')
            try:
                self.process.stdin.write(json.dumps(request, separators=(',', ':')) + '\n')
                self.process.stdin.flush()
                line = self.lines.get(timeout=timeout)
                if line is None or len(line) > 1024 * 1024:
                    raise GuardRefused('canonical guard response unavailable')
                reply = json.loads(line)
                if reply.get('ok') is not True or 'result' not in reply:
                    raise BridgeRejected('canonical bridge refused: ' + str(reply.get('error', 'invalid_response'))[:100])
                return reply['result']
            except BridgeRejected:
                # A semantic refusal keeps the original HeldGuard channel usable
                # for its permitted cleanup request after lease expiry.
                raise
            except Exception:
                self.failed = True
                raise

    def close(self):
        # An unfinished guard observes EOF and retains its canonical UNKNOWN binding.
        try:
            self.process.stdin.close()
        except (OSError, ValueError):
            pass
        try:
            self.process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                pass


def remote_argv(cfg, config_path, config_sha, mode, effect):
    shim = ('import pathlib,hashlib,sys; p=pathlib.Path(sys.argv[1]); b=p.read_bytes(); '
            'assert hashlib.sha256(b).hexdigest()==sys.argv[2]; '
            'sys.argv=[str(p)]+sys.argv[3:]; exec(compile(b,str(p),"exec"))')
    argv = ['python3', '-c', shim, cfg['module'], cfg['module_sha256'],
            '--config', config_path, '--config-sha256', config_sha,
            '--bridge', mode, '--effect', effect]
    return list(cfg['bridge_ssh_argv']) + [shlex.join(argv)]


class GuardWindow:
    def __init__(self, cfg, config_path, config_sha, effect, channel_factory=Channel):
        self.cfg, self.path, self.sha, self.effect = cfg, config_path, config_sha, effect
        self.channel_factory = channel_factory
        self.channel = None
        self.guard_id = None
        self.renewal_error = None
        self.stop = threading.Event()
        self.thread = None
        self.started = time.monotonic()
        self.finished = False

    def lease(self, value):
        lease = value.get('lease', value)
        if (lease.get('project') != self.cfg['project'] or lease.get('owner') != self.cfg['owner']
                or lease.get('fence') != self.cfg['fence']):
            raise GuardRefused('canonical response lost exact claim identity')
        return lease

    def open(self):
        self.channel = self.channel_factory(remote_argv(self.cfg, self.path, self.sha, 'guard', self.effect))
        result = self.channel.call({'action': 'guard'})
        if result.get('guarded') is not True or not isinstance(result.get('guardId'), str):
            raise GuardRefused('canonical guard was not acquired')
        self.guard_id = result['guardId']
        self.lease(result)
        self.check()
        self.thread = threading.Thread(target=self._renew_loop, daemon=True)
        self.thread.start()
        return self

    def _renew_loop(self):
        while not self.stop.wait(self.cfg['renew_seconds']):
            try:
                self.renew()
            except Exception as error:
                self.renewal_error = type(error).__name__
                return

    def renew(self):
        channel = self.channel_factory(remote_argv(self.cfg, self.path, self.sha, 'renew', self.effect))
        try:
            self.lease(channel.call({'action': 'renew'}))
        finally:
            channel.close()

    def check(self, cleanup=False):
        if self.finished or not self.guard_id or self.channel is None:
            raise GuardRefused('the original canonical guard is not open')
        if not cleanup and (self.renewal_error or time.monotonic() - self.started > self.cfg['forward_window_seconds']):
            raise GuardRefused('forward work forbidden after renewal failure or window expiry')
        # This goes to the same bridge PID and HeldGuard, never a new check process.
        result = self.channel.call({'action': 'check', **({'cleanup': True} if cleanup else {})})
        lease = self.lease(result)
        if not cleanup and lease.get('expiresAt', 0) <= time.time() * 1000:
            raise GuardRefused('canonical claim expired')
        return {'guard_id': self.guard_id, 'fence': self.cfg['fence'],
                'cleanup': cleanup, 'lease': lease}

    def finish(self, terminal):
        if not terminal.get('verified_terminal'):
            raise GuardRefused('unresolved effect cannot finish a canonical guard')
        self.stop.set()
        if self.thread:
            self.thread.join(timeout=30)
            if self.thread.is_alive():
                raise GuardRefused('canonical renewal is still uncertain')
        self.check(cleanup=True)
        result = self.channel.call({'action': 'finished'})
        if result.get('finished') is not True or result.get('guardId') != self.guard_id:
            raise GuardRefused('canonical guard terminal receipt mismatch')
        self.finished = True
        return result

    def close(self):
        self.stop.set()
        if self.thread:
            self.thread.join(timeout=30)
        if self.channel:
            self.channel.close()


def bridge_main(args, cfg):
    """Runs through SSH100; proof and token never leave their existing host."""
    if socket.gethostname() != cfg['owner']['hostId']:
        raise GuardRefused('canonical proof is on a different host')
    cgroup = Path('/proc/self/cgroup').read_text()
    if any(unit in cgroup for unit in cfg['affected_controller_units']):
        raise GuardRefused('canonical guard is inside a stopped controller cgroup')
    proof = json.loads(checked(cfg['proof_path'], cfg['proof_sha256'], True))
    if proof['owner'] != cfg['owner'] or proof['operation_id'] != cfg['operation_id']:
        raise GuardRefused('private proof is for another operation')
    result = proof['result']
    if result.get('acquired') is not True or result['lease']['fence'] != cfg['fence']:
        raise GuardRefused('private claim proof differs')
    settings = json.loads(Path(cfg['settings_path']).read_bytes())
    if not occupancy_enabled(settings):
        raise GuardRefused('canonical coordination setting changed')
    checked(cfg['bridge_binary'], cfg['bridge_sha256'])
    checked(cfg['bridge_config'], cfg['bridge_config_sha256'], True)
    base = {'project': cfg['project'], 'owner': cfg['owner'],
            'token': result['token'], 'fence': cfg['fence']}
    channel = Channel([cfg['bridge_binary'], '--config', cfg['bridge_config']])
    first = True
    terminal = False
    effect = args.effect
    if effect not in ('preparation', 'fleet', 'prelaunch'):
        raise GuardRefused('unknown reviewed effect')
    try:
        for line in sys.stdin:
            request = json.loads(line)
            action = request.get('action')
            if args.bridge == 'guard':
                if first:
                    if request != {'action': 'guard'}:
                        raise GuardRefused('guard must be the first action')
                    durable(Path(cfg['packet']) / ('release-' + effect + '-guard-intent.json'),
                            {'operation_id': cfg['operation_id'], 'manifest_sha256': cfg['fleet_manifest_sha256'],
                             'fence': cfg['fence'], 'effect': effect, 'pid': os.getpid(),
                             'cgroup': cgroup, 'at': time.time(), 'retry_allowed': False})
                    value = channel.call({'action': 'guard', **base})
                    durable(Path(cfg['packet']) / ('release-' + effect + '-guard-admitted.json'), value)
                    first = False
                else:
                    if request not in ({'action': 'check'}, {'action': 'check', 'cleanup': True}, {'action': 'finished'}):
                        raise GuardRefused('unsupported original-channel request')
                    try:
                        value = channel.call(request)
                    except BridgeRejected:
                        print(json.dumps({'ok': False, 'error': 'canonical_request_refused'}), flush=True)
                        continue
                    if action == 'finished':
                        durable(Path(cfg['packet']) / ('release-' + effect + '-guard-finished.json'), value)
                        terminal = True
            else:
                if not first or request != {'action': args.bridge}:
                    raise GuardRefused('one canonical request only')
                value = channel.call({'action': args.bridge, **base,
                                      **({'ttlSeconds': 1800} if args.bridge == 'renew' else {})})
                first = False
                terminal = True
            print(json.dumps({'ok': True, 'result': value}), flush=True)
            if terminal:
                break
    finally:
        channel.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('--config-sha256', required=True)
    parser.add_argument('--bridge', choices=('guard', 'renew', 'check', 'release'), required=True)
    parser.add_argument('--effect', required=True)
    args = parser.parse_args()
    cfg = configuration(args.config, args.config_sha256)
    try:
        bridge_main(args, cfg)
    except Exception as error:
        # Never print request values, private proof, child stderr or tokens.
        print(json.dumps({'ok': False, 'error': type(error).__name__}), flush=True)
        raise SystemExit(1)


if __name__ == '__main__':
    main()
