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
import ctypes
import errno
import hashlib
import json
import os
from pathlib import Path
import platform as platform_module
import pwd
import re
import secrets
import signal
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
SHELLMCP_PACKAGE_FILE = 'bin/shellmcp'
PROXY_ROUTE_PACKAGE_FILE = 'systemd/gptadmin-proxy-route-relay02.service'
REQUIRED_PACKAGE_FILES = ('bin/gptadmin-node', SHELLMCP_PACKAGE_FILE, PROXY_ROUTE_PACKAGE_FILE)
FIXED_PACKAGE_FILES = frozenset((*REQUIRED_PACKAGE_FILES, 'cli.py',
    'scripts/gptadmin_node_manage.py', 'scripts/gptadmin_node_probe.py',
    'scripts/gptadmin_node_autoupdate.py'))
ADMIN_UI_INDEX = 'public/admin/index.html'
ADMIN_UI_ASSET_RE = re.compile(r'public/admin/assets/[A-Za-z0-9][A-Za-z0-9._-]*\.(?:js|css)\Z')
SHELLMCP_SERVICE = 'gptadmin-shellmcp-go.service'
SHELLMCP_LEGACY_BINARY = Path('/opt/gptadmin/bin/shellmcp')
SHELLMCP_ENV_FILE = Path('/etc/gptadmin/go-shellmcp.env')
SHELLMCP_PROXY_SERVICE_DROPIN_NAME = '80-gptadmin-node-package.conf'
SHELLMCP_PROXY_SERVICE_DROPIN_TEXT = '''[Service]
ExecStart=
ExecStart=/opt/gptadmin-shellmcp/current/bin/shellmcp
'''
PROXY_ROUTE_SERVICE = 'gptadmin-proxy-route-relay02.service'
PROXY_ROUTE_UNIT_TEXT_REQUIRED = 'User=admin'
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
AUTOUPDATE_CONFIG = Path('/etc/gptadmin-node/autoupdate.json')
AUTOUPDATE_SCRIPT = Path('/usr/local/lib/gptadmin/gptadmin_node_autoupdate.py')
AUTOUPDATE_MANAGER = Path('/usr/local/lib/gptadmin/gptadmin_node_manage.py')
AUTOUPDATE_PROBE = Path('/usr/local/lib/gptadmin/gptadmin_node_probe.py')
AUTOUPDATE_SERVICE = 'gptadmin-node-autoupdate.service'
AUTOUPDATE_TIMER = 'gptadmin-node-autoupdate.timer'
AUTOUPDATE_SERVICE_TIMEOUT_SECS = 1300
AUTOUPDATE_SERVICE_PATH = Path('/etc/systemd/system') / AUTOUPDATE_SERVICE
AUTOUPDATE_TIMER_PATH = Path('/etc/systemd/system') / AUTOUPDATE_TIMER
AUTOUPDATE_SERVICE_TEXT = f'''[Unit]
Description=Verified public GPTAdmin Node package update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/bin/python3 /usr/local/lib/gptadmin/gptadmin_node_autoupdate.py
TimeoutStartSec={AUTOUPDATE_SERVICE_TIMEOUT_SECS}
MemoryHigh=256M
MemoryMax=384M
MemorySwapMax=0
CPUQuota=100%
TasksMax=64
IOWeight=10
UMask=0077
Environment=GOMAXPROCS=1
Environment=PYTHONDONTWRITEBYTECODE=1
PrivateTmp=true
ProtectHome=read-only
ReadWritePaths=/usr/local/lib/gptadmin
ProtectSystem=full
'''
AUTOUPDATE_TIMER_TEXT = '''[Unit]
Description=Periodic public GPTAdmin Node updates

[Timer]
OnBootSec=10min
OnUnitActiveSec=6h
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
'''
PRIMARY_ENV_FILE = Path('/etc/gptadmin/nodes/primary.env')
PRIMARY_BASE_ENV_FILE = Path('/etc/gptadmin/gptadmin.env')
PRIMARY_BINARY = Path('/opt/gptadmin/bin/gptadmin-node')
PRIMARY_SERVICE = 'gptadmin-hub.service'
PRIMARY_DROPIN = Path('/etc/systemd/system/gptadmin-hub.service.d/zzzzzz-node-autoupdate.conf')

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
    'GPTADMIN_NODE_PUBLIC_URL',
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


class UpdateBudget:
    """Catchable operation deadline with a separate finite rollback allowance."""
    def __init__(self, seconds):
        if seconds is not None and not 1 <= seconds <= 50:
            raise UsageError('operation deadline must be between 1 and 50 seconds')
        self.seconds = seconds
        self.cleaning = False

    def __enter__(self):
        if self.seconds is not None:
            self.previous = signal.getsignal(signal.SIGALRM)
            signal.signal(signal.SIGALRM, self.expired)
            signal.setitimer(signal.ITIMER_REAL, self.seconds)
        return self

    @staticmethod
    def expired(signum, frame):
        raise RuntimeFailure('native Node update deadline; rollback required')

    def cleanup(self):
        if self.seconds is not None and not self.cleaning:
            self.cleaning = True
            signal.setitimer(signal.ITIMER_REAL, 80)

    def __exit__(self, *_):
        if self.seconds is not None:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, self.previous)


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
                 unit_path=Path('/etc/systemd/system/gptadmin-node@.service'),
                 shellmcp_opt_root=Path('/opt/gptadmin-shellmcp'),
                 shellmcp_service_dropin=Path('/etc/systemd/system/gptadmin-shellmcp-go.service.d') / SHELLMCP_PROXY_SERVICE_DROPIN_NAME,
                 shellmcp_env_file=SHELLMCP_ENV_FILE,
                 shellmcp_proc_root=Path('/proc'),
                 proxy_route_unit_path=Path('/etc/systemd/system') / PROXY_ROUTE_SERVICE,
                 proxy_route_state_file=Path('/var/lib/gptadmin-node/proxy-route-relay02-package.json')):
        self.opt_root = Path(opt_root)
        self.state_root = Path(state_root)
        self.config_root = Path(config_root)
        self.unit_path = Path(unit_path)
        self.shellmcp_opt_root = Path(shellmcp_opt_root)
        self.shellmcp_service_dropin = Path(shellmcp_service_dropin)
        self.shellmcp_env_file = Path(shellmcp_env_file)
        self.shellmcp_proc_root = Path(shellmcp_proc_root)
        self.proxy_route_unit_path = Path(proxy_route_unit_path)
        self.proxy_route_state_file = Path(proxy_route_state_file)

    def versions_root(self) -> Path:
        return self.opt_root / 'versions'

    def current_link(self) -> Path:
        return self.opt_root / 'current'

    def shellmcp_versions_root(self) -> Path:
        return self.shellmcp_opt_root / 'versions'

    def shellmcp_current_link(self) -> Path:
        return self.shellmcp_opt_root / 'current'

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
        if name == 'primary':
            return 'gptadmin-hub.service'
        return f'gptadmin-node@{name}.service'


DEFAULT_LAYOUT = Layout()


# ---------------------------------------------------------------------------
# Small shared helpers
# ---------------------------------------------------------------------------


class Runner:
    """Process boundary for systemctl; replaced only by real callers, never faked in tests."""

    def run(self, argv, timeout=None):
        return subprocess.run(argv, capture_output=True, text=True, check=False, timeout=timeout)


def systemctl(runner: Runner, *args: str, check: bool = True, timeout: float = 45):
    try:
        result = runner.run(['systemctl', *args], timeout=timeout)
    except subprocess.TimeoutExpired:
        raise RuntimeFailure('systemctl operation exceeded its finite deadline') from None
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


def parse_build_version(value) -> int | None:
    """Normalize the Hub's JSON integer or decimal-string build version."""
    if type(value) is int:
        build = value
    elif isinstance(value, str) and re.fullmatch(r'[1-9][0-9]{0,8}', value):
        build = int(value)
    else:
        return None
    return build if 1 <= build <= 999_999_999 else None


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


def validate_node_public_url(text: str) -> str:
    value = validate_origin(text, '--node-public-url')
    if urllib.parse.urlsplit(value).scheme != 'https':
        raise UsageError('--node-public-url must use HTTPS')
    return value


