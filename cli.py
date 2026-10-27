#!/usr/bin/env python3
# -*- coding: utf-8 -*-
from __future__ import annotations

import argparse
import gzip
import io
import stat
import os
import sys
import tarfile
import tempfile
import base64
import hmac
import hashlib
import json
import subprocess
import shutil
import socket
import re
import secrets
import time
import urllib.parse
import urllib.request
import urllib.error
import pwd
from functools import wraps
from email.utils import parsedate_to_datetime
from pathlib import Path, PurePosixPath
# The curl bootstrap must run on a stock Python installation.  The signed
# optional MCP catalogue is the only feature that needs ``cryptography``;
# defer that dependency so setup/update are not blocked before packages can
# install their runtime payloads.
try:
    from cryptography.exceptions import InvalidSignature
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
except ImportError:  # pragma: no cover - exercised in the installer subprocess
    InvalidSignature = ValueError
    Ed25519PublicKey = None
try:
    import tomllib
except Exception:
    tomllib = None

# CTL_TOKEN is a deprecated compatibility credential. New and updated
# installations do not create it, but an existing credential remains valid
# until its owner explicitly rotates or removes it.

# ===== Platform =====
IS_MACOS = sys.platform == 'darwin'
IS_WINDOWS = sys.platform == 'win32'

# ===== ANSI Colors =====
_IS_TTY = sys.stderr.isatty() if hasattr(sys, 'stderr') else False
_NO_COLOR = os.environ.get('NO_COLOR', '').strip() or os.environ.get('GPTADMIN_NO_COLOR', '').strip()

def _c(code: str, text: str) -> str:
    """Wrap text in ANSI color if TTY and not disabled."""
    if not _IS_TTY or _NO_COLOR:
        return text
    return f'\033[{code}m{text}\033[0m'

def c_green(t):  return _c('32', t)
def c_red(t):    return _c('31', t)
def c_yellow(t): return _c('33', t)
def c_cyan(t):   return _c('36', t)
def c_dim(t):    return _c('2', t)
def c_bold(t):   return _c('1', t)
def c_mag(t):    return _c('35', t)

def c_ok(t):     return c_green('✓ ' + t)
def c_err(t):    return c_red('✗ ' + t)
def c_warn(t):   return c_yellow('⚠ ' + t)
def c_info(t):   return c_cyan('→ ' + t)

def print_ok(t):    print(c_ok(t))
def print_err(t):   print(c_err(t), file=sys.stderr)
def print_warn(t):  print(c_warn(t), file=sys.stderr)
def print_info(t):  print(c_info(t))
def print_header(t): print(c_bold(c_mag(t)))

# ===== Install mode & paths =====
def _early_install_mode_user() -> bool:
    mode = os.environ.get('GPTADMIN_INSTALL_MODE', '').strip().lower()
    if mode in {'user', 'userspace', 'local', 'nonroot'}:
        return True
    if mode in {'system', 'root', 'admin'}:
        return False
    if '--user' in sys.argv:
        return True
    if '--system' in sys.argv:
        return False
    try:
        return os.geteuid() != 0
    except Exception:
        return False


def _install_user_home() -> Path:
    if os.environ.get('GPTADMIN_USER_HOME'):
        return Path(os.environ['GPTADMIN_USER_HOME']).expanduser()
    try:
        if os.geteuid() == 0:
            sudo_user = os.environ.get('SUDO_USER')
            if sudo_user and sudo_user != 'root':
                try:
                    return Path(pwd.getpwnam(sudo_user).pw_dir)
                except Exception:
                    return Path('/Users' if IS_MACOS else '/home') / sudo_user
    except Exception:
        pass
    return Path.home()


IS_USER_INSTALL = _early_install_mode_user()
INSTALL_SCOPE = 'user' if IS_USER_INSTALL else 'system'
USER_HOME = _install_user_home()

if IS_USER_INSTALL:
    INSTALL_DIR = Path(os.environ.get('GPTADMIN_HOME', str(USER_HOME / '.local' / 'share' / 'gptadmin'))).expanduser()
    ETC_DIR = Path(os.environ.get('GPTADMIN_CONFIG_DIR', str(USER_HOME / '.config' / 'gptadmin'))).expanduser()
    CLI_PATH = Path(os.environ.get('GPTADMIN_CLI_PATH', str(USER_HOME / '.local' / 'bin' / 'gptadmin'))).expanduser()
else:
    INSTALL_DIR = Path(os.environ.get('GPTADMIN_HOME', '/opt/gptadmin'))
    ETC_DIR = Path(os.environ.get('GPTADMIN_CONFIG_DIR', '/etc/gptadmin'))
    CLI_PATH = Path(os.environ.get('GPTADMIN_CLI_PATH', '/usr/local/bin/gptadmin'))

# ===== Paths & constants =====
BIN_DIR = INSTALL_DIR / 'bin'
ENV_FILE = ETC_DIR / 'gptadmin.env'
INSTALLED_BUILD_FILE = INSTALL_DIR / 'gptadmin_installed_build.json'
MCP_CONFIG_FILE = ETC_DIR / 'mcp.json'
UPDATE_CHECK_CACHE = INSTALL_DIR / 'update_check.json'
UPDATE_CHECK_COOLDOWN_S = 3600       # 1 hour after failed network attempt
UPDATE_CHECK_FRESH_S = 86400         # 24 hours for successful check
UPDATE_CHECK_TIMEOUT_S = 3           # manifest fetch timeout
MCP_AGENTS_DIR = ETC_DIR / 'mcp-agents.d'
MCP_SUPERVISOR_CONFIG = ETC_DIR / 'mcp-supervisor.json'
MCP_TOKEN_FILE = ETC_DIR / 'mcp-relay.token'
STARTUP_INSTRUCTIONS_FILE = ETC_DIR / 'startup_instructions.md'
GREPMESH_CONFIG_FILE = ETC_DIR / 'grepmesh.json'
STARTUP_INSTRUCTIONS_MAX_BYTES = 16 * 1024

MCP_CAPABILITY_CATALOG_VERSION = 'gptadmin-capabilities/v1'
# The private signing key is intentionally not part of GPTAdmin. This detached
# public key and signature authenticate the immutable catalog shipped in this
# release; changing a definition requires a deliberate release-time re-sign.
MCP_CAPABILITY_CATALOG_PUBLIC_KEY_B64 = 'em0_XbX6G5Tr8CSbH9EfQMFWUyHFHwyjYhs6JkCTl3s'
MCP_CAPABILITY_CATALOG_SIGNATURE_B64 = 'fMIkvEZ7uOq3L19onPa9JyYK9oef-B5-Go9_CmaHYO9a2MKRMTHmogA9D4bK5mfhqF3K6dOBXfOWcjNRFOUmDQ'
MCP_CAPABILITY_CATALOG = (
    {
        'id': 'gptadmin-safe-demo',
        'version': '1.0.0',
        'provenance': 'bundled:gptadmin-hub',
        'scopes': ['gptadmin.read'],
        'network_needs': [],
        'risk_level': 'low',
        'maintenance_owner': 'GPTAdmin',
        'tools': ['demo'],
    },
    {
        'id': 'gptadmin-readonly-inspection',
        'version': '1.0.0',
        'provenance': 'bundled:gptadmin-shellmcp',
        'scopes': ['gptadmin.inspect'],
        'network_needs': [],
        'risk_level': 'medium',
        'maintenance_owner': 'GPTAdmin',
        'tools': ['system_inspect', 'inspect'],
    },
    {
        'id': 'gptadmin-network-tunnel',
        'version': '1.0.0',
        'provenance': 'bundled:gptadmin-network-proxy',
        'scopes': ['gptadmin.network'],
        'network_needs': ['explicit finite LAN or internet-egress target policy'],
        'risk_level': 'high',
        'maintenance_owner': 'GPTAdmin',
        'tools': ['network_proxy_request', 'network_proxy_issue'],
    },
)

if IS_MACOS:
    SERVICES_DIR = USER_HOME / 'Library' / 'LaunchAgents' if IS_USER_INSTALL else Path('/Library/LaunchDaemons')
    LOG_DIR = USER_HOME / 'Library' / 'Logs' / 'gptadmin' if IS_USER_INSTALL else Path('/var/log/gptadmin')
    # Optional test namespace for parallel macOS launchd installs.
    # Example: GPTADMIN_SERVICE_SUFFIX=.e2e42 -> com.gptadmin.e2e42.hub
    # Empty by default, so normal production labels stay unchanged.
    SERVICE_SUFFIX = os.environ.get('GPTADMIN_SERVICE_SUFFIX', '').strip()
    if SERVICE_SUFFIX and not re.fullmatch(r'[A-Za-z0-9_.-]+', SERVICE_SUFFIX):
        die('GPTADMIN_SERVICE_SUFFIX may contain only letters, digits, dot, underscore and dash')
    SERVICE_PREFIX = f'com.gptadmin{SERVICE_SUFFIX}'
    SVC_HUB_LABEL   = f'{SERVICE_PREFIX}.hub'
    SVC_SHELLMCP_LABEL = f'{SERVICE_PREFIX}.shellmcp'
    SVC_GREPMESH_LABEL = f'{SERVICE_PREFIX}.grepmesh'
    SVC_FRPC_LABEL  = f'{SERVICE_PREFIX}.tunnel-frpc'
    SVC_CLOUDFLARED_LABEL = f'{SERVICE_PREFIX}.cloudflared'
    SVC_AUTO_UPDATE_LABEL = f'{SERVICE_PREFIX}.auto-update'
    UNIT_PATH_HUB   = SERVICES_DIR / f'{SVC_HUB_LABEL}.plist'
    UNIT_PATH_SHELLMCP = SERVICES_DIR / f'{SVC_SHELLMCP_LABEL}.plist'
    UNIT_PATH_GREPMESH = SERVICES_DIR / f'{SVC_GREPMESH_LABEL}.plist'
    UNIT_PATH_FRPC  = SERVICES_DIR / f'{SVC_FRPC_LABEL}.plist'
    UNIT_PATH_CLOUDFLARED = SERVICES_DIR / f'{SVC_CLOUDFLARED_LABEL}.plist'
    UNIT_PATH_AUTO_UPDATE = SERVICES_DIR / f'{SVC_AUTO_UPDATE_LABEL}.plist'
    FRPC_CONF = ETC_DIR / 'frpc.toml'
else:
    SYSTEMD_DIR = USER_HOME / '.config' / 'systemd' / 'user' if IS_USER_INSTALL else Path('/etc/systemd/system')
    LOG_DIR = Path(os.environ.get(
        'GPTADMIN_LOG_DIR',
        str((USER_HOME / '.local' / 'state' / 'gptadmin' / 'logs') if IS_USER_INSTALL else Path('/var/log/gptadmin'))
    )).expanduser()
    SYSTEMD_HUB   = 'gptadmin-hub.service'
    SYSTEMD_HUB_STANDBY = 'gptadmin-hub-standby.service'
    SYSTEMD_SHELLMCP = 'shellmcp.service'
    SYSTEMD_GREPMESH = 'gptadmin-grepmesh-mcp.service'
    SYSTEMD_FRPC  = 'gptadmin-tunnel-frpc.service'
    SYSTEMD_CLOUDFLARED = 'gptadmin-cloudflared.service'
    SYSTEMD_AUTO_UPDATE = 'gptadmin-auto-update.service'
    SYSTEMD_AUTO_UPDATE_TIMER = 'gptadmin-auto-update.timer'
    UNIT_PATH_HUB   = SYSTEMD_DIR / SYSTEMD_HUB
    UNIT_PATH_HUB_STANDBY = SYSTEMD_DIR / SYSTEMD_HUB_STANDBY
    UNIT_PATH_SHELLMCP = SYSTEMD_DIR / SYSTEMD_SHELLMCP
    UNIT_PATH_GREPMESH = SYSTEMD_DIR / SYSTEMD_GREPMESH
    UNIT_PATH_FRPC  = SYSTEMD_DIR / SYSTEMD_FRPC
    UNIT_PATH_CLOUDFLARED = SYSTEMD_DIR / SYSTEMD_CLOUDFLARED
    UNIT_PATH_AUTO_UPDATE = SYSTEMD_DIR / SYSTEMD_AUTO_UPDATE
    UNIT_PATH_AUTO_UPDATE_TIMER = SYSTEMD_DIR / SYSTEMD_AUTO_UPDATE_TIMER
    FRPC_CONF = ETC_DIR / 'frpc.toml'

# Package URLs can be overridden by env or args
PKG_BASE_URL_DEFAULT = os.environ.get('PKG_BASE_URL', 'https://github.com/megamen32/gptadmin_opensource/releases/latest/download').rstrip('/')
PKG_ALL_URL_DEFAULT   = os.environ.get('PKG_ALL_URL',   f'{PKG_BASE_URL_DEFAULT}/gptadmin.tar.gz')
PKG_HUB_URL_DEFAULT   = os.environ.get('PKG_HUB_URL',   f'{PKG_BASE_URL_DEFAULT}/gptadmin-hub.tar.gz')
PKG_SHELLMCP_URL_DEFAULT = os.environ.get('PKG_SHELLMCP_URL', f'{PKG_BASE_URL_DEFAULT}/gptadmin-shellmcp.tar.gz')
REQUIRED_CMDS = ['curl', 'launchctl' if IS_MACOS else 'systemctl']

# ===== macOS plist + launchctl helpers (module-level for cross-platform testability) =====
# These helpers live at module level so they can be unit-tested on Linux even
# though they generate launchd-only artifacts. The Darwin-only branch below
# composes them; Linux never imports them.

def _plist_oneshot(label: str, wrapper: Path, log_file: Path, interval: int | None = None) -> str:
    """Generate a launchd plist for the auto-update oneshot job.

    Semantics:
      - RunAtLoad=false and KeepAlive=false: the job does NOT start on its own
        and does NOT auto-restart. It must be triggered explicitly via
        `launchctl kickstart` (which is what we use for both periodic and
        manual update triggers).
      - AbandonProcessGroup=true: prevents launchd from sending SIGTERM to
        the wrapper's process group on bootout. The job is therefore allowed
        to complete even if the parent launchd job is unloaded mid-run.
        Children are *abandoned*, not cleaned up. The wrapper `exec`s into
        the CLI without forking, so we have no children to worry about;
        the flag is set for bootout-resilience, not for cleanup.
      - StartInterval (optional): if provided, launchd schedules the job to
        run every <interval> seconds. When omitted, the plist is a pure
        "service unit always present" that does nothing until kicked.
    """
    interval_line = (
        f'    <key>StartInterval</key><integer>{int(interval)}</integer>\n'
        if interval is not None else ''
    )
    return (
        '<?xml version="1.0" encoding="UTF-8"?>\n'
        '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"'
        ' "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n'
        '<plist version="1.0"><dict>\n'
        f'    <key>Label</key><string>{label}</string>\n'
        '    <key>ProgramArguments</key><array>\n'
        '        <string>/bin/sh</string>\n'
        f'        <string>{wrapper}</string>\n'
        '    </array>\n'
        '    <key>RunAtLoad</key><false/>\n'
        '    <key>KeepAlive</key><false/>\n'
        '    <key>AbandonProcessGroup</key><true/>\n'
        f'{interval_line}'
        f'    <key>StandardOutPath</key><string>{log_file}</string>\n'
        f'    <key>StandardErrorPath</key><string>{log_file}</string>\n'
        '</dict></plist>\n'
    )


def _launchctl_kickstart_cmd(label: str, is_user: bool) -> list[str]:
    """Return the argv for `launchctl kickstart -k <domain>/<label>`.

    The `-k` flag kills any existing instance first, then starts a fresh one.
    That makes the same invocation safe for both "first ever run" and
    "already-running, restart" — which is exactly what the auto-update path
    needs.
    """
    if is_user:
        try:
            uid = str(os.getuid())
        except Exception:
            uid = '0'
        domain = f'gui/{uid}'
    else:
        domain = 'system'
    return ['launchctl', 'kickstart', '-k', f'{domain}/{label}']

# ===== FRPC defaults =====
FRPC_VERSION          = os.environ.get('FRPC_VERSION', '0.64.0')
FRPC_BASE_URL         = os.environ.get('FRPC_BASE_URL', 'https://became.bezrabotnyi.com/frp-mirror')
FRPC_SERVER_ADDR_DEFAULT = 'became.bezrabotnyi.com'
FRPC_SERVER_PORT_DEFAULT = '7000'
FRPC_TOKEN_DEFAULT = os.environ.get('GPTADMIN_FRP_TOKEN', '').strip()
FRPC_DOMAIN_DEFAULT   = 't.became.bezrabotnyi.com'
FRPC_SERVER_ENDPOINTS_DEFAULT = os.environ.get(
    'FRPC_SERVER_ENDPOINTS_DEFAULT',
    'primary=became.bezrabotnyi.com:7000,relay01=relay01.bezrabotnyi.com:27001,relay02=relay02.bezrabotnyi.com:27000'
).strip()
CLOUDFLARED_VERSION   = os.environ.get('CLOUDFLARED_VERSION', 'latest')

# ===== Helpers =====

def die(msg: str, code: int = 1):
    print(f'ERROR: {msg}', file=sys.stderr)
    sys.exit(code)

def need_root():
    if IS_USER_INSTALL:
        return
    if os.geteuid() != 0:
        die('run as root (sudo), or use --user / GPTADMIN_INSTALL_MODE=user')

def have(cmd: str) -> bool:
    return shutil.which(cmd) is not None

def run(cmd, check=True, capture=False, timeout=None):
    try:
        if capture:
            return subprocess.run(cmd, check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
        return subprocess.run(cmd, check=check, timeout=timeout)
    except subprocess.TimeoutExpired as e:
        if check:
            raise
        print(f'WARNING: command timed out and was ignored: {cmd}', file=sys.stderr)
        return subprocess.CompletedProcess(cmd, 124, stdout=getattr(e, 'stdout', None), stderr=getattr(e, 'stderr', None))

# .env read/write

def env_read() -> dict:
    d = {}
    if ENV_FILE.exists():
        for raw in ENV_FILE.read_text().splitlines():
            line = raw.strip()
            if not line or line.startswith('#'):
                continue
            if '=' in line:
                k, v = line.split('=', 1)
                d[k.strip()] = v.strip()
    return d

def env_set_many(upd: dict):
    cur = env_read()
    cur.update(upd)
    lines = [f'{k}={cur[k]}' for k in sorted(cur.keys())]
    ENV_FILE.parent.mkdir(parents=True, exist_ok=True)
    tmp = ENV_FILE.with_name(f'.{ENV_FILE.name}.{os.getpid()}.tmp')
    try:
        tmp.write_text('\n'.join(lines) + '\n')
        # gptadmin.env contains bearer/OAuth/admin secrets.  Keep it private in
        # both user and system installs; doctor deliberately rejects group-read.
        os.chmod(tmp, 0o600)
        os.replace(tmp, ENV_FILE)
    finally:
        tmp.unlink(missing_ok=True)


_PERSISTENT_AUTH_KEYS = frozenset({
    'CTL_TOKEN',
    'SHELLMCP_TOKEN',
    'SHELL_TOKEN',
    'ROOTD_TOKEN',
    'ROOTD_UPDATE_TOKEN',
    'ADMIN_PASSWORD',
    'OAUTH_CLIENT_SECRET',
    'MCP_BRIDGE_KEY',
    'MCP_RELAY_AGENT_TOKEN',
    'MCP_RELAY_ENROLLMENT_PASSWORD',
    'SHELLMCP_UPDATE_TOKEN',
})


def _capture_persistent_auth_material(env: dict) -> dict[str, str]:
    """Capture secrets and client JWTs that an in-place update must preserve."""
    return {
        key: str(value)
        for key, value in env.items()
        if value and (key in _PERSISTENT_AUTH_KEYS or key.startswith('GPTADMIN_') and key.endswith('_MCP_BEARER'))
    }


def _restore_persistent_auth_material(env: dict, saved: dict[str, str]) -> None:
    """Restore auth material captured before package replacement."""
    env.update(saved)


def normalize_bundled_shellmcp_hub_url(env: dict, install_hub: bool, install_shellmcp: bool) -> None:
    """Keep same-host ShellMCP on the local Hub even when public ingress fails over.

    HUB_PUBLIC_URL/PUBLIC_ORIGIN/MCP_RESOURCE are the external identity. HUB_URL is
    the durable transport used by ShellMCP. A bundled Hub+ShellMCP install must
    never poll its own public failover URL: during a Hub handover that can point
    at standby and strand jobs created on the restarted primary.
    """
    if not (install_hub and install_shellmcp):
        return
    local_hub = f"http://127.0.0.1:{env.get('HUB_PORT') or '9001'}"
    env['HUB_URL'] = local_hub
    transport = str(env.get('SHELLMCP_TRANSPORT') or 'polling').strip().lower()
    queue_enabled = str(env.get('SHELLMCP_QUEUE') or env.get('SHELL_QUEUE') or '1').strip().lower()
    if transport in {'polling', 'long_poll', 'long-poll'} or queue_enabled in {'1', 'true', 'yes', 'on'}:
        env['QUEUE_URL'] = local_hub + '/queue'


def ensure_shellmcp_default_user(env: dict) -> None:
    """Persist the invoking non-root account for ordinary ShellMCP commands."""
    if env.get('SHELL_DEFAULT_USER') or env.get('SHELLMCP_DEFAULT_USER'):
        return
    candidate = os.environ.get('SHELLMCP_DEFAULT_USER') or os.environ.get('SHELL_DEFAULT_USER')
    if not candidate:
        sudo_user = os.environ.get('SUDO_USER', '')
        if sudo_user and sudo_user != 'root':
            candidate = sudo_user
    if not candidate:
        try:
            if os.geteuid() != 0:
                candidate = pwd.getpwuid(os.geteuid()).pw_name
        except (AttributeError, KeyError, OSError):
            pass
    if not candidate or candidate == 'root':
        return
    try:
        home = pwd.getpwnam(candidate).pw_dir
    except KeyError:
        home = str(Path('/Users' if IS_MACOS else '/home') / candidate)
    env.setdefault('SHELLMCP_DEFAULT_USER', candidate)
    env.setdefault('SHELLMCP_DEFAULT_HOME', home)
    env.setdefault('SHELLMCP_DEFAULT_CWD', home)


def env_remove_keys(keys: list[str] | str, *additional: str):
    """Atomically remove transient secret values from the persistent env file."""
    if isinstance(keys, str):
        keys = [keys, *additional]
    else:
        keys = [*keys, *additional]
    cur = env_read()
    changed = False
    for key in keys:
        if key in cur:
            cur.pop(key, None)
            changed = True
    if changed:
        lines = [f'{k}={cur[k]}' for k in sorted(cur.keys())]
        ENV_FILE.parent.mkdir(parents=True, exist_ok=True)
        tmp = ENV_FILE.with_name(f'.{ENV_FILE.name}.{os.getpid()}.tmp')
        try:
            tmp.write_text('\n'.join(lines) + '\n', encoding='utf-8')
            os.chmod(tmp, 0o600)
            os.replace(tmp, ENV_FILE)
        finally:
            tmp.unlink(missing_ok=True)

def env_bool(env: dict, key: str, default: bool = False) -> bool:
    raw = env.get(key)
    if raw is None or str(raw).strip() == '':
        return default
    return str(raw).strip().lower() in {'1', 'true', 'yes', 'on'}


def auto_update_enabled(env: dict) -> bool:
    # Автообновление GPTAdmin включено по умолчанию. Отключается явно:
    # GPTADMIN_AUTO_UPDATE=false или `gptadmin auto-update disable`.
    return env_bool(env, 'GPTADMIN_AUTO_UPDATE', True)


def auto_update_interval_seconds(env: dict) -> int:
    try:
        value = int(str(env.get('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC') or '21600').strip())
    except Exception:
        value = 21600
    return max(value, 300)


def auto_update_randomized_delay_seconds(env: dict) -> int:
    try:
        value = int(str(env.get('GPTADMIN_AUTO_UPDATE_RANDOM_DELAY_SEC') or '1800').strip())
    except Exception:
        value = 1800
    return max(value, 0)


# tokens

def gen_hex(nbytes=16) -> str:
    try:
        return secrets.token_hex(nbytes)
    except Exception:
        out = run(['openssl', 'rand', '-hex', str(nbytes)], capture=True)
        return out.stdout.strip()

def gen_subdomain() -> str:
    return f"u-{gen_hex(4)}"

# network

def first_ip() -> str:
    try:
        hostname = socket.gethostname()
        ips = socket.gethostbyname_ex(hostname)[2]
        for ip in ips:
            if ip and ip != '127.0.0.1':
                return ip
    except Exception:
        pass
    return '127.0.0.1'

# http(s) URL validator
HTTPS_RE = re.compile(r'^https://[A-Za-z0-9._\-]+(:\d+)?(/.*)?$')

def ensure_https(url: str):
    if not HTTPS_RE.match(url or ''):
        die('Нужен корректный HTTPS URL (например, https://gptadmin.example.com)')

# download & extract

def _local_source_path(source: str) -> Path | None:
    """Resolve a package/matrix source to a local path when applicable.

    Plain filesystem paths and file:// URLs are supported. HTTP(S) and other
    schemes remain remote sources. Relative paths are resolved against cwd.
    """
    value = str(source or '').strip()
    if not value:
        return None
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme == 'file':
        if parsed.netloc not in {'', 'localhost'}:
            return None
        return Path(urllib.request.url2pathname(parsed.path)).expanduser().resolve()
    if parsed.scheme:
        return None
    return Path(value).expanduser().resolve()


def download(url: str, dest: Path):
    """Download or copy one release artifact into ``dest``.

    Local filesystem paths and file:// URLs are copied directly; HTTP(S) uses
    curl with its progress meter visible. Set GPTADMIN_DOWNLOAD_QUIET=1 to keep
    the old silent network-download behavior.
    """
    local = _local_source_path(url)
    if local is not None:
        if not local.is_file():
            raise subprocess.CalledProcessError(1, ['local-copy', str(local)])
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(local, dest)
        print(f'  Локальный файл: {local}', flush=True)
    else:
        quiet = os.environ.get('GPTADMIN_DOWNLOAD_QUIET', '').strip().lower() in {'1', 'true', 'yes', 'on'}
        if quiet:
            cmd = ['curl', '-fsSL', url, '-o', str(dest)]
        else:
            print(f'  URL: {url}', flush=True)
            cmd = ['curl', '-fL', url, '-o', str(dest)]
        run(cmd)
    try:
        size = dest.stat().st_size
    except OSError:
        size = 0
    if size:
        print(f'  Готово: {size / (1024 * 1024):.1f} MiB -> {dest}', flush=True)

def extract_tgz(tgz_path: Path, target_dir: Path):
    with tarfile.open(tgz_path, 'r:gz') as tar:
        tar.extractall(path=target_dir)

# package install

def _install_cli_executable_from_file(src: Path):
    if not src.exists() or not src.is_file():
        return
    try:
        candidate_text = src.read_text(encoding='utf-8', errors='replace')
        current_text = CLI_PATH.read_text(encoding='utf-8', errors='replace') if CLI_PATH.exists() else ''
        # Do not let older component packages downgrade a CLI that already knows
        # about the new automatic updater. Fresh/future packages with cmd_autoupdate
        # still update the executable normally.
        if 'cmd_autoupdate' in current_text and 'cmd_autoupdate' not in candidate_text:
            print(f'WARNING: package CLI {src} is older than installed CLI; keeping {CLI_PATH}', file=sys.stderr)
            return
    except Exception as exc:
        print(f'WARNING: could not inspect package CLI {src}: {exc}; keeping installed CLI', file=sys.stderr)
        return
    CLI_PATH.parent.mkdir(parents=True, exist_ok=True)
    tmp = CLI_PATH.with_name(CLI_PATH.name + '.new')
    shutil.copy2(src, tmp)
    os.chmod(tmp, 0o755)
    os.replace(tmp, CLI_PATH)


def _copy_pkg_runtime_payloads(tdp: Path):
    cli_src = tdp / 'cli'
    if cli_src.exists():
        _install_cli_executable_from_file(cli_src / 'gptadmin.py')
        cli_dst = INSTALL_DIR / 'cli'
        if cli_dst.exists():
            shutil.rmtree(cli_dst, ignore_errors=True)
        shutil.copytree(cli_src, cli_dst)
    agents_src = tdp / 'agents'
    if agents_src.exists():
        agents_dst = INSTALL_DIR / 'agents'
        if agents_dst.exists():
            shutil.rmtree(agents_dst, ignore_errors=True)
        shutil.copytree(agents_src, agents_dst)
    client_src = tdp / 'client'
    if client_src.exists():
        client_dst = INSTALL_DIR / 'client'
        if client_dst.exists():
            shutil.rmtree(client_dst, ignore_errors=True)
        shutil.copytree(client_src, client_dst)
    public_src = tdp / 'public'
    admin_src = public_src / 'admin'
    if admin_src.exists():
        public_dst = INSTALL_DIR / 'public'
        public_dst.mkdir(parents=True, exist_ok=True)
        admin_dst = public_dst / 'admin'
        staged_admin = public_dst / '.admin-react.new'
        if staged_admin.exists():
            shutil.rmtree(staged_admin, ignore_errors=True)
        shutil.copytree(admin_src, staged_admin)
        if admin_dst.exists():
            shutil.rmtree(admin_dst, ignore_errors=True)
        os.replace(staged_admin, admin_dst)
        legacy_src = public_src / 'admin-legacy'
        if legacy_src.exists():
            legacy_dst = public_dst / 'admin-legacy'
            staged_legacy = public_dst / '.admin-legacy.new'
            if staged_legacy.exists():
                shutil.rmtree(staged_legacy, ignore_errors=True)
            shutil.copytree(legacy_src, staged_legacy)
            if legacy_dst.exists():
                shutil.rmtree(legacy_dst, ignore_errors=True)
            os.replace(staged_legacy, legacy_dst)
        for sibling in public_src.iterdir():
            if sibling.name in {'admin', 'admin-legacy'}:
                continue
            target = public_dst / sibling.name
            if sibling.is_dir():
                staged = public_dst / f'.{sibling.name}.new'
                if staged.exists():
                    shutil.rmtree(staged, ignore_errors=True)
                shutil.copytree(sibling, staged)
                if target.exists():
                    shutil.rmtree(target, ignore_errors=True)
                os.replace(staged, target)
            elif sibling.is_file():
                shutil.copy2(sibling, target)


def _arch_tag() -> str:
    machine = (os.uname().machine if hasattr(os, 'uname') else '').lower()
    if machine in {'arm64', 'aarch64'}:
        return 'arm64'
    if machine in {'x86_64', 'amd64'}:
        return 'amd64'
    return machine or 'unknown'


def _release_platform_tag() -> str:
    return 'macos' if IS_MACOS else 'ubuntu'


def _release_arch_tag() -> str:
    arch = _arch_tag()
    if arch == 'amd64':
        return 'x64'
    if arch == 'arm64':
        return 'arm64'
    return arch


def platform_pkg_url_default(edition: str = 'full') -> str:
    if edition not in {'full', 'client'}:
        raise ValueError(f'unsupported release edition: {edition}')
    return os.environ.get(
        'PKG_PLATFORM_URL',
        f'{PKG_BASE_URL_DEFAULT}/gptadmin-{_release_platform_tag()}-{_release_arch_tag()}-{edition}.tar.gz',
    )


def _platform_hub_candidates(tdp: Path) -> list[Path]:
    if IS_MACOS:
        arch = _arch_tag()
        tags = [f'darwin_{arch}', f'macos_{arch}']
        # Legacy location is accepted only when the archive itself was built on macOS.
        legacy = [tdp / 'gptadmin_hub' / 'dist' / 'gptadmin_hub']
    else:
        arch = _arch_tag()
        tags = [f'linux_{arch}']
        legacy = [tdp / 'gptadmin_hub' / 'dist' / 'gptadmin_hub', tdp / 'build' / 'gptadmin_hub' / 'dist' / 'gptadmin_hub']
    return [tdp / 'bin' / 'gptadmin-hub'] + [tdp / 'gptadmin_hub' / tag / 'gptadmin_hub' for tag in tags] + legacy


def _binary_looks_native(path: Path) -> bool:
    if not IS_MACOS:
        return True
    try:
        out = run(['/usr/bin/file', str(path)], check=False, capture=True).stdout
    except Exception:
        return True
    return 'Mach-O' in out


def _macos_unquarantine_and_codesign(path: Path):
    if not IS_MACOS:
        return
    # Files extracted from browser/curl-delivered archives can inherit macOS
    # provenance/quarantine metadata. launchd may then kill ad-hoc binaries with
    # OS_REASON_CODESIGNING. Clear xattrs and apply a local ad-hoc signature.
    run(['/usr/bin/xattr', '-cr', str(path)], check=False, timeout=10)
    if _binary_looks_native(path):
        run(['/usr/bin/codesign', '--force', '--sign', '-', str(path)], check=False, timeout=20)


def _install_runtime_binary(src: Path, dst: Path) -> None:
    """Install a runtime binary by atomic replacement.

    Replacing the directory entry is safe while the old executable is running;
    copying directly to ``dst`` is not (Linux returns ETXTBSY/Text file busy).
    """
    dst.parent.mkdir(parents=True, exist_ok=True)
    tmp_name = None
    try:
        with tempfile.NamedTemporaryFile(
            dir=dst.parent, prefix=f'.{dst.name}.', suffix='.new', delete=False
        ) as tmp:
            tmp_name = Path(tmp.name)
        shutil.copy2(src, tmp_name)
        os.chmod(tmp_name, 0o755)
        os.replace(tmp_name, dst)
    finally:
        if tmp_name is not None:
            tmp_name.unlink(missing_ok=True)


def _install_hub_binary_from_pkg(tdp: Path):
    for c in _platform_hub_candidates(tdp):
        if c.exists() and _binary_looks_native(c):
            BIN_DIR.mkdir(parents=True, exist_ok=True)
            dst = BIN_DIR / 'gptadmin_hub'
            _install_runtime_binary(c, dst)
            _macos_unquarantine_and_codesign(dst)
            return
    if IS_MACOS:
        die('macOS gptadmin_hub binary not found in package: expected gptadmin_hub/darwin_arm64/gptadmin_hub or gptadmin_hub/darwin_amd64/gptadmin_hub')
    die('gptadmin_hub binary not found in package')


def _shellmcp_go_binary_candidates(tdp: Path) -> list[Path]:
    arch = _arch_tag()
    if IS_MACOS:
        tags = [f'darwin_{arch}', f'macos_{arch}']
    else:
        tags = [f'linux_{arch}']
    names = ('shellmcp-go', 'rootd-go', 'rootd-go-canary', 'shellmcp')
    roots = (
        tdp / 'go-shellmcp',
        tdp / 'shellmcp-go',
        tdp / 'rootd-go',
        tdp / 'shellmcp',
        tdp / 'build' / 'go-shellmcp',
        tdp / 'build' / 'shellmcp-go',
        tdp / 'build' / 'rootd-go',
    )
    out: list[Path] = [tdp / 'bin' / 'shellmcp']
    for root in roots:
        for tag in tags:
            for name in names:
                out.append(root / tag / name)
        for name in names:
            out.append(root / name)
    out += [tdp / name for name in names]
    return out


def _install_shellmcp_binary_from_pkg(tdp: Path) -> None:
    # ShellMCP is now Go-only (go-shellmcp). Legacy Python/PyInstaller fallback removed.
    for c in _shellmcp_go_binary_candidates(tdp):
        if c.exists() and c.is_file():
            BIN_DIR.mkdir(parents=True, exist_ok=True)
            dst = BIN_DIR / 'shellmcp'
            _install_runtime_binary(c, dst)
            _macos_unquarantine_and_codesign(dst)
            return
    die('Go ShellMCP/rootd binary not found in package. Legacy Python/PyInstaller shellmcp has been removed; ensure the package contains go-shellmcp/<platform>/<arch>/shellmcp-go or rootd-go.')


def _grepmesh_binary_candidates(tdp: Path) -> list[Path]:
    arch = _arch_tag()
    tags = [f'darwin_{arch}', f'macos_{arch}'] if IS_MACOS else [f'linux_{arch}']
    out = [tdp / 'bin' / 'grepmesh-mcp', tdp / 'grepmesh-mcp']
    for tag in tags:
        out.extend([
            tdp / 'grepmesh' / tag / 'grepmesh-mcp',
            tdp / 'build' / 'grepmesh' / tag / 'grepmesh-mcp',
        ])
    return out


def _install_grepmesh_binary_from_pkg(tdp: Path) -> bool:
    """Install the bundled GrepMesh binary when this platform package carries it.

    GrepMesh is a default-on companion capability, but package portability remains
    fail-open: older packages and unsupported cross-builds simply omit the binary.
    Explicit --no-grepmesh controls activation, not package extraction.
    """
    for candidate in _grepmesh_binary_candidates(tdp):
        if candidate.exists() and candidate.is_file() and _binary_looks_native(candidate):
            BIN_DIR.mkdir(parents=True, exist_ok=True)
            dst = BIN_DIR / 'grepmesh-mcp'
            _install_runtime_binary(candidate, dst)
            _macos_unquarantine_and_codesign(dst)
            return True
    return False


def _cleanup_obsolete_runtime_files():
    """Remove obsolete replaceable runtime files after an in-place upgrade.

    User configuration, identities, tokens, logs, registry state and MCP config are
    intentionally preserved. Only old executable aliases, interrupted .new files
    and binary backup artifacts are removed.
    """
    obsolete = [
        BIN_DIR / 'rootd-go',
        BIN_DIR / 'rootd-go-canary',
        BIN_DIR / 'shellmcp-go',
        BIN_DIR / 'gptadmin_hub.py',
        BIN_DIR / 'shellmcp.py',
        BIN_DIR / 'shellmcp_pure.py',
        CLI_PATH.with_name(CLI_PATH.name + '.new'),
    ]
    for path in obsolete:
        try:
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
        except FileNotFoundError:
            pass
    if not IS_MACOS:
        # Older Go-canary installs overrode both ExecStart and EnvironmentFile,
        # leaving Hub and ShellMCP on different token sources after rotation.
        # The canonical unit now reads only gptadmin.env; preserve unrelated
        # drop-ins such as spool permissions and auto-update settings.
        dropin_dir = SYSTEMD_DIR / f'{SYSTEMD_SHELLMCP}.d'
        for obsolete_dropin in dropin_dir.glob('*go-primary*.conf'):
            # Older installers used different numeric prefixes; any matching
            # override can replace the canonical gptadmin.env credential.
            obsolete_dropin.unlink(missing_ok=True)
        # Older installs could pin ShellMCP itself to the operator account.
        # The paired file:<host> target now needs a privileged filesystem boundary
        # in system mode.  ShellMCP therefore runs as root while shell_exec still
        # defaults to SHELLMCP_DEFAULT_USER and only elevates when explicitly asked.
        legacy_user_mode = dropin_dir / '100-gptadmin-user-mode.conf'
        legacy_system_dropins = (
            legacy_user_mode,
            dropin_dir / '110-user-mode-no-self-update.conf',
            dropin_dir / '120-config-dir-access.conf',
            dropin_dir / 'zz-user-mode-no-self-update.conf',
        )
        if not IS_USER_INSTALL:
            for legacy_dropin in legacy_system_dropins:
                try:
                    legacy_dropin.unlink(missing_ok=True)
                except OSError as exc:
                    print_warn(f'Could not retire legacy ShellMCP drop-in {legacy_dropin.name}: {exc}')
    # Do not delete ad-hoc *.bak/*.old/*.new files here. They may be operator
    # rollback artifacts unrelated to this update. Managed checkpoint/backup GC
    # is explicit and scoped to its own store.


def install_component_from_pkg(pkg_tgz: Path, component: str):
    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        extract_tgz(pkg_tgz, tdp)
        _copy_pkg_runtime_payloads(tdp)
        if component == 'hub':
            _install_hub_binary_from_pkg(tdp)
            return
        if component == 'shellmcp':
            _install_shellmcp_binary_from_pkg(tdp)
            _install_grepmesh_binary_from_pkg(tdp)
            return
        die(f'unknown component: {component}')

# ===== Service management =====

if IS_MACOS:
    def _plist_path(label: str) -> Path:
        return SERVICES_DIR / f'{label}.plist'

    def _wrapper_script(name: str, bin_path: Path) -> Path:
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        script = BIN_DIR / f'run_{name}.sh'
        script.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'exec {bin_path}\n'
        )
        os.chmod(script, 0o755)
        return script

    def _make_plist(label: str, wrapper: Path, log_file: Path) -> str:
        # Long-running launchd service plist (hub / shellmcp / frpc / cloudflared):
        # KeepAlive=true so launchd respawns the job if it exits, and RunAtLoad
        # so it boots with the system. The auto-update path does NOT use this
        # template — see _plist_oneshot at module level for oneshot semantics.
        return (
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"'
            ' "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n'
            '<plist version="1.0"><dict>\n'
            f'    <key>Label</key><string>{label}</string>\n'
            '    <key>ProgramArguments</key><array>\n'
            '        <string>/bin/sh</string>\n'
            f'        <string>{wrapper}</string>\n'
            '    </array>\n'
            '    <key>RunAtLoad</key><true/>\n'
            '    <key>KeepAlive</key><true/>\n'
            f'    <key>StandardOutPath</key><string>{log_file}</string>\n'
            f'    <key>StandardErrorPath</key><string>{log_file}</string>\n'
            '</dict></plist>\n'
        )

    def _make_interval_plist(label: str, wrapper: Path, log_file: Path, interval: int) -> str:
        # Kept as a thin wrapper for backwards compatibility with any external
        # caller. New auto-update code calls module-level _plist_oneshot
        # directly so the template is testable on Linux.
        return _plist_oneshot(label, wrapper, log_file, interval=interval)

    def svc_daemon_reload():
        pass  # launchd has no daemon-reload

    def svc_enable(label: str, unit_path: Path):
        # Load the (possibly rewritten) plist into launchd WITHOUT starting
        # the job. This is the right primitive for "I just changed the plist
        # config — pick up the new file but do not run the job now." Used by:
        #   * write_autoupdate_unit (install-time registration; we want the
        #     job loaded so manual kickstart works, but we do NOT want a
        #     surprise update to fire at install time).
        #   * timer_disable (user said "stop periodic updates" — we rewrite
        #     the plist without StartInterval; loading it must not run the
        #     job because that would violate the user's intent).
        #
        # macOS launchctl load/unload is legacy and can silently fail to
        # restore a LaunchAgent after bootout during in-place update. Prefer
        # bootstrap into the explicit domain, then enable; keep load -w as
        # fallback for older systems.
        domains = _launchd_domains()
        # A missing/unloaded launchd job is normal during update or first install.
        # `launchctl bootout` prints "Boot-out failed: 3: No such process" to
        # stderr in that case; suppress it so a harmless pre-cleanup does not look
        # like an update failure.
        for domain in domains:
            _launchctl_capture(['launchctl', 'bootout', _launchd_service_target(label, domain)])
        for domain in domains:
            bootstrap = _launchctl_capture(['launchctl', 'bootstrap', domain, str(unit_path)])
            if bootstrap.returncode != 0 and not _launchd_is_loaded(label):
                _launchctl_capture(['launchctl', 'remove', label])
                time.sleep(0.2)
                _launchctl_capture(['launchctl', 'bootstrap', domain, str(unit_path)])
            if _launchd_is_loaded(label):
                break
        _launchctl_capture(['launchctl', 'enable', _launchd_loaded_target(label) or _launchd_service_target(label)])
        if not _launchd_is_loaded(label):
            run(['launchctl', 'load', '-w', str(unit_path)], check=False)
        if not _launchd_is_loaded(label):
            raise RuntimeError(f'launchd service did not load: {_launchd_service_target(label)}')

    def svc_enable_start(label: str, unit_path: Path):
        # Load the plist AND kickstart it. Used for long-running services
        # (hub / shellmcp / frpc / cloudflared) where RunAtLoad + KeepAlive
        # semantics mean "bootstrap should also start the job now." Do NOT
        # use this for oneshot plists whose purpose is to be triggered only
        # on explicit kickstart (e.g., auto-update) — calling it there
        # would fire one unintended run per config reload.
        svc_enable(label, unit_path)
        # kickstart can block for long-running LaunchAgents on some macOS
        # versions; keep kickstart as silent best-effort only.
        try:
            subprocess.run(
                ['launchctl', 'kickstart', '-k', _launchd_loaded_target(label) or _launchd_service_target(label)],
                check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=int(os.environ.get('GPTADMIN_LAUNCHCTL_KICKSTART_TIMEOUT', '2')),
            )
        except subprocess.TimeoutExpired:
            pass

    def svc_restart(label: str, unit_path: Path):
        svc_disable_stop(label, unit_path)
        svc_enable_start(label, unit_path)

    def _launchd_uid() -> str:
        if IS_USER_INSTALL:
            sudo_uid = os.environ.get('SUDO_UID')
            if sudo_uid and sudo_uid != '0':
                return sudo_uid
            try:
                return str(os.getuid())
            except Exception:
                return '0'
        return ''

    def _launchd_domain() -> str:
        return f'gui/{_launchd_uid()}' if IS_USER_INSTALL else 'system'

    def _launchd_domains() -> list[str]:
        """Return interactive and SSH launchd domains for a user install."""
        primary = _launchd_domain()
        if not IS_USER_INSTALL:
            return [primary]
        fallback = f'user/{_launchd_uid()}'
        return [primary] if fallback == primary else [primary, fallback]

    def _launchctl_capture(args: list[str]) -> subprocess.CompletedProcess:
        return subprocess.run(args, check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)

    def _launchd_service_target(label: str, domain: str | None = None) -> str:
        return f'{domain or _launchd_domain()}/{label}'

    def _launchd_loaded_target(label: str) -> str:
        for domain in _launchd_domains():
            target = _launchd_service_target(label, domain)
            if _launchctl_capture(['launchctl', 'print', target]).returncode == 0:
                return target
        return ''

    def _launchd_is_loaded(label: str) -> bool:
        return bool(_launchd_loaded_target(label))

    def _launchd_stop(label: str, unit_path: Path) -> tuple[bool, list[str]]:
        attempts = [['launchctl', 'bootout', _launchd_service_target(label, domain)] for domain in _launchd_domains()]
        attempts.append(['launchctl', 'remove', label])
        if unit_path.exists():
            for domain in reversed(_launchd_domains()):
                attempts.insert(1, ['launchctl', 'bootout', domain, str(unit_path)])
            attempts.append(['launchctl', 'unload', '-w', str(unit_path)])
        messages: list[str] = []
        for cmd in attempts:
            res = _launchctl_capture(cmd)
            if res.returncode != 0 and res.stdout.strip():
                messages.append('$ ' + ' '.join(cmd) + '\n' + res.stdout.strip())
        loaded = _launchd_is_loaded(label)
        if loaded and not messages:
            messages.append(f'launchd service is still loaded: {_launchd_service_target(label)}')
        return (not loaded), messages

    def svc_disable_stop(label: str, unit_path: Path):
        ok, messages = _launchd_stop(label, unit_path)
        if not ok:
            print(f'WARN: не удалось выгрузить launchd service {label}', file=sys.stderr)
            for msg in messages[-3:]:
                print(msg, file=sys.stderr)
        return ok

    def svc_status_multi(labels_and_paths):
        for label, path in labels_and_paths:
            if not path.exists():
                print(f'  {c_bold(label):<40} {c_red("● missing")}')
                continue
            r = _launchctl_capture(['launchctl', 'list', label])
            out = r.stdout or ''
            pid = '0'
            status = 'not loaded' if r.returncode != 0 else 'unknown'
            for line in out.splitlines():
                stripped = line.strip()
                if stripped.startswith('"PID"'):
                    pid = stripped.split('=')[-1].strip().rstrip(';')
                elif stripped.startswith('"state"'):
                    status = stripped.split('=')[-1].strip().strip('"').rstrip(';')
            if status == 'unknown' and pid not in ('', '0'):
                status = 'running'
            if status == 'running':
                status_str = c_green('● running')
            elif status in ('exited',):
                status_str = c_yellow('● exited')
            elif status in ('not loaded',):
                status_str = c_red('● not loaded')
            else:
                status_str = c_yellow('● ' + status)
            print(f'  {c_bold(label):<40} {status_str}  PID {c_dim(pid):<8}')

    def svc_start_multi(labels_and_paths):
        for label, path in labels_and_paths:
            if path.exists():
                svc_enable_start(label, path)

    def svc_stop_multi(labels_and_paths):
        for label, path in reversed(labels_and_paths):
            if path.exists() or _launchd_is_loaded(label):
                svc_disable_stop(label, path)

    def svc_logs_one(_label: str, log_file: Path):
        if log_file.exists():
            run(['tail', '-n', '200', '-f', str(log_file)], check=False)
        else:
            print(f'Лог-файл не найден: {log_file}')

    def svc_logs_all(labels_paths_logs):
        for _, _, log_file in labels_paths_logs:
            if log_file and log_file.exists():
                run(['tail', '-n', '200', '-f', str(log_file)], check=False)

    def write_hub_unit(install_hub: bool, _install_shellmcp: bool):
        if not install_hub:
            return
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = _wrapper_script('hub', BIN_DIR / 'gptadmin_hub')
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_HUB.write_text(_make_plist(SVC_HUB_LABEL, wrapper, LOG_DIR / 'hub.log'))

    def write_shellmcp_unit(_install_hub: bool, install_shellmcp: bool):
        if not install_shellmcp:
            return
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = _wrapper_script('shellmcp', BIN_DIR / 'shellmcp')
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_SHELLMCP.write_text(_make_plist(SVC_SHELLMCP_LABEL, wrapper, LOG_DIR / 'shellmcp.log'))

    def write_grepmesh_unit(enabled: bool, env: dict):
        if not enabled or not (BIN_DIR / 'grepmesh-mcp').exists():
            return
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = _wrapper_script('grepmesh', BIN_DIR / 'grepmesh-mcp')
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'exec {BIN_DIR / "grepmesh-mcp"} --config {GREPMESH_CONFIG_FILE}\n'
        )
        os.chmod(wrapper, 0o755)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_GREPMESH.write_text(_make_plist(SVC_GREPMESH_LABEL, wrapper, LOG_DIR / 'grepmesh.log'))

    def svc_grepmesh_name(): return SVC_GREPMESH_LABEL

    def write_frpc_unit(frpc_bin: str):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_frpc_all.sh'
        wrapper.write_text(frpc_wrapper_script(frpc_bin, env_read()))
        os.chmod(wrapper, 0o755)
        UNIT_PATH_FRPC.write_text(_make_plist(SVC_FRPC_LABEL, wrapper, LOG_DIR / 'frpc.log'))


    def write_cloudflared_unit(cloudflared_bin: str, env: dict):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_cloudflared.sh'
        local_url = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'exec {cloudflared_bin} tunnel --protocol http2 --url {local_url} --no-autoupdate\n'
        )
        os.chmod(wrapper, 0o755)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_CLOUDFLARED.write_text(_make_plist(SVC_CLOUDFLARED_LABEL, wrapper, LOG_DIR / 'cloudflared.log'))


    def write_autoupdate_unit(env: dict):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        BIN_DIR.mkdir(parents=True, exist_ok=True)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_auto_update.sh'
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'exec {CLI_PATH} --{INSTALL_SCOPE} update --auto\n'
        )
        os.chmod(wrapper, 0o755)
        # Always present, oneshot-style: RunAtLoad/KeepAlive are false so the
        # job does nothing on its own. StartInterval is omitted entirely so we
        # never accidentally auto-run until timer_enable rewrites the plist
        # with an interval. Keep the wrapper around for both periodic and
        # manual `launchctl kickstart` invocations.
        UNIT_PATH_AUTO_UPDATE.write_text(
            _plist_oneshot(SVC_AUTO_UPDATE_LABEL, wrapper, LOG_DIR / 'auto-update.log'))
        # Register the job with launchd so manual `launchctl kickstart` works
        # on a fresh install. We use the LOAD-ONLY path (svc_enable, no
        # kickstart) on purpose: a freshly installed auto-update must not
        # fire on its own. The first kick only happens when the user clicks
        # "update now" or when timer_enable rewrites the plist with a
        # StartInterval and explicitly kicks once.
        svc_enable(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)

    def svc_hub_name():  return SVC_HUB_LABEL
    def svc_shellmcp_name(): return SVC_SHELLMCP_LABEL
    def svc_frpc_name():  return SVC_FRPC_LABEL
    def svc_cloudflared_name(): return SVC_CLOUDFLARED_LABEL

    def timer_enable(timer_unit: str):
        env = env_read()
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        BIN_DIR.mkdir(parents=True, exist_ok=True)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_auto_update.sh'
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'exec {CLI_PATH} --{INSTALL_SCOPE} update --auto\n'
        )
        os.chmod(wrapper, 0o755)
        # Rewrite the plist WITH StartInterval so launchd schedules the
        # periodic trigger, then (re)load via the existing helper. The job
        # was previously either absent or loaded without StartInterval; the
        # enable_start path handles bootout + bootstrap so the new plist
        # content is picked up.
        UNIT_PATH_AUTO_UPDATE.write_text(
            _plist_oneshot(SVC_AUTO_UPDATE_LABEL, wrapper, LOG_DIR / 'auto-update.log',
                           interval=auto_update_interval_seconds(env)))
        svc_enable_start(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)

    def timer_disable(timer_unit: str):
        # Rewrite the plist WITHOUT StartInterval so launchd stops scheduling
        # periodic runs. Keep the job loaded (do NOT call svc_disable_stop
        # here) so manual `launchctl kickstart` invocations still work.
        UNIT_PATH_AUTO_UPDATE.write_text(
            _plist_oneshot(SVC_AUTO_UPDATE_LABEL, BIN_DIR / 'run_auto_update.sh',
                           LOG_DIR / 'auto-update.log'))
        # Reload to pick up the StartInterval removal. Use the LOAD-ONLY
        # path (svc_enable, no kickstart) on purpose: the user just said
        # "stop periodic updates" — booting the new plist must NOT fire an
        # update. The job is still loaded afterwards; only its schedule
        # changed.
        svc_enable(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)

    def timer_status(timer_unit: str, timer_path: Path):
        if not _launchd_is_loaded(SVC_AUTO_UPDATE_LABEL):
            print(f'  LaunchAgent {SVC_AUTO_UPDATE_LABEL} not loaded')
            return
        # Distinguish periodic vs manual mode by reading the StartInterval
        # key from the on-disk plist. Periodic = enabled, no key = disabled
        # but still present (manual kickstart still works).
        on_disk = UNIT_PATH_AUTO_UPDATE
        has_interval = False
        if on_disk.exists():
            try:
                plist_xml = on_disk.read_text()
                has_interval = '<key>StartInterval</key>' in plist_xml
            except Exception:
                pass
        mode_str = c_green('● enabled (periodic)') if has_interval else c_yellow('● disabled (manual kickstart)')
        print(f'  {c_bold(SVC_AUTO_UPDATE_LABEL):<40} {mode_str}')

else:
    # Linux systemd. In user mode this uses systemd --user and ~/.config/systemd/user.
    LINUX_WANTED_BY = 'default.target' if IS_USER_INSTALL else 'multi-user.target'

    def linux_systemd_hardening(mode: str, custom: dict | None = None) -> str:
        """Return systemd restrictions for an explicit security profile.

        The normal profile is intentionally frictionless.  Stronger process
        isolation is opt-in so it cannot silently invalidate ShellMCP's
        explicit privileged execution contract.
        """
        if str(mode).strip().lower() == 'normal':
            return ''
        normalized = str(mode).strip().lower()
        if normalized not in {'maximum', 'custom'}:
            raise ValueError('security mode must be normal, maximum or custom')
        flags = {
            'no_new_privileges': normalized == 'maximum',
            'private_tmp': normalized == 'maximum',
            'protect_system': normalized == 'maximum',
            'protect_home': normalized == 'maximum',
        }
        if normalized == 'custom':
            for key in flags:
                if custom and key in custom:
                    flags[key] = bool(custom[key])
            if custom and custom.get('allow_privileged_execution') is False and not flags['no_new_privileges']:
                raise ValueError('custom profile denying privileged execution requires no_new_privileges')
        lines = []
        if flags['no_new_privileges']:
            lines.append('NoNewPrivileges=true')
        if flags['private_tmp']:
            lines.append('PrivateTmp=true')
        if flags['protect_system']:
            lines.append('ProtectSystem=full')
        if flags['protect_home']:
            lines.append('ProtectHome=true')
        if flags['protect_system'] or flags['protect_home']:
            lines.append(f'ReadWritePaths={ETC_DIR} {INSTALL_DIR} {Path.home() / ".gptadmin"}')
        return '\n'.join(lines) + ('\n' if lines else '')

    def configured_process_security_profile() -> tuple[str, dict]:
        """Read the operator profile persisted by the Hub security API."""
        env_mode = os.environ.get('GPTADMIN_SECURITY_MODE', '').strip().lower()
        mode = env_mode or 'normal'
        custom = {}
        profile_path = ETC_DIR / 'security_state.json'
        try:
            persisted = json.loads(profile_path.read_text(encoding='utf-8'))
            profile = persisted.get('process_profile') or {}
            if isinstance(profile, dict) and profile.get('mode'):
                # An explicit non-normal environment mode is a CLI override;
                # otherwise the admin-configured profile is authoritative.
                if env_mode not in {'maximum', 'custom'}:
                    mode = str(profile.get('mode')).strip().lower()
                custom = profile
        except (OSError, ValueError, TypeError):
            pass
        return mode, custom

    LINUX_SECURITY_MODE, LINUX_SECURITY_CUSTOM = configured_process_security_profile()
    LINUX_HARDENING = '' if IS_USER_INSTALL else linux_systemd_hardening(LINUX_SECURITY_MODE, LINUX_SECURITY_CUSTOM)

    def process_hardening_for_env(env: dict | None = None) -> str:
        """Render the selected profile at unit-write time, not import time."""
        if IS_USER_INSTALL:
            return ''
        if env and str(env.get('GPTADMIN_SECURITY_MODE', '')).strip().lower() in {'maximum', 'custom'}:
            mode = str(env['GPTADMIN_SECURITY_MODE']).strip().lower()
            _, persisted = configured_process_security_profile()
            return linux_systemd_hardening(mode, persisted)
        mode, custom = configured_process_security_profile()
        return linux_systemd_hardening(mode, custom)

    def render_unit_with_hardening(unit: str, hardening: str) -> str:
        """Replace the import-time profile without corrupting normal units."""
        if LINUX_HARDENING:
            unit = unit.replace(LINUX_HARDENING, '', 1)
        return unit.replace('\n[Install]', f'\n{hardening}[Install]', 1)

    UNIT_HUB = f"""
[Unit]
Description=GPTAdmin Hub Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
ExecStart={BIN_DIR}/gptadmin_hub
Restart=always
RestartSec=3
{LINUX_HARDENING}
[Install]
WantedBy={LINUX_WANTED_BY}
"""

    UNIT_HUB_STANDBY = f"""
[Unit]
Description=GPTAdmin Hub Standby for zero-downtime handover
After=network-online.target {SYSTEMD_HUB}
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
Environment=GPTADMIN_HUB_HOST=127.0.0.1
Environment=GPTADMIN_HUB_PORT=19001
Environment=HUB_PORT=19001
Environment=HUB_URL=http://127.0.0.1:19001
ExecStart=/usr/bin/env GPTADMIN_HUB_HOST=127.0.0.1 GPTADMIN_HUB_PORT=19001 HUB_PORT=19001 HUB_URL=http://127.0.0.1:19001 {BIN_DIR}/gptadmin_hub
Restart=always
RestartSec=2
{LINUX_HARDENING}
[Install]
WantedBy={LINUX_WANTED_BY}
"""

    UNIT_SHELLMCP = f"""
[Unit]
Description=GPTAdmin Shell MCP Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
ExecStart={BIN_DIR}/shellmcp
Restart=always
RestartSec=3
{LINUX_HARDENING}
[Install]
WantedBy={LINUX_WANTED_BY}
"""

    GREPMESH_UNIT_TPL = """[Unit]
Description=GPTAdmin GrepMesh search MCP
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
{user_line}{group_line}Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart={grepmesh_bin} --config {config_file}
Restart=always
RestartSec=3

[Install]
WantedBy={wanted_by}
"""

    FRPC_UNIT_TPL = """[Unit]
Description=FRP client for GPTAdmin
BindsTo=gptadmin-hub.service
After=network-online.target gptadmin-hub.service
Wants=network-online.target

[Service]
Type=simple
ExecStart={frpc_bin}
Restart=always
RestartSec=3
{hardening}
[Install]
WantedBy={wanted_by}
"""

    CLOUDFLARED_UNIT_TPL = """[Unit]
Description=Cloudflare Quick Tunnel for GPTAdmin Hub
After=network-online.target gptadmin-hub.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={env_file}
ExecStart={cloudflared_bin} tunnel --protocol http2 --url http://127.0.0.1:{hub_port} --no-autoupdate
Restart=always
RestartSec=3
{hardening}
[Install]
WantedBy={wanted_by}
"""


    AUTO_UPDATE_SERVICE_TPL = """[Unit]
Description=GPTAdmin automatic update runner
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile={env_file}
ExecStart={cli_path} --{install_scope} update --auto
"""

    AUTO_UPDATE_TIMER_TPL = """[Unit]
Description=Run GPTAdmin automatic update periodically

[Timer]
OnBootSec=5min
OnUnitActiveSec={interval_sec}s
RandomizedDelaySec={random_delay_sec}s
Persistent=true
Unit={service_name}

[Install]
WantedBy=timers.target
"""

    def _systemctl_cmd(*args: str) -> list[str]:
        return ['systemctl', '--user', *args] if IS_USER_INSTALL else ['systemctl', *args]

    def _journalctl_cmd(*args: str) -> list[str]:
        return ['journalctl', '--user', *args] if IS_USER_INSTALL else ['journalctl', *args]

    def svc_daemon_reload():
        run(_systemctl_cmd('daemon-reload'))

    def svc_enable_start(name: str, _unit_path: Path):
        run(_systemctl_cmd('enable', name))
        run(_systemctl_cmd('restart', name))

    def svc_restart(name: str, _unit_path: Path):
        run(_systemctl_cmd('restart', name))

    def svc_disable_stop(name: str, _unit_path: Path):
        run(_systemctl_cmd('disable', '--now', name), check=False)

    def svc_status_multi(names_and_paths):
        names = [n for n, p in names_and_paths if p.exists()]
        if not names:
            return
        for name in names:
            is_active = run(_systemctl_cmd('is-active', name), check=False)
            active = is_active.stdout.strip() if is_active.stdout else 'unknown'
            is_enabled = run(_systemctl_cmd('is-enabled', name), check=False)
            enabled = is_enabled.stdout.strip() if is_enabled.stdout else 'unknown'
            pid_out = run(_systemctl_cmd('show', name, '--property=MainPID', '--value'), check=False)
            pid = pid_out.stdout.strip() if pid_out.stdout else '0'
            if active == 'active':
                status_str = c_green('● active')
            elif active in ('inactive', 'deactivating'):
                status_str = c_red('● inactive')
            elif active in ('failed',):
                status_str = c_red('● failed')
            else:
                status_str = c_yellow('● ' + active)
            print(f'  {c_bold(name):<40} {status_str}  PID {c_dim(pid):<8} {c_dim("enabled" if enabled == "enabled" else enabled)}')

    def svc_start_multi(names_and_paths):
        names = [n for n, p in names_and_paths if p.exists()]
        if names:
            run(_systemctl_cmd('start', *names))

    def svc_stop_multi(names_and_paths):
        names = [n for n, p in reversed(names_and_paths) if p.exists()]
        if names:
            run(_systemctl_cmd('stop', *names))

    def svc_logs_one(name: str, _log_file=None):
        run(_journalctl_cmd('-u', name, '-e', '-n', '200', '-f'), check=False)

    def svc_logs_all(names_paths_logs):
        names = [n for n, p, _ in names_paths_logs if p.exists()]
        if names:
            run(_journalctl_cmd(*sum([['-u', u] for u in names], []), '-e', '-n', '200', '-f'), check=False)
        else:
            print('Журналы пусты: сервисы не установлены.')

    def _write_handover_helper():
        helper = BIN_DIR / 'gptadmin-handover'
        helper.write_text('''#!/bin/bash
set -euo pipefail
ACTION=${1:-restart-primary}
STATE=${GPTADMIN_HANDOVER_STATE:-/run/gptadmin-handover.json}
UPSTREAM=${GPTADMIN_HANDOVER_UPSTREAM_FILE:-/etc/nginx/conf.d/gptadmin-hub-upstream.conf}
PRIMARY_PORT=${GPTADMIN_HUB_PORT:-${HUB_PORT:-9001}}
STANDBY_PORT=${GPTADMIN_HANDOVER_STANDBY_PORT:-19001}
DRAIN_SECONDS=${GPTADMIN_HANDOVER_DRAIN_SECONDS:-65}
write_state(){ printf '{"status":"%s","action":"%s","ts":%s,"detail":"%s"}\n' "$1" "$ACTION" "$(date +%s)" "${2:-}" > "$STATE"; }
health(){ curl -fsS --max-time 2 "$1/healthz" >/dev/null; }
write_upstream(){
  local primary=$1 backup=$2 tmp
  test -d "$(dirname "$UPSTREAM")"
  tmp=$(mktemp "$(dirname "$UPSTREAM")/.gptadmin-hub-upstream.XXXXXX")
  cat >"$tmp" <<CFG
upstream gptadmin_hub_active {
    zone gptadmin_hub_active 64k;
    server 127.0.0.1:${primary} max_fails=1 fail_timeout=1s;
    server 127.0.0.1:${backup} backup;
    keepalive 64;
}
CFG
  chmod 0644 "$tmp"; mv "$tmp" "$UPSTREAM"; nginx -t; systemctl reload nginx
}
case "$ACTION" in
restart-primary)
  command -v nginx >/dev/null || { write_state failed 'nginx unavailable'; exit 1; }
  test -f "$UPSTREAM" || { write_state failed 'managed nginx upstream missing'; exit 1; }
  systemctl start gptadmin-hub-standby.service
  write_state running 'waiting for standby'
  health "http://127.0.0.1:${STANDBY_PORT}" || { write_state failed 'standby unhealthy'; exit 1; }
  write_upstream "$STANDBY_PORT" "$PRIMARY_PORT"
  sleep "$DRAIN_SECONDS"
  write_state running 'standby active; restarting primary'
  systemctl restart gptadmin-hub.service
  for _ in $(seq 1 100); do health "http://127.0.0.1:${PRIMARY_PORT}" && break; sleep .1; done
  health "http://127.0.0.1:${PRIMARY_PORT}" || { write_state failed 'primary unhealthy after restart'; exit 1; }
  write_upstream "$PRIMARY_PORT" "$STANDBY_PORT"
  write_state completed 'primary healthy and active'
  ;;
*) write_state failed 'unknown action'; exit 2;;
esac
''')
        os.chmod(helper, 0o755)
        return helper

    def write_hub_unit(install_hub: bool, _install_shellmcp: bool):
        if install_hub:
            UNIT_PATH_HUB.parent.mkdir(parents=True, exist_ok=True)
            UNIT_PATH_HUB.write_text(render_unit_with_hardening(UNIT_HUB, process_hardening_for_env(env_read())))
            if not IS_USER_INSTALL:
                UNIT_PATH_HUB_STANDBY.write_text(render_unit_with_hardening(UNIT_HUB_STANDBY, process_hardening_for_env(env_read())))
                _write_handover_helper()

    def write_shellmcp_unit(_install_hub: bool, install_shellmcp: bool):
        if install_shellmcp:
            UNIT_PATH_SHELLMCP.parent.mkdir(parents=True, exist_ok=True)
            UNIT_PATH_SHELLMCP.write_text(render_unit_with_hardening(UNIT_SHELLMCP, process_hardening_for_env(env_read())))

    def write_grepmesh_unit(enabled: bool, env: dict):
        if not enabled or not (BIN_DIR / 'grepmesh-mcp').exists():
            return
        UNIT_PATH_GREPMESH.parent.mkdir(parents=True, exist_ok=True)
        user = '' if IS_USER_INSTALL else (env.get('SHELLMCP_DEFAULT_USER') or env.get('SHELL_DEFAULT_USER') or '')
        UNIT_PATH_GREPMESH.write_text(GREPMESH_UNIT_TPL.format(
            user_line=(f'User={user}\n' if user else ''),
            group_line='',
            grepmesh_bin=BIN_DIR / 'grepmesh-mcp',
            config_file=GREPMESH_CONFIG_FILE,
            wanted_by=LINUX_WANTED_BY,
        ))

    def svc_grepmesh_name(): return SYSTEMD_GREPMESH

    def write_frpc_unit(frpc_bin: str):
        template = FRPC_UNIT_TPL
        if _local_unified_node_enabled():
            template = template.replace('BindsTo=gptadmin-hub.service\n', '').replace(
                'After=network-online.target gptadmin-hub.service\n', 'After=network-online.target\n')
        UNIT_PATH_FRPC.parent.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_frpc_all.sh'
        wrapper.write_text(frpc_wrapper_script(frpc_bin, env_read()))
        os.chmod(wrapper, 0o755)
        UNIT_PATH_FRPC.write_text(template.format(
            frpc_bin=wrapper,
            hardening=process_hardening_for_env(env_read()),
            wanted_by=LINUX_WANTED_BY,
        ))

    def write_cloudflared_unit(cloudflared_bin: str, env: dict):
        UNIT_PATH_CLOUDFLARED.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_CLOUDFLARED.write_text(CLOUDFLARED_UNIT_TPL.format(
            cloudflared_bin=cloudflared_bin, env_file=ENV_FILE, hub_port=env.get('HUB_PORT', '9001'),
            hardening=process_hardening_for_env(env), wanted_by=LINUX_WANTED_BY))


    def write_autoupdate_unit(env: dict):
        UNIT_PATH_AUTO_UPDATE.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_AUTO_UPDATE.write_text(AUTO_UPDATE_SERVICE_TPL.format(
            env_file=ENV_FILE, cli_path=CLI_PATH, install_scope=INSTALL_SCOPE))

    def svc_hub_name():   return SYSTEMD_HUB
    def svc_hub_standby_name(): return SYSTEMD_HUB_STANDBY
    def svc_shellmcp_name(): return SYSTEMD_SHELLMCP
    def svc_frpc_name():  return SYSTEMD_FRPC
    def svc_cloudflared_name(): return SYSTEMD_CLOUDFLARED

    def timer_enable(timer_unit: str):
        UNIT_PATH_AUTO_UPDATE_TIMER.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_AUTO_UPDATE_TIMER.write_text(AUTO_UPDATE_TIMER_TPL.format(
            interval_sec=auto_update_interval_seconds(env_read()),
            random_delay_sec=auto_update_randomized_delay_seconds(env_read()),
            service_name=SYSTEMD_AUTO_UPDATE))
        run(_systemctl_cmd('enable', '--now', timer_unit))

    def timer_disable(timer_unit: str):
        run(_systemctl_cmd('disable', '--now', timer_unit), check=False)
        safe_rm(UNIT_PATH_AUTO_UPDATE_TIMER)

    def timer_status(timer_unit: str, timer_path: Path):
        if not timer_path.exists():
            print(f'  Timer {timer_unit} not found on disk')
            return
        is_active = run(_systemctl_cmd('is-active', timer_unit), check=False)
        active = is_active.stdout.strip() if is_active.stdout else 'unknown'
        is_enabled = run(_systemctl_cmd('is-enabled', timer_unit), check=False)
        enabled = is_enabled.stdout.strip() if is_enabled.stdout else 'unknown'
        next_run = run(_systemctl_cmd('show', timer_unit, '--property=NextElapseUSecMonotonic', '--value'), check=False)
        next_str = next_run.stdout.strip() if next_run.stdout else 'N/A'
        if active == 'active':
            status_str = c_green('● active')
        elif active in ('inactive', 'deactivating'):
            status_str = c_red('● inactive')
        elif active in ('failed',):
            status_str = c_red('● failed')
        else:
            status_str = c_yellow('● ' + active)
        print(f'  {c_bold(timer_unit):<40} {status_str}  {c_dim("enabled" if enabled == "enabled" else enabled)}')
        if next_str != 'N/A':
            print(f'  {c_dim("Next run:")} {c_dim(next_str)}')


# ===== FRP helpers =====

def detect_arch() -> str:
    m = os.uname().machine
    if m in ('x86_64', 'amd64'):
        return 'amd64'
    if m in ('aarch64', 'arm64'):
        return 'arm64'
    if m in ('armv7l', 'armv7'):
        return 'arm'
    die(f'Unsupported arch: {m} (expected x86_64/arm64/armv7)')

def ensure_frpc_installed() -> str:
    existing = shutil.which('frpc')
    if existing:
        return existing
    managed = BIN_DIR / 'frpc'
    if managed.exists() and os.access(managed, os.X_OK):
        return str(managed)

    arch = detect_arch()
    os_name = 'darwin' if IS_MACOS else 'linux'
    tarname = f"frp_{FRPC_VERSION}_{os_name}_{arch}.tar.gz"
    url = f"{FRPC_BASE_URL.rstrip('/')}/{tarname}"

    BIN_DIR.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        pkg = tdp / tarname
        download(url, pkg)
        extract_tgz(pkg, tdp)
        frpc_src = tdp / f"frp_{FRPC_VERSION}_{os_name}_{arch}" / "frpc"
        if not frpc_src.exists():
            die('frpc binary not found in downloaded archive')
        frpc_dst = BIN_DIR / "frpc"
        shutil.copy2(frpc_src, frpc_dst)
        os.chmod(frpc_dst, 0o755)
        return str(frpc_dst)

def _frpc_slug(value: str) -> str:
    slug = re.sub(r'[^a-zA-Z0-9]+', '-', value.strip().lower()).strip('-')
    return slug or 'edge'


def frpc_endpoint_specs(env: dict) -> list[dict]:
    """Return desired FRP client endpoints.

    FRP_SERVER_ENDPOINTS is a comma-separated list:
      name=host:port,host2:port2
    If unset, GPTAdmin uses the public 3-edge defaults for t.gptadmin.
    Legacy FRP_SERVER_ADDR/FRP_SERVER_PORT still work when endpoints are set to
    an empty string by packagers/users through FRPC_SERVER_ENDPOINTS_DEFAULT=''.
    """
    raw = (env.get('FRP_SERVER_ENDPOINTS') or FRPC_SERVER_ENDPOINTS_DEFAULT or '').strip()
    items = [x.strip() for x in raw.split(',') if x.strip()]
    if not items:
        items = [f"primary={env.get('FRP_SERVER_ADDR', FRPC_SERVER_ADDR_DEFAULT)}:{env.get('FRP_SERVER_PORT', FRPC_SERVER_PORT_DEFAULT)}"]
    specs = []
    for idx, item in enumerate(items):
        name = None
        target = item
        if '=' in item:
            name, target = item.split('=', 1)
            name = name.strip()
            target = target.strip()
        port = env.get('FRP_SERVER_PORT', FRPC_SERVER_PORT_DEFAULT)
        addr = target
        if target.startswith('[') and ']' in target:
            # Minimal IPv6 support: [addr]:port
            host, rest = target[1:].split(']', 1)
            addr = host
            if rest.startswith(':') and rest[1:]:
                port = rest[1:]
        elif ':' in target:
            host, maybe_port = target.rsplit(':', 1)
            if maybe_port:
                addr = host
                port = maybe_port
        slug = _frpc_slug(name or ('primary' if idx == 0 else addr))
        specs.append({
            'idx': idx,
            'primary': idx == 0,
            'name': name or slug,
            'slug': slug,
            'addr': addr,
            'port': str(port),
            'domain': env.get('FRP_DOMAIN', FRPC_DOMAIN_DEFAULT),
        })
    return specs


def frpc_conf_path(spec: dict) -> Path:
    return FRPC_CONF if spec.get('primary') else ETC_DIR / f"frpc-{spec['slug']}.toml"


def frpc_desired_unit() -> tuple[str, Path]:
    return svc_frpc_name(), UNIT_PATH_FRPC


def frpc_legacy_units() -> list[tuple[str, Path]]:
    units: dict[str, tuple[str, Path]] = {}
    if IS_MACOS:
        for path in SERVICES_DIR.glob('com.gptadmin*.frpc*.plist'):
            if path != UNIT_PATH_FRPC:
                units[str(path)] = (path.stem, path)
    else:
        for pattern in ('gptadmin-frpc.service', 'gptadmin-frpc-*.service'):
            for path in SYSTEMD_DIR.glob(pattern):
                if path != UNIT_PATH_FRPC:
                    units[str(path)] = (path.name, path)
    return list(units.values())


def frpc_unit_specs(env: dict | None = None) -> list[tuple[str, Path]]:
    return [frpc_desired_unit()]


def frpc_installed_units(env: dict | None = None) -> list[tuple[str, Path]]:
    units = {str(UNIT_PATH_FRPC): frpc_desired_unit()}
    for name, path in frpc_legacy_units():
        units.setdefault(str(path), (name, path))
    return list(units.values())


def svc_frpc_enable_start_all(env: dict | None = None):
    for name, path in frpc_legacy_units():
        svc_disable_stop(name, path)
        try:
            path.unlink()
        except FileNotFoundError:
            pass
        except Exception as e:
            print(f'WARN: не удалось удалить legacy FRP unit {path}: {e}', file=sys.stderr)
    name, path = frpc_desired_unit()
    svc_enable_start(name, path)


def svc_frpc_restart_all(env: dict | None = None):
    name, path = frpc_desired_unit()
    svc_restart(name, path)


def svc_frpc_disable_stop_all(env: dict | None = None):
    for name, path in reversed(frpc_installed_units(env or env_read())):
        svc_disable_stop(name, path)


def frpc_wrapper_script(frpc_bin: str, env: dict) -> str:
    confs = ' '.join(repr(str(frpc_conf_path(spec))) for spec in frpc_endpoint_specs(env))
    return """#!/usr/bin/env bash
set -Eeuo pipefail
FRPC_BIN={frpc_bin!r}
CONFS=({confs})
pids=()
next_retry=()
retry_delay=5
start_child() {{
  local index="$1"
  "$FRPC_BIN" -c "${{CONFS[$index]}}" &
  pids[$index]="$!"
  next_retry[$index]=0
}}
cleanup() {{
  trap - TERM INT EXIT
  for pid in "${{pids[@]}}"; do
    if [[ -n "$pid" ]]; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  wait 2>/dev/null || true
}}
trap cleanup TERM INT EXIT
for index in "${{!CONFS[@]}}"; do
  start_child "$index"
done
while true; do
  now="$(date +%s)"
  for index in "${{!CONFS[@]}}"; do
    pid="${{pids[$index]:-}}"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      continue
    fi
    if [[ "$now" -ge "${{next_retry[$index]:-0}}" ]]; then
      # Restart this edge independently; one failed edge must not stop healthy FRP edges.
      wait "$pid" 2>/dev/null || true
      start_child "$index"
      next_retry[$index]="$((now + retry_delay))"
    fi
  done
  sleep 2
done
""".format(frpc_bin=str(frpc_bin), confs=confs)


def write_frpc_conf(env: dict):
    FRPC_CONF.parent.mkdir(parents=True, exist_ok=True)
    local_port = env.get('HUB_PORT', '9001')
    local_ip = env.get('FRP_LOCAL_IP', '127.0.0.1')
    for spec in frpc_endpoint_specs(env):
        proxy_name = f"gptadmin-web-{env['FRP_SUBDOMAIN']}"
        if not spec['primary']:
            proxy_name += f"-{spec['slug']}"
        content = f"""serverAddr = "{spec['addr']}"
serverPort = {spec['port']}

[auth]
token = "{env['FRP_TOKEN']}"

[transport.tls]
enable = true
serverName = "{spec['domain']}"

[[proxies]]
name = "{proxy_name}"
type = "http"
localIP = "{local_ip}"
localPort = {local_port}
subdomain = "{env['FRP_SUBDOMAIN']}"
"""
        path = frpc_conf_path(spec)
        path.write_text(content)
        os.chmod(path, 0o640)


# ===== Cloudflare Quick Tunnel helpers =====

def ensure_cloudflared_installed() -> str:
    existing = shutil.which('cloudflared')
    if existing:
        return existing

    arch = detect_arch()
    BIN_DIR.mkdir(parents=True, exist_ok=True)
    dst = BIN_DIR / 'cloudflared'
    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        if IS_MACOS:
            asset_arch = 'arm64' if arch == 'arm64' else 'amd64'
            url = f'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-{asset_arch}.tgz'
            pkg = tdp / 'cloudflared.tgz'
            download(url, pkg)
            extract_tgz(pkg, tdp)
            candidates = [x for x in tdp.rglob('cloudflared') if x.is_file()]
            if not candidates:
                die('cloudflared binary not found in downloaded archive')
            shutil.copy2(candidates[0], dst)
        else:
            if arch not in {'amd64', 'arm64'}:
                die(f'cloudflared unsupported arch for direct install: {arch}')
            url = f'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-{arch}'
            download(url, dst)
        os.chmod(dst, 0o755)
    return str(dst)


def cloudflared_log_file() -> Path:
    return LOG_DIR / 'cloudflared.log'


def wait_cloudflare_quick_url(timeout_s: int = 90) -> str:
    log_file = cloudflared_log_file()
    pat = re.compile(r'https://[-a-zA-Z0-9]+(?:[-a-zA-Z0-9]*[-a-zA-Z0-9])?\.trycloudflare\.com')
    deadline = time.time() + timeout_s
    last = ''
    while time.time() < deadline:
        try:
            text = log_file.read_text(errors='ignore')[-20000:] if log_file.exists() else ''
            last = text[-1200:]
            m = pat.search(text)
            if m:
                return m.group(0).rstrip('/')
        except Exception:
            pass
        time.sleep(1)
    die(f'Cloudflare quick tunnel URL was not found in {log_file}. Last log tail:\n{last}')



def wait_cloudflare_public_health(public_url: str, timeout_s: int = 45, fatal: bool = False) -> bool:
    """Best-effort check that the published quick-tunnel URL reaches the hub.

    Client DNS may lag or be different from external DNS. For hub+local shell
    installs the shell uses localhost, while external clients use HUB_PUBLIC_URL.
    """
    url = public_url.rstrip('/') + '/version'
    log_file = cloudflared_log_file()
    deadline = time.time() + timeout_s
    last_err = ''
    while time.time() < deadline:
        res = subprocess.run(['curl', '-fsS', '--max-time', '10', url], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        if res.returncode == 0 and 'gptadmin_hub' in res.stdout:
            return True
        last_err = (res.stdout or '').strip()
        time.sleep(3)
    log_tail = ''
    try:
        log_tail = log_file.read_text(errors='ignore')[-2500:]
    except Exception:
        pass
    msg = (
        'Cloudflare quick tunnel URL was created but local public-health check did not pass: ' + url +
        ('\nLast curl output:\n' + last_err if last_err else '') +
        ('\ncloudflared log tail:\n' + log_tail if log_tail else '')
    )
    if fatal or os.environ.get('GPTADMIN_CLOUDFLARE_HEALTH_FATAL', '').strip().lower() in {'1', 'true', 'yes', 'on'}:
        die(msg)
    print('WARNING: ' + msg, file=sys.stderr)
    return False
# ===== Interactive setup =====

def ask(prompt: str, default: str = '') -> str:
    sfx = f' [{default}]' if default else ''
    val = input(f"{prompt}{sfx}: ").strip()
    return val or default


def configure_shellmcp_transport(env: dict, install_hub: bool, install_shellmcp: bool):
    if not install_shellmcp or not env.get('HUB_URL'):
        return
    print('\nКак ShellMCP будет подключаться к хабу?')
    print('  1) long-polling / polling — рекомендуется, работает за NAT/firewall')
    print('  2) webhook — только если хаб может напрямую достучаться до ShellMCP')
    print('  3) websocket — experimental')
    default_transport = env.get('SHELLMCP_TRANSPORT', 'polling')
    default_choice = {'polling': '1', 'webhook': '2', 'websocket': '3'}.get(default_transport, '1')
    ch = ask('Ваш выбор', default_choice)
    hub = env['HUB_URL'].rstrip('/')
    if ch == '2':
        env['SHELLMCP_TRANSPORT'] = 'webhook'
        env['SHELLMCP_QUEUE'] = '0'
        heartbeat = ask('Включить heartbeat? Обычно не нужен: webhook уже обновляет active (y/N)', 'n')
        env['SHELLMCP_HEARTBEAT'] = '1' if heartbeat.lower() in {'y', 'yes', 'д', 'да'} else '0'
        env['HB_INTERVAL_S'] = '3600'
        env.pop('QUEUE_URL', None)
        shellmcp_url_default = env.get('SHELLMCP_URL') or f"http://{first_ip()}:{env.get('SHELLMCP_PORT', '25900')}"
        env['SHELLMCP_URL'] = ask('Введите SHELLMCP_URL, доступный хабу', shellmcp_url_default)
    elif ch == '3':
        env['SHELLMCP_TRANSPORT'] = 'websocket'
        env['SHELLMCP_QUEUE'] = '0'
        env['SHELLMCP_HEARTBEAT'] = '0'
        env['HB_INTERVAL_S'] = '3600'
        env.pop('QUEUE_URL', None)
        env['WS_URL'] = hub.replace('https://', 'wss://').replace('http://', 'ws://') + '/ws/shellmcp'
        env['SHELLMCP_URL'] = ''
    else:
        env['SHELLMCP_TRANSPORT'] = 'polling'
        env['SHELLMCP_QUEUE'] = '1'
        env['SHELLMCP_HEARTBEAT'] = '0'
        env['HB_INTERVAL_S'] = '3600'
        env['QUEUE_URL'] = hub + '/queue'
        env['SHELLMCP_URL'] = ''
        env.setdefault('SHELLMCP_BIND', '127.0.0.1')



def configure_shellmcp_transport_noninteractive(env: dict, transport: str | None = None, heartbeat: bool = False) -> None:
    transport = (transport or env.get('SHELLMCP_TRANSPORT') or 'polling').strip().lower()
    if transport in {'long_poll', 'long-poll'}:
        transport = 'polling'
    hub = (env.get('HUB_URL') or '').rstrip('/')
    if not hub:
        die('HUB_URL is required for non-interactive ShellMCP transport setup')
    if transport == 'webhook':
        env['SHELLMCP_TRANSPORT'] = 'webhook'
        env['SHELLMCP_QUEUE'] = '0'
        env['SHELLMCP_HEARTBEAT'] = '1' if heartbeat else '0'
        env['HB_INTERVAL_S'] = '3600'
        env.pop('QUEUE_URL', None)
        env.setdefault('SHELLMCP_URL', f"http://{first_ip()}:{env.get('SHELLMCP_PORT', '25900')}")
    elif transport == 'websocket':
        env['SHELLMCP_TRANSPORT'] = 'websocket'
        env['SHELLMCP_QUEUE'] = '0'
        env['SHELLMCP_HEARTBEAT'] = '1' if heartbeat else '0'
        env['HB_INTERVAL_S'] = '3600'
        env.pop('QUEUE_URL', None)
        env['WS_URL'] = hub.replace('https://', 'wss://').replace('http://', 'ws://') + '/ws/shellmcp'
        env['SHELLMCP_URL'] = ''
    elif transport == 'polling':
        env['SHELLMCP_TRANSPORT'] = 'polling'
        env['SHELLMCP_QUEUE'] = '1'
        env['SHELLMCP_HEARTBEAT'] = '0'
        env['HB_INTERVAL_S'] = '3600'
        env['QUEUE_URL'] = hub + '/queue'
        env['SHELLMCP_URL'] = ''
        env.setdefault('SHELLMCP_BIND', '127.0.0.1')
    else:
        die('unknown ShellMCP transport. Use: polling, webhook, websocket')


def shellmcp_identity_dir_default() -> str:
    """Return the safest identity dir for ShellMCP.

    Older user installs used ~/.gptadmin by default. New installs use ETC_DIR
    (~/.config/gptadmin for user mode, /etc/gptadmin for system mode). During
    update, prefer an existing identity to avoid changing server_id/fingerprint
    and forcing users to re-approve the machine.
    """
    candidates = []
    for env_name in ('SHELLMCP_IDENTITY_DIR', 'SHELL_IDENTITY_DIR'):
        val = os.environ.get(env_name)
        if val:
            candidates.append(Path(val).expanduser())
    candidates.append(ETC_DIR)
    if IS_USER_INSTALL:
        candidates.append(USER_HOME / '.gptadmin')
    for d in candidates:
        try:
            if (d / 'shellmcp_identity.json').exists() or (d / 'shellmcp_ed25519').exists():
                return str(d)
        except Exception:
            pass
    return str(ETC_DIR)


def ensure_shellmcp_identity_env(env: dict) -> None:
    ident = env.get('SHELLMCP_IDENTITY_DIR') or env.get('SHELL_IDENTITY_DIR') or shellmcp_identity_dir_default()
    env['SHELLMCP_IDENTITY_DIR'] = ident
    env['SHELL_IDENTITY_DIR'] = ident

def sync_oauth_origin_env(env: dict) -> None:
    """Point OAuth discovery to this hub's public URL.

    Remote MCP clients such as Codex discover OAuth from the MCP endpoint.
    Leaving PUBLIC_ORIGIN/MCP_RESOURCE unset makes the Go hub use a legacy
    global default origin (retired 2026-09-10), which redirects users to
    the wrong authorization server/password.
    """
    # HUB_URL is normally the loopback listener for a Hub that is exposed by
    # FRP/Cloudflare.  It must never overwrite the already-published origin:
    # issue-token derives iss/aud/resource from these fields.
    public = canonical_public_url(
        env.get('PUBLIC_ORIGIN')
        or env.get('HUB_PUBLIC_URL')
        or env.get('MCP_RESOURCE')
        or env.get('HUB_URL')
        or ''
    )
    if env.get('FRP_ENABLE', '').lower() == 'true' and not public.lower().startswith('https://'):
        subdomain = str(env.get('FRP_SUBDOMAIN') or '').strip().strip('.')
        domain = str(env.get('FRP_DOMAIN') or '').strip().strip('.')
        if subdomain and domain:
            # Older FRP installs could retain a private HUB_PUBLIC_URL after
            # an update. Use the deterministic tunnel name only as recovery;
            # an explicit HTTPS origin may intentionally be a stable alias.
            public = f'https://{subdomain}.{domain}'
            env['HUB_PUBLIC_URL'] = public
    if not public:
        return
    env['PUBLIC_ORIGIN'] = public
    env['MCP_RESOURCE'] = public


def wait_local_hub_health(env: dict, timeout_s: int = 90) -> bool:
    if not env.get('HUB_PORT'):
        return False
    url = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}/version"
    deadline = time.time() + timeout_s
    last_err = ''
    while time.time() < deadline:
        res = subprocess.run(['curl', '-fsS', '--max-time', '5', url], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if res.returncode == 0:
            try:
                payload = json.loads(res.stdout or '{}')
            except (TypeError, ValueError):
                payload = {}
            name = str(payload.get('name') or '')
            if payload.get('ok') is True and name in {'gptadmin_hub', 'gptadmin-go-hub'}:
                return True
        last_err = (res.stderr or res.stdout or f'curl rc={res.returncode}').strip()
        time.sleep(2)
    print('WARNING: Local hub health check did not pass before starting dependent services' + (f': {last_err}' if last_err else ''), file=sys.stderr)
    return False


def _require_local_hub_health(env: dict, timeout_s: int = 90) -> None:
    """Abort setup when the newly started local Hub never becomes healthy.

    Args:
        env: Installer environment containing the Hub address and credentials.
        timeout_s: Maximum number of seconds to wait for the health endpoint.

    Raises:
        RuntimeError: If the Hub health gate does not pass.
    """
    if not wait_local_hub_health(env, timeout_s=timeout_s):
        raise RuntimeError('local Hub health check failed during setup')


def _load_local_shellmcp_identity(env: dict, timeout_s: int = 30) -> dict:
    identity_dir = Path(env.get('IDENTITY_DIR') or env.get('SHELLMCP_IDENTITY_DIR') or str(ETC_DIR))
    ident_file = identity_dir / 'shellmcp_identity.json'
    deadline = time.time() + max(0, timeout_s)
    while True:
        try:
            data = json.loads(ident_file.read_text())
            return data if isinstance(data, dict) else {}
        except Exception:
            if time.time() >= deadline:
                return {}
            time.sleep(1)


def _normalize_local_shell_identity(identity: dict) -> dict:
    if not isinstance(identity, dict):
        return {}
    nested = identity.get('identity') if isinstance(identity.get('identity'), dict) else {}
    return {
        'server_id': identity.get('server_id') or nested.get('server_id'),
        'public_key': identity.get('public_key') or identity.get('public_key_b64') or nested.get('public_key') or nested.get('public_key_b64'),
        'fingerprint': identity.get('fingerprint') or nested.get('fingerprint'),
    }


def _server_matches_local_shell_identity(server: dict, identity: dict) -> bool:
    if not identity or not isinstance(server, dict):
        return False
    expected_identity = _normalize_local_shell_identity(identity)
    payload = server.get('payload') if isinstance(server.get('payload'), dict) else {}
    meta = server.get('meta') if isinstance(server.get('meta'), dict) else {}
    # Security boundary for local auto-approve: the pending server must match
    # the cryptographic identity generated on this machine.  Hub discover puts
    # the relay's stable server_id/public key/fingerprint in meta, while older
    # pending payloads exposed them at top level/payload.  Accept either shape,
    # but never hostname/base_url/client IP as identity proof.
    matched_required = {}
    for key in ('server_id', 'public_key', 'fingerprint'):
        expected = str(expected_identity.get(key) or '')
        actuals = {
            str(value) for value in (server.get(key), payload.get(key), meta.get(key))
            if value not in (None, '')
        }
        if expected and actuals and expected not in actuals:
            # server_id has one intentional presentation alias: top-level
            # shell:<hostname> is the user-facing target, while meta.server_id
            # is the signed ShellMCP identity.  If meta matches, it is valid.
            if not (key == 'server_id' and str(meta.get('server_id') or '') == expected):
                return False
        matched_required[key] = bool(expected and expected in actuals)
    return bool(matched_required.get('server_id') and matched_required.get('public_key'))


def _server_active_matches_local_shell_identity(server: dict, identity: dict) -> bool:
    if not isinstance(server, dict) or str(server.get('status')) != 'active':
        return False
    expected_identity = _normalize_local_shell_identity(identity)
    expected_server_id = str(expected_identity.get('server_id') or '')
    actual_server_id = str(server.get('server_id') or '')
    return bool(expected_server_id and actual_server_id and expected_server_id == actual_server_id)


def _approve_pending_response_ok(res: dict) -> bool:
    if not isinstance(res, dict):
        return False
    candidates = [res]
    response = res.get('response')
    if isinstance(response, dict):
        candidates.append(response)
        sc = response.get('structuredContent')
        if isinstance(sc, dict):
            candidates.append(sc)
    sc = res.get('structuredContent')
    if isinstance(sc, dict):
        candidates.append(sc)
    for item in candidates:
        if not isinstance(item, dict):
            continue
        if item.get('ok') is True or item.get('status') in {'approved', 'active'}:
            return True
    return False


def _require_instruction_receipt(payload) -> str:
    """Keep the current instruction document with its caller-bound receipt."""
    if not isinstance(payload, dict):
        raise RuntimeError('Hub instruction discovery returned an invalid response')
    instructions, context = payload.get('instructions'), payload.get('context')
    if (not isinstance(instructions, str) or not instructions.strip()
            or not isinstance(context, str) or not context):
        raise RuntimeError('Hub instruction discovery omitted current instructions/context')
    return context


def _installer_instruction_context(discovered, fetch_tools) -> str:
    """Allow old installers only after a closed schema proves no context support.

    Partial/malformed receipts are never a legacy signal. Native tools/list
    advertises the schema facade even when legacy hubTools() does not.
    """
    if not isinstance(discovered, dict) or 'instructions' in discovered or 'context' in discovered:
        return _require_instruction_receipt(discovered)
    advertised = fetch_tools()
    if (not isinstance(advertised, dict) or advertised.get('jsonrpc') != '2.0'
            or advertised.get('id') != 1 or advertised.get('error') is not None):
        raise RuntimeError('Hub context capability schema is unavailable')
    response = advertised.get('result')
    tools = response.get('tools') if isinstance(response, dict) and response.get('error') is None and not response.get('isError') else None
    if not isinstance(tools, list) or not all(isinstance(tool, dict) for tool in tools):
        raise RuntimeError('Hub context capability schema is invalid')
    selected = [tool for tool in tools if tool.get('name') == 'schema']
    definition = selected[0].get('inputSchema') if len(selected) == 1 else None
    properties = definition.get('properties') if isinstance(definition, dict) else None
    target = properties.get('target') if isinstance(properties, dict) else None
    if (not isinstance(properties, dict) or definition.get('type') != 'object'
            or definition.get('additionalProperties') is not False
            or not isinstance(target, dict) or target.get('type') != 'string'
            or any(key in definition for key in ('$ref', 'anyOf', 'oneOf', 'allOf'))):
        raise RuntimeError('Hub context capability schema is ambiguous')
    if 'context' in properties:
        raise RuntimeError('Hub instruction discovery omitted current instructions/context')
    return ''


def maybe_autoapprove_local_shellmcp(env: dict, install_hub: bool, install_shellmcp: bool) -> None:
    """Approve only the freshly generated same-machine ShellMCP identity.

    Unknown remote agents remain pending.  Bundled hub+shell setup is different:
    setup generated the ShellMCP keypair locally, so it can prove which pending
    registration belongs to this machine and approve exactly that one.  A failure
    here aborts setup instead of leaving a superficially successful but unusable
    installation.
    """
    if not (install_hub and install_shellmcp):
        return
    flag = os.environ.get('GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP', env.get('GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP', '1')).strip().lower()
    if flag in {'0', 'false', 'no', 'off'}:
        print('Local ShellMCP auto-approve skipped: GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP=0')
        return

    fresh_after = time.time() - 0.25
    identity = _load_local_shellmcp_identity(env, timeout_s=30)
    normalized = _normalize_local_shell_identity(identity)
    if not normalized.get('server_id') or not normalized.get('public_key'):
        raise RuntimeError('local ShellMCP identity was not created; refusing blind auto-approval')

    base = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
    token = make_mcp_bearer_token(env, 'gptadmin-local-shellmcp-autoapprove', ttl_days=1, access_mode='full')

    def http_json(method: str, path: str, payload=None, timeout: float = 5.0):
        data = None if payload is None else json.dumps(payload).encode('utf-8')
        request = urllib.request.Request(
            base + path, data=data, method=method,
            headers={
                'Accept': 'application/json',
                'Content-Type': 'application/json',
                'Authorization': 'Bearer ' + token,
                'User-Agent': 'gptadmin-local-autoapprove/1',
            },
        )
        with urllib.request.urlopen(request, timeout=timeout) as response:
            body = response.read(1024 * 1024 + 1)
            if len(body) > 1024 * 1024:
                raise RuntimeError('local Hub discovery/schema response exceeds installer limit')
            body = body.decode('utf-8', 'replace')
            return json.loads(body or '{}')

    deadline = time.monotonic() + 30
    candidate = None
    last_servers = []
    while time.monotonic() < deadline:
        discovered = http_json('GET', '/mcp-relay/servers?detail=full', timeout=5.0)
        last_servers = discovered.get('servers', []) if isinstance(discovered, dict) else []
        matches = [
            server for server in last_servers
            if isinstance(server, dict)
            and str(server.get('status') or '') == 'awaiting_approval'
            and _server_matches_local_shell_identity(server, identity)
        ]
        if len(matches) == 1:
            candidate = matches[0]
            break
        if len(matches) > 1:
            raise RuntimeError('multiple pending ShellMCP agents matched the local cryptographic identity')
        # It may already be online after a retry/restart.
        if any((
            _server_active_matches_local_shell_identity(server, identity) or (
                _server_matches_local_shell_identity(server, identity)
                and str(server.get('status') or '') == 'online'
            )
        ) and float(server.get('last_seen') or 0) >= fresh_after for server in last_servers):
            print('Local ShellMCP already approved and freshly online.')
            return
        time.sleep(0.5)

    if candidate is None:
        raise RuntimeError('local ShellMCP registration did not appear for cryptographic auto-approval')

    target = str(candidate.get('server_id') or '')
    context = _installer_instruction_context(discovered, lambda: http_json(
        'POST', '/mcp', {'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list', 'params': {}}, timeout=5.0,
    ))
    result = http_json(
        'POST', '/mcp-relay/call',
        {'target': 'hub', 'tool': 'approve_pending_server', 'arguments': {'server_id': target},
         **({'context': context} if context else {})},
        timeout=10.0,
    )
    if not _approve_pending_response_ok(result):
        raise RuntimeError(f'local ShellMCP auto-approval failed for {target}')

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        discovered = http_json('GET', '/mcp-relay/servers?detail=full', timeout=5.0)
        servers = discovered.get('servers', []) if isinstance(discovered, dict) else []
        for server in servers:
            if (
                isinstance(server, dict)
                and str(server.get('status') or '') == 'online'
                and _server_matches_local_shell_identity(server, identity)
                and float(server.get('last_seen') or 0) >= fresh_after
            ):
                print(f'Local ShellMCP auto-approved: {server.get("server_id") or target}')
                return
        time.sleep(0.25)
    raise RuntimeError(f'local ShellMCP {target} was approved but did not become online')

def setup_interactive(args):
    need_root()
    for c in REQUIRED_CMDS:
        if not have(c):
            die(f'required: {c}')

    silent = bool(getattr(args, 'silent', False) or getattr(args, 'yes', False))
    print('=== GPTAdmin setup ===')
    print(f'Install mode: {INSTALL_SCOPE}  install_dir={INSTALL_DIR}  config_dir={ETC_DIR}')
    if silent:
        wants_hub = bool(getattr(args, 'hub', False))
        wants_shell = bool(getattr(args, 'shellmcp', False))
        no_hub = bool(getattr(args, 'no_hub', False))
        no_shell = bool(getattr(args, 'no_shellmcp', False))
        if not wants_hub and not wants_shell:
            wants_hub = wants_shell = True
        install_hub = wants_hub and not no_hub
        install_shellmcp = wants_shell and not no_shell
        if not install_hub and not install_shellmcp:
            die('nothing to install: --no-hub and --no-shellmcp selected')
        print(f'Non-interactive install: hub={install_hub} shellmcp={install_shellmcp}')
    else:
        print('Что устанавливать?')
        print('  1) gptadmin_hub и ShellMCP agent')
        print('  2) только gptadmin_hub')
        print('  3) только ShellMCP agent')
        ch = ask('Ваш выбор', '1')
        install_hub = ch in ('1', '2')
        install_shellmcp = ch in ('1', '3')

    env = env_read()
    install_grepmesh = _grepmesh_enabled_from_args(args, env, install_shellmcp)
    env['GREPMESH_ENABLE'] = 'true' if install_grepmesh else 'false'

    # The normal profile is intentionally frictionless. Stronger process
    # isolation is an explicit operator choice, never a hidden system-install
    # side effect.
    requested_security_mode = getattr(args, 'security_mode', None)
    if requested_security_mode:
        env['GPTADMIN_SECURITY_MODE'] = requested_security_mode
    else:
        env.setdefault('GPTADMIN_SECURITY_MODE', 'normal')
    env.setdefault('SHELLMCP_TOKEN', gen_hex())
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    if install_shellmcp:
        env.setdefault('SHELLMCP_AUTO_UPDATE', '1')
        env.setdefault('SHELLMCP_SELF_REPAIR_DISABLE', '0')
        env.setdefault('SHELLMCP_LEGACY_MANIFEST_UPDATE', '0')
        ensure_shellmcp_default_user(env)
        ensure_shellmcp_identity_env(env)
        env.setdefault('SHELLMCP_UPDATE_INTERVAL_S', '3600')
        env.setdefault('SHELLMCP_UPDATE_TOKEN', env.get('SHELLMCP_TOKEN', ''))
        env.setdefault('SHELLMCP_UPDATE_MANIFEST_URL', (env.get('HUB_URL') or env.get('HUB_PUBLIC_URL') or 'https://became.bezrabotnyi.com').rstrip('/') + '/artifacts/shellmcp.json')
        env.setdefault('SHELLMCP_SERVICE_NAME', svc_shellmcp_name())
        env.setdefault('SHELLMCP_SERVICE_SCOPE', INSTALL_SCOPE)
        if not IS_USER_INSTALL:
            env.setdefault('SHELLMCP_FILE_CHECKPOINT_ROOT', '/var/lib/gptadmin/file-checkpoints')
    shellmcp_default_uid = os.environ.get('SHELLMCP_DEFAULT_UID')
    if shellmcp_default_uid and shellmcp_default_uid.isdigit() and shellmcp_default_uid != '0':
        env.setdefault('SHELLMCP_DEFAULT_UID', shellmcp_default_uid)

    env.setdefault('GPTADMIN_HOME', str(INSTALL_DIR))
    env.setdefault('GPTADMIN_CONFIG_DIR', str(ETC_DIR))
    env.setdefault('SHELLMCP_MCP_CONFIG', str(MCP_SUPERVISOR_CONFIG))
    env.setdefault('GPTADMIN_AUDIT_LOG', str((globals().get('LOG_DIR', Path('/var/log/gptadmin'))) / 'audit.log'))
    env['HUB_BIND'] = '127.0.0.1'
    env['HUB_PORT'] = str(getattr(args, 'hub_port', None) or env.get('HUB_PORT') or '9001')
    env.setdefault('SHELLMCP_BIND', '127.0.0.1')
    env.setdefault('SHELLMCP_PORT', '25900')

    if install_hub:
        if silent:
            mode = (getattr(args, 'tunnel', None) or 'frp').strip().lower()
        else:
            print('\nДоступ к хабу из Интернета:')
            if IS_MACOS:
                print('  1) Авто-туннель через наш FRP — рекомендуется, по умолчанию')
                print('  2) Cloudflare Quick Tunnel (*.trycloudflare.com) — без домена/port-forward, но иногда нестабилен и может отдавать 530')
                print('  3) У меня есть свой домен + HTTPS. Я настрою reverse-proxy на 127.0.0.1:%s' % env['HUB_PORT'])
                mode = ask('Ваш выбор', '1')
            else:
                print('  1) Авто-туннель через наш FRP (без вашего домена). Быстрый старт.')
                print('  2) У меня есть свой домен + HTTPS. Я настрою reverse-proxy (nginx/caddy/traefik)')
                print('     на 127.0.0.1:%s (его можно позже сменить: gptadmin port <port>)' % env['HUB_PORT'])
                mode = ask('Ваш выбор', '1')
        if mode in {'1', 'frp', 'auto'}:
            env['TUNNEL_MODE'] = 'frp'
            env['FRP_ENABLE'] = 'true'
            env['CLOUDFLARE_TUNNEL_ENABLE'] = 'false'
            env['FRP_SERVER_ADDR'] = env.get('FRP_SERVER_ADDR') or FRPC_SERVER_ADDR_DEFAULT
            env['FRP_SERVER_PORT'] = env.get('FRP_SERVER_PORT') or FRPC_SERVER_PORT_DEFAULT
            env['FRP_DOMAIN'] = env.get('FRP_DOMAIN') or FRPC_DOMAIN_DEFAULT
            env['FRP_SUBDOMAIN'] = env.get('FRP_SUBDOMAIN') or gen_subdomain()
            env['FRP_TOKEN'] = env.get('FRP_TOKEN') or FRPC_TOKEN_DEFAULT
            env['HUB_PUBLIC_URL'] = f"https://{env['FRP_SUBDOMAIN']}.{env['FRP_DOMAIN']}"
            # Public FRP is for ChatGPT/external clients. Same-machine ShellMCP
            # still uses the separate durable hub↔shell transport against local hub.
            if install_shellmcp:
                env['HUB_URL'] = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
        elif (IS_MACOS and mode == '2') or mode == 'cloudflare':
            print('WARNING: Cloudflare Quick Tunnel без аккаунта удобен для тестов, но может быть нестабилен и иногда отдавать HTTP 530.', file=sys.stderr)
            env['TUNNEL_MODE'] = 'cloudflare'
            env['CLOUDFLARE_TUNNEL_ENABLE'] = 'true'
            env['FRP_ENABLE'] = 'false'
            env.pop('HUB_PUBLIC_URL', None)
            env.pop('HUB_URL', None)
            if install_shellmcp:
                env['HUB_URL'] = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
        elif mode in {'2', '3', 'manual'}:
            url = getattr(args, 'hub_url', None) if silent else ask('Введите публичный HTTPS URL хаба (например, https://gptadmin.example.com)')
            ensure_https(url)
            env['TUNNEL_MODE'] = 'manual'
            env['FRP_ENABLE'] = 'false'
            env['CLOUDFLARE_TUNNEL_ENABLE'] = 'false'
            env['HUB_PUBLIC_URL'] = url
            env['HUB_URL'] = url
        elif mode in {'none', 'off', 'local'}:
            env['TUNNEL_MODE'] = 'none'
            env['FRP_ENABLE'] = 'false'
            env['CLOUDFLARE_TUNNEL_ENABLE'] = 'false'
            env.pop('HUB_PUBLIC_URL', None)
            env['HUB_URL'] = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
        else:
            die('unknown tunnel mode. Use: frp, manual, cloudflare, none')
    else:
        print('\nУстановка только ShellMCP agent.')
        url = getattr(args, 'hub_url', None) if silent else ask('Введите HUB_URL (публичный HTTPS адрес вашего хаба, например, https://gptadmin.example.com)')
        ensure_https(url)
        env['FRP_ENABLE'] = 'false'
        env['HUB_URL'] = url
        relay_token = getattr(args, 'mcp_relay_token', None) or os.environ.get('GPTADMIN_MCP_RELAY_TOKEN')
        enrollment_password = getattr(args, 'admin_password', None) or os.environ.get('GPTADMIN_MCP_RELAY_ENROLLMENT_PASSWORD')
        if relay_token:
            env['MCP_RELAY_AGENT_TOKEN'] = relay_token.strip()
        elif enrollment_password:
            env['MCP_RELAY_ENROLLMENT_PASSWORD'] = enrollment_password.strip()
        elif not silent:
            enrollment_password = ask('Введите пароль администратора этого Hub (нужен однократно для безопасного подключения MCP relay; Enter — настроить позже)')
            if enrollment_password:
                env['MCP_RELAY_ENROLLMENT_PASSWORD'] = enrollment_password

    normalize_bundled_shellmcp_hub_url(env, install_hub, install_shellmcp)
    if install_shellmcp:
        if silent:
            configure_shellmcp_transport_noninteractive(
                env,
                getattr(args, 'shell_transport', None) or 'polling',
                getattr(args, 'shell_heartbeat', False),
            )
        elif not (install_hub and env.get('TUNNEL_MODE') == 'cloudflare'):
            configure_shellmcp_transport(env, install_hub, install_shellmcp)
    if install_shellmcp:
        hub_for_update = (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or 'https://became.bezrabotnyi.com').rstrip('/')
        env['SHELLMCP_UPDATE_MANIFEST_URL'] = hub_for_update + '/artifacts/shellmcp.json'
        env['SHELLMCP_UPDATE_TOKEN'] = env.get('SHELLMCP_UPDATE_TOKEN') or env.get('SHELLMCP_TOKEN', '')
        env['SHELLMCP_SERVICE_NAME'] = svc_shellmcp_name()
        env['SHELLMCP_SERVICE_SCOPE'] = INSTALL_SCOPE

    env['INSTALL_HUB'] = 'true' if install_hub else 'false'
    env['INSTALL_SHELLMCP'] = 'true' if install_shellmcp else 'false'

    if not silent:
        existing_in_file = env_read().get('GPTADMIN_AUTO_UPDATE', '')
        if existing_in_file and existing_in_file.lower() in ('false', '0', 'no'):
            default_choice = 'n'
        else:
            default_choice = 'y'
        print()
        print(c_bold('  Автообновление'))
        ch = ask('Включить автообновление (проверка каждые 6ч, systemd timer / launchd)?', default_choice)
        if ch.lower() in ('n', 'no', 'нет'):
            env['GPTADMIN_AUTO_UPDATE'] = 'false'
            print(f'  {c_dim("Автообновление выключено. Включить потом:")} {c_green("gptadmin auto-update enable")}')
        else:
            env['GPTADMIN_AUTO_UPDATE'] = 'true'
    else:
        env.setdefault('GPTADMIN_AUTO_UPDATE', 'true')

    env.setdefault('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC', '21600')
    enrollment_password = env.pop('MCP_RELAY_ENROLLMENT_PASSWORD', '')
    if enrollment_password and not MCP_TOKEN_FILE.exists():
        MCP_TOKEN_FILE.parent.mkdir(parents=True, exist_ok=True)
        MCP_TOKEN_FILE.write_text(enrollment_password.strip() + '\n', encoding='utf-8')
        os.chmod(MCP_TOKEN_FILE, 0o600)
    sync_oauth_origin_env(env)
    env_set_many(env)
    env_remove_keys('MCP_RELAY_ENROLLMENT_PASSWORD')

    BIN_DIR.mkdir(parents=True, exist_ok=True)
    CLI_PATH.parent.mkdir(parents=True, exist_ok=True)
    pkg_all   = args.pkg_all   or platform_pkg_url_default()
    pkg_hub   = args.pkg_hub   or platform_pkg_url_default('full')
    pkg_shellmcp = args.pkg_shellmcp or platform_pkg_url_default('client')

    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        if install_hub and install_shellmcp:
            print('\n[Загрузка] общий пакет...')
            pkg = tdp / 'all.tgz'
            try:
                download(pkg_all, pkg)
            except subprocess.CalledProcessError:
                if pkg_all == PKG_ALL_URL_DEFAULT:
                    raise
                print('  Platform package unavailable, using full package...')
                download(PKG_ALL_URL_DEFAULT, pkg)
            install_component_from_pkg(pkg, 'hub')
            install_component_from_pkg(pkg, 'shellmcp')
        elif install_hub:
            print('\n[Загрузка] gptadmin_hub...')
            pkg = tdp / 'hub.tgz'
            try:
                download(pkg_hub, pkg)
            except subprocess.CalledProcessError:
                print('  Нет компонентного архива, беру общий...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'hub')
        else:
            print('\n[Загрузка] ShellMCP agent...')
            pkg = tdp / 'shellmcp.tgz'
            try:
                download(pkg_shellmcp, pkg)
            except subprocess.CalledProcessError:
                print('  Нет компонентного архива, беру общий...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'shellmcp')

    write_hub_unit(install_hub, install_shellmcp)
    write_shellmcp_unit(install_hub, install_shellmcp)
    grepmesh_ready = _configure_builtin_grepmesh(env, install_grepmesh) if install_shellmcp else False
    write_grepmesh_unit(grepmesh_ready, env)

    if env.get('FRP_ENABLE', 'false') == 'true':
        frpc_bin = ensure_frpc_installed()
        write_frpc_conf(env)
        write_frpc_unit(frpc_bin)
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        cloudflared_bin = ensure_cloudflared_installed()
        write_cloudflared_unit(cloudflared_bin, env)

    svc_daemon_reload()
    if install_hub:
        svc_enable_start(svc_hub_name(), UNIT_PATH_HUB)
        _require_local_hub_health(env)
        if not IS_USER_INSTALL and UNIT_PATH_HUB_STANDBY.exists():
            svc_enable_start(svc_hub_standby_name(), UNIT_PATH_HUB_STANDBY)
    if env.get('FRP_ENABLE', 'false') == 'true':
        svc_frpc_enable_start_all(env)
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        svc_enable_start(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED)
        public_url = wait_cloudflare_quick_url()
        wait_cloudflare_public_health(public_url)
        env = env_read()
        env['TUNNEL_MODE'] = 'cloudflare'
        env['CLOUDFLARE_TUNNEL_ENABLE'] = 'true'
        env['FRP_ENABLE'] = 'false'
        env['HUB_PUBLIC_URL'] = public_url
        if install_shellmcp:
            local_hub = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
            env['HUB_URL'] = local_hub
            env['SHELLMCP_TRANSPORT'] = 'polling'
            env['SHELLMCP_QUEUE'] = '1'
            env['SHELLMCP_HEARTBEAT'] = '0'
            env['HB_INTERVAL_S'] = '3600'
            env['QUEUE_URL'] = local_hub.rstrip('/') + '/queue'
            env['SHELLMCP_URL'] = ''
            env['SHELLMCP_UPDATE_MANIFEST_URL'] = public_url.rstrip('/') + '/artifacts/shellmcp.json'
            env['SHELLMCP_UPDATE_TOKEN'] = env.get('SHELLMCP_UPDATE_TOKEN') or env.get('SHELLMCP_TOKEN', '')
        else:
            env['HUB_URL'] = public_url
        sync_oauth_origin_env(env)
        env_set_many(env)
    if grepmesh_ready:
        svc_enable_start(svc_grepmesh_name(), UNIT_PATH_GREPMESH)
    elif UNIT_PATH_GREPMESH.exists():
        svc_disable_stop(svc_grepmesh_name(), UNIT_PATH_GREPMESH)
    if install_shellmcp:
        svc_enable_start(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
        maybe_import_and_install_mcp_from_desktop_clients()
    svc_autoupdate_enable_start(env_read())
    maybe_autoapprove_local_shellmcp(env, install_hub, install_shellmcp)
    if install_hub and install_shellmcp:
        verified = _require_real_local_shell_exec(env_read(), timeout_s=90)
        print(f"Local runtime execution verified: {verified['target']}")
    auto_configure_ai_mcp_clients(env_read(), install_hub)

    env = env_read()
    print('\n=== Готово ===')
    if install_hub:
        print(f"Hub URL: {env.get('HUB_PUBLIC_URL', '—')}")
        print('Подключение: AdminPassword/OAuth (existing legacy bearer remains valid until explicitly rotated or removed)')
    if install_shellmcp and not install_hub:
        print(f"HUB_URL для ShellMCP: {env.get('HUB_URL', '—')}")
    if install_shellmcp:
        print('ShellMCP agent установлен.')

    installed = [n for n, p in [
        ('gptadmin-hub' if not IS_MACOS else SVC_HUB_LABEL, UNIT_PATH_HUB),
        (svc_shellmcp_name(), UNIT_PATH_SHELLMCP),
        (svc_grepmesh_name(), UNIT_PATH_GREPMESH if grepmesh_ready else None),
        (SYSTEMD_FRPC if not IS_MACOS else SVC_FRPC_LABEL,
         UNIT_PATH_FRPC if env.get('FRP_ENABLE', 'false') == 'true' else None),
        ('gptadmin-cloudflared' if not IS_MACOS else SVC_CLOUDFLARED_LABEL,
         UNIT_PATH_CLOUDFLARED if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true' else None)
    ] if p and Path(p).exists()]
    print("Сервисы: " + ", ".join(installed))
    if IS_USER_INSTALL and str(CLI_PATH.parent) not in os.environ.get('PATH', '').split(os.pathsep):
        print(f'Добавьте CLI в PATH: export PATH="{CLI_PATH.parent}:$PATH"')

    if install_hub:
        print(f'''
-------------------
1) Перейдите на chatgpt.com/gpts/editor

2) Нажмите «Создать новое действие».

3) Выберите импорт по URL: https://became.bezrabotnyi.com/api.json

4) Замените в "servers": "url": на свой Hub URL {env.get('HUB_PUBLIC_URL') or env.get('HUB_URL', '—')}

5) Завершите OAuth-подключение через страницу Hub; bearer-копирование больше не используется.
---------------------''')

# ===== Commands =====

# ===== MCP stdio relay manager =====

def _json_read(path: Path, default):
    if path.exists():
        try:
            return json.loads(path.read_text(encoding='utf-8'))
        except PermissionError:
            die(f'permission denied reading {path}; run with sudo')
    return default


def _json_write(path: Path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    try:
        os.chmod(path, 0o640)
    except Exception:
        pass


def _mcp_default_hub_url() -> str:
    env = env_read()
    return (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or os.environ.get('GPTADMIN_MCP_RELAY_HUB') or 'https://became.bezrabotnyi.com').rstrip('/')


def _mcp_config() -> dict:
    cfg = _json_read(MCP_CONFIG_FILE, {})
    if not isinstance(cfg, dict):
        cfg = {}
    cfg.setdefault('gptadmin', {})
    cfg.setdefault('mcpServers', {})
    g = cfg['gptadmin']
    g.setdefault('hub_url', _mcp_default_hub_url())
    g.setdefault('token_file', str(MCP_TOKEN_FILE))
    g.setdefault('config_style', 'claude-compatible')
    return cfg


def _mcp_save(cfg: dict):
    _json_write(MCP_CONFIG_FILE, cfg)


def _mcp_go_supervisor_enabled() -> bool:
    """Return whether the installed ShellMCP service owns MCP relay children."""
    env = env_read()
    config = (
        os.environ.get('SHELLMCP_MCP_CONFIG')
        or env.get('SHELLMCP_MCP_CONFIG')
        or os.environ.get('GPTADMIN_MCP_CONFIG')
        or env.get('GPTADMIN_MCP_CONFIG')
        or os.environ.get('GPTADMIN_MCP_AGENTS_DIR')
        or env.get('GPTADMIN_MCP_AGENTS_DIR')
    )
    if config and config.strip():
        return True
    # Go-only upgrades may predate SHELLMCP_MCP_CONFIG in gptadmin.env. The
    # installed native binary is authoritative; falling back to standalone
    # Python relays here recreates duplicate services on every `mcp install`.
    binary = BIN_DIR / 'shellmcp'
    return binary.is_file() and _binary_looks_native(binary)


def _mcp_slug(name: str) -> str:
    return re.sub(r'[^A-Za-z0-9_.-]+', '-', name.strip()).strip('-._') or 'mcp'


def _mcp_agent_id(name: str, server: dict) -> str:
    if server.get('agent_id'):
        return str(server['agent_id'])
    return f"{socket.gethostname()}-{_mcp_slug(name)}"


def _mcp_ensure_token_file():
    env = env_read()
    # MCP_RELAY_AGENT_TOKEN is legacy migration input only. New installations
    # enroll once with ADMIN_PASSWORD and immediately replace this file with a
    # per-agent credential issued by the Hub.
    token = (
        env.get('MCP_RELAY_AGENT_TOKEN')
        or env.get('MCP_RELAY_ENROLLMENT_PASSWORD')
        or os.environ.get('GPTADMIN_MCP_RELAY_TOKEN')
        or os.environ.get('GPTADMIN_MCP_RELAY_ENROLLMENT_PASSWORD')
    )
    # The relay atomically replaces its enrollment password with its own
    # credential after the first successful registration. Never overwrite that
    # credential during later catalog/config refreshes.
    if MCP_TOKEN_FILE.exists():
        return
    if not token:
        die(
            'Managed Hub connection is not configured. Re-run setup for the '
            'target Hub or complete pairing from its connection page.'
        )
    token = token.strip()
    MCP_TOKEN_FILE.parent.mkdir(parents=True, exist_ok=True)
    MCP_TOKEN_FILE.write_text(token + '\n', encoding='utf-8')
    os.chmod(MCP_TOKEN_FILE, 0o600)


def _mcp_agent_config(name: str, cfg: dict) -> dict:
    servers = cfg.get('mcpServers') or {}
    if name not in servers:
        die(f'MCP server not found: {name}')
    server = dict(servers[name])
    if 'command' not in server:
        die(f'MCP server {name!r} has no command')
    g = cfg.get('gptadmin') or {}
    out = {
        'agent_id': _mcp_agent_id(name, server),
        'name': str(server.get('name') or f'{name} via {socket.gethostname()}'),
        'hub_url': str(server.get('hub_url') or g.get('hub_url') or _mcp_default_hub_url()),
        'token_file': str(server.get('token_file') or g.get('token_file') or MCP_TOKEN_FILE),
        'command': str(server['command']),
        'args': [str(x) for x in server.get('args', [])],
        'env': {str(k): str(v) for k, v in (server.get('env') or {}).items()},
        'cwd': str(server.get('cwd') or ('/Users/' + os.environ.get('SUDO_USER', os.environ.get('USER', 'user')) if IS_MACOS else '/')),
        'stdio_format': str(server.get('stdio_format') or server.get('transport') or 'auto'),
        'run_as_user': str(server.get('run_as_user') or server.get('user') or (os.environ.get('USER') if IS_USER_INSTALL and os.name != 'nt' else ('root' if os.name != 'nt' else 'SYSTEM'))),
        'auto_start': bool(server.get('auto_start', True)),
        'mode': 'agent-config',
    }
    for k in ('python', 'init_timeout', 'verbose', 'trace_json', 'log_dir'):
        if k in server:
            out[k] = server[k]
    return out


def _mcp_write_agent_config(name: str, cfg: dict) -> Path:
    # Native ShellMCP consumes the single aggregate Go supervisor registry.
    # Per-agent Python relay descriptors are intentionally retired.
    _mcp_sync_go_supervisor_config(cfg)
    return MCP_SUPERVISOR_CONFIG


def _mcp_sync_go_supervisor_config(cfg: dict) -> None:
    """Project real MCP children into the aggregate Go ShellMCP registry."""
    agents = []
    for name, server in sorted((cfg.get('mcpServers') or {}).items()):
        if not server.get('enabled', True):
            continue
        agent = {
            'ref': _mcp_agent_id(name, server),
            'name': str(server.get('name') or name),
            'command': str(server.get('command') or ''),
            'args': [str(value) for value in server.get('args', [])],
            'env': {str(key): str(value) for key, value in (server.get('env') or {}).items()},
            'cwd': str(server.get('cwd') or '/'),
            'user': str(server.get('run_as_user') or server.get('user') or ''),
            'enabled': True,
        }
        if server.get('url'):
            agent['url'] = str(server['url'])
        if server.get('headers'):
            agent['headers'] = {str(key): str(value) for key, value in server['headers'].items()}
        transport = str(server.get('transport') or '').strip().lower()
        if transport in {'stdio', 'streamable-http', 'sse'}:
            agent['transport'] = transport
        agents.append(agent)
    _json_write(MCP_SUPERVISOR_CONFIG, agents)


def _mcp_retire_legacy_relay_services(cfg: dict, names: list[str], backend: str | None = None) -> None:
    """Stop stale standalone Python relay services superseded by Go."""
    if IS_MACOS:
        for unit_path in sorted(SERVICES_DIR.glob('com.gptadmin.mcp.*.plist')):
            svc_disable_stop(unit_path.stem, unit_path)
            unit_path.unlink(missing_ok=True)
    elif os.name != 'nt':
        removed = False
        for unit_path in sorted(SYSTEMD_DIR.glob('gptadmin-mcp-*.service')):
            svc_disable_stop(unit_path.name, unit_path)
            unit_path.unlink(missing_ok=True)
            removed = True
        if removed:
            svc_daemon_reload()


def _mcp_refresh_generated_configs(cfg: dict) -> None:
    """Regenerate the native ShellMCP supervisor registry."""
    _mcp_sync_go_supervisor_config(cfg)



def _grepmesh_default_config(env: dict) -> dict:
    home = Path(env.get('SHELLMCP_DEFAULT_HOME') or env.get('SHELL_DEFAULT_HOME') or USER_HOME).expanduser()
    host_id = (env.get('SHELLMCP_NAME') or socket.gethostname() or 'gptadmin-host').strip()
    exclude_globs = [
        '**/.git/**', '**/.svn/**', '**/.hg/**', '**/node_modules/**',
        '**/.pnpm-store/**', '**/venv/**', '**/.venv/**', '**/__pycache__/**',
        '**/.pytest_cache/**', '**/.mypy_cache/**', '**/.ruff_cache/**',
        '**/target/**', '**/dist/**', '**/build/**', '**/out/**', '**/.cache/**',
        '**/.cargo/**', '**/.rustup/**', '**/go/pkg/mod/**', '**/.local/share/Trash/**',
        '**/.ssh/**', '**/.gnupg/**', '**/.aws/credentials', '**/.netrc',
        '**/id_rsa', '**/id_ed25519', '**/*.pem', '**/*.key', '**/shadow', '**/gshadow',
    ]
    return {
        'host_id': host_id,
        'bind': '127.0.0.1:9419',
        'root': str(home),
        'roots': {'home': [str(home)]},
        'peers': [],
        'exclude_globs': exclude_globs,
        'limits': {'max_results': 64, 'context_lines': 2, 'max_response_bytes': 131072,
                   'peer_timeout_ms': 2000, 'overall_timeout_ms': 5000, 'max_file_bytes': 16777216},
    }


def _grepmesh_matching_servers(servers: dict) -> list[tuple[str, dict]]:
    """Find GrepMesh definitions regardless of config key/casing."""
    matches: list[tuple[str, dict]] = []
    for key, server in servers.items():
        if not isinstance(server, dict):
            continue
        identities = {
            str(key).strip().lower(),
            str(server.get('name') or '').strip().lower(),
            str(server.get('agent_id') or '').strip().lower(),
        }
        url = str(server.get('url') or '').rstrip('/').lower()
        command_text = ' '.join([
            str(server.get('command') or ''),
            *[str(value) for value in (server.get('args') or [])],
        ]).lower()
        if (
            'grepmesh' in identities
            or url in {'http://127.0.0.1:9419/mcp', 'http://localhost:9419/mcp'}
            or 'grepmesh-mcp' in command_text
        ):
            matches.append((str(key), server))
    return matches


def _configure_builtin_grepmesh(env: dict, enabled: bool) -> bool:
    """Provision the bundled GrepMesh service and native ShellMCP child entry.

    Default-on is intentionally non-destructive: an existing user-owned GrepMesh
    config or MCP entry is preserved. --no-grepmesh only removes entries carrying
    our gptadmin_builtin marker and disables our service unit.
    """
    cfg = _mcp_config()
    servers = cfg.setdefault('mcpServers', {})
    matches = _grepmesh_matching_servers(servers)
    builtin = next(((key, spec) for key, spec in matches if spec.get('gptadmin_builtin') is True), None)
    operator = next(((key, spec) for key, spec in matches if spec.get('gptadmin_builtin') is not True), None)
    if not enabled:
        removed = False
        for key, spec in list(matches):
            if spec.get('gptadmin_builtin') is True:
                servers.pop(key, None)
                removed = True
        if removed:
            _mcp_save(cfg)
            _mcp_refresh_generated_configs(cfg)
        return False
    if operator is not None:
        # Preserve an operator-managed GrepMesh regardless of config key/casing
        # and never create a second child/listener for the same endpoint.
        return False
    existing = builtin[1] if builtin is not None else None
    binary = BIN_DIR / 'grepmesh-mcp'
    if not binary.exists():
        print_warn('Bundled GrepMesh binary is unavailable for this package/platform; continuing without GrepMesh. Use --no-grepmesh to silence this capability on future installs.')
        return False
    GREPMESH_CONFIG_FILE.parent.mkdir(parents=True, exist_ok=True)
    if not GREPMESH_CONFIG_FILE.exists():
        _json_write(GREPMESH_CONFIG_FILE, _grepmesh_default_config(env))
    if not IS_USER_INSTALL and os.name != 'nt':
        # /etc/gptadmin also contains the secret 0600 gptadmin.env.  GrepMesh
        # runs as the configured operator, so give only that operator's primary
        # group traverse access to the directory and read access to the known
        # non-secret grepmesh.json path.  0710 intentionally does not allow
        # directory listing; gptadmin.env remains 0600 root:root.
        grepmesh_user = env.get('SHELLMCP_DEFAULT_USER') or env.get('SHELL_DEFAULT_USER') or ''
        if grepmesh_user:
            try:
                grepmesh_gid = pwd.getpwnam(grepmesh_user).pw_gid
                os.chown(GREPMESH_CONFIG_FILE.parent, 0, grepmesh_gid)
                os.chmod(GREPMESH_CONFIG_FILE.parent, 0o710)
                os.chown(GREPMESH_CONFIG_FILE, 0, grepmesh_gid)
                os.chmod(GREPMESH_CONFIG_FILE, 0o640)
            except (KeyError, OSError) as exc:
                print_warn(f'Could not grant GrepMesh config access to {grepmesh_user}: {exc}')
        else:
            os.chmod(GREPMESH_CONFIG_FILE, 0o600)
    if existing is None:
        servers['GrepMesh'] = {
            'name': 'grepmesh',
            'transport': 'streamable-http',
            'url': 'http://127.0.0.1:9419/mcp',
            'enabled': True,
            'gptadmin_builtin': True,
        }
        _mcp_save(cfg)
        _mcp_refresh_generated_configs(cfg)
    return True


def _grepmesh_enabled_from_args(args, env: dict, install_shellmcp: bool) -> bool:
    if not install_shellmcp:
        return False
    if bool(getattr(args, 'no_grepmesh', False)):
        return False
    if bool(getattr(args, 'grepmesh', False)):
        return True
    return str(env.get('GREPMESH_ENABLE', 'true')).strip().lower() not in {'0', 'false', 'no', 'off'}


def _mcp_fix_read_permissions(path: Path, run_as_user: str | None = None):
    """Make generated MCP files readable by the relay service user."""
    try:
        path = Path(path)
        user = (run_as_user or '').strip()
        gid = -1
        if user and user not in ('root', 'SYSTEM') and os.name != 'nt':
            try:
                gid = pwd.getpwnam(user).pw_gid
            except Exception:
                gid = -1
        if os.name != 'nt':
            try:
                os.chown(path, 0, gid if gid >= 0 else -1)
            except PermissionError:
                raise
            except Exception:
                pass
        os.chmod(path, 0o640)
    except Exception as e:
        print(f'WARNING: failed to adjust permissions for {path}: {e}', file=sys.stderr)


def _mcp_fix_access_for_agent_config(agent_config: Path, cfg: dict, name: str):
    spec = _mcp_agent_config(name, cfg)
    run_as_user = str(spec.get('run_as_user') or 'root')
    _mcp_fix_read_permissions(agent_config, run_as_user)
    token_file = Path(str(spec.get('token_file') or MCP_TOKEN_FILE))
    if token_file.exists():
        _mcp_fix_read_permissions(token_file, run_as_user)

def _mcp_names_from_arg(args, cfg: dict):
    servers = cfg.get('mcpServers') or {}
    if getattr(args, 'name', None):
        return [args.name]
    return sorted(servers.keys())


def cmd_mcp_list(args):
    cfg = _mcp_config()
    servers = cfg.get('mcpServers') or {}
    if args.json:
        print(json.dumps(cfg, ensure_ascii=False, indent=2))
        return
    if not servers:
        print(f'No MCP servers configured. Add one: gptadmin mcp add gptadminmcp --url https://.../mcp')
        return
    for name, spec in sorted(servers.items()):
        enabled = spec.get('enabled', True)
        cmd = spec.get('command', '')
        argv = ' '.join(str(x) for x in spec.get('args', []))
        fmt = spec.get('stdio_format', spec.get('transport', 'auto'))
        catalog = spec.get('catalog_id', '')
        catalog_text = f'\t{catalog}' if catalog else ''
        print(f"{name}\t{'enabled' if enabled else 'disabled'}\t{fmt}{catalog_text}\t{cmd} {argv}")


def _mcp_catalog_signed_material(payload: dict) -> dict:
    """Return only the catalog fields covered by the detached signature."""

    return {
        'catalog_version': payload['catalog_version'],
        'source': payload['source'],
        'definitions': payload['definitions'],
    }


def _mcp_catalog_canonical_bytes(payload: dict) -> bytes:
    """Serialize signed catalog material deterministically for verification."""

    return json.dumps(_mcp_catalog_signed_material(payload), ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode('utf-8')


def _verify_mcp_catalog_payload(payload: dict) -> None:
    """Fail closed when the bundled capability catalog has been tampered with."""

    if Ed25519PublicKey is None:
        raise ValueError(
            'MCP capability catalog verification requires the optional '
            'Python package "cryptography". Install GPTAdmin with its Python '
            'runtime dependencies before using bundled catalog capabilities.'
        )
    try:
        public_key = base64.urlsafe_b64decode(MCP_CAPABILITY_CATALOG_PUBLIC_KEY_B64 + '==')
        signature = base64.urlsafe_b64decode(MCP_CAPABILITY_CATALOG_SIGNATURE_B64 + '==')
        Ed25519PublicKey.from_public_bytes(public_key).verify(signature, _mcp_catalog_canonical_bytes(payload))
    except (KeyError, ValueError, TypeError, InvalidSignature) as exc:
        raise ValueError('MCP capability catalog signature verification failed') from exc


def _mcp_catalog_payload() -> dict:
    """Return the immutable bundled capability catalog with verified provenance."""
    payload = {
        'catalog_version': MCP_CAPABILITY_CATALOG_VERSION,
        'source': 'GPTAdmin bundled capability catalog',
        'definitions': [dict(item) for item in MCP_CAPABILITY_CATALOG],
    }
    _verify_mcp_catalog_payload(payload)
    payload['catalog_digest_sha256'] = hashlib.sha256(_mcp_catalog_canonical_bytes(payload)).hexdigest()
    payload['signature'] = {
        'algorithm': 'Ed25519',
        'public_key': MCP_CAPABILITY_CATALOG_PUBLIC_KEY_B64,
        'verified': True,
    }
    return payload


def _mcp_catalog_definition(catalog_id: str) -> dict | None:
    """Return one catalog definition by ID, or ``None`` for an uncurated server."""
    if not catalog_id:
        return None
    for definition in _mcp_catalog_payload()['definitions']:
        if definition['id'] == catalog_id:
            return dict(definition)
    die(f'unknown MCP capability catalog id: {catalog_id}')


def cmd_mcp_catalog(args):
    """Display curated capabilities before an operator activates one."""
    payload = _mcp_catalog_payload()
    if getattr(args, 'json', False):
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return
    print(f"Catalog: {payload['catalog_version']} ({payload['source']})")
    for definition in payload['definitions']:
        print(f"{definition['id']}\t{definition['version']}\t{definition['risk_level']}\t{definition['provenance']}")
        print(f"  scopes={','.join(definition['scopes'])} tools={','.join(definition['tools'])}")
        print(f"  network_needs={'; '.join(definition['network_needs']) or 'none'} owner={definition['maintenance_owner']}")
    print(f"  signature={payload['signature']['algorithm']} verified digest={payload['catalog_digest_sha256']}")


def _validate_mcp_extension_manifest(path: Path) -> dict:
    """Validate a third-party MCP extension manifest without executing it."""
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise ValueError(f'cannot read extension manifest: {exc}') from exc
    if len(raw) > 128 * 1024:
        raise ValueError('extension manifest exceeds 128 KiB')
    try:
        manifest = json.loads(raw.decode('utf-8'))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError(f'extension manifest is not valid UTF-8 JSON: {exc}') from exc
    if not isinstance(manifest, dict):
        raise ValueError('extension manifest must be a JSON object')
    required = ('schema', 'id', 'version', 'kind', 'protocol', 'entrypoint', 'capabilities', 'scopes', 'network_needs', 'provenance', 'risk_level', 'maintenance_owner')
    for key in required:
        if key not in manifest:
            raise ValueError(f'extension manifest requires {key}')
    if manifest['schema'] != 'gptadmin.mcp-extension/v1':
        raise ValueError('unsupported extension manifest schema')
    for key in ('id', 'version', 'kind', 'protocol', 'maintenance_owner', 'provenance'):
        if not isinstance(manifest[key], str) or not manifest[key].strip():
            raise ValueError(f'extension manifest {key} must be a non-empty string')
    if manifest['kind'] not in {'stdio', 'http'} or manifest['protocol'] != 'mcp-jsonrpc':
        raise ValueError('extension manifest kind/protocol is unsupported')
    if not isinstance(manifest['entrypoint'], list) or not manifest['entrypoint'] or not all(isinstance(item, str) and item for item in manifest['entrypoint']):
        raise ValueError('extension manifest entrypoint must be a non-empty string list')
    if not isinstance(manifest['capabilities'], list) or not manifest['capabilities']:
        raise ValueError('extension manifest capabilities must be a non-empty list')
    for capability in manifest['capabilities']:
        if not isinstance(capability, dict) or not isinstance(capability.get('name'), str) or not capability['name'].strip():
            raise ValueError('extension capability name is required')
        if not isinstance(capability.get('description'), str) or not capability['description'].strip():
            raise ValueError('extension capability description is required')
        if not isinstance(capability.get('input_schema'), dict):
            raise ValueError('extension capability input_schema is required')
    for key in ('scopes', 'network_needs'):
        if not isinstance(manifest[key], list) or not all(isinstance(item, str) for item in manifest[key]):
            raise ValueError(f'extension manifest {key} must be a string list')
    if manifest['risk_level'] not in {'low', 'medium', 'high'}:
        raise ValueError('extension manifest risk_level must be low, medium or high')
    return manifest


def cmd_mcp_extension_validate(args):
    """Validate and print an extension manifest without installing it."""
    manifest = _validate_mcp_extension_manifest(Path(args.manifest).expanduser())
    print(json.dumps({'valid': True, 'manifest': manifest}, ensure_ascii=False, indent=2))



def _mcp_extract_tail_options(args):
    # argparse.REMAINDER is used so command tails like "npx -y ..." survive.
    # That also means "gptadmin mcp add name --url ..." lands in args.command/args.args.
    tail = []
    if getattr(args, 'command', None):
        tail.append(args.command)
    tail.extend(getattr(args, 'args', None) or [])
    # argparse.REMAINDER preserves the conventional command separator.  It is
    # syntax, not the executable; accepting it keeps `mcp add NAME -- npx ...`
    # consistent with the documented examples and mcp-add helper.
    if tail and tail[0] == '--':
        tail.pop(0)
    if not tail:
        return
    cleaned = []
    i = 0
    known_value_opts = {
        '--url': 'url',
        '--stdio-format': 'stdio_format',
        '--cwd': 'cwd',
        '--agent-id': 'agent_id',
        '--run-as-user': 'run_as_user',
        '--hub-url': 'hub_url',
        '--catalog-id': 'catalog_id',
    }
    while i < len(tail):
        item = tail[i]
        if item in known_value_opts and i + 1 < len(tail):
            setattr(args, known_value_opts[item], tail[i + 1])
            i += 2
            continue
        if item == '--env' and i + 1 < len(tail):
            cur = getattr(args, 'env', None) or []
            cur.append(tail[i + 1])
            args.env = cur
            i += 2
            continue
        if item == '--disabled':
            args.disabled = True
            i += 1
            continue
        if item == '--force':
            args.force = True
            i += 1
            continue
        if item == '--install':
            args.install = True
            i += 1
            continue
        if item == '--accept-capability':
            args.accept_capability = True
            i += 1
            continue
        if item == '--status':
            args.status = True
            i += 1
            continue
        cleaned.append(item)
        i += 1
    args.command = cleaned[0] if cleaned else None
    args.args = cleaned[1:] if len(cleaned) > 1 else []

def cmd_mcp_add(args):
    need_root()
    _mcp_extract_tail_options(args)
    catalog_id = getattr(args, 'catalog_id', None) or ''
    catalog_definition = _mcp_catalog_definition(catalog_id)
    if catalog_definition:
        print('Curated capability requested:')
        print(json.dumps(catalog_definition, ensure_ascii=False, indent=2))
        if getattr(args, 'install', False) and not getattr(args, 'accept_capability', False):
            die('curated MCP activation requires --accept-capability after reviewing the catalog definition')
    # Validate the requested executable before touching persistent/global config.
    # This keeps malformed CLI invocations deterministic even when the caller
    # cannot read a system installation's /etc/gptadmin state.
    if not args.url:
        if not args.command:
            die('provide --url URL or COMMAND [ARGS...]')
        if args.command.startswith('-'):
            die(
                f'invalid MCP command {args.command!r}: the first token after NAME must be an executable. '
                f'For Chrome DevTools use: gptadmin mcp add chrome-devtools npx -y '
                f'chrome-devtools-mcp@latest --browser-url=http://127.0.0.1:9223'
            )
    if getattr(args, 'install', False) and not getattr(args, 'disabled', False) and not _mcp_go_supervisor_enabled():
        die('Native ShellMCP is required for MCP servers. Install/update ShellMCP, then run `gptadmin mcp install`.')

    cfg = _mcp_config()
    servers = cfg.setdefault('mcpServers', {})
    if args.name in servers and not args.force:
        die(f'MCP server already exists: {args.name}; use --force to overwrite')
    env = {}
    for item in args.env or []:
        if '=' not in item:
            die(f'--env must be KEY=VALUE, got: {item}')
        k, v = item.split('=', 1)
        env[k] = v
    if args.url:
        command = args.command or 'npx'
        cmd_args = args.args or ['-y', 'mcp-remote', args.url]
        stdio = args.stdio_format or 'framed'
    else:
        command = args.command
        cmd_args = args.args or []
        stdio = args.stdio_format or 'auto'
    servers[args.name] = {
        'command': command,
        'args': [str(x) for x in cmd_args],
        'env': env,
        'cwd': args.cwd,
        'stdio_format': stdio,
        'enabled': not args.disabled,
    }
    if args.agent_id:
        servers[args.name]['agent_id'] = args.agent_id
    if args.run_as_user:
        servers[args.name]['run_as_user'] = args.run_as_user
    if args.hub_url:
        cfg.setdefault('gptadmin', {})['hub_url'] = args.hub_url.rstrip('/')
    if catalog_definition:
        servers[args.name]['catalog_id'] = catalog_definition['id']
        servers[args.name]['catalog_version'] = catalog_definition['version']
        servers[args.name]['catalog_provenance'] = catalog_definition['provenance']
    _mcp_save(cfg)
    agent_config = _mcp_write_agent_config(args.name, cfg)
    print(f'Added MCP server {args.name}')
    print(f'Config: {MCP_CONFIG_FILE}')
    print(f'ShellMCP supervisor config: {agent_config}')
    if getattr(args, 'install', False):
        if args.disabled:
            print(f'Skip disabled MCP server: {args.name}')
        else:
            print(f'ShellMCP supervisor will manage MCP server {args.name}')
    if getattr(args, 'status', False):
        print(f'### {args.name}: managed by native ShellMCP')

def cmd_mcp_remove(args):
    need_root()
    cfg = _mcp_config()
    servers = cfg.get('mcpServers') or {}
    if args.name not in servers:
        die(f'MCP server not found: {args.name}')
    servers.pop(args.name)
    _mcp_save(cfg)
    _mcp_sync_go_supervisor_config(cfg)
    print(f'Removed MCP server {args.name}')


def cmd_mcp_edit(args):
    need_root()
    cfg = _mcp_config()
    _mcp_save(cfg)
    editor = os.environ.get('EDITOR') or ('nano' if have('nano') else 'vi')
    run([editor, str(MCP_CONFIG_FILE)])
    # The editor can change enabled state and definitions, so refresh both
    # generated representations from the final file rather than just mcp.json.
    cfg = _mcp_config()
    _mcp_refresh_generated_configs(cfg)
    print(f'Updated {MCP_CONFIG_FILE}')


def cmd_mcp_render(args):
    cfg = _mcp_config()
    _mcp_sync_go_supervisor_config(cfg)
    print(MCP_SUPERVISOR_CONFIG)


def cmd_mcp_install(args):
    need_root()
    cfg = _mcp_config()
    names = _mcp_names_from_arg(args, cfg)
    if not names:
        die('no MCP servers configured')
    if _mcp_go_supervisor_enabled():
        env_updates = {'SHELLMCP_MCP_CONFIG': str(MCP_SUPERVISOR_CONFIG)}
        if IS_MACOS:
            migrated_env = env_read()
            ensure_shellmcp_default_user(migrated_env)
            for key in ('SHELLMCP_DEFAULT_USER', 'SHELLMCP_DEFAULT_HOME', 'SHELLMCP_DEFAULT_CWD'):
                if migrated_env.get(key):
                    env_updates[key] = migrated_env[key]
        env_set_many(env_updates)
        _mcp_refresh_generated_configs(cfg)
        _mcp_retire_legacy_relay_services(cfg, names, args.backend)
        svc_restart(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
        print('ShellMCP Go supervisor owns MCP servers; legacy relay services removed')
        return
    die('Native ShellMCP is required for MCP servers. Install/update ShellMCP, then run `gptadmin mcp install`.')


def cmd_mcp_status(args):
    cfg = _mcp_config()
    _mcp_sync_go_supervisor_config(cfg)
    print(f'Native ShellMCP supervisor config: {MCP_SUPERVISOR_CONFIG}')


def cmd_mcp_cat(args):
    cfg = _mcp_config()
    if args.name:
        print(json.dumps((cfg.get('mcpServers') or {}).get(args.name, {}), ensure_ascii=False, indent=2))
    else:
        print(json.dumps(cfg, ensure_ascii=False, indent=2))


def _user_home(username: str | None) -> Path:
    if username:
        try:
            return Path(pwd.getpwnam(username).pw_dir)
        except Exception:
            return Path('/Users' if IS_MACOS else '/home') / username
    sudo_user = os.environ.get('SUDO_USER') if os.geteuid() == 0 else None
    if sudo_user and sudo_user != 'root':
        return _user_home(sudo_user)
    return Path.home()


def _mcp_external_path(kind: str, username: str | None, explicit: str | None) -> Path:
    if kind == 'claude':
        kind = 'claude-desktop'
    if explicit:
        return Path(explicit).expanduser()
    home = _user_home(username)
    if kind == 'claude-desktop':
        if sys.platform == 'darwin':
            return home / 'Library' / 'Application Support' / 'Claude' / 'claude_desktop_config.json'
        if sys.platform.startswith('win'):
            appdata = os.environ.get('APPDATA')
            return Path(appdata) / 'Claude' / 'claude_desktop_config.json' if appdata else home / 'AppData' / 'Roaming' / 'Claude' / 'claude_desktop_config.json'
        return home / '.config' / 'Claude' / 'claude_desktop_config.json'
    if kind == 'claude-code':
        return home / '.claude.json'
    if kind == 'codex':
        return home / '.codex' / 'config.toml'
    die(f'unknown MCP config format: {kind}')


def _mcp_merge_servers(dst: dict, src_servers: dict, overwrite: bool = False) -> int:
    dst.setdefault('mcpServers', {})
    n = 0
    for name, spec in (src_servers or {}).items():
        if name in dst['mcpServers'] and not overwrite:
            continue
        if isinstance(spec, dict) and spec.get('command'):
            clean = {
                'command': spec.get('command'),
                'args': spec.get('args') or [],
                'env': spec.get('env') or {},
            }
            for k in ('cwd', 'stdio_format', 'transport', 'enabled', 'agent_id', 'run_as_user', 'name'):
                if k in spec:
                    clean[k] = spec[k]
            dst['mcpServers'][name] = clean
            n += 1
    return n


def _mcp_simple_toml_value(raw: str):
    raw = raw.strip()
    if raw.startswith('[') and raw.endswith(']'):
        body = raw[1:-1].strip()
        if not body:
            return []
        return [_mcp_simple_toml_value(x.strip()) for x in body.split(',') if x.strip()]
    if (raw.startswith('"') and raw.endswith('"')) or (raw.startswith("'") and raw.endswith("'")):
        try:
            return json.loads(raw) if raw.startswith('"') else raw[1:-1]
        except Exception:
            return raw[1:-1]
    if raw.lower() in ('true', 'false'):
        return raw.lower() == 'true'
    return raw


def _mcp_simple_codex_toml_read(path: Path) -> dict:
    out = {'mcpServers': {}}
    current = None
    current_env = False
    section_re = re.compile(r'^\[mcp_servers\.("[^"]+"|[^.\]]+)(\.env)?\]$')
    for raw in path.read_text(encoding='utf-8').splitlines():
        line = raw.split('#', 1)[0].strip()
        if not line:
            continue
        m = section_re.match(line)
        if m:
            name = _mcp_simple_toml_value(m.group(1))
            current = str(name)
            current_env = bool(m.group(2))
            out['mcpServers'].setdefault(current, {'env': {}})
            continue
        if current and '=' in line:
            k, v = line.split('=', 1)
            key = _mcp_simple_toml_value(k.strip())
            val = _mcp_simple_toml_value(v.strip())
            if current_env:
                out['mcpServers'][current].setdefault('env', {})[str(key)] = val
            else:
                out['mcpServers'][current][str(key)] = val
    return out


def _mcp_codex_read(path: Path) -> dict:
    if not path.exists():
        return {'mcpServers': {}}
    if tomllib:
        data = tomllib.loads(path.read_text(encoding='utf-8'))
        servers = data.get('mcp_servers') or data.get('mcpServers') or {}
        out = {'mcpServers': {}}
        for name, spec in servers.items():
            if not isinstance(spec, dict):
                continue
            command = spec.get('command') or spec.get('cmd')
            if not command:
                continue
            out['mcpServers'][name] = {
                'command': command,
                'args': spec.get('args') or [],
                'env': spec.get('env') or {},
            }
            if spec.get('cwd'):
                out['mcpServers'][name]['cwd'] = spec.get('cwd')
        return out
    out = _mcp_simple_codex_toml_read(path)
    for name, spec in list(out.get('mcpServers', {}).items()):
        if not spec.get('command'):
            out['mcpServers'].pop(name, None)
    return out


def _toml_quote(v) -> str:
    return json.dumps(str(v), ensure_ascii=False)


def _mcp_codex_write(path: Path, cfg: dict):
    lines = ['# Generated by gptadmin mcp export codex', '']
    for name, spec in sorted((cfg.get('mcpServers') or {}).items()):
        lines.append(f'[mcp_servers.{_toml_quote(name)}]')
        lines.append(f'command = {_toml_quote(spec.get("command", ""))}')
        args = ', '.join(_toml_quote(x) for x in (spec.get('args') or []))
        lines.append(f'args = [{args}]')
        if spec.get('cwd'):
            lines.append(f'cwd = {_toml_quote(spec.get("cwd"))}')
        env = spec.get('env') or {}
        if env:
            lines.append('[mcp_servers.%s.env]' % _toml_quote(name))
            for k, v in sorted(env.items()):
                lines.append(f'{_toml_quote(k)} = {_toml_quote(v)}')
        lines.append('')
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text('\n'.join(lines), encoding='utf-8')


def _mcp_external_read(kind: str, path: Path) -> dict:
    if kind == 'codex':
        return _mcp_codex_read(path)
    if not path.exists():
        return {'mcpServers': {}}
    data = json.loads(path.read_text(encoding='utf-8'))
    if 'mcpServers' not in data and 'mcp_servers' in data:
        data['mcpServers'] = data.get('mcp_servers') or {}
    data.setdefault('mcpServers', {})
    return data


def _mcp_external_write(kind: str, path: Path, cfg: dict, merge: bool = True):
    if kind == 'codex':
        _mcp_codex_write(path, cfg)
        return
    data = {'mcpServers': {}}
    if merge and path.exists():
        try:
            data = json.loads(path.read_text(encoding='utf-8'))
        except Exception:
            data = {}
    data.setdefault('mcpServers', {})
    data['mcpServers'].update(cfg.get('mcpServers') or {})
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def cmd_mcp_import(args):
    need_root()
    path = _mcp_external_path(args.format, args.user, args.path)
    ext = _mcp_external_read(args.format, path)
    cfg = _mcp_config()
    n = _mcp_merge_servers(cfg, ext.get('mcpServers') or {}, overwrite=getattr(args, 'force', False))
    _mcp_save(cfg)
    _mcp_refresh_generated_configs(cfg)
    print(f'Imported {n} MCP server(s) from {args.format}: {path}')
    print(f'GPTAdmin config: {MCP_CONFIG_FILE}')


def cmd_mcp_export(args):
    cfg = _mcp_config()
    path = _mcp_external_path(args.format, args.user, args.path)
    out = {'mcpServers': {}}
    names = [args.name] if args.name else sorted((cfg.get('mcpServers') or {}).keys())
    for name in names:
        if name not in (cfg.get('mcpServers') or {}):
            die(f'MCP server not found: {name}')
        spec = dict(cfg['mcpServers'][name])
        out['mcpServers'][name] = {
            'command': spec.get('command'),
            'args': spec.get('args') or [],
            'env': spec.get('env') or {},
        }
        if spec.get('cwd'):
            out['mcpServers'][name]['cwd'] = spec.get('cwd')
    _mcp_external_write(args.format, path, out, merge=not args.no_merge)
    print(f'Exported {len(out["mcpServers"])} MCP server(s) to {args.format}: {path}')


def cmd_mcp_sync(args):
    # Import first, then export merged config back to the same user-facing file.
    cmd_mcp_import(args)
    export_args = argparse.Namespace(format=args.format, user=args.user, path=args.path, name=None, no_merge=False)
    cmd_mcp_export(export_args)


def maybe_import_and_install_mcp_from_desktop_clients():
    """Offer to import existing Claude MCP servers during setup and install them.

    Best-effort: a failure here must not break the main shellmcp install.
    """
    if os.environ.get('GPTADMIN_SKIP_MCP_IMPORT', '').strip().lower() in {'1', 'true', 'yes', 'on'}:
        return
    if not _mcp_go_supervisor_enabled():
        return
    candidates = []
    try:
        p = _mcp_external_path('claude-desktop', None, None)
        if p.exists():
            candidates.append(('claude-desktop', p))
    except Exception:
        pass
    if not candidates:
        return
    default = os.environ.get('GPTADMIN_MCP_AUTO_IMPORT', '').strip().lower()
    do_it = default in {'1', 'true', 'yes', 'on'}
    if not do_it:
        try:
            do_it = ask('Найдены MCP servers в Claude. Импортировать и запустить через GPTAdmin shellmcp?', 'y').lower().startswith('y')
        except Exception:
            do_it = False
    if not do_it:
        return
    for fmt, path in candidates:
        try:
            import_args = argparse.Namespace(format=fmt, user=None, path=str(path), force=True)
            cmd_mcp_import(import_args)
        except Exception as e:
            print(f'WARN: не удалось импортировать MCP из {fmt}: {e}', file=sys.stderr)
    cfg = _mcp_config()
    if not (cfg.get('mcpServers') or {}):
        print('MCP import did not add any servers; skip MCP service install')
        return
    try:
        backend = 'launchd' if IS_MACOS else ('windows-task' if IS_WINDOWS else 'systemd')
        install_args = argparse.Namespace(name=None, backend=backend)
        cmd_mcp_install(install_args)
    except Exception as e:
        print(f'WARN: не удалось установить MCP services: {e}', file=sys.stderr)


def installed_units():
    res = []
    if UNIT_PATH_HUB.exists():   res.append((svc_hub_name(),   UNIT_PATH_HUB))
    if UNIT_PATH_SHELLMCP.exists(): res.append((svc_shellmcp_name(), UNIT_PATH_SHELLMCP))
    for unit in frpc_installed_units(env_read()):
        if unit[1].exists(): res.append(unit)
    if UNIT_PATH_CLOUDFLARED.exists(): res.append((svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED))
    return res


def autoupdate_unit_pairs():
    if IS_MACOS:
        return [(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)]
    return [(SYSTEMD_AUTO_UPDATE, UNIT_PATH_AUTO_UPDATE), (SYSTEMD_AUTO_UPDATE_TIMER, UNIT_PATH_AUTO_UPDATE_TIMER)]


def svc_autoupdate_enable_start(env: dict):
    # Always write the service unit (needed for manual update button).
    write_autoupdate_unit(env)

    if not auto_update_enabled(env):
        svc_autoupdate_disable_stop()
        return

    svc_daemon_reload()
    if IS_MACOS:
        timer_enable(SVC_AUTO_UPDATE_LABEL)
    else:
        timer_enable(SYSTEMD_AUTO_UPDATE_TIMER)


def svc_autoupdate_disable_stop():
    if IS_MACOS:
        timer_disable(SVC_AUTO_UPDATE_LABEL)
    else:
        timer_disable(SYSTEMD_AUTO_UPDATE_TIMER)
        svc_daemon_reload()


def print_autoupdate_status(env: dict):
    enabled = auto_update_enabled(env)
    state = c_green('enabled') if enabled else c_red('disabled')
    print(f'  {c_dim("Auto-update:")} {state} interval={auto_update_interval_seconds(env)}s')
    existing = [(n, p) for n, p in autoupdate_unit_pairs() if p.exists()]
    if existing:
        svc_status_multi(existing)


def cmd_autoupdate(args):
    need_root()
    env = env_read()
    action = getattr(args, 'action', None) or 'status'
    if action == 'status':
        print_header('GPTAdmin Auto-update')
        print_autoupdate_status(env)
        return
    if action == 'enable':
        env['GPTADMIN_AUTO_UPDATE'] = 'true'
        env.setdefault('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC', '21600')
        env_set_many(env)
        svc_autoupdate_enable_start(env)
        print('GPTAdmin auto-update enabled.')
        return
    if action == 'disable':
        env['GPTADMIN_AUTO_UPDATE'] = 'false'
        env_set_many(env)
        svc_autoupdate_disable_stop()
        print('GPTAdmin auto-update disabled.')
        return
    if action == 'run':
        args.hub = False
        args.shellmcp = False
        args.no_hub = False
        args.no_shellmcp = False
        args.pkg_all = None
        args.pkg_hub = None
        args.pkg_shellmcp = None
        args.auto = True
        cmd_update(args)
        return
    die('unknown auto-update action. Use: status, enable, disable, run')


def cmd_config_shellmcp(args):
    need_root()
    env = env_read()
    if args.hub_url:
        ensure_https(args.hub_url)
        env['HUB_URL'] = args.hub_url.rstrip('/')
    if not env.get('HUB_URL'):
        url = ask('Введите HUB_URL (публичный HTTPS адрес хаба, например, https://gptadmin.example.com)')
        ensure_https(url)
        env['HUB_URL'] = url.rstrip('/')
    transport = args.transport
    if not transport:
        configure_shellmcp_transport(env, install_hub=False, install_shellmcp=True)
    else:
        hub = env['HUB_URL'].rstrip('/')
        env['SHELLMCP_TRANSPORT'] = transport
        if transport == 'polling':
            env['SHELLMCP_QUEUE'] = '1'
            env['SHELLMCP_HEARTBEAT'] = '0'
            env['HB_INTERVAL_S'] = '3600'
            env['QUEUE_URL'] = hub + '/queue'
            env['SHELLMCP_URL'] = ''
            env.setdefault('SHELLMCP_BIND', '127.0.0.1')
        elif transport == 'webhook':
            env['SHELLMCP_QUEUE'] = '0'
            env['SHELLMCP_HEARTBEAT'] = '1' if args.heartbeat else '0'
            env['HB_INTERVAL_S'] = '3600'
            env.pop('QUEUE_URL', None)
            env['SHELLMCP_URL'] = args.shellmcp_url or env.get('SHELLMCP_URL') or f"http://{first_ip()}:{env.get('SHELLMCP_PORT', '25900')}"
        elif transport == 'websocket':
            env['SHELLMCP_QUEUE'] = '0'
            env['SHELLMCP_HEARTBEAT'] = '1' if args.heartbeat else '0'
            env['HB_INTERVAL_S'] = '3600'
            env.pop('QUEUE_URL', None)
            env['WS_URL'] = hub.replace('https://', 'wss://').replace('http://', 'ws://') + '/ws/shellmcp'
            env['SHELLMCP_URL'] = ''
    env_set_many(env)
    if UNIT_PATH_SHELLMCP.exists():
        svc_restart(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
    print('ShellMCP transport configured:')
    print(f"  SHELLMCP_TRANSPORT={env.get('SHELLMCP_TRANSPORT', 'polling')}")
    print(f"  HUB_URL={env.get('HUB_URL', '')}")
    if env.get('QUEUE_URL'):
        print(f"  QUEUE_URL={env['QUEUE_URL']}")
    if env.get('SHELLMCP_URL'):
        print(f"  SHELLMCP_URL={env['SHELLMCP_URL']}")


cmd_config_shell = cmd_config_shellmcp  # legacy internal alias


def cmd_version(_):
    """Print version and build info."""
    try:
        v = (Path(__file__).parent / 'VERSION').read_text().strip()
    except Exception:
        v = 'unknown'
    print(f'GPTAdmin {c_bold(v)}')
    import platform
    print(f'  {c_dim("Python:")}    {platform.python_version()}')
    print(f'  {c_dim("Platform:")}  {platform.system()} {platform.machine()}')
    mode = 'user' if _early_install_mode_user() else 'system'
    print(f'  {c_dim("Mode:")}      {mode}')
    home = str(INSTALL_DIR) if INSTALL_DIR else 'not set'
    print(f'  {c_dim("Home:")}      {home}')

def _doctor_service_runtime(label: str) -> tuple[str, str]:
    """Probe one installed service without exposing service-manager output."""
    try:
        if IS_MACOS:
            result = run(['launchctl', 'list', label], check=False, capture=True, timeout=3)
            if result.returncode == 0:
                return 'ok', 'launchd job is loaded'
            return 'error', 'launchd job is not loaded'
        if IS_WINDOWS:
            result = run(['schtasks', '/Query', '/TN', label, '/FO', 'LIST', '/NH'], check=False, capture=True, timeout=3)
            output = (result.stdout or '').lower()
            if result.returncode == 0 and ('running' in output or 'выполняется' in output):
                return 'ok', 'Windows task is running'
            if result.returncode == 0:
                return 'warning', 'Windows task is registered but not reported running'
            return 'error', 'Windows task is not available'
        command = ['systemctl']
        if IS_USER_INSTALL:
            command.append('--user')
        command.extend(['is-active', label])
        result = run(command, check=False, capture=True, timeout=3)
        state = (result.stdout or '').strip().lower()
        if result.returncode == 0 and state == 'active':
            return 'ok', 'systemd service is active'
        if state in {'failed', 'inactive', 'deactivating', 'dead'}:
            return 'error', f'systemd service is {state}'
        return 'warning', 'systemd runtime state is unavailable'
    except (OSError, subprocess.SubprocessError):
        return 'warning', 'service manager is unavailable'


def _doctor_shellmcp_unit_contract(path: Path) -> tuple[str, str] | None:
    """Detect a legacy rootd binary hidden behind the canonical ShellMCP unit."""
    try:
        lines = path.read_text(encoding='utf-8').splitlines()
    except OSError:
        return 'warning', 'ShellMCP unit could not be inspected'
    exec_lines = [line.strip() for line in lines if line.strip().startswith('ExecStart=')]
    if any(re.search(r'/rootd-go(?:-canary)?(?:\s|$)', line) for line in exec_lines):
        return 'error', 'legacy ShellMCP binary is configured; run gptadmin update'
    return None


def _doctor_local_hub_health(host: str, port: str) -> tuple[str, str]:
    """Require an explicit local Hub bind to answer its health contract."""
    probe_host = host.strip()
    if probe_host in {'', '0.0.0.0', '::'}:
        probe_host = '127.0.0.1'
    url_host = f'[{probe_host}]' if ':' in probe_host and not probe_host.startswith('[') else probe_host
    try:
        request = urllib.request.Request(f'http://{url_host}:{int(port)}/healthz', headers={'Accept': 'application/json'})
        with urllib.request.urlopen(request, timeout=3) as response:
            payload = json.loads(response.read().decode('utf-8', 'replace') or '{}')
            if response.status != 200 or not isinstance(payload, dict) or payload.get('ok') is not True:
                raise ValueError('invalid Hub health response')
    except Exception:
        return 'error', 'configured Hub port does not answer the local health contract'
    return 'ok', 'local Hub health passed'


def _doctor_report() -> dict:
    """Collect service, configuration and local Hub readiness checks."""
    checks = []
    issues = 0

    units = installed_units()
    if not units:
        checks.append({'name': 'services', 'status': 'error', 'message': 'No services installed'})
        issues += 1
    else:
        for label, path in units:
            if path.exists():
                checks.append({'name': f'service:{label}', 'status': 'ok', 'message': 'unit installed'})
                runtime_status, runtime_message = _doctor_service_runtime(label)
                checks.append({'name': f'service_runtime:{label}', 'status': runtime_status, 'message': runtime_message})
                if runtime_status == 'error':
                    issues += 1
                if label == svc_shellmcp_name():
                    unit_contract = _doctor_shellmcp_unit_contract(path)
                    if unit_contract is not None:
                        contract_status, contract_message = unit_contract
                        checks.append({'name': 'shellmcp_unit', 'status': contract_status, 'message': contract_message})
                        if contract_status == 'error':
                            issues += 1
            else:
                checks.append({'name': f'service:{label}', 'status': 'error', 'message': 'unit missing'})
                issues += 1

    env = env_read()
    if env.get('ADMIN_PASSWORD', ''):
        checks.append({'name': 'admin_password', 'status': 'ok', 'message': 'configured'})
    else:
        checks.append({'name': 'admin_password', 'status': 'error', 'message': 'not configured'})
        issues += 1
    if env.get('CTL_TOKEN'):
        checks.append({'name': 'legacy_bearer', 'status': 'warning', 'message': 'present; migrate to AdminPassword/OAuth'})

    hub_url = env.get('HUB_URL', env.get('PUBLIC_ORIGIN', ''))
    if hub_url:
        checks.append({'name': 'hub_url', 'status': 'ok', 'message': 'configured'})
    else:
        checks.append({'name': 'hub_url', 'status': 'error', 'message': 'not set'})
        issues += 1

    version_path = Path(__file__).parent / 'VERSION'
    try:
        local_version = version_path.read_text(encoding='utf-8').strip()
    except OSError:
        local_version = 'unknown'
    checks.append({'name': 'version', 'status': 'ok' if local_version != 'unknown' else 'warning', 'message': f'local build {local_version}'})

    remote_build = None
    if hub_url:
        try:
            health_url = hub_url.rstrip('/') + '/healthz'
            request = urllib.request.Request(health_url, headers={'Accept': 'application/json'})
            with urllib.request.urlopen(request, timeout=3) as response:
                body = response.read().decode('utf-8', 'replace')
                remote = json.loads(body) if body else {}
                if response.status != 200 or not isinstance(remote, dict) or remote.get('ok') is not True:
                    raise RuntimeError(f'HTTP {response.status}')
                remote_build = remote.get('build_version')
                checks.append({'name': 'remote_health', 'status': 'ok', 'message': f'Hub reachable at {hub_url}'})
                remote_date = response.headers.get('Date', '')
                if remote_date:
                    remote_clock = parsedate_to_datetime(remote_date).timestamp()
                    drift = abs(time.time() - remote_clock)
                    clock_status = 'ok' if drift <= 120 else 'error'
                    checks.append({'name': 'remote_clock', 'status': clock_status, 'message': f'clock drift {drift:.0f}s'})
                    if clock_status == 'error':
                        issues += 1
                else:
                    checks.append({'name': 'remote_clock', 'status': 'warning', 'message': 'Hub did not provide a Date header'})
        except Exception as exc:
            checks.append({'name': 'remote_health', 'status': 'error', 'message': f'Hub health check failed: {exc}'})
            issues += 1
            checks.append({'name': 'remote_clock', 'status': 'warning', 'message': 'unavailable because Hub health failed'})
    else:
        checks.append({'name': 'remote_health', 'status': 'warning', 'message': 'skipped because Hub URL is not configured'})
        checks.append({'name': 'remote_clock', 'status': 'warning', 'message': 'skipped because Hub URL is not configured'})

    auth_token = str(env.get('CTL_TOKEN', '')).strip()
    if hub_url and auth_token:
        try:
            auth_url = hub_url.rstrip('/') + '/admin/api/overview'
            auth_request = urllib.request.Request(
                auth_url,
                headers={'Accept': 'application/json', 'Authorization': f'Bearer {auth_token}'},
            )
            with urllib.request.urlopen(auth_request, timeout=3) as response:
                if response.status != 200:
                    raise RuntimeError(f'HTTP {response.status}')
                payload = json.loads(response.read().decode('utf-8', 'replace') or '{}')
                if not isinstance(payload, dict):
                    raise RuntimeError('invalid JSON response')
            checks.append({'name': 'remote_auth', 'status': 'ok', 'message': 'authenticated Hub probe passed'})
        except Exception:
            checks.append({'name': 'remote_auth', 'status': 'error', 'message': 'authenticated Hub probe failed'})
            issues += 1
    elif hub_url:
        checks.append({'name': 'remote_auth', 'status': 'warning', 'message': 'not probed; no machine credential is configured'})

    if ENV_FILE.exists():
        mode = stat.S_IMODE(ENV_FILE.stat().st_mode)
        if mode & 0o077:
            checks.append({'name': 'env_permissions', 'status': 'error', 'message': f'{ENV_FILE.name} is too permissive'})
            issues += 1
        else:
            checks.append({'name': 'env_permissions', 'status': 'ok', 'message': f'{ENV_FILE.name} is private'})
    else:
        checks.append({'name': 'env_permissions', 'status': 'warning', 'message': 'env file is not present'})

    hub_port = env.get('HUB_PORT', '9001')
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(1)
        result = sock.connect_ex(('127.0.0.1', int(hub_port)))
        sock.close()
        if result == 0:
            checks.append({'name': 'hub_port', 'status': 'ok', 'message': f'port {hub_port} is listening'})
        else:
            checks.append({'name': 'hub_port', 'status': 'error', 'message': f'port {hub_port} is not listening'})
            issues += 1
    except (OSError, TypeError, ValueError) as exc:
        checks.append({'name': 'hub_port', 'status': 'error', 'message': f'invalid port configuration: {exc}'})
        issues += 1

    local_host = env.get('HUB_HOST', env.get('HUB_BIND', '')).strip()
    if local_host:
        local_health_status, local_health_message = _doctor_local_hub_health(local_host, hub_port)
        checks.append({'name': 'hub_local_health', 'status': local_health_status, 'message': local_health_message})
        if local_health_status == 'error':
            issues += 1

    return {'ok': issues == 0, 'issues': issues, 'hub_url': hub_url or None, 'remote_build': remote_build, 'checks': checks}


BACKUP_FORMAT = 'gptadmin.backup/v1'
BACKUP_MANIFEST_NAME = 'manifest.json'
BACKUP_MAX_FILE_BYTES = 64 << 20


def _backup_member_name(value: str) -> str:
    """Validate and normalize one archive-relative POSIX member name."""
    name = str(value)
    path = PurePosixPath(name)
    if not name or name.startswith('/') or '\\' in name or name != path.as_posix() or any(part in {'', '.', '..'} for part in path.parts):
        raise ValueError(f'unsafe archive member: {name!r}')
    return path.as_posix()


def _backup_files(source: Path) -> list[tuple[str, Path, int]]:
    """Return regular files below source with validated relative names."""
    source = source.resolve()
    if not source.is_dir():
        raise ValueError(f'backup source is not a directory: {source}')
    files: list[tuple[str, Path, int]] = []
    for path in sorted(source.rglob('*')):
        relative = path.relative_to(source).as_posix()
        if path.is_symlink():
            raise ValueError(f'backup source contains symlink: {relative}')
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError(f'backup source contains unsupported entry: {relative}')
        size = path.stat().st_size
        if size > BACKUP_MAX_FILE_BYTES:
            raise ValueError(f'backup file is too large: {relative}')
        files.append((_backup_member_name(relative), path, stat.S_IMODE(path.stat().st_mode)))
    return files


def _backup_manifest(source: Path) -> dict:
    """Build a stable manifest of regular files under source."""
    entries = []
    for relative, path, mode in _backup_files(source):
        digest = hashlib.sha256()
        with path.open('rb') as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b''):
                digest.update(chunk)
        entries.append({'path': relative, 'size': path.stat().st_size, 'sha256': digest.hexdigest(), 'mode': mode})
    entries.sort(key=lambda item: item['path'])
    return {'format': BACKUP_FORMAT, 'files': entries}


def _validate_backup_manifest(manifest: object) -> dict:
    """Validate the untrusted manifest object and return its canonical shape."""
    if not isinstance(manifest, dict) or manifest.get('format') != BACKUP_FORMAT or not isinstance(manifest.get('files'), list):
        raise ValueError('invalid backup manifest format')
    entries = []
    seen: set[str] = set()
    for item in manifest['files']:
        if not isinstance(item, dict):
            raise ValueError('invalid backup manifest entry')
        relative = _backup_member_name(item.get('path', ''))
        if relative in seen:
            raise ValueError(f'duplicate backup member: {relative}')
        seen.add(relative)
        size = item.get('size')
        mode = item.get('mode')
        digest = item.get('sha256')
        if not isinstance(size, int) or size < 0 or size > BACKUP_MAX_FILE_BYTES:
            raise ValueError(f'invalid backup size: {relative}')
        if not isinstance(mode, int) or mode < 0 or mode > 0o7777:
            raise ValueError(f'invalid backup mode: {relative}')
        if not isinstance(digest, str) or not re.fullmatch(r'[0-9a-f]{64}', digest):
            raise ValueError(f'invalid backup digest: {relative}')
        entries.append({'path': relative, 'size': size, 'sha256': digest, 'mode': mode})
    entries.sort(key=lambda item: item['path'])
    return {'format': BACKUP_FORMAT, 'files': entries}


def _backup_tar_info(name: str, size: int, mode: int) -> tarfile.TarInfo:
    info = tarfile.TarInfo(name)
    info.size = size
    info.mode = mode
    info.mtime = 0
    info.uid = 0
    info.gid = 0
    info.uname = ''
    info.gname = ''
    return info


def create_backup_archive(source: Path, archive: Path) -> dict:
    """Create an atomic, manifest-first configuration backup archive."""
    source = Path(source).resolve()
    archive = Path(archive).resolve()
    try:
        archive.relative_to(source)
    except ValueError:
        pass
    else:
        raise ValueError('backup archive must be outside its source directory')
    manifest = _backup_manifest(source)
    archive.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary_name = tempfile.mkstemp(prefix=f'.{archive.name}.', suffix='.tmp', dir=archive.parent)
    os.close(fd)
    temporary = Path(temporary_name)
    try:
        with temporary.open('wb') as raw:
            with gzip.GzipFile(fileobj=raw, mode='wb', mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode='w') as handle:
                    manifest_bytes = json.dumps(manifest, sort_keys=True, separators=(',', ':')).encode('utf-8')
                    manifest_info = _backup_tar_info(BACKUP_MANIFEST_NAME, len(manifest_bytes), 0o600)
                    handle.addfile(manifest_info, io.BytesIO(manifest_bytes))
                    for item in manifest['files']:
                        path = source / item['path']
                        info = _backup_tar_info(item['path'], item['size'], item['mode'])
                        with path.open('rb') as file_handle:
                            handle.addfile(info, file_handle)
        os.chmod(temporary, 0o600)
        os.replace(temporary, archive)
    finally:
        temporary.unlink(missing_ok=True)
    return manifest


def verify_backup_archive(archive: Path) -> dict:
    """Verify archive safety, member set, sizes and SHA-256 digests."""
    archive = Path(archive)
    with tarfile.open(archive, 'r:gz') as handle:
        members = handle.getmembers()
        manifest_members = [member for member in members if member.name == BACKUP_MANIFEST_NAME]
        if len(manifest_members) != 1 or not manifest_members[0].isreg():
            raise ValueError('backup manifest is missing or invalid')
        manifest_payload = handle.extractfile(manifest_members[0])
        if manifest_payload is None:
            raise ValueError('backup manifest cannot be read')
        manifest = _validate_backup_manifest(json.loads(manifest_payload.read()))
        expected = {item['path']: item for item in manifest['files']}
        actual: dict[str, tarfile.TarInfo] = {}
        for member in members:
            if member.name == BACKUP_MANIFEST_NAME:
                continue
            name = _backup_member_name(member.name)
            if not member.isreg():
                raise ValueError(f'unsafe archive member: {member.name!r}')
            if name in actual:
                raise ValueError(f'duplicate archive member: {name}')
            actual[name] = member
        if set(actual) != set(expected):
            raise ValueError('backup members do not match manifest')
        for name, item in expected.items():
            member = actual[name]
            if member.size != item['size']:
                raise ValueError(f'backup size mismatch: {name}')
            digest = hashlib.sha256()
            stream = handle.extractfile(member)
            if stream is None:
                raise ValueError(f'backup member cannot be read: {name}')
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
            if digest.hexdigest() != item['sha256']:
                raise ValueError(f'backup digest mismatch: {name}')
    return manifest


def restore_backup_archive(archive: Path, target: Path) -> dict:
    """Atomically restore a verified archive into a new target directory."""
    manifest = verify_backup_archive(archive)
    target = Path(target).resolve()
    if target.exists():
        raise ValueError('restore target must not already exist')
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix=f'.{target.name}.', dir=target.parent))
    try:
        with tarfile.open(archive, 'r:gz') as handle:
            for item in manifest['files']:
                member = handle.getmember(item['path'])
                destination = temporary / item['path']
                destination.parent.mkdir(parents=True, exist_ok=True)
                stream = handle.extractfile(member)
                if stream is None:
                    raise ValueError(f'backup member cannot be read: {item["path"]}')
                with destination.open('xb') as output:
                    shutil.copyfileobj(stream, output)
                os.chmod(destination, item['mode'])
        if _backup_manifest(temporary) != manifest:
            raise ValueError('restored files do not match backup manifest')
        os.replace(temporary, target)
    except Exception:
        shutil.rmtree(temporary, ignore_errors=True)
        raise
    return manifest


def cmd_backup(args):
    try:
        if args.backup_cmd == 'create':
            manifest = create_backup_archive(Path(args.source), Path(args.archive))
            print(json.dumps({'ok': True, 'archive': str(Path(args.archive)), 'file_count': len(manifest['files']), 'format': BACKUP_FORMAT}))
        elif args.backup_cmd == 'verify':
            manifest = verify_backup_archive(Path(args.archive))
            print(json.dumps({'ok': True, 'archive': str(Path(args.archive)), 'file_count': len(manifest['files']), 'format': BACKUP_FORMAT}))
        elif args.backup_cmd == 'restore':
            manifest = restore_backup_archive(Path(args.archive), Path(args.target))
            print(json.dumps({'ok': True, 'target': str(Path(args.target)), 'file_count': len(manifest['files']), 'format': BACKUP_FORMAT}))
        else:
            die('backup command is required: create, verify or restore')
    except (OSError, ValueError, tarfile.TarError, json.JSONDecodeError) as exc:
        die(f'backup failed: {exc}')


def cmd_doctor(args):
    """Health check — services, ports, config, tokens."""
    report = _doctor_report()
    if args.json:
        print(json.dumps(report, ensure_ascii=False, sort_keys=True))
        return

    print_header('GPTAdmin Doctor')
    for check in report['checks']:
        message = check['message']
        if check['name'] == 'hub_url' and check['status'] == 'ok':
            message = f"Hub URL: {report['hub_url']}"
        elif check['name'] == 'legacy_bearer':
            message = "Legacy Hub bearer is present and remains valid until explicitly rotated or removed."
        elif check['name'].startswith('service:'):
            message = f"{check['name'].split(':', 1)[1]} — {message}"
        elif check['name'] == 'admin_password':
            message = f"AdminPassword is {message}"
        elif check['name'] == 'hub_url' and check['status'] == 'error':
            message = 'Hub URL is not set (needed for agents to connect)'
        if check['status'] == 'ok':
            print_ok(message)
        elif check['status'] == 'warning':
            print_warn(message)
        else:
            print_err(message)
    print()
    if report['ok']:
        print_ok('All checks passed.')
    else:
        print_warn(f"{report['issues']} issue(s) found. Fix them before proceeding.")

def cmd_status(_):
    units = installed_units()
    if not units:
        print_warn('Нет установленных сервисов.')
        print_info('Запусти: ' + c_bold('gptadmin setup'))
        return
    print_header('GPTAdmin Status')
    svc_status_multi(units)
    env = env_read()
    hub_url = env.get('HUB_URL', env.get('PUBLIC_ORIGIN', ''))
    if hub_url:
        print(f'  {c_dim("Hub URL:")} {c_cyan(hub_url)}')
    tunnel = env.get('TUNNEL_MODE', '')
    if tunnel:
        print(f'  {c_dim("Tunnel:")}  {c_green(tunnel)}')
    print_autoupdate_status(env)

def startup_instructions_path() -> Path:
    """Return the startup-instructions path for the selected install scope."""
    return STARTUP_INSTRUCTIONS_FILE


def read_startup_instructions(path: Path | None = None) -> str:
    path = path or startup_instructions_path()
    try:
        stat = path.stat()
    except FileNotFoundError:
        die(f'startup instructions file does not exist: {path}')
    if not path.is_file() or stat.st_size > STARTUP_INSTRUCTIONS_MAX_BYTES:
        die(f'startup instructions must be a regular file of at most {STARTUP_INSTRUCTIONS_MAX_BYTES} bytes')
    data = path.read_bytes()
    if len(data) > STARTUP_INSTRUCTIONS_MAX_BYTES:
        die(f'startup instructions exceed {STARTUP_INSTRUCTIONS_MAX_BYTES} bytes')
    try:
        return data.decode('utf-8')
    except UnicodeDecodeError:
        die('startup instructions must be UTF-8 text')


def set_startup_instructions_file(source: Path, destination: Path | None = None) -> Path:
    destination = destination or startup_instructions_path()
    try:
        stat = source.stat()
    except FileNotFoundError:
        die(f'source file does not exist: {source}')
    if not source.is_file() or stat.st_size > STARTUP_INSTRUCTIONS_MAX_BYTES:
        die(f'source must be a regular file of at most {STARTUP_INSTRUCTIONS_MAX_BYTES} bytes')
    data = source.read_bytes()
    if not data or len(data) > STARTUP_INSTRUCTIONS_MAX_BYTES:
        die(f'source must contain 1-{STARTUP_INSTRUCTIONS_MAX_BYTES} bytes')
    try:
        data.decode('utf-8')
    except UnicodeDecodeError:
        die('source must be UTF-8 text')

    destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.startup_instructions.', dir=destination.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'wb') as handle:
            fd = -1
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, destination)
        os.chmod(destination, 0o600)
    finally:
        if fd >= 0:
            os.close(fd)
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
    return destination


def cmd_instructions(args):
    path = startup_instructions_path()
    if args.instructions_cmd == 'path':
        print(path)
        return
    if args.instructions_cmd == 'show':
        # This is the only instructions command that emits file contents.
        sys.stdout.write(read_startup_instructions(path))
        return
    if args.instructions_cmd == 'set-file':
        written = set_startup_instructions_file(Path(args.source).expanduser(), path)
        print(f'Startup instructions installed: {written}')
        print('Restart the hub to apply them: gptadmin hub restart')
        return
    die('choose one of: show, path, set-file')


def cmd_start(_):
    need_root()
    svc_start_multi(installed_units())

def cmd_stop(_):
    need_root()
    svc_stop_multi(installed_units())

def cmd_restart(_):
    need_root()
    for name, path in installed_units():
        svc_restart(name, path)

def cmd_enable(_):
    need_root()
    for name, path in installed_units():
        svc_enable_start(name, path)
    env = env_read()
    env.setdefault('GPTADMIN_AUTO_UPDATE', 'true')
    env_set_many(env)
    svc_autoupdate_enable_start(env)

def cmd_disable(_):
    need_root()
    for name, path in installed_units():
        svc_disable_stop(name, path)
    svc_autoupdate_disable_stop()

def _log_file(label: str) -> Path:
    if IS_MACOS:
        return LOG_DIR / f'{label.split(".")[-1]}.log'
    return None  # journalctl handles it on Linux

def cmd_logs(args):
    svc = args.service
    if svc in ('shellmcp', 'shell', 'shell-mcp'):
        svc = 'shell'
    if svc in ('cloudflare', 'cloudflared', 'cf'):
        svc = 'cloudflared'
    if svc not in ('hub', 'shell', 'frpc', 'cloudflared', 'all'):
        die('unknown service. Use: hub, shellmcp, shell, frpc, cloudflared, all')
    if IS_MACOS:
        mapping = {
            'hub':   (SVC_HUB_LABEL, UNIT_PATH_HUB, _log_file(SVC_HUB_LABEL)),
            'shell': (svc_shellmcp_name(), UNIT_PATH_SHELLMCP, _log_file(svc_shellmcp_name())),
            'frpc':  (SVC_FRPC_LABEL, UNIT_PATH_FRPC, _log_file(SVC_FRPC_LABEL)),
            'cloudflared': (SVC_CLOUDFLARED_LABEL, UNIT_PATH_CLOUDFLARED, cloudflared_log_file()),
        }
        if svc == 'all':
            svc_logs_all(list(mapping.values()))
        else:
            label, path, log_file = mapping[svc]
            svc_logs_one(label, log_file)
    else:
        name_map = {
            'hub':   SYSTEMD_HUB,
            'shell': SYSTEMD_SHELLMCP,
            'frpc':  SYSTEMD_FRPC,
            'cloudflared': SYSTEMD_CLOUDFLARED,
        }
        if svc == 'all':
            units = installed_units()
            svc_logs_all([(n, p, None) for n, p in units])
        else:
            svc_logs_one(name_map[svc])

def cmd_tokens(args):
    env = env_read()
    show_shell = getattr(args, 'show_shellmcp', False) if hasattr(args, 'show_shellmcp') else False
    print_header('GPTAdmin Connections')
    print(f'  Hub URL              {env.get("HUB_URL", env.get("PUBLIC_ORIGIN", c_dim("(not set)")))}')
    # Never print internal credential names or prefixes in normal status output.
    for k in sorted(env):
        if k.startswith('GPTADMIN_') and k.endswith('_MCP_BEARER'):
            label = k.replace('GPTADMIN_', '').replace('_MCP_BEARER', '').lower()
            print(f'  MCP connection       {c_cyan(label)}: {c_green("configured") if env[k] else c_red("(not set)")}')
    shell_tok = env.get('SHELLMCP_TOKEN', '')
    if show_shell:
        print(f'  Agent connection      {c_green("configured") if shell_tok else c_red("(not set)")}')
        print_warn('The agent credential is internal and is never displayed.')
    else:
        print(f'  Agent connection      {c_yellow("configured (hidden)") if shell_tok else c_red("(not set)")}')
    bridge = env.get('MCP_BRIDGE_KEY', '')
    if bridge and bridge != env.get('CTL_TOKEN', ''):
        print(f'  Network bridge        {c_green("configured (hidden)")}')
    print()
    if env.get('CTL_TOKEN'):
        print_warn('Legacy Hub bearer is configured and remains valid until explicitly rotated or removed.')
    print(c_dim('  Issue new MCP token:  gptadmin token issue <name>'))
    print(c_dim('  Rotate tokens:        gptadmin token rotate [shellmcp]'))

def cmd_rotate(args):
    need_root()
    which = args.which
    if which in ('shellmcp', 'shell', 'shell-mcp'):
        which = 'shellmcp'
    if which == 'hub':
        die('Hub credential rotation is an explicit global invalidation operation; use the security UI only when every affected client is ready to reconnect.')
    if which not in ('hub', 'shellmcp'):
        die('unknown token target. Use: shellmcp')
    newtok = gen_hex()
    env_set_many({'SHELLMCP_TOKEN': newtok})
    if UNIT_PATH_SHELLMCP.exists():
        svc_restart(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
    print('ShellMCP token rotated (значение не выводится).')

def cmd_port(args):
    need_root()
    port = str(args.port)
    env = env_read()
    env['HUB_PORT'] = port
    env_set_many(env)
    if UNIT_PATH_HUB.exists():
        svc_restart(svc_hub_name(), UNIT_PATH_HUB)
    if UNIT_PATH_FRPC.exists() and env.get('FRP_ENABLE', 'false') == 'true':
        write_frpc_conf(env)
        svc_frpc_restart_all(env)
    if UNIT_PATH_CLOUDFLARED.exists() and (env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true'):
        cloudflared_bin = ensure_cloudflared_installed()
        write_cloudflared_unit(cloudflared_bin, env)
        svc_restart(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED)
    print(f'Локальный порт хаба изменён на {port}.')

def cmd_seturl(args):
    need_root()
    url = args.url
    ensure_https(url)
    env = env_read(); env.update({'HUB_PUBLIC_URL': url, 'HUB_URL': url, 'FRP_ENABLE': 'false', 'CLOUDFLARE_TUNNEL_ENABLE': 'false', 'TUNNEL_MODE': 'manual'}); sync_oauth_origin_env(env); env_set_many(env)
    if UNIT_PATH_SHELLMCP.exists():
        svc_restart(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
    if UNIT_PATH_FRPC.exists():
        svc_frpc_disable_stop_all(env)
    if UNIT_PATH_CLOUDFLARED.exists():
        svc_disable_stop(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED)
    print(f'HUB_PUBLIC_URL/HUB_URL = {url}; tunnels disabled.')

# FRP subcommands

def cmd_tunnel_status(_):
    units = []
    units.extend(frpc_installed_units(env_read()))
    if UNIT_PATH_CLOUDFLARED.exists():
        units.append((svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED))
    if units:
        svc_status_multi(units)
    else:
        print('Tunnel не сконфигурирован. Запусти: gptadmin setup')


def _read_env_file(path: Path) -> dict:
    out: dict[str, str] = {}
    try:
        text = path.read_text()
    except FileNotFoundError:
        return out
    except PermissionError:
        return out
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith('#') or '=' not in line:
            continue
        k, v = line.split('=', 1)
        out[k.strip()] = v.strip().strip('"').strip("'")
    return out


def _urls_env() -> tuple[dict, list[str]]:
    warnings: list[str] = []
    try:
        env = env_read()
    except PermissionError:
        env = {}
        warnings.append(f'cannot read {ENV_FILE}; try sudo gptadmin --system urls')
    system_env = Path('/etc/gptadmin/gptadmin.env')
    if not env and ENV_FILE != system_env and system_env.exists():
        fallback = _read_env_file(system_env)
        if fallback:
            env = fallback
            warnings.append('using /etc/gptadmin/gptadmin.env fallback')
        else:
            warnings.append('system config exists but is not readable; try sudo gptadmin --system urls')
    return env, warnings


def _rstrip_url(url: str) -> str:
    return str(url or '').strip().rstrip('/')


def canonical_public_url(value: str) -> str:
    """Normalize the public Hub identity shared by CLI-issued JWTs and Hub."""
    raw = str(value or '').strip()
    if not raw:
        return ''
    parsed = urllib.parse.urlsplit(raw)
    if parsed.scheme.lower() not in {'http', 'https'} or not parsed.hostname:
        return raw.rstrip('/')
    try:
        port = parsed.port
    except ValueError:
        return raw.rstrip('/')
    hostname = parsed.hostname.lower()
    if ':' in hostname and not hostname.startswith('['):
        hostname = f'[{hostname}]'
    netloc = hostname if port is None else f'{hostname}:{port}'
    return urllib.parse.urlunsplit((parsed.scheme.lower(), netloc, parsed.path.rstrip('/'), '', ''))


def _join_url(base: str, path: str) -> str:
    base = _rstrip_url(base)
    if not base:
        return ''
    return base + '/' + path.lstrip('/')


def _url_http_json(url: str, bearer: str = '', timeout: float = 5.0) -> dict | None:
    if not url:
        return None
    try:
        headers = {'User-Agent': 'gptadmin-cli-urls/1'}
        if bearer:
            headers['Authorization'] = 'Bearer ' + bearer
        req = urllib.request.Request(url, headers=headers)
        with urllib.request.urlopen(req, timeout=timeout) as r:  # noqa: S310 - admin configured URL
            if not (200 <= int(getattr(r, 'status', 0)) < 300):
                return None
            return json.loads(r.read().decode())
    except Exception:
        return None


def _urls_local_hub(env: dict) -> str:
    hub = _rstrip_url(env.get('HUB_URL') or '')
    if hub:
        return hub
    port = str(env.get('HUB_PORT') or '9001').strip() or '9001'
    return f'http://127.0.0.1:{port}'


def _urls_public_hub(env: dict) -> str:
    for key in ('HUB_PUBLIC_URL', 'PUBLIC_ORIGIN', 'MCP_RESOURCE'):
        value = _rstrip_url(env.get(key) or '')
        if value:
            return value
    hub = _rstrip_url(env.get('HUB_URL') or '')
    return hub if hub.startswith('https://') else ''


def _urls_overview(local_hub: str, env: dict) -> dict | None:
    token = env.get('CTL_TOKEN') or env.get('GPTADMIN_CTL_TOKEN') or ''
    if not token:
        return None
    return _url_http_json(_join_url(local_hub, '/admin/api/overview?limit=1'), token, timeout=5.0)


def _urls_server_rows(overview: dict | None) -> list[dict]:
    if not isinstance(overview, dict):
        return []
    rows = overview.get('servers') or overview.get('agents') or []
    return [r for r in rows if isinstance(r, dict)]


def _urls_endpoint_rows(public_hub: str, servers: list[dict], include_all: bool) -> list[dict]:
    rows: list[dict] = []
    if public_hub:
        rows.append({'kind': 'hub', 'name': 'public hub', 'url': public_hub})
        rows.append({'kind': 'hub', 'name': 'health', 'url': _join_url(public_hub, '/healthz')})
        rows.append({'kind': 'hub', 'name': 'admin', 'url': _join_url(public_hub, '/admin')})
        rows.append({'kind': 'mcp', 'name': 'full hub MCP', 'url': _join_url(public_hub, '/server/hub/mcp')})
        rows.append({'kind': 'action', 'name': 'full hub OpenAPI Action', 'url': _join_url(public_hub, '/actions/openapi.yaml')})
    for item in servers:
        sid = str(item.get('server_id') or item.get('agent_id') or item.get('id') or '')
        meta = item.get('meta') if isinstance(item.get('meta'), dict) else {}
        slug = str(meta.get('public_mcp_slug') or '').strip()
        endpoint = str(meta.get('public_mcp_endpoint') or '').strip()
        if not endpoint and public_hub and slug:
            endpoint = _join_url(public_hub, f'/server/{slug}/mcp')
        is_shell = sid.startswith('shell:') or str(item.get('kind') or '').startswith('virtual_shell')
        if not include_all and not is_shell:
            continue
        if endpoint:
            rows.append({'kind': 'shellmcp' if is_shell else 'mcp', 'name': sid, 'status': item.get('status'), 'url': endpoint})
        if public_hub and slug:
            rows.append({'kind': 'action', 'name': sid + ' OpenAPI Action', 'status': item.get('status'), 'url': _join_url(public_hub, f'/server/{slug}/actions/openapi.yaml')})
    return rows


def cmd_urls(args):
    env, warnings = _urls_env()
    local_hub = _urls_local_hub(env)
    public_hub = _urls_public_hub(env)
    overview = _urls_overview(local_hub, env)
    if isinstance(overview, dict):
        public_hub = _rstrip_url(overview.get('hub_public_url') or overview.get('public_origin') or public_hub)
    servers = _urls_server_rows(overview)
    endpoints = _urls_endpoint_rows(public_hub, servers, bool(getattr(args, 'all', False)))
    payload = {
        'install_scope': INSTALL_SCOPE,
        'env_file': str(ENV_FILE),
        'local_hub_url': local_hub,
        'public_hub_url': public_hub,
        'tunnel_mode': env.get('TUNNEL_MODE') or ('frp' if env.get('FRP_ENABLE', '').lower() == 'true' else ('cloudflare' if env.get('CLOUDFLARE_TUNNEL_ENABLE', '').lower() == 'true' else '')),
        'server_count': len(servers),
        'endpoints': endpoints,
        'warnings': warnings,
    }
    if getattr(args, 'json', False):
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return
    print_header('GPTAdmin URLs')
    print(f'  install scope: {payload["install_scope"]}')
    print(f'  env file:      {payload["env_file"]}')
    print(f'  local hub:     {local_hub or "—"}')
    print(f'  public hub:    {public_hub or "—"}')
    print(f'  tunnel:        {payload["tunnel_mode"] or "—"}')
    if warnings:
        for w in warnings:
            print_warn(w)
    print()
    if public_hub:
        print_header('Hub')
        for label, url in [
            ('admin', _join_url(public_hub, '/admin')),
            ('health', _join_url(public_hub, '/healthz')),
            ('version', _join_url(public_hub, '/version')),
            ('legacy MCP', _join_url(public_hub, '/mcp')),
            ('full hub MCP', _join_url(public_hub, '/server/hub/mcp')),
            ('OpenAPI Action', _join_url(public_hub, '/actions/openapi.yaml')),
        ]:
            print(f'  {label:<18} {url}')
    else:
        print_warn('public hub URL is not configured')
    if servers:
        print()
        print_header('Per-server MCP via GPTAdmin tunnel')
        for item in servers:
            sid = str(item.get('server_id') or item.get('agent_id') or item.get('id') or '')
            meta = item.get('meta') if isinstance(item.get('meta'), dict) else {}
            slug = str(meta.get('public_mcp_slug') or '').strip()
            endpoint = str(meta.get('public_mcp_endpoint') or '').strip()
            is_shell = sid.startswith('shell:') or str(item.get('kind') or '').startswith('virtual_shell')
            if not getattr(args, 'all', False) and not is_shell:
                continue
            if not endpoint and public_hub and slug:
                endpoint = _join_url(public_hub, f'/server/{slug}/mcp')
            if not endpoint:
                continue
            status = str(item.get('status') or '?')
            label = 'shellmcp' if is_shell else 'mcp'
            print(f'  {sid} ({label}, {status})')
            print(f'    mcp:    {endpoint}')
            if public_hub and slug:
                print(f'    action: {_join_url(public_hub, f"/server/{slug}/actions/openapi.yaml")}')
    else:
        print()
        print_warn('server list is unavailable; open the Hub connection page or run with sudo/--system')

def cmd_tunnel_logs(_):
    env = env_read()
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        if IS_MACOS:
            svc_logs_one(svc_cloudflared_name(), cloudflared_log_file())
        else:
            run(['journalctl', '-u', SYSTEMD_CLOUDFLARED, '-e', '-n', '200', '-f'], check=False)
    else:
        units = frpc_installed_units(env)
        if IS_MACOS:
            svc_logs_all([(name, path, _log_file(name)) for name, path in units])
        else:
            svc_logs_all([(name, path, None) for name, path in units])

def cmd_tunnel_enable(args):
    need_root()
    env = env_read()
    env['FRP_ENABLE'] = 'true'
    env.setdefault('FRP_SERVER_ADDR', FRPC_SERVER_ADDR_DEFAULT)
    env.setdefault('FRP_SERVER_PORT', FRPC_SERVER_PORT_DEFAULT)
    env.setdefault('FRP_DOMAIN', FRPC_DOMAIN_DEFAULT)
    env.setdefault('FRP_SUBDOMAIN', gen_subdomain())
    env.setdefault('FRP_TOKEN', FRPC_TOKEN_DEFAULT)
    env.setdefault('HUB_PORT', '9001')
    env_set_many(env)

    frpc_bin = ensure_frpc_installed()
    write_frpc_conf(env)
    write_frpc_unit(frpc_bin)
    svc_daemon_reload()
    svc_frpc_enable_start_all(env)

    env['HUB_PUBLIC_URL'] = f"https://{env['FRP_SUBDOMAIN']}.{env['FRP_DOMAIN']}"
    # FRP publishes the hub for external clients. Do not clobber the local
    # hub→ShellMCP durable transport URL on bundled installs.
    if env.get('INSTALL_SHELLMCP') == 'true' and env.get('INSTALL_HUB') == 'true':
        env['HUB_URL'] = env.get('HUB_URL') or f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
    else:
        env['HUB_URL'] = env['HUB_PUBLIC_URL']
    sync_oauth_origin_env(env)
    env_set_many(env)

    print('FRP tunnel enabled.')
    print(f"FRP URL: {env['HUB_PUBLIC_URL']}")

def cmd_tunnel_disable(_):
    need_root()
    env = env_read()
    if frpc_installed_units(env):
        svc_frpc_disable_stop_all(env)
    if UNIT_PATH_CLOUDFLARED.exists():
        svc_disable_stop(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED)
    env['FRP_ENABLE'] = 'false'; env['CLOUDFLARE_TUNNEL_ENABLE'] = 'false'; env['TUNNEL_MODE'] = 'manual'; env_set_many(env)
    print('Tunnel disabled.')



def _parse_build_info_text(text: str) -> dict:
    out = {}
    patterns = {
        'build_version': r'BUILD_VERSION\s*=\s*([0-9]+)',
        'build_ts': r'BUILD_TS\s*=\s*[\"\']([^\"\']+)[\"\']',
        'git_commit': r'GIT_COMMIT\s*=\s*[\"\']([^\"\']+)[\"\']',
    }
    for key, pattern in patterns.items():
        m = re.search(pattern, text)
        if not m:
            continue
        value = m.group(1)
        if key == 'build_version':
            try:
                value = int(value)
            except ValueError:
                continue
        out[key] = value
    return out


def _read_installed_build_marker() -> dict:
    try:
        if INSTALLED_BUILD_FILE.exists():
            data = json.loads(INSTALLED_BUILD_FILE.read_text())
            if isinstance(data, dict):
                return {k: data.get(k) for k in ('build_version', 'build_ts', 'git_commit', 'sha256', 'size', 'package_url') if data.get(k) is not None}
    except Exception:
        pass
    return {}


def _write_installed_build_marker(info: dict, package_url: str):
    if not info:
        return
    try:
        payload = {k: info.get(k) for k in ('build_version', 'build_ts', 'git_commit', 'sha256', 'size') if info.get(k) is not None}
        if not payload.get('build_version'):
            return
        payload['package_url'] = package_url
        payload['installed_at'] = int(time.time())
        INSTALL_DIR.mkdir(parents=True, exist_ok=True)
        INSTALLED_BUILD_FILE.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + '\n')
        os.chmod(INSTALLED_BUILD_FILE, 0o644)
    except Exception as exc:
        print(f'WARNING: could not write installed build marker: {exc}', file=sys.stderr)


def _read_update_cache():
    """Return update check cache dict or None on any error (missing, corrupt, parse failure)."""
    try:
        raw = UPDATE_CHECK_CACHE.read_text(encoding='utf-8')
        return json.loads(raw)
    except Exception:
        return None


def _write_update_cache(data: dict):
    """Atomically write update check cache (0600)."""
    tmp = UPDATE_CHECK_CACHE.with_name(UPDATE_CHECK_CACHE.name + '.tmp')
    try:
        tmp.write_text(json.dumps(data, ensure_ascii=False), encoding='utf-8')
        os.chmod(tmp, 0o600)
        os.replace(tmp, UPDATE_CHECK_CACHE)
    except Exception:
        try:
            if tmp.exists():
                tmp.unlink()
        except Exception:
            pass


def _installed_build_info(env: dict, install_hub: bool) -> dict:
    marker_info = _read_installed_build_marker()
    if marker_info:
        return marker_info
    infos = []
    for path in (INSTALL_DIR / 'client' / 'gptadmin_build_info.py', INSTALL_DIR / 'hub_source' / 'gptadmin_build_info.py'):
        try:
            if path.exists():
                info = _parse_build_info_text(path.read_text(errors='replace'))
                if info:
                    infos.append(info)
        except Exception:
            pass
    if install_hub:
        port = env.get('HUB_PORT') or '9001'
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{port}/version', timeout=2) as r:
                if r.status == 200:
                    data = json.loads(r.read().decode('utf-8', 'replace'))
                    if isinstance(data, dict):
                        info = {k: data.get(k) for k in ('build_version', 'build_ts', 'git_commit') if data.get(k) is not None}
                        if info:
                            infos.append(info)
        except Exception:
            pass
    if not infos:
        return {}
    def version(info):
        try:
            return int(info.get('build_version') or 0)
        except Exception:
            return 0
    return max(infos, key=version)


def _artifact_name_from_url(url: str) -> str:
    local = _local_source_path(url)
    if local is not None:
        return local.name
    parsed = urllib.parse.urlparse(str(url))
    return Path(urllib.parse.unquote(parsed.path)).name


def _read_release_matrix_source(source: str) -> tuple[dict, str]:
    """Read a release matrix from a local path/file URL or HTTP(S) URL."""
    local = _local_source_path(source)
    if local is not None:
        matrix = json.loads(local.read_text(encoding='utf-8'))
        return matrix, str(local)
    with urllib.request.urlopen(source, timeout=8) as response:
        matrix = json.loads(response.read().decode('utf-8', 'replace'))
        final_url = getattr(response, 'url', source)
    return matrix, str(final_url)


def _release_manifest_bypass_enabled() -> bool:
    """Return whether the operator explicitly disabled release verification."""
    return os.environ.get('GPTADMIN_UPDATE_SKIP_MANIFEST', '').strip().lower() in {'1', 'true', 'yes', 'on'}


def _remote_artifact_build_info(pkg_url: str) -> dict:
    if _release_manifest_bypass_enabled():
        return {}
    local_pkg = _local_source_path(pkg_url)
    explicit_matrix = os.environ.get('GPTADMIN_RELEASE_MATRIX_URL')
    if explicit_matrix:
        matrix_url = explicit_matrix
    elif local_pkg is not None:
        matrix_url = str(local_pkg.with_name('gptadmin-release-matrix.json'))
    else:
        base = pkg_url.rsplit('/', 1)[0]
        matrix_url = base.rstrip('/') + '/gptadmin-release-matrix.json'
    name = _artifact_name_from_url(pkg_url)
    try:
        matrix, final_url = _read_release_matrix_source(matrix_url)
    except Exception as exc:
        print(f'WARNING: release matrix unavailable: {exc}', file=sys.stderr)
        return {}
    artifact = next(
        (item for item in (matrix.get('artifacts') or []) if isinstance(item, dict) and item.get('file') == name),
        {},
    )
    if not artifact:
        return {}
    build_version = matrix.get('build_version')
    if not build_version:
        match = re.search(r'/download/v(\d+)/', str(final_url))
        if match:
            build_version = int(match.group(1))
    return {
        key: value
        for key, value in {
            'build_version': artifact.get('build_version', build_version),
            'build_ts': matrix.get('build_ts'),
            'git_commit': artifact.get('git_commit', matrix.get('git_commit')),
            'sha256': artifact.get('sha256'),
            'size': artifact.get('size'),
        }.items()
        if value is not None
    }


def _verify_downloaded_artifact(path: Path, metadata: dict, *, require_metadata: bool = False) -> None:
    """Reject a downloaded package when a published digest/size disagrees."""
    expected_sha = str(metadata.get('sha256') or '').strip().lower()
    expected_size = metadata.get('size')
    if require_metadata and (not expected_sha or expected_size is None) and not _release_manifest_bypass_enabled():
        die(f'Проверка релиза не пройдена: manifest не содержит полный digest/size ({path.name})')
    if not expected_sha and expected_size is None:
        return
    actual_size = path.stat().st_size
    if expected_size is not None and int(expected_size) != actual_size:
        die(f'Проверка релиза не пройдена: размер пакета не совпадает ({path.name})')
    if expected_sha:
        digest = hashlib.sha256()
        with path.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        if digest.hexdigest() != expected_sha:
            die(f'Проверка релиза не пройдена: SHA-256 пакета не совпадает ({path.name})')


def _should_skip_update(installed: dict, remote: dict) -> bool:
    try:
        installed_v = int(installed.get('build_version') or 0)
        remote_v = int(remote.get('build_version') or 0)
    except Exception:
        return False
    if not (installed_v > 0 and remote_v > 0):
        return False
    if installed_v < remote_v:
        return False
    installed_sha = str(installed.get('sha256') or '').strip().lower()
    remote_sha = str(remote.get('sha256') or '').strip().lower()
    if installed_v == remote_v and installed_sha and remote_sha and installed_sha != remote_sha:
        return False
    return True


# ===== Update / in-place upgrade =====

def _service_pairs_for_update(install_hub: bool, install_shellmcp: bool, env: dict):
    # Keep ShellMCP alive while the Hub binary is replaced. ShellMCP is the
    # management/recovery plane and already reconnects to a restarted Hub.
    # Stopping it together with Hub makes every remote admin call disappear
    # exactly when rollback/recovery may be needed.
    pairs = []
    if install_hub:
        pairs.append((svc_hub_name(), UNIT_PATH_HUB))
    if env.get('FRP_ENABLE', 'false') == 'true':
        pairs.append((svc_frpc_name(), UNIT_PATH_FRPC))
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        pairs.append((svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED))
    return pairs


class _UpdateRuntimeSnapshot:
    """Private snapshot of files that an in-place update can replace."""

    def __init__(self, paths: list[Path]):
        self.root = Path(tempfile.mkdtemp(prefix='gptadmin-update-rollback-'))
        self.entries: list[tuple[Path, Path, str, bool]] = []
        self.runtime_stopped = False
        try:
            for index, source in enumerate(dict.fromkeys(Path(path) for path in paths)):
                backup = self.root / str(index)
                if source.is_symlink():
                    raise ValueError(f'update rollback source cannot be a symlink: {source}')
                if source.is_dir():
                    shutil.copytree(source, backup, symlinks=True)
                    kind, existed = 'directory', True
                elif source.is_file():
                    backup.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(source, backup)
                    kind, existed = 'file', True
                else:
                    kind, existed = 'missing', False
                self.entries.append((source, backup, kind, existed))
        except BaseException:
            self.cleanup()
            raise

    def restore(self) -> None:
        """Restore the pre-update paths, removing files created by the failed update."""

        for source, backup, kind, existed in reversed(self.entries):
            if source.is_dir() and not source.is_symlink():
                shutil.rmtree(source)
            elif source.exists() or source.is_symlink():
                source.unlink()
            if not existed:
                continue
            source.parent.mkdir(parents=True, exist_ok=True)
            if kind == 'directory':
                shutil.copytree(backup, source, symlinks=True)
            else:
                shutil.copy2(backup, source)

    def cleanup(self) -> None:
        """Remove the private snapshot directory after success or rollback."""

        shutil.rmtree(self.root, ignore_errors=True)


def _run_update_transaction(paths: list[Path], operation, *, rollback_callback=None):
    """Run an update operation with file restoration on any failure.

    The callback is intentionally invoked only after the snapshot has been
    restored, so service restart observes the previous binaries/configuration.
    """

    snapshot = _UpdateRuntimeSnapshot(paths)
    try:
        return operation()
    except BaseException:
        try:
            snapshot.restore()
        except BaseException as restore_error:
            print('WARNING: update rollback could not restore the previous runtime', file=sys.stderr)
            if rollback_callback is None:
                raise restore_error
        if rollback_callback is not None:
            rollback_callback()
        raise
    finally:
        snapshot.cleanup()


_active_update_snapshot: _UpdateRuntimeSnapshot | None = None


def _mark_update_runtime_started() -> None:
    """Mark the point after which a failed update must restart old services."""

    if _active_update_snapshot is not None:
        _active_update_snapshot.runtime_stopped = True


def _update_runtime_paths() -> list[Path]:
    """Return the installed paths touched by package replacement or restart."""

    return [
        INSTALL_DIR / 'cli',
        INSTALL_DIR / 'agents',
        INSTALL_DIR / 'client',
        INSTALL_DIR / 'public',
        BIN_DIR,
        CLI_PATH,
        ENV_FILE,
        INSTALLED_BUILD_FILE,
        FRPC_CONF,
        UNIT_PATH_HUB,
        UNIT_PATH_SHELLMCP,
        UNIT_PATH_FRPC,
        UNIT_PATH_CLOUDFLARED,
        UNIT_PATH_AUTO_UPDATE,
        globals().get('UNIT_PATH_AUTO_UPDATE_TIMER', UNIT_PATH_AUTO_UPDATE),
    ]


def _restart_update_services_after_rollback() -> None:
    """Best-effort restart of the restored service set after a failed update."""

    try:
        env = env_read()
        svc_daemon_reload()
        install_hub = env.get('INSTALL_HUB') == 'true' or UNIT_PATH_HUB.exists()
        install_shellmcp = env.get('INSTALL_SHELLMCP') == 'true' or UNIT_PATH_SHELLMCP.exists()
        for name, path in _service_pairs_for_update(install_hub, install_shellmcp, env):
            svc_enable_start(name, path)
        if install_shellmcp and UNIT_PATH_SHELLMCP.exists():
            svc_restart(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
    except BaseException:
        print('WARNING: restored runtime could not be restarted automatically', file=sys.stderr)


def _local_unified_node_enabled():
    """Detect the configured local primary, including effective systemd overrides."""
    modes = [os.environ.get('GPTADMIN_AUTH_MODE', '')]
    # primary.env is the existing unified-primary override, not an extra marker.
    # Other node instances (e.g. nodes/canary.env) do not select this primary.
    for path in (ENV_FILE, ENV_FILE.parent / 'nodes' / 'primary.env'):
        try:
            lines = path.read_text().splitlines()
        except FileNotFoundError:
            continue
        except OSError:
            die(f'cannot check unified node configuration: {path}')
        mode = ''
        for line in lines:
            key, separator, value = line.strip().partition('=')
            if separator and key.strip() == 'GPTADMIN_AUTH_MODE':
                mode = value.strip().strip('"\'').lower()
        modes.append(mode)
    if any(mode.strip().lower() in ('writer', 'reader') for mode in modes):
        return True
    if IS_MACOS or not UNIT_PATH_HUB.exists():
        return False
    # Include effective systemd drop-ins, including a Node running in legacy
    # auth mode. This is a read-only query, never a service start/restart.
    command = ['systemctl'] + (['--user'] if IS_USER_INSTALL else [])
    command += ['show', SYSTEMD_HUB, '--property=ExecStart', '--value']
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=3, check=False)
    except (OSError, subprocess.TimeoutExpired):
        die('cannot inspect Hub ExecStart before legacy update; check systemd and retry')
    if result.returncode != 0:
        die('cannot inspect Hub ExecStart before legacy update; check systemd and retry')
    return bool(re.search(r'(?:^|[/\s=])gptadmin-node(?:[\s;}]|$)', result.stdout or ''))


def _reject_legacy_update_for_unified_node():
    """Read-only fence before the legacy updater snapshots or changes anything."""
    if _local_unified_node_enabled():
        die('legacy update is disabled for a unified node; use the unified node deployment path')


def _transactional_update(func):
    """Decorate the update command with rollback-safe runtime restoration."""

    @wraps(func)
    def wrapped(args):
        global _active_update_snapshot
        need_root()
        _reject_legacy_update_for_unified_node()
        snapshot = _UpdateRuntimeSnapshot(_update_runtime_paths())
        _active_update_snapshot = snapshot
        try:
            return func(args)
        except BaseException:
            try:
                snapshot.restore()
            except BaseException:
                print('WARNING: update rollback could not restore the previous runtime', file=sys.stderr)
            if snapshot.runtime_stopped:
                _restart_update_services_after_rollback()
            raise
        finally:
            _active_update_snapshot = None
            snapshot.cleanup()

    return wrapped


def cmd_security_profile(args):
    """Read or persist the process security profile used by unit generation."""
    path = ETC_DIR / 'security_state.json'
    try:
        state = json.loads(path.read_text(encoding='utf-8')) if path.exists() else {}
    except (OSError, ValueError) as exc:
        die(f'cannot read security profile: {exc}')
    if not isinstance(state, dict):
        die('security state must be a JSON object')
    current = state.get('process_profile')
    if not isinstance(current, dict):
        current = {'mode': 'normal', 'allow_privileged_execution': True}
    if not getattr(args, 'mode', None):
        print(json.dumps({'process_profile': current}, ensure_ascii=False, indent=2))
        return

    mode = args.mode
    if mode == 'normal':
        profile = {
            'mode': mode, 'no_new_privileges': False, 'private_tmp': False,
            'protect_system': False, 'protect_home': False,
            'allow_privileged_execution': True,
        }
    elif mode == 'maximum':
        profile = {
            'mode': mode, 'no_new_privileges': True, 'private_tmp': True,
            'protect_system': True, 'protect_home': True,
            'allow_privileged_execution': False,
        }
    else:
        profile = {'mode': mode}
        for key, value in (
            ('no_new_privileges', args.no_new_privileges),
            ('private_tmp', args.private_tmp),
            ('protect_system', args.protect_system),
            ('protect_home', args.protect_home),
            ('allow_privileged_execution', args.allow_privileged_execution),
        ):
            profile[key] = bool(current.get(key, False if key != 'allow_privileged_execution' else True)) if value is None else value
        if not profile['allow_privileged_execution'] and not profile['no_new_privileges']:
            die('custom profile denying privileged execution must enable --no-new-privileges')

    state['process_profile'] = profile
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f'.{path.name}.{os.getpid()}.tmp')
    try:
        tmp.write_text(json.dumps(state, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)
    print(json.dumps({'process_profile': profile, 'restart_bound': True}, ensure_ascii=False, indent=2))


def cmd_security_bearer(args):
    """Read or persist the claim/protocol checks for signed bearers."""
    path = ETC_DIR / 'security_state.json'
    try:
        state = json.loads(path.read_text(encoding='utf-8')) if path.exists() else {}
    except (OSError, ValueError) as exc:
        die(f'cannot read security profile: {exc}')
    if not isinstance(state, dict):
        die('security state must be a JSON object')
    current = state.get('bearer_profile')
    if not isinstance(current, dict):
        current = {'mode': 'normal'}
    mode = getattr(args, 'mode', None)
    if not mode:
        print(json.dumps({'bearer_profile': current}, ensure_ascii=False, indent=2))
        return
    keys = ('issuer', 'audience', 'resource', 'scope', 'subject', 'issued_at', 'expiry', 'pkce')
    if mode == 'maximum':
        profile = {'mode': mode, **{f'require_{key}': True for key in keys}, 'enforce_token_lifecycle': True, 'enforce_redirect_allowlist': True, 'enforce_resource_allowlist': True}
    elif mode == 'normal':
        profile = {'mode': mode, 'require_issuer': False, **{f'require_{key}': True for key in keys if key != 'issuer'}, 'enforce_token_lifecycle': True, 'enforce_redirect_allowlist': False, 'enforce_resource_allowlist': False}
    else:
        profile = {'mode': mode}
        for key in keys:
            value = getattr(args, f'require_{key}', None)
            profile[f'require_{key}'] = bool(current.get(f'require_{key}', True)) if value is None else value
        for key in ('token_lifecycle', 'redirect_allowlist', 'resource_allowlist'):
            value = getattr(args, f'enforce_{key}', None)
            profile[f'enforce_{key}'] = bool(current.get(f'enforce_{key}', True)) if value is None else value
    state['bearer_profile'] = profile
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f'.{path.name}.{os.getpid()}.tmp')
    try:
        tmp.write_text(json.dumps(state, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)
    print(json.dumps({'bearer_profile': profile, 'restart_bound': True}, ensure_ascii=False, indent=2))



def _candidate_client_origin(env: dict) -> str:
    """Return the public tenant URL used by the real Custom GPT client.

    TENANT_ORIGIN wins intentionally: HUB_PUBLIC_URL/PUBLIC_ORIGIN can name the
    canonical control-plane origin, while update admission must preserve the
    already-documented client-facing tenant URL.
    """
    tenant = canonical_public_url(env.get('TENANT_ORIGIN') or '')
    if tenant:
        return tenant
    legacy = str(env.get('GPTADMIN_LEGACY_PUBLIC_ORIGINS') or '').split(',')[0].strip()
    if legacy:
        return canonical_public_url(legacy)
    return canonical_public_url(env.get('PUBLIC_ORIGIN') or env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '')


def _openapi_operation_map(text: str) -> dict[str, tuple[str, str]]:
    """Read operationId -> (method, path) from our plain OpenAPI YAML.

    This deliberately does not import server internals: the candidate gate
    discovers the public API from the same document a Custom GPT imports.
    """
    operations: dict[str, tuple[str, str]] = {}
    current_path = ''
    current_method = ''
    methods = {'get', 'post', 'put', 'patch', 'delete', 'head', 'options'}
    for raw in text.splitlines():
        stripped = raw.strip()
        indent = len(raw) - len(raw.lstrip(' '))
        if indent == 2 and stripped.startswith('/') and stripped.endswith(':'):
            current_path = stripped[:-1]
            current_method = ''
            continue
        if indent == 4 and stripped.endswith(':') and stripped[:-1].lower() in methods:
            current_method = stripped[:-1].upper()
            continue
        if indent == 6 and stripped.startswith('operationId:') and current_path and current_method:
            operation_id = stripped.split(':', 1)[1].strip().strip('"\'')
            if operation_id:
                operations[operation_id] = (current_method, current_path)
    return operations


def _candidate_http(base: str, method: str, path: str, *, host: str = '', token: str = '', payload=None, timeout: float = 12.0, max_response_bytes: int = 0):
    data = None if payload is None else json.dumps(payload).encode('utf-8')
    headers = {'Accept': 'application/json, text/yaml, */*', 'User-Agent': 'gptadmin-candidate-gate/1'}
    if host:
        headers['Host'] = host
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if data is not None:
        headers['Content-Type'] = 'application/json'
    request = urllib.request.Request(base.rstrip('/') + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=timeout) as response:  # noqa: S310 - explicit local/public update probe
        body = response.read(max_response_bytes + 1) if max_response_bytes else response.read()
        if max_response_bytes and len(body) > max_response_bytes:
            raise RuntimeError('candidate API response exceeds installer limit')
        if not 200 <= response.status < 300:
            raise RuntimeError(f'candidate API {method} {path} returned HTTP {response.status}')
        ctype = response.headers.get('Content-Type', '')
        if 'json' in ctype:
            return json.loads(body.decode('utf-8'))
        return body.decode('utf-8')


def _candidate_mcp_rpc(base: str, method: str, params: dict, *, token: str, host: str, request_id: int):
    payload = _candidate_http(
        base, 'POST', '/mcp', host=host, token=token,
        payload={'jsonrpc': '2.0', 'id': request_id, 'method': method, 'params': params},
        timeout=12.0,
    )
    if not isinstance(payload, dict):
        raise RuntimeError(f'candidate gate: MCP {method} did not return a JSON object')
    if payload.get('error'):
        raise RuntimeError(f'candidate gate: MCP {method} returned an error')
    if 'result' not in payload:
        raise RuntimeError(f'candidate gate: MCP {method} response has no result')
    return payload['result']


def _candidate_mcp_content(result) -> dict:
    if not isinstance(result, dict) or result.get('isError'):
        raise RuntimeError('candidate gate: MCP tool failed')
    payload = result.get('structuredContent')
    if not isinstance(payload, dict) or payload.get('error') or payload.get('status') == 'failed':
        raise RuntimeError('candidate gate: MCP tool returned an invalid result')
    return payload


def _require_candidate_demo(result) -> None:
    if not isinstance(result, dict) or result.get('error') or result.get('executed') is False:
        raise RuntimeError('candidate gate: demo execution failed')
    # The Actions/legacy path returns `response`; native code-mode `exec`
    # wraps the mcp.call value under `result` alongside logs and call counts.
    demo = result.get('response', result.get('result', result))
    if (not isinstance(demo, dict) or demo.get('status') != 'ok'
            or 'read_only_demo' not in demo.get('capabilities', [])):
        raise RuntimeError('candidate gate: safe demo result is missing')


def _candidate_tools(payload) -> list[dict]:
    if not isinstance(payload, dict):
        return []
    tools = payload.get('tools')
    if not isinstance(tools, list):
        response = payload.get('response')
        tools = response.get('tools') if isinstance(response, dict) else []
    return [item for item in tools if isinstance(item, dict)] if isinstance(tools, list) else []

def _require_candidate_mcp_catalog(tools) -> None:
    """Fail closed unless the installed native MCP facade is the current catalog."""
    names = {str(item.get('name') or '') for item in tools if isinstance(item, dict)} if isinstance(tools, list) else set()
    required = {'discover', 'search', 'schema', 'exec', 'ro', 'job', 'expand'}
    missing = sorted(required - names)
    if missing:
        raise RuntimeError('candidate gate: MCP core catalog is incomplete: ' + ', '.join(missing))


def _candidate_servers(payload) -> list[dict]:
    if not isinstance(payload, dict):
        return []
    rows = payload.get('servers')
    if not isinstance(rows, list):
        response = payload.get('response')
        rows = response.get('servers') if isinstance(response, dict) else []
    return [item for item in rows if isinstance(item, dict)] if isinstance(rows, list) else []


def _run_hub_candidate_pre_restart_gate(pkg_tgz: Path, env: dict) -> dict:
    """Black-box a downloaded Hub before any running Hub process is stopped.

    The public tenant URL is probed for the client-imported OpenAPI document.
    The downloaded binary then runs on an isolated loopback port with a private
    copy of state, and is exercised through both documented client surfaces:
    Custom GPT/OpenAPI (discover -> schema -> safe execute) and native MCP
    (initialize -> tools/list -> safe tools/call).
    """
    client_origin = _candidate_client_origin(env)
    if not client_origin:
        raise RuntimeError('candidate gate: client-facing TENANT_ORIGIN/public URL is missing')
    client_host = urllib.parse.urlsplit(client_origin).netloc
    # Verify the address the real client imports, not a control-plane shortcut.
    public_schema_url = client_origin.rstrip('/') + '/actions/openapi.yaml'
    req = urllib.request.Request(public_schema_url, headers={'User-Agent': 'gptadmin-candidate-gate/1'})
    with urllib.request.urlopen(req, timeout=12) as response:  # noqa: S310 - configured public client URL
        if response.status != 200:
            raise RuntimeError(f'candidate gate: client OpenAPI returned HTTP {response.status}')
        live_schema = response.read().decode('utf-8')
    if 'operationId: discover' not in live_schema or 'operationId: execute' not in live_schema:
        raise RuntimeError('candidate gate: client OpenAPI does not document discover/execute')

    with tempfile.TemporaryDirectory(prefix='gptadmin-candidate-gate-') as raw:
        root = Path(raw)
        extracted = root / 'release'
        extracted.mkdir()
        extract_tgz(pkg_tgz, extracted)
        candidate = next((path for path in _platform_hub_candidates(extracted) if path.is_file() and _binary_looks_native(path)), None)
        if candidate is None:
            raise RuntimeError('candidate gate: downloaded package contains no Hub binary for this platform')
        os.chmod(candidate, 0o755)
        candidate_public = extracted / 'public'
        if not (candidate_public / 'openapi.yaml').is_file():
            raise RuntimeError('candidate gate: downloaded package contains no public/openapi.yaml')

        config_source = Path(env.get('GPTADMIN_CONFIG_DIR') or ETC_DIR)
        config_copy = root / 'config'
        if config_source.is_dir():
            shutil.copytree(config_source, config_copy, symlinks=False)
        else:
            config_copy.mkdir()

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        local_base = f'http://127.0.0.1:{port}'
        child_env = os.environ.copy()
        child_env.update({str(k): str(v) for k, v in env.items() if v is not None})
        child_env.update({
            'GPTADMIN_HUB_HOST': '127.0.0.1', 'HUB_HOST': '127.0.0.1', 'HUB_BIND': '127.0.0.1',
            'GPTADMIN_HUB_PORT': str(port), 'HUB_PORT': str(port), 'PORT': str(port),
            'GPTADMIN_CONFIG_DIR': str(config_copy),
            'GPTADMIN_PUBLIC_DIR': str(candidate_public),
            'GPTADMIN_OUTPUT_DIR': str(config_copy / 'outputs'),
            'GPTADMIN_ENV_FILE': str(root / 'candidate.env'),
            'NO_PROXY': 'localhost,127.0.0.1', 'no_proxy': 'localhost,127.0.0.1',
        })
        process = subprocess.Popen(
            [str(candidate)], cwd=str(extracted), env=child_env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError('candidate gate: downloaded Hub exited before readiness')
                try:
                    _candidate_http(local_base, 'GET', '/version', host=client_host, timeout=1.0)
                    break
                except Exception:
                    time.sleep(0.1)
            else:
                raise RuntimeError('candidate gate: downloaded Hub did not become ready')

            candidate_schema = _candidate_http(local_base, 'GET', '/actions/openapi.yaml', host=client_host)
            if not isinstance(candidate_schema, str):
                raise RuntimeError('candidate gate: candidate OpenAPI is not text')
            ops = _openapi_operation_map(candidate_schema)
            missing = sorted({'discover', 'schema', 'execute'} - set(ops))
            if missing:
                raise RuntimeError('candidate gate: candidate OpenAPI is missing operations: ' + ', '.join(missing))

            token_env = dict(env)
            # Reproduce the real existing client's identity. This is what would
            # have caught the canonical-origin regression before restart.
            token_env['PUBLIC_ORIGIN'] = client_origin
            token_env['MCP_RESOURCE'] = client_origin
            token = make_mcp_bearer_token(token_env, 'gptadmin-candidate-preflight', ttl_days=1, access_mode='full')

            # Native MCP client contract. This is intentionally separate from the
            # Custom GPT Actions contract below: both must survive an update.
            initialized = _candidate_mcp_rpc(
                local_base, 'initialize',
                {
                    'protocolVersion': '2024-11-05',
                    'capabilities': {},
                    'clientInfo': {'name': 'gptadmin-candidate-gate', 'version': '1'},
                },
                token=token, host=client_host, request_id=1,
            )
            if not isinstance(initialized, dict) or not initialized.get('protocolVersion') or not initialized.get('serverInfo'):
                raise RuntimeError('candidate gate: MCP initialize returned an invalid result')
            initial_instructions = initialized.get('instructions')
            if not isinstance(initial_instructions, str) or not initial_instructions.strip():
                raise RuntimeError('candidate gate: MCP initialize omitted profile instructions')
            listed = _candidate_mcp_rpc(
                local_base, 'tools/list', {}, token=token, host=client_host, request_id=2,
            )
            mcp_tools = listed.get('tools') if isinstance(listed, dict) else None
            _require_candidate_mcp_catalog(mcp_tools)
            mcp_discovered = _candidate_mcp_content(_candidate_mcp_rpc(
                local_base, 'tools/call', {'name': 'discover', 'arguments': {}},
                token=token, host=client_host, request_id=3,
            ))
            if not any(str(server.get('server_id') or '') == 'hub'
                       for server in _candidate_servers(mcp_discovered)):
                raise RuntimeError('candidate gate: MCP discover omitted the hub target')
            mcp_searched = _candidate_mcp_content(_candidate_mcp_rpc(
                local_base, 'tools/call', {'name': 'search', 'arguments': {'query': 'demo'}},
                token=token, host=client_host, request_id=4,
            ))
            if mcp_searched.get('status') == 'failed':
                raise RuntimeError('candidate gate: MCP search failed after instruction delivery')
            mcp_schema = _candidate_mcp_content(_candidate_mcp_rpc(
                local_base, 'tools/call', {'name': 'schema', 'arguments': {
                    'target': 'hub', 'tool': 'demo', 'detail': 'full',
                }}, token=token, host=client_host, request_id=5,
            ))
            definitions = _candidate_tools(mcp_schema)
            if len(definitions) != 1 or definitions[0].get('name') != 'demo':
                raise RuntimeError('candidate gate: exact MCP demo schema is missing')
            mcp_called = _candidate_mcp_rpc(
                local_base, 'tools/call', {'name': 'exec', 'arguments': {
                    'code': "return await mcp.call('hub','demo',{});",
                }}, token=token, host=client_host, request_id=6,
            )
            _require_candidate_demo(_candidate_mcp_content(mcp_called))

            # Custom GPT Actions contract, discovered from the candidate's own
            # OpenAPI artifact rather than hard-coded relay paths.
            method, path = ops['discover']
            discovered = _candidate_http(local_base, method, path, host=client_host, token=token)
            servers = _candidate_servers(discovered)
            if not servers:
                raise RuntimeError('candidate gate: discover returned no targets')

            selected_target = next(
                (str(server.get('server_id') or server.get('agent_id') or server.get('id') or '')
                 for server in servers
                 if str(server.get('server_id') or server.get('agent_id') or server.get('id') or '') == 'hub'),
                '',
            )
            if not selected_target:
                raise RuntimeError('candidate gate: documented hub target is absent from discover')

            smethod, spath = ops['schema']
            schema_payload = _candidate_http(
                local_base, smethod, spath, host=client_host, token=token,
                payload={'target': selected_target, 'tool': 'demo', 'detail': 'full'}, timeout=8.0,
            )
            tools = _candidate_tools(schema_payload)
            demo = next((item for item in tools if str(item.get('name') or '') == 'demo'), None)
            if demo is None or len(tools) != 1:
                raise RuntimeError('candidate gate: hub schema does not expose the safe demo tool')
            input_schema = demo.get('inputSchema') if isinstance(demo.get('inputSchema'), dict) else {}
            if input_schema.get('required'):
                raise RuntimeError('candidate gate: safe demo unexpectedly requires arguments')

            emethod, epath = ops['execute']
            result = _candidate_http(
                local_base, emethod, epath, host=client_host, token=token,
                payload={'target': selected_target, 'tool_name': 'demo', 'arguments': {}},
                timeout=12.0,
            )
            job_id = str(result.get('job_id') or '') if isinstance(result, dict) else ''
            if job_id:
                jmethod, jpath = ops.get('job', ('GET', '/mcp-relay/job/{job_id}'))
                jpath = jpath.replace('{job_id}', urllib.parse.quote(job_id, safe=''))
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    result = _candidate_http(local_base, jmethod, jpath, host=client_host, token=token, timeout=5.0)
                    status = str(result.get('status') or '') if isinstance(result, dict) else ''
                    if status in {'completed', 'success'}:
                        break
                    if status in {'failed', 'error', 'cancelled'}:
                        raise RuntimeError(f'candidate gate: execute job failed with status {status}')
                    time.sleep(0.2)
                else:
                    raise RuntimeError('candidate gate: execute job did not complete')
            elif isinstance(result, dict):
                status = str(result.get('status') or '')
                if status and status not in {'completed', 'success'}:
                    raise RuntimeError(f'candidate gate: execute returned status {status}')
            _require_candidate_demo(result)
            return {'status': 'passed', 'client_origin': client_origin, 'target': selected_target, 'custom_gpt': 'passed', 'mcp': 'passed'}
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


def _require_real_local_shell_exec(env: dict, timeout_s: int = 90) -> dict:
    """Prove Hub -> local ShellMCP execution with one harmless real command."""
    identity = _load_local_shellmcp_identity(env, timeout_s=min(timeout_s, 30))
    if not _normalize_local_shell_identity(identity).get('public_key'):
        raise RuntimeError('local ShellMCP identity is unavailable for execution verification')
    base = f"http://127.0.0.1:{env.get('HUB_PORT') or '9001'}"
    token = make_mcp_bearer_token(env, 'gptadmin-local-runtime-verifier', ttl_days=1, access_mode='full')
    deadline = time.monotonic() + max(10, timeout_s)
    selected = ''
    while time.monotonic() < deadline:
        try:
            payload = _candidate_http(base, 'GET', '/mcp-relay/servers?detail=full', token=token, timeout=5.0)
            servers = payload.get('servers', []) if isinstance(payload, dict) else []
            selected = next((
                str(server.get('server_id') or '') for server in servers
                if isinstance(server, dict)
                and str(server.get('status') or '') == 'online'
                and _server_matches_local_shell_identity(server, identity)
            ), '')
        except Exception:
            selected = ''
        if selected:
            break
        time.sleep(0.25)
    if not selected:
        raise RuntimeError('local ShellMCP did not become online for execution verification')
    context = _installer_instruction_context(payload, lambda: _candidate_http(
        base, 'POST', '/mcp', token=token,
        payload={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list', 'params': {}}, timeout=5.0, max_response_bytes=1024 * 1024,
    ))

    # A Hub restart can strand one already-dispatched queue job in ``running``
    # even though the new ShellMCP is healthy.  Do not let one stale verifier
    # job consume the whole update deadline: issue a few independent harmless
    # probes, while still requiring one *real* Hub -> ShellMCP round-trip to
    # return its unique marker before the update is accepted.
    max_attempts = 3
    last_job_id = ''
    last_status = ''
    attempts = 0
    while attempts < max_attempts and time.monotonic() < deadline:
        attempts += 1
        expected = 'gptadmin-runtime-' + secrets.token_hex(8)
        result = _candidate_http(
            base, 'POST', '/mcp-relay/call', token=token,
            payload={
                'target': selected,
                'tool': 'shell_exec',
                'arguments': {'cmd': f"printf '{expected}'", 'run_as_user': 'root'},
                'background': True,
                **({'context': context} if context else {}),
            },
            timeout=10.0,
        )
        if not isinstance(result, dict):
            raise RuntimeError('local ShellMCP execution verifier returned an invalid response')
        job_id = str(result.get('job_id') or result.get('task_id') or '')
        last_job_id = job_id
        if not job_id:
            if expected in json.dumps(result, ensure_ascii=False):
                return {'status': 'passed', 'target': selected, 'mode': 'inline', 'attempts': attempts}
            raise RuntimeError(f'local ShellMCP execution verifier returned no job id: {result}')

        # Give each probe a bounded slice of the total deadline.  This is long
        # enough for a healthy local queue but short enough to recover from a
        # single job stranded across Hub/ShellMCP restart.
        remaining = max(0.0, deadline - time.monotonic())
        per_attempt = min(15.0, max(3.0, float(timeout_s) / max_attempts), remaining)
        attempt_deadline = time.monotonic() + per_attempt
        while time.monotonic() < attempt_deadline and time.monotonic() < deadline:
            current = _candidate_http(
                base, 'GET', '/mcp-relay/job/' + urllib.parse.quote(job_id, safe=''),
                token=token, timeout=5.0,
            )
            status = str(current.get('status') or '') if isinstance(current, dict) else ''
            last_status = status
            if status in {'completed', 'success'}:
                if expected not in json.dumps(current, ensure_ascii=False):
                    raise RuntimeError(f'local ShellMCP execution completed without expected output: {current}')
                return {
                    'status': 'passed', 'target': selected, 'job_id': job_id,
                    'mode': 'job', 'attempts': attempts,
                }
            if status in {'failed', 'error', 'cancelled'}:
                break
            time.sleep(0.25)

    detail = f'last job {last_job_id or "none"} status {last_status or "unknown"}'
    raise RuntimeError(
        f'local ShellMCP execution did not complete within {timeout_s}s '
        f'after {attempts} attempt(s) ({detail})'
    )


@_transactional_update
def cmd_update(args):
    """In-place upgrade for existing installs.

    Safe for old clients: preserves tokens, subdomain, URLs and MCP config;
    refreshes binaries/CLI/service files; backfills OAuth env required by Codex.
    """
    need_root()
    env = env_read()
    saved_auth_material = _capture_persistent_auth_material(env)
    if not env and not any(p.exists() for p in (UNIT_PATH_HUB, UNIT_PATH_SHELLMCP, CLI_PATH, BIN_DIR / 'gptadmin_hub', BIN_DIR / 'shellmcp')):
        die('GPTAdmin installation was not found. Run: gptadmin setup')

    install_hub = (env.get('INSTALL_HUB') == 'true') if 'INSTALL_HUB' in env else bool(UNIT_PATH_HUB.exists() or (BIN_DIR / 'gptadmin_hub').exists())
    install_shellmcp = (env.get('INSTALL_SHELLMCP') == 'true') if 'INSTALL_SHELLMCP' in env else bool(UNIT_PATH_SHELLMCP.exists() or (BIN_DIR / 'shellmcp').exists())
    if args.hub:
        install_hub = True
    if args.shellmcp:
        install_shellmcp = True
    if args.no_hub:
        install_hub = False
    if args.no_shellmcp:
        install_shellmcp = False
    if not install_hub and not install_shellmcp:
        die('No installed components detected. Use --hub and/or --shellmcp, or run: gptadmin setup')
    install_grepmesh = _grepmesh_enabled_from_args(args, env, install_shellmcp)
    env['GREPMESH_ENABLE'] = 'true' if install_grepmesh else 'false'

    env.setdefault('SHELLMCP_TOKEN', gen_hex())
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    env.setdefault('GPTADMIN_SECURITY_MODE', 'normal')
    env['INSTALL_HUB'] = 'true' if install_hub else 'false'
    env['INSTALL_SHELLMCP'] = 'true' if install_shellmcp else 'false'
    normalize_bundled_shellmcp_hub_url(env, install_hub, install_shellmcp)
    env.setdefault('GPTADMIN_AUTO_UPDATE', 'true')
    env.setdefault('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC', '21600')
    if getattr(args, 'auto', False) and not auto_update_enabled(env):
        print('GPTAdmin auto-update is disabled; skipping automatic update.')
        return
    if install_shellmcp:
        ensure_shellmcp_default_user(env)
        ensure_shellmcp_identity_env(env)
        env.setdefault('SHELLMCP_AUTO_UPDATE', '1')
        env.setdefault('SHELLMCP_SELF_REPAIR_DISABLE', '0')
        env.setdefault('SHELLMCP_LEGACY_MANIFEST_UPDATE', '0')
        hub_for_update = (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or 'https://became.bezrabotnyi.com').rstrip('/')
        env['SHELLMCP_UPDATE_MANIFEST_URL'] = hub_for_update + '/artifacts/shellmcp.json'
        env['SHELLMCP_UPDATE_TOKEN'] = env.get('SHELLMCP_UPDATE_TOKEN') or env.get('SHELLMCP_TOKEN', '')
        env['SHELLMCP_SERVICE_NAME'] = svc_shellmcp_name()
        env['SHELLMCP_SERVICE_SCOPE'] = INSTALL_SCOPE
        if not IS_USER_INSTALL:
            env.setdefault('SHELLMCP_FILE_CHECKPOINT_ROOT', '/var/lib/gptadmin/file-checkpoints')
    sync_oauth_origin_env(env)
    env_set_many(env)

    pkg_all = args.pkg_all or platform_pkg_url_default()
    pkg_hub = args.pkg_hub or platform_pkg_url_default('full')
    pkg_shellmcp = args.pkg_shellmcp or platform_pkg_url_default('client')

    target_pkg = pkg_all if (install_hub and install_shellmcp) else (pkg_hub if install_hub else pkg_shellmcp)
    remote_info = _remote_artifact_build_info(target_pkg)
    if not _release_manifest_bypass_enabled() and (not remote_info.get('sha256') or remote_info.get('size') is None):
        die(f'Проверка релиза не пройдена: manifest недоступен или не содержит полный digest/size ({_artifact_name_from_url(target_pkg)})')
    if not getattr(args, 'force', False):
        installed_info = _installed_build_info(env, install_hub)
        if _should_skip_update(installed_info, remote_info):
            print(
                'GPTAdmin already up to date: '
                f"installed build_version={installed_info.get('build_version')} "
                f"git_commit={installed_info.get('git_commit') or 'unknown'}; "
                f"latest build_version={remote_info.get('build_version')} "
                f"git_commit={remote_info.get('git_commit') or 'unknown'}."
            )
            print('Use `gptadmin update --force` to reinstall anyway.')
            return

    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)

        def download_release(url: str, destination: Path) -> None:
            """Download one package and enforce its published manifest digest."""
            metadata = _remote_artifact_build_info(url)
            download(url, destination)
            _verify_downloaded_artifact(destination, metadata, require_metadata=True)

        if install_hub and install_shellmcp:
            print('[Update] downloading full package...')
            pkg = tdp / 'all.tgz'
            try:
                download_release(pkg_all, pkg)
            except subprocess.CalledProcessError:
                if pkg_all == PKG_ALL_URL_DEFAULT:
                    raise
                print('  Platform package unavailable, using full package...')
                download_release(PKG_ALL_URL_DEFAULT, pkg)
            components = ('hub', 'shellmcp')
        elif install_hub:
            print('[Update] downloading hub package...')
            pkg = tdp / 'hub.tgz'
            try:
                download_release(pkg_hub, pkg)
            except subprocess.CalledProcessError:
                print('  Component package unavailable, using full package...')
                download_release(pkg_all, pkg)
            components = ('hub',)
        elif install_shellmcp:
            print('[Update] downloading shellmcp package...')
            pkg = tdp / 'shellmcp.tgz'
            try:
                download_release(pkg_shellmcp, pkg)
            except subprocess.CalledProcessError:
                print('  Component package unavailable, using full package...')
                download_release(pkg_all, pkg)
            components = ('shellmcp',)

        # Admission gate: the downloaded Hub must pass the client-facing public
        # contract while the old Hub is still serving traffic. A failed
        # candidate is rejected here; no Hub service has been stopped yet.
        if install_hub:
            print('[Update] black-box testing downloaded Hub before restart...')
            gate = _run_hub_candidate_pre_restart_gate(pkg, env)
            print(f"  Candidate gate passed via {gate['client_origin']} ({gate['target']})")

        # Only a candidate that passed the external contract may cross the
        # service-stop boundary.
        print('Stopping installed GPTAdmin services for safe in-place update...')
        _mark_update_runtime_started()
        svc_stop_multi(_service_pairs_for_update(install_hub, install_shellmcp, env))
        for component in components:
            install_component_from_pkg(pkg, component)

    # Package payloads must never be able to invalidate existing Hub JWTs or
    # client credentials. Restore the pre-update auth state before services
    # restart, then persist it atomically through env_set_many.
    env = env_read()
    _restore_persistent_auth_material(env, saved_auth_material)
    env_set_many(env)

    _write_installed_build_marker(remote_info, target_pkg)
    _cleanup_obsolete_runtime_files()

    write_hub_unit(install_hub, install_shellmcp)
    write_shellmcp_unit(install_hub, install_shellmcp)
    grepmesh_ready = _configure_builtin_grepmesh(env, install_grepmesh) if install_shellmcp else False
    write_grepmesh_unit(grepmesh_ready, env)
    if env.get('FRP_ENABLE', 'false') == 'true':
        frpc_bin = ensure_frpc_installed()
        write_frpc_conf(env)
        write_frpc_unit(frpc_bin)
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        cloudflared_bin = ensure_cloudflared_installed()
        write_cloudflared_unit(cloudflared_bin, env)

    svc_daemon_reload()
    if install_hub:
        svc_enable_start(svc_hub_name(), UNIT_PATH_HUB)
        if not wait_local_hub_health(env, timeout_s=90):
            raise RuntimeError('local Hub health check failed after update')
        if not IS_USER_INSTALL and UNIT_PATH_HUB_STANDBY.exists():
            svc_enable_start(svc_hub_standby_name(), UNIT_PATH_HUB_STANDBY)
    if env.get('FRP_ENABLE', 'false') == 'true':
        svc_frpc_enable_start_all(env)
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        svc_enable_start(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED)
    if grepmesh_ready:
        if UNIT_PATH_GREPMESH.exists():
            svc_enable_start(svc_grepmesh_name(), UNIT_PATH_GREPMESH)
    elif UNIT_PATH_GREPMESH.exists():
        svc_disable_stop(svc_grepmesh_name(), UNIT_PATH_GREPMESH)
    if install_shellmcp:
        if UNIT_PATH_SHELLMCP.exists():
            svc_restart(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
        else:
            svc_enable_start(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
        # An update is not healthy merely because systemd started ShellMCP.
        # Require a fresh, cryptographically matching poll to the restarted Hub
        # and re-approve only this machine's persisted identity if necessary.
        # This prevents returning success while the shell plane is stale/pending.
        maybe_autoapprove_local_shellmcp(env_read(), install_hub, install_shellmcp)
        if install_hub:
            verified = _require_real_local_shell_exec(env_read(), timeout_s=90)
            print(f"Post-update runtime execution verified: {verified['target']}")
    if not getattr(args, 'auto', False):
        svc_autoupdate_enable_start(env_read())
    # A new desktop client should work after an ordinary update, without making
    # the user rediscover the Hub URL or a client-specific transport command.
    auto_configure_ai_mcp_clients(env_read(), install_hub)

    env = env_read()
    print('GPTAdmin updated in-place.')
    if install_hub:
        print(f"Hub URL: {env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '—'}")
        print(f"OAuth resource: {env.get('MCP_RESOURCE') or env.get('PUBLIC_ORIGIN') or '—'}")


# ===== AI client MCP auto-configuration =====

DEFAULT_MCP_TOKEN_TTL_DAYS = 5 * 365


def _b64url_bytes(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b'=').decode()


def _b64url_json(obj: dict) -> str:
    return _b64url_bytes(json.dumps(obj, separators=(',', ':')).encode())


def make_mcp_bearer_token(env: dict, client_id: str, ttl_days: int = DEFAULT_MCP_TOKEN_TTL_DAYS, access_mode: str = 'full') -> str:
    secret = env.get('OAUTH_CLIENT_SECRET') or ''
    if not secret:
        raise RuntimeError('OAUTH_CLIENT_SECRET is missing')
    origin = canonical_public_url(env.get('PUBLIC_ORIGIN') or env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '')
    resource = canonical_public_url(env.get('MCP_RESOURCE') or origin)
    if not origin or not resource:
        raise RuntimeError('PUBLIC_ORIGIN/MCP_RESOURCE is missing')
    now = int(time.time())
    ttl_days = max(1, int(ttl_days or DEFAULT_MCP_TOKEN_TTL_DAYS))
    access_mode = str(access_mode or 'full').strip().lower()
    if access_mode not in {'full', 'readonly'}:
        raise ValueError('access_mode must be full or readonly')
    scope = 'gptadmin.read gptadmin.exec' if access_mode == 'full' else 'gptadmin.read gptadmin.inspect'
    header = {'alg': 'HS256', 'typ': 'JWT'}
    body = {
        'sub': 'admin',
        'scope': scope,
        'access_mode': access_mode,
        'client_id': client_id,
        'iss': origin,
        'aud': resource,
        # Hub validates both OAuth audience and protected-resource binding.
        # Keep this CLI fallback wire-compatible with Hub-issued tokens.
        'resource': resource,
        'iat': now,
        # Match the Hub verifier's explicit key-id contract.  Installations
        # that rotate the JWT signing key configure the same value here.
        'kid': env.get('GPTADMIN_JWT_KEY_ID') or 'gptadmin-hs256-v1',
    }
    body['exp'] = now + ttl_days * 24 * 3600
    signing_input = f'{_b64url_json(header)}.{_b64url_json(body)}'.encode()
    sig = hmac.new(secret.encode(), signing_input, hashlib.sha256).digest()
    return signing_input.decode() + '.' + _b64url_bytes(sig)


def _mcp_client_url(env: dict) -> str:
    # ShellMCP can intentionally use a loopback HUB_URL while desktop clients
    # need the canonical externally reachable Hub identity.
    base = canonical_public_url(env.get('HUB_PUBLIC_URL') or env.get('PUBLIC_ORIGIN') or env.get('HUB_URL') or '')
    if not base:
        base = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
    return base + '/mcp'


def _client_token_env_key(client_id: str) -> str:
    safe = re.sub(r'[^A-Za-z0-9]+', '_', client_id.upper()).strip('_') or 'CUSTOM'
    return f'GPTADMIN_{safe}_MCP_BEARER'


def issue_mcp_bearer(env: dict, client_id: str, ttl_days: int = DEFAULT_MCP_TOKEN_TTL_DAYS, access_mode: str = 'full') -> tuple[str, str, str]:
    env = dict(env)
    if not (env.get('HUB_URL') or env.get('HUB_PUBLIC_URL') or env.get('PUBLIC_ORIGIN')):
        env['HUB_URL'] = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
    sync_oauth_origin_env(env)
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    token = make_mcp_bearer_token(env, client_id, ttl_days=ttl_days, access_mode=access_mode)
    return token, _mcp_client_url(env), _client_token_env_key(client_id)


def cmd_mcp_token(args):
    need_root()
    env = env_read()
    if not (env.get('HUB_URL') or env.get('HUB_PUBLIC_URL') or env.get('PUBLIC_ORIGIN')):
        env['HUB_URL'] = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
    sync_oauth_origin_env(env)
    client_id = str(getattr(args, 'name', '') or '').strip()
    if not client_id:
        client_id = ask('MCP token name / client_id', 'custom-mcp-client').strip() or 'custom-mcp-client'
    ttl_days = int(getattr(args, 'ttl_days', DEFAULT_MCP_TOKEN_TTL_DAYS) or DEFAULT_MCP_TOKEN_TTL_DAYS)
    access_mode = 'readonly' if bool(getattr(args, 'readonly', False)) else 'full'
    token, url, default_key = issue_mcp_bearer(env, client_id, ttl_days=ttl_days, access_mode=access_mode)
    env_key = str(getattr(args, 'env_key', '') or default_key).strip()
    save = not bool(getattr(args, 'no_save', False))
    update = {
        'HUB_URL': env.get('HUB_URL', ''),
        'PUBLIC_ORIGIN': env.get('PUBLIC_ORIGIN') or env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '',
        'MCP_RESOURCE': env.get('MCP_RESOURCE') or env.get('PUBLIC_ORIGIN') or env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '',
        'ADMIN_PASSWORD': env.get('ADMIN_PASSWORD') or gen_hex(),
        'OAUTH_CLIENT_SECRET': env.get('OAUTH_CLIENT_SECRET') or gen_hex(32),
    }
    if save:
        update[env_key] = token
    env_set_many(update)
    if save:
        _set_process_env_for_gui_clients({env_key: token})
    print(f'MCP token issued: {client_id}')
    print(f'URL: {url}')
    print(f'Env: {env_key}' + ('' if save else '  # not saved'))
    print(f'Expires in: {ttl_days} days')
    print(f'Access: {"read-only inspection" if access_mode == "readonly" else "full"}')
    print(f'Authorization: Bearer {token}')


def cmd_auth_diagnose(args):
    """Print the token/origin contract without exposing credentials."""
    env = env_read()
    origin = canonical_public_url(env.get('PUBLIC_ORIGIN') or env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '')
    resource = canonical_public_url(env.get('MCP_RESOURCE') or origin)
    secret = env.get('OAUTH_CLIENT_SECRET') or ''
    print('Auth diagnostics (secrets redacted)')
    print(f'CLI env file: {ENV_FILE}')
    print(f'Hub env file: {env.get("GPTADMIN_ENV_FILE") or ENV_FILE}')
    print(f'Public origin: {origin or "<missing>"}')
    print(f'MCP resource: {resource or "<missing>"}')
    print(f'Expected issuer: {origin or "<missing>"}')
    print(f'Expected audience: {resource or "<missing>"}')
    print(f'Expected resource: {resource or "<missing>"}')
    print(f'Signing key fingerprint: {hashlib.sha256(secret.encode()).hexdigest()[:16] if secret else "<missing>"}')
    if origin and not bool(getattr(args, 'offline', False)):
        metadata = _url_http_json(_join_url(origin, '/.well-known/oauth-protected-resource'), timeout=5.0)
        if isinstance(metadata, dict):
            advertised_resource = canonical_public_url(str(metadata.get('resource') or ''))
            servers = metadata.get('authorization_servers')
            advertised_issuer = canonical_public_url(str(servers[0])) if isinstance(servers, list) and servers else ''
            print(f'Hub advertised issuer: {advertised_issuer or "<missing>"}')
            print(f'Hub advertised resource: {advertised_resource or "<missing>"}')
            if advertised_issuer != origin or advertised_resource != resource:
                print('Hub metadata verdict: configured origin/resource mismatch')
            else:
                print('Hub metadata verdict: matches configured contract')
        else:
            print('Hub metadata verdict: unavailable')
    token = str(getattr(args, 'token', '') or '').strip()
    if not token:
        return
    parts = token.split('.')
    if len(parts) != 3:
        print('Token verdict: malformed JWT')
        return
    try:
        header = json.loads(base64.urlsafe_b64decode(parts[0] + '=' * (-len(parts[0]) % 4)))
        claims = json.loads(base64.urlsafe_b64decode(parts[1] + '=' * (-len(parts[1]) % 4)))
    except Exception:
        print('Token verdict: malformed JWT payload')
        return
    signing_input = f'{parts[0]}.{parts[1]}'.encode()
    valid_signature = bool(secret) and hmac.compare_digest(_b64url_bytes(hmac.new(secret.encode(), signing_input, hashlib.sha256).digest()), parts[2])
    print(f'Token key id: {header.get("kid") or claims.get("kid") or "<missing>"}')
    print(f'Token issuer: {claims.get("iss") or "<missing>"}')
    print(f'Token audience: {claims.get("aud") or "<missing>"}')
    print(f'Token resource: {claims.get("resource") or "<missing>"}')
    if not valid_signature:
        print('Token verdict: invalid signature')
    elif claims.get('iss') and canonical_public_url(str(claims.get('iss'))) != origin:
        print('Token verdict: issuer mismatch')
    elif canonical_public_url(str(claims.get('aud') or '')) != resource:
        print('Token verdict: audience mismatch')
    elif canonical_public_url(str(claims.get('resource') or '')) != resource:
        print('Token verdict: resource mismatch')
    elif str(header.get('kid') or claims.get('kid') or '') != (env.get('GPTADMIN_JWT_KEY_ID') or 'gptadmin-hs256-v1'):
        print('Token verdict: key id mismatch')
    else:
        print('Token verdict: local contract accepted')


def configure_ai_mcp_clients(env: dict, *, rotate: bool = False, clients: set[str] | None = None, print_custom: bool = False) -> dict:
    env = dict(env)
    sync_oauth_origin_env(env)
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    wanted = clients or {'claude-code', 'codex', 'opencode', 'vscode', 'hermes', 'openclaw', 'zed', 'fast-agent', 'zcode'}
    tokens = {
        'GPTADMIN_CLAUDE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_CLAUDE_MCP_BEARER')) or make_mcp_bearer_token(env, 'claude-code'),
        'GPTADMIN_CODEX_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_CODEX_MCP_BEARER')) or make_mcp_bearer_token(env, 'codex'),
        'GPTADMIN_OPENCODE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_OPENCODE_MCP_BEARER')) or make_mcp_bearer_token(env, 'opencode'),
        'GPTADMIN_VSCODE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_VSCODE_MCP_BEARER')) or make_mcp_bearer_token(env, 'vscode'),
        'GPTADMIN_HERMES_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_HERMES_MCP_BEARER')) or make_mcp_bearer_token(env, 'hermes-agent'),
        'GPTADMIN_OPENCLAW_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_OPENCLAW_MCP_BEARER')) or make_mcp_bearer_token(env, 'openclaw-assistant'),
        'GPTADMIN_ZED_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_ZED_MCP_BEARER')) or make_mcp_bearer_token(env, 'zed'),
        'GPTADMIN_FAST_AGENT_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_FAST_AGENT_MCP_BEARER')) or make_mcp_bearer_token(env, 'fast-agent'),
        'GPTADMIN_ZCODE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_ZCODE_MCP_BEARER')) or make_mcp_bearer_token(env, 'zcode'),
        'GPTADMIN_CUSTOM_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_CUSTOM_MCP_BEARER')) or make_mcp_bearer_token(env, 'custom-mcp-client'),
    }
    env.update(tokens)
    env_remove_keys(['GPTADMIN_MCP_BEARER'])
    env_set_many({
        'PUBLIC_ORIGIN': env.get('PUBLIC_ORIGIN', ''),
        'MCP_RESOURCE': env.get('MCP_RESOURCE', ''),
        'ADMIN_PASSWORD': env.get('ADMIN_PASSWORD', ''),
        'OAUTH_CLIENT_SECRET': env.get('OAUTH_CLIENT_SECRET', ''),
        **tokens,
    })
    _set_process_env_for_gui_clients(tokens)
    url = _mcp_client_url(env)
    results: dict[str, str] = {}
    if 'claude-code' in wanted or 'claude' in wanted:
        results['claude-code'] = _configure_claude_code_mcp(url, tokens['GPTADMIN_CLAUDE_MCP_BEARER'])
    if 'codex' in wanted:
        results['codex'] = _configure_codex_mcp(url, tokens['GPTADMIN_CODEX_MCP_BEARER'])
    if 'opencode' in wanted:
        results['opencode'] = _configure_opencode_mcp(url, tokens['GPTADMIN_OPENCODE_MCP_BEARER'])
    if 'vscode' in wanted:
        results['vscode'] = _configure_vscode_mcp(url, tokens['GPTADMIN_VSCODE_MCP_BEARER'])
    if 'hermes' in wanted:
        results['hermes'] = _configure_hermes_mcp(url, tokens['GPTADMIN_HERMES_MCP_BEARER'])
    if 'openclaw' in wanted:
        results['openclaw'] = _configure_openclaw_mcp(url, tokens['GPTADMIN_OPENCLAW_MCP_BEARER'])
    if 'zed' in wanted:
        results['zed'] = _configure_zed_mcp(url, tokens['GPTADMIN_ZED_MCP_BEARER'])
    if 'fast-agent' in wanted:
        results['fast-agent'] = _configure_fast_agent_mcp(url, tokens['GPTADMIN_FAST_AGENT_MCP_BEARER'])
    if 'zcode' in wanted:
        results['zcode'] = _configure_zcode_mcp(url, tokens['GPTADMIN_ZCODE_MCP_BEARER'])
    results['_url'] = url
    if print_custom:
        results['_custom_token'] = tokens['GPTADMIN_CUSTOM_MCP_BEARER']
    return results


def cmd_mcp_connect(args):
    need_root()
    env = env_read()
    selected = set(getattr(args, 'client', None) or [])
    aliases = {'claude': 'claude-code'}
    selected = {aliases.get(x, x) for x in selected}
    if not selected:
        selected = {'claude-code', 'codex', 'opencode', 'vscode'}
    results = configure_ai_mcp_clients(env, rotate=bool(getattr(args, 'fresh', False)), clients=selected)
    url = results.pop('_url')
    print('GPTAdmin MCP client install: ' + ', '.join(f'{k}={v}' for k, v in results.items()))
    print(f'URL: {url}')


def _run_quiet(cmd: list[str], env: dict | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, check=False)


def _merge_env_for_client(tokens: dict[str, str] | None = None) -> dict:
    child_env = os.environ.copy()
    for key, token in (tokens or {}).items():
        child_env[key] = token
    return child_env


def _set_process_env_for_gui_clients(tokens: dict[str, str]) -> None:
    if IS_MACOS and shutil.which('launchctl'):
        for key, token in tokens.items():
            _run_quiet(['launchctl', 'setenv', key, token])


def _configure_codex_mcp(url: str, token: str) -> str:
    if not shutil.which('codex'):
        return 'skip: codex not found'
    env = _merge_env_for_client({'GPTADMIN_CODEX_MCP_BEARER': token})
    _run_quiet(['codex', 'mcp', 'remove', 'gptadmin'], env=env)
    res = _run_quiet(['codex', 'mcp', 'add', 'gptadmin', '--url', url, '--bearer-token-env-var', 'GPTADMIN_CODEX_MCP_BEARER'], env=env)
    if res.returncode != 0:
        return 'error: ' + ((res.stderr or res.stdout).strip() or f'codex rc={res.returncode}')
    return 'ok'


def _configure_claude_code_mcp(url: str, token: str) -> str:
    if not shutil.which('claude'):
        return 'skip: claude not found'
    env = _merge_env_for_client({'GPTADMIN_CLAUDE_MCP_BEARER': token})
    _run_quiet(['claude', 'mcp', 'remove', '--scope', 'user', 'gptadmin'], env=env)
    _run_quiet(['claude', 'mcp', 'remove', '--scope', 'local', 'gptadmin'], env=env)
    res = _run_quiet(['claude', 'mcp', 'add', '--scope', 'user', '--transport', 'http', 'gptadmin', url, '--header', f'Authorization: Bearer {token}'], env=env)
    if res.returncode != 0:
        return 'error: ' + ((res.stderr or res.stdout).strip() or f'claude rc={res.returncode}')
    return 'ok'


def _configure_opencode_mcp(url: str, token: str) -> str:
    if not shutil.which('opencode') and not (USER_HOME / '.config' / 'opencode').exists():
        return 'skip: opencode not found'
    cfg_dir = USER_HOME / '.config' / 'opencode'
    cfg_dir.mkdir(parents=True, exist_ok=True)
    cfg = cfg_dir / 'opencode.json'
    data: dict = {}
    if cfg.exists():
        try:
            data = json.loads(cfg.read_text())
        except Exception as e:
            return f'error: cannot parse {cfg}: {e}'
    data.setdefault('mcp', {})
    data['mcp']['gptadmin'] = {
        'type': 'remote',
        'url': url,
        'enabled': True,
        'headers': {'Authorization': f'Bearer {token}'},
    }
    if cfg.exists():
        backup = cfg.with_suffix(cfg.suffix + '.bak.gptadmin-mcp.' + time.strftime('%Y%m%d_%H%M%S'))
        shutil.copy2(cfg, backup)
    cfg.write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')
    try:
        os.chmod(cfg, 0o600)
    except Exception:
        pass
    return 'ok'


def _configure_vscode_mcp(url: str, token: str) -> str:
    """Register GPTAdmin as a global remote MCP server in VS Code."""
    if not shutil.which('code'):
        return 'skip: VS Code not found'
    config = {
        'name': 'gptadmin',
        'type': 'http',
        'url': url,
        'headers': {'Authorization': f'Bearer {token}'},
    }
    res = _run_quiet(['code', '--add-mcp', json.dumps(config, separators=(',', ':'))])
    if res.returncode != 0:
        return 'error: ' + ((res.stderr or res.stdout).strip() or f'code rc={res.returncode}')
    return 'ok'


def _configure_hermes_mcp(url: str, token: str) -> str:
    """Register GPTAdmin as a remote MCP server in Hermes (Nous/Nous-platform agent).

    Mirrors what claude mcp add does for Claude Code: a non-interactive
    `hermes mcp add <name> --url ... --auth header --env KEY=VAL` call.
    The auth header is sourced from the bearer token via an environment
    variable so the value never lands on the command line.
    """
    if not shutil.which('hermes'):
        return 'skip: hermes CLI not found'
    env_var = 'GPTADMIN_GPTADMIN_MCP_BEARER'
    env = _merge_env_for_client({env_var: token})
    _run_quiet(['hermes', 'mcp', 'remove', 'gptadmin'], env=env)
    res = _run_quiet(
        ['hermes', 'mcp', 'add', 'gptadmin',
         '--url', url,
         '--auth', 'header',
         '--env', f'{env_var}={token}'],
        env=env,
    )
    if res.returncode != 0:
        err = ((res.stderr or res.stdout).strip() or f'hermes rc={res.returncode}')
        # If `hermes mcp add` rejected `--env` for HTTP transport, fall back
        # to writing the header-value directly (still better than nothing).
        if 'env is only supported' in err or 'stdio' in err:
            res = _run_quiet(
                ['hermes', 'mcp', 'add', 'gptadmin',
                 '--url', url,
                 '--auth', 'header'],
                env=env,
            )
            err = ((res.stderr or res.stdout).strip() or f'hermes rc={res.returncode}')
            if res.returncode == 0:
                _run_quiet(['hermes', 'config', 'set', f'mcp_servers.gptadmin.headers.Authorization', f'Bearer {token}'], env=env)
                return 'ok'
        return 'error: ' + err
    return 'ok'


def _configure_openclaw_mcp(url: str, token: str) -> str:
    """Register GPTAdmin as a remote MCP server in OpenClaw (HA add-on assistant).

    OpenClaw runs as a Home Assistant add-on. Its config is managed via the
    HA Supervisor API (POST /core/api/services/<domain>/<service>). The
    add-on slug on this host is `64fc532e_openclaw_assistant`. Update this
    helper once the canonical registration endpoint is documented in
    docs/INTEGRATIONS.md.
    """
    return (
        'skip: openclaw is a Home Assistant add-on; configure via the '
        'HA Supervisor UI or POST /core/api/services/persistent_notification/... '
        '(see docs/INTEGRATIONS.md)'
    )


def _configure_zed_mcp(url: str, token: str) -> str:
    """Register GPTAdmin as a remote MCP server in Zed (context_server).

    Zed stores MCP servers under "context_servers" in ~/.config/zed/settings.json
    using one entry per server, with an HTTP transport exposing URL + headers.
    The token is written into the JSON file; that path is fine because the
    settings file is mode 0600 on first write.
    """
    cfg_dir = USER_HOME / '.config' / 'zed'
    cfg_dir.mkdir(parents=True, exist_ok=True)
    cfg = cfg_dir / 'settings.json'
    data: dict = {}
    if cfg.exists():
        try:
            data = json.loads(cfg.read_text() or '{}')
        except Exception as e:
            return f'error: cannot parse {cfg}: {e}'
    data.setdefault('context_servers', {})
    data['context_servers']['gptadmin'] = {
        'name': 'gptadmin',
        'url': url,
        'headers': {'Authorization': f'Bearer {token}'},
        'enabled': True,
    }
    if cfg.exists():
        backup = cfg.with_suffix(cfg.suffix + '.bak.gptadmin-mcp.' + time.strftime('%Y%m%d_%H%M%S'))
        shutil.copy2(cfg, backup)
    cfg.write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')
    try:
        os.chmod(cfg, 0o600)
    except Exception:
        pass
    return 'ok'


def _write_agent_mcp_config(path: Path, url: str, token: str, *, yaml_format: bool = False, agent_card: bool = False) -> str:
    """Update just GPTAdmin, retaining peer/model settings and file ownership."""
    try:
        if yaml_format:
            import yaml
            parse = yaml.safe_load
            render = lambda value: yaml.safe_dump(value, allow_unicode=True, sort_keys=False)
        else:
            parse = json.loads
            render = lambda value: json.dumps(value, ensure_ascii=False, indent=2) + '\n'
        data = parse(path.read_text()) if path.exists() else {}
        if data is None:
            data = {}
        if not isinstance(data, dict):
            raise ValueError('configuration must be an object')
        if agent_card:
            servers = data.setdefault('servers', [])
            if not isinstance(servers, list):
                raise ValueError('agent servers must be a list')
            if 'gptadmin' not in servers:
                servers.append('gptadmin')
        else:
            mcp = data.setdefault('mcp', {})
            servers = mcp.setdefault('servers', {})
            servers['gptadmin'] = {
                ('transport' if yaml_format else 'type'): 'http',
                ('protocol_mode' if yaml_format else 'protocolVersion'): 'legacy',
                'url': url,
                'headers': {'Authorization': f'Bearer {token}', 'X-GPTAdmin-Result-Mode': 'text'},
            }
        text = render(data)
        path.parent.mkdir(parents=True, exist_ok=True)
        previous = path.stat() if path.exists() else path.parent.stat()
        if path.exists():
            backup = path.with_name(path.name + '.bak.gptadmin-mcp.' + time.strftime('%Y%m%d_%H%M%S') + '.' + secrets.token_hex(3))
            shutil.copy2(path, backup)
            os.chmod(backup, 0o600)
            if os.geteuid() == 0:
                os.chown(backup, previous.st_uid, previous.st_gid)
        fd, temporary = tempfile.mkstemp(prefix='.' + path.name + '.gptadmin-', dir=path.parent)
        try:
            with os.fdopen(fd, 'w') as stream:
                stream.write(text)
                stream.flush()
                os.fsync(stream.fileno())
            if os.geteuid() == 0:
                os.chown(temporary, previous.st_uid, previous.st_gid)
            os.replace(temporary, path)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        return 'ok'
    except Exception as exc:
        # Parser exceptions may contain credential-bearing source lines.
        return f'error: cannot update {path}: {type(exc).__name__}'


def _configure_fast_agent_mcp(url: str, token: str) -> str:
    home = USER_HOME / '.fast-agent'
    if not shutil.which('fast-agent') and not home.exists():
        return 'skip: fast-agent not found'
    result = _write_agent_mcp_config(home / 'fast-agent.yaml', url, token, yaml_format=True)
    # Herder's explicit MiniMax profile does not inherit the default config.
    profile = USER_HOME / '.config' / 'agent-herder' / 'fast-agent-minimax.yaml'
    if result == 'ok' and profile.exists():
        result = _write_agent_mcp_config(profile, url, token, yaml_format=True)
        card = profile.with_name('fast-agent-minimax-card.yaml')
        if result == 'ok' and card.exists():
            result = _write_agent_mcp_config(card, url, token, yaml_format=True, agent_card=True)
    return result


def _configure_zcode_mcp(url: str, token: str) -> str:
    home = USER_HOME / '.zcode' / 'cli'
    if not shutil.which('zcode') and not home.exists():
        return 'skip: zcode not found'
    return _write_agent_mcp_config(home / 'config.json', url, token)


# ===== Notice Place: one MCP point per AI environment =====

# Notice Place is one MCP server with one contract. Every AI environment must
# expose it exactly once, under one canonical name, so an agent sees a single
# Notice Place tool surface instead of several aliases of the same backend.
#
# The install path is non-destructive: an alias of the same backend is disabled
# in place (reversible, the entry and its credentials stay in the file), the
# canonical point is normalized to the credential-free contract, and every
# write keeps a timestamped backup. Nothing else in a client config is touched.
NOTICE_PLACE_MCP_NAME = 'noticeplace'
NOTICE_PLACE_MCP_COMMAND = '/opt/noticeplace/bin/ask-human-mcp'
NOTICE_PLACE_MCP_ENV = {'ASK_HUMAN_ENV_FILE': '/home/admin/.config/ask-human/env'}
NOTICE_PLACE_MCP_URL = 'https://notify.bezrabotnyi.com/mcp'
NOTICE_PLACE_URL_HOST = 'notify.bezrabotnyi.com'
NOTICE_PLACE_URL_PATH = '/mcp'
NOTICE_PLACE_COMMAND_NAMES = ('ask-human-mcp', 'notify_mcp.py')
NOTICE_PLACE_CLIENTS = ('opencode', 'codex', 'hermes', 'zcode', 'fast-agent')
NOTICE_PLACE_PROJECTION_FILES = ('mcp.json', 'mcp.codex.json', 'opencode.json', 'fast-agent.yaml')


def _notice_place_local_server() -> bool:
    """True when this host runs the Notice Place MCP server itself."""
    return Path(NOTICE_PLACE_MCP_COMMAND).is_file()


def _notice_place_backend(spec) -> str | None:
    """Return 'endpoint' or 'local' when an MCP entry points at Notice Place."""
    if not isinstance(spec, dict):
        return None
    url = str(spec.get('url') or spec.get('target') or '').strip()
    if url:
        parsed = urllib.parse.urlparse(url if '://' in url else 'https://' + url)
        if parsed.hostname == NOTICE_PLACE_URL_HOST and parsed.path.rstrip('/').startswith(NOTICE_PLACE_URL_PATH):
            return 'endpoint'
    command = spec.get('command')
    parts: list[str] = []
    if isinstance(command, str):
        parts = [command]
    elif isinstance(command, (list, tuple)):
        parts = [str(item) for item in command]
    for part in parts:
        if part.rsplit('/', 1)[-1] in NOTICE_PLACE_COMMAND_NAMES:
            return 'local'
    return None


def _notice_place_credential(servers: dict) -> str | None:
    """Reuse the Notice Place bearer credential already present in a config."""
    for spec in servers.values():
        if _notice_place_backend(spec) is None:
            continue
        headers = spec.get('headers')
        if not isinstance(headers, dict):
            headers = spec.get('http_headers')
        if not isinstance(headers, dict):
            continue
        for key in ('Authorization', 'authorization'):
            value = headers.get(key)
            if isinstance(value, str) and value.startswith('Bearer '):
                return value
    return None


def _notice_place_entry(dialect: str, credential: str | None = None) -> dict | None:
    """The one canonical Notice Place registration, per client dialect.

    The contract is the published Streamable HTTP endpoint of the single
    running Notice Place MCP. Every client reaches that one server, so no
    client starts its own MCP process and no client gets a second backend.
    The bearer is the credential the client already holds for this endpoint -
    it is moved inside the same config file, never rotated and never stored
    somewhere new. Without such a credential there is no canonical entry.
    """
    if not credential:
        return None
    if dialect == 'opencode':
        return {
            'type': 'remote',
            'url': NOTICE_PLACE_MCP_URL,
            'headers': {'Authorization': credential},
            'enabled': True,
        }
    if dialect == 'zcode':
        return {
            'type': 'http',
            'url': NOTICE_PLACE_MCP_URL,
            'headers': {'Authorization': credential},
            'enabled': True,
        }
    return {
        'url': NOTICE_PLACE_MCP_URL,
        'headers': {'Authorization': credential},
        'enabled': True,
    }


def _strip_jsonc_comments(text: str) -> tuple[str, bool]:
    """Remove JSONC comments outside strings; report whether any were found."""
    out: list[str] = []
    in_string = False
    escaped = False
    has_comments = False
    index = 0
    size = len(text)
    while index < size:
        char = text[index]
        if in_string:
            out.append(char)
            if escaped:
                escaped = False
            elif char == '\\':
                escaped = True
            elif char == '"':
                in_string = False
            index += 1
            continue
        if char == '"':
            in_string = True
            out.append(char)
            index += 1
            continue
        if char == '/' and index + 1 < size:
            following = text[index + 1]
            if following == '/':
                has_comments = True
                while index < size and text[index] != '\n':
                    index += 1
                continue
            if following == '*':
                has_comments = True
                index += 2
                while index + 1 < size and not (text[index] == '*' and text[index + 1] == '/'):
                    index += 1
                index += 2
                continue
        out.append(char)
        index += 1
    return ''.join(out), has_comments


def _load_client_config(path: Path) -> tuple[dict | None, str]:
    """Read a JSON/JSONC/YAML client config; never echo its content on error."""
    try:
        text = path.read_text(encoding='utf-8')
    except FileNotFoundError:
        return None, 'missing'
    except Exception:
        return None, 'unreadable'
    if path.suffix in {'.yaml', '.yml'}:
        try:
            import yaml
            data = yaml.safe_load(text)
        except Exception:
            return None, 'invalid'
        return (data if isinstance(data, dict) else None), 'ok'
    stripped, has_comments = _strip_jsonc_comments(text)
    try:
        data = json.loads(stripped)
    except Exception:
        return None, 'invalid'
    if has_comments:
        return (data if isinstance(data, dict) else None), 'comments'
    return (data if isinstance(data, dict) else None), 'ok'


def _render_client_config(data: dict, style: str) -> str:
    if style == 'yaml':
        import yaml
        return yaml.safe_dump(data, allow_unicode=True, sort_keys=False)
    return json.dumps(data, ensure_ascii=False, indent=2) + '\n'


def _write_client_config(path: Path, text: str) -> str:
    """Atomically persist a client config, keeping a timestamped backup."""
    try:
        previous = path.stat() if path.exists() else path.parent.stat()
        if path.exists():
            backup = path.with_name(path.name + '.bak.noticeplace.' + time.strftime('%Y%m%d%H%M%S') + '.' + secrets.token_hex(3))
            shutil.copy2(path, backup)
            os.chmod(backup, 0o600)
            if os.geteuid() == 0:
                os.chown(backup, previous.st_uid, previous.st_gid)
        fd, temporary = tempfile.mkstemp(prefix='.' + path.name + '.noticeplace-', dir=path.parent)
        try:
            with os.fdopen(fd, 'w') as stream:
                stream.write(text)
                stream.flush()
                os.fsync(stream.fileno())
            if path.exists():
                os.chmod(temporary, stat.S_IMODE(previous.st_mode))
            if os.geteuid() == 0:
                os.chown(temporary, previous.st_uid, previous.st_gid)
            os.replace(temporary, path)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        return 'ok'
    except Exception as exc:
        return f'error: cannot update {path}: {type(exc).__name__}'


def _notice_place_servers(config: dict, container: tuple[str, ...]) -> dict:
    """Return (creating when needed) the MCP server map of a client config."""
    node = config
    for key in container[:-1]:
        child = node.get(key)
        if not isinstance(child, dict):
            child = {}
            node[key] = child
        node = child
    servers = node.get(container[-1])
    if not isinstance(servers, dict):
        servers = {}
        node[container[-1]] = servers
    return servers


def _notice_place_probe(spec, timeout: int = 25) -> dict:
    """Prove that a Notice Place registration answers with its tool list.

    Only `initialize` and `tools/list` are sent: nothing is notified, asked or
    changed. The result carries the server identity and the tool count, never
    a credential.
    """
    result: dict = {'ok': False, 'server': None, 'version': None, 'tool_count': None, 'error': None}
    command = spec.get('command')
    parts = [command] if isinstance(command, str) else list(command or []) if isinstance(command, list) else []
    if parts:
        env = dict(os.environ)
        for key, value in (spec.get('env') or spec.get('environment') or {}).items():
            env[str(key)] = str(value)
        requests = [
            {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
             'params': {'protocolVersion': '2024-11-05', 'capabilities': {},
                        'clientInfo': {'name': 'gptadmin', 'version': '1'}}},
            {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list', 'params': {}},
        ]
        try:
            process = subprocess.Popen(
                [str(part) for part in parts], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL, env=env, text=True, bufsize=1)
        except Exception as exc:
            result['error'] = type(exc).__name__
            return result
        try:
            for request in requests:
                process.stdin.write(json.dumps(request) + '\n')
                process.stdin.flush()
                line = process.stdout.readline()
                if not line:
                    result['error'] = 'no response'
                    return result
                payload = json.loads(line)
                if 'error' in payload:
                    result['error'] = 'rpc error'
                    return result
                body = payload.get('result') or {}
                if request['method'] == 'initialize':
                    info = body.get('serverInfo') or {}
                    result['server'] = info.get('name')
                    result['version'] = info.get('version')
                else:
                    result['tool_count'] = len(body.get('tools') or [])
                    result['ok'] = True
            return result
        except Exception as exc:
            result['error'] = type(exc).__name__
            return result
        finally:
            try:
                process.stdin.close()
            except Exception:
                pass
            try:
                process.terminate()
                process.wait(timeout=5)
            except Exception:
                try:
                    process.kill()
                except Exception:
                    pass
    url = str(spec.get('url') or spec.get('target') or '').strip()
    if not url:
        result['error'] = 'no command and no url'
        return result
    headers = {'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream'}
    for key, value in (spec.get('headers') or spec.get('http_headers') or {}).items():
        headers[str(key)] = str(value)
    requests = [
        {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize',
         'params': {'protocolVersion': '2024-11-05', 'capabilities': {},
                    'clientInfo': {'name': 'gptadmin', 'version': '1'}}},
        {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list', 'params': {}},
    ]
    try:
        for request in requests:
            body = json.dumps(request).encode('utf-8')
            with urllib.request.urlopen(
                    urllib.request.Request(url, data=body, headers=headers), timeout=timeout) as response:
                raw = response.read()
            text = raw.decode('utf-8', 'replace').strip()
            if text.startswith('event:') or text.startswith('data:'):
                chunks = [line[5:].strip() for line in text.splitlines() if line.startswith('data:')]
                text = chunks[-1] if chunks else '{}'
            payload = json.loads(text)
            if 'error' in payload:
                result['error'] = 'rpc error'
                return result
            data = payload.get('result') or {}
            if request['method'] == 'initialize':
                info = data.get('serverInfo') or {}
                result['server'] = info.get('name')
                result['version'] = info.get('version')
            else:
                result['tool_count'] = len(data.get('tools') or [])
                result['ok'] = True
        return result
    except Exception as exc:
        result['error'] = type(exc).__name__
        return result


def _notice_place_probe_result(spec) -> dict:
    """Probe with a readable failure reason."""
    result = _notice_place_probe(spec)
    if not result.get('ok') and result.get('error') in {None, ''}:
        result['error'] = 'no tool list'
    return result


def _notice_place_plugin_mcp_files(client: str) -> list[tuple[Path, str]]:
    """Installed plugin manifests that can declare a Notice Place MCP server."""
    roots = {
        'opencode': USER_HOME / '.config' / 'opencode' / 'plugins' / 'noticeplace',
        'codex': USER_HOME / '.codex' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace',
        'zcode': USER_HOME / '.zcode' / 'cli' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace',
    }
    root = roots.get(client)
    if root is None or not root.is_dir():
        return []
    files: list[tuple[Path, str]] = []
    for version in sorted(root.glob('*')):
        if not version.is_dir():
            continue
        for name in ('.codex-plugin/plugin.json', '.zcode-plugin/plugin.json'):
            manifest = version / name
            if manifest.is_file():
                files.append((manifest, 'mcpServers'))
        projection = version / 'opencode.json'
        if projection.is_file():
            files.append((projection, 'mcp'))
    return files


def _notice_place_plugin_mcp_active(client: str) -> bool:
    """True when the installed plugin still declares a Notice Place MCP server."""
    for path, key in _notice_place_plugin_mcp_files(client):
        data, status = _load_client_config(path)
        if status not in {'ok', 'comments'} or not isinstance(data, dict):
            continue
        declared = data.get(key)
        if isinstance(declared, str) and declared.strip():
            return True
        if isinstance(declared, dict) and declared:
            return True
    return False


def _notice_place_disable_plugin_mcp(client: str, apply: bool) -> list[str]:
    """Disable only the MCP surface of the Notice Place plugin.

    The plugin keeps its skill - the contract an agent reads - and stops
    registering a second Notice Place server of its own. The client config
    stays the single writer of the canonical HTTP point.
    """
    changed: list[str] = []
    for path, key in _notice_place_plugin_mcp_files(client):
        data, status = _load_client_config(path)
        if status not in {'ok', 'comments'} or not isinstance(data, dict):
            continue
        declared = data.get(key)
        active = (isinstance(declared, str) and declared.strip()) or (isinstance(declared, dict) and declared)
        if not active:
            continue
        if not apply:
            changed.append(str(path))
            continue
        backup = path.with_name(path.name + '.bak.noticeplace.' + time.strftime('%Y%m%d%H%M%S'))
        shutil.copy2(path, backup)
        data.pop(key, None)
        result = _write_client_config(path, _render_client_config(data, 'json'))
        if result == 'ok':
            changed.append(str(path))
    return changed


def _notice_place_host_credential() -> str | None:
    """The Notice Place bearer another client of this host already carries.

    The Notice Place MCP is one server for the whole fleet, so its bearer is
    one shared credential. It is read locally and never printed, never rotated
    and never written to a new store: it only moves inside the client configs
    that already belong to this host.
    """
    for client in NOTICE_PLACE_CLIENTS:
        if client == 'codex':
            entries = _notice_place_codex_scan()['entries']
            credential = _notice_place_credential(entries)
            if credential:
                return credential
            continue
        for path, container, _style, _dialect in _notice_place_client_targets(client):
            data, status = _load_client_config(path)
            if status not in {'ok', 'comments'} or not isinstance(data, dict):
                continue
            credential = _notice_place_credential(_notice_place_servers(data, container))
            if credential:
                return credential
    return None


def _notice_place_client_targets(client: str) -> list[tuple[Path, tuple[str, ...], str, str]]:
    """Exact config files, MCP map location, file format and client dialect."""
    if client == 'opencode':
        return [
            (USER_HOME / '.config' / 'opencode' / 'opencode.json', ('mcp',), 'json', 'opencode'),
            (USER_HOME / '.config' / 'opencode' / 'opencode.jsonc', ('mcp',), 'json', 'opencode'),
        ]
    if client == 'hermes':
        return [(USER_HOME / '.hermes' / 'config.yaml', ('mcp_servers',), 'yaml', 'hermes')]
    if client == 'zcode':
        return [(USER_HOME / '.zcode' / 'cli' / 'config.json', ('mcp', 'servers'), 'json', 'zcode')]
    if client == 'fast-agent':
        return [(USER_HOME / '.fast-agent' / 'fast-agent.yaml', ('mcp', 'servers'), 'yaml', 'fast-agent')]
    return []


def _notice_place_plugin_spec(client: str) -> dict | None:
    """The Notice Place MCP entry a published and enabled plugin provides."""
    roots = {
        'opencode': USER_HOME / '.config' / 'opencode' / 'plugins' / 'noticeplace',
        'codex': USER_HOME / '.codex' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace',
        'zcode': USER_HOME / '.zcode' / 'cli' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace',
    }
    root = roots.get(client)
    if root is None or not root.is_dir():
        return None
    if not _notice_place_plugin_enabled(client):
        return None
    for version in sorted(root.glob('*')):
        for name in ('opencode.json', 'mcp.codex.json'):
            candidate = version / name
            if not candidate.is_file():
                continue
            data, status = _load_client_config(candidate)
            if status not in {'ok', 'comments'} or not isinstance(data, dict):
                continue
            servers = data.get('mcpServers')
            if isinstance(servers, dict) and NOTICE_PLACE_MCP_NAME in servers:
                return servers[NOTICE_PLACE_MCP_NAME]
            inner = data.get('mcp')
            if isinstance(inner, dict) and NOTICE_PLACE_MCP_NAME in inner:
                return inner[NOTICE_PLACE_MCP_NAME]
    return None


def _notice_place_plugin_enabled(client: str) -> bool:
    """True when the harness has the Notice Place plugin switched on."""
    if client == 'codex':
        path = USER_HOME / '.codex' / 'config.toml'
        if not path.is_file():
            return False
        text = path.read_text(encoding='utf-8')
        header = '[plugins."noticeplace@noticeplace-local"]'
        start = text.find(header)
        if start < 0:
            return False
        following = text.find('\n[', start + 1)
        block = text[start:following if following > 0 else len(text)]
        return 'enabled = true' in block
    if client == 'zcode':
        path = USER_HOME / '.zcode' / 'cli' / 'config.json'
        data, status = _load_client_config(path)
        if not isinstance(data, dict):
            return False
        plugins = data.get('plugins')
        if not isinstance(plugins, dict):
            return False
        enabled = plugins.get('enabledPlugins')
        return isinstance(enabled, dict) and enabled.get('noticeplace@noticeplace-local') is True
    return True


def _notice_place_codex_scan() -> dict:
    """Read the Codex MCP table without a TOML parser.

    Sub-tables such as `[mcp_servers.<name>.http_headers]` are folded into
    their server entry, so a credential kept there is found like any other.
    """
    path = USER_HOME / '.codex' / 'config.toml'
    result: dict = {'path': str(path), 'entries': {}, 'status': 'missing'}
    if not path.is_file():
        return result
    text = path.read_text(encoding='utf-8')
    entries: dict[str, dict] = {}
    current = ''
    current_sub = ''
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith('[') and stripped.endswith(']'):
            header = stripped[1:-1].strip()
            if header.startswith('mcp_servers.'):
                rest = header[len('mcp_servers.'):]
                if '.' in rest:
                    owner, _, sub = rest.partition('.')
                    current = owner.strip('"')
                    current_sub = sub.strip('"')
                    entries.setdefault(current, {}).setdefault(current_sub, {})
                else:
                    current = rest.strip('"')
                    current_sub = ''
                    entries.setdefault(current, {})
                continue
            current = ''
            current_sub = ''
            continue
        if not current or '=' not in stripped:
            continue
        key, _, value = stripped.partition('=')
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        if current_sub:
            entries[current][current_sub][key.strip()] = value
        else:
            entries[current][key.strip()] = value
    result['entries'] = entries
    result['status'] = 'ok'
    return result


def _notice_place_codex_set_enabled(text: str, names: list[str], enabled: bool) -> tuple[str, list[str]]:
    """Set `enabled` inside the named Codex MCP tables, keeping other keys.

    A table keeps exactly one `enabled` key: an existing key is replaced, a
    missing one is inserted after the first key of the table. A duplicated key
    would make the whole Codex config unparsable, so it is never produced.
    """
    lines = text.splitlines(keepends=True)
    output: list[str] = []
    touched: list[str] = []
    replacement = f'enabled = {"true" if enabled else "false"}\n'
    index = 0
    while index < len(lines):
        line = lines[index]
        stripped = line.strip()
        if not (stripped.startswith('[') and stripped.endswith(']') and not stripped.startswith('[[')):
            output.append(line)
            index += 1
            continue
        header = stripped[1:-1].strip()
        rest = header[len('mcp_servers.'):] if header.startswith('mcp_servers.') else ''
        owner = rest.strip('"') if rest and '.' not in rest else ''
        body: list[str] = []
        cursor = index + 1
        while cursor < len(lines):
            following = lines[cursor].strip()
            if following.startswith('[') and following.endswith(']'):
                break
            body.append(lines[cursor])
            cursor += 1
        if owner in names:
            if any(re.match(r'^enabled\s*=', item.strip()) for item in body):
                rebuilt: list[str] = []
                written = False
                for item in body:
                    if re.match(r'^enabled\s*=', item.strip()):
                        if not written:
                            rebuilt.append(replacement)
                            written = True
                        continue
                    rebuilt.append(item)
                body = rebuilt
            else:
                rebuilt = []
                inserted = False
                for item in body:
                    rebuilt.append(item)
                    if not inserted and '=' in item and not item.strip().startswith('#'):
                        rebuilt.append(replacement)
                        inserted = True
                body = rebuilt
            if owner not in touched:
                touched.append(owner)
        output.append(line)
        output.extend(body)
        index = cursor
    return ''.join(output), touched


def _notice_place_codex_remove_table(text: str, name: str) -> str:
    """Remove one Codex MCP table together with its sub-tables."""
    lines = text.splitlines(keepends=True)
    output: list[str] = []
    index = 0
    while index < len(lines):
        stripped = lines[index].strip()
        if stripped.startswith('[') and stripped.endswith(']') and not stripped.startswith('[['):
            header = stripped[1:-1].strip()
            rest = header[len('mcp_servers.'):] if header.startswith('mcp_servers.') else ''
            owner = rest.split('.')[0].strip('"') if rest else ''
            if owner == name:
                index += 1
                while index < len(lines):
                    following = lines[index].strip()
                    if following.startswith('[') and following.endswith(']'):
                        break
                    index += 1
                while output and output[-1].strip() == '':
                    output.pop()
                output.append('\n')
                continue
        output.append(lines[index])
        index += 1
    return ''.join(output)


def _notice_place_codex_canonical_block(credential: str | None = None) -> str:
    """The canonical Codex registration of the single Notice Place HTTP MCP."""
    if not credential:
        return ''
    return (
        f'[mcp_servers.{NOTICE_PLACE_MCP_NAME}]\n'
        f'url = "{NOTICE_PLACE_MCP_URL}"\n'
        f'enabled = true\n'
        f'\n[mcp_servers.{NOTICE_PLACE_MCP_NAME}.http_headers]\n'
        f'Authorization = "{credential}"\n'
    )


def _notice_place_usable(spec) -> bool:
    """True when a Notice Place entry can actually be started on this host."""
    backend = _notice_place_backend(spec)
    if backend is None:
        return False
    if backend == 'endpoint':
        return True
    command = spec.get('command')
    parts = [command] if isinstance(command, str) else list(command or []) if isinstance(command, list) else []
    return any(Path(str(part)).is_file() for part in parts)


def _notice_place_status(client: str) -> dict:
    """Read-only view of every Notice Place registration of one client."""
    report: dict = {'client': client, 'entries': {}, 'canonical': None, 'aliases': [], 'files': [], 'plugin': None}
    plugin_spec = _notice_place_plugin_spec(client)
    report['plugin'] = 'enabled' if plugin_spec else 'absent'
    if client == 'codex':
        scan = _notice_place_codex_scan()
        report['files'] = [scan['path']]
        for name, spec in scan['entries'].items():
            backend = _notice_place_backend(spec)
            if backend is None:
                continue
            report['entries'][name] = {
                'backend': backend,
                'enabled': str(spec.get('enabled', 'true')).lower() not in {'false', '0'},
                'usable': _notice_place_usable(spec),
            }
        if NOTICE_PLACE_MCP_NAME in report['entries']:
            report['canonical'] = report['entries'][NOTICE_PLACE_MCP_NAME]
            report['canonical_source'] = 'config'
        elif plugin_spec:
            report['canonical'] = {'backend': _notice_place_backend(plugin_spec) or 'local', 'enabled': True}
            report['canonical_source'] = 'plugin'
        else:
            report['canonical_source'] = 'absent'
        report['aliases'] = sorted(name for name in report['entries'] if name.lower() != NOTICE_PLACE_MCP_NAME)
        return report
    for path, container, _style, _dialect in _notice_place_client_targets(client):
        report['files'].append(str(path))
        data, status = _load_client_config(path)
        if not isinstance(data, dict):
            report.setdefault('unreadable', []).append(f'{path} ({status})')
            continue
        servers = _notice_place_servers(data, container)
        for name, spec in servers.items():
            backend = _notice_place_backend(spec)
            if backend is None:
                continue
            report['entries'][name] = {
                'backend': backend,
                'enabled': spec.get('enabled', True) is not False,
                'usable': _notice_place_usable(spec),
            }
        if NOTICE_PLACE_MCP_NAME in report['entries']:
            report['canonical'] = report['entries'][NOTICE_PLACE_MCP_NAME]
            report['canonical_source'] = 'config'
        elif plugin_spec and client in {'codex', 'zcode'}:
            report['canonical'] = {'backend': _notice_place_backend(plugin_spec) or 'local', 'enabled': True}
            report['canonical_source'] = 'plugin'
        else:
            report['canonical_source'] = 'absent'
        report['aliases'] = sorted(name for name in report['entries'] if name.lower() != NOTICE_PLACE_MCP_NAME)
    return report


def _notice_place_install_client(client: str, apply: bool) -> dict:
    """Make one environment expose exactly one canonical Notice Place point.

    The order is fixed: the canonical point is created and proven with a real
    `initialize` + `tools/list` handshake, and only a proven canonical point
    allows an alias of the same backend to be switched off. Nothing is deleted:
    an alias stays in the file with `enabled: false` and can be switched back on.
    Every write keeps a timestamped backup of the whole file.
    """
    report: dict = {
        'client': client,
        'added': [],
        'renamed': [],
        'normalized': [],
        'disabled': [],
        'files': [],
        'status': 'ok',
        'canonical_proof': None,
        'plugin_mcp_disabled': [],
    }
    plugin_spec = _notice_place_plugin_spec(client)
    client_owns_canonical = True
    if _notice_place_plugin_mcp_active(client):
        # The plugin would register a second Notice Place server of its own.
        # Only its MCP surface is switched off, the skill stays - and it is
        # switched off only when the client config can hold the canonical point.
        client_canonical = False
        client_credential = None
        for path, container, _style, _dialect in _notice_place_client_targets(client):
            data, status = _load_client_config(path)
            if status not in {'ok', 'comments'} or not isinstance(data, dict):
                continue
            servers = _notice_place_servers(data, container)
            if any(str(name).lower() == NOTICE_PLACE_MCP_NAME for name in servers):
                client_canonical = True
            client_credential = client_credential or _notice_place_credential(servers)
        client_credential = client_credential or _notice_place_host_credential()
        if client_canonical or client_credential:
            report['plugin_mcp_disabled'] = _notice_place_disable_plugin_mcp(client, apply)
            plugin_spec = None
    if client == 'codex':
        return _notice_place_install_codex(client, apply, plugin_spec, report)
    targets = _notice_place_client_targets(client)
    loaded: list[tuple[Path, tuple[str, ...], str, str, dict]] = []
    canonical_seen = False
    for path, container, style, dialect in targets:
        report['files'].append(str(path))
        data, status = _load_client_config(path)
        if status == 'missing':
            continue
        if status == 'comments':
            report['status'] = f'error: {path} keeps comments; refusing to rewrite'
            continue
        if not isinstance(data, dict):
            report['status'] = f'error: {path} is not a config object'
            continue
        loaded.append((path, container, style, dialect, data))
        servers = _notice_place_servers(data, container)
        if any(str(name).lower() == NOTICE_PLACE_MCP_NAME for name in servers):
            canonical_seen = True
    if not canonical_seen and plugin_spec and not client_owns_canonical:
        canonical_seen = True

    # Phase 1: one canonical entry, in at most one file of the client.
    canonical_written = False
    changed_files: list[tuple[Path, str, dict]] = []
    canonical_spec = None
    # the credential may live in any file of the client, so it is collected
    # across the whole client before the canonical entry is written
    client_credential = None
    for _path, _container, _style, _dialect, data in loaded:
        client_credential = client_credential or _notice_place_credential(_notice_place_servers(data, _container))
    client_credential = client_credential or _notice_place_host_credential()
    for path, container, style, dialect, data in loaded:
        servers = _notice_place_servers(data, container)
        credential = _notice_place_credential(servers) or client_credential
        expected = _notice_place_entry(dialect, credential)
        canonical = servers.get(NOTICE_PLACE_MCP_NAME)
        if canonical is None:
            for name in list(servers.keys()):
                if str(name).lower() == NOTICE_PLACE_MCP_NAME:
                    servers[NOTICE_PLACE_MCP_NAME] = servers.pop(name)
                    report['renamed'].append(name)
                    canonical = servers[NOTICE_PLACE_MCP_NAME]
                    break
        if canonical is None:
            if canonical_seen or canonical_written:
                continue
            if expected is None:
                report['status'] = 'no canonical Notice Place point available; the alias stays live'
                continue
            servers[NOTICE_PLACE_MCP_NAME] = expected
            report['added'].append(NOTICE_PLACE_MCP_NAME)
            canonical_written = True
        elif expected is not None and canonical != expected and client_owns_canonical:
            servers[NOTICE_PLACE_MCP_NAME] = expected
            report['normalized'].append(NOTICE_PLACE_MCP_NAME)
        if canonical_spec is None and NOTICE_PLACE_MCP_NAME in servers:
            canonical_spec = servers[NOTICE_PLACE_MCP_NAME]
        if report['added'] or report['renamed'] or report['normalized']:
            changed_files.append((path, style, data))
    if canonical_spec is None and plugin_spec:
        canonical_spec = plugin_spec
    if changed_files and apply:
        for path, style, data in changed_files:
            result = _write_client_config(path, _render_client_config(data, style))
            if result != 'ok':
                report['status'] = result
                return report

    # Phase 2: prove the canonical point before an alias is switched off.
    aliases: list[tuple[Path, tuple[str, ...], str, dict, str]] = []
    for path, container, style, dialect, data in loaded:
        servers = _notice_place_servers(data, container)
        for name, spec in servers.items():
            if _notice_place_backend(spec) is None:
                continue
            if str(name).lower() == NOTICE_PLACE_MCP_NAME:
                continue
            if spec.get('enabled', True) is False:
                continue
            aliases.append((path, container, style, data, name))
    if not aliases:
        return report
    if canonical_spec is None:
        report['status'] = 'no canonical Notice Place point available; the alias stays live'
        return report
    proof = _notice_place_probe_result(canonical_spec)
    report['canonical_proof'] = proof
    if not proof.get('ok'):
        report['status'] = f"canonical Notice Place point not proven ({proof.get('error')}); the alias stays live"
        return report
    touched: set[Path] = set()
    for path, container, style, data, name in aliases:
        servers = _notice_place_servers(data, container)
        spec = servers.get(name)
        if not isinstance(spec, dict):
            continue
        spec['enabled'] = False
        report['disabled'].append(name)
        touched.add(path)
    if touched:
        for path, container, style, dialect, data in loaded:
            if path not in touched:
                continue
            if not apply:
                report['status'] = 'dry-run'
                continue
            result = _write_client_config(path, _render_client_config(data, style))
            if result != 'ok':
                report['status'] = result
    if not apply and report['status'] == 'ok' and (report['added'] or report['renamed']
                                                    or report['normalized'] or report['disabled']):
        report['status'] = 'dry-run'
    return report


def _notice_place_install_codex(client: str, apply: bool, plugin_spec, report: dict) -> dict:
    """Codex keeps its own TOML table shape; the same order applies."""
    path = USER_HOME / '.codex' / 'config.toml'
    report['files'] = [str(path)]
    if not path.is_file():
        report['status'] = 'missing'
        return report
    text = path.read_text(encoding='utf-8')
    scan = _notice_place_codex_scan()
    aliases = []
    live_aliases = []
    for name, spec in scan['entries'].items():
        if _notice_place_backend(spec) is None:
            continue
        if name.lower() == NOTICE_PLACE_MCP_NAME:
            continue
        aliases.append(name)
        if str(spec.get('enabled', 'true')).lower() not in {'false', '0'}:
            live_aliases.append(name)
    updated = text
    canonical_present = NOTICE_PLACE_MCP_NAME in scan['entries']
    credential = _notice_place_credential(scan['entries']) or _notice_place_host_credential()
    canonical_spec = scan['entries'].get(NOTICE_PLACE_MCP_NAME)
    if canonical_present:
        # the canonical point must be the shared HTTP endpoint, not a local
        # per-client MCP process: replace the table with the HTTP form
        block = _notice_place_codex_canonical_block(credential)
        current = canonical_spec or {}
        if block and (current.get('url') != NOTICE_PLACE_MCP_URL):
            updated = _notice_place_codex_remove_table(updated, NOTICE_PLACE_MCP_NAME)
            updated = updated.rstrip('\n') + '\n\n' + block
            report['normalized'].append(NOTICE_PLACE_MCP_NAME)
            canonical_spec = {'url': NOTICE_PLACE_MCP_URL, 'headers': {'Authorization': credential}}
    if not canonical_present:
        if plugin_spec:
            canonical_spec = plugin_spec
        else:
            block = _notice_place_codex_canonical_block(credential)
            if block:
                updated = updated.rstrip('\n') + '\n\n' + block
                report['added'].append(NOTICE_PLACE_MCP_NAME)
                if _notice_place_local_server():
                    canonical_spec = {'command': NOTICE_PLACE_MCP_COMMAND, 'env': dict(NOTICE_PLACE_MCP_ENV)}
                else:
                    canonical_spec = {'url': NOTICE_PLACE_MCP_URL, 'headers': {'Authorization': credential}}
            else:
                report['status'] = 'no canonical Notice Place point available; the alias stays live'
                return report
    if updated != text:
        if apply:
            result = _write_client_config(path, updated)
            if result != 'ok':
                report['status'] = result
                return report
            scan = _notice_place_codex_scan()
            canonical_spec = scan['entries'].get(NOTICE_PLACE_MCP_NAME) or canonical_spec
    if not live_aliases:
        if not apply and report['status'] == 'ok' and report['added']:
            report['status'] = 'dry-run'
        return report
    if canonical_spec is None:
        report['status'] = 'no canonical Notice Place point available; the alias stays live'
        return report
    proof = _notice_place_probe_result(canonical_spec)
    report['canonical_proof'] = proof
    if not proof.get('ok'):
        report['status'] = f"canonical Notice Place point not proven ({proof.get('error')}); the alias stays live"
        return report
    updated, touched = _notice_place_codex_set_enabled(updated, live_aliases, False)
    report['disabled'].extend(name for name in touched if name in live_aliases)
    if updated != text:
        if not apply:
            report['status'] = 'dry-run'
            return report
        result = _write_client_config(path, updated)
        if result != 'ok':
            report['status'] = result
    return report


def _notice_place_package_sources() -> list[Path]:
    """Canonical published Notice Place plugin packages, newest first.

    The package owner is the noticeplace repository; the published releases and
    the fleet registry copy follow it.
    """
    sources: list[Path] = []
    owner = USER_HOME / 'agents-projects' / 'noticeplace' / 'noticeplace-plugin' / 'packages' / 'noticeplace'
    if owner.is_dir():
        for version in sorted(owner.glob('*'), reverse=True):
            if (version / 'plugin.json').is_file():
                sources.append(version)
    releases = Path('/opt/noticeplace-releases')
    if releases.is_dir():
        for release in sorted(releases.glob('*'), reverse=True):
            candidate = release / 'noticeplace-plugin' / 'packages' / 'noticeplace'
            if not candidate.is_dir():
                continue
            for version in sorted(candidate.glob('*'), reverse=True):
                if (version / 'plugin.json').is_file():
                    sources.append(version)
    registry = USER_HOME / 'agents-projects' / 'agent-harness-fleet' / 'registry' / 'packages' / 'noticeplace'
    if registry.is_dir():
        for version in sorted(registry.glob('*'), reverse=True):
            if (version / 'plugin.json').is_file():
                sources.append(version)
    return sources


def _notice_place_empty_projection(path: Path) -> str:
    """Write a Notice Place projection that registers no server of its own.

    The client config is the single writer of the canonical HTTP point, so the
    installed plugin keeps its skill and stops carrying a Notice Place server.
    A stale local stdio process is never left behind, and a later publish
    cannot add a second registration either.
    """
    try:
        text = path.read_text(encoding='utf-8')
    except Exception:
        return 'ok'
    if path.suffix in {'.yaml', '.yml'}:
        try:
            import yaml
            data = yaml.safe_load(text)
        except Exception:
            return 'ok'
        if not isinstance(data, dict):
            return 'ok'
        data.setdefault('mcp', {})
        if isinstance(data['mcp'], dict):
            data['mcp']['servers'] = {}
        return _write_client_config(path, _render_client_config(data, 'yaml'))
    data, status = _load_client_config(path)
    if status not in {'ok', 'comments'} or not isinstance(data, dict):
        return 'ok'
    if 'mcpServers' in data:
        data['mcpServers'] = {}
    if isinstance(data.get('mcp'), dict):
        data['mcp'] = {}
    return _write_client_config(path, _render_client_config(data, 'json'))


def _notice_place_refresh_plugin(apply: bool) -> dict:
    """Converge installed plugin copies onto the canonical package.

    The published package still carries a local stdio projection; the installed
    copies must not keep a second Notice Place server or a per-client process.
    The MCP surface of the installed plugin is therefore emptied, and its skill
    stays.
    """
    result: dict = {'client': 'plugin', 'updated': [], 'checked': 0, 'status': 'ok'}
    sources = _notice_place_package_sources()
    if not sources:
        result['status'] = 'no canonical package found'
        return result
    source = sources[0]
    by_version: dict[str, Path] = {}
    for path in sources:
        # the first source of a version wins, so the package owner is preferred
        by_version.setdefault(path.name, path)
    targets = {
        'opencode': USER_HOME / '.config' / 'opencode' / 'plugins' / 'noticeplace',
        'codex': USER_HOME / '.codex' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace',
        'zcode': USER_HOME / '.zcode' / 'cli' / 'plugins' / 'cache' / 'noticeplace-local' / 'noticeplace',
    }
    for root in targets.values():
        if not root.is_dir():
            continue
        for version in sorted(root.glob('*')):
            if not version.is_dir():
                continue
            # an installed version is refreshed from the package of the same
            # version, so a self-contained bundle stays as it is
            origin_root = by_version.get(version.name) or source
            for name in NOTICE_PLACE_PROJECTION_FILES:
                origin = origin_root / name
                target = version / name
                if not origin.is_file() or not target.is_file():
                    continue
                result['checked'] += 1
                if origin.read_bytes() == target.read_bytes() and 'ask-human-mcp' not in target.read_text():
                    continue
                if not apply:
                    result['updated'].append(str(target))
                    continue
                try:
                    backup = target.with_name(target.name + '.bak.noticeplace.' + time.strftime('%Y%m%d%H%M%S'))
                    shutil.copy2(target, backup)
                    shutil.copy2(origin, target)
                    result['updated'].append(str(target))
                except Exception:
                    continue
    return result


def cmd_notice_place(args) -> None:
    """Show or install the single canonical Notice Place point per AI client."""
    action = getattr(args, 'notice_place_cmd', None) or 'status'
    clients = list(getattr(args, 'client', None) or NOTICE_PLACE_CLIENTS)
    if action == 'status':
        reports = [_notice_place_status(client) for client in clients]
        if getattr(args, 'json', False):
            print(json.dumps(reports, ensure_ascii=False, indent=2))
            return
        for report in reports:
            entries = report['entries']
            canonical = report.get('canonical')
            live = [name for name, info in entries.items() if info.get('enabled')]
            print(
                f"{report['client']}: canonical={'present' if canonical else 'absent'}"
                f" live={live or 'none'} aliases={report['aliases'] or 'none'}"
                f" plugin={report['plugin']}"
            )
        return
    apply = not getattr(args, 'dry_run', False)
    reports = []
    for client in clients:
        reports.append(_notice_place_install_client(client, apply))
    if getattr(args, 'refresh_plugin', False):
        reports.append(_notice_place_refresh_plugin(apply))
    if getattr(args, 'json', False):
        print(json.dumps(reports, ensure_ascii=False, indent=2))
        return
    for report in reports:
        print(
            f"{report.get('client')}: status={report.get('status', 'ok')}"
            f" added={report.get('added') or []} renamed={report.get('renamed') or []}"
            f" normalized={report.get('normalized') or []} disabled={report.get('disabled') or []}"
        )


def auto_configure_ai_mcp_clients(env: dict, install_hub: bool) -> None:
    if not install_hub:
        return
    flag = os.environ.get('GPTADMIN_AUTO_CONFIGURE_AI_MCP', env.get('GPTADMIN_AUTO_CONFIGURE_AI_MCP', '1')).strip().lower()
    if flag in {'0', 'false', 'no', 'off'}:
        print('AI MCP clients auto-config skipped: GPTADMIN_AUTO_CONFIGURE_AI_MCP=0')
        return
    try:
        results = configure_ai_mcp_clients(env, rotate=False)
        url = results.pop('_url')
        print('AI MCP clients auto-config: ' + ', '.join(f'{k}={v}' for k, v in results.items()))
        print(f'Hub URL: {url.removesuffix("/mcp")}')
    except Exception as e:
        print(f'WARNING: AI MCP clients auto-config failed: {e}', file=sys.stderr)

# ===== Uninstall =====

def safe_rm(p: Path):
    try:
        if p.is_symlink() or p.is_file():
            p.unlink(missing_ok=True)
        elif p.is_dir():
            shutil.rmtree(p, ignore_errors=True)
    except Exception as e:
        print(f'WARN: не удалось удалить {p}: {e}', file=sys.stderr)

def cmd_uninstall(args):
    need_root()
    failures = []
    # Remove stale standalone relay units from pre-Go installations. New MCP
    # servers are owned only by the native ShellMCP supervisor.
    try:
        _mcp_retire_legacy_relay_services(_mcp_config(), list((_mcp_config().get('mcpServers') or {}).keys()))
    except Exception as e:
        failures.append(f'не удалось убрать legacy MCP relay services: {e}')
    try:
        svc_autoupdate_disable_stop()
    except Exception as e:
        failures.append(f'не удалось отключить auto-update: {e}')
    for name, path in [(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED),
                       (svc_frpc_name(), UNIT_PATH_FRPC),
                       (svc_shellmcp_name(), UNIT_PATH_SHELLMCP),
                       (svc_hub_name(), UNIT_PATH_HUB)]:
        stopped = svc_disable_stop(name, path)
        if stopped is False:
            failures.append(f'не удалось остановить service {name}')
        safe_rm(path)
    svc_daemon_reload()

    local_frpc = BIN_DIR / 'frpc'
    if local_frpc.exists():
        safe_rm(local_frpc)
    local_cloudflared = BIN_DIR / 'cloudflared'
    if local_cloudflared.exists():
        safe_rm(local_cloudflared)

    safe_rm(INSTALL_DIR)
    safe_rm(ETC_DIR)

    removed_cli = False
    if CLI_PATH.exists():
        try:
            CLI_PATH.unlink()
            removed_cli = True
        except Exception as e:
            print(f'WARN: не удалось удалить {CLI_PATH}: {e}', file=sys.stderr)
            failures.append(f'не удалось удалить CLI {CLI_PATH}: {e}')

    if failures:
        print('GPTAdmin удалён частично, но остались ошибки:', file=sys.stderr)
        for failure in failures:
            print(f' - {failure}', file=sys.stderr)
        if not removed_cli and CLI_PATH.exists():
            print(f'Чтобы удалить CLI, выполните: rm -f {CLI_PATH}', file=sys.stderr)
        raise SystemExit(1)

    print('GPTAdmin полностью удалён: службы, конфиги и бинарники.')
    if not removed_cli and CLI_PATH.exists():
        print(f'Чтобы удалить CLI, выполните: rm -f {CLI_PATH}')

# ===== Main =====

def maybe_update_hint(args):
    """Check for available update and print hint to stderr (best-effort).

    Only runs when auto-update is disabled and a newer version may exist.
    Uses local cache to avoid network requests on every CLI invocation.
    """
    # Skip when auto-update is enabled — no hint needed.
    try:
        from_env = env_read()
        if auto_update_enabled(from_env):
            return
    except Exception:
        return

    # Skip for certain commands.
    cmd = getattr(args, 'cmd', None) or getattr(args, 'command', None)
    if cmd in ('update', 'auto-update'):
        return
    if getattr(args, 'auto', False):
        return

    # Read cache.
    cache = _read_update_cache()
    now = int(time.time())

    last_success = cache.get('last_success_ts', 0) if cache else 0
    last_attempt = cache.get('last_attempt_ts', 0) if cache else 0
    age_success = now - last_success
    age_attempt = now - last_attempt

    remote_version = None
    remote_sha = ''

    if cache and 0 <= age_success < UPDATE_CHECK_FRESH_S:
        # Cache still fresh — use cached remote version.
        remote_version = cache.get('remote_version')
        remote_sha = cache.get('remote_sha256', '')
    elif age_attempt < UPDATE_CHECK_COOLDOWN_S and age_success >= UPDATE_CHECK_FRESH_S:
        # Recent failed attempt — cooldown active, skip network check.
        return
    else:
        # Need network check.
        try:
            pkg_url = platform_pkg_url_default()
            info = _remote_artifact_build_info(pkg_url)
            if info:
                remote_version = info.get('build_version')
                if isinstance(remote_version, str):
                    remote_version = int(remote_version)
                remote_sha = info.get('sha256', '')
            cache_data = {
                'last_success_ts': now,
                'last_attempt_ts': now,
                'remote_version': remote_version,
                'remote_sha256': remote_sha or '',
            }
            _write_update_cache(cache_data)
        except Exception:
            # Network error — record attempt for cooldown.
            cache_data = {
                'last_success_ts': last_success,
                'last_attempt_ts': now,
                'remote_version': remote_version,
                'remote_sha256': remote_sha or '',
            }
            _write_update_cache(cache_data)
            return

    if remote_version is None:
        return

    # Compare with installed version.
    try:
        marker = _read_installed_build_marker()
        if not marker:
            return
        installed_v = marker.get('build_version')
        if isinstance(installed_v, str):
            installed_v = int(installed_v)
        if installed_v is None or installed_v == 0:
            return
        installed_v = int(installed_v)
        remote_v = int(remote_version)
    except (TypeError, ValueError, Exception):
        return

    if remote_v <= installed_v:
        return  # already up to date

    # Print hint to stderr.
    print(
        f'\nℹ {c_yellow("Доступно обновление")}: build {installed_v} → {remote_v}.',
        f'  {c_dim("Обновить:")}          {c_green("gptadmin update")}',
        f'  {c_dim("Включить авто:")}     {c_green("gptadmin auto-update enable")}',
        sep='\n', file=sys.stderr,
    )


def main():
    # The portable Node lifecycle owns its instance paths and service transaction.
    # Dispatch before legacy parsing/update hints, including `node --help`.
    if len(sys.argv) > 1 and sys.argv[1] == 'node':
        root = Path(__file__).resolve().parent
        manager = next((p for p in (root / 'scripts' / 'gptadmin_node_manage.py',
                                   root / 'gptadmin_node_manage.py',
                                   INSTALL_DIR / 'cli' / 'scripts' / 'gptadmin_node_manage.py')
                        if p.is_file()), None)
        if manager is None:
            die('Node manager is missing; install the complete Node or CLI package.')
        os.execv(sys.executable, [sys.executable, str(manager), *sys.argv[2:]])
    # Backward-compatible command aliases, hidden from help.
    if len(sys.argv) > 1 and sys.argv[1] in ('config-shell', 'config-shellmcp'):
        legacy = sys.argv[1]
        sys.argv[1:2] = ['config', 'shellmcp']
        # Keep old --shellmcp-url accepted by the ShellMCP config parser.
    ap = argparse.ArgumentParser(prog='gptadmin', description='GPTAdmin manager (hub + shell agents)')
    ap.add_argument('--user', action='store_true', help='Use per-user install paths/services (default when not root)')
    ap.add_argument('--system', action='store_true', help='Use system install paths/services (default when root)')
    sub = ap.add_subparsers(dest='cmd')

    sub.add_parser('node', help='Установить или обновить единый Node (Linux/systemd)')

    sub.add_parser('version', help='Показать версию и информацию о сборке').set_defaults(func=cmd_version)
    ap_doctor = sub.add_parser('doctor', help='Проверка здоровья: Hub, MCP clients, Tunnel и конфиг')
    ap_doctor.add_argument('--json', action='store_true', help='Вывести машиночитаемый JSON без секретов')
    ap_doctor.set_defaults(func=cmd_doctor)

    ap_backup = sub.add_parser('backup', help='Создать, проверить или восстановить конфигурационный backup')
    backup_sub = ap_backup.add_subparsers(dest='backup_cmd')
    ap_backup_create = backup_sub.add_parser('create', help='Создать backup с manifest и SHA-256')
    ap_backup_create.add_argument('archive')
    ap_backup_create.add_argument('--source', default=str(ETC_DIR), help='Каталог конфигурации; по умолчанию текущий Hub config')
    ap_backup_create.set_defaults(func=cmd_backup)
    ap_backup_verify = backup_sub.add_parser('verify', help='Проверить backup без распаковки')
    ap_backup_verify.add_argument('archive')
    ap_backup_verify.set_defaults(func=cmd_backup)
    ap_backup_restore = backup_sub.add_parser('restore', help='Атомарно восстановить backup в новый каталог')
    ap_backup_restore.add_argument('archive')
    ap_backup_restore.add_argument('target')
    ap_backup_restore.set_defaults(func=cmd_backup)
    ap_setup = sub.add_parser('setup', help='Установка и настройка')
    ap_setup.add_argument('--pkg-all')
    ap_setup.add_argument('--pkg-hub')
    ap_setup.add_argument('--pkg-shellmcp')
    ap_setup.add_argument('--silent', '--yes', dest='silent', action='store_true', help='Non-interactive install; defaults to hub+shellmcp+FRP')
    ap_setup.add_argument('--hub', action='store_true', help='Install hub component in non-interactive mode')
    ap_setup.add_argument('--shellmcp', '--shell', dest='shellmcp', action='store_true', help='Install ShellMCP/rootd component in non-interactive mode')
    ap_setup.add_argument('--no-hub', action='store_true', help='Do not install hub component')
    ap_setup.add_argument('--no-shellmcp', '--no-shell', dest='no_shellmcp', action='store_true', help='Do not install ShellMCP/rootd component')
    ap_setup.add_argument('--grepmesh', dest='grepmesh', action='store_true', help='Enable bundled GrepMesh companion (default with ShellMCP)')
    ap_setup.add_argument('--no-grepmesh', dest='no_grepmesh', action='store_true', help='Opt out of bundled GrepMesh companion')
    ap_setup.add_argument('--tunnel', choices=['frp', 'manual', 'cloudflare', 'none'], help='Public hub tunnel mode; --silent defaults to frp')
    ap_setup.add_argument('--hub-url', help='Existing public hub URL for manual tunnel or shell-only install')
    ap_setup.add_argument('--admin-password', help='One-time administrator password for secure shell-only MCP relay enrollment')
    ap_setup.add_argument('--mcp-relay-token', help='Legacy migration credential for an existing Hub; new installs use --admin-password')
    ap_setup.add_argument('--hub-port', help='Local hub port; default 9001')
    ap_setup.add_argument('--shell-transport', choices=['polling', 'webhook', 'websocket'], default='polling', help='Internal hub↔ShellMCP transport; default polling')
    ap_setup.add_argument('--shell-heartbeat', action='store_true', help='Enable optional ShellMCP heartbeat (disabled by default)')
    ap_setup.add_argument('--security-mode', choices=['normal', 'maximum', 'custom'], default=None, help='Process security profile; normal is frictionless by default')
    ap_setup.add_argument('--pair', help='Reserved one-time pairing token for GPTAdmin Cloud installs')
    ap_setup.add_argument('--user', action='store_true', help='Use per-user install paths/services')
    ap_setup.add_argument('--system', action='store_true', help='Use system install paths/services')
    ap_setup.set_defaults(func=setup_interactive)

    ap_update = sub.add_parser('update', help='Обновить установленную версию')
    ap_update.add_argument('--pkg-all')
    ap_update.add_argument('--pkg-hub')
    ap_update.add_argument('--pkg-shellmcp')
    ap_update.add_argument('--force', action='store_true', help='Reinstall even when manifest says the installed build is current')
    ap_update.add_argument('--hub', action='store_true', help='Force updating/installing hub component')
    ap_update.add_argument('--shellmcp', action='store_true', help='Force updating/installing ShellMCP component')
    ap_update.add_argument('--no-hub', action='store_true', help='Do not update hub component')
    ap_update.add_argument('--no-shellmcp', action='store_true', help='Do not update ShellMCP component')
    ap_update.add_argument('--grepmesh', dest='grepmesh', action='store_true', help='Enable bundled GrepMesh companion')
    ap_update.add_argument('--no-grepmesh', dest='no_grepmesh', action='store_true', help='Opt out of bundled GrepMesh companion')
    ap_update.add_argument('--user', action='store_true', help='Use per-user install paths/services')
    ap_update.add_argument('--system', action='store_true', help='Use system install paths/services')
    ap_update.add_argument('--auto', action='store_true', help='Run from the automatic updater; obey GPTADMIN_AUTO_UPDATE')
    ap_update.set_defaults(func=cmd_update)

    ap_security = sub.add_parser('security', help='Настроить режимы безопасности процессов')
    security_sub = ap_security.add_subparsers(dest='security_cmd')
    ap_security_profile = security_sub.add_parser('profile', help='Показать или изменить профиль process hardening')
    ap_security_profile.add_argument('--mode', choices=['normal', 'maximum', 'custom'], help='normal: штатная работа; maximum: строгая изоляция; custom: явные флаги')
    for flag, dest in (
        ('no-new-privileges', 'no_new_privileges'),
        ('private-tmp', 'private_tmp'),
        ('protect-system', 'protect_system'),
        ('protect-home', 'protect_home'),
    ):
        ap_security_profile.add_argument(f'--{flag}', dest=dest, action='store_true', default=None)
        ap_security_profile.add_argument(f'--allow-{flag}', dest=dest, action='store_false')
    ap_security_profile.add_argument('--allow-privileged-execution', dest='allow_privileged_execution', action='store_true', default=None)
    ap_security_profile.add_argument('--deny-privileged-execution', dest='allow_privileged_execution', action='store_false')
    ap_security_profile.set_defaults(func=cmd_security_profile)
    ap_security_bearer = security_sub.add_parser('bearer', help='Показать или изменить проверки подписанного bearer/OAuth')
    ap_security_bearer.add_argument('--mode', choices=['normal', 'maximum', 'custom'], help='normal: рабочий auth-контракт; maximum: все проверки; custom: явные проверки')
    for key in ('issuer', 'audience', 'resource', 'scope', 'subject', 'issued-at', 'expiry', 'pkce'):
        dest = 'require_' + key.replace('-', '_')
        ap_security_bearer.add_argument(f'--require-{key}', dest=dest, action='store_true', default=None)
        ap_security_bearer.add_argument(f'--allow-{key}', dest=dest, action='store_false')
    for key in ('token-lifecycle', 'redirect-allowlist', 'resource-allowlist'):
        dest = 'enforce_' + key.replace('-', '_')
        ap_security_bearer.add_argument(f'--enforce-{key}', dest=dest, action='store_true', default=None)
        ap_security_bearer.add_argument(f'--allow-{key}', dest=dest, action='store_false')
    ap_security_bearer.set_defaults(func=cmd_security_bearer)

    ap_autoupdate = sub.add_parser('auto-update', aliases=['autoupdate'], help='Управление автообновлением GPTAdmin')
    ap_autoupdate.add_argument('action', nargs='?', choices=['status', 'enable', 'disable', 'run'], default='status')
    ap_autoupdate.add_argument('--force', action='store_true', help='For run: reinstall even when already current')
    ap_autoupdate.add_argument('--user', action='store_true', help='Use per-user install paths/services')
    ap_autoupdate.add_argument('--system', action='store_true', help='Use system install paths/services')
    ap_autoupdate.set_defaults(func=cmd_autoupdate)

    ap_config = sub.add_parser('config', help='Настроить компоненты (shellmcp transport)')
    config_sub = ap_config.add_subparsers(dest='config_target')
    ap_conf = config_sub.add_parser('shellmcp', help='Настроить транспорт shellmcp: polling/webhook/websocket')
    ap_conf.add_argument('--transport', choices=['polling', 'webhook', 'websocket'])
    ap_conf.add_argument('--hub-url')
    ap_conf.add_argument('--shellmcp-url', '--shell-url', dest='shellmcp_url', help='URL ShellMCP agent для webhook режима')
    ap_conf.add_argument('--heartbeat', action='store_true', help='Enable optional ShellMCP heartbeat (disabled by default)')
    ap_conf.set_defaults(func=cmd_config_shellmcp)

    ap_urls = sub.add_parser('urls', help='Показать текущие публичные URL хаба, MCP и Actions')
    ap_urls.add_argument('--json', action='store_true')
    ap_urls.add_argument('--all', action='store_true', help='Показать не только shellmcp, но и все MCP-серверы')
    ap_urls.set_defaults(func=cmd_urls)

    sub.add_parser('status', help='Статус сервисов').set_defaults(func=cmd_status)
    sub.add_parser('start', help='Запустить сервисы').set_defaults(func=cmd_start)
    sub.add_parser('stop', help='Остановить сервисы').set_defaults(func=cmd_stop)
    sub.add_parser('restart', help='Перезапустить сервисы').set_defaults(func=cmd_restart)

    ap_instructions = sub.add_parser('instructions', help='Управление startup instructions хаба')
    instructions_sub = ap_instructions.add_subparsers(dest='instructions_cmd')
    instructions_sub.add_parser('show', help='Явно показать содержимое инструкций').set_defaults(func=cmd_instructions)
    instructions_sub.add_parser('path', help='Показать путь без содержимого').set_defaults(func=cmd_instructions)
    ap_instructions_set = instructions_sub.add_parser('set-file', help='Безопасно установить инструкции из UTF-8 файла')
    ap_instructions_set.add_argument('source')
    ap_instructions_set.set_defaults(func=cmd_instructions)

    hub = sub.add_parser('hub', help='Управление хабом')
    hub_sub = hub.add_subparsers(dest='hub_cmd')
    hub_sub.add_parser('status', help='Статус хаба').set_defaults(func=cmd_status)
    hub_sub.add_parser('start', help='Запустить хаб').set_defaults(func=cmd_start)
    hub_sub.add_parser('stop', help='Остановить хаб').set_defaults(func=cmd_stop)
    hub_sub.add_parser('restart', help='Перезапустить хаб').set_defaults(func=cmd_restart)

    for alias in ('shell', 'shellmcp'):
        rp = sub.add_parser(alias)
        rs = rp.add_subparsers(dest='svc_cmd')
        rs.add_parser('status').set_defaults(func=cmd_status)

    sub.add_parser('enable', help='Включить автозапуск сервисов').set_defaults(func=cmd_enable)
    sub.add_parser('disable', help='Выключить автозапуск сервисов').set_defaults(func=cmd_disable)

    ap_logs = sub.add_parser('logs', help='Логи сервисов (по умолчанию все; shell = shellmcp)')
    ap_logs.add_argument('service', nargs='?', default='all', metavar='service', help='hub | shell | frpc | all')
    ap_logs.set_defaults(func=cmd_logs)

    ap_tok = sub.add_parser('tokens', help='Показать состояние подключений GPTAdmin')
    ap_tok.add_argument('--show-shellmcp', action='store_true', help='Показать состояние подключения агента; секрет не выводится')
    ap_tok.set_defaults(func=cmd_tokens)

    ap_mcp_token_top = sub.add_parser('issue-token', aliases=['token'], help='Выпустить JWT для MCP-клиента без OAuth')
    ap_mcp_token_top.add_argument('name', nargs='?', help='client_id / имя токена, например codex-work')
    ap_mcp_token_top.add_argument('--ttl-days', type=int, default=DEFAULT_MCP_TOKEN_TTL_DAYS)
    ap_mcp_token_top.add_argument('--env-key', help='Имя переменной для сохранения в gptadmin.env')
    ap_mcp_token_top.add_argument('--no-save', action='store_true', help='Только напечатать token, не сохранять в gptadmin.env')
    ap_mcp_token_top.add_argument('--readonly', action='store_true', help='Только просмотр без shell-команд; найденные секреты скрываются')
    ap_mcp_token_top.set_defaults(func=cmd_mcp_token)

    ap_auth_diag = sub.add_parser('auth-diagnose', help='Безопасно проверить контракт JWT/Hub без раскрытия секретов')
    ap_auth_diag.add_argument('--token', help='JWT для локальной диагностики; значение не выводится')
    ap_auth_diag.add_argument('--offline', action='store_true', help='Не запрашивать public OAuth metadata')
    ap_auth_diag.set_defaults(func=cmd_auth_diagnose)

    ap_mcp_connect_top = sub.add_parser('connect-mcp', aliases=['mcp-connect'], help='Подключить GPTAdmin как MCP в локальных AI-клиентах')
    ap_mcp_connect_top.add_argument('--client', action='append', choices=['codex', 'claude', 'claude-code', 'opencode', 'vscode', 'fast-agent', 'zcode'], help='Кого настроить; можно повторять. По умолчанию все найденные')
    ap_mcp_connect_top.add_argument('--fresh', action='store_true', help='Выпустить новые токены для AI MCP clients')
    ap_mcp_connect_top.set_defaults(func=cmd_mcp_connect)

    ap_notice_place = sub.add_parser('notice-place', help='Единая точка Notice Place в локальных AI-клиентах')
    ap_notice_place_sub = ap_notice_place.add_subparsers(dest='notice_place_cmd')
    ap_notice_place_status = ap_notice_place_sub.add_parser('status', help='Показать текущие регистрации Notice Place (только чтение)')
    ap_notice_place_status.add_argument('--client', action='append', choices=list(NOTICE_PLACE_CLIENTS))
    ap_notice_place_status.add_argument('--json', action='store_true')
    ap_notice_place_status.set_defaults(func=cmd_notice_place)
    ap_notice_place_install = ap_notice_place_sub.add_parser('install', help='Свести регистрации Notice Place к одной канонической точке')
    ap_notice_place_install.add_argument('--client', action='append', choices=list(NOTICE_PLACE_CLIENTS))
    ap_notice_place_install.add_argument('--dry-run', action='store_true', help='Показать изменения без записи')
    ap_notice_place_install.add_argument('--refresh-plugin', action='store_true', help='Свести установленные копии плагина к каноническому пакету')
    ap_notice_place_install.add_argument('--json', action='store_true')
    ap_notice_place_install.set_defaults(func=cmd_notice_place)

    ap_rot = sub.add_parser('rotate', help='Переиздать токен (hub/shellmcp/mcp)')
    ap_rot.add_argument('which', metavar='which', help='hub | shell')
    ap_rot.set_defaults(func=cmd_rotate)

    ap_port = sub.add_parser('port', help='Сменить порт хаба')
    ap_port.add_argument('port', type=int)
    ap_port.set_defaults(func=cmd_port)

    ap_url = sub.add_parser('set-url', help='Задать публичный URL хаба (отключает FRP)')
    ap_url.add_argument('url')
    ap_url.set_defaults(func=cmd_seturl)

    ap_mcp = sub.add_parser('mcp', help='Управление MCP relay-агентами')
    mcp_sub = ap_mcp.add_subparsers(dest='mcp_cmd')

    ap_mcp_list = mcp_sub.add_parser('list', help='Список настроенных MCP-серверов')
    ap_mcp_list.add_argument('--json', action='store_true')
    ap_mcp_list.set_defaults(func=cmd_mcp_list)

    ap_mcp_token = mcp_sub.add_parser('token', help='Выпустить JWT для MCP-клиента без OAuth')
    ap_mcp_token.add_argument('name', nargs='?', help='client_id / имя токена')
    ap_mcp_token.add_argument('--ttl-days', type=int, default=DEFAULT_MCP_TOKEN_TTL_DAYS)
    ap_mcp_token.add_argument('--env-key')
    ap_mcp_token.add_argument('--no-save', action='store_true')
    ap_mcp_token.add_argument('--readonly', action='store_true', help='Только просмотр без shell-команд; найденные секреты скрываются')
    ap_mcp_token.set_defaults(func=cmd_mcp_token)

    ap_mcp_connect = mcp_sub.add_parser('connect', aliases=['self-install', 'install-self'], help='Подключить GPTAdmin как MCP в локальных AI-клиентах')
    ap_mcp_connect.add_argument('--client', action='append', choices=['codex', 'claude', 'claude-code', 'opencode', 'vscode', 'fast-agent', 'zcode'])
    ap_mcp_connect.add_argument('--fresh', action='store_true')
    ap_mcp_connect.set_defaults(func=cmd_mcp_connect)

    ap_mcp_catalog = mcp_sub.add_parser('catalog', help='Показать атрибутированный каталог MCP capability')
    ap_mcp_catalog.add_argument('--json', action='store_true')
    ap_mcp_catalog.set_defaults(func=cmd_mcp_catalog)

    ap_mcp_extension = mcp_sub.add_parser('extension-validate', aliases=['validate-extension'], help='Проверить manifest стороннего MCP extension')
    ap_mcp_extension.add_argument('manifest')
    ap_mcp_extension.set_defaults(func=cmd_mcp_extension_validate)

    ap_mcp_add = mcp_sub.add_parser('add', help='Добавить MCP-сервер (стиль Claude/Codex)')
    ap_mcp_add.add_argument('name')
    ap_mcp_add.add_argument('command', nargs='?', help='Command, e.g. npx')
    ap_mcp_add.add_argument('args', nargs=argparse.REMAINDER, help='Command args, e.g. -y mcp-remote https://...')
    ap_mcp_add.add_argument('--url', help='Shortcut for: npx -y mcp-remote URL')
    ap_mcp_add.add_argument('--stdio-format', choices=['auto', 'framed', 'ndjson', 'jsonl', 'content-length'])
    ap_mcp_add.add_argument('--cwd')
    ap_mcp_add.add_argument('--env', action='append', help='KEY=VALUE, repeatable')
    ap_mcp_add.add_argument('--agent-id')
    ap_mcp_add.add_argument('--run-as-user')
    ap_mcp_add.add_argument('--hub-url')
    ap_mcp_add.add_argument('--catalog-id', help='Bind this server to a curated capability definition')
    ap_mcp_add.add_argument('--accept-capability', action='store_true', help='Acknowledge the displayed catalog scope/risk before install')
    ap_mcp_add.add_argument('--disabled', action='store_true')
    ap_mcp_add.add_argument('--force', action='store_true')
    ap_mcp_add.add_argument('--install', action='store_true', help='Сразу установить и запустить relay service')
    ap_mcp_add.add_argument('--status', action='store_true', help='После добавления показать статус relay service')
    ap_mcp_add.set_defaults(func=cmd_mcp_add)

    ap_mcp_rm = mcp_sub.add_parser('remove', aliases=['rm'], help='Удалить MCP-сервер из конфига')
    ap_mcp_rm.add_argument('name')
    ap_mcp_rm.add_argument('--keep-service', action='store_true')
    ap_mcp_rm.add_argument('--backend', choices=['systemd', 'launchd', 'windows-task'])
    ap_mcp_rm.set_defaults(func=cmd_mcp_remove)

    ap_mcp_edit = mcp_sub.add_parser('edit', help='Редактировать mcp.json')
    ap_mcp_edit.set_defaults(func=cmd_mcp_edit)

    ap_mcp_cat = mcp_sub.add_parser('cat', help='Показать MCP-конфиг')
    ap_mcp_cat.add_argument('name', nargs='?')
    ap_mcp_cat.set_defaults(func=cmd_mcp_cat)

    for action_name, func, help_text in [
        ('import', cmd_mcp_import, 'Импортировать MCP из Claude/Codex'),
        ('export', cmd_mcp_export, 'Экспортировать MCP в Claude/Codex'),
        ('sync', cmd_mcp_sync, 'Синхронизировать MCP с Claude/Codex'),
    ]:
        p = mcp_sub.add_parser(action_name, help=help_text)
        p.add_argument('format', choices=['claude-desktop', 'claude-code', 'codex'])
        if action_name == 'export':
            p.add_argument('name', nargs='?')
            p.add_argument('--no-merge', action='store_true', help='Replace target file instead of merging')
        p.add_argument('--path')
        p.add_argument('--user')
        if action_name == 'import':
            p.add_argument('--force', action='store_true', help='Overwrite existing GPTAdmin entries')
        p.set_defaults(func=func)

    for action_name, func, help_text in [
        ('render', cmd_mcp_render, 'Показать конфиг супервизора'),
        ('install', cmd_mcp_install, 'Установить/запустить MCP-сервис'),
        ('status', cmd_mcp_status, 'Статус MCP-сервисов'),
    ]:
        p = mcp_sub.add_parser(action_name, help=help_text)
        p.add_argument('name', nargs='?')
        p.add_argument('--backend', choices=['systemd', 'launchd', 'windows-task'])
        p.set_defaults(func=func)

    ap_tun = sub.add_parser('tunnel', help='Управление туннелем (FRP/Cloudflare)')
    tun_sub = ap_tun.add_subparsers(dest='tun_cmd')
    tun_sub.add_parser('status', help='Статус туннеля').set_defaults(func=cmd_tunnel_status)
    tun_sub.add_parser('logs', help='Логи туннеля').set_defaults(func=cmd_tunnel_logs)
    tun_sub.add_parser('enable', help='Включить туннель').set_defaults(func=cmd_tunnel_enable)
    tun_sub.add_parser('disable', help='Выключить туннель').set_defaults(func=cmd_tunnel_disable)

    sub.add_parser('uninstall', help='Полное удаление GPTAdmin и всех сервисов').set_defaults(func=cmd_uninstall)

    args = ap.parse_args()
    if not getattr(args, 'cmd', None):
        ap.print_help(); return
    if args.cmd == 'mcp' and not getattr(args, 'mcp_cmd', None):
        ap_mcp.print_help(); return
    if args.cmd == 'instructions' and not getattr(args, 'instructions_cmd', None):
        ap_instructions.print_help(); return
    # Best-effort update hint (silent on any error, auto-update off, new version available).
    try:
        maybe_update_hint(args)
    except Exception:
        pass
    args.func(args)

if __name__ == '__main__':
    main()
