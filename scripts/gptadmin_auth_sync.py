#!/usr/bin/env python3
"""Copy bounded auth snapshots; report promotion eligibility, never promote nodes."""
import argparse
import fcntl
import http.client
import ipaddress
import json
import math
import os
from pathlib import Path
import signal
import socket
import ssl
import stat
import time
import urllib.parse


STATUS_LIMIT = 4096
MAX_BUDGET = 10 << 20


def validate_origin(value, allow_loopback=False):
    url = urllib.parse.urlsplit(value)
    if (url.scheme not in ('https', 'http') or not url.hostname or url.username
            or url.password or url.path not in ('', '/') or url.query or url.fragment):
        raise ValueError('invalid_origin')
    _ = url.port
    if url.scheme == 'http':
        try:
            loopback = ipaddress.ip_address(url.hostname).is_loopback
        except ValueError:
            loopback = False
        if not allow_loopback or not loopback:
            raise ValueError('https_required_except_explicit_loopback')
    return url


def validate_ack(ack, bundle):
    if (not isinstance(ack, dict) or ack.get('ok') is not True
            or ack.get('writer_id') != bundle['writer_id']
            or type(ack.get('generation')) is not int
            or ack['generation'] != bundle['generation']):
        raise ValueError('reader_ack_mismatch')


def freshness(state, max_age, now=None, expected=None):
    now = time.time() if now is None else now
    success = state.get('last_success')
    valid = (type(state.get('version')) is int and state['version'] == 1
             and type(state.get('snapshot_bytes')) is int and 0 < state['snapshot_bytes'] <= MAX_BUDGET
             and type(state.get('generation')) is int and state['generation'] > 0
             and isinstance(state.get('writer_id'), str) and bool(state['writer_id'])
             and type(success) in (int, float) and math.isfinite(success)
             and success > 0 and now >= success)
    age = now - success if valid else None
    bound = (expected is not None and state.get('binding') == expected
             and state.get('writer_id') == expected.get('source_id'))
    return {**state, 'age_seconds': round(age, 3) if age is not None else None,
            'max_age_seconds': max_age, 'configuration_bound': bound,
            'promotion_eligible': valid and bound and age <= max_age}


def configuration(args):
    urls = [validate_origin(u, args.allow_loopback_http) for u in (args.writer_url, args.reader_url)]
    if not args.source_id or len(args.source_id) > 128 or args.source_id.strip() != args.source_id:
        raise ValueError('source_identity_required')
    connect_to = ''
    if args.writer_connect_to:
        if urls[0].scheme != 'https':
            raise ValueError('connect_to_requires_https')
        connect_to = str(ipaddress.ip_address(args.writer_connect_to))
    binding = {'writer_url': urls[0].geturl().rstrip('/'), 'reader_url': urls[1].geturl().rstrip('/'),
               'writer_connect_to': connect_to, 'source_id': args.source_id}
    return urls, binding


def read_status(path):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return {}
    with os.fdopen(fd, 'rb') as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise ValueError('status_not_regular')
        raw = stream.read(STATUS_LIMIT + 1)
    if len(raw) > STATUS_LIMIT:
        raise ValueError('status_oversize')
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError('status_invalid')
    return value


def write_status(path, state):
    raw = (json.dumps(state, sort_keys=True) + '\n').encode()
    if len(raw) > STATUS_LIMIT:
        raise ValueError('status_oversize')
    temp = path.with_name(path.name + '.tmp')
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, 'wb') as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
        parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        if temp.exists():
            temp.unlink()


class Endpoint:
    """One reusable connection per endpoint; http.client never retries a POST."""
    def __init__(self, url, token, timeout, label, connect_to=None):
        self.label, self.token = label, token
        cls = http.client.HTTPSConnection if url.scheme == 'https' else http.client.HTTPConnection
        kwargs = {'context': ssl.create_default_context()} if url.scheme == 'https' else {}
        if connect_to:
            if url.scheme != 'https':
                raise ValueError('connect_to_requires_https')
            ipaddress.ip_address(connect_to)
            class DirectHTTPSConnection(http.client.HTTPSConnection):
                def connect(self):
                    sock = socket.create_connection((connect_to, self.port), self.timeout)
                    try:
                        self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
                    except Exception:
                        sock.close()
                        raise
            cls = DirectHTTPSConnection
        self.connection = cls(url.hostname, url.port, timeout=timeout, **kwargs)

    def post(self, path, body, limit):
        try:
            self.connection.request('POST', path, body=body, headers={
                'Authorization': 'Bearer ' + self.token, 'Content-Type': 'application/json',
                'Accept': 'application/json', 'Cache-Control': 'no-store'})
            response = self.connection.getresponse()
            if response.status != 200:
                raise ValueError(self.label + '_http_' + str(response.status))
            raw = response.read(limit + 1)
            if len(raw) > limit:
                raise ValueError(self.label + '_response_oversize')
            return raw
        except Exception:
            self.connection.close()
            raise

    def close(self):
        self.connection.close()