def read_join_grant_stdin() -> str:
    """Read exactly one bounded single-use hex grant without putting it in argv."""
    line = sys.stdin.readline(130)
    if not line.endswith('\n') or len(line) > 129 or sys.stdin.read(1):
        raise UsageError('join grant stdin must contain exactly one bounded line')
    value = line[:-1]
    if value.endswith('\r'):
        value = value[:-1]
    if not re.fullmatch(r'[0-9a-fA-F]{64}', value):
        raise UsageError('join grant stdin must contain one 64-character hex grant')
    return value


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
    if not re.fullmatch(r'[0-9a-f]{40}', str(manifest.get('git_commit', ''))):
        raise PreflightError('manifest git_commit is not a full lowercase commit id')
    if manifest.get('source_dirty') is not False:
        raise PreflightError('refusing a package without a clean exact source manifest')
    build_version = manifest.get('build_version')
    if not isinstance(build_version, int) or isinstance(build_version, bool) or build_version < 1:
        raise PreflightError('manifest build_version is not a positive integer')
    files = manifest.get('files')
    if not isinstance(files, dict) or not files:
        raise PreflightError('manifest lists no files')
    for required in REQUIRED_PACKAGE_FILES:
        if required not in files:
            raise PreflightError(f'manifest does not ship {required}')
    names = set(files)
    if any(not isinstance(name, str) for name in names):
        raise PreflightError('manifest package path is not a string')
    ui_names = names - FIXED_PACKAGE_FILES
    if (not FIXED_PACKAGE_FILES <= names or ADMIN_UI_INDEX not in ui_names
            or len(ui_names) < 2
            or any(name != ADMIN_UI_INDEX and not ADMIN_UI_ASSET_RE.fullmatch(name)
                   for name in ui_names)):
        raise PreflightError('manifest package closure is missing required components or contains an unapproved path')
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
        if package.is_symlink() or package.stat().st_size > 128 * 1024**2:
            raise PreflightError('invalid bounded node archive')
        tar = tarfile.open(package, 'r:gz')
    except (tarfile.TarError, OSError) as error:
        raise PreflightError(f'cannot read package {package}: {type(error).__name__}') from None
    with tar:
        members = {}
        size = 0
        for member in tar:
            size += member.size
            if len(members) >= 64 or size > 256 * 1024**2 or member.name in members:
                raise PreflightError('node archive count/byte/duplicate limit')
            if member.name.startswith('/') or '..' in Path(member.name).parts:
                raise PreflightError(f'unsafe package member name: {member.name}')
            members[member.name] = member
        manifest_member = members.get('manifest.json')
        if manifest_member is None or not manifest_member.isfile():
            raise PreflightError('package has no readable manifest.json member')
        if manifest_member.size > 65536:
            raise PreflightError('node manifest exceeds byte limit')
        try:
            manifest = json.loads(tar.extractfile(manifest_member).read())
        except (ValueError, OSError) as error:
            raise PreflightError(f'manifest.json is not valid JSON: {type(error).__name__}') from None
        verify_manifest_schema(manifest, expected_arch)
        listed = manifest['files']
        if set(members) != set(listed) | {'manifest.json'}:
            raise PreflightError('node archive members do not exactly match the manifest closure')
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
        index = tar.extractfile(members[ADMIN_UI_INDEX]).read()
        for name in listed:
            if name in FIXED_PACKAGE_FILES or name == ADMIN_UI_INDEX:
                continue
            if name.rsplit('/', 1)[-1].encode() not in index:
                raise PreflightError('package contains an unreferenced admin UI asset')
        if members[PROXY_ROUTE_PACKAGE_FILE].size > 256 * 1024:
            raise PreflightError('proxy route unit exceeds its package size limit')
        try:
            unit_text = tar.extractfile(members[PROXY_ROUTE_PACKAGE_FILE]).read().decode('utf-8', 'strict')
        except UnicodeDecodeError:
            raise PreflightError('proxy route unit is not UTF-8 text') from None
        if '[Unit]' not in unit_text or '[Service]' not in unit_text or not re.search(
                r'(?m)^User=admin\s*$', unit_text):
            raise PreflightError('proxy route unit does not match the fixed admin service contract')
        # Everything verified; materialize exactly the manifest tree.
        if dest.is_symlink() or (dest.exists() and not dest.is_dir()):
            raise PreflightError('package extraction destination is not a regular directory')
        dest.mkdir(parents=True, exist_ok=True)
        (dest / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
        for name in listed:
            target = dest / name
            target.parent.mkdir(parents=True, exist_ok=True)
            with tar.extractfile(members[name]) as source, open(target, 'wb') as out:
                shutil.copyfileobj(source, out)
            os.chmod(target, 0o755 if name in ('bin/gptadmin-node', SHELLMCP_PACKAGE_FILE) else 0o644)
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
                     node_public_url: str | None, seed: str | None, peers: str | None,
                     default_user: str, default_home: str) -> dict:
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
    if node_public_url:
        values['GPTADMIN_NODE_PUBLIC_URL'] = node_public_url
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
# Measured Node fleet budget: ~10-40 MiB working set, one CPU, no swap.
MemoryHigh=128M
MemoryMax=256M
MemorySwapMax=0
CPUQuota=100%
TasksMax=128
IOWeight=10
Environment=GOMAXPROCS=1
TimeoutStartSec=30s
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


def validate_primary_current(version_dir: Path, live_version: dict) -> None:
    """Adopt only a current pointer whose package exactly matches the live primary."""
    manifest_path = version_dir / 'manifest.json'
    binary = version_dir / 'bin' / 'gptadmin-node'
    if (version_dir.is_symlink() or manifest_path.is_symlink() or not manifest_path.is_file()
            or binary.is_symlink() or not binary.is_file()):
        raise PreflightError('existing primary Node current package is incomplete or unsafe')
    try:
        manifest = json.loads(manifest_path.read_text())
    except (OSError, ValueError):
        raise PreflightError('existing primary Node current manifest is invalid') from None
    if (not isinstance(manifest, dict) or manifest.get('schema') != PACKAGE_SCHEMA
            or manifest.get('source_dirty') is not False
            or manifest.get('git_commit') != live_version.get('git_commit')
            or manifest.get('build_version') != live_version.get('build_version')
            or manifest.get('platform') != 'linux' or manifest.get('arch') != host_arch()):
        raise PreflightError('existing primary Node current package does not match the live primary')
    files = manifest.get('files')
    if not isinstance(files, dict):
        raise PreflightError('existing primary Node manifest has invalid file entries')
    entry = files.get('bin/gptadmin-node')
    data = binary.read_bytes()
    if (not isinstance(entry, dict) or entry.get('size') != len(data)
            or entry.get('sha256') != hashlib.sha256(data).hexdigest()):
        raise PreflightError('existing primary Node binary does not match its manifest')
    verify_elf_binary(binary)


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
    env = parse_env_file(PRIMARY_BASE_ENV_FILE) if name == 'primary' and PRIMARY_BASE_ENV_FILE.is_file() else {}
    env.update(parse_env_file(env_path))
    return env


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------


def cmd_install(args, layout: Layout, runner: Runner) -> None:
    validate_name(args.name)
    if getattr(args, 'join_grant_stdin', False):
        if args.join_grant is not None:
            raise UsageError('--join-grant and --join-grant-stdin are mutually exclusive')
        args.join_grant = read_join_grant_stdin()
    if bool(args.join_url) != bool(args.join_grant):
        raise UsageError('--join-url and --join-grant must be given together')
    if args.join_url:
        validate_origin(args.join_url, '--join-url')
    if args.public_origin:
        validate_origin(args.public_origin, '--public-origin')
    node_public_url = validate_node_public_url(args.node_public_url) if args.node_public_url else None
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
            if node_public_url is None:
                node_public_url = existing_env.get('GPTADMIN_NODE_PUBLIC_URL') or None
            seed = args.join_url if args.join_url is not None else existing_env.get('GPTADMIN_NODE_SEED', '')
            peers = args.seed_peers if args.seed_peers is not None else existing_env.get('GPTADMIN_NODE_PEERS', '')
        else:
            public_origin = args.public_origin
            seed = args.join_url or ''
            peers = args.seed_peers or ''
        values = build_env_values(
            name=args.name, layout=layout, bind_host=bind_host, bind_port=bind_port, token=token,
            admin_password=admin_password, public_origin=public_origin, seed=seed, peers=peers,
            node_public_url=node_public_url,
            default_user='root', default_home=user_home('root'))
        if existing_env:
            values = merge_preserving(existing_env, values)
        if args.node_public_url is not None:
            values['GPTADMIN_NODE_PUBLIC_URL'] = node_public_url
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


def verify_ui_payload(version_dir: Path) -> tuple[Path, dict]:
    """Verify the current version's packaged admin UI against its manifest."""
    manifest_path = version_dir / 'manifest.json'
    try:
        manifest = json.loads(manifest_path.read_text())
    except (OSError, ValueError):
        raise PreflightError('current Node manifest is unavailable or invalid') from None
    if manifest.get('schema') != PACKAGE_SCHEMA or not re.fullmatch(
            r'[0-9a-f]{40}', str(manifest.get('git_commit', ''))):
        raise PreflightError('current Node manifest is not a supported clean release')
    if manifest.get('source_dirty') is not False:
        raise PreflightError('admin UI publication requires a clean source package')
    files = manifest.get('files')
    if not isinstance(files, dict):
        raise PreflightError('current Node manifest has no file map')
    ui_files = {name: meta for name, meta in files.items() if name.startswith('public/admin/')}
    if 'public/admin/index.html' not in ui_files or len(ui_files) < 2:
        raise PreflightError('current Node package does not contain the complete admin UI')
    source = version_dir / 'public' / 'admin'
    if source.is_symlink() or not source.is_dir():
        raise PreflightError('current Node package admin UI directory is missing or unsafe')
    actual_files = set()
    for path in source.rglob('*'):
        if path.is_symlink():
            raise PreflightError('current Node package admin UI contains a symlink')
        if path.is_file():
            actual_files.add((Path('public/admin') / path.relative_to(source)).as_posix())
    if actual_files != set(ui_files):
        raise PreflightError('current Node package admin UI file set does not match its manifest')
    for name, meta in ui_files.items():
        if not isinstance(meta, dict) or not isinstance(meta.get('size'), int) or not re.fullmatch(
                r'[0-9a-f]{64}', str(meta.get('sha256', ''))):
            raise PreflightError('admin UI manifest has invalid file metadata')
        path = Path(name)
        if path.is_absolute() or '..' in path.parts or path.parts[:2] != ('public', 'admin'):
            raise PreflightError('admin UI manifest contains an unsafe path')
        target = version_dir / path
        if target.is_symlink() or not target.is_file():
            raise PreflightError(f'admin UI package file is missing or unsafe: {path.name}')
        try:
            size = target.stat().st_size
            digest = hashlib.sha256(target.read_bytes()).hexdigest()
        except OSError:
            raise PreflightError(f'cannot read admin UI package file: {path.name}') from None
        if size != meta.get('size') or digest != meta.get('sha256'):
            raise PreflightError(f'admin UI package file fails manifest verification: {path.name}')
    return source, manifest


