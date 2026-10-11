#!/usr/bin/env python3
"""GPTAdmin Node lifecycle manager: install, status, update, rollback, uninstall, grant, join.

Portable, stdlib-only. Dispatched by the CLI as `gptadmin node ...`.
Layout: /opt/gptadmin-node/versions/<git_commit>-<n>/ + atomic `current` symlink,
state /var/lib/gptadmin-node/<name>/, config /etc/gptadmin-node/<name>/,
systemd template unit gptadmin-node@.service (instance gptadmin-node@<name>).
Exit codes: 0 ok, 2 usage, 3 preflight, 4 runtime.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform as platform_module
import pwd
import re
import secrets
import shutil
import socket
import string
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

PACKAGE_SCHEMA = 'gptadmin.node-package/v1'
IDENTITY_SCHEMA = 'gptadmin.node-identity/v1'
REQUIRED_PACKAGE_FILES = ('bin/gptadmin-node',)
DISK_FLOOR_BYTES = 500 * 1024 * 1024
HEALTH_TIMEOUT_SECS = 30.0
DRAIN_POLL_INTERVAL_SECS = 2.0
HTTP_CALL_TIMEOUT_SECS = 5.0
JOIN_HTTP_TIMEOUT_SECS = 15.0
ELF_MAGIC = b'\x7fELF'
DEFAULT_BIND = ('127.0.0.1', 9001)
DRAIN_ENDPOINT = '/admin/api/node/drain'
JOIN_ENDPOINT = '/admin/api/nodes/join'
GRANT_ENDPOINT = '/admin/api/nodes/grant'
NODES_ENDPOINT = '/admin/api/nodes'
NODE_CAPABILITIES = ['ingress', 'executor', 'replica']
DEFAULT_BIND_TEXT = f'{DEFAULT_BIND[0]}:{DEFAULT_BIND[1]}'

EX_OK, EX_USAGE, EX_PREFLIGHT, EX_RUNTIME = 0, 2, 3, 4

# Exact key order of the generated systemd EnvironmentFile.
ENV_KEY_ORDER = (
    'SHELL_TOKEN',
    'CTL_TOKEN',
    'ADMIN_PASSWORD',  # legacy local admin line; present only when explicitly provided
    'GPTADMIN_HUB_HOST',
    'GPTADMIN_HUB_PORT',
    'GPTADMIN_ROOT',
    'GPTADMIN_CONFIG_DIR',
    'GPTADMIN_ENV_FILE',
    'PUBLIC_ORIGIN',
    'GPTADMIN_AUTH_MODE',
    'SHELL_NAME',
    'SHELL_IDENTITY_DIR',
    'SHELL_SPOOL_DIR',
    'SHELL_OUTBOX_DIR',
    'SHELL_DEFAULT_USER',
    'SHELL_DEFAULT_CWD',
    'SHELL_DEFAULT_HOME',
    'SHELLMCP_SELF_REPAIR_DISABLE',
    'ROOTD_AUTO_UPDATE',
    'SHELL_SERVICE_NAME',
    'ROOTD_SERVICE_NAME',
    'GPTADMIN_NODE_IDENTITY',
    'GPTADMIN_NODE_SEED',
    'GPTADMIN_NODE_PEERS',
    'GPTADMIN_AUTH_SOURCE_ID',
)


class UsageError(RuntimeError):
    """Invalid operator input; maps to exit code 2."""


class PreflightError(RuntimeError):
    """Fail-closed pre-install/update check; maps to exit code 3."""


class RuntimeFailure(RuntimeError):
    """Operation failed after it started; maps to exit code 4."""


def fail(message: str, code: int) -> 'NoReturn':  # type: ignore[valid-type]
    print(f'gptadmin-node-manage: {message}', file=sys.stderr)
    sys.exit(code)


def utc_now_iso() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace('+00:00', 'Z')


# ---------------------------------------------------------------------------
# Paths (injectable so the local logic is testable in temp directories)
# ---------------------------------------------------------------------------


class Layout:
    """Filesystem roots of the Node lifecycle. Tests pass temp directories."""

    def __init__(self, opt_root=Path('/opt/gptadmin-node'),
                 state_root=Path('/var/lib/gptadmin-node'),
                 config_root=Path('/etc/gptadmin-node'),
                 unit_path=Path('/etc/systemd/system/gptadmin-node@.service')):
        self.opt_root = Path(opt_root)
        self.state_root = Path(state_root)
        self.config_root = Path(config_root)
        self.unit_path = Path(unit_path)

    def versions_root(self) -> Path:
        return self.opt_root / 'versions'

    def current_link(self) -> Path:
        return self.opt_root / 'current'

    def state(self, name: str) -> Path:
        return self.state_root / name

    def identity_file(self, name: str) -> Path:
        return self.state(name) / 'node-identity.json'

    def spool_dir(self, name: str) -> Path:
        return self.state(name) / 'spool'

    def outbox_dir(self, name: str) -> Path:
        return self.spool_dir(name) / 'outbox'

    def config(self, name: str) -> Path:
        return self.config_root / name

    def env_file(self, name: str) -> Path:
        return self.config(name) / 'gptadmin.env'

    def owner_credential(self, name: str) -> Path:
        return self.config(name) / 'owner.credential'

    def unit_instance(self, name: str) -> str:
        return f'gptadmin-node@{name}.service'


DEFAULT_LAYOUT = Layout()


# ---------------------------------------------------------------------------
# Small shared helpers
# ---------------------------------------------------------------------------


class Runner:
    """Process boundary for systemctl; replaced only by real callers, never faked in tests."""

    def run(self, argv, timeout=None):
        return subprocess.run(argv, capture_output=True, text=True, check=False, timeout=timeout)


def systemctl(runner: Runner, *args: str, check: bool = True):
    result = runner.run(['systemctl', *args])
    if check and result.returncode != 0:
        detail = (result.stderr or result.stdout or '').strip().splitlines()
        raise RuntimeFailure(
            f"systemctl {' '.join(args)} failed (rc={result.returncode}): {detail[-1] if detail else 'no output'}")
    return result


def atomic_write(path: Path, text: str, mode: int) -> None:
    """Write file contents atomically with the requested permission bits."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f'{path.name}.new-{os.getpid()}')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, 'w') as handle:
        handle.write(text)
    os.chmod(tmp, mode)
    os.replace(tmp, path)