def sync_once(writer, reader, source_id, budget, state):
    started = time.time()
    raw = writer.post('/admin/api/auth-snapshot/export', b'{}', budget)
    bundle = json.loads(raw)
    if (not isinstance(bundle, dict) or bundle.get('writer_id') != source_id
            or type(bundle.get('generation')) is not int or bundle['generation'] <= 0
            or bundle['generation'] <= state.get('generation', 0)
            or bundle.get('version') != 1 or 'profiles' not in bundle):
        raise ValueError('writer_snapshot_mismatch')
    ack = json.loads(reader.post('/admin/api/auth-snapshot/apply', raw, STATUS_LIMIT))
    validate_ack(ack, bundle)
    return {'version': 1, 'writer_id': source_id, 'generation': bundle['generation'],
            # Count from export request start, not from potentially delayed receipt.
            'last_success': started, 'last_attempt': started, 'last_error': None,
            'snapshot_bytes': len(raw)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['once', 'run', 'check'])
    parser.add_argument('--writer-url', default=os.getenv('GPTADMIN_AUTH_SYNC_WRITER_URL', ''))
    parser.add_argument('--reader-url', default=os.getenv('GPTADMIN_AUTH_SYNC_READER_URL', ''))
    parser.add_argument('--writer-connect-to', default=os.getenv('GPTADMIN_AUTH_SYNC_WRITER_CONNECT_TO', ''))
    parser.add_argument('--source-id', default=os.getenv('GPTADMIN_AUTH_SOURCE_ID', ''))
    parser.add_argument('--writer-token-env', default=os.getenv('GPTADMIN_AUTH_SYNC_WRITER_TOKEN_ENV', 'GPTADMIN_AUTH_SYNC_WRITER_TOKEN'))
    parser.add_argument('--reader-token-env', default=os.getenv('GPTADMIN_AUTH_SYNC_READER_TOKEN_ENV', 'CTL_TOKEN'))
    parser.add_argument('--status-file', type=Path, default=os.getenv('GPTADMIN_AUTH_SYNC_STATUS_FILE'))
    parser.add_argument('--interval', type=float, default=os.getenv('GPTADMIN_AUTH_SYNC_INTERVAL_S', '30'))
    parser.add_argument('--max-age', type=float, default=os.getenv('GPTADMIN_AUTH_SYNC_MAX_AGE_S', '120'))
    parser.add_argument('--timeout', type=float, default=os.getenv('GPTADMIN_AUTH_SYNC_TIMEOUT_S', '10'))
    parser.add_argument('--max-bytes', type=int, default=os.getenv('GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES', str(256 << 10)))
    parser.add_argument('--allow-loopback-http', action='store_true')
    args = parser.parse_args()
    if (not args.status_file or not args.status_file.is_absolute()
            or not all(math.isfinite(v) and v > 0 for v in (args.interval, args.max_age, args.timeout))
            or args.interval < 1 or args.timeout > 300 or not 1 <= args.max_bytes <= MAX_BUDGET):
        parser.error('absolute status path and valid bounded durations/budget required')
    try:
        state = read_status(args.status_file)
        if args.action == 'check':
            expected = None
            if args.source_id or args.writer_url or args.reader_url or args.writer_connect_to:
                _, expected = configuration(args)
            result = freshness(state, args.max_age, expected=expected)
            print(json.dumps(result))
            return 0 if result['promotion_eligible'] else 1
        urls, binding = configuration(args)
        tokens = [os.getenv(k, '') for k in (args.writer_token_env, args.reader_token_env)]
        if any(not t or any(ord(c) < 33 or ord(c) > 126 for c in t) for t in tokens):
            raise ValueError('token_environment_invalid')
        if state and (state.get('writer_id') != args.source_id or state.get('binding') != binding):
            raise ValueError('status_configuration_mismatch')
        # Parent is provisioned by the operator; never create arbitrary directory trees.
        lock_fd = os.open(str(args.status_file) + '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        writer = Endpoint(urls[0], tokens[0], args.timeout, 'writer', args.writer_connect_to)
        reader = Endpoint(urls[1], tokens[1], args.timeout, 'reader')
        stopped = False
        def stop(signum, frame):
            nonlocal stopped
            stopped = True
        def timeout(signum, frame):
            raise TimeoutError()
        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        signal.signal(signal.SIGALRM, timeout)
        try:
            while not stopped:
                attempt = time.time()
                try:
                    signal.setitimer(signal.ITIMER_REAL, args.timeout)
                    next_state = sync_once(writer, reader, args.source_id, args.max_bytes, state)
                    ok = True
                except Exception as error:
                    # Only locally constructed fixed codes may reach disk/stdout.
                    code = str(error) if type(error) is ValueError and str(error).startswith(
                        ('writer_http_', 'reader_http_', 'writer_response_', 'reader_response_',
                         'writer_snapshot_', 'reader_ack_')) else type(error).__name__
                    next_state = {**state, 'version': 1, 'writer_id': args.source_id,
                                  'last_attempt': attempt, 'last_error': code}
                    ok = False
                    writer.close(); reader.close()
                finally:
                    signal.setitimer(signal.ITIMER_REAL, 0)
                next_state['binding'] = binding
                write_status(args.status_file, next_state)
                state = next_state
                print(json.dumps(freshness(state, args.max_age, expected=binding)), flush=True)
                if args.action == 'once':
                    return 0 if ok else 1
                until = time.monotonic() + args.interval
                while not stopped and time.monotonic() < until:
                    time.sleep(min(.2, max(0, until - time.monotonic())))
            return 0
        finally:
            writer.close(); reader.close()
            os.close(lock_fd)
    except Exception as error:
        print(json.dumps({'promotion_eligible': False, 'error': type(error).__name__}))
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