def shellmcp_current_target(layout: Layout) -> Path | None:
    link = layout.shellmcp_current_link()
    if not link.is_symlink() and not link.exists():
        return None
    resolved = Path(os.path.realpath(link))
    return resolved if resolved.is_dir() else None


def atomic_symlink(target: Path, link: Path) -> None:
    link.parent.mkdir(parents=True, exist_ok=True)
    tmp = link.with_name(f'{link.name}.new-{os.getpid()}')
    if tmp.is_symlink() or tmp.exists():
        tmp.unlink()
    os.symlink(str(Path(os.path.realpath(target))), tmp)
    os.replace(tmp, link)


def shellmcp_dropin_text(layout: Layout) -> str:
    return ('[Service]\nExecStart=\nExecStart=' +
            str(layout.shellmcp_current_link() / 'bin' / 'shellmcp') + '\n')


def shellmcp_queue_mode(layout: Layout) -> bool:
    """Queue transports intentionally have no local HTTP listener."""
    env = parse_env_file(layout.shellmcp_env_file) if layout.shellmcp_env_file.is_file() else {}
    mode = env.get('SHELLMCP_MODE') or env.get('SHELL_MODE') or ''
    default_queue = '0' if mode == 'webhook' else '1'
    value = env.get('SHELLMCP_QUEUE', env.get('SHELL_QUEUE', default_queue))
    return str(value).strip().lower() in ('1', 'true', 'yes', 'on')


def shellmcp_version_url(layout: Layout) -> str | None:
    if shellmcp_queue_mode(layout):
        return None
    env = parse_env_file(layout.shellmcp_env_file) if layout.shellmcp_env_file.is_file() else {}
    host = env.get('SHELL_HOST') or env.get('SHELLMCP_HOST') or '127.0.0.1'
    port_text = env.get('SHELL_PORT') or env.get('SHELLMCP_PORT') or env.get('PORT') or '25900'
    if host in ('0.0.0.0', '::', ''):
        host = '127.0.0.1'
    if not re.fullmatch(r'[A-Za-z0-9.:-]{1,255}', host):
        raise PreflightError('ShellMCP environment has an invalid local listen address')
    try:
        port = int(port_text)
    except (ValueError, TypeError):
        raise PreflightError('ShellMCP environment has an invalid local port') from None
    if not 1 <= port <= 65535:
        raise PreflightError('ShellMCP environment has an invalid local port')
    if ':' in host and not host.startswith('['):
        host = '[' + host + ']'
    return f'http://{host}:{port}/version'


def shellmcp_live_version(layout: Layout) -> dict:
    url = shellmcp_version_url(layout)
    if url is None:
        raise PreflightError('queue-mode ShellMCP requires external Hub heartbeat readback')
    status, payload = http_json('GET', url, timeout=HTTP_CALL_TIMEOUT_SECS)
    if status != 200 or not isinstance(payload, dict) or payload.get('component') != 'shellmcp-go' \
            or payload.get('status') != 'ready':
        raise RuntimeFailure(f'ShellMCP version readback failed (HTTP {status})')
    return payload


def shellmcp_connection_refused(error: BaseException) -> bool:
    reason = getattr(error, 'reason', None)
    return isinstance(reason, OSError) and reason.errno == errno.ECONNREFUSED


def shellmcp_legacy_binary_digest(binary: Path | None = None) -> str:
    binary = Path(binary) if binary is not None else SHELLMCP_LEGACY_BINARY
    if binary.is_symlink() or not binary.is_file() or not os.access(binary, os.X_OK):
        raise PreflightError('legacy ShellMCP binary is not a regular executable')
    return digest_file(binary)


def verify_shellmcp_legacy_restore(layout: Layout, runner: Runner, expected_digest: str) -> dict:
    managed, contract = verify_shellmcp_service_contract(layout, runner)
    link = layout.shellmcp_current_link()
    if managed or link.exists() or link.is_symlink():
        raise RuntimeFailure('legacy ShellMCP package state was not restored')
    if shellmcp_legacy_binary_digest() != expected_digest:
        raise RuntimeFailure('legacy ShellMCP executable changed during package rollback')
    if shellmcp_queue_mode(layout):
        shellmcp_process_snapshot(layout, runner, SHELLMCP_LEGACY_BINARY, expected_digest)
    return contract


def shellmcp_systemctl_property(runner: Runner, unit: str, key: str) -> str:
    try:
        result = runner.run(['systemctl', 'show', unit, '-p', key, '--value'], timeout=10)
    except subprocess.TimeoutExpired:
        raise RuntimeFailure(f'systemd readback timed out for {unit} {key}') from None
    if result.returncode:
        raise PreflightError(f'cannot read active systemd property {key} for {unit}')
    return result.stdout.strip()


def shellmcp_environment_files_match(value: str, expected: Path) -> bool:
    body = value.strip()
    if body.startswith('{') and body.endswith('}'):
        body = body[1:-1].strip()
    entries = []
    offset = 0
    pattern = re.compile(r'([^\s{}]+)\s+\(ignore_errors=(yes|no)\)')
    while offset < len(body):
        while offset < len(body) and body[offset].isspace():
            offset += 1
        if offset == len(body):
            break
        match = pattern.match(body, offset)
        if not match:
            return False
        entries.append((match.group(1), match.group(2)))
        offset = match.end()
    return entries == [(str(expected), 'no')]


def shellmcp_execstart_matches(value: str, expected: str) -> bool:
    text = value.strip()
    if not (text.startswith('{') and text.endswith('}')):
        return False
    fields = [field.strip() for field in text[1:-1].split(';') if field.strip()]
    if fields[:3] != [f'path={expected}', f'argv[]={expected}', 'ignore_errors=no']:
        return False
    seen = set()
    metadata_patterns = {
        'start_time': r'.+',
        'stop_time': r'.+',
        'pid': r'\d+',
        'code': r'\((?:null|exited|killed|dumped)\)',
        'status': r'[^;]+',
    }
    for field in fields[3:]:
        key, separator, raw_value = field.partition('=')
        if not separator or key not in metadata_patterns or key in seen:
            return False
        if not re.fullmatch(metadata_patterns[key], raw_value):
            return False
        seen.add(key)
    return True


def verify_shellmcp_service_contract(layout: Layout, runner: Runner) -> tuple[bool, dict]:
    """Require the known active service and preserve its env/user/argument contract."""
    active = systemctl(runner, 'is-active', SHELLMCP_SERVICE, check=False)
    if active.returncode or active.stdout.strip() != 'active':
        raise PreflightError(f'{SHELLMCP_SERVICE} must already be active')
    user = shellmcp_systemctl_property(runner, SHELLMCP_SERVICE, 'User')
    env_files = shellmcp_systemctl_property(runner, SHELLMCP_SERVICE, 'EnvironmentFiles')
    exec_start = shellmcp_systemctl_property(runner, SHELLMCP_SERVICE, 'ExecStart')
    if user != 'root' or not shellmcp_environment_files_match(env_files, layout.shellmcp_env_file):
        raise PreflightError('active ShellMCP unit user or EnvironmentFile differs from the supported contract')
    dropin = layout.shellmcp_service_dropin
    if (dropin.exists() or dropin.is_symlink()) and (dropin.is_symlink() or not dropin.is_file()):
        raise PreflightError('ShellMCP package drop-in is not a regular file')
    managed = dropin.is_file()
    if managed and dropin.read_text() != shellmcp_dropin_text(layout):
        raise PreflightError('ShellMCP package drop-in exists with unexpected contents')
    expected = (str(layout.shellmcp_current_link() / 'bin' / 'shellmcp') if managed
                else str(SHELLMCP_LEGACY_BINARY))
    if not shellmcp_execstart_matches(exec_start, expected):
        raise PreflightError('active ShellMCP ExecStart differs from the supported no-argument binary contract')
    return managed, {'active': active.stdout.strip(), 'user': user,
                     'environment_file': str(layout.shellmcp_env_file), 'exec_start': expected}


def wait_shellmcp_version(layout: Layout, commit: str, build_version: int) -> dict:
    deadline = time.monotonic() + HEALTH_TIMEOUT_SECS
    last_error = 'no readback yet'
    while time.monotonic() < deadline:
        try:
            version = shellmcp_live_version(layout)
            if version.get('git_commit') == commit and version.get('build_version') == build_version:
                return version
            last_error = 'running service reports a different package version'
        except (OSError, urllib.error.URLError, RuntimeFailure) as error:
            last_error = str(error)
        time.sleep(0.5)
    raise RuntimeFailure(f'ShellMCP did not report the expected package version: {last_error}')