def write_secret_file(path: Path, secret: str) -> None:
    atomic_write(path, secret + '\n', 0o600)


def http_json(method: str, url: str, *, token: str | None = None, body=None,
              timeout: float = HTTP_CALL_TIMEOUT_SECS):
    """One real HTTP request. Returns (status, parsed JSON or None). Connection errors raise."""
    headers = {'Accept': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers['Content-Type'] = 'application/json'
        # urllib omits Content-Length on some proxies; nginx will not deliver the
        # body without it. Setting it explicitly keeps the request body intact.
        headers['Content-Length'] = str(len(data))
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=timeout) as response:
            status, raw = response.status, response.read(4 * 1024 * 1024 + 1)
    except urllib.error.HTTPError as error:
        status = error.code
        try:
            raw = error.read(4 * 1024 * 1024 + 1)
        except Exception:
            raw = b''
    payload = None
    if raw:
        try:
            payload = json.loads(raw)
        except ValueError:
            payload = None
    return status, payload


def validate_name(name: str) -> None:
    allowed = set(string.ascii_letters + string.digits + '_-.')
    if not name or len(name) > 64 or name[0] not in string.ascii_letters + string.digits \
            or not set(name) <= allowed:
        raise UsageError(f'invalid node name {name!r}; use letters, digits, ".", "_" or "-"')


def parse_bind(text: str | None) -> tuple[str, int]:
    if text is None:
        return DEFAULT_BIND
    match = re.fullmatch(r'(\S+):(\d{1,5})', text.strip())
    if not match:
        raise UsageError(f'--bind must be HOST:PORT, got {text!r}')
    port = int(match.group(2))
    if not 1 <= port <= 65535:
        raise UsageError(f'--bind port out of range: {port}')
    return match.group(1), port


def validate_origin(text: str, label: str) -> str:
    parsed = urllib.parse.urlsplit(text)
    if parsed.scheme not in ('http', 'https') or not parsed.hostname or parsed.username \
            or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/'):
        raise UsageError(f'{label} must be an origin (scheme://host[:port]) without credentials, query or path')
    return text.rstrip('/')


def confirm(yes: bool, prompt: str) -> bool:
    if yes:
        return True
    if not sys.stdin.isatty():
        return False
    try:
        return input(prompt).strip().lower() in ('y', 'yes')
    except EOFError:
        return False


# ---------------------------------------------------------------------------
# Package verification (fail closed before any install mutation)
# ---------------------------------------------------------------------------


def host_arch() -> str:
    machine = platform_module.machine().lower()
    mapping = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}
    arch = mapping.get(machine)
    if not arch:
        raise PreflightError(f'unsupported host machine {machine!r}; this manager installs linux/amd64 or linux/arm64')
    return arch


def verify_manifest_schema(manifest, expected_arch: str) -> dict:
    if not isinstance(manifest, dict):
        raise PreflightError('manifest.json is not a JSON object')
    if manifest.get('schema') != PACKAGE_SCHEMA:
        raise PreflightError(f'unsupported package schema {manifest.get("schema")!r}; expected {PACKAGE_SCHEMA!r}')
    if manifest.get('platform') != 'linux':
        raise PreflightError(f'package platform {manifest.get("platform")!r} is not linux')
    if manifest.get('arch') != expected_arch:
        raise PreflightError(f'package arch {manifest.get("arch")!r} does not match this host ({expected_arch!r})')
    files = manifest.get('files')
    if not isinstance(files, dict) or not files:
        raise PreflightError('manifest lists no files')
    for required in REQUIRED_PACKAGE_FILES:
        if required not in files:
            raise PreflightError(f'manifest does not ship {required}')
    for name, meta in files.items():
        if not isinstance(meta, dict):
            raise PreflightError(f'manifest entry for {name} is not an object')
        sha = meta.get('sha256')
        if not isinstance(sha, str) or not re.fullmatch(r'[0-9a-f]{64}', sha):
            raise PreflightError(f'manifest sha256 for {name} is not a lowercase hex digest')
        size = meta.get('size')
        if not isinstance(size, int) or isinstance(size, bool) or size < 0:
            raise PreflightError(f'manifest size for {name} is not a non-negative integer')
    return manifest


