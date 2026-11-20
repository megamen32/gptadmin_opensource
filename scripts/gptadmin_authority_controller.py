#!/usr/bin/env python3
"""Phase runner outside a GPTAdmin service, using an exact reviewed node manifest.

It does not choose a writer, change ingress, replace binaries, or retry an
uncertain mutation. Every action has an exclusive durable receipt. The Go
helper owns authority validation, file CAS, import, and recovery boundaries.
"""
import argparse
from datetime import datetime, timezone
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import re
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request


class Refused(RuntimeError):
    pass


class BinaryDigestMismatch(Refused):
    """A regular executable exists, but has not reached the reviewed bytes."""


class RuntimeReadinessFailure(Refused):
    def __init__(self, reason, metadata):
        super().__init__(reason)
        self.safe_metadata = metadata


class CommandFailure(Refused):
    """A failed command with bounded metadata, never its argv or output."""
    def __init__(self, reason, metadata):
        super().__init__(reason)
        self.safe_metadata = metadata


def digest(data):
    return hashlib.sha256(data).hexdigest()


def parse_membership_lease(value):
    """Read Go RFC3339Nano leases on Python 3.10, with an explicit timezone."""
    if not isinstance(value, str):
        raise ValueError('membership lease must be an RFC3339 timestamp')
    match = re.fullmatch(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})'
                         r'(?:\.(\d{1,9}))?(Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)', value)
    if not match:
        raise ValueError('membership lease requires an RFC3339 timezone')
    base, fraction, offset = match.groups()
    # datetime has microsecond precision. Rounding down cannot extend a lease;
    # retain the original wire value in the acceptance receipt below.
    fraction = ('.' + fraction[:6].ljust(6, '0')) if fraction else ''
    return datetime.fromisoformat(base + fraction + ('+00:00' if offset == 'Z' else offset))


def sync_dir(path):
    fd = os.open(str(path), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def private_json(path, value, exclusive=False):
    path = Path(path)
    raw = (json.dumps(value, sort_keys=True, indent=2) + '\n').encode()
    if exclusive:
        fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, 'wb') as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
        finally:
            sync_dir(path.parent)
        return
    tmp = path.with_name(path.name + '.pending-' + str(os.getpid()))
    private_json(tmp, value, True)
    os.replace(str(tmp), str(path))
    sync_dir(path.parent)