def shellmcp_version_manifest(version_dir: Path) -> dict:
    try:
        if version_dir.is_symlink() or not version_dir.is_dir():
            raise ValueError('unsafe ShellMCP version directory')
        manifest = json.loads((version_dir / 'manifest.json').read_text())
        if not isinstance(manifest, dict):
            raise ValueError('ShellMCP manifest is not an object')
        binary = version_dir / 'bin' / 'shellmcp'
        expected_sha = manifest.get('sha256')
        expected_size = manifest.get('size')
        if (manifest.get('schema') != PACKAGE_SCHEMA
                or not re.fullmatch(r'[0-9a-f]{40}', str(manifest.get('git_commit', '')))
                or type(manifest.get('build_version')) is not int or manifest['build_version'] < 1
                or not isinstance(expected_sha, str) or not re.fullmatch(r'[0-9a-f]{64}', expected_sha)
                or type(expected_size) is not int or expected_size < 1
                or not binary.is_file() or binary.is_symlink()
                or binary.stat().st_size != expected_size
                or digest_file(binary) != expected_sha):
            raise ValueError('invalid ShellMCP version binding')
    except (OSError, ValueError, TypeError):
        raise PreflightError(f'ShellMCP version directory is not manifest-verified: {version_dir.name}') from None
    verify_elf_binary(binary)
    return manifest


def digest_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def shellmcp_process_snapshot(layout: Layout, runner: Runner, expected_binary: Path,
                              expected_sha256: str) -> dict:
    """Prove the active service PID executes the exact package-owned binary."""
    active = systemctl(runner, 'is-active', SHELLMCP_SERVICE, check=False)
    if active.returncode or active.stdout.strip() != 'active':
        raise RuntimeFailure('ShellMCP service is not active')
    first_pid_text = shellmcp_systemctl_property(runner, SHELLMCP_SERVICE, 'MainPID')
    if not first_pid_text.isdigit() or int(first_pid_text) <= 0:
        raise RuntimeFailure('ShellMCP service has no active MainPID')
    pid = int(first_pid_text)
    process_exe = layout.shellmcp_proc_root / str(pid) / 'exe'
    try:
        link_target = os.readlink(process_exe)
        if link_target.endswith(' (deleted)'):
            raise RuntimeFailure('ShellMCP process executable is deleted')
        actual_path = process_exe.resolve(strict=True)
        expected_path = Path(expected_binary).resolve(strict=True)
        actual_sha256 = digest_file(process_exe)
    except OSError:
        raise RuntimeFailure('cannot read the active ShellMCP process executable') from None
    if actual_path != expected_path or actual_sha256 != expected_sha256:
        raise RuntimeFailure('active ShellMCP process differs from the verified package binary')
    second_pid_text = shellmcp_systemctl_property(runner, SHELLMCP_SERVICE, 'MainPID')
    if second_pid_text != first_pid_text:
        raise RuntimeFailure('ShellMCP MainPID changed during package readback')
    active_after = systemctl(runner, 'is-active', SHELLMCP_SERVICE, check=False)
    if active_after.returncode or active_after.stdout.strip() != 'active':
        raise RuntimeFailure('ShellMCP service stopped during package readback')
    return {'pid': pid, 'sha256': actual_sha256}


def wait_shellmcp_process(layout: Layout, runner: Runner, expected_binary: Path,
                          expected_sha256: str) -> dict:
    deadline = time.monotonic() + HEALTH_TIMEOUT_SECS
    while time.monotonic() < deadline:
        try:
            verify_shellmcp_service_contract(layout, runner)
            return shellmcp_process_snapshot(layout, runner, expected_binary, expected_sha256)
        except (RuntimeFailure, PreflightError, OSError):
            time.sleep(min(0.5, max(0.05, deadline - time.monotonic())))
    raise RuntimeFailure('ShellMCP queue-mode process did not match the verified package before the deadline')


def cmd_shellmcp_status(args, layout: Layout, runner: Runner) -> None:
    managed, contract = verify_shellmcp_service_contract(layout, runner)
    target = shellmcp_current_target(layout)
    if shellmcp_queue_mode(layout):
        package = None
        if managed:
            if target is None or target.parent != layout.shellmcp_versions_root():
                raise PreflightError('managed ShellMCP service has no package-owned current version')
            package = shellmcp_version_manifest(target)
            expected_binary = target / 'bin' / 'shellmcp'
            expected_sha256 = package['sha256']
        else:
            if target is not None or layout.shellmcp_current_link().exists() or layout.shellmcp_current_link().is_symlink():
                raise PreflightError('unmanaged ShellMCP service has conflicting package state')
            expected_binary = Path(contract['exec_start'])
            expected_sha256 = shellmcp_legacy_binary_digest(expected_binary)
        process = shellmcp_process_snapshot(layout, runner, expected_binary, expected_sha256)
        print(json.dumps({'ok': True, 'command': 'shellmcp status', 'managed': managed,
                          'current': target.name if target else None, 'package': package, 'service': contract,
                          'version': None, 'runtime_status': 'pending_external_hub_heartbeat',
                          'hub_heartbeat_required': True, 'process': process}, sort_keys=True))
        return
    version = shellmcp_live_version(layout)
    package = None
    if managed:
        if target is None or target.parent != layout.shellmcp_versions_root():
            raise PreflightError('managed ShellMCP service has no package-owned current version')
        package = shellmcp_version_manifest(target)
        if (version.get('git_commit') != package['git_commit']
                or version.get('build_version') != package['build_version']):
            raise RuntimeFailure('ShellMCP live version differs from its manifest-verified current package')
    print(json.dumps({'ok': True, 'command': 'shellmcp status', 'managed': managed,
                      'current': target.name if target else None, 'package': package, 'service': contract,
                      'version': version}, sort_keys=True))


def cmd_shellmcp_rollback(args, layout: Layout, runner: Runner) -> None:
    require_root()
    managed, contract = verify_shellmcp_service_contract(layout, runner)
    if not managed:
        raise PreflightError('ShellMCP package rollback requires a Node package managed installation')
    current = shellmcp_current_target(layout)
    if current is None:
        raise PreflightError('no current ShellMCP package is installed')
    if current.parent != layout.shellmcp_versions_root():
        raise PreflightError('ShellMCP current link does not point inside its version store')
    previous = shellmcp_version_manifest(current)
    target = select_rollback_dir(layout.shellmcp_versions_root(), current, args.to)
    target_manifest = shellmcp_version_manifest(target)
    queue_mode = shellmcp_queue_mode(layout)
    if queue_mode:
        live_before = None
        shellmcp_process_snapshot(layout, runner, current / 'bin' / 'shellmcp', previous['sha256'])
    else:
        try:
            live_before = shellmcp_live_version(layout)
        except (OSError, urllib.error.URLError, RuntimeFailure) as error:
            raise PreflightError(f'cannot verify current ShellMCP service before rollback: {error}') from None
    try:
        atomic_symlink(target, layout.shellmcp_current_link())
        systemctl(runner, 'restart', SHELLMCP_SERVICE)
        if queue_mode:
            live = None
            process = wait_shellmcp_process(layout, runner, target / 'bin' / 'shellmcp', target_manifest['sha256'])
        else:
            live = wait_shellmcp_version(layout, target_manifest['git_commit'], target_manifest['build_version'])
    except (RuntimeFailure, PreflightError, OSError, urllib.error.URLError) as error:
        try:
            atomic_symlink(current, layout.shellmcp_current_link())
            systemctl(runner, 'restart', SHELLMCP_SERVICE)
            if queue_mode:
                wait_shellmcp_process(layout, runner, current / 'bin' / 'shellmcp', previous['sha256'])
            else:
                restored = shellmcp_live_version(layout)
                if (restored.get('git_commit') != live_before.get('git_commit')
                        or restored.get('build_version') != live_before.get('build_version')):
                    raise RuntimeFailure('rollback recovery readback differs from the pre-rollback ShellMCP version')
        except (RuntimeFailure, PreflightError, OSError, urllib.error.URLError) as restore:
            raise RuntimeFailure(f'ShellMCP rollback failed ({error}); service recovery failed ({restore})') from None
        raise RuntimeFailure(f'ShellMCP rollback failed ({error}); current version restored and read back') from None
    result = {'ok': True, 'command': 'shellmcp rollback', 'version_dir': target.name,
              'previous_dir': current.name, 'previous_version': previous,
              'version': live, 'service': contract}
    if queue_mode:
        result.update({'runtime_status': 'pending_external_hub_heartbeat',
                       'hub_heartbeat_required': True, 'process': process})
    print(json.dumps(result, sort_keys=True))


