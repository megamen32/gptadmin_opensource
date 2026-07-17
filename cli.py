#!/usr/bin/env python3
# -*- coding: utf-8 -*-
from __future__ import annotations

import argparse
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
import urllib.request
import urllib.error
import pwd
from pathlib import Path
try:
    import tomllib
except Exception:
    tomllib = None

# ===== Platform =====
IS_MACOS = sys.platform == 'darwin'

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
MCP_RUNTIME_DIR = INSTALL_DIR / 'agents' / 'generic_stdio_mcp_relay'
MCP_MANAGER = MCP_RUNTIME_DIR / 'mcp_agent_manager.py'
MCP_RELAY = MCP_RUNTIME_DIR / 'generic_stdio_mcp_relay.py'

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
    SVC_FRPC_LABEL  = f'{SERVICE_PREFIX}.tunnel-frpc'
    SVC_CLOUDFLARED_LABEL = f'{SERVICE_PREFIX}.cloudflared'
    SVC_AUTO_UPDATE_LABEL = f'{SERVICE_PREFIX}.auto-update'
    UNIT_PATH_HUB   = SERVICES_DIR / f'{SVC_HUB_LABEL}.plist'
    UNIT_PATH_SHELLMCP = SERVICES_DIR / f'{SVC_SHELLMCP_LABEL}.plist'
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
    SYSTEMD_SHELLMCP = 'gptadmin-shellmcp.service'
    SYSTEMD_FRPC  = 'gptadmin-tunnel-frpc.service'
    SYSTEMD_CLOUDFLARED = 'gptadmin-cloudflared.service'
    SYSTEMD_AUTO_UPDATE = 'gptadmin-auto-update.service'
    SYSTEMD_AUTO_UPDATE_TIMER = 'gptadmin-auto-update.timer'
    UNIT_PATH_HUB   = SYSTEMD_DIR / SYSTEMD_HUB
    UNIT_PATH_SHELLMCP = SYSTEMD_DIR / SYSTEMD_SHELLMCP
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
FRPC_SERVER_ADDR_DEFAULT = 'gptadmin.bezrabotnyi.com'
FRPC_SERVER_PORT_DEFAULT = '7000'
FRPC_TOKEN_DEFAULT    = 'E10WCLE7ZFT+0NDgOFWwyPV8fb7hG7cLn320aHL0fVk='
FRPC_DOMAIN_DEFAULT   = 't.gptadmin.bezrabotnyi.com'
FRPC_SERVER_ENDPOINTS_DEFAULT = os.environ.get(
    'FRPC_SERVER_ENDPOINTS_DEFAULT',
    'primary=gptadmin.bezrabotnyi.com:7000,vpn2=vpn2.bezrabotnyi.com:27000,vusa=vusa.bezrabotnyi.com:27000'
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
        os.chmod(tmp, 0o640)
        os.replace(tmp, ENV_FILE)
    finally:
        tmp.unlink(missing_ok=True)


_PERSISTENT_AUTH_KEYS = frozenset({
    'CTL_TOKEN',
    'SHELLMCP_TOKEN',
    'ADMIN_PASSWORD',
    'OAUTH_CLIENT_SECRET',
    'MCP_BRIDGE_KEY',
    'MCP_RELAY_AGENT_TOKEN',
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


def env_remove_keys(keys: list[str]):
    cur = env_read()
    changed = False
    for key in keys:
        if key in cur:
            cur.pop(key, None)
            changed = True
    if changed:
        lines = [f'{k}={cur[k]}' for k in sorted(cur.keys())]
        ENV_FILE.parent.mkdir(parents=True, exist_ok=True)
        ENV_FILE.write_text('\n'.join(lines) + '\n')
        os.chmod(ENV_FILE, 0o640)

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

def download(url: str, dest: Path):
    """Download a file with curl's progress meter visible.

    curl's default meter shows total size, received bytes, average speed,
    elapsed time, estimated time left and current speed when Content-Length is
    available. Set GPTADMIN_DOWNLOAD_QUIET=1 to keep the old silent behavior.
    """
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


def _arch_tag() -> str:
    machine = (os.uname().machine if hasattr(os, 'uname') else '').lower()
    if machine in {'arm64', 'aarch64'}:
        return 'arm64'
    if machine in {'x86_64', 'amd64'}:
        return 'amd64'
    return machine or 'unknown'


def platform_pkg_url_default() -> str:
    platform = 'darwin' if IS_MACOS else 'linux'
    return os.environ.get('PKG_PLATFORM_URL', f'{PKG_BASE_URL_DEFAULT}/gptadmin-{platform}-{_arch_tag()}.tar.gz')


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
    return [tdp / 'gptadmin_hub' / tag / 'gptadmin_hub' for tag in tags] + legacy


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


def _install_hub_binary_from_pkg(tdp: Path):
    for c in _platform_hub_candidates(tdp):
        if c.exists() and _binary_looks_native(c):
            BIN_DIR.mkdir(parents=True, exist_ok=True)
            dst = BIN_DIR / 'gptadmin_hub'
            shutil.copy2(c, dst)
            os.chmod(dst, 0o755)
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
    out: list[Path] = []
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
            shutil.copy2(c, dst)
            os.chmod(dst, 0o755)
            _macos_unquarantine_and_codesign(dst)
            return
    die('Go ShellMCP/rootd binary not found in package. Legacy Python/PyInstaller shellmcp has been removed; ensure the package contains go-shellmcp/<platform>/<arch>/shellmcp-go or rootd-go.')


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
    for directory in (BIN_DIR, CLI_PATH.parent):
        if not directory.exists():
            continue
        for pattern in ('*.bak.*', '*.old', '*.new'):
            for path in directory.glob(pattern):
                try:
                    if path.is_dir():
                        shutil.rmtree(path)
                    else:
                        path.unlink()
                except FileNotFoundError:
                    pass


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
            return
        die(f'unknown component: {component}')

# ===== Service management =====

if IS_MACOS:
    def _mac_python() -> str:
        candidates = [
            os.environ.get('GPTADMIN_PYTHON'),
            '/Library/Frameworks/Python.framework/Versions/3.11/bin/python3',
            '/opt/homebrew/bin/python3',
            '/usr/local/bin/python3',
            sys.executable,
            '/usr/bin/python3',
        ]
        for c in candidates:
            if c and Path(c).exists():
                return c
        return 'python3'

    def _plist_path(label: str) -> Path:
        return SERVICES_DIR / f'{label}.plist'

    def _wrapper_script(name: str, bin_path: Path) -> Path:
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        script = BIN_DIR / f'run_{name}.sh'
        if name == 'shellmcp' and not _binary_looks_native(bin_path):
            exec_line = f'PYTHONPATH={INSTALL_DIR}/client${{PYTHONPATH:+:$PYTHONPATH}} exec {_mac_python()} {bin_path}'
        else:
            exec_line = f'exec {bin_path}'
        script.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'{exec_line}\n'
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
        domain = _launchd_domain()
        # A missing/unloaded launchd job is normal during update or first install.
        # `launchctl bootout` prints "Boot-out failed: 3: No such process" to
        # stderr in that case; suppress it so a harmless pre-cleanup does not look
        # like an update failure.
        _launchctl_capture(['launchctl', 'bootout', _launchd_service_target(label)])
        bootstrap = _launchctl_capture(['launchctl', 'bootstrap', domain, str(unit_path)])
        if bootstrap.returncode != 0 and not _launchd_is_loaded(label):
            # A stale launchd registration can transiently return EIO. Retry once
            # after removing the label, while keeping harmless output quiet.
            _launchctl_capture(['launchctl', 'remove', label])
            time.sleep(0.2)
            bootstrap = _launchctl_capture(['launchctl', 'bootstrap', domain, str(unit_path)])
        _launchctl_capture(['launchctl', 'enable', _launchd_service_target(label)])
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
                ['launchctl', 'kickstart', '-k', _launchd_service_target(label)],
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

    def _launchctl_capture(args: list[str]) -> subprocess.CompletedProcess:
        return subprocess.run(args, check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)

    def _launchd_service_target(label: str) -> str:
        return f'{_launchd_domain()}/{label}'

    def _launchd_is_loaded(label: str) -> bool:
        return _launchctl_capture(['launchctl', 'print', _launchd_service_target(label)]).returncode == 0

    def _launchd_stop(label: str, unit_path: Path) -> tuple[bool, list[str]]:
        attempts = [
            ['launchctl', 'bootout', _launchd_service_target(label)],
            ['launchctl', 'remove', label],
        ]
        if unit_path.exists():
            attempts.insert(1, ['launchctl', 'bootout', _launchd_domain(), str(unit_path)])
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
    LINUX_HARDENING = '' if IS_USER_INSTALL else f'NoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=full\nProtectHome=true\nReadWritePaths={ETC_DIR} {INSTALL_DIR} {Path.home() / ".gptadmin"}\n'

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

    FRPC_UNIT_TPL = """[Unit]
Description=FRP client for GPTAdmin
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

    def write_hub_unit(install_hub: bool, _install_shellmcp: bool):
        if install_hub:
            UNIT_PATH_HUB.parent.mkdir(parents=True, exist_ok=True)
            UNIT_PATH_HUB.write_text(UNIT_HUB)

    def write_shellmcp_unit(_install_hub: bool, install_shellmcp: bool):
        if install_shellmcp:
            UNIT_PATH_SHELLMCP.parent.mkdir(parents=True, exist_ok=True)
            UNIT_PATH_SHELLMCP.write_text(UNIT_SHELLMCP)

    def write_frpc_unit(frpc_bin: str):
        UNIT_PATH_FRPC.parent.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_frpc_all.sh'
        wrapper.write_text(frpc_wrapper_script(frpc_bin, env_read()))
        os.chmod(wrapper, 0o755)
        UNIT_PATH_FRPC.write_text(FRPC_UNIT_TPL.format(
            frpc_bin=wrapper,
            hardening=LINUX_HARDENING,
            wanted_by=LINUX_WANTED_BY,
        ))

    def write_cloudflared_unit(cloudflared_bin: str, env: dict):
        UNIT_PATH_CLOUDFLARED.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_CLOUDFLARED.write_text(CLOUDFLARED_UNIT_TPL.format(
            cloudflared_bin=cloudflared_bin, env_file=ENV_FILE, hub_port=env.get('HUB_PORT', '9001'),
            hardening=LINUX_HARDENING, wanted_by=LINUX_WANTED_BY))


    def write_autoupdate_unit(env: dict):
        UNIT_PATH_AUTO_UPDATE.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_AUTO_UPDATE.write_text(AUTO_UPDATE_SERVICE_TPL.format(
            env_file=ENV_FILE, cli_path=CLI_PATH, install_scope=INSTALL_SCOPE))

    def svc_hub_name():   return SYSTEMD_HUB
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
cleanup() {{
  trap - TERM INT EXIT
  for pid in "${{pids[@]}}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}}
trap cleanup TERM INT EXIT
for conf in "${{CONFS[@]}}"; do
  "$FRPC_BIN" -c "$conf" &
  pids+=("$!")
done
while true; do
  for pid in "${{pids[@]}}"; do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" || exit $?
      exit 1
    fi
  done
  sleep 2
done
""".format(frpc_bin=str(frpc_bin), confs=confs)


def write_frpc_conf(env: dict):
    FRPC_CONF.parent.mkdir(parents=True, exist_ok=True)
    local_port = env.get('HUB_PORT', '9001')
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
localIP = "127.0.0.1"
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
    Leaving PUBLIC_ORIGIN/MCP_RESOURCE unset makes the Go hub use the
    legacy global default gptadminmcp.bezrabotnyi.com, which redirects users to
    the wrong authorization server/password.
    """
    public = (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '').rstrip('/')
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
        if res.returncode == 0 and 'gptadmin_hub' in (res.stdout or ''):
            return True
        last_err = (res.stderr or res.stdout or f'curl rc={res.returncode}').strip()
        time.sleep(2)
    print('WARNING: Local hub health check did not pass before starting dependent services' + (f': {last_err}' if last_err else ''), file=sys.stderr)
    return False


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
    # This is the security boundary for local auto-approve: the pending server
    # must match the private key material generated on this machine. Hostname,
    # base_url, mode, and client IP are advisory and can be spoofed.
    for key in ('server_id', 'public_key', 'fingerprint'):
        expected = str(expected_identity.get(key) or '')
        actual = str(server.get(key) or payload.get(key) or '')
        if expected and actual and expected != actual:
            return False
    actual_server_id = server.get('server_id') or payload.get('server_id')
    actual_public_key = server.get('public_key') or payload.get('public_key')
    return bool(expected_identity.get('server_id') and actual_server_id and expected_identity.get('public_key') and actual_public_key)


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


def maybe_autoapprove_local_shellmcp(env: dict, install_hub: bool, install_shellmcp: bool) -> None:
    """Approve the local shell agent created by a same-machine hub+shell setup.

    A fresh hub intentionally keeps unknown shell agents pending. During a bundled
    local install the agent was just generated by this setup command, so approving
    it avoids a broken first-run where long-poll returns 401 until the user finds
    the pending approval step.
    """
    if not (install_hub and install_shellmcp):
        return
    flag = os.environ.get('GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP', env.get('GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP', '1')).strip().lower()
    if flag in {'0', 'false', 'no', 'off'}:
        print('Local ShellMCP auto-approve skipped: GPTADMIN_AUTO_APPROVE_LOCAL_SHELLMCP=0')
        return
    token = env.get('CTL_TOKEN') or ''
    hub_port = env.get('HUB_PORT', '9001')
    if not token:
        print('WARNING: Local ShellMCP auto-approve skipped: CTL_TOKEN is empty', file=sys.stderr)
        return
    # launchd may report the service as loaded before the hub process actually
    # accepts connections. Re-check here so auto-approve does not race first
    # registration and leave the local ShellMCP pending with 401 queue polls.
    health_env = dict(env)
    health_env.setdefault('HUB_PORT', hub_port or '9001')
    wait_local_hub_health(health_env, timeout_s=180)

    base = f'http://127.0.0.1:{hub_port}'
    headers = ['-H', f'Authorization: Bearer {token}', '-H', 'Content-Type: application/json']

    def curl_json(path: str, payload: dict | None = None) -> dict:
        cmd = ['curl', '-fsS', '--max-time', '10', *headers]
        if payload is not None:
            cmd += ['-d', json.dumps(payload, separators=(',', ':'))]
        cmd.append(base + path)
        res = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if res.returncode != 0:
            raise RuntimeError((res.stderr or res.stdout or f'curl rc={res.returncode}').strip())
        return json.loads(res.stdout or '{}')

    expected_identity = _load_local_shellmcp_identity(env, timeout_s=60)
    if not expected_identity:
        print('WARNING: Local ShellMCP auto-approve skipped: local shellmcp identity did not appear', file=sys.stderr)
        return

    last_err = ''
    for attempt in range(1, 31):
        try:
            data = curl_json('/servers')
            pending = data.get('pending') or []
            servers = data.get('servers') or []
            for x in servers:
                if not isinstance(x, dict):
                    continue
                if _server_active_matches_local_shell_identity(x, expected_identity):
                    print('Local ShellMCP auto-approve: already active')
                    return
            approved = []
            mismatched = []
            for item in pending:
                if not isinstance(item, dict):
                    continue
                name = str(item.get('name') or '')
                if not name:
                    continue
                if not _server_matches_local_shell_identity(item, expected_identity):
                    mismatched.append(name)
                    continue
                payload = {'target': 'hub', 'tool_name': 'approve_pending_server', 'arguments': {'name': name}, 'timeout': 30}
                res = curl_json('/mcp-relay/call', payload)
                if _approve_pending_response_ok(res):
                    approved.append(name)
            if approved:
                for _ in range(10):
                    data2 = curl_json('/servers')
                    for x in data2.get('servers') or []:
                        if _server_active_matches_local_shell_identity(x, expected_identity):
                            print('Local ShellMCP auto-approved: ' + ', '.join(approved))
                            return
                    time.sleep(1)
                print('Local ShellMCP auto-approved: ' + ', '.join(approved))
                return
            if mismatched:
                last_err = 'pending server(s) did not match local shellmcp identity: ' + ', '.join(mismatched)
        except Exception as e:
            last_err = str(e)
        time.sleep(2)
    print('WARNING: Local ShellMCP auto-approve did not complete' + (f': {last_err}' if last_err else ''), file=sys.stderr)

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

    env.setdefault('CTL_TOKEN', gen_hex())
    env.setdefault('SHELLMCP_TOKEN', gen_hex())
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    if install_hub:
        # Remote MCP relays authenticate independently from administrator
        # clients.  A ShellMCP-only installation must receive this exact token
        # from the Hub it connects to, never a freshly generated local CTL key.
        env.setdefault('MCP_RELAY_AGENT_TOKEN', gen_hex())
    if install_shellmcp:
        env.setdefault('SHELLMCP_AUTO_UPDATE', '1')
        ensure_shellmcp_default_user(env)
        ensure_shellmcp_identity_env(env)
        env.setdefault('SHELLMCP_UPDATE_INTERVAL_S', '3600')
        env.setdefault('SHELLMCP_UPDATE_TOKEN', env.get('CTL_TOKEN', ''))
        env.setdefault('SHELLMCP_UPDATE_MANIFEST_URL', (env.get('HUB_URL') or env.get('HUB_PUBLIC_URL') or 'https://gptadmin.bezrabotnyi.com').rstrip('/') + '/artifacts/shellmcp.json')
        env.setdefault('SHELLMCP_SERVICE_NAME', svc_shellmcp_name())
        env.setdefault('SHELLMCP_SERVICE_SCOPE', INSTALL_SCOPE)
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
        if relay_token:
            env['MCP_RELAY_AGENT_TOKEN'] = relay_token.strip()
        elif not silent:
            relay_token = ask('Введите MCP_RELAY_AGENT_TOKEN этого Hub (нужен для MCP relay; Enter — настроить позже)')
            if relay_token:
                env['MCP_RELAY_AGENT_TOKEN'] = relay_token

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
        hub_for_update = (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or 'https://gptadmin.bezrabotnyi.com').rstrip('/')
        env['SHELLMCP_UPDATE_MANIFEST_URL'] = hub_for_update + '/artifacts/shellmcp.json'
        env['SHELLMCP_UPDATE_TOKEN'] = env.get('SHELLMCP_UPDATE_TOKEN') or env.get('CTL_TOKEN', '')
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
    sync_oauth_origin_env(env)
    env_set_many(env)

    BIN_DIR.mkdir(parents=True, exist_ok=True)
    CLI_PATH.parent.mkdir(parents=True, exist_ok=True)
    pkg_all   = args.pkg_all   or platform_pkg_url_default()
    pkg_hub   = args.pkg_hub   or PKG_HUB_URL_DEFAULT
    pkg_shellmcp = args.pkg_shellmcp or PKG_SHELLMCP_URL_DEFAULT

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
        wait_local_hub_health(env)
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
            env['SHELLMCP_UPDATE_TOKEN'] = env.get('SHELLMCP_UPDATE_TOKEN') or env.get('CTL_TOKEN', '')
        else:
            env['HUB_URL'] = public_url
        sync_oauth_origin_env(env)
        env_set_many(env)
    if install_shellmcp:
        svc_enable_start(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
        maybe_import_and_install_mcp_from_desktop_clients()
    svc_autoupdate_enable_start(env_read())
    maybe_autoapprove_local_shellmcp(env, install_hub, install_shellmcp)
    auto_configure_ai_mcp_clients(env_read(), install_hub)

    env = env_read()
    print('\n=== Готово ===')
    if install_hub:
        print(f"Hub URL: {env.get('HUB_PUBLIC_URL', '—')}")
        print(f"API-Ключ (Bearer): {env['CTL_TOKEN']}")
    if install_shellmcp and not install_hub:
        print(f"HUB_URL для ShellMCP: {env.get('HUB_URL', '—')}")
    if install_shellmcp:
        print('ShellMCP agent установлен.')

    installed = [n for n, p in [
        ('gptadmin-hub' if not IS_MACOS else SVC_HUB_LABEL, UNIT_PATH_HUB),
        (svc_shellmcp_name(), UNIT_PATH_SHELLMCP),
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

4) Заменитие в "servers": "url": на свой Hub URL {env.get('HUB_PUBLIC_URL') or env.get('HUB_URL', '—')}

5) В разделе «Аутентификация» выберите тип API ключ, Bearer и вставьте ключ {env['CTL_TOKEN']}
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
    return (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or os.environ.get('GPTADMIN_MCP_RELAY_HUB') or 'https://gptadmin.bezrabotnyi.com').rstrip('/')


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
    return bool(config and config.strip())


def _mcp_slug(name: str) -> str:
    return re.sub(r'[^A-Za-z0-9_.-]+', '-', name.strip()).strip('-._') or 'mcp'


def _mcp_agent_id(name: str, server: dict) -> str:
    if server.get('agent_id'):
        return str(server['agent_id'])
    return f"{socket.gethostname()}-{_mcp_slug(name)}"


def _mcp_ensure_token_file():
    env = env_read()
    token = env.get('MCP_RELAY_AGENT_TOKEN') or os.environ.get('GPTADMIN_MCP_RELAY_TOKEN')
    if MCP_TOKEN_FILE.exists() and not token:
        return
    if not token:
        die(
            'MCP relay token is not configured. Re-run setup with --mcp-relay-token TOKEN '
            'or set MCP_RELAY_AGENT_TOKEN in gptadmin.env; this must be the token of the target Hub.'
        )
    token = token.strip()
    if MCP_TOKEN_FILE.exists() and MCP_TOKEN_FILE.read_text(encoding='utf-8').strip() == token:
        return
    MCP_TOKEN_FILE.parent.mkdir(parents=True, exist_ok=True)
    MCP_TOKEN_FILE.write_text(token + '\n', encoding='utf-8')
    os.chmod(MCP_TOKEN_FILE, 0o640)


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
    _mcp_ensure_token_file()
    MCP_AGENTS_DIR.mkdir(parents=True, exist_ok=True)
    path = MCP_AGENTS_DIR / f'{_mcp_slug(name)}.json'
    _json_write(path, _mcp_agent_config(name, cfg))
    _mcp_fix_access_for_agent_config(path, cfg, name)
    return path


def _mcp_sync_go_supervisor_config(cfg: dict) -> None:
    """Make Go ShellMCP own MCP relay children in one aggregate registry."""
    agents = []
    relay = INSTALL_DIR / 'agents' / 'generic_stdio_mcp_relay' / 'generic_stdio_mcp_relay.py'
    python = sys.executable or 'python3'
    for name, server in sorted((cfg.get('mcpServers') or {}).items()):
        if not server.get('enabled', True):
            continue
        agent_path = MCP_AGENTS_DIR / f'{_mcp_slug(name)}.json'
        agents.append({
            'ref': _mcp_agent_id(name, server),
            'name': str(server.get('name') or name),
            'command': python,
            'args': [str(relay), '--agent-config', str(agent_path)],
            'cwd': str(server.get('cwd') or '/'),
            'enabled': True,
        })
    _json_write(MCP_SUPERVISOR_CONFIG, agents)


def _mcp_refresh_generated_configs(cfg: dict) -> None:
    """Regenerate relay inputs and the aggregate ShellMCP supervisor registry."""
    for name in sorted((cfg.get('mcpServers') or {}).keys()):
        _mcp_write_agent_config(name, cfg)
    _mcp_sync_go_supervisor_config(cfg)



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

def _mcp_runtime_candidates() -> list[Path]:
    here = Path(__file__).resolve()
    return [
        MCP_RUNTIME_DIR,
        here.parent.parent / 'agents' / 'generic_stdio_mcp_relay',
        here.parent / 'agents' / 'generic_stdio_mcp_relay',
    ]


def _mcp_runtime_dir() -> Path | None:
    for d in _mcp_runtime_candidates():
        if (d / 'mcp_agent_manager.py').exists() and (d / 'generic_stdio_mcp_relay.py').exists():
            return d
    return None


def _mcp_manager_exists():
    return _mcp_runtime_dir() is not None


def _mcp_manager_cmd(action: str, agent_config: Path, backend: str | None = None) -> list:
    runtime = _mcp_runtime_dir()
    if not runtime:
        expected = ', '.join(str(x) for x in _mcp_runtime_candidates())
        die(f'MCP runtime is not installed. Expected generic_stdio_mcp_relay under one of: {expected}. Install/update GPTAdmin package first.')
    cmd = [sys.executable or 'python3', str(runtime / 'mcp_agent_manager.py'), action, str(agent_config)]
    if backend:
        cmd += ['--backend', backend]
    return cmd


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
        print(f"{name}\t{'enabled' if enabled else 'disabled'}\t{fmt}\t{cmd} {argv}")



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
        if not args.command:
            die('provide --url URL or COMMAND [ARGS...]')
        if args.command.startswith('-'):
            die(
                f'invalid MCP command {args.command!r}: the first token after NAME must be an executable. '
                f'For Chrome DevTools use: gptadmin mcp add chrome-devtools npx -y '
                f'chrome-devtools-mcp@latest --browser-url=http://127.0.0.1:9223'
            )
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
    _mcp_save(cfg)
    agent_config = _mcp_write_agent_config(args.name, cfg)
    _mcp_sync_go_supervisor_config(cfg)
    print(f'Added MCP server {args.name}')
    print(f'Config: {MCP_CONFIG_FILE}')
    print(f'Agent config: {agent_config}')
    if getattr(args, 'install', False):
        if args.disabled:
            print(f'Skip disabled MCP server: {args.name}')
        elif _mcp_go_supervisor_enabled():
            print(f'ShellMCP supervisor will manage MCP server {args.name}')
        else:
            print(f'Installing MCP server {args.name}: {agent_config}')
            run(_mcp_manager_cmd('install', agent_config))
    if getattr(args, 'status', False):
        print(f'### {args.name}')
        run(_mcp_manager_cmd('status', agent_config), check=False)

def cmd_mcp_remove(args):
    need_root()
    cfg = _mcp_config()
    servers = cfg.get('mcpServers') or {}
    if args.name not in servers:
        die(f'MCP server not found: {args.name}')
    if not args.keep_service:
        agent_config = MCP_AGENTS_DIR / f'{_mcp_slug(args.name)}.json'
        if agent_config.exists() and _mcp_manager_exists():
            run(_mcp_manager_cmd('uninstall', agent_config, args.backend), check=False)
    servers.pop(args.name)
    _mcp_save(cfg)
    _mcp_sync_go_supervisor_config(cfg)
    try:
        (MCP_AGENTS_DIR / f'{_mcp_slug(args.name)}.json').unlink(missing_ok=True)
    except Exception:
        pass
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
    for name in _mcp_names_from_arg(args, cfg):
        agent_config = _mcp_write_agent_config(name, cfg)
        print(f'### {name}: {agent_config}')
        run(_mcp_manager_cmd('render', agent_config, args.backend), check=False)


def cmd_mcp_install(args):
    need_root()
    cfg = _mcp_config()
    names = _mcp_names_from_arg(args, cfg)
    if not names:
        die('no MCP servers configured')
    if _mcp_go_supervisor_enabled():
        _mcp_refresh_generated_configs(cfg)
        print('ShellMCP supervisor manages MCP relay services; standalone install skipped')
        return
    for name in names:
        if not (cfg.get('mcpServers') or {}).get(name, {}).get('enabled', True):
            print(f'Skip disabled MCP server: {name}')
            continue
        agent_config = _mcp_write_agent_config(name, cfg)
        print(f'Installing MCP server {name}: {agent_config}')
        run(_mcp_manager_cmd('install', agent_config, args.backend))


def cmd_mcp_status(args):
    cfg = _mcp_config()
    for name in _mcp_names_from_arg(args, cfg):
        agent_config = _mcp_write_agent_config(name, cfg)
        print(f'### {name}')
        run(_mcp_manager_cmd('status', agent_config, args.backend), check=False)


def cmd_mcp_cat(args):
    cfg = _mcp_config()
    if args.name:
        print(json.dumps(_mcp_agent_config(args.name, cfg), ensure_ascii=False, indent=2))
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
    if not _mcp_manager_exists():
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

def cmd_doctor(_):
    """Health check — services, ports, config, tokens."""
    print_header('GPTAdmin Doctor')
    issues = 0
    # Check services
    units = installed_units()
    if not units:
        print_err('No services installed. Run: gptadmin setup')
        issues += 1
    else:
        for label, path in units:
            exists = path.exists()
            if exists:
                print_ok(f'{label} — unit installed')
            else:
                print_err(f'{label} — unit missing')
                issues += 1
    # Check config
    env = env_read()
    ctl = env.get('CTL_TOKEN', '')
    if ctl:
        print_ok(f'CTL_TOKEN is set ({len(ctl)} chars)')
    else:
        print_err('CTL_TOKEN is not set')
        issues += 1
    hub_url = env.get('HUB_URL', env.get('PUBLIC_ORIGIN', ''))
    if hub_url:
        print_ok(f'Hub URL: {hub_url}')
    else:
        print_warn('Hub URL is not set (needed for agents to connect)')
        issues += 1
    # Check port
    hub_port = env.get('HUB_PORT', '9001')
    try:
        import socket as _sock
        sock = _sock.socket(_sock.AF_INET, _sock.SOCK_STREAM)
        sock.settimeout(1)
        result = sock.connect_ex(('127.0.0.1', int(hub_port)))
        sock.close()
        if result == 0:
            print_ok(f'Port {hub_port} is listening')
        else:
            print_warn(f'Port {hub_port} is not listening (hub not running?)')
            issues += 1
    except Exception:
        pass
    # Summary
    print()
    if issues == 0:
        print_ok('All checks passed.')
    else:
        print_warn(f'{issues} issue(s) found. Fix them before proceeding.')

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
    print_header('GPTAdmin Tokens')
    ctl = env.get('CTL_TOKEN', '')
    print(f'  {c_dim("CTL_TOKEN")}     {c_green(ctl) if ctl else c_red("(not set)")}')
    print(f'  {c_dim("HUB_URL")}       {env.get("HUB_URL", env.get("PUBLIC_ORIGIN", c_dim("(not set)")))}')
    # MCP bearer tokens
    for k in sorted(env):
        if k.startswith('GPTADMIN_') and k.endswith('_MCP_BEARER'):
            val = env[k]
            label = k.replace('GPTADMIN_', '').replace('_MCP_BEARER', '').lower()
            print(f'  {c_dim("MCP_BEARER")}    {c_cyan(label)}: {c_green(val[:16] + "..." if len(val) > 20 else val) if val else c_red("(not set)")}')
    # ShellMCP token
    shell_tok = env.get('SHELLMCP_TOKEN', '')
    if show_shell:
        print(f'  {c_dim("SHELLMCP_TOKEN")} {c_yellow(shell_tok) if shell_tok else c_red("(not set)")}')
        print_warn('SHELLMCP_TOKEN is sensitive — do not share it.')
    else:
        print(f'  {c_dim("SHELLMCP_TOKEN")} {c_yellow("(hidden, use --show-shellmcp to reveal)")}')
    # MCP_BRIDGE_KEY
    bridge = env.get('MCP_BRIDGE_KEY', '')
    if bridge and bridge != ctl:
        print(f'  {c_dim("MCP_BRIDGE_KEY")} {c_green(bridge[:16] + "...")}')
    print()
    print(c_dim('  Issue new MCP token:  gptadmin token issue <name>'))
    print(c_dim('  Rotate tokens:        gptadmin token rotate [hub|shellmcp|mcp]'))

def cmd_rotate(args):
    need_root()
    which = args.which
    if which in ('shellmcp', 'shell', 'shell-mcp'):
        which = 'shellmcp'
    if which not in ('hub', 'shellmcp'):
        die('unknown token target. Use: hub or shellmcp')
    newtok = gen_hex()
    if which == 'hub':
        env_set_many({'CTL_TOKEN': newtok})
        if UNIT_PATH_HUB.exists():
            svc_restart(svc_hub_name(), UNIT_PATH_HUB)
        print(f'New hub CTL_TOKEN: {newtok}')
    else:
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
        print_warn('server list is unavailable; set CTL_TOKEN in config or run with sudo/--system')

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
    return url.rstrip('/').rsplit('/', 1)[-1]


def _remote_artifact_build_info(pkg_url: str) -> dict:
    if os.environ.get('GPTADMIN_UPDATE_SKIP_MANIFEST', '').strip().lower() in {'1', 'true', 'yes', 'on'}:
        return {}
    base = pkg_url.rsplit('/', 1)[0]
    manifest_url = os.environ.get('GPTADMIN_MANIFEST_URL') or (base.rstrip('/') + '/manifest.json')
    name = _artifact_name_from_url(pkg_url)
    try:
        with urllib.request.urlopen(manifest_url, timeout=5) as r:
            manifest = json.loads(r.read().decode('utf-8', 'replace'))
    except Exception as exc:
        print(f'WARNING: update manifest unavailable, continuing with download: {exc}', file=sys.stderr)
        return {}
    artifact = (manifest.get('artifacts') or {}).get(name) or {}
    return {k: artifact.get(k) for k in ('build_version', 'build_ts', 'git_commit', 'sha256', 'size') if artifact.get(k) is not None}


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
    pairs = []
    if install_hub:
        pairs.append((svc_hub_name(), UNIT_PATH_HUB))
    if env.get('FRP_ENABLE', 'false') == 'true':
        pairs.append((svc_frpc_name(), UNIT_PATH_FRPC))
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        pairs.append((svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED))
    if install_shellmcp:
        pairs.append((svc_shellmcp_name(), UNIT_PATH_SHELLMCP))
    return pairs


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

    env.setdefault('CTL_TOKEN', gen_hex())
    env.setdefault('SHELLMCP_TOKEN', gen_hex())
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    env['INSTALL_HUB'] = 'true' if install_hub else 'false'
    env['INSTALL_SHELLMCP'] = 'true' if install_shellmcp else 'false'
    env.setdefault('GPTADMIN_AUTO_UPDATE', 'true')
    env.setdefault('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC', '21600')
    if getattr(args, 'auto', False) and not auto_update_enabled(env):
        print('GPTAdmin auto-update is disabled; skipping automatic update.')
        return
    if install_shellmcp:
        ensure_shellmcp_default_user(env)
        ensure_shellmcp_identity_env(env)
        env.setdefault('SHELLMCP_AUTO_UPDATE', '1')
        hub_for_update = (env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or 'https://gptadmin.bezrabotnyi.com').rstrip('/')
        env['SHELLMCP_UPDATE_MANIFEST_URL'] = hub_for_update + '/artifacts/shellmcp.json'
        env['SHELLMCP_UPDATE_TOKEN'] = env.get('SHELLMCP_UPDATE_TOKEN') or env.get('CTL_TOKEN', '')
        env['SHELLMCP_SERVICE_NAME'] = svc_shellmcp_name()
        env['SHELLMCP_SERVICE_SCOPE'] = INSTALL_SCOPE
    sync_oauth_origin_env(env)
    env_set_many(env)

    pkg_all = args.pkg_all or platform_pkg_url_default()
    pkg_hub = args.pkg_hub or PKG_HUB_URL_DEFAULT
    pkg_shellmcp = args.pkg_shellmcp or PKG_SHELLMCP_URL_DEFAULT

    target_pkg = pkg_all if (install_hub and install_shellmcp) else (pkg_hub if install_hub else pkg_shellmcp)
    remote_info = _remote_artifact_build_info(target_pkg)
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

    print('Stopping installed GPTAdmin services for safe in-place update...')
    svc_stop_multi(_service_pairs_for_update(install_hub, install_shellmcp, env))

    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        if install_hub and install_shellmcp:
            print('[Update] downloading full package...')
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
            print('[Update] downloading hub package...')
            pkg = tdp / 'hub.tgz'
            try:
                download(pkg_hub, pkg)
            except subprocess.CalledProcessError:
                print('  Component package unavailable, using full package...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'hub')
        elif install_shellmcp:
            print('[Update] downloading shellmcp package...')
            pkg = tdp / 'shellmcp.tgz'
            try:
                download(pkg_shellmcp, pkg)
            except subprocess.CalledProcessError:
                print('  Component package unavailable, using full package...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'shellmcp')

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
        wait_local_hub_health(env, timeout_s=90)
    if env.get('FRP_ENABLE', 'false') == 'true':
        svc_frpc_enable_start_all(env)
    if env.get('TUNNEL_MODE') == 'cloudflare' or env.get('CLOUDFLARE_TUNNEL_ENABLE', 'false') == 'true':
        svc_enable_start(svc_cloudflared_name(), UNIT_PATH_CLOUDFLARED)
    if install_shellmcp:
        svc_enable_start(svc_shellmcp_name(), UNIT_PATH_SHELLMCP)
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

def _b64url_bytes(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b'=').decode()


def _b64url_json(obj: dict) -> str:
    return _b64url_bytes(json.dumps(obj, separators=(',', ':')).encode())


def make_mcp_bearer_token(env: dict, client_id: str, ttl_days: int = 365, access_mode: str = 'full') -> str:
    secret = env.get('OAUTH_CLIENT_SECRET') or ''
    if not secret:
        raise RuntimeError('OAUTH_CLIENT_SECRET is missing')
    origin = (env.get('PUBLIC_ORIGIN') or env.get('HUB_PUBLIC_URL') or env.get('HUB_URL') or '').rstrip('/')
    resource = (env.get('MCP_RESOURCE') or origin).rstrip('/')
    if not origin or not resource:
        raise RuntimeError('PUBLIC_ORIGIN/MCP_RESOURCE is missing')
    now = int(time.time())
    ttl_days = max(1, int(ttl_days or 365))
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
        'iat': now,
        'exp': now + ttl_days * 24 * 3600,
    }
    signing_input = f'{_b64url_json(header)}.{_b64url_json(body)}'.encode()
    sig = hmac.new(secret.encode(), signing_input, hashlib.sha256).digest()
    return signing_input.decode() + '.' + _b64url_bytes(sig)


def _mcp_client_url(env: dict) -> str:
    # ShellMCP can intentionally use a loopback HUB_URL while desktop clients
    # need the canonical externally reachable Hub identity.
    base = (env.get('HUB_PUBLIC_URL') or env.get('PUBLIC_ORIGIN') or env.get('HUB_URL') or '').rstrip('/')
    if not base:
        base = f"http://127.0.0.1:{env.get('HUB_PORT', '9001')}"
    return base + '/mcp'


def _client_token_env_key(client_id: str) -> str:
    safe = re.sub(r'[^A-Za-z0-9]+', '_', client_id.upper()).strip('_') or 'CUSTOM'
    return f'GPTADMIN_{safe}_MCP_BEARER'


def issue_mcp_bearer(env: dict, client_id: str, ttl_days: int = 365, access_mode: str = 'full') -> tuple[str, str, str]:
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
    ttl_days = int(getattr(args, 'ttl_days', 365) or 365)
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


def configure_ai_mcp_clients(env: dict, *, rotate: bool = False, clients: set[str] | None = None, print_custom: bool = False) -> dict:
    env = dict(env)
    sync_oauth_origin_env(env)
    env.setdefault('OAUTH_CLIENT_SECRET', gen_hex(32))
    env.setdefault('ADMIN_PASSWORD', gen_hex())
    wanted = clients or {'claude-code', 'codex', 'opencode', 'vscode'}
    tokens = {
        'GPTADMIN_CLAUDE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_CLAUDE_MCP_BEARER')) or make_mcp_bearer_token(env, 'claude-code'),
        'GPTADMIN_CODEX_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_CODEX_MCP_BEARER')) or make_mcp_bearer_token(env, 'codex'),
        'GPTADMIN_OPENCODE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_OPENCODE_MCP_BEARER')) or make_mcp_bearer_token(env, 'opencode'),
        'GPTADMIN_VSCODE_MCP_BEARER': ('' if rotate else env.get('GPTADMIN_VSCODE_MCP_BEARER')) or make_mcp_bearer_token(env, 'vscode'),
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
    # MCP stdio relays are separate launchd/systemd jobs.  Leaving them behind
    # makes a claimed full reinstall retain old processes and credentials.
    if _mcp_manager_exists():
        for agent_config in MCP_AGENTS_DIR.glob('*.json'):
            try:
                run(_mcp_manager_cmd('uninstall', agent_config), check=False)
            except Exception as e:
                failures.append(f'не удалось удалить MCP relay {agent_config.name}: {e}')
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
    cmd = getattr(args, 'command', None)
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
    # Backward-compatible command aliases, hidden from help.
    if len(sys.argv) > 1 and sys.argv[1] in ('config-shell', 'config-shellmcp'):
        legacy = sys.argv[1]
        sys.argv[1:2] = ['config', 'shellmcp']
        # Keep old --shellmcp-url accepted by the ShellMCP config parser.
    ap = argparse.ArgumentParser(prog='gptadmin', description='GPTAdmin manager (hub + shell agents)')
    ap.add_argument('--user', action='store_true', help='Use per-user install paths/services (default when not root)')
    ap.add_argument('--system', action='store_true', help='Use system install paths/services (default when root)')
    sub = ap.add_subparsers(dest='cmd')

    sub.add_parser('version', help='Показать версию и информацию о сборке').set_defaults(func=cmd_version)
    sub.add_parser('doctor', help='Проверка здоровья: сервисы, порты, конфиг, токены').set_defaults(func=cmd_doctor)
    ap_setup = sub.add_parser('setup', help='Установка и настройка')
    ap_setup.add_argument('--pkg-all')
    ap_setup.add_argument('--pkg-hub')
    ap_setup.add_argument('--pkg-shellmcp')
    ap_setup.add_argument('--silent', '--yes', dest='silent', action='store_true', help='Non-interactive install; defaults to hub+shellmcp+FRP')
    ap_setup.add_argument('--hub', action='store_true', help='Install hub component in non-interactive mode')
    ap_setup.add_argument('--shellmcp', '--shell', dest='shellmcp', action='store_true', help='Install ShellMCP/rootd component in non-interactive mode')
    ap_setup.add_argument('--no-hub', action='store_true', help='Do not install hub component')
    ap_setup.add_argument('--no-shellmcp', '--no-shell', dest='no_shellmcp', action='store_true', help='Do not install ShellMCP/rootd component')
    ap_setup.add_argument('--tunnel', choices=['frp', 'manual', 'cloudflare', 'none'], help='Public hub tunnel mode; --silent defaults to frp')
    ap_setup.add_argument('--hub-url', help='Existing public hub URL for manual tunnel or shell-only install')
    ap_setup.add_argument('--mcp-relay-token', help='MCP_RELAY_AGENT_TOKEN of an existing Hub for shell-only installs')
    ap_setup.add_argument('--hub-port', help='Local hub port; default 9001')
    ap_setup.add_argument('--shell-transport', choices=['polling', 'webhook', 'websocket'], default='polling', help='Internal hub↔ShellMCP transport; default polling')
    ap_setup.add_argument('--shell-heartbeat', action='store_true', help='Enable optional ShellMCP heartbeat (disabled by default)')
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
    ap_update.add_argument('--user', action='store_true', help='Use per-user install paths/services')
    ap_update.add_argument('--system', action='store_true', help='Use system install paths/services')
    ap_update.add_argument('--auto', action='store_true', help='Run from the automatic updater; obey GPTADMIN_AUTO_UPDATE')
    ap_update.set_defaults(func=cmd_update)

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

    ap_tok = sub.add_parser('tokens', help='Показать все токены GPTAdmin')
    ap_tok.add_argument('--show-shellmcp', action='store_true', help='Показать SHELLMCP_TOKEN (опасно!)')
    ap_tok.set_defaults(func=cmd_tokens)

    ap_mcp_token_top = sub.add_parser('issue-token', aliases=['token'], help='Выпустить JWT для MCP-клиента без OAuth')
    ap_mcp_token_top.add_argument('name', nargs='?', help='client_id / имя токена, например codex-work')
    ap_mcp_token_top.add_argument('--ttl-days', type=int, default=365)
    ap_mcp_token_top.add_argument('--env-key', help='Имя переменной для сохранения в gptadmin.env')
    ap_mcp_token_top.add_argument('--no-save', action='store_true', help='Только напечатать token, не сохранять в gptadmin.env')
    ap_mcp_token_top.add_argument('--readonly', action='store_true', help='Только просмотр без shell-команд; найденные секреты скрываются')
    ap_mcp_token_top.set_defaults(func=cmd_mcp_token)

    ap_mcp_connect_top = sub.add_parser('connect-mcp', aliases=['mcp-connect'], help='Подключить GPTAdmin как MCP в локальных AI-клиентах')
    ap_mcp_connect_top.add_argument('--client', action='append', choices=['codex', 'claude', 'claude-code', 'opencode', 'vscode'], help='Кого настроить; можно повторять. По умолчанию все найденные')
    ap_mcp_connect_top.add_argument('--fresh', action='store_true', help='Выпустить новые токены для AI MCP clients')
    ap_mcp_connect_top.set_defaults(func=cmd_mcp_connect)

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
    ap_mcp_token.add_argument('--ttl-days', type=int, default=365)
    ap_mcp_token.add_argument('--env-key')
    ap_mcp_token.add_argument('--no-save', action='store_true')
    ap_mcp_token.add_argument('--readonly', action='store_true', help='Только просмотр без shell-команд; найденные секреты скрываются')
    ap_mcp_token.set_defaults(func=cmd_mcp_token)

    ap_mcp_connect = mcp_sub.add_parser('connect', aliases=['self-install', 'install-self'], help='Подключить GPTAdmin как MCP в локальных AI-клиентах')
    ap_mcp_connect.add_argument('--client', action='append', choices=['codex', 'claude', 'claude-code', 'opencode', 'vscode'])
    ap_mcp_connect.add_argument('--fresh', action='store_true')
    ap_mcp_connect.set_defaults(func=cmd_mcp_connect)

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
    # Best-effort update hint (silent on any error, auto-update off, new version available).
    try:
        maybe_update_hint(args)
    except Exception:
        pass
    args.func(args)

if __name__ == '__main__':
    main()