def read_json(path):
    path = Path(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > 32 * 1024 * 1024:
        raise Refused('non-regular or oversized private input')
    return json.loads(path.read_bytes())


def checked_file(path, expected, executable=False, mismatch_class=Refused):
    path = Path(path)
    info = path.stat()
    if not path.is_absolute() or not stat.S_ISREG(info.st_mode):
        raise Refused('expected an absolute regular file')
    if info.st_mode & 0o022 or (executable and not info.st_mode & 0o111):
        raise Refused('unsafe file mode')
    if digest(path.read_bytes()) != expected:
        raise mismatch_class('reviewed file digest changed: ' + path.name)


def command(argv, timeout=15, env=None, check=True, stage=None):
    started = time.monotonic()
    metadata = {'command': Path(argv[0]).name, 'stage': stage or Path(argv[0]).name,
                'timeout_seconds': timeout, 'timed_out': False, 'returncode': None}
    try:
        result = subprocess.run(argv, env=env, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        metadata.update(timed_out=True, elapsed_seconds=round(time.monotonic() - started, 6))
        raise CommandFailure('command deadline exceeded', metadata) from None
    except OSError as error:
        metadata.update(errno=error.errno, elapsed_seconds=round(time.monotonic() - started, 6))
        raise CommandFailure('command could not start', metadata) from None
    metadata.update(returncode=result.returncode, elapsed_seconds=round(time.monotonic() - started, 6))
    result.safe_metadata = metadata
    if check and result.returncode:
        # Never echo subprocess output: an env-aware subprocess may include
        # credential-bearing diagnostics in an exception or command line.
        raise CommandFailure('command failed: %s rc=%s' % (Path(argv[0]).name, result.returncode), metadata)
    return result


def process_env(pid):
    values = {}
    for item in Path('/proc/%d/environ' % pid).read_bytes().split(b'\0'):
        if b'=' in item:
            key, value = item.split(b'=', 1)
            values[os.fsdecode(key)] = os.fsdecode(value)
    return values


def http_json(origin, token, action=None, path='/admin/api/node/drain'):
    data = None if action is None else json.dumps(action if isinstance(action, dict) else {'action': action}).encode()
    headers = {'Accept': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if data is not None:
        headers['Content-Type'] = 'application/json'
    request = urllib.request.Request(origin + path, data=data, headers=headers,
                                     method='GET' if data is None else 'POST')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=8) as response:
            status, raw = response.status, response.read(1024 * 1024 + 1)
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read(1024 * 1024 + 1)
    if len(raw) > 1024 * 1024:
        raise Refused('oversized local response')
    try:
        value = json.loads(raw)
    except ValueError:
        value = None
    return status, value


class Systemd:
    def __init__(self, manifest):
        self.m = manifest

    def info(self):
        unit = self.m['unit']
        result = command(['systemctl', 'show', unit, '-p', 'MainPID', '-p', 'ControlGroup',
                          '-p', 'ActiveState', '-p', 'FragmentPath', '-p', 'DropInPaths',
                          '-p', 'EnvironmentFiles', '-p', 'ExecStart', '-p', 'SubState',
                          '-p', 'ControlPID', '-p', 'Job', '-p', 'InvocationID'])
        values = {}
        for line in result.stdout.decode().splitlines():
            key, value = line.split('=', 1)
            values.setdefault(key, []).append(value)
        pid = int(values['MainPID'][0])
        group = values['ControlGroup'][0]
        own = Path('/proc/self/cgroup').read_text()
        if group and any(line.split(':', 2)[-1] == group or
                         line.split(':', 2)[-1].startswith(group + '/')
                         for line in own.splitlines()):
            raise Refused('controller is inside the affected service cgroup')
        actual_files = [re.sub(r' \(ignore_errors=(?:yes|no)\)$', '', x)
                        for x in values.get('EnvironmentFiles', [])]
        if actual_files != self.m['environment_files']:
            raise Refused('effective systemd environment source order changed')
        populated = False
        if group:
            events = Path('/sys/fs/cgroup') / group.lstrip('/') / 'cgroup.events'
            if events.exists():
                populated = dict(line.split() for line in events.read_text().splitlines()).get('populated') == '1'
        return {'pid': pid, 'generation': values['InvocationID'][0],
                'active': values['ActiveState'][0], 'sub_state': values['SubState'][0],
                'control_pid': int(values['ControlPID'][0]), 'job': values.get('Job', [''])[0],
                'cgroup': group, 'cgroup_populated': populated,
                'controller_pid': os.getpid(), 'controller_cgroup': own,
                'environment_files': actual_files,
                'unit_metadata_sha256': digest(result.stdout)}

    def stopped(self, value):
        return (not value['pid'] and not value['control_pid'] and value['job'] in ('', '0')
                and value['active'] in ('inactive', 'failed') and value['sub_state'] in ('dead', 'failed')
                and not value['cgroup_populated'])

    def has_node(self, value):
        return bool(value['pid'])

    def environment(self, info):
        if info['pid'] <= 0:
            raise Refused('service has no live environment to capture')
        return process_env(info['pid'])

    def binary(self, info):
        return str(Path('/proc/%d/exe' % info['pid']).resolve())

    def auxiliaries(self):
        values = {}
        for unit in self.m.get('pause_timers', []):
            result = command(['systemctl', 'show', unit, '-p', 'ActiveState', '--value'])
            values[unit] = result.stdout.decode().strip()
        for unit in self.m.get('require_idle_units', []):
            deadline = time.monotonic() + 45
            while True:
                result = command(['systemctl', 'show', unit, '-p', 'MainPID', '-p', 'ControlPID', '-p', 'ActiveState', '-p', 'Job'])
                state = dict(line.split('=', 1) for line in result.stdout.decode().splitlines() if '=' in line)
                if state.get('MainPID', '0') == '0' and state.get('ControlPID', '0') == '0' and not state.get('Job') and state.get('ActiveState') in ('inactive', 'failed'):
                    break
                if time.monotonic() >= deadline:
                    raise Refused('a competing lifecycle unit did not settle: ' + unit)
                time.sleep(.5)
        return values

    def pause(self, states):
        for unit, state in states.items():
            if state == 'active':
                command(['systemctl', 'stop', unit], timeout=15)
        current = self.auxiliaries()
        if any(value not in ('inactive', 'failed') for value in current.values()):
            raise Refused('updater timer did not remain paused or its prior state changed')

    def resume(self, states):
        for unit, state in states.items():
            if state == 'active':
                command(['systemctl', 'start', unit], timeout=15)

    def stop(self):
        # The exact manager's normal SIGTERM/timeout policy remains intact.
        command(['systemctl', 'stop', self.m['unit']], timeout=self.m.get('stop_timeout', 150))
        value = self.info()
        if not self.stopped(value):
            raise Refused('service manager did not confirm stopped state')
        return value

    def start(self):
        command(['systemctl', 'start', self.m['unit']], timeout=30)


class Launchd:
    def __init__(self, manifest):
        self.m = manifest
        self.domain = 'gui/' + str(os.getuid())
        if os.getuid() != manifest['uid']:
            raise Refused('launchd owner UID differs from manifest')

    def job(self, label):
        result = command(['/bin/launchctl', 'print', self.domain + '/' + label], check=False,
                         stage='launchd.print_job')
        if result.returncode:
            # Observed native missing-service response, not a general error.
            expected = ('Could not find service "' + label + '" in domain for user gui: '
                        + str(self.m['uid']))
            lines = result.stderr.decode(errors='replace').splitlines()
            if (result.returncode == 113 and not result.stdout.strip()
                    and lines == ['Bad request.', expected]):
                # A missing or inaccessible domain is not an unloaded job.
                command(['/bin/launchctl', 'print', self.domain], stage='launchd.print_domain')
                return {'loaded': False, 'pid': 0}
            raise CommandFailure('launchd job status is unavailable', result.safe_metadata)
        match = re.search(r'^\s*pid = (\d+)\s*$', result.stdout.decode(), re.M)
        return {'loaded': True, 'pid': int(match.group(1)) if match else 0}

    def process_generation(self, pid):
        result = command(['/bin/ps', '-p', str(pid), '-o', 'lstart='], check=False,
                         stage='launchd.pid_generation')
        if result.returncode == 0 and result.stdout.strip():
            return result.stdout.decode().strip()
        if result.returncode == 1 and not result.stdout.strip() and not result.stderr.strip():
            return None
        raise CommandFailure('process generation is unavailable', result.safe_metadata)

    def info(self):
        value = self.job(self.m['label'])
        generation = self.process_generation(value['pid']) if value['pid'] else None
        if value['pid'] and generation is None:
            # The PID may exit between launchctl and ps. Re-read manager state;
            # a stale loaded job remains loaded and cannot authorize mutation.
            value = self.job(self.m['label'])
            generation = self.process_generation(value['pid']) if value['pid'] else None
        value['generation'] = generation or ''
        ancestor = os.getpid()
        while ancestor > 1:
            if ancestor == value['pid']:
                raise Refused('controller is descended from the affected node')
            result = command(['/bin/ps', '-p', str(ancestor), '-o', 'ppid='],
                             stage='launchd.controller_ancestry')
            ancestor = int(result.stdout.strip())
        value.update({'active': 'active' if value['loaded'] else 'inactive',
                      'controller_pid': os.getpid(), 'domain': self.domain})
        return value

    def environment(self, info):
        if not info['loaded'] or not info['pid']:
            raise Refused('node launchd job is not running')
        plist = plistlib.loads(Path(self.m['plist']).read_bytes())
        values = read_json(self.m['environment_file'])
        if plist.get('EnvironmentVariables') != values:
            raise Refused('canonical environment differs from actual launcher')
        if plist.get('ProgramArguments') != [self.m['binary']]:
            raise Refused('launchd executable changed')
        if not values.get('CTL_TOKEN') or not values.get('SHELL_TOKEN'):
            raise Refused('canonical manager would generate a new credential')
        return dict(os.environ, **values)

    def stopped(self, value):
        return not value['loaded'] and not value['pid']

    def has_node(self, value):
        return bool(value['loaded'] and value['pid'])

    def binary(self, info):
        return self.m['binary']

    def auxiliaries(self):
        result = {}
        for item in self.m.get('pause_launchd', []):
            state = self.wait_updater_idle(item['label'])
            result[item['label']] = state['loaded']
        return result

    def wait_updater_idle(self, label):
        deadline = time.monotonic() + 45
        while True:
            state = self.job(label)
            if not state['pid']:
                return state
            if time.monotonic() >= deadline:
                raise Refused('updater did not finish before the finite pause')
            time.sleep(.5)

    def pause(self, states):
        for label, loaded in states.items():
            state = self.wait_updater_idle(label)
            if state['loaded'] and not loaded:
                raise Refused('an originally unloaded updater was loaded by another actor')
            if loaded and state['loaded']:
                command(['/bin/launchctl', 'bootout', self.domain + '/' + label])
                if self.job(label)['loaded']:
                    raise Refused('updater did not unload')

    def resume(self, states):
        for item in self.m.get('pause_launchd', []):
            if states[item['label']] and not self.job(item['label'])['loaded']:
                command(['/bin/launchctl', 'bootstrap', self.domain, item['plist']])

    def stop(self, expected=None):
        before = self.info()
        if not self.has_node(before) or not before.get('generation'):
            raise Refused('launchd stop requires a proved live process')
        if expected is not None and any(before.get(k) != expected.get(k) for k in ('pid', 'generation', 'domain')):
            raise Refused('launchd fenced process changed at manager boundary')
        self.last_stop_metadata = {'phase': 'bootout_requested', 'prior_pid': before['pid'],
                                   'prior_generation': before['generation']}
        try:
            result = command(['/bin/launchctl', 'bootout', self.domain + '/' + self.m['label']],
                             timeout=30, check=False, stage='launchd.bootout')
            self.last_stop_metadata['command'] = result.safe_metadata
        except CommandFailure as error:
            # Reconcile one requested stop only by reads. Never submit it again.
            self.last_stop_metadata['command'] = error.safe_metadata
        self.last_stop_metadata['phase'] = 'confirming_stop'
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                value = self.info()
                self.last_stop_metadata['manager_loaded'] = value['loaded']
                self.last_stop_metadata['manager_pid'] = value['pid']
                if self.stopped(value):
                    generation = self.process_generation(before['pid'])
                    if generation is None or generation != before['generation']:
                        self.last_stop_metadata.update(phase='stopped_confirmed', prior_process_gone=True)
                        value['stop_confirmation'] = dict(self.last_stop_metadata)
                        return value
            except CommandFailure as error:
                self.last_stop_metadata['readback_failure'] = error.safe_metadata
            time.sleep(.2)
        raise CommandFailure('node launchd stop was not confirmed', dict(self.last_stop_metadata))

    def start(self):
        command(['/bin/launchctl', 'bootstrap', self.domain, self.m['plist']], timeout=15)

    def regenerate(self):
        manager = self.m['manager']
        checked_file(manager, self.m['manager_sha256'])
        root = Path(self.m['root'])
        env = read_json(self.m['environment_file'])
        fixed = {'GPTADMIN_CONFIG_DIR': str(root / 'config'), 'GPTADMIN_ROOT': str(root),
                 'GPTADMIN_NODE_IDENTITY': str(root / 'config/node-identity.json'),
                 'GPTADMIN_MCP_BRIDGE_URL': 'http://127.0.0.1:25991'}
        if any(env.get(k) != v for k, v in fixed.items()) or not env.get('GPTADMIN_NODE_ID'):
            raise Refused('canonical manager fixed fields would change unrelated state')
        if not env.get('CTL_TOKEN') or not env.get('SHELL_TOKEN'):
            raise Refused('canonical manager would generate a new credential')
        spec = importlib.util.spec_from_file_location('reviewed_macos_manager', manager)
        module = importlib.util.module_from_spec(spec)
        sys.path.insert(0, str(Path(manager).parent))
        spec.loader.exec_module(module)
        before = digest(Path(self.m['environment_file']).read_bytes())
        module.write_plist(root, Path(self.m['plist']))
        if digest(Path(self.m['environment_file']).read_bytes()) != before:
            raise Refused('canonical launcher regeneration changed the prepared environment')
        actual = plistlib.loads(Path(self.m['plist']).read_bytes())
        if actual.get('EnvironmentVariables') != env:
            raise Refused('regenerated launchd environment mismatch')
        return {'plist_sha256': digest(Path(self.m['plist']).read_bytes()),
                'environment_sha256': before}


class HAOS:
    """Supervisor lifecycle from the separate SSH add-on, never the node."""
    def __init__(self, manifest, manifest_hash=None):
        self.m = manifest
        self.manifest_hash = manifest_hash
        self.docker = '/usr/local/bin/docker'
        self.client_env = {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
                           'DOCKER_HOST': 'unix:///var/run/docker.sock'}

    def inspect_container(self, name, allow_absent=False):
        result = command([self.docker, 'inspect', '--type', 'container', name],
                         env=self.client_env, check=False, stage='haos.inspect_container')
        if result.returncode:
            # This exact response was retained from a normal Supervisor stop.
            # A daemon, permission, timeout, or ambiguous lookup is not absence.
            missing = ('Error response from daemon: No such container: ' + name + '\n').encode()
            if (allow_absent and result.returncode == 1 and result.stdout == b'[]\n'
                    and result.stderr == missing):
                return None
            raise CommandFailure('HAOS container lookup was not confirmed', result.safe_metadata)
        values = json.loads(result.stdout)
        if not isinstance(values, list) or len(values) != 1 or not isinstance(values[0], dict):
            raise Refused('ambiguous HAOS container')
        return values[0]

    def inspect(self, name):
        return self.inspect_container(name)

    def management_info(self):
        management = self.inspect(self.m['management_container'])
        state = management['State']
        if (os.uname().nodename != management['Config']['Hostname']
                or state.get('Running') is not True or state.get('Restarting')
                or state.get('Dead') or state.get('Pid', 0) <= 0):
            raise Refused('HAOS controller is not in the independently running SSH add-on')
        share = {x['Destination']: x for x in management['Mounts']}.get('/share', {})
        if (share.get('Type') != 'bind' or share.get('Source') != self.m['share_host_path']
                or share.get('RW') is not True):
            raise Refused('external controller work volume is not shared as reviewed')
        return management

    def absent_info(self, management):
        # The initial capture requires a live node. Only this exact private
        # operation's prior capture can establish a removed node's identity.
        path = Path(self.m['operation_dir']) / 'capture.private.json'
        metadata = path.lstat()
        if (not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o077
                or metadata.st_uid != os.geteuid()):
            raise Refused('HAOS absence requires the owned private capture')
        saved = read_json(path)
        if (not self.manifest_hash or saved.get('manifest_sha256') != self.manifest_hash
                or saved['service']['management_container_id'] != management['Id']
                or saved['service']['image_id'] != self.m['image_id']):
            raise Refused('HAOS absence capture or management identity differs')
        old_id = saved['service']['container_id']
        if (not re.fullmatch('[0-9a-f]{64}', old_id) or old_id == management['Id']
                or self.inspect_container(old_id, allow_absent=True) is not None):
            raise Refused('captured HAOS node container still exists or is unproved')
        # A successful daemon query must independently confirm both identities
        # absent. In particular a renamed original container cannot be hidden.
        for filter_value in ('name=^/' + re.escape(self.m['container']) + '$', 'id=' + old_id):
            result = command([self.docker, 'ps', '--all', '--no-trunc', '--filter', filter_value,
                              '--format', '{{.ID}}'], env=self.client_env, stage='haos.confirm_absence')
            if result.stdout.strip():
                raise Refused('HAOS node appeared during absence confirmation')
        supervisor = self.supervisor_info()
        original = saved['auxiliaries']
        if (supervisor['boot'] != original['boot'] or original['watchdog'] is not False
                or (supervisor['auto_update'] and not original['auto_update'])):
            raise Refused('HAOS absent-node lifecycle policy changed')
        jobs = json.loads(command(['/usr/bin/ha', 'jobs', 'info', '--raw-json'],
                                  stage='haos.absence_jobs').stdout)
        jobs = jobs.get('data', jobs)
        if not isinstance(jobs.get('jobs'), list):
            raise Refused('Supervisor lifecycle jobs are not observable')
        image = json.loads(command([self.docker, 'image', 'inspect', self.m['image_id']],
                                    env=self.client_env, stage='haos.absence_image').stdout)
        if not isinstance(image, list) or len(image) != 1 or image[0].get('Id') != self.m['image_id']:
            raise Refused('pinned HAOS image is unavailable')
        # These reads use the pinned image, canonical data bind and network-none.
        # They also refuse a missing data directory; no substitute volume exists.
        if self.launcher_digest() != saved['environment_file_sha256']:
            raise Refused('HAOS absent-node launcher or canonical data changed')
        for guard in self.m.get('file_guards', []):
            actual = self.file_digest(guard['path'])
            if (guard.get('absent') and actual) or (not guard.get('absent') and actual != guard['sha256']):
                raise Refused('HAOS absent-node reviewed file guard changed')
        idle = jobs['jobs'] == []
        settled = supervisor['state'] == 'stopped' and idle
        return {'pid': 0, 'inner_pid': 0, 'generation': '', 'container_id': '',
                'container_present': False, 'absence_confirmed': True,
                'container_running': False, 'container_restarting': False, 'container_dead': False,
                'container_status': 'absent', 'active': 'inactive' if settled else 'transitioning',
                'supervisor_state': supervisor['state'], 'lifecycle_jobs_idle': idle,
                'image_id': self.m['image_id'], 'management_container_id': management['Id'],
                'controller_pid': os.getpid(), 'controller_cgroup': Path('/proc/self/cgroup').read_text()}

    def info(self):
        management = self.management_info()
        value = self.inspect_container(self.m['container'], allow_absent=True)
        if value is None:
            return self.absent_info(management)
        if management['Id'] == value['Id']:
            raise Refused('HAOS controller is not in the independently pinned SSH add-on')
        mounts = {x['Destination']: x for x in value['Mounts']}
        if value['Image'] != self.m['image_id'] or value['HostConfig']['RestartPolicy']['Name'] != 'no':
            raise Refused('HAOS image or lifecycle policy changed')
        for destination, source in [('/data', self.m['data_host_path']), ('/share', self.m['share_host_path'])]:
            mount = mounts.get(destination, {})
            if mount.get('Type') != 'bind' or mount.get('Source') != source or mount.get('RW') is not True:
                raise Refused('HAOS canonical volume changed')
        running = value['State']['Running']
        inner_pid = 0
        if running:
            program = ('import os,json; out=[]\n'
                       'for x in os.listdir("/proc"):\n'
                       ' if not x.isdigit():continue\n'
                       ' try:\n'
                       '  if os.readlink("/proc/"+x+"/exe")=="/usr/local/bin/gptadmin-node":\n'
                       '   start=open("/proc/"+x+"/stat").read().rsplit(")",1)[1].split()[19]; out.append([int(x),start])\n'
                       ' except OSError:pass\n'
                       'print(json.dumps(out))')
            found = json.loads(command([self.docker, 'exec', value['Id'], '/usr/bin/python3', '-c', program],
                                       env=self.client_env).stdout)
            if len(found) > 1:
                raise Refused('HAOS has multiple node processes')
            inner_pid = found[0][0] if found else 0
        generation = value['State']['StartedAt'] + ':' + (found[0][1] if running and inner_pid else '')
        return {'pid': value['State']['Pid'] if running else 0, 'inner_pid': inner_pid,
                'generation': generation,
                'active': 'active' if running else 'inactive', 'container_id': value['Id'],
                'container_present': True, 'absence_confirmed': False,
                'container_running': running, 'container_status': value['State']['Status'],
                'container_restarting': value['State'].get('Restarting', False), 'container_dead': value['State'].get('Dead', False),
                'image_id': value['Image'], 'management_container_id': management['Id'],
                'controller_pid': os.getpid(), 'controller_cgroup': Path('/proc/self/cgroup').read_text()}

    def stopped(self, value):
        if value.get('container_present') is False:
            return (value.get('absence_confirmed') is True and value.get('supervisor_state') == 'stopped'
                    and value.get('lifecycle_jobs_idle') is True and not value.get('container_id')
                    and value.get('container_status') == 'absent' and not value.get('container_running')
                    and not value.get('container_restarting') and not value.get('container_dead')
                    and not value.get('generation') and not value.get('pid') and not value.get('inner_pid'))
        return (not value['container_running'] and not value['container_restarting'] and not value['container_dead']
                and value['container_status'] in ('exited', 'created') and not value['pid'])

    def has_node(self, value):
        return bool(value['container_running'] and value['inner_pid'])

    def environment(self, info):
        if not info['pid'] or not info['inner_pid']:
            raise Refused('HAOS node has no live process environment')
        raw = command([self.docker, 'exec', info['container_id'], '/bin/cat',
                       '/proc/%s/environ' % info['inner_pid']], env=self.client_env).stdout
        return {os.fsdecode(k): os.fsdecode(v) for k, v in
                (item.split(b'=', 1) for item in raw.split(b'\0') if b'=' in item)}

    def check_binary(self, info):
        raw = command([self.docker, 'exec', info['container_id'], '/usr/bin/python3', '-c',
                       'import hashlib,pathlib;print(hashlib.sha256(pathlib.Path("/proc/%s/exe").read_bytes()).hexdigest())'
                       % info['inner_pid']], env=self.client_env).stdout.decode().strip()
        if raw != self.m['binary_sha256']:
            raise Refused('HAOS running binary differs from the reviewed image')

    def isolated(self, executable, args, env=None, read_only=True):
        # Only the canonical node data and exact private operation directory
        # are mounted. This is not volumes-from and exposes no host /dev tree.
        work = Path(self.m['operation_dir'])
        if work.parent != Path('/share') or not work.name.startswith('gptadmin-authority-'):
            raise Refused('HAOS private work directory is outside the reviewed share')
        host_work = str(Path(self.m['share_host_path']) / work.name)
        argv = [self.docker, 'run', '--rm', '--pull=never', '--network', 'none', '--read-only',
                '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--user', '0:0',
                '--tmpfs', '/tmp:rw,nosuid,nodev,size=128m',
                '--mount', 'type=bind,src=%s,dst=/data%s' %
                (self.m['data_host_path'], ',readonly' if read_only else ''),
                '--mount', 'type=bind,src=%s,dst=%s' % (host_work, str(work)),
                '--entrypoint', executable]
        client_env = dict(self.client_env)
        for key, value in (env or {}).items():
            if not re.fullmatch('[A-Za-z_][A-Za-z0-9_]*', key):
                raise Refused('invalid captured environment name')
            if key.startswith('DOCKER_'):
                continue
            argv += ['--env', key]
            client_env[key] = value
        client_env['DOCKER_HOST'] = 'unix:///var/run/docker.sock'
        argv += [self.m['image_id']] + list(args)
        return command(argv, timeout=95, env=client_env)

    def helper(self, args, env):
        if args[0] == 'preflight':
            # A live WAL-mode SQLite file can have no -shm sidecar. SQLite's
            # mode=ro/query_only reader still needs normal shared-memory
            # bookkeeping, so run this read-only helper in the existing node
            # container. Lifecycle control remains in the separate SSH add-on.
            info = self.info()
            if not self.has_node(info):
                raise Refused('HAOS live preflight requires the existing running node')
            argv = [self.docker, 'exec', '--user', '0:0']
            client_env = dict(self.client_env)
            for key, value in env.items():
                if not re.fullmatch('[A-Za-z_][A-Za-z0-9_]*', key):
                    raise Refused('invalid captured environment name')
                if key.startswith('DOCKER_'):
                    continue
                argv += ['--env', key]
                client_env[key] = value
            client_env['DOCKER_HOST'] = 'unix:///var/run/docker.sock'
            argv += [info['container_id'], self.m['helper']] + list(args)
            return command(argv, timeout=95, env=client_env)
        # All actual offline operations are fenced by the caller's strong
        # stopped proof and use the native runtime lock in this isolated image.
        return self.isolated(self.m['helper'], args, env, read_only=False)

    def read_file(self, path):
        if not (str(path).startswith('/data/') or str(path) == '/run.sh'):
            raise Refused('HAOS image read is outside canonical state')
        return self.isolated('/usr/bin/python3', ['-c',
            'import pathlib,sys;sys.stdout.buffer.write(pathlib.Path(sys.argv[1]).read_bytes())', str(path)]).stdout

    def file_digest(self, path):
        if not (str(path).startswith('/data/') or str(path) == '/run.sh'):
            return digest(Path(path).read_bytes()) if Path(path).exists() else ''
        result = self.isolated('/usr/bin/python3', ['-c',
            'import pathlib,sys,hashlib;p=pathlib.Path(sys.argv[1]);print(hashlib.sha256(p.read_bytes()).hexdigest() if p.exists() else "")', str(path)])
        return result.stdout.decode().strip()

    def launcher_digest(self):
        return digest((self.file_digest('/run.sh') + ':' + self.file_digest('/data/options.json')).encode())

    def supervisor_info(self):
        raw = command(['/usr/bin/ha', 'addons', 'info', self.m['addon'], '--raw-json']).stdout
        value = json.loads(raw)
        value = value.get('data', value)
        if (value.get('slug') != self.m['addon'] or value.get('watchdog') is not False
                or value.get('version') != self.m['addon_version'] or not isinstance(value.get('auto_update'), bool)
                or not isinstance(value.get('state'), str) or not value['state'] or not isinstance(value.get('boot'), str)):
            raise Refused('Supervisor watchdog or version differs from reviewed lifecycle')
        return value

    def auxiliaries(self):
        value = self.supervisor_info()
        return {'watchdog': False, 'boot': value.get('boot'), 'auto_update': value['auto_update']}

    def set_automatic_updates(self, enabled):
        # Official Supervisor settings API. Only this reversible scheduling
        # flag is sent; application options and runtime are left untouched.
        if not isinstance(enabled, bool) or not re.fullmatch('[a-z0-9_]+', self.m['addon']):
            raise Refused('invalid exact Supervisor update setting')
        management = self.inspect(self.m['management_container'])
        env = dict(item.split('=', 1) for item in management['Config'].get('Env', []) if '=' in item)
        token = env.get('SUPERVISOR_TOKEN')
        if not token:
            raise Refused('the existing management container has no Supervisor API token')
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, request, fp, code, message, headers, newurl):
                raise Refused('Supervisor settings request was redirected')
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        request = urllib.request.Request('http://supervisor/addons/' + self.m['addon'] + '/options',
            data=json.dumps({'auto_update': enabled}).encode(), method='POST',
            headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        with opener.open(request, timeout=15) as response:
            result = json.load(response)
            if response.status != 200 or result.get('result') != 'ok':
                raise Refused('Supervisor did not confirm the update scheduling flag')
        if self.auxiliaries()['auto_update'] is not enabled:
            raise Refused('Supervisor update setting readback differs')

    def wait_updates_idle(self):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            value = json.loads(command(['/usr/bin/ha', 'jobs', 'info', '--raw-json']).stdout)
            value = value.get('data', value)
            if value.get('jobs') == []:
                return
            # Do not cancel an in-flight installation or infer an unknown job
            # schema is harmless. A busy Supervisor keeps this phase closed.
            time.sleep(.5)
        raise Refused('Supervisor jobs did not settle before the finite update pause')

    def pause(self, states):
        info = self.info()
        if not self.has_node(info) and not self.stopped(info):
            raise Refused('HAOS manager transition is uncertain before update pause')
        current = self.auxiliaries()
        if any(current.get(key) != states.get(key) for key in ('watchdog', 'boot')) or (current['auto_update'] and not states['auto_update']):
            raise Refused('Supervisor lifecycle policy changed before the window')
        if current['auto_update']:
            self.set_automatic_updates(False)
        self.wait_updates_idle()

    def resume(self, states):
        info = self.info()
        if not self.has_node(info) and not self.stopped(info):
            raise Refused('HAOS manager transition is uncertain before update restoration')
        current = self.auxiliaries()
        if any(current.get(key) != states.get(key) for key in ('watchdog', 'boot')):
            raise Refused('Supervisor lifecycle policy changed before restoration')
        if current['auto_update'] != states['auto_update']:
            self.set_automatic_updates(states['auto_update'])

    def stop(self):
        command(['/usr/bin/ha', 'addons', 'stop', self.m['addon']], timeout=90)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            result = self.info()
            if self.stopped(result):
                return result
            time.sleep(.5)
        raise Refused('Supervisor did not confirm the node add-on stopped')

    def start(self):
        command(['/usr/bin/ha', 'addons', 'start', self.m['addon']], timeout=90)


class Controller:
    def __init__(self, manifest, expected):
        checked_file(manifest, expected)
        self.m = read_json(manifest)
        if self.m.get('schema') != 'gptadmin.authority-controller/v1':
            raise Refused('unknown controller manifest')
        self.manifest_hash = expected
        self.work = Path(self.m['operation_dir'])
        if not self.work.is_absolute():
            raise Refused('operation directory must be absolute')
        checked_file(self.m['helper'], self.m['helper_sha256'], True)
        adapter = self.m['adapter']
        self.lifecycle = Systemd(self.m) if adapter == 'systemd' else Launchd(self.m) if adapter == 'launchd' else HAOS(self.m, self.manifest_hash) if adapter == 'haos' else None
        if self.lifecycle is None:
            raise Refused('unsupported lifecycle adapter')

    def snapshot(self):
        return read_json(self.work / 'capture.private.json')

    def helper(self, args, env):
        checked_file(self.m['helper'], self.m['helper_sha256'], True)
        if '--physical-identity' not in args:
            args = list(args) + ['--physical-identity', self.m['physical_identity_file']]
        result = self.lifecycle.helper(args, env) if self.m['adapter'] == 'haos' else command([self.m['helper']] + args, timeout=90, env=env)
        if len(result.stdout) > 1024 * 1024:
            raise Refused('oversized helper receipt')
        return json.loads(result.stdout)

    def file_digest(self, path):
        if self.m['adapter'] == 'haos':
            return self.lifecycle.file_digest(path)
        return digest(Path(path).read_bytes()) if Path(path).exists() else ''

    def launcher_digest(self):
        return self.lifecycle.launcher_digest() if self.m['adapter'] == 'haos' else self.file_digest(self.m['environment_file'])

    def check_binary(self, info):
        if self.m['adapter'] == 'haos':
            self.lifecycle.check_binary(info)
        else:
            checked_file(self.lifecycle.binary(info), self.m['binary_sha256'], True,
                         BinaryDigestMismatch if self.m['adapter'] == 'systemd' else Refused)

    def runtime_guards(self):
        for item in self.m.get('file_guards', []):
            if item.get('absent'):
                if self.file_digest(item['path']):
                    raise Refused('required absence guard changed')
            else:
                if self.file_digest(item['path']) != item['sha256']:
                    raise Refused('reviewed runtime guard changed')

    def capture(self, save=True):
        self.runtime_guards()
        info = self.lifecycle.info()
        if info['pid'] != self.m['expected_pid']:
            raise Refused('running PID changed before capture')
        self.check_binary(info)
        env = self.lifecycle.environment(info)
        if env.get('GPTADMIN_CONFIG_DIR') != self.m['config_dir']:
            raise Refused('effective runtime config directory changed')
        status, drain = http_json(self.m['local_origin'], env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD'))
        if status != 200 or not isinstance(drain, dict) or drain.get('draining') is not False:
            raise Refused('supported authenticated drain is unavailable or already owned')
        preflight = self.helper(['preflight', '--config-dir', self.m['config_dir'], '--target',
                                 self.m['target'], '--expect-local-id', self.m['local_id']], env)
        private = {'manifest_sha256': self.manifest_hash, 'environment': env,
                   'service': info, 'auxiliaries': self.lifecycle.auxiliaries(),
                   'environment_file_sha256': self.launcher_digest()}
        if self.m['adapter'] == 'launchd':
            private['plist_bytes'] = Path(self.m['plist']).read_text()
        if save:
            private_json(self.work / 'capture.private.json', private, True)
        return {'service': info, 'preflight': preflight,
                'helper_sha256': self.m['helper_sha256'],
                'signer_fingerprint': digest(env.get('OAUTH_CLIENT_SECRET', '').encode()),
                'oauth_key_id': preflight.get('oauth_key_id'), 'drain': drain}

    def service_identity(self, info):
        if not self.lifecycle.has_node(info) or not info.get('generation'):
            raise Refused('live process generation is not proved')
        return {key: info[key] for key in ('pid', 'generation', 'unit_metadata_sha256',
                'container_id', 'inner_pid', 'image_id', 'domain') if key in info}

    def original_environment(self, info):
        saved = self.snapshot()
        self.runtime_guards()
        if self.service_identity(info) != self.service_identity(saved['service']):
            raise Refused('original runtime generation changed before fence')
        if self.launcher_digest() != saved['environment_file_sha256']:
            raise Refused('launcher changed before freeze')
        self.check_binary(info)
        return saved['environment']

    def fence_path(self, plan_digest=None):
        if plan_digest is not None and not re.fullmatch('[0-9a-f]{64}', plan_digest):
            raise Refused('invalid applied fence plan digest')
        return self.work / ('fence-' + (plan_digest or 'original') + '.json')

    def read_fence(self, plan_digest=None):
        value = read_json(self.fence_path(plan_digest))
        if value.get('manifest_sha256') != self.manifest_hash or value.get('plan_digest') != plan_digest:
            raise Refused('fence receipt belongs to another operation or plan')
        return value

    def check_own_drain(self, fence, value):
        if (not isinstance(value, dict) or value.get('draining') is not True
                or value.get('reason') != fence['reason'] or not value.get('since')
                or (fence.get('drain') and value.get('since') != fence['drain']['since'])):
            raise Refused('current drain does not belong to this fence')

    def begin_fence(self, info, env, plan_digest=None):
        path = self.fence_path(plan_digest)
        if path.exists():
            raise Refused('fence was already attempted; inspect rather than repeat POST')
        token = env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD')
        status, before = http_json(self.m['local_origin'], token)
        if status != 200 or not isinstance(before, dict) or before.get('draining') is not False:
            raise Refused('drain is unavailable or already owned before fence')
        identity = self.service_identity(info)
        if self.service_identity(self.lifecycle.info()) != identity:
            raise Refused('runtime changed before drain admission')
        fence = {'manifest_sha256': self.manifest_hash, 'plan_digest': plan_digest,
                 'service': identity, 'phase': 'begin_requested',
                 'reason': 'authority:' + self.manifest_hash + ':' + (plan_digest or 'original')}
        private_json(path, fence, True)
        status, value = http_json(self.m['local_origin'], token, {'action': 'begin', 'reason': fence['reason']})
        if status != 200:
            raise Refused('drain admission unconfirmed; no POST retry is allowed')
        self.check_own_drain(fence, value)
        if self.service_identity(self.lifecycle.info()) != identity:
            raise Refused('runtime changed while admitting drain')
        fence.update(phase='fenced', drain=value)
        private_json(path, fence)
        return {'service': info, 'drain': value, 'listening': True}

    def fence(self):
        info = self.lifecycle.info()
        env = self.original_environment(info)
        self.lifecycle.pause(self.snapshot()['auxiliaries'])
        return self.begin_fence(info, env)

    def verify_fenced_ledger(self, backup=None, plan_digest=None):
        """Authority transfer needs an empty ledger, unlike ordinary drain."""
        self.runtime_guards()
        before = self.lifecycle.info()
        stopped = self.lifecycle.stopped(before)
        if stopped:
            if backup is None:
                raise Refused('original writer must remain live for its early ledger check')
            env = self.applied_environment(backup, plan_digest)
            fence = None
        else:
            fence = self.read_fence(plan_digest)
            if fence['phase'] != 'fenced' or self.service_identity(before) != fence['service']:
                raise Refused('ledger check requires this live writer fence')
            env = (self.applied_live_environment(before, backup, plan_digest) if backup else
                   self.original_environment(before))
            # Already admitted submissions and callbacks may still finish.
            # Keep every reader and the writer listening while they settle.
            # Durable queued tasks deliberately do not block generic drain;
            # the separate ledger proof below must still reject those tasks.
            deadline = time.monotonic() + self.m.get('drain_timeout', 45)
            while True:
                self.runtime_guards()
                if self.service_identity(self.lifecycle.info()) != fence['service']:
                    raise Refused('writer generation changed while awaiting ledger readiness')
                status, value = http_json(self.m['local_origin'], env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD'))
                if status != 200:
                    raise Refused('writer drain unavailable during ledger check')
                self.check_own_drain(fence, value)
                counters = value.get('counters', {})
                if value.get('ready_to_stop') is True and all(type(counters.get(key)) is int and counters[key] == 0 for key in (
                        'active_submissions', 'active_callbacks', 'unfinished_executions', 'outbox_pending')):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeReadinessFailure('writer callbacks did not settle before authority ledger check',
                        {'stage': 'authority_ledger', 'reason': 'owned_drain_not_ready'})
                time.sleep(.5)
        preflight = self.helper(['preflight', '--config-dir', self.m['config_dir'], '--target',
                                 self.m['target'], '--expect-local-id', self.m['local_id']], env)
        if (preflight.get('auth_mode') != 'writer' or preflight.get('auth_pending') is not False
                or preflight.get('auth_state_stable_during_read') is not True
                or preflight.get('local_id') != self.m['local_id']
                or preflight.get('target') != self.m['target']
                or preflight.get('config_dir') != self.m['config_dir']):
            raise Refused('authority ledger proof is not a stable local writer view')
        for key in ('pending_task_records', 'pending_task_controls'):
            if type(preflight.get(key)) is not int or preflight[key] != 0:
                raise RuntimeReadinessFailure('authority ledger is not empty before stop',
                    {'stage': 'authority_ledger', 'reason': key,
                     'count': preflight.get(key) if type(preflight.get(key)) is int else None})
        after = self.lifecycle.info()
        if stopped:
            if not self.lifecycle.stopped(after):
                raise Refused('stopped applied writer changed during ledger check')
        else:
            if self.service_identity(after) != fence['service']:
                raise Refused('writer generation changed during ledger check')
            if backup:
                self.applied_live_environment(after, backup, plan_digest)
            else:
                self.original_environment(after)
            status, value = http_json(self.m['local_origin'], env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD'))
            if status != 200:
                raise Refused('writer drain disappeared during ledger check')
            self.check_own_drain(fence, value)
        return {'authority_ledger_empty': True, 'read_only': True, 'service': after,
                'preflight': preflight, 'already_stopped': stopped}

    def stop_fenced(self, backup=None, plan_digest=None, authority_writer=False):
        fence = self.read_fence(plan_digest)
        if fence['phase'] != 'fenced':
            raise Refused('stop requires an acknowledged fence, not an uncertain mutation')
        env = self.applied_live_environment(self.lifecycle.info(), backup, plan_digest) if backup else self.original_environment(self.lifecycle.info())
        token = env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD')
        deadline = time.monotonic() + self.m.get('drain_timeout', 45)
        while time.monotonic() < deadline:
            self.runtime_guards()
            info = self.lifecycle.info()
            if self.service_identity(info) != fence['service']:
                raise Refused('fenced runtime generation changed before stop')
            status, value = http_json(self.m['local_origin'], token)
            if status != 200:
                raise Refused('drain status unavailable; no manager stop issued')
            self.check_own_drain(fence, value)
            counters = value.get('counters', {})
            if value.get('ready_to_stop') is True and all(type(counters.get(key)) is int and counters[key] == 0 for key in (
                    'active_submissions', 'active_callbacks', 'unfinished_executions', 'outbox_pending')):
                # Recheck the same generation immediately before the single
                # normal manager stop. A lost stop response remains uncertain.
                if self.service_identity(self.lifecycle.info()) != fence['service']:
                    raise Refused('fenced runtime changed at the stop boundary')
                if backup:
                    self.applied_live_environment(info, backup, plan_digest)
                else:
                    self.original_environment(info)
                # Generic drain intentionally preserves durable queued work.
                # Only authority migration requires both ledgers to be empty,
                # freshly checked at the final writer stop boundary.
                ledger = self.verify_fenced_ledger(backup, plan_digest) if authority_writer else None
                if self.service_identity(self.lifecycle.info()) != fence['service']:
                    raise Refused('fenced runtime changed after the final ledger check')
                fence.update(phase='stop_requested', drain=value)
                private_json(self.fence_path(plan_digest), fence)
                stopped = (self.lifecycle.stop(fence['service']) if isinstance(self.lifecycle, Launchd)
                           else self.lifecycle.stop())
                if not self.lifecycle.stopped(stopped):
                    raise Refused('manager did not confirm the fenced runtime stopped')
                fence.update(phase='stopped', stopped=stopped)
                private_json(self.fence_path(plan_digest), fence)
                return {'drain': value, 'stopped': stopped, **({'authority_ledger': ledger} if ledger else {})}
            time.sleep(.5)
        raise Refused('drain deadline reached; no manager stop issued')

    def freeze(self):
        self.fence()
        return self.stop_fenced()

    def run_offline(self, args):
        allowed = {'preflight', 'export', 'capture-legacy-baseline', 'prepare-writer', 'prepare-reader', 'apply', 'inspect',
                   'authorize-activation', 'restore-before-activation'}
        if not args or args[0] not in allowed:
            raise Refused('unsupported offline helper action')
        if '--config-dir' not in args or args[args.index('--config-dir') + 1] != self.m['config_dir']:
            raise Refused('helper action config mismatch')
        if not self.lifecycle.stopped(self.lifecycle.info()):
            raise Refused('offline helper requires confirmed stopped runtime')
        self.runtime_guards()
        return self.helper(args, self.snapshot()['environment'])

    def applied_environment(self, backup, plan_digest):
        journal = read_json(Path(backup) / 'rollback.json')
        if journal.get('plan_digest') != plan_digest:
            raise Refused('current environment journal digest mismatch')
        plan = journal['plan']
        if plan.get('config_dir') != self.m['config_dir']:
            raise Refused('current environment journal config mismatch')
        env = dict(self.snapshot()['environment'])
        path = plan.get('environment_file')
        if not path and self.m['adapter'] == 'haos' and plan.get('identity_derived_environment'):
            if self.launcher_digest() != self.snapshot()['environment_file_sha256']:
                raise Refused('identity-derived HAOS entrypoint/options changed')
            env.update(plan['environment'])
            return env
        if not path:
            raise Refused('current environment requires a concrete launcher source')
        files = [x for x in plan['files'] if x.get('destination') == path]
        if len(files) != 1 or digest(Path(path).read_bytes()) != files[0]['after']:
            raise Refused('current effective environment no longer matches applied plan')
        env.update(plan['environment'])
        return env

    def run_from_applied(self, backup, plan_digest, args):
        if not args or args[0] not in {'preflight', 'export', 'prepare-writer', 'prepare-reader', 'apply',
                                      'inspect', 'authorize-activation', 'restore-before-activation'}:
            raise Refused('unsupported current-state helper action')
        if not self.lifecycle.stopped(self.lifecycle.info()):
            raise Refused('current-state helper requires a stopped runtime')
        if '--config-dir' not in args or args[args.index('--config-dir') + 1] != self.m['config_dir']:
            raise Refused('current-state helper config mismatch')
        return self.helper(args, self.applied_environment(backup, plan_digest))

    def applied_live_environment(self, info, backup, plan_digest):
        expected = self.applied_environment(backup, plan_digest)
        live = self.lifecycle.environment(info)
        for key, value in read_json(Path(backup) / 'rollback.json')['plan']['environment'].items():
            if live.get(key, '') != value:
                raise Refused('live role differs from the applied environment')
        self.check_binary(info)
        return expected

    def fence_applied(self, backup, plan_digest):
        self.runtime_guards()
        # A prior completion path may already have restored some updaters.
        # Fresh reverse must pause them again, including when the node itself
        # is already stopped. This remains idempotent for untouched auxiliaries.
        self.lifecycle.pause(self.snapshot()['auxiliaries'])
        info = self.lifecycle.info()
        deadline = time.monotonic() + 15
        while not self.lifecycle.stopped(info) and not self.lifecycle.has_node(info) and time.monotonic() < deadline:
            time.sleep(.3)
            info = self.lifecycle.info()
        if self.lifecycle.stopped(info):
            return {'stopped': info, 'already_stopped': True}
        if not self.lifecycle.has_node(info):
            # A previous start may have been admitted while its response was
            # lost. Settle that manager job with one normal stop, never another
            # start, and require the full unloaded/stopped proof before export.
            stopped = self.lifecycle.stop()
            if not self.lifecycle.stopped(stopped):
                raise Refused('uncertain start did not settle into stopped state')
            return {'stopped': stopped, 'normal_stop_settled_unknown_start': True}
        expected = self.applied_live_environment(info, backup, plan_digest)
        return self.begin_fence(info, expected, plan_digest)

    def freeze_applied(self, backup, plan_digest):
        result = self.fence_applied(backup, plan_digest)
        return result if 'stopped' in result else self.stop_fenced(backup, plan_digest)

    def inspect_prepared(self, directory, plan_digest):
        path = Path(directory) / 'plan.json'
        if digest(path.read_bytes()) != plan_digest:
            raise Refused('inspection plan digest mismatch')
        plan = read_json(path)
        if plan.get('config_dir') != self.m['config_dir']:
            raise Refused('inspection config mismatch')
        before = after = conflicts = 0
        for item in plan['files']:
            destination = Path(item.get('destination') or str(Path(plan['config_dir']) / item['relative']))
            actual = self.file_digest(destination)
            if actual == item.get('before', ''):
                before += 1
            elif actual == item.get('after', ''):
                after += 1
            else:
                conflicts += 1
        return {'files_before': before, 'files_after': after, 'conflicts': conflicts,
                'files': len(plan['files'])}

    def start_unchanged_plan(self, directory, plan_digest, backup):
        if (Path(backup) / 'rollback.json').exists() or (Path(backup) / 'activation-authorized.json').exists():
            raise Refused('journal exists; use supported Go restore before activation')
        state = self.inspect_prepared(directory, plan_digest)
        if state['files_after'] or state['conflicts'] or state['files_before'] != state['files']:
            raise Refused('original before-state cannot be proved')
        if not self.lifecycle.stopped(self.lifecycle.info()):
            raise Refused('a runtime already exists')
        self.lifecycle.start()
        ready = self.wait_original_ready(after_start=True)
        self.lifecycle.resume(self.snapshot()['auxiliaries'])
        return {'unchanged_original_restarted': True, 'state': state, **ready}

    def start_applied(self, backup, plan_digest):
        saved = self.snapshot()
        if not self.lifecycle.stopped(self.lifecycle.info()):
            raise Refused('runtime already started; inspect the original start receipt')
        marker = read_json(Path(backup) / 'activation-authorized.json')
        if marker.get('plan_digest') != plan_digest:
            raise Refused('activation marker does not bind the reviewed plan')
        # The Go helper already rechecked all after bytes before authorization.
        # A separate inspect remains mandatory immediately before start.
        state = self.helper(['inspect', '--config-dir', self.m['config_dir'], '--backup', backup,
                             '--plan-sha256', plan_digest], saved['environment'])
        if not state.get('activation_authorized') or state.get('conflicts') or state.get('guard_conflicts') or state.get('files_before') or state.get('pending_auth_marker') or state.get('restore_started'):
            raise Refused('authorized after-state no longer complete')
        if self.m['adapter'] == 'launchd':
            self.lifecycle.regenerate()
        self.lifecycle.start()
        deadline = time.monotonic() + 40
        while time.monotonic() < deadline:
            info = self.lifecycle.info()
            if self.lifecycle.has_node(info):
                try:
                    status, _ = http_json(self.m['local_origin'], None, path='/healthz')
                    if status == 200:
                        env = self.lifecycle.environment(info)
                        preflight = self.helper(['preflight', '--config-dir', self.m['config_dir'],
                                                '--target', self.m['target'], '--expect-local-id', self.m['local_id']], env)
                        anonymous = {}
                        for origin in [self.m['local_origin']] + self.m.get('additional_readiness_origins', []):
                            health, _ = http_json(origin, None, path='/healthz')
                            denied, _ = http_json(origin, None, path='/mcp')
                            if health != 200 or denied != 401:
                                raise Refused('listener health or anonymous MCP boundary failed')
                            anonymous[origin] = {'health_status': health, 'anonymous_mcp_status': denied}
                        return {'service': info, 'health_status': status, 'preflight': preflight,
                                'signer_fingerprint': digest(env.get('OAUTH_CLIENT_SECRET', '').encode()),
                                'listeners': anonymous}
                except (OSError, urllib.error.URLError):
                    pass
            time.sleep(.5)
        raise Refused('new runtime did not become ready; fresh recovery required after activation')

    def resume_auxiliaries(self):
        self.lifecycle.resume(self.snapshot()['auxiliaries'])
        return {'auxiliaries_restored': True}

    def pause_auxiliaries(self):
        saved = self.snapshot()
        self.runtime_guards()
        info = self.lifecycle.info()
        if info['pid'] != saved['service']['pid'] or self.launcher_digest() != saved['environment_file_sha256']:
            raise Refused('runtime or launcher changed before publication pause')
        if self.m['adapter'] == 'systemd' and info['unit_metadata_sha256'] != saved['service']['unit_metadata_sha256']:
            raise Refused('systemd unit metadata changed before publication pause')
        self.lifecycle.pause(saved['auxiliaries'])
        return {'auxiliaries_paused': True, 'original_states': saved['auxiliaries'], 'runtime_stopped': False}

    def verify_membership(self, transition_path, transition_sha, expected_json):
        path = Path(transition_path)
        if digest(path.read_bytes()) != transition_sha:
            raise Refused('acceptance transition image changed')
        transition = read_json(path)
        expected = json.loads(expected_json)
        writer = expected['writer_id']
        if transition.get('writer_id') != writer:
            raise Refused('acceptance writer differs from applied authority')
        origin = transition['writer_url']
        if origin != 'https://mcp.bezrabotnyi.com':
            raise Refused('acceptance public origin is not the reviewed endpoint')
        token = transition['source_identity']['node_key']
        deadline = time.monotonic() + 50
        while time.monotonic() < deadline:
            status, envelope = http_json(origin, token, path='/admin/api/nodes/membership')
            view = envelope.get('membership') if isinstance(envelope, dict) else None
            if status != 200 or not isinstance(view, dict) or view.get('writer_id') != writer:
                time.sleep(.5)
                continue
            nodes = view.get('nodes', [])
            if isinstance(nodes, dict):
                nodes = list(nodes.values())
            if not isinstance(nodes, list) or any(not isinstance(node, dict) or not isinstance(node.get('node_id'), str) or not node['node_id'] for node in nodes):
                time.sleep(.5)
                continue
            by_id = {node['node_id']: node for node in nodes}
            if len(by_id) != len(nodes):
                time.sleep(.5)
                continue
            safe = []
            good = True
            for target, want in expected['members'].items():
                node = by_id.get(want['node_id'], {})
                try:
                    lease = parse_membership_lease(node.get('lease_expires', ''))
                    valid_lease = lease > datetime.now(timezone.utc)
                except ValueError:
                    valid_lease = False
                if node.get('name') != target or node.get('local_id') != want['local_id'] or node.get('state') != 'ready' or not valid_lease:
                    good = False
                if node.get('endpoints') != [want['endpoint']]:
                    good = False
                if want['node_id'] != writer:
                    revision = node.get('snapshot_revision', 0)
                    if type(revision) is not int or revision < expected['auth_generation']:
                        good = False
                safe.append({key: node.get(key) for key in ['node_id', 'name', 'local_id', 'state', 'endpoints', 'snapshot_revision', 'lease_expires']})
            if good:
                return {'public_membership_authenticated': True, 'writer_id': writer,
                        'revision': view.get('revision'), 'body_sha256': digest(json.dumps(envelope, sort_keys=True).encode()),
                        'members': safe, 'public_mcp_execution_acceptance': 'pending-independent-caller'}
            time.sleep(.5)
        raise Refused('public writer authentication or retained reader convergence failed')

    def start_restored(self, backup, plan_digest):
        saved = self.snapshot()
        if not self.lifecycle.stopped(self.lifecycle.info()):
            raise Refused('original runtime already started; inspect the exact recovery receipt')
        marker = read_json(Path(backup) / 'restored.json')
        if marker.get('plan_digest') != plan_digest or (Path(backup) / 'activation-authorized.json').exists():
            raise Refused('restored state is not eligible for preactivation manager recovery')
        state = self.helper(['inspect', '--config-dir', self.m['config_dir'], '--backup', backup,
                             '--plan-sha256', plan_digest], saved['environment'])
        if not state.get('restored_receipt') or state.get('conflicts') or state.get('guard_conflicts') or state.get('files_after') or state.get('pending_auth_marker'):
            raise Refused('restored original authority is incomplete')
        if self.launcher_digest() != saved['environment_file_sha256']:
            raise Refused('restored launcher differs from capture')
        if self.m['adapter'] == 'launchd':
            self.lifecycle.regenerate()
        self.lifecycle.start()
        ready = self.wait_original_ready(after_start=True)
        self.lifecycle.resume(saved['auxiliaries'])
        return {'original_runtime_restarted': True, **ready}

    def wait_original_ready(self, observe=None, cancel=None, after_start=False):
        """Wait with reads only; a drain cancellation is dispatched at most once."""
        saved = self.snapshot()
        token = saved['environment'].get('CTL_TOKEN') or saved['environment'].get('ADMIN_PASSWORD')
        deadline = time.monotonic() + self.m.get('recovery_ready_timeout', 40)
        observed_process = None
        binary_ready = False
        waiting_for = 'manager_process'
        while time.monotonic() < deadline:
            self.runtime_guards()
            if self.launcher_digest() != saved['environment_file_sha256']:
                raise Refused('original launcher changed during recovery readiness')
            info = self.lifecycle.info()
            if self.lifecycle.has_node(info):
                process = (info['pid'], info.get('generation'))
                if not process[1]:
                    raise Refused('original recovery process generation is missing')
                if observed_process is not None and process != observed_process:
                    raise Refused('runtime changed while waiting for original recovery')
                observed_process = process
                try:
                    self.check_binary(info)
                except (BinaryDigestMismatch, FileNotFoundError, ProcessLookupError):
                    # Type=simple may report the launcher's PID before exec.
                    # Tolerate only this narrowly typed startup observation,
                    # in the live call that issued one successful normal start.
                    # Permissions, file guards, launcher drift and an already
                    # verified binary changing remain immediate refusals.
                    if not after_start or self.m['adapter'] != 'systemd' or binary_ready:
                        raise
                    waiting_for = 'reviewed_binary'
                    time.sleep(.5)
                    continue
                binary_ready = True
                if observe is not None:
                    observe(info)
                waiting_for = 'http_readiness'
                try:
                    health, _ = http_json(self.m['local_origin'], None, path='/healthz')
                    status, value = http_json(self.m['local_origin'], token)
                except (OSError, urllib.error.URLError):
                    # A successful manager start may precede the HTTP listener.
                    # Only GETs are repeated here, never manager start or POST.
                    time.sleep(.5)
                    continue
                if status in (401, 403):
                    raise Refused('original recovery control authentication rejected')
                if health == 200 and status == 200 and isinstance(value, dict):
                    current = self.lifecycle.info()
                    if (current['pid'], current.get('generation')) != observed_process:
                        raise Refused('runtime changed across recovery HTTP checks')
                    self.check_binary(current)
                    if observe is not None:
                        observe(current)
                    cancelled = value.get('draining') is True
                    if cancelled:
                        # Keep this mutation outside the retry handler. If its
                        # response is lost, the claimed step requires inspection.
                        status, value = (cancel(info, value) if cancel is not None else
                                         http_json(self.m['local_origin'], token, 'cancel'))
                    if status != 200 or not isinstance(value, dict) or value.get('draining') is not False:
                        raise Refused('original recovery drain cancellation unverified')
                    final = self.lifecycle.info()
                    if (final['pid'], final.get('generation')) != observed_process:
                        raise Refused('runtime changed at recovery readiness completion')
                    self.check_binary(final)
                    if observe is not None:
                        observe(final)
                    return {'service': final, 'health_status': health, 'drain': value,
                            'drain_cancel_issued': cancelled}
            time.sleep(.5)
        raise RuntimeReadinessFailure('original runtime readiness deadline; inspect the existing start',
            {'stage': 'original_runtime_readiness', 'reason': waiting_for,
             'pid': observed_process[0] if observed_process else None,
             'generation': observed_process[1] if observed_process else None})

    def restore_fenced(self):
        """Recover only this operation's original fence, without replaying it."""
        saved = self.snapshot()
        self.runtime_guards()
        if self.launcher_digest() != saved['environment_file_sha256']:
            raise Refused('fence recovery requires unchanged original launcher')
        for receipt in self.work.glob('step-*.json'):
            row = read_json(receipt)
            if row.get('action') == 'helper' and row.get('arguments', [None])[0] == 'apply':
                raise Refused('an authority apply was attempted; use Go recovery')
        path = self.fence_path()
        fence = self.read_fence() if path.exists() else None
        original = self.service_identity(saved['service'])
        info = self.lifecycle.info()
        token = saved['environment'].get('CTL_TOKEN') or saved['environment'].get('ADMIN_PASSWORD')

        if fence is not None and fence['service'] != original:
            raise Refused('original fence generation differs from capture')
        started = False
        if self.lifecycle.stopped(info):
            if fence is None or fence['phase'] not in ('stop_requested', 'stopped'):
                raise Refused('stopped service has no unreplayed owned stop receipt')
            fence.update(phase='recovery_start_requested')
            private_json(path, fence)
            self.lifecycle.start()
            started = True
        elif not self.lifecycle.has_node(info):
            raise Refused('manager transition is uncertain; no additional stop/start allowed')
        elif fence is not None and fence['phase'] in ('stop_requested', 'stopped', 'recovery_start_requested'):
            raise Refused('prior manager mutation lacks a confirmed owned runtime outcome')

        def observe(current):
            identity = self.service_identity(current)
            if fence is not None and fence['phase'] == 'recovery_start_requested':
                # Only this live call reaches here after one successful start.
                # Persist the first actual generation before any HTTP retry.
                fence.update(phase='recovery_started', recovery_service=identity)
                private_json(path, fence)
            expected = fence.get('recovery_service', original) if fence is not None else original
            if identity != expected:
                raise Refused('another runtime owns fence recovery')
            if self.launcher_digest() != saved['environment_file_sha256']:
                raise Refused('original launcher changed during fence recovery')

        def cancel_owned(current, value):
            if fence is None or self.service_identity(current) != original:
                raise Refused('drain is not owned by this original fence')
            self.check_own_drain(fence, value)
            if fence['phase'] not in ('begin_requested', 'fenced'):
                raise Refused('drain cancellation was already attempted or is unowned')
            fence.update(phase='cancel_requested', drain=value)
            private_json(path, fence)
            return http_json(self.m['local_origin'], token, 'cancel')

        ready = self.wait_original_ready(observe=observe, cancel=cancel_owned, after_start=started)
        if fence is not None:
            if fence['phase'] == 'begin_requested':
                raise Refused('unknown begin was not observed; leave its outcome for reconciliation')
            fence.update(phase='recovered', recovery_service=self.service_identity(ready['service']))
            private_json(path, fence)
        return {'original_runtime_restored': True, 'fence_owned': fence is not None, **ready}

    def restore_unapplied(self):
        saved = self.snapshot()
        self.runtime_guards()
        if self.launcher_digest() != saved['environment_file_sha256']:
            raise Refused('unapplied recovery requires unchanged launcher environment')
        # This action is only for a failed drain before any helper apply.
        for receipt in self.work.glob('step-*.json'):
            row = read_json(receipt)
            if row.get('action') == 'helper' and row.get('arguments', [None])[0] == 'apply':
                raise Refused('an apply was attempted; use exact Go restore before manager recovery')
        info = self.lifecycle.info()
        if info['pid'] and info['pid'] != saved['service']['pid']:
            raise Refused('another runtime owns unapplied recovery')
        if not self.lifecycle.has_node(info) and not self.lifecycle.stopped(info):
            info = self.lifecycle.stop()
        started = False
        if self.lifecycle.stopped(info):
            self.lifecycle.start()
            started = True
        ready = self.wait_original_ready(after_start=started)
        self.lifecycle.resume(saved['auxiliaries'])
        return {'original_runtime_restored': True, **ready}

    def execute(self, step, action, args):
        if not re.fullmatch('[a-z0-9][a-z0-9_-]{0,79}', step):
            raise Refused('invalid stable step ID')
        if not self.work.exists():
            if action != 'capture':
                raise Refused('capture has not been performed')
            self.work.mkdir(mode=0o700)
            sync_dir(self.work.parent)
        if self.work.is_symlink() or self.work.stat().st_mode & 0o077:
            raise Refused('operation directory must be private and real')
        path = self.work / ('step-' + step + '.json')
        request = {'manifest_sha256': self.manifest_hash, 'action': action, 'arguments': args}
        if path.exists():
            prior = read_json(path)
            if any(prior.get(k) != v for k, v in request.items()):
                raise Refused('stable step ID was reused with different arguments')
            if prior.get('status') == 'completed':
                return prior
            raise Refused('previous step outcome is uncertain; inspect its exact receipts')
        lock = os.open(str(self.work / 'controller.lock'), os.O_CREAT | os.O_RDWR, 0o600)
        intent_written = False
        action_started = time.perf_counter()
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            request.update({'status': 'started', 'started_at': time.time(), 'pid': os.getpid()})
            private_json(path, request, True)
            intent_written = True
            if action == 'capture':
                result = self.capture()
            elif action == 'preflight':
                result = self.capture(save=False)
            elif action == 'freeze':
                result = self.freeze()
            elif action == 'fence':
                result = self.fence()
            elif action == 'stop-fenced':
                result = self.stop_fenced()
            elif action == 'verify-fenced-ledger' and not args:
                result = self.verify_fenced_ledger()
            elif action == 'verify-fenced-ledger-applied' and len(args) == 2:
                result = self.verify_fenced_ledger(args[0], args[1])
            elif action == 'stop-fenced-writer' and not args:
                result = self.stop_fenced(authority_writer=True)
            elif action == 'stop-fenced-writer-applied' and len(args) == 2:
                result = self.stop_fenced(args[0], args[1], authority_writer=True)
            elif action == 'fence-applied' and len(args) == 2:
                result = self.fence_applied(args[0], args[1])
            elif action == 'stop-fenced-applied' and len(args) == 2:
                result = self.stop_fenced(args[0], args[1])
            elif action == 'helper':
                result = self.run_offline(args)
            elif action == 'helper-from-applied' and len(args) >= 3:
                result = self.run_from_applied(args[0], args[1], args[2:])
            elif action == 'freeze-applied' and len(args) == 2:
                result = self.freeze_applied(args[0], args[1])
            elif action == 'inspect-prepared' and len(args) == 2:
                result = self.inspect_prepared(args[0], args[1])
            elif action == 'start-unchanged-plan' and len(args) == 3:
                result = self.start_unchanged_plan(args[0], args[1], args[2])
            elif action == 'start-applied' and len(args) == 2:
                result = self.start_applied(args[0], args[1])
            elif action == 'start-restored' and len(args) == 2:
                result = self.start_restored(args[0], args[1])
            elif action == 'resume-auxiliaries':
                result = self.resume_auxiliaries()
            elif action == 'pause-auxiliaries':
                result = self.pause_auxiliaries()
            elif action == 'verify-membership' and len(args) == 3:
                result = self.verify_membership(args[0], args[1], args[2])
            elif action == 'restore-unapplied':
                result = self.restore_unapplied()
            elif action == 'restore-fenced':
                result = self.restore_fenced()
            else:
                raise Refused('unsupported controller action')
            request.update({'status': 'completed', 'completed_at': time.time(), 'result': result})
            private_json(path, request)
            return request
        except Exception as error:
            if intent_written:
                failure = {'class': type(error).__name__, 'action': action,
                           'elapsed_seconds': round(time.perf_counter() - action_started, 6),
                           'outcome': 'unknown_requires_readback'}
                if isinstance(error, CommandFailure):
                    failure['command'] = error.safe_metadata
                elif isinstance(error, RuntimeReadinessFailure):
                    failure['readiness'] = error.safe_metadata
                if hasattr(getattr(self, 'lifecycle', None), 'last_stop_metadata'):
                    failure['stop'] = self.lifecycle.last_stop_metadata
                # Keep the original uncertain-operation marker. A failed
                # executor does not establish that its mutation was rejected.
                request.update(execution_status='failed', failed_at=time.time(), failure=failure)
                private_json(path, request)
            raise
        finally:
            os.close(lock)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', required=True)
    parser.add_argument('--manifest-sha256', required=True)
    parser.add_argument('--step', required=True)
    parser.add_argument('action')
    parser.add_argument('arguments', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    controller = Controller(args.manifest, args.manifest_sha256)
    result = controller.execute(args.step, args.action, args.arguments)
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (Refused, OSError, ValueError, subprocess.TimeoutExpired) as error:
        # Suppress full command/env-bearing exception reprs.
        print('authority controller: ' + (str(error) if isinstance(error, Refused) else type(error).__name__), file=sys.stderr)
        raise SystemExit(1)