def cmd_shellmcp_update(args, layout: Layout, runner: Runner) -> None:
    require_root()
    check_disk_floor(layout.shellmcp_opt_root, 'ShellMCP install root')
    managed, contract = verify_shellmcp_service_contract(layout, runner)
    previous_target = shellmcp_current_target(layout)
    queue_mode = shellmcp_queue_mode(layout)
    legacy_digest = None
    current_link = layout.shellmcp_current_link()
    if (not managed and previous_target is None and not current_link.exists()
            and not current_link.is_symlink() and contract['exec_start'] == str(SHELLMCP_LEGACY_BINARY)):
        try:
            legacy_digest = shellmcp_legacy_binary_digest()
        except PreflightError:
            legacy_digest = None
    previous_manifest = None
    if queue_mode:
        before = None
        if managed:
            if previous_target is None or previous_target.parent != layout.shellmcp_versions_root():
                raise PreflightError('managed ShellMCP service has no package-owned current version')
            previous_manifest = shellmcp_version_manifest(previous_target)
            shellmcp_process_snapshot(layout, runner, previous_target / 'bin' / 'shellmcp', previous_manifest['sha256'])
        elif legacy_digest is not None:
            shellmcp_process_snapshot(layout, runner, Path(contract['exec_start']), legacy_digest)
        else:
            raise PreflightError('queue-mode ShellMCP legacy process cannot be matched to its binary')
    else:
        try:
            before = shellmcp_live_version(layout)
        except (OSError, urllib.error.URLError, RuntimeFailure) as error:
            if legacy_digest is not None and shellmcp_connection_refused(error):
                before = None
            else:
                raise PreflightError(f'cannot verify current ShellMCP service before update: {error}') from None
        if managed:
            if previous_target is None or previous_target.parent != layout.shellmcp_versions_root():
                raise PreflightError('managed ShellMCP service has no package-owned current version')
            previous_manifest = shellmcp_version_manifest(previous_target)
            if (before.get('git_commit') != previous_manifest['git_commit']
                    or before.get('build_version') != previous_manifest['build_version']):
                raise PreflightError('ShellMCP live version differs from its current package; refusing update')
    versions_root = layout.shellmcp_versions_root()
    staging = versions_root / f'.staging-{os.getpid()}'
    if staging.is_symlink():
        staging.unlink()
    else:
        shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True, mode=0o700)
    final = None
    try:
        manifest = verify_and_extract(Path(args.package), host_arch(), staging)
        binary = staging / SHELLMCP_PACKAGE_FILE
        verify_elf_binary(binary)
        final = next_version_dir(versions_root, manifest['git_commit'])
        final.mkdir(parents=True)
        (final / 'bin').mkdir()
        shutil.copyfile(binary, final / 'bin' / 'shellmcp')
        os.chmod(final / 'bin' / 'shellmcp', 0o755)
        (final / 'manifest.json').write_text(json.dumps({
            'schema': PACKAGE_SCHEMA, 'git_commit': manifest['git_commit'],
            'build_version': manifest['build_version'], 'source_dirty': False,
            'sha256': manifest['files'][SHELLMCP_PACKAGE_FILE]['sha256'],
            'size': manifest['files'][SHELLMCP_PACKAGE_FILE]['size'],
        }, sort_keys=True, indent=2) + '\n')
        shellmcp_version_manifest(final)
    except (RuntimeFailure, PreflightError, OSError):
        if final is not None:
            shutil.rmtree(final, ignore_errors=True)
        raise
    finally:
        shutil.rmtree(staging, ignore_errors=True)

    current_link = layout.shellmcp_current_link()
    dropin = layout.shellmcp_service_dropin
    previous_dropin = dropin.read_bytes() if dropin.is_file() else None
    if previous_dropin is not None and not managed:
        raise PreflightError('refusing an unmanaged ShellMCP service drop-in')
    try:
        atomic_symlink(final, current_link)
        atomic_write(dropin, shellmcp_dropin_text(layout), 0o644)
        systemctl(runner, 'daemon-reload')
        systemctl(runner, 'restart', SHELLMCP_SERVICE)
        if queue_mode:
            version = None
            process = wait_shellmcp_process(layout, runner, final / 'bin' / 'shellmcp', manifest['files'][SHELLMCP_PACKAGE_FILE]['sha256'])
        else:
            version = wait_shellmcp_version(layout, manifest['git_commit'], manifest['build_version'])
    except (RuntimeFailure, PreflightError, OSError, urllib.error.URLError) as error:
        rollback_error = None
        try:
            if previous_target is None:
                current_link.unlink(missing_ok=True)
            else:
                atomic_symlink(previous_target, current_link)
            if previous_dropin is None:
                dropin.unlink(missing_ok=True)
            else:
                atomic_write(dropin, previous_dropin.decode('utf-8'), 0o644)
            systemctl(runner, 'daemon-reload')
            systemctl(runner, 'restart', SHELLMCP_SERVICE)
            if queue_mode and previous_target is not None and previous_manifest is not None:
                wait_shellmcp_process(layout, runner, previous_target / 'bin' / 'shellmcp', previous_manifest['sha256'])
            elif before is None:
                verify_shellmcp_legacy_restore(layout, runner, legacy_digest)
            else:
                restored = shellmcp_live_version(layout)
                if (restored.get('git_commit') != before.get('git_commit')
                        or restored.get('build_version') != before.get('build_version')):
                    raise RuntimeFailure('rollback readback differs from the pre-update ShellMCP version')
        except (RuntimeFailure, PreflightError, OSError, urllib.error.URLError) as restore:
            rollback_error = str(restore)
        restored_legacy = before is None and previous_target is None
        detail = (f' ShellMCP rollback failed: {rollback_error}' if rollback_error else
                  ' legacy service restored and binary/unit verified' if restored_legacy else
                  ' previous version restored and read back')
        raise RuntimeFailure(f'ShellMCP update failed ({error});{detail}') from None
    result = {'ok': True, 'command': 'shellmcp update', 'previous': before, 'version': version,
              'bootstrapped_from_legacy': previous_target is None,
              'version_dir': final.name, 'service': contract}
    if queue_mode:
        result.update({'runtime_status': 'pending_external_hub_heartbeat',
                       'hub_heartbeat_required': True, 'process': process})
    print(json.dumps(result, sort_keys=True))


def cmd_proxy_route_status(args, layout: Layout, runner: Runner) -> None:
    path = layout.proxy_route_unit_path
    state = None
    if layout.proxy_route_state_file.is_file():
        try:
            state = json.loads(layout.proxy_route_state_file.read_text())
        except (OSError, ValueError):
            raise RuntimeFailure('proxy route package ownership state is unreadable') from None
    active = systemctl(runner, 'is-active', PROXY_ROUTE_SERVICE, check=False)
    enabled = systemctl(runner, 'is-enabled', PROXY_ROUTE_SERVICE, check=False)
    print(json.dumps({'ok': True, 'command': 'proxy-route status',
                      'installed': path.is_file() and not path.is_symlink(),
                      'managed': isinstance(state, dict) and state.get('schema') == 'gptadmin.proxy-route-package/v1',
                      'active': active.stdout.strip() if active.returncode == 0 else 'inactive',
                      'enabled': enabled.stdout.strip() if enabled.returncode == 0 else 'disabled',
                      'path': str(path)}, sort_keys=True))


def cmd_proxy_route_install(args, layout: Layout, runner: Runner) -> None:
    require_root()
    hub = systemctl(runner, 'is-active', PRIMARY_SERVICE, check=False)
    if hub.returncode or hub.stdout.strip() != 'active':
        raise PreflightError('fixed proxy route unit may only be installed on the active Hub host')
    staging = layout.opt_root / f'.proxy-route-staging-{os.getpid()}'
    if staging.is_symlink():
        staging.unlink()
    else:
        shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True, mode=0o700)
    try:
        manifest = verify_and_extract(Path(args.package), host_arch(), staging)
    except (RuntimeFailure, PreflightError, OSError):
        shutil.rmtree(staging, ignore_errors=True)
        raise
    source = staging / PROXY_ROUTE_PACKAGE_FILE
    unit_text = source.read_text()
    target = layout.proxy_route_unit_path
    state_path = layout.proxy_route_state_file
    new_digest = manifest['files'][PROXY_ROUTE_PACKAGE_FILE]['sha256']
    old_text = target.read_text() if target.is_file() and not target.is_symlink() else None
    old_state = None
    if state_path.is_file():
        try:
            old_state = json.loads(state_path.read_text())
        except (OSError, ValueError):
            raise PreflightError('proxy route package ownership state is unreadable; refusing overwrite') from None
    if target.exists() or target.is_symlink():
        if target.is_symlink() or old_text is None:
            raise PreflightError('existing proxy route unit is not a regular file; refusing overwrite')
        if old_text != unit_text and not (isinstance(old_state, dict)
                and old_state.get('schema') == 'gptadmin.proxy-route-package/v1'
                and old_state.get('sha256') == hashlib.sha256(old_text.encode()).hexdigest()):
            raise PreflightError('existing proxy route unit is not owned by the Node package manager')
    try:
        atomic_write(target, unit_text, 0o644)
        atomic_write(state_path, json.dumps({'schema': 'gptadmin.proxy-route-package/v1',
                    'git_commit': manifest['git_commit'], 'sha256': new_digest}, sort_keys=True) + '\n', 0o600)
        systemctl(runner, 'daemon-reload')
        loaded = runner.run(['systemctl', 'show', PROXY_ROUTE_SERVICE,
                             '-p', 'FragmentPath', '--value'], timeout=10)
        if loaded.returncode or Path(loaded.stdout.strip()).resolve() != target.resolve():
            raise RuntimeFailure('systemd did not read back the installed fixed proxy unit path')
    except (RuntimeFailure, OSError) as error:
        if old_text is None:
            target.unlink(missing_ok=True)
        else:
            atomic_write(target, old_text, 0o644)
        if old_state is None:
            state_path.unlink(missing_ok=True)
        else:
            atomic_write(state_path, json.dumps(old_state, sort_keys=True) + '\n', 0o600)
        systemctl(runner, 'daemon-reload', check=False)
        raise RuntimeFailure(f'proxy route unit install rolled back ({error})') from None
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    print(json.dumps({'ok': True, 'command': 'proxy-route install', 'installed': True,
                      'enabled_or_started': False, 'path': str(target),
                      'git_commit': manifest['git_commit'], 'sha256': new_digest}, sort_keys=True))