def verify_and_extract(package: Path, expected_arch: str, dest: Path) -> dict:
    """Verify every manifest digest, then extract only the whitelisted tree into dest."""
    try:
        tar = tarfile.open(package, 'r:gz')
    except (tarfile.TarError, OSError) as error:
        raise PreflightError(f'cannot read package {package}: {type(error).__name__}') from None
    with tar:
        members = {}
        for member in tar.getmembers():
            if member.name.startswith('/') or '..' in Path(member.name).parts:
                raise PreflightError(f'unsafe package member name: {member.name}')
            members[member.name] = member
        manifest_member = members.get('manifest.json')
        if manifest_member is None or not manifest_member.isfile():
            raise PreflightError('package has no readable manifest.json member')
        try:
            manifest = json.loads(tar.extractfile(manifest_member).read())
        except (ValueError, OSError) as error:
            raise PreflightError(f'manifest.json is not valid JSON: {type(error).__name__}') from None
        verify_manifest_schema(manifest, expected_arch)
        listed = manifest['files']
        for name, meta in sorted(listed.items()):
            member = members.get(name)
            if member is None or not member.isfile():
                raise PreflightError(f'package is missing manifest-listed file {name}')
            digest = hashlib.sha256()
            size = 0
            stream = tar.extractfile(member)
            if stream is None:
                raise PreflightError(f'cannot read package file {name}')
            with stream:
                for block in iter(lambda: stream.read(1024 * 1024), b''):
                    digest.update(block)
                    size += len(block)
            if digest.hexdigest() != meta['sha256'] or size != meta['size']:
                raise PreflightError(f'package file {name} fails manifest digest/size verification')
        # Everything verified; materialize exactly the manifest tree.
        dest.mkdir(parents=True, exist_ok=True)
        (dest / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
        for name in listed:
            target = dest / name
            target.parent.mkdir(parents=True, exist_ok=True)
            with tar.extractfile(members[name]) as source, open(target, 'wb') as out:
                shutil.copyfileobj(source, out)
            os.chmod(target, 0o755 if name == 'bin/gptadmin-node' else 0o644)
    return manifest


def verify_elf_binary(path: Path) -> None:
    """Static smoke only: never execute the node binary during preflight."""
    if not path.is_file():
        raise PreflightError(f'package binary is missing: {path}')
    if not os.access(path, os.X_OK):
        raise PreflightError(f'package binary is not executable: {path}')
    with open(path, 'rb') as handle:
        magic = handle.read(4)
    if magic != ELF_MAGIC:
        raise PreflightError(f'package binary is not an ELF executable: {path}')


def existing_ancestor(path: Path) -> Path:
    path = Path(path)
    probe = path
    while not probe.exists():
        if probe.parent == probe:
            raise PreflightError(f'no existing filesystem ancestor for {path}')
        probe = probe.parent
    return probe


def check_disk_floor(path: Path, label: str) -> None:
    usage = shutil.disk_usage(existing_ancestor(path))
    if usage.free < DISK_FLOOR_BYTES:
        raise PreflightError(f'only {usage.free // (1024 * 1024)}MB free on {label} ({path}); need 500MB')


def port_is_free(host: str, port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            sock.bind((host, port))
            return True
        except OSError:
            return False


def require_root() -> None:
    if os.geteuid() != 0:
        raise PreflightError('root is required for /opt/gptadmin-node, /etc/systemd and systemctl')


# ---------------------------------------------------------------------------
# Identity, version dirs, current switch, env file
# ---------------------------------------------------------------------------


def ensure_identity(path: Path) -> dict:
    """Create the node identity exactly once; an existing file is never overwritten."""
    if path.exists():
        try:
            identity = json.loads(path.read_text())
        except (ValueError, OSError) as error:
            raise RuntimeFailure(
                f'existing identity file {path} is unreadable ({type(error).__name__}); refusing to overwrite') from None
        if not isinstance(identity, dict) or not identity.get('node_id') or not identity.get('node_key'):
            raise RuntimeFailure(f'existing identity file {path} lacks node_id/node_key; refusing to overwrite')
        return identity
    identity = {
        'schema': IDENTITY_SCHEMA,
        'node_id': secrets.token_hex(16),
        'node_key': secrets.token_hex(32),
        'created': utc_now_iso(),
    }
    atomic_write(path, json.dumps(identity, indent=2) + '\n', 0o600)
    return identity


def merge_identity(path: Path, extra: dict) -> dict:
    """Merge manager-owned fields (e.g. writer_url) into the identity; keeps 0600."""
    identity = ensure_identity(path)
    identity.update({key: value for key, value in extra.items() if value is not None})
    atomic_write(path, json.dumps(identity, indent=2) + '\n', 0o600)
    return identity


def next_version_dir(versions_root: Path, git_commit: str) -> Path:
    """<git_commit>-<n> with the smallest unused n; never overwrites an existing directory."""
    versions_root.mkdir(parents=True, exist_ok=True)
    used = set()
    for entry in versions_root.iterdir():
        if entry.is_dir() and entry.name.startswith(git_commit + '-'):
            suffix = entry.name[len(git_commit) + 1:]
            if suffix.isdigit():
                used.add(int(suffix))
    number = 1
    while number in used:
        number += 1
    return versions_root / f'{git_commit}-{number}'


def switch_current(layout: Layout, target: Path) -> None:
    """Atomically repoint <opt>/current at target (symlink + rename, mv -T semantics)."""
    link = layout.current_link()
    resolved = Path(os.path.realpath(target))
    if not resolved.is_dir():
        raise RuntimeFailure(f'cannot switch current to missing directory {target}')
    tmp = link.with_name(f'current.new-{os.getpid()}')
    if tmp.is_symlink() or tmp.exists():
        tmp.unlink()
    os.symlink(str(resolved), tmp)
    os.replace(tmp, link)


def current_target(layout: Layout) -> Path | None:
    link = layout.current_link()
    if not link.is_symlink() and not link.exists():
        return None
    resolved = Path(os.path.realpath(link))
    return resolved if resolved.is_dir() else None


def select_rollback_dir(versions_root: Path, current: Path | None, explicit: str | None) -> Path:
    """--to picks a named version dir; otherwise the most recently used other version."""
    if explicit:
        candidate = versions_root / explicit
        if not candidate.is_dir():
            raise RuntimeFailure(f'rollback target not found: {explicit}')
        return candidate
    best = None
    best_key = None
    for entry in sorted(versions_root.iterdir()):
        if not entry.is_dir() or entry.name.startswith('.'):
            continue
        if current is not None and os.path.realpath(entry) == os.path.realpath(current):
            continue
        key = (entry.stat().st_mtime_ns, entry.name)
        if best_key is None or key > best_key:
            best, best_key = entry, key
    if best is None:
        raise RuntimeFailure('no previous version directory is available for rollback')
    return best


def quote_env_value(value) -> str:
    text = str(value)
    return '"' + text.replace('\\', '\\\\').replace('"', '\\"') + '"'


def render_env_file(values: dict) -> str:
    """Render the systemd EnvironmentFile; known keys first, then any preserved extras."""
    lines = []
    emitted = set()
    for key in ENV_KEY_ORDER:
        if key in values:
            lines.append(f'{key}={quote_env_value(values[key])}')
            emitted.add(key)
    for key in sorted(set(values) - emitted):
        lines.append(f'{key}={quote_env_value(values[key])}')
    return '\n'.join(lines) + '\n'


def parse_env_file(path: Path) -> dict:
    values = {}
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith('#') or line.startswith(';') or '=' not in line:
            continue
        key, _, value = line.partition('=')
        value = value.strip()
        if len(value) >= 2 and value[0] == '"' and value[-1] == '"':
            value = value[1:-1].replace('\\"', '"').replace('\\\\', '\\')
        values[key.strip()] = value
    return values


def write_env_file(path: Path, values: dict) -> None:
    atomic_write(path, render_env_file(values), 0o600)


def merge_preserving(existing: dict, fresh: dict) -> dict:
    """Re-install merge: non-empty configured values win, empty/missing ones take fresh values."""
    merged = dict(fresh)
    for key, value in existing.items():
        if value != '':
            merged[key] = value
    return merged


def user_home(user: str) -> str:
    try:
        return pwd.getpwnam(user).pw_dir
    except KeyError as error:
        raise PreflightError(f'default OS user {user!r} does not exist') from None


def build_env_values(*, name: str, layout: Layout, bind_host: str, bind_port: int,
                     token: str, admin_password: str | None, public_origin: str | None,
                     seed: str | None, peers: str | None, default_user: str, default_home: str) -> dict:
    state_dir = layout.state(name)
    config_dir = layout.config(name)
    values = {
        'SHELL_TOKEN': token,
        'CTL_TOKEN': token,
        'GPTADMIN_HUB_HOST': bind_host,
        'GPTADMIN_HUB_PORT': str(bind_port),
        'GPTADMIN_ROOT': str(layout.current_link()),
        'GPTADMIN_CONFIG_DIR': str(config_dir),
        'GPTADMIN_ENV_FILE': str(config_dir / 'gptadmin.env'),
        'GPTADMIN_AUTH_MODE': 'reader',
        'SHELL_NAME': name,
        'SHELL_IDENTITY_DIR': str(state_dir),
        'SHELL_SPOOL_DIR': str(layout.spool_dir(name)),
        'SHELL_OUTBOX_DIR': str(layout.outbox_dir(name)),
        'SHELL_DEFAULT_USER': default_user,
        'SHELL_DEFAULT_CWD': default_home,
        'SHELL_DEFAULT_HOME': default_home,
        'SHELLMCP_SELF_REPAIR_DISABLE': '1',
        'ROOTD_AUTO_UPDATE': '0',
        'SHELL_SERVICE_NAME': layout.unit_instance(name),
        'ROOTD_SERVICE_NAME': layout.unit_instance(name),
        'GPTADMIN_NODE_IDENTITY': str(layout.identity_file(name)),
        'GPTADMIN_NODE_SEED': seed or '',
        'GPTADMIN_NODE_PEERS': peers or '',
        # GPTADMIN_AUTH_SOURCE_ID is the writer's local server_id. The reader
        # binary refuses to boot without it; the value lands after join succeeds
        # because only then is the writer identity trustworthy.
        'GPTADMIN_AUTH_SOURCE_ID': '',
    }
    if admin_password:
        values['ADMIN_PASSWORD'] = admin_password
    if public_origin:
        values['PUBLIC_ORIGIN'] = public_origin
    return values


def render_unit_template(layout: Layout) -> str:
    return f'''[Unit]
Description=GPTAdmin Node %i
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
EnvironmentFile={layout.config_root}/%i/gptadmin.env
ExecStart={layout.opt_root}/current/bin/gptadmin-node
Restart=on-failure
RestartSec=3
# SIGTERM stops active groups and preserves receipts (binary contract)
TimeoutStopSec=120
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
'''


def write_unit_template(layout: Layout) -> None:
    atomic_write(layout.unit_path, render_unit_template(layout), 0o644)


# ---------------------------------------------------------------------------
# Node HTTP: health, drain, membership
# ---------------------------------------------------------------------------


def origin_from_env(env: dict) -> str:
    host = env.get('GPTADMIN_HUB_HOST') or DEFAULT_BIND[0]
    port = env.get('GPTADMIN_HUB_PORT') or str(DEFAULT_BIND[1])
    return f'http://{host}:{port}'


def read_admin_token(layout: Layout, name: str, env: dict) -> str:
    """CTL_TOKEN first (amended contract), then owner.credential, then legacy ADMIN_PASSWORD."""
    token = env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD')
    if not token:
        credential = layout.owner_credential(name)
        if credential.is_file():
            token = credential.read_text().strip()
    if not token:
        raise PreflightError(f'no CTL_TOKEN/owner.credential found for node {name!r}')
    return token


def wait_healthy(origin: str, expected_commit: str, timeout: float = HEALTH_TIMEOUT_SECS) -> dict:
    """Poll /healthz, then require /version git_commit == expected_commit."""
    deadline = time.monotonic() + timeout
    detail = 'no response yet'
    while time.monotonic() < deadline:
        remaining = max(0.5, deadline - time.monotonic())
        try:
            status, _ = http_json('GET', origin + '/healthz', timeout=min(3.0, remaining))
            if 200 <= status < 300:
                vstatus, version = http_json('GET', origin + '/version', timeout=min(3.0, remaining))
                if 200 <= vstatus < 300 and isinstance(version, dict):
                    # An empty expectation accepts any healthy build (unknown old manifest).
                    if not expected_commit or version.get('git_commit') == expected_commit:
                        return version
                    detail = f'/version git_commit={version.get("git_commit")!r}, expected {expected_commit!r}'
                else:
                    detail = f'/version returned HTTP {vstatus}'
            else:
                detail = f'/healthz returned HTTP {status}'
        except (OSError, urllib.error.URLError) as error:
            detail = type(error).__name__
        time.sleep(min(0.5, max(0.05, deadline - time.monotonic())))
    raise RuntimeFailure(f'node at {origin} did not become healthy within {timeout:.0f}s ({detail})')


def drain_begin(origin: str, token: str) -> bool:
    """Start node drain; returns False (with a warning) for binaries without the endpoint."""
    status, payload = http_json('POST', origin + DRAIN_ENDPOINT, token=token, body={'action': 'begin'})
    if status == 404:
        print('warning: node does not implement drain (old binary); continuing without drain', file=sys.stderr)
        return False
    if not 200 <= status < 300:
        raise RuntimeFailure(f'drain begin failed: HTTP {status} {payload!r}')
    return True


def drain_wait_ready(origin: str, token: str, wait_secs: float,
                     poll_interval: float = DRAIN_POLL_INTERVAL_SECS) -> dict:
    """Poll drain status until ready_to_stop or timeout (caller must then cancel + abort)."""
    deadline = time.monotonic() + wait_secs
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        status, payload = http_json('POST', origin + DRAIN_ENDPOINT, token=token,
                                    body={'action': 'status'}, timeout=HTTP_CALL_TIMEOUT_SECS)
        if status == 404:
            print('warning: drain endpoint disappeared; continuing', file=sys.stderr)
            return {'skipped': True}
        if not 200 <= status < 300:
            raise RuntimeFailure(f'drain status failed: HTTP {status}')
        if isinstance(payload, dict) and payload.get('ready_to_stop') is True:
            return payload
        time.sleep(max(0.01, min(poll_interval, deadline - time.monotonic())))
    raise RuntimeFailure(f'drain did not reach ready_to_stop within {wait_secs:.0f}s')


def drain_cancel(origin: str, token: str) -> bool:
    try:
        status, _ = http_json('POST', origin + DRAIN_ENDPOINT, token=token, body={'action': 'cancel'})
    except (OSError, urllib.error.URLError) as error:
        print(f'warning: drain cancel unreachable ({type(error).__name__})', file=sys.stderr)
        return False
    if status == 404:
        return True  # old binary without drain: nothing to cancel
    if not 200 <= status < 300:
        print(f'warning: drain cancel returned HTTP {status}', file=sys.stderr)
        return False
    return True


def local_lan_ip(host: str, port: int) -> str | None:
    """Best-effort local source address toward host:port (UDP connect sends no packets)."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.connect((host, port))
        return sock.getsockname()[0]
    except OSError:
        return None
    finally:
        sock.close()


def fallback_lan_ip() -> str | None:
    try:
        return socket.gethostbyname(socket.gethostname())
    except OSError:
        return None


# ---------------------------------------------------------------------------
# Join / grant (mesh membership)
# ---------------------------------------------------------------------------


def build_join_body(identity: dict, name: str, endpoints: list[str], connect_to: str | None,
                    manifest: dict | None) -> dict:
    """Membership join payload. Sends node_key (dispatch contract) AND node_key_hash (writer
    contract sha256 hex); the server consumes the field its schema expects."""
    node_key = identity.get('node_key') or ''
    build = {}
    if manifest:
        build = {'git_commit': manifest.get('git_commit'), 'build_version': manifest.get('build_version')}
    body = {
        'node_id': identity.get('node_id'),
        'node_key': node_key,
        'node_key_hash': hashlib.sha256(node_key.encode()).hexdigest() if node_key else None,
        'name': name,
        'endpoints': list(endpoints),
        'connect_to': connect_to,
        'capabilities': list(NODE_CAPABILITIES),
        'build': build,
    }
    if not body['node_key_hash']:
        body.pop('node_key_hash')
    if not connect_to:
        body.pop('connect_to')
    return body


def membership_target(name: str) -> str:
    """Instance names are bare; the membership API speaks shell:<name> targets."""
    return name if name.startswith('shell:') else 'shell:' + name


def perform_join(origin: str, grant: str, identity_path: Path, name: str, endpoints: list[str],
                 connect_to: str | None, manifest: dict | None) -> dict:
    """Join via the hub; on 409 writer redirect retry once against writer_url and persist it."""
    identity = ensure_identity(identity_path)
    body = build_join_body(identity, membership_target(name), endpoints, connect_to, manifest)
    status, payload = http_json('POST', origin + JOIN_ENDPOINT, token=grant, body=body,
                                timeout=JOIN_HTTP_TIMEOUT_SECS)
    final_origin = origin
    if status == 409 and isinstance(payload, dict) and payload.get('writer_url'):
        writer = validate_origin(str(payload['writer_url']), 'writer_url redirect')
        final_origin = writer
        status, payload = http_json('POST', writer + JOIN_ENDPOINT, token=grant, body=body,
                                    timeout=JOIN_HTTP_TIMEOUT_SECS)
    if not 200 <= status < 300:
        raise RuntimeFailure(f'join at {final_origin} failed: HTTP {status} {payload!r}')
    # writer_id pins the member's control-snapshot source; the join response
    # is the only trustworthy place to learn it.
    updates = {'writer_url': final_origin}
    if isinstance(payload, dict) and payload.get('writer_id'):
        updates['writer_id'] = str(payload['writer_id'])
    merge_identity(identity_path, updates)
    return payload if isinstance(payload, dict) else {}


def manifest_summary(version_dir: Path) -> dict | None:
    manifest_path = version_dir / 'manifest.json'
    if not manifest_path.is_file():
        return None
    try:
        manifest = json.loads(manifest_path.read_text())
    except ValueError:
        return None
    if not isinstance(manifest, dict):
        return None
    return {key: manifest.get(key) for key in
            ('schema', 'git_commit', 'build_version', 'platform', 'arch')}


# ---------------------------------------------------------------------------
# Instance helpers
# ---------------------------------------------------------------------------


def configured_names(layout: Layout) -> list[str]:
    if not layout.config_root.is_dir():
        return []
    return sorted(entry.name for entry in layout.config_root.iterdir()
                  if (entry / 'gptadmin.env').is_file())


def resolve_instance_name(name: str | None, layout: Layout) -> str:
    if name:
        validate_name(name)
        if not layout.env_file(name).is_file():
            raise PreflightError(f'node {name!r} is not installed here (no env file)')
        return name
    names = configured_names(layout)
    if len(names) == 1:
        return names[0]
    if not names:
        raise UsageError('no installed node instance found; pass --name')
    raise UsageError(f'multiple installed instances found ({", ".join(names)}); pass --name')


def read_installed_env(layout: Layout, name: str) -> dict:
    env_path = layout.env_file(name)
    if not env_path.is_file():
        raise PreflightError(f'node {name!r} is not installed here (no env file at {env_path})')
    return parse_env_file(env_path)


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------


def cmd_install(args, layout: Layout, runner: Runner) -> None:
    validate_name(args.name)
    if bool(args.join_url) != bool(args.join_grant):
        raise UsageError('--join-url and --join-grant must be given together')
    if args.join_url:
        validate_origin(args.join_url, '--join-url')
    if args.public_origin:
        validate_origin(args.public_origin, '--public-origin')
    if args.seed_peers:
        try:
            json.loads(args.seed_peers)
        except ValueError as error:
            raise UsageError(f'--seed-peers must be valid JSON: {error}') from None
    require_root()
    bind_host, bind_port = parse_bind(args.bind)
    arch = host_arch()
    env_path = layout.env_file(args.name)
    existing_env = parse_env_file(env_path) if env_path.is_file() else None
    if existing_env is None and not port_is_free(bind_host, bind_port):
        raise PreflightError(f'bind address {bind_host}:{bind_port} is already in use')
    check_disk_floor(layout.opt_root, 'install root')
    check_disk_floor(layout.state_root, 'state root')

    versions_root = layout.versions_root()
    staging = versions_root / f'.staging-{os.getpid()}'
    shutil.rmtree(staging, ignore_errors=True)
    manifest = verify_and_extract(Path(args.package), arch, staging)
    verify_elf_binary(staging / 'bin' / 'gptadmin-node')

    if not confirm(args.yes, f'Install GPTAdmin node {args.name!r} (commit {manifest.get("git_commit")})? [y/N] '):
        shutil.rmtree(staging, ignore_errors=True)
        raise UsageError('install aborted; pass --yes for non-interactive installs')

    prior_target = current_target(layout)
    final = next_version_dir(versions_root, manifest['git_commit'])
    os.rename(staging, final)
    unit = layout.unit_instance(args.name)
    try:
        # The unit's ExecStart resolves through the current symlink; switch it
        # before the first start or systemd spawns against a missing binary.
        switch_current(layout, final)
        layout.spool_dir(args.name).mkdir(parents=True, exist_ok=True)
        layout.outbox_dir(args.name).mkdir(parents=True, exist_ok=True)
        identity = ensure_identity(layout.identity_file(args.name))
        token = (existing_env or {}).get('CTL_TOKEN') or secrets.token_hex(32)
        admin_password = args.admin_password or (existing_env or {}).get('ADMIN_PASSWORD')
        if existing_env:
            if args.bind is None:
                bind_host = existing_env.get('GPTADMIN_HUB_HOST') or bind_host
                bind_port = int(existing_env.get('GPTADMIN_HUB_PORT') or bind_port)
            public_origin = args.public_origin if args.public_origin is not None else existing_env.get('PUBLIC_ORIGIN')
            seed = args.join_url if args.join_url is not None else existing_env.get('GPTADMIN_NODE_SEED', '')
            peers = args.seed_peers if args.seed_peers is not None else existing_env.get('GPTADMIN_NODE_PEERS', '')
        else:
            public_origin = args.public_origin
            seed = args.join_url or ''
            peers = args.seed_peers or ''
        values = build_env_values(
            name=args.name, layout=layout, bind_host=bind_host, bind_port=bind_port, token=token,
            admin_password=admin_password, public_origin=public_origin, seed=seed, peers=peers,
            default_user='root', default_home=user_home('root'))
        if existing_env:
            values = merge_preserving(existing_env, values)
        write_env_file(env_path, values)
        write_secret_file(layout.owner_credential(args.name), token)
        write_unit_template(layout)
        systemctl(runner, 'daemon-reload')
        systemctl(runner, 'enable', unit)
        # Join precedes the first start: a reader node cannot boot without the
        # writer identity the join writes into node-identity.json. The env file
        # is rewritten after join so GPTADMIN_AUTH_SOURCE_ID lands, then the
        # service restarts to pick it up.
        joined = None
        if args.join_url:
            parsed = urllib.parse.urlsplit(args.join_url)
            join_host, join_port = parsed.hostname, parsed.port or (443 if parsed.scheme == 'https' else 80)
            lan_ip = local_lan_ip(join_host, join_port) or fallback_lan_ip()
            endpoints = [f'http://{lan_ip}:{bind_port}'] if lan_ip else []
            joined = perform_join(args.join_url, args.join_grant, layout.identity_file(args.name),
                                  args.name, endpoints, lan_ip, manifest)
            post_env = read_installed_env(layout, args.name)
            writer_id = str((joined or {}).get('writer_id') or '').strip()
            if writer_id:
                post_env['GPTADMIN_AUTH_SOURCE_ID'] = writer_id
                write_env_file(env_path, post_env)
        systemctl(runner, 'start', unit)
        origin = f'http://{bind_host}:{bind_port}'
        wait_healthy(origin, manifest['git_commit'])
    except (RuntimeFailure, PreflightError, OSError) as error:
        # Keep nothing half-installed, but never touch an existing state directory.
        systemctl(runner, 'stop', unit, check=False)
        systemctl(runner, 'disable', unit, check=False)
        if prior_target is not None and prior_target.is_dir():
            try:
                switch_current(layout, prior_target)
                systemctl(runner, 'restart', unit, check=False)
            except (OSError, RuntimeFailure):
                pass
        elif current_target(layout) is not None and \
                os.path.realpath(layout.current_link()) == str(Path(os.path.realpath(final))):
            layout.current_link().unlink(missing_ok=True)
        shutil.rmtree(final, ignore_errors=True)
        raise RuntimeFailure(f'install failed: {error}') from None

    print(json.dumps({
        'ok': True, 'command': 'install', 'name': args.name, 'unit': unit,
        'version_dir': final.name, 'git_commit': manifest.get('git_commit'),
        'build_version': manifest.get('build_version'), 'bind': f'{bind_host}:{bind_port}',
        'node_id': identity.get('node_id'), 'joined': joined is not None,
    }, sort_keys=True))


def cmd_status(args, layout: Layout, runner: Runner) -> None:
    name = resolve_instance_name(args.name, layout)
    unit = layout.unit_instance(name)
    versions = sorted(entry.name for entry in layout.versions_root().iterdir()
                      if entry.is_dir() and not entry.name.startswith('.')) \
        if layout.versions_root().is_dir() else []
    current = current_target(layout)
    active = enabled = 'unknown'
    result = systemctl(runner, 'is-active', unit, check=False)
    if result.returncode == 0:
        active = result.stdout.strip()
    result = systemctl(runner, 'is-enabled', unit, check=False)
    if result.returncode == 0:
        enabled = result.stdout.strip()

    env = {}
    if layout.env_file(name).is_file():
        env = parse_env_file(layout.env_file(name))
    origin = origin_from_env(env)
    health = {'origin': origin, 'healthz': None, 'version': None}
    try:
        status, _ = http_json('GET', origin + '/healthz')
        health['healthz'] = status
        if 200 <= status < 300:
            vstatus, version = http_json('GET', origin + '/version')
            if 200 <= vstatus < 300:
                health['version'] = version
    except (OSError, urllib.error.URLError) as error:
        health['error'] = type(error).__name__

    identity_info = None
    identity_path = layout.identity_file(name)
    if identity_path.is_file():
        try:
            identity = json.loads(identity_path.read_text())
            identity_info = {'node_id': str(identity.get('node_id', ''))[:8] + '…',
                             'created': identity.get('created'),
                             'writer_url': identity.get('writer_url')}
        except ValueError:
            identity_info = {'error': 'identity file is not valid JSON'}

    membership = None
    token = env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD')
    if token:
        try:
            status, payload = http_json('GET', origin + NODES_ENDPOINT, token=token)
            if 200 <= status < 300:
                membership = payload
            else:
                membership = {'http_status': status}
        except (OSError, urllib.error.URLError) as error:
            membership = {'error': type(error).__name__}

    print(json.dumps({
        'name': name, 'unit': unit,
        'versions': versions,
        'current': {'dir': current.name, 'path': str(current), 'manifest': manifest_summary(current)}
        if current else None,
        'service': {'active': active, 'enabled': enabled},
        'health': health,
        'identity': identity_info,
        'membership': membership,
    }, sort_keys=True))


def cmd_update(args, layout: Layout, runner: Runner) -> None:
    require_root()
    name = resolve_instance_name(args.name, layout)
    env = read_installed_env(layout, name)
    token = read_admin_token(layout, name, env)
    origin = origin_from_env(env)
    current = current_target(layout)
    if current is None:
        raise PreflightError('no current version installed; run install first')
    previous_manifest = manifest_summary(current) or {}
    arch = host_arch()
    check_disk_floor(layout.opt_root, 'install root')

    # Preflight: full digest + ELF verification into a staging dir (no install mutation yet).
    versions_root = layout.versions_root()
    staging = versions_root / f'.staging-{os.getpid()}'
    shutil.rmtree(staging, ignore_errors=True)
    manifest = verify_and_extract(Path(args.package), arch, staging)
    verify_elf_binary(staging / 'bin' / 'gptadmin-node')
    new_commit = manifest['git_commit']

    unit = layout.unit_instance(name)
    drain_active = False
    try:
        drain_active = drain_begin(origin, token)
        if drain_active:
            drain_wait_ready(origin, token, args.wait_secs)
    except RuntimeFailure:
        if drain_active:
            drain_cancel(origin, token)
        raise

    final = next_version_dir(versions_root, new_commit)
    os.rename(staging, final)
    try:
        switch_current(layout, final)
        systemctl(runner, 'restart', unit)
        wait_healthy(origin, new_commit)
    except (RuntimeFailure, OSError) as error:
        # ANY failure after the switch rolls back to the previous version dir.
        rollback_note = ''
        try:
            switch_current(layout, current)
            systemctl(runner, 'restart', unit)
            wait_healthy(origin, previous_manifest.get('git_commit') or '')
            drain_cancel(origin, token)
        except (RuntimeFailure, OSError) as rollback_error:
            rollback_note = f' ROLLBACK ALSO FAILED: {rollback_error}'
        raise RuntimeFailure(
            f'update to {final.name} failed ({error});{rollback_note} '
            f'{"rolled back to " + current.name if not rollback_note else "manual recovery required"}') from None
    drain_cancel(origin, token)
    print(json.dumps({
        'ok': True, 'command': 'update', 'name': name, 'unit': unit,
        'version_dir': final.name, 'previous_dir': current.name,
        'git_commit': new_commit, 'build_version': manifest.get('build_version'),
    }, sort_keys=True))


def cmd_rollback(args, layout: Layout, runner: Runner) -> None:
    require_root()
    name = resolve_instance_name(args.name, layout)
    env = read_installed_env(layout, name)
    token = read_admin_token(layout, name, env)
    origin = origin_from_env(env)
    current = current_target(layout)
    if current is None:
        raise PreflightError('no current version installed; nothing to roll back')
    target = select_rollback_dir(layout.versions_root(), current, args.to)
    if os.path.realpath(target) == os.path.realpath(current):
        raise UsageError(f'{target.name} is already the current version')
    target_manifest = manifest_summary(target) or {}
    unit = layout.unit_instance(name)
    switch_current(layout, target)
    try:
        systemctl(runner, 'restart', unit)
        wait_healthy(origin, target_manifest.get('git_commit') or '')
    except (RuntimeFailure, OSError) as error:
        try:
            switch_current(layout, current)
            systemctl(runner, 'restart', unit)
        except (RuntimeFailure, OSError):
            pass
        raise RuntimeFailure(f'rollback to {target.name} failed: {error}') from None
    drain_cancel(origin, token)
    print(json.dumps({
        'ok': True, 'command': 'rollback', 'name': name, 'unit': unit,
        'version_dir': target.name, 'previous_dir': current.name,
        'git_commit': target_manifest.get('git_commit'),
    }, sort_keys=True))


def cmd_uninstall(args, layout: Layout, runner: Runner) -> None:
    require_root()
    if args.purge and not args.purge_and_delete_data:
        raise UsageError('--purge requires typed confirmation flag --purge-and-delete-data')
    name = resolve_instance_name(args.name, layout)
    unit = layout.unit_instance(name)
    systemctl(runner, 'stop', unit, check=False)
    systemctl(runner, 'disable', unit, check=False)
    removed = {'versions': False, 'current_link': False, 'unit_template': False,
               'state': False, 'config': False}
    remaining = [candidate for candidate in configured_names(layout) if candidate != name]
    if not remaining:
        # Code versions are shared machine-wide; remove them only with the last instance.
        if layout.versions_root().is_dir():
            shutil.rmtree(layout.versions_root(), ignore_errors=True)
            removed['versions'] = True
        if layout.current_link().is_symlink() or layout.current_link().exists():
            layout.current_link().unlink()
            removed['current_link'] = True
        if layout.unit_path.is_file():
            layout.unit_path.unlink()
            removed['unit_template'] = True
        systemctl(runner, 'daemon-reload', check=False)
    if args.purge:
        shutil.rmtree(layout.state(name), ignore_errors=True)
        removed['state'] = True
        shutil.rmtree(layout.config(name), ignore_errors=True)
        removed['config'] = True
    print(json.dumps({'ok': True, 'command': 'uninstall', 'name': name,
                      'purged': bool(args.purge), 'removed': removed}, sort_keys=True))


def cmd_grant_issue(args, layout: Layout, runner: Runner) -> None:
    token = os.environ.get(args.admin_token_env, '').strip()
    if not token:
        raise UsageError(f'environment variable {args.admin_token_env} is empty; '
                         'export the writer owner bearer token first')
    writer_text = args.writer_url or os.environ.get('GPTADMIN_WRITER_URL', '')
    if not writer_text:
        raise UsageError('--writer-url is required (or export GPTADMIN_WRITER_URL)')
    writer = validate_origin(writer_text, '--writer-url')
    if args.ttl_mins <= 0 or args.ttl_mins > 24 * 60:
        raise UsageError('--ttl-mins must be between 1 and 1440')
    url = writer + GRANT_ENDPOINT
    body = {'name': membership_target(args.name), 'ttl_minutes': args.ttl_mins}
    try:
        status, payload = http_json('POST', url, token=token, body=body, timeout=JOIN_HTTP_TIMEOUT_SECS)
    except (OSError, urllib.error.URLError) as error:
        raise RuntimeFailure(f'writer unreachable at {url} ({type(error).__name__}); '
                             'is the writer node up?') from None
    if not 200 <= status < 300:
        raise RuntimeFailure(f'grant issue failed at {url}: HTTP {status} {payload!r}')
    print(json.dumps(payload if payload is not None else {}, sort_keys=True))


def cmd_join(args, layout: Layout, runner: Runner) -> None:
    validate_name(args.name)
    origin = validate_origin(args.url, '--url')
    env = read_installed_env(layout, args.name)
    bind_port = int(env.get('GPTADMIN_HUB_PORT') or DEFAULT_BIND[1])
    current = current_target(layout)
    manifest = manifest_summary(current) if current else None
    if args.endpoint:
        endpoints = [validate_origin(ep, '--endpoint') for ep in args.endpoint]
        connect_to = None
    else:
        parsed = urllib.parse.urlsplit(origin)
        join_port = parsed.port or (443 if parsed.scheme == 'https' else 80)
        lan_ip = local_lan_ip(parsed.hostname, join_port) or fallback_lan_ip()
        if not lan_ip:
            raise PreflightError('could not determine the local LAN address; pass --endpoint explicitly')
        endpoints = [f'http://{lan_ip}:{bind_port}']
        connect_to = lan_ip
    payload = perform_join(origin, args.grant, layout.identity_file(args.name), args.name,
                           endpoints, connect_to, manifest)
    print(json.dumps({'ok': True, 'command': 'join', 'name': args.name,
                      'membership': payload}, sort_keys=True))


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog='gptadmin node', description=__doc__)
    sub = parser.add_subparsers(dest='command', metavar='COMMAND', required=True)

    install = sub.add_parser('install', help='install a node from a portable package tarball')
    install.add_argument('--package', required=True, metavar='TAR.GZ')
    install.add_argument('--name', required=True, metavar='NAME')
    install.add_argument('--bind', default=None, metavar='HOST:PORT', help=f'default {DEFAULT_BIND_TEXT}')
    install.add_argument('--public-origin', default=None, metavar='URL')
    install.add_argument('--join-url', default=None, metavar='ORIGIN')
    install.add_argument('--join-grant', default=None, metavar='GRANT')
    install.add_argument('--seed-peers', default=None, metavar='JSON')
    install.add_argument('--admin-password', default=None,
                         help='legacy local ADMIN_PASSWORD env line; omit by default')
    install.add_argument('--yes', action='store_true', help='non-interactive install')
    install.set_defaults(func=cmd_install)

    status = sub.add_parser('status', help='report versions, service, health and membership as JSON')
    status.add_argument('--name', default=None, metavar='NAME')
    status.set_defaults(func=cmd_status)

    update = sub.add_parser('update', help='drain, switch to a new package, verify; auto-rollback on failure')
    update.add_argument('--package', required=True, metavar='TAR.GZ')
    update.add_argument('--name', default=None, metavar='NAME')
    update.add_argument('--wait-secs', type=float, default=300, metavar='SECS',
                        help='drain ready_to_stop deadline; default 300')
    update.set_defaults(func=cmd_update)

    rollback = sub.add_parser('rollback', help='switch to the previous (or named) version and restart')
    rollback.add_argument('--name', default=None, metavar='NAME')
    rollback.add_argument('--to', default=None, metavar='VERSION-DIR')
    rollback.set_defaults(func=cmd_rollback)

    uninstall = sub.add_parser('uninstall', help='stop and remove code; keep state/config unless purged')
    uninstall.add_argument('--name', required=True, metavar='NAME')
    uninstall.add_argument('--purge', action='store_true',
                           help='also delete state and config (requires --purge-and-delete-data)')
    uninstall.add_argument('--purge-and-delete-data', action='store_true',
                           help='typed confirmation for --purge')
    uninstall.set_defaults(func=cmd_uninstall)

    grant = sub.add_parser('grant', help='owner operations against the writer node')
    grant_sub = grant.add_subparsers(dest='grant_command', metavar='ACTION', required=True)
    issue = grant_sub.add_parser('issue', help='issue a single-use join grant for a new node')
    issue.add_argument('--name', required=True, metavar='NEW-NODE-NAME')
    issue.add_argument('--ttl-mins', type=int, default=30, metavar='MINUTES')
    issue.add_argument('--writer-url', default=None, metavar='ORIGIN',
                       help='writer origin; default $GPTADMIN_WRITER_URL')
    issue.add_argument('--admin-token-env', default='GPTADMIN_ADMIN_BEARER', metavar='ENV',
                       help='environment variable holding the writer owner bearer; default GPTADMIN_ADMIN_BEARER')
    issue.set_defaults(func=cmd_grant_issue)

    join = sub.add_parser('join', help='join this installed node to a mesh writer with a grant')
    join.add_argument('--name', required=True, metavar='NAME')
    join.add_argument('--url', required=True, metavar='ORIGIN')
    join.add_argument('--grant', required=True, metavar='GRANT')
    join.add_argument('--endpoint', action='append', metavar='URL',
                      help='advertised endpoint; repeatable, default auto LAN detection')
    join.set_defaults(func=cmd_join)
    return parser


def main(argv: list[str] | None = None) -> None:
    parser = build_parser()
    args = parser.parse_args(argv)
    layout = Layout()
    runner = Runner()
    try:
        args.func(args, layout, runner)
    except UsageError as error:
        fail(f'{error}', EX_USAGE)
    except PreflightError as error:
        fail(f'preflight failed: {error}', EX_PREFLIGHT)
    except RuntimeFailure as error:
        fail(f'{error}', EX_RUNTIME)
    except (OSError, urllib.error.URLError) as error:
        fail(f'runtime failure: {type(error).__name__}: {error}', EX_RUNTIME)
    except KeyboardInterrupt:
        fail('interrupted', 130)


if __name__ == '__main__':
    main()
