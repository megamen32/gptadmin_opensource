#!/usr/bin/env python3
"""One-way ingress failover. Never changes auth authority or retries DNS writes."""
import argparse
import fcntl
import hashlib
import ipaddress
import json
import math
import os
from pathlib import Path
import re
import selectors
import signal
import stat
import subprocess
import sys
import time
from types import SimpleNamespace
from urllib.parse import urlsplit

import gptadmin_auth_sync as auth_sync

LIMIT = 16384
HERE = Path(__file__).resolve().parent


class ProviderConflict(Exception):
    """Only explicit, allowlisted adapter conflicts may become durable errors."""


def bounded_json(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise ValueError('not_regular_file')
        data = stream.read(LIMIT + 1)
    if len(data) > LIMIT:
        raise ValueError('file_too_large')
    value = json.loads(data)
    if not isinstance(value, dict):
        raise ValueError('object_required')
    return value


def positive(value, maximum):
    return type(value) in (int, float) and math.isfinite(value) and 0 < value <= maximum


def load_config(path):
    c = bounded_json(path)
    required = {'primary', 'candidate', 'auth_sync', 'provider_argv', 'name', 'expected', 'value', 'state_file'}
    optional = {'failure_threshold', 'interval', 'probe_timeout', 'provider_timeout'}
    if not required <= c.keys() or c.keys() - required - optional:
        raise ValueError('config_fields_invalid')
    for key, default in [('failure_threshold', 3), ('interval', 10), ('probe_timeout', 15), ('provider_timeout', 95)]:
        c.setdefault(key, default)
    if type(c['failure_threshold']) is not int or not 1 <= c['failure_threshold'] <= 100:
        raise ValueError('failure_threshold_invalid')
    if not positive(c['interval'], 3600) or c['interval'] < 1 or any(not positive(c[k], 300) for k in ('probe_timeout', 'provider_timeout')):
        raise ValueError('duration_invalid')
    for name in ('primary', 'candidate'):
        endpoint = c[name]
        if (not isinstance(endpoint, dict) or not {'url', 'target', 'token_env'} <= endpoint.keys()
                or endpoint.keys() - {'url', 'target', 'token_env', 'connect_to', 'allow_loopback_http'}):
            raise ValueError('probe_config_invalid')
        if type(endpoint.get('allow_loopback_http', False)) is not bool:
            raise ValueError('loopback_option_invalid')
        origin = auth_sync.validate_origin(endpoint['url'], endpoint.get('allow_loopback_http', False))
        endpoint['url'] = origin.geturl().rstrip('/')
        endpoint.setdefault('connect_to', '')
        if endpoint['connect_to']:
            if origin.scheme != 'https':
                raise ValueError('pin_requires_https')
            endpoint['connect_to'] = str(ipaddress.ip_address(endpoint['connect_to']))
        if not isinstance(endpoint['target'], str) or not re.fullmatch(r'shell:[^\s/\\]+', endpoint['target']):
            raise ValueError('target_invalid')
        if not isinstance(endpoint['token_env'], str) or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', endpoint['token_env']):
            raise ValueError('token_env_invalid')
    if c['primary']['target'] == c['candidate']['target']:
        raise ValueError('distinct_executors_required')
    sync = c['auth_sync']
    fields = {'writer_url', 'writer_connect_to', 'reader_url', 'source_id', 'status_file', 'max_age', 'allow_loopback_http'}
    if not isinstance(sync, dict) or sync.keys() != fields or not positive(sync['max_age'], 86400):
        raise ValueError('auth_sync_config_invalid')
    if type(sync['allow_loopback_http']) is not bool:
        raise ValueError('auth_sync_loopback_invalid')
    _, binding = auth_sync.configuration(SimpleNamespace(**sync))
    primary_url, writer_url = urlsplit(c['primary']['url']), urlsplit(binding['writer_url'])
    same_pin = binding['writer_connect_to'] == c['primary']['connect_to']
    pinned_alias = (same_pin and bool(binding['writer_connect_to'])
                    and primary_url.scheme == writer_url.scheme == 'https'
                    and (primary_url.port if primary_url.port is not None else 443)
                    == (writer_url.port if writer_url.port is not None else 443))
    if not (same_pin and binding['writer_url'] == c['primary']['url']) and not pinned_alias:
        raise ValueError('sync_source_must_match_physical_primary')
    for key in ('writer_url', 'writer_connect_to', 'reader_url'):
        sync[key] = binding[key]
    for value in (c['state_file'], sync['status_file']):
        if not isinstance(value, str) or not Path(value).is_absolute():
            raise ValueError('absolute_state_paths_required')
    if c['state_file'] == sync['status_file']:
        raise ValueError('distinct_state_paths_required')
    argv = c['provider_argv']
    if not isinstance(argv, list) or not argv or not all(isinstance(v, str) and v and '\x00' not in v for v in argv):
        raise ValueError('provider_argv_invalid')
    if not isinstance(c['name'], str) or len(c['name']) > 253 or not re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?', c['name']):
        raise ValueError('dns_name_invalid')
    for key in ('expected', 'value'):
        c[key] = str(ipaddress.ip_address(c[key]))
    if c['expected'] == c['value']:
        raise ValueError('distinct_dns_values_required')
    for side, key in (('primary', 'expected'), ('candidate', 'value')):
        if c[side]['connect_to'] and c[side]['connect_to'] != c[key]:
            raise ValueError('probe_pin_must_match_dns_value')
    return c


def config_digest(config):
    return hashlib.sha256(json.dumps(config, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def load_state(path, binding):
    try:
        state = bounded_json(path)
    except FileNotFoundError:
        return {'version': 1, 'binding': binding, 'phase': 'watching', 'failures': 0}
    if (type(state.get('version')) is not int or state['version'] != 1 or state.get('binding') != binding
            or state.get('phase') not in ('watching', 'pending', 'promoted', 'blocked')
            or type(state.get('failures')) is not int or not 0 <= state['failures'] <= 100):
        raise ValueError('state_invalid_or_configuration_changed')
    if state['phase'] in ('pending', 'promoted') and not positive(state.get('decision_at'), 1e15):
        raise ValueError('decision_timestamp_required')
    return state


def save_state(path, state):
    # Existing atomic writer fsyncs file and parent directory; fixed temp file,
    # O_NOFOLLOW and caller-held lock. At most state, lock and transient temp.
    auth_sync.write_status(path, state)


def child_json(argv, timeout, payload=None):
    """Bounded stdout; discard stderr rather than expose provider/credential data."""
    data = None if payload is None else json.dumps(payload).encode()
    proc = subprocess.Popen(argv, stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    output = bytearray()
    deadline = time.monotonic() + timeout
    try:
        if data is not None:
            proc.stdin.write(data)
            proc.stdin.close()  # provider input is tiny, smaller than pipe capacity
        with selectors.DefaultSelector() as selector:
            selector.register(proc.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError()
                for key, _ in selector.select(remaining):
                    chunk = os.read(key.fd, LIMIT + 1 - len(output))
                    if not chunk:
                        selector.unregister(key.fileobj)
                    else:
                        output.extend(chunk)
                        if len(output) > LIMIT:
                            raise ValueError('child_output_too_large')
        rc = proc.wait(timeout=max(.001, deadline - time.monotonic()))
        if rc != 0:
            raise ValueError('child_failed')
        value = json.loads(output)
        if not isinstance(value, dict):
            raise ValueError('child_response_invalid')
        return value
    finally:
        # Kill owned descendants as well on timeout/error; no provider retry.
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        proc.wait()
        proc.stdout.close()
        if proc.stdin and not proc.stdin.closed:
            proc.stdin.close()


def probe(config, which):
    endpoint = config[which]
    argv = [sys.executable, str(HERE / 'gptadmin_node_probe.py'), '--url', endpoint['url'],
            '--target', endpoint['target'], '--token-env', endpoint['token_env'],
            '--timeout', str(config['probe_timeout'])]
    if endpoint.get('connect_to'):
        argv += ['--connect-to', endpoint['connect_to']]
    try:
        value = child_json(argv, config['probe_timeout'] + 2)
        return (value.get('ok') is True and value.get('origin') == endpoint['url']
                and value.get('target') == endpoint['target']
                and value.get('connect_to') == (endpoint.get('connect_to') or None)
                and isinstance(value.get('job_id'), str) and bool(value['job_id'])
                and isinstance(value.get('stdout'), str) and value['stdout'].startswith('node-probe-'))
    except Exception:
        return False


def fresh(config):
    sync = config['auth_sync']
    try:
        _, binding = auth_sync.configuration(SimpleNamespace(**sync))
        status = auth_sync.read_status(Path(sync['status_file']))
        return auth_sync.freshness(status, sync['max_age'], expected=binding)['promotion_eligible']
    except Exception:
        return False


def provider(config, action):
    value = child_json(config['provider_argv'], config['provider_timeout'],
                       {'action': action, 'name': config['name'], 'expected': config['expected'], 'value': config['value']})
    if value.get('ok') is False and value.get('error') in ('other_records_changed', 'record_conflict'):
        raise ProviderConflict(value['error'])
    if value.get('ok') is not True or not isinstance(value.get('value'), str):
        raise ValueError('provider_reply_invalid')
    return str(ipaddress.ip_address(value['value']))


def step(config, state, persist):
    state = dict(state)
    if state['phase'] in ('promoted', 'blocked'):
        return state
    if state['phase'] == 'pending':
        try:
            actual = provider(config, 'read')
            if actual == config['value']:
                state.update(phase='promoted', last_error='')
            elif actual == config['expected']:
                state['last_error'] = 'pending_not_observed_no_write_retry'
            else:
                state.update(phase='blocked', last_error='dns_value_conflict')
        except ProviderConflict as error:
            state.update(phase='blocked', last_error=str(error))
            persist(state)
        except Exception:
            state['last_error'] = 'pending_read_failed'
        return state
    if probe(config, 'primary'):
        state.update(failures=0, last_error='')
        return state
    state['failures'] = min(config['failure_threshold'], state['failures'] + 1)
    state['last_error'] = 'primary_probe_failed'
    if state['failures'] < config['failure_threshold']:
        return state
    if not probe(config, 'candidate'):
        state['last_error'] = 'candidate_probe_failed'
        return state
    # Check after the potentially slow real command, not before it.
    if not fresh(config):
        state['last_error'] = 'candidate_auth_not_fresh'
        return state
    try:
        actual = provider(config, 'read')
    except ProviderConflict as error:
        state.update(phase='blocked', last_error=str(error))
        persist(state)
        return state
    except Exception:
        state['last_error'] = 'provider_read_failed'
        return state
    if actual != config['expected']:
        state.update(phase='blocked', last_error='dns_value_conflict')
        return state
    # Read may have consumed the remainder of the freshness window.
    if not fresh(config):
        state['last_error'] = 'candidate_auth_not_fresh'
        return state
    state.update(phase='pending', decision_at=time.time(), last_error='')
    persist(state)  # MUST succeed durably before the sole mutation attempt.
    try:
        actual = provider(config, 'compare_and_set')
        if actual == config['value']:
            state.update(phase='promoted', last_error='')
        else:
            state['last_error'] = 'pending_write_unconfirmed'
    except ProviderConflict as error:
        state.update(phase='blocked', last_error=str(error))
        persist(state)
    except Exception:
        state['last_error'] = 'pending_write_unconfirmed'
    return state


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('once', 'run'))
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    # Unwind child_json's finally block on service stop, including owned children.
    def stop(signum, frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, stop)
    config = load_config(args.config)
    path = Path(config['state_file'])
    # Parent is explicitly provisioned; never create directory trees here.
    fd = os.open(str(path) + '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
            raise ValueError('lock_not_regular')
        os.fchmod(lock.fileno(), 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        state = load_state(path, config_digest(config))
        while True:
            next_state = step(config, state, lambda s: save_state(path, s))
            changed = next_state != state or not path.exists()
            if changed:
                save_state(path, next_state)
            state = next_state
            if changed or args.action == 'once':
                print(json.dumps({'phase': state['phase'], 'failures': state['failures'],
                                  'last_error': state.get('last_error', '')}), flush=True)
            if args.action == 'once':
                return 0 if state['phase'] == 'promoted' or not state.get('last_error') else 1
            time.sleep(config['interval'])


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as error:
        # No raw child output, URLs, response bodies or configuration on errors.
        print(json.dumps({'ok': False, 'error': type(error).__name__}), file=sys.stderr)
        sys.exit(2)