def publish_admin_ui(source: Path, public_root: Path, commit: str) -> dict:
    """Atomically publish verified static assets while preserving the old UI."""
    public_root = Path(public_root)
    if not public_root.is_dir() or public_root.is_symlink():
        raise PreflightError('configured public directory is missing or unsafe')
    destination = public_root / 'admin'
    if destination.is_symlink() or (destination.exists() and not destination.is_dir()):
        raise PreflightError('existing admin UI destination is not a regular directory')
    index_hash = hashlib.sha256((source / 'index.html').read_bytes()).hexdigest()
    staging = public_root / f'.admin-ui-staging-{commit}-{os.getpid()}'
    if staging.exists():
        raise PreflightError('admin UI staging path already exists')
    switched = False
    previous = None
    try:
        shutil.copytree(source, staging)
        if destination.is_dir():
            retain_admin_ui_assets(destination / 'assets', staging / 'assets')
        for root, dirs, files in os.walk(staging):
            os.chmod(root, 0o755)
            for directory in dirs:
                os.chmod(Path(root) / directory, 0o755)
            for filename in files:
                path = Path(root) / filename
                if path.is_symlink() or not path.is_file():
                    raise PreflightError('admin UI source contains an unsafe entry')
                os.chmod(path, 0o644)
        if destination.exists():
            rename_exchange(staging, destination)
            switched = True
            previous = staging
        else:
            os.replace(staging, destination)
            switched = True
    except Exception:
        if staging.exists() and not switched:
            shutil.rmtree(staging, ignore_errors=True)
        raise
    return {'path': str(destination), 'previous': str(previous) if previous is not None else None,
            'git_commit': commit, 'index_sha256': index_hash}


def retain_admin_ui_assets(previous_assets: Path, candidate_assets: Path) -> None:
    """Keep old fingerprinted URLs resolvable after an index/asset tree exchange."""
    if not previous_assets.exists():
        return
    if previous_assets.is_symlink() or not previous_assets.is_dir():
        raise PreflightError('existing admin UI assets directory is unsafe')
    if candidate_assets.is_symlink() or not candidate_assets.is_dir():
        raise PreflightError('candidate admin UI assets directory is unsafe')
    for source in sorted(previous_assets.rglob('*')):
        if source.is_symlink():
            raise PreflightError('existing admin UI assets contain an unsafe entry')
        if not source.is_file():
            continue
        relative = source.relative_to(previous_assets)
        target = candidate_assets / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.exists():
            try:
                same = hashlib.sha256(source.read_bytes()).digest() == hashlib.sha256(target.read_bytes()).digest()
            except OSError:
                raise PreflightError(f'cannot compare retained admin UI asset: {relative.as_posix()}') from None
            if not same:
                raise PreflightError(f'admin UI asset name has conflicting contents: {relative.as_posix()}')
            continue
        try:
            shutil.copyfile(source, target)
        except OSError:
            raise PreflightError(f'cannot retain previous admin UI asset: {relative.as_posix()}') from None


def restore_admin_ui(receipt: dict, commit: str) -> None:
    """Exchange the previous static directory back without removing /admin."""
    destination = Path(receipt['path'])
    previous = Path(receipt['previous']) if receipt.get('previous') else None
    if previous is not None:
        rename_exchange(previous, destination)
        return
    empty = destination.with_name(f'.admin-ui-empty-{commit}-{os.getpid()}')
    if empty.exists():
        raise PreflightError('admin UI rollback placeholder already exists')
    empty.mkdir(mode=0o755)
    try:
        rename_exchange(empty, destination)
    except Exception:
        empty.rmdir()
        raise


def rename_exchange(left: Path, right: Path) -> None:
    """Linux atomic path exchange. Fail closed when RENAME_EXCHANGE is absent."""
    if not sys.platform.startswith('linux'):
        raise PreflightError('atomic admin UI replacement requires Linux renameat2')
    try:
        renameat2 = ctypes.CDLL(None, use_errno=True).renameat2
    except AttributeError:
        raise PreflightError('atomic admin UI replacement requires renameat2') from None
    renameat2.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    renameat2.restype = ctypes.c_int
    result = renameat2(-100, os.fsencode(left), -100, os.fsencode(right), 2)
    if result != 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error), f'{left} <-> {right}')


def cmd_ui_publish(args, layout: Layout, runner: Runner) -> None:
    require_root()
    name = resolve_instance_name(args.name, layout)
    if name != 'primary':
        raise UsageError('admin UI publication is supported only for the primary node')
    current = current_target(layout)
    if current is None:
        raise PreflightError('no current Node package is installed')
    source, manifest = verify_ui_payload(current)
    env = read_installed_env(layout, name)
    public_root = Path(env.get('GPTADMIN_PUBLIC_DIR') or (Path(env.get('GPTADMIN_ROOT') or '.') / 'public'))
    receipt = publish_admin_ui(source, public_root, manifest['git_commit'])
    receipt['asset_count'] = sum(1 for _ in source.rglob('*') if _.is_file())
    print(json.dumps({'ok': True, 'command': 'ui-publish', **receipt}, sort_keys=True))


def cmd_update(args, layout: Layout, runner: Runner) -> None:
    with UpdateBudget(getattr(args, 'operation_deadline_secs', None)) as budget:
        return update_with_budget(args, layout, runner, budget)


def update_with_budget(args, layout: Layout, runner: Runner, budget: UpdateBudget) -> None:
    require_root()
    name = resolve_instance_name(args.name, layout)
    env = read_installed_env(layout, name)
    requested_node_url = getattr(args, 'node_public_url', None)
    if requested_node_url is not None:
        requested_node_url = validate_node_public_url(requested_node_url)
        if not getattr(args, 'require_drain', False):
            raise UsageError('--node-public-url updates require --require-drain')
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
    has_admin_ui = any(name.startswith('public/admin/') for name in manifest.get('files', {}))
    if has_admin_ui:
        verify_ui_payload(staging)

    # Existing primary installs also need lifecycle migration. Reload before
    # drain/restart so stopping the old process cannot invoke legacy HA hooks.
    if name == 'primary':
        prepare_primary_dropin()
        systemctl(runner, 'daemon-reload')

    unit = layout.unit_instance(name)
    env_path = layout.env_file(name)
    previous_env_text = env_path.read_text() if requested_node_url is not None else None
    drain_active = False
    drain_attempted = False
    try:
        drain_attempted = True
        drain_active = drain_begin(origin, token)
        if not drain_active and getattr(args, 'require_drain', False):
            raise RuntimeFailure('automatic update requires the supported drain endpoint')
        if drain_active:
            disposition = drain_wait_ready(origin, token, args.wait_secs)
            if disposition.get('skipped') and getattr(args, 'require_drain', False):
                raise RuntimeFailure('drain readiness unavailable; automatic update refused')
    except RuntimeFailure:
        budget.cleanup()
        # begin may have been accepted before its response/deadline was lost.
        if drain_attempted and not drain_cancel(origin, token):
            raise RuntimeFailure('drain admission failed and cancel is unverified; manual recovery required') from None
        raise

    final = None
    env_updated = False
    ui_receipt = None
    try:
        final = next_version_dir(versions_root, new_commit)
        os.rename(staging, final)
        if requested_node_url is not None:
            updated_env = dict(env)
            updated_env['GPTADMIN_NODE_PUBLIC_URL'] = requested_node_url
            write_env_file(env_path, updated_env)
            env_updated = True
        switch_current(layout, final)
        systemctl(runner, 'restart', unit)
        wait_healthy(origin, new_commit)
        if name == 'primary' and has_admin_ui:
            ui_source, ui_manifest = verify_ui_payload(final)
            public_root = Path(env.get('GPTADMIN_PUBLIC_DIR') or (Path(env.get('GPTADMIN_ROOT') or '.') / 'public'))
            ui_receipt = publish_admin_ui(ui_source, public_root, ui_manifest['git_commit'])
    except (RuntimeFailure, PreflightError, OSError) as error:
        budget.cleanup()
        # ANY failure after the switch rolls back to the previous version dir.
        rollback_note = ''
        try:
            if ui_receipt is not None:
                restore_admin_ui(ui_receipt, new_commit)
            if env_updated and previous_env_text is not None:
                atomic_write(env_path, previous_env_text, 0o600)
            switch_current(layout, current)
            systemctl(runner, 'restart', unit)
            wait_healthy(origin, previous_manifest.get('git_commit') or '')
            if not drain_cancel(origin, token):
                raise RuntimeFailure('rollback drain cancel unverified')
        except (RuntimeFailure, PreflightError, OSError) as rollback_error:
            rollback_note = f' ROLLBACK ALSO FAILED: {rollback_error}'
        raise RuntimeFailure(
            f'update to {final.name if final else new_commit} failed ({error});{rollback_note} '
            f'{"rolled back to " + current.name if not rollback_note else "manual recovery required"}') from None
    budget.cleanup()
    if not drain_cancel(origin, token):
        raise RuntimeFailure('new version healthy but drain cancel unverified; manual recovery required')
    print(json.dumps({
        'ok': True, 'command': 'update', 'name': name, 'unit': unit,
        'version_dir': final.name, 'previous_dir': current.name,
        'git_commit': new_commit, 'build_version': manifest.get('build_version'),
        'node_public_url': requested_node_url, 'admin_ui': ui_receipt,
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
    if getattr(args, 'join_grant_stdin', False):
        if args.grant:
            raise UsageError('--grant and --join-grant-stdin are mutually exclusive')
        args.grant = read_join_grant_stdin()
    if not args.grant:
        raise UsageError('--grant or --join-grant-stdin is required')

    env = {} if args.identity_file else read_installed_env(layout, args.name)
    bind_port = int(args.bind_port or env.get('GPTADMIN_HUB_PORT') or DEFAULT_BIND[1])
    if not 1 <= bind_port <= 65535:
        raise UsageError('--bind-port must be between 1 and 65535')
    current = current_target(layout) if not args.identity_file else None
    manifest = manifest_summary(current) if current else None
    if args.endpoint:
        endpoints = [validate_origin(ep, '--endpoint') for ep in args.endpoint]
        connect_to = args.connect_to
    else:
        parsed = urllib.parse.urlsplit(origin)
        join_port = parsed.port or (443 if parsed.scheme == 'https' else 80)
        lan_ip = local_lan_ip(parsed.hostname, join_port) or fallback_lan_ip()
        if not lan_ip:
            raise PreflightError('could not determine the local LAN address; pass --endpoint explicitly')
        endpoints = [f'http://{lan_ip}:{bind_port}']
        connect_to = args.connect_to or lan_ip
    if connect_to:
        try:
            socket.inet_pton(socket.AF_INET6 if ':' in connect_to else socket.AF_INET, connect_to)
        except OSError:
            raise UsageError('--connect-to must be a literal IP address') from None

    identity_path = Path(args.identity_file) if args.identity_file else layout.identity_file(args.name)
    identity_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    payload = perform_join(origin, args.grant, identity_path, args.name,
                           endpoints, connect_to, manifest)
    identity = json.loads(identity_path.read_text(encoding='utf-8'))
    print(json.dumps({'ok': True, 'command': 'join', 'name': args.name,
                      'writer_url': identity.get('writer_url'),
                      'writer_id': identity.get('writer_id'),
                      'membership': payload}, sort_keys=True))


def active_node_names(runner: Runner) -> list[str]:
    result = runner.run(['systemctl', 'list-units', 'gptadmin-node@*.service',
                         '--state=active', '--plain', '--no-legend'], timeout=5)
    if result.returncode:
        raise RuntimeFailure('could not enumerate active GPTAdmin Node instances')
    names = []
    for line in result.stdout.splitlines():
        unit = line.split()[0] if line.split() else ''
        if unit.startswith('gptadmin-node@') and unit.endswith('.service'):
            name = unit[len('gptadmin-node@'):-len('.service')]
            validate_name(name)
            names.append(name)
    if primary_unified_node(runner) and 'primary' not in names:
        names.append('primary')
    if not 1 <= len(names) <= 8 or len(names) != len(set(names)):
        raise PreflightError('automatic updates require 1..8 distinct active GPTAdmin Node instances')
    return sorted(names)


def active_node_versions(layout: Layout, names: list[str]) -> dict[str, dict]:
    """Read the live source/version baseline for every explicitly enrolled process."""
    versions = {}
    for name in names:
        env = read_primary_effective_env() if name == 'primary' else read_installed_env(layout, name)
        status, version = http_json('GET', origin_from_env(env) + '/version')
        build = parse_build_version(version.get('build_version')) if isinstance(version, dict) else None
        if (status != 200 or not isinstance(version, dict) or version.get('name') != 'gptadmin-go-hub'
                or not re.fullmatch(r'[0-9a-f]{40}', str(version.get('git_commit', '')))
                or build is None):
            raise PreflightError(f'live Node version could not be verified for {name}')
        versions[name] = {'git_commit': version['git_commit'], 'build_version': build}
    return versions


def primary_unified_node(runner: Runner) -> bool:
    primary_env = PRIMARY_ENV_FILE.is_file() and not PRIMARY_ENV_FILE.is_symlink()
    base_env = PRIMARY_BASE_ENV_FILE.is_file() and not PRIMARY_BASE_ENV_FILE.is_symlink()
    if not primary_env and not base_env:
        return False
    if runner.run(['systemctl', 'is-active', PRIMARY_SERVICE], timeout=5).returncode:
        return False
    service = runner.run(['systemctl', 'show', PRIMARY_SERVICE, '-p', 'ExecStart', '--value'], timeout=5)
    supported_execs = (
        '/opt/gptadmin/bin/gptadmin-node',
        '/opt/gptadmin-node/current/bin/gptadmin-node',
    )
    if service.returncode or not any(path in service.stdout for path in supported_execs):
        return False
    env = read_primary_effective_env()
    status, version = http_json('GET', origin_from_env(env) + '/version')
    return (status == 200 and isinstance(version, dict)
            and version.get('name') == 'gptadmin-go-hub'
            and re.fullmatch(r'[0-9a-f]{40}', str(version.get('git_commit', ''))) is not None)


def prepare_primary_node(layout: Layout, runner: Runner) -> None:
    """Adopt the existing unified primary Node into the same version-pointer updater."""
    if not primary_unified_node(runner):
        return
    st = PRIMARY_ENV_FILE.stat()
    if st.st_uid != 0 or st.st_mode & 0o077:
        raise PreflightError('primary Node environment is not root-owned and private')
    if PRIMARY_BINARY.is_symlink() or not PRIMARY_BINARY.is_file() or PRIMARY_BINARY.stat().st_uid != 0:
        raise PreflightError('primary unified Node binary is not a protected regular file')
    env = read_primary_effective_env()
    if not (env.get('CTL_TOKEN') or env.get('ADMIN_PASSWORD')):
        raise PreflightError('primary unified Node lacks its local drain credential')
    status, version = http_json('GET', origin_from_env(env) + '/version')
    build = parse_build_version(version.get('build_version')) if isinstance(version, dict) else None
    if (status != 200 or not isinstance(version, dict) or version.get('name') != 'gptadmin-go-hub'
            or not re.fullmatch(r'[0-9a-f]{40}', str(version.get('git_commit', '')))
            or build is None):
        raise PreflightError('primary unified Node version could not be verified')
    version = {**version, 'build_version': build}
    version_dir = current_target(layout)
    if version_dir is not None:
        validate_primary_current(version_dir, version)
    else:
        versions = layout.versions_root()
        versions.mkdir(mode=0o755, parents=True, exist_ok=True)
        commit = version['git_commit']
        version_dir = next_version_dir(versions, commit)
        binary_dir = version_dir / 'bin'
        binary_dir.mkdir(parents=True, mode=0o755)
        shutil.copy2(PRIMARY_BINARY, binary_dir / 'gptadmin-node')
        os.chmod(binary_dir / 'gptadmin-node', 0o755)
        manifest = {
            'schema': PACKAGE_SCHEMA, 'git_commit': commit,
            'build_version': version['build_version'], 'platform': 'linux',
            'arch': host_arch(), 'source_dirty': False,
            'files': {'bin/gptadmin-node': {'sha256': hashlib.sha256((binary_dir / 'gptadmin-node').read_bytes()).hexdigest(),
                                            'size': (binary_dir / 'gptadmin-node').stat().st_size}},
        }
        atomic_write(version_dir / 'manifest.json', json.dumps(manifest, sort_keys=True) + '\n', 0o644)
        switch_current(layout, version_dir)
    config_dir = layout.config('primary')
    config_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    env_alias = layout.env_file('primary')
    if env_alias.exists() and not env_alias.is_symlink():
        if env_alias.read_bytes() != PRIMARY_ENV_FILE.read_bytes():
            raise PreflightError('primary Node manager environment already has a different owner')
    elif env_alias.is_symlink():
        if env_alias.resolve() != PRIMARY_ENV_FILE.resolve():
            raise PreflightError('primary Node manager environment symlink points elsewhere')
    else:
        os.symlink(PRIMARY_ENV_FILE, env_alias)
    prepare_primary_dropin()


def prepare_primary_dropin() -> None:
    """Migrate only the known primary Node lifecycle override."""
    legacy_dropin_text = '[Service]\nExecStart=\nExecStart=/opt/gptadmin-node/current/bin/gptadmin-node\n'
    # Unified Node owns its lifecycle; inherited legacy hooks operate a different
    # binary and local standby. Clear them in the highest-priority Node drop-in.
    dropin_text = ('[Unit]\nOnFailure=\n\n[Service]\n'
                   'ExecStop=\nExecStopPost=\nExecStartPost=\n'
                   'ExecStart=\nExecStart=/opt/gptadmin-node/current/bin/gptadmin-node\n')
    if PRIMARY_DROPIN.exists() and PRIMARY_DROPIN.read_text() not in (legacy_dropin_text, dropin_text):
        raise PreflightError('primary auto-update service drop-in already has different contents')
    atomic_write(PRIMARY_DROPIN, dropin_text, 0o644)


def read_primary_effective_env() -> dict:
    env = parse_env_file(PRIMARY_BASE_ENV_FILE) if PRIMARY_BASE_ENV_FILE.is_file() else {}
    env.update(parse_env_file(PRIMARY_ENV_FILE))
    return env


def cmd_autoupdate(args, layout: Layout, runner: Runner) -> None:
    """Manage the explicit opt-in to the verified public Node release channel."""
    require_root()
    action = args.action
    if AUTOUPDATE_CONFIG.is_symlink():
        raise PreflightError('automatic update config must not be a symlink')
    try:
        existing = json.loads(AUTOUPDATE_CONFIG.read_text()) if AUTOUPDATE_CONFIG.is_file() else {}
    except (OSError, ValueError):
        raise PreflightError('automatic update config is unreadable') from None
    if action == 'status':
        active = runner.run(['systemctl', 'is-active', AUTOUPDATE_TIMER], timeout=5)
        enabled = runner.run(['systemctl', 'is-enabled', AUTOUPDATE_TIMER], timeout=5)
        print(json.dumps({'enabled': existing.get('enabled') is True,
                          'names': existing.get('names', []),
                          'timer_active': active.returncode == 0,
                          'timer_enabled': enabled.returncode == 0}, sort_keys=True))
        return
    if action == 'disable':
        names = existing.get('names', [])
        if not isinstance(names, list) or any(not isinstance(n, str) for n in names):
            raise PreflightError('automatic update config has invalid instance names')
        atomic_write(AUTOUPDATE_CONFIG, json.dumps({'enabled': False, 'names': names}, sort_keys=True) + '\n', 0o600)
        runner.run(['systemctl', 'disable', '--now', AUTOUPDATE_TIMER], timeout=30)
        print(json.dumps({'enabled': False, 'names': names}, sort_keys=True))
        return
    if action == 'run':
        systemctl(runner, 'start', AUTOUPDATE_SERVICE,
                  timeout=AUTOUPDATE_SERVICE_TIMEOUT_SECS + 30)
        return
    if action != 'enable':
        raise UsageError('autoupdate action must be status, enable, disable or run')
    names = active_node_names(runner)
    live_versions = active_node_versions(layout, names)
    source_dir = Path(__file__).resolve().parent
    helpers = (
        (AUTOUPDATE_MANAGER, source_dir / 'gptadmin_node_manage.py'),
        (AUTOUPDATE_PROBE, source_dir / 'gptadmin_node_probe.py'),
        (AUTOUPDATE_SCRIPT, source_dir / 'gptadmin_node_autoupdate.py'),
    )
    helper_backups = {}
    helper_contents = {}
    for destination, source in helpers:
        if source.is_symlink() or not source.is_file() or source.stat().st_size > 2 * 1024**2:
            raise PreflightError('Node updater helper source is missing or unsafe')
        if destination.is_symlink() or (destination.exists() and
                (destination.stat().st_uid != 0 or destination.stat().st_mode & 0o022)):
            raise PreflightError('installed Node updater helper is not protected')
        helper_backups[destination] = (
            destination.read_bytes(), destination.stat().st_mode & 0o777
        ) if destination.exists() else None
        helper_contents[destination] = source.read_text(encoding='utf-8')
    AUTOUPDATE_CONFIG.parent.mkdir(parents=True, exist_ok=True)
    AUTOUPDATE_SCRIPT.parent.mkdir(parents=True, exist_ok=True)
    for destination, contents in helper_contents.items():
        atomic_write(destination, contents, 0o644)
    config = {
        'enabled': True,
        'names': names,
        'minimum_build_version': max(v['build_version'] for v in live_versions.values()),
        'minimum_sources': sorted({v['git_commit'] for v in live_versions.values()}),
    }
    atomic_write(AUTOUPDATE_CONFIG, json.dumps(config, sort_keys=True) + '\n', 0o600)
    try:
        check = subprocess.run(['/usr/bin/python3', str(AUTOUPDATE_SCRIPT), '--check'],
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180, check=False)
    except (OSError, subprocess.TimeoutExpired):
        check = None
    if check is None or check.returncode:
        restore = existing if existing.get('enabled') is True else {'enabled': False, 'names': names}
        atomic_write(AUTOUPDATE_CONFIG, json.dumps(restore, sort_keys=True) + '\n', 0o600)
        for destination, backup in helper_backups.items():
            if backup is None:
                destination.unlink(missing_ok=True)
            else:
                data, mode = backup
                atomic_write(destination, data, mode)
        raise PreflightError('public Node package verification failed; existing timer state was preserved')
    prepare_primary_node(layout, runner)
    atomic_write(AUTOUPDATE_SERVICE_PATH, AUTOUPDATE_SERVICE_TEXT, 0o644)
    atomic_write(AUTOUPDATE_TIMER_PATH, AUTOUPDATE_TIMER_TEXT, 0o644)
    systemctl(runner, 'daemon-reload')
    systemctl(runner, 'enable', '--now', AUTOUPDATE_TIMER)
    print(json.dumps({'enabled': True, 'names': names, 'timer': AUTOUPDATE_TIMER,
                      'minimum_build_version': config['minimum_build_version']}, sort_keys=True))


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
    install.add_argument('--node-public-url', default=None, metavar='HTTPS-URL',
                         help='public HTTPS URL used by quorum peers; separate from PUBLIC_ORIGIN')
    install.add_argument('--join-url', default=None, metavar='ORIGIN')
    install.add_argument('--join-grant', default=None, metavar='GRANT')
    install.add_argument('--join-grant-stdin', action='store_true',
                         help='read one-time join grant from stdin instead of process arguments')
    install.add_argument('--seed-peers', default=None, metavar='JSON')
    install.add_argument('--admin-password', default=None,
                         help='legacy local ADMIN_PASSWORD env line; omit by default')
    install.add_argument('--yes', action='store_true', help='non-interactive install')
    install.set_defaults(func=cmd_install)

    status = sub.add_parser('status', help='report versions, service, health and membership as JSON')
    status.add_argument('--name', default=None, metavar='NAME')
    status.set_defaults(func=cmd_status)

    ui_publish = sub.add_parser('ui-publish', help='publish the current package UI atomically; no Hub restart')
    ui_publish.add_argument('--name', default='primary', metavar='NAME')
    ui_publish.set_defaults(func=cmd_ui_publish)

    update = sub.add_parser('update', help='drain, switch to a new package, verify; auto-rollback on failure')
    update.add_argument('--package', required=True, metavar='TAR.GZ')
    update.add_argument('--name', default=None, metavar='NAME')
    update.add_argument('--wait-secs', type=float, default=300, metavar='SECS',
                        help='drain ready_to_stop deadline; default 300')
    update.add_argument('--require-drain', action='store_true',
                        help='refuse automatic update if drain/readiness is unsupported')
    update.add_argument('--node-public-url', default=None, metavar='HTTPS-URL',
                        help='change this node public HTTPS URL during the drained update')
    update.add_argument('--operation-deadline-secs', type=float, default=None,
                        help='catchable update deadline (1..50s), plus 80s rollback reserve')
    update.set_defaults(func=cmd_update)

    shellmcp = sub.add_parser('shellmcp', help='manage the standalone Go ShellMCP component')
    shellmcp_sub = shellmcp.add_subparsers(dest='shellmcp_action', metavar='ACTION', required=True)
    shellmcp_status = shellmcp_sub.add_parser('status', help='read active ShellMCP service and version')
    shellmcp_status.set_defaults(func=cmd_shellmcp_status)
    shellmcp_update = shellmcp_sub.add_parser('update', help='install a verified ShellMCP binary and rollback on failed readback')
    shellmcp_update.add_argument('--package', required=True, metavar='TAR.GZ')
    shellmcp_update.set_defaults(func=cmd_shellmcp_update)
    shellmcp_rollback = shellmcp_sub.add_parser('rollback', help='switch to a previous verified ShellMCP version')
    shellmcp_rollback.add_argument('--to', default=None, metavar='VERSION-DIR')
    shellmcp_rollback.set_defaults(func=cmd_shellmcp_rollback)

    proxy_route = sub.add_parser('proxy-route', help='install or inspect the fixed VUSA tunnel unit')
    proxy_sub = proxy_route.add_subparsers(dest='proxy_route_action', metavar='ACTION', required=True)
    proxy_status = proxy_sub.add_parser('status', help='read fixed proxy unit installation/activation state')
    proxy_status.set_defaults(func=cmd_proxy_route_status)
    proxy_install = proxy_sub.add_parser('install', help='install the fixed unit from a verified Node package; never starts it')
    proxy_install.add_argument('--package', required=True, metavar='TAR.GZ')
    proxy_install.set_defaults(func=cmd_proxy_route_install)

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

    join = sub.add_parser('join', help='join a node to a mesh writer with a one-time grant')
    join.add_argument('--name', required=True, metavar='NAME')
    join.add_argument('--url', required=True, metavar='ORIGIN')
    join.add_argument('--grant', default=None, metavar='GRANT')
    join.add_argument('--join-grant-stdin', action='store_true',
                      help='read the one-time join grant from stdin instead of argv')
    join.add_argument('--identity-file', default=None, metavar='PATH',
                      help='write the joined identity here instead of the installed-node state path')
    join.add_argument('--bind-port', type=int, default=None, metavar='PORT',
                      help=f'advertised local node port; default {DEFAULT_BIND[1]}')
    join.add_argument('--endpoint', action='append', metavar='URL',
                      help='advertised endpoint; repeatable, default auto LAN detection')
    join.add_argument('--connect-to', default=None, metavar='IP',
                      help='connect directly to this LAN IP while preserving endpoint TLS verification')
    join.set_defaults(func=cmd_join)

    autoupdate = sub.add_parser('autoupdate', help='opt in to verified public Node releases')
    autoupdate.add_argument('action', choices=('status', 'enable', 'disable', 'run'), nargs='?', default='status')
    autoupdate.set_defaults(func=cmd_autoupdate)
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
