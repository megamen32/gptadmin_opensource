#!/usr/bin/env python3
# -*- coding: utf-8 -*-
from __future__ import annotations

import argparse
import os
import sys
import tarfile
import tempfile
import json
import subprocess
import shutil
import socket
import re
import secrets
import pwd
from pathlib import Path
try:
    import tomllib
except Exception:
    tomllib = None

# ===== Platform =====
IS_MACOS = sys.platform == 'darwin'

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
MCP_CONFIG_FILE = ETC_DIR / 'mcp.json'
MCP_AGENTS_DIR = ETC_DIR / 'mcp-agents.d'
MCP_TOKEN_FILE = ETC_DIR / 'mcp-relay.token'
MCP_RUNTIME_DIR = INSTALL_DIR / 'agents' / 'generic_stdio_mcp_relay'
MCP_MANAGER = MCP_RUNTIME_DIR / 'mcp_agent_manager.py'
MCP_RELAY = MCP_RUNTIME_DIR / 'generic_stdio_mcp_relay.py'
HUB_SOURCE_DIR = INSTALL_DIR / 'hub_source'
HUB_VENV_DIR = INSTALL_DIR / 'hub_venv'

if IS_MACOS:
    SERVICES_DIR = USER_HOME / 'Library' / 'LaunchAgents' if IS_USER_INSTALL else Path('/Library/LaunchDaemons')
    LOG_DIR = USER_HOME / 'Library' / 'Logs' / 'gptadmin' if IS_USER_INSTALL else Path('/var/log/gptadmin')
    SVC_HUB_LABEL   = 'com.gptadmin.hub'
    SVC_ROOTD_LABEL = 'com.gptadmin.rootd'
    SVC_FRPC_LABEL  = 'com.gptadmin.frpc'
    UNIT_PATH_HUB   = SERVICES_DIR / f'{SVC_HUB_LABEL}.plist'
    UNIT_PATH_ROOTD = SERVICES_DIR / f'{SVC_ROOTD_LABEL}.plist'
    UNIT_PATH_FRPC  = SERVICES_DIR / f'{SVC_FRPC_LABEL}.plist'
    FRPC_CONF = ETC_DIR / 'frpc.toml'
else:
    SYSTEMD_DIR = USER_HOME / '.config' / 'systemd' / 'user' if IS_USER_INSTALL else Path('/etc/systemd/system')
    SYSTEMD_HUB   = 'gptadmin-hub.service'
    SYSTEMD_ROOTD = 'gptadmin-rootd.service'
    SYSTEMD_FRPC  = 'gptadmin-frpc.service'
    UNIT_PATH_HUB   = SYSTEMD_DIR / SYSTEMD_HUB
    UNIT_PATH_ROOTD = SYSTEMD_DIR / SYSTEMD_ROOTD
    UNIT_PATH_FRPC  = SYSTEMD_DIR / SYSTEMD_FRPC
    FRPC_CONF = ETC_DIR / 'frpc.toml'

# Package URLs can be overridden by env or args
PKG_ALL_URL_DEFAULT   = os.environ.get('PKG_ALL_URL',   'https://became.bezrabotnyi.com/gptadmin.tar.gz')
PKG_HUB_URL_DEFAULT   = os.environ.get('PKG_HUB_URL',   'https://became.bezrabotnyi.com/gptadmin-hub.tar.gz')
PKG_ROOTD_URL_DEFAULT = os.environ.get('PKG_ROOTD_URL', 'https://became.bezrabotnyi.com/gptadmin-rootd.tar.gz')
ROOTD_PURE_URL_DEFAULT = os.environ.get('ROOTD_PURE_URL', 'https://became.bezrabotnyi.com/rootd_pure.py')

REQUIRED_CMDS = ['curl', 'launchctl' if IS_MACOS else 'systemctl']

# ===== FRPC defaults =====
FRPC_VERSION          = os.environ.get('FRPC_VERSION', '0.64.0')
FRPC_SERVER_ADDR_DEFAULT = 't.gptadmin.bezrabotnyi.com'
FRPC_SERVER_PORT_DEFAULT = '7000'
FRPC_TOKEN_DEFAULT    = 'E10WCLE7ZFT+0NDgOFWwyPV8fb7hG7cLn320aHL0fVk='
FRPC_DOMAIN_DEFAULT   = FRPC_SERVER_ADDR_DEFAULT

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

def run(cmd, check=True, capture=False):
    if capture:
        return subprocess.run(cmd, check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return subprocess.run(cmd, check=check)

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
    ENV_FILE.write_text('\n'.join(lines) + '\n')
    os.chmod(ENV_FILE, 0o640)

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

def _install_macos_hub_from_pkg(tdp: Path):
    src_candidates = [tdp / 'hub_source', tdp / 'hub', tdp / 'services' / 'main_package']
    src = next((x for x in src_candidates if (x / 'hub_proxy.py').exists()), None)
    if src is None:
        die('macOS hub source not found in package: expected hub_source/hub_proxy.py')
    if HUB_SOURCE_DIR.exists():
        shutil.rmtree(HUB_SOURCE_DIR, ignore_errors=True)
    HUB_SOURCE_DIR.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(src, HUB_SOURCE_DIR)
    # Install a small dedicated venv for source-mode hub on macOS.
    py = _mac_python() if IS_MACOS else (sys.executable or 'python3')
    if not (HUB_VENV_DIR / 'bin' / 'python').exists():
        run([py, '-m', 'venv', str(HUB_VENV_DIR)])
    vpy = HUB_VENV_DIR / 'bin' / 'python'
    vpip = HUB_VENV_DIR / 'bin' / 'pip'
    req = HUB_SOURCE_DIR / 'requirements-hub.txt'
    if not req.exists():
        req.write_text('fastapi\nuvicorn[standard]\nhttpx\npydantic\ncryptography\npsutil\nrequests\n', encoding='utf-8')
    run([str(vpy), '-m', 'pip', 'install', '--upgrade', 'pip'], check=False)
    run([str(vpip), 'install', '-r', str(req)])
    BIN_DIR.mkdir(parents=True, exist_ok=True)
    wrapper = BIN_DIR / 'hub_proxy'
    wrapper.write_text(
        '#!/bin/sh\n'
        f'cd {HUB_SOURCE_DIR}\n'
        f'exec {vpy} hub_proxy.py\n',
        encoding='utf-8',
    )
    os.chmod(wrapper, 0o755)


def install_component_from_pkg(pkg_tgz: Path, component: str):
    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        extract_tgz(pkg_tgz, tdp)
        cli_src = tdp / 'cli'
        if cli_src.exists():
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
        if component == 'hub' and IS_MACOS:
            _install_macos_hub_from_pkg(tdp)
            return
        if component == 'hub':
            candidates = [tdp / 'hub_proxy' / 'dist' / 'hub_proxy', tdp / 'build' / 'hub_proxy' / 'dist' / 'hub_proxy']
        elif IS_MACOS:
            # Linux PyInstaller rootd cannot run on macOS. Install bundled
            # pure-Python long-poll rootd from this package, while still copying
            # cli/agents runtime needed for MCP relay management.
            candidates = [tdp / 'client' / 'rootd_pure.py']
        else:
            candidates = [tdp / 'rootd' / 'dist' / 'rootd', tdp / 'build' / 'rootd' / 'dist' / 'rootd']
        for c in candidates:
            if c.exists():
                BIN_DIR.mkdir(parents=True, exist_ok=True)
                dst_name = 'rootd' if (component == 'rootd' and IS_MACOS) else c.name
                shutil.copy2(c, BIN_DIR / dst_name)
                os.chmod(BIN_DIR / dst_name, 0o755)
                return
        if component == 'rootd' and IS_MACOS:
            # Backward compatibility with old archives.
            BIN_DIR.mkdir(parents=True, exist_ok=True)
            download(ROOTD_PURE_URL_DEFAULT, BIN_DIR / 'rootd')
            os.chmod(BIN_DIR / 'rootd', 0o755)
            return
        die(f'{component} binary not found in package')

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
        if name == 'rootd':
            exec_line = f'exec {_mac_python()} {bin_path}'
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

    def svc_daemon_reload():
        pass  # launchd has no daemon-reload

    def svc_enable_start(_label: str, unit_path: Path):
        run(['launchctl', 'load', '-w', str(unit_path)], check=False)

    def svc_restart(_label: str, unit_path: Path):
        run(['launchctl', 'unload', str(unit_path)], check=False)
        run(['launchctl', 'load', '-w', str(unit_path)], check=False)

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
            if path.exists():
                run(['launchctl', 'list', label], check=False)

    def svc_start_multi(labels_and_paths):
        for _label, path in labels_and_paths:
            if path.exists():
                run(['launchctl', 'load', '-w', str(path)], check=False)

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

    def write_hub_unit(install_hub: bool, _install_rootd: bool):
        if not install_hub:
            return
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = _wrapper_script('hub', BIN_DIR / 'hub_proxy')
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_HUB.write_text(_make_plist(SVC_HUB_LABEL, wrapper, LOG_DIR / 'hub.log'))

    def write_rootd_unit(_install_hub: bool, install_rootd: bool):
        if not install_rootd:
            return
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = _wrapper_script('rootd', BIN_DIR / 'rootd')
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_ROOTD.write_text(_make_plist(SVC_ROOTD_LABEL, wrapper, LOG_DIR / 'rootd.log'))

    def write_frpc_unit(frpc_bin: str):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_frpc.sh'
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'exec {frpc_bin} -c {FRPC_CONF}\n'
        )
        os.chmod(wrapper, 0o755)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_FRPC.write_text(_make_plist(SVC_FRPC_LABEL, wrapper, LOG_DIR / 'frpc.log'))

    def svc_hub_name():  return SVC_HUB_LABEL
    def svc_rootd_name(): return SVC_ROOTD_LABEL
    def svc_frpc_name():  return SVC_FRPC_LABEL

else:
    # Linux systemd. In user mode this uses systemd --user and ~/.config/systemd/user.
    LINUX_WANTED_BY = 'default.target' if IS_USER_INSTALL else 'multi-user.target'
    LINUX_HARDENING = '' if IS_USER_INSTALL else 'NoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=full\nProtectHome=true\n'

    UNIT_HUB = f"""
[Unit]
Description=GPTAdmin Hub Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
ExecStart={BIN_DIR}/hub_proxy
Restart=always
RestartSec=3
{LINUX_HARDENING}
[Install]
WantedBy={LINUX_WANTED_BY}
"""

    UNIT_ROOTD = f"""
[Unit]
Description=GPTAdmin Shell MCP Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
ExecStart={BIN_DIR}/rootd
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
ExecStart={frpc_bin} -c {frpc_conf}
Restart=always
RestartSec=3
{hardening}
[Install]
WantedBy={wanted_by}
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
        if names:
            run(_systemctl_cmd('--no-pager', 'status', *names), check=False)

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

    def write_hub_unit(install_hub: bool, _install_rootd: bool):
        if install_hub:
            UNIT_PATH_HUB.parent.mkdir(parents=True, exist_ok=True)
            UNIT_PATH_HUB.write_text(UNIT_HUB)

    def write_rootd_unit(_install_hub: bool, install_rootd: bool):
        if install_rootd:
            UNIT_PATH_ROOTD.parent.mkdir(parents=True, exist_ok=True)
            UNIT_PATH_ROOTD.write_text(UNIT_ROOTD)

    def write_frpc_unit(frpc_bin: str):
        UNIT_PATH_FRPC.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_FRPC.write_text(FRPC_UNIT_TPL.format(frpc_bin=frpc_bin, frpc_conf=FRPC_CONF, hardening=LINUX_HARDENING, wanted_by=LINUX_WANTED_BY))

    def svc_hub_name():   return SYSTEMD_HUB
    def svc_rootd_name(): return SYSTEMD_ROOTD
    def svc_frpc_name():  return SYSTEMD_FRPC


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

    arch = detect_arch()
    os_name = 'darwin' if IS_MACOS else 'linux'
    tarname = f"frp_{FRPC_VERSION}_{os_name}_{arch}.tar.gz"
    url = f"https://github.com/fatedier/frp/releases/download/v{FRPC_VERSION}/{tarname}"

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

def write_frpc_conf(env: dict):
    FRPC_CONF.parent.mkdir(parents=True, exist_ok=True)
    local_port = env.get('HUB_PORT', '9001')
    content = f"""serverAddr = "{env['FRP_SERVER_ADDR']}"
serverPort = {env['FRP_SERVER_PORT']}

[auth]
token = "{env['FRP_TOKEN']}"

[transport.tls]
enable = true
serverName = "{env['FRP_DOMAIN']}"

[[proxies]]
name = "gptadmin-web"
type = "http"
localPort = {local_port}
subdomain = "{env['FRP_SUBDOMAIN']}"
"""
    FRPC_CONF.write_text(content)
    os.chmod(FRPC_CONF, 0o640)

# ===== Interactive setup =====

def ask(prompt: str, default: str = '') -> str:
    sfx = f' [{default}]' if default else ''
    val = input(f"{prompt}{sfx}: ").strip()
    return val or default


def configure_rootd_transport(env: dict, install_hub: bool, install_rootd: bool):
    if not install_rootd or not env.get('HUB_URL'):
        return
    print('\nКак TermCP будет подключаться к хабу?')
    print('  1) long-polling / polling — рекомендуется, работает за NAT/firewall')
    print('  2) webhook — только если хаб может напрямую достучаться до TermCP')
    print('  3) websocket — experimental')
    default_transport = env.get('ROOTD_TRANSPORT', 'polling')
    default_choice = {'polling': '1', 'webhook': '2', 'websocket': '3'}.get(default_transport, '1')
    ch = ask('Ваш выбор', default_choice)
    hub = env['HUB_URL'].rstrip('/')
    if ch == '2':
        env['ROOTD_TRANSPORT'] = 'webhook'
        env.pop('QUEUE_URL', None)
        rootd_url_default = env.get('ROOTD_URL') or f"http://{first_ip()}:{env.get('ROOTD_PORT', '25900')}"
        env['ROOTD_URL'] = ask('Введите ROOTD_URL, доступный хабу', rootd_url_default)
    elif ch == '3':
        env['ROOTD_TRANSPORT'] = 'websocket'
        env.pop('QUEUE_URL', None)
        env['WS_URL'] = hub.replace('https://', 'wss://').replace('http://', 'ws://') + '/ws/rootd'
        env['ROOTD_URL'] = ''
    else:
        env['ROOTD_TRANSPORT'] = 'polling'
        env['QUEUE_URL'] = hub + '/queue'
        env['ROOTD_URL'] = ''
        env.setdefault('ROOTD_BIND', '127.0.0.1')


def setup_interactive(args):
    need_root()
    for c in REQUIRED_CMDS:
        if not have(c):
            die(f'required: {c}')

    print('=== GPTAdmin setup ===')
    print(f'Install mode: {INSTALL_SCOPE}  install_dir={INSTALL_DIR}  config_dir={ETC_DIR}')
    print('Что устанавливать?')
    print('  1) hub_proxy и TermCP agent')
    print('  2) только hub_proxy')
    print('  3) только TermCP agent')
    ch = ask('Ваш выбор', '1')
    install_hub = ch in ('1', '2')
    install_rootd = ch in ('1', '3')

    env = env_read()

    env.setdefault('CTL_TOKEN', gen_hex())
    env.setdefault('ROOTD_TOKEN', gen_hex())
    if install_rootd:
        env.setdefault('ROOTD_AUTO_UPDATE', '1')
        env.setdefault('ROOTD_UPDATE_INTERVAL_S', '3600')
        env.setdefault('ROOTD_UPDATE_TOKEN', env.get('CTL_TOKEN', ''))
        env.setdefault('ROOTD_UPDATE_MANIFEST_URL', (env.get('HUB_URL') or env.get('HUB_PUBLIC_URL') or 'https://gptadmin.bezrabotnyi.com').rstrip('/') + '/artifacts/rootd.json')
        env.setdefault('ROOTD_SERVICE_NAME', svc_rootd_name())
        env.setdefault('ROOTD_SERVICE_SCOPE', INSTALL_SCOPE)
    rootd_default_uid = os.environ.get('ROOTD_DEFAULT_UID')
    if rootd_default_uid and rootd_default_uid.isdigit() and rootd_default_uid != '0':
        env.setdefault('ROOTD_DEFAULT_UID', rootd_default_uid)

    env.setdefault('GPTADMIN_HOME', str(INSTALL_DIR))
    env.setdefault('GPTADMIN_CONFIG_DIR', str(ETC_DIR))
    env.setdefault('GPTADMIN_AUDIT_LOG', str(LOG_DIR / 'audit.log'))
    env['HUB_BIND'] = '127.0.0.1'
    env.setdefault('HUB_PORT', '9001')
    env.setdefault('ROOTD_BIND', '127.0.0.1')
    env.setdefault('ROOTD_PORT', '25900')

    if install_hub:
        print('\nДоступ к хабу из Интернета:')
        print('  1) Авто-туннель через наш FRP (без вашего домена). Быстрый старт.')
        print('  2) У меня есть свой домен + HTTPS. Я настрою reverse-proxy (nginx/caddy/traefik)')
        print('     на 127.0.0.1:%s (его можно позже сменить: gptadmin port <port>)' % env['HUB_PORT'])
        mode = ask('Ваш выбор', '1')

        if mode == '1':
            env['FRP_ENABLE'] = 'true'
            env['FRP_SERVER_ADDR'] = FRPC_SERVER_ADDR_DEFAULT
            env['FRP_SERVER_PORT'] = FRPC_SERVER_PORT_DEFAULT
            env['FRP_DOMAIN'] = FRPC_DOMAIN_DEFAULT
            env['FRP_SUBDOMAIN'] = gen_subdomain()
            env['FRP_TOKEN'] = FRPC_TOKEN_DEFAULT
            env['HUB_PUBLIC_URL'] = f"https://{env['FRP_SUBDOMAIN']}.{env['FRP_DOMAIN']}"
            if install_rootd:
                env['HUB_URL'] = env['HUB_PUBLIC_URL']
        else:
            url = ask('Введите публичный HTTPS URL хаба (например, https://gptadmin.example.com)')
            ensure_https(url)
            env['FRP_ENABLE'] = 'false'
            env['HUB_PUBLIC_URL'] = url
            env['HUB_URL'] = url
    else:
        print('\nУстановка только TermCP agent.')
        url = ask('Введите HUB_URL (публичный HTTPS адрес вашего хаба, например, https://gptadmin.example.com)')
        ensure_https(url)
        env['FRP_ENABLE'] = 'false'
        env['HUB_URL'] = url

    configure_rootd_transport(env, install_hub, install_rootd)
    if install_rootd:
        hub_for_update = (env.get('HUB_URL') or env.get('HUB_PUBLIC_URL') or 'https://gptadmin.bezrabotnyi.com').rstrip('/')
        env['ROOTD_UPDATE_MANIFEST_URL'] = hub_for_update + '/artifacts/rootd.json'
        env['ROOTD_UPDATE_TOKEN'] = env.get('ROOTD_UPDATE_TOKEN') or env.get('CTL_TOKEN', '')
        env['ROOTD_SERVICE_NAME'] = svc_rootd_name()
        env['ROOTD_SERVICE_SCOPE'] = INSTALL_SCOPE

    env['INSTALL_HUB'] = 'true' if install_hub else 'false'
    env['INSTALL_ROOTD'] = 'true' if install_rootd else 'false'
    env_set_many(env)

    BIN_DIR.mkdir(parents=True, exist_ok=True)
    CLI_PATH.parent.mkdir(parents=True, exist_ok=True)
    pkg_all   = args.pkg_all   or PKG_ALL_URL_DEFAULT
    pkg_hub   = args.pkg_hub   or PKG_HUB_URL_DEFAULT
    pkg_rootd = args.pkg_rootd or PKG_ROOTD_URL_DEFAULT

    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        if install_hub and install_rootd:
            print('\n[Загрузка] общий пакет...')
            pkg = tdp / 'all.tgz'
            download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'hub')
            install_component_from_pkg(pkg, 'rootd')
        elif install_hub:
            print('\n[Загрузка] hub_proxy...')
            pkg = tdp / 'hub.tgz'
            try:
                download(pkg_hub, pkg)
            except subprocess.CalledProcessError:
                print('  Нет компонентного архива, беру общий...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'hub')
        else:
            print('\n[Загрузка] TermCP agent...')
            pkg = tdp / 'rootd.tgz'
            try:
                download(pkg_rootd, pkg)
            except subprocess.CalledProcessError:
                print('  Нет компонентного архива, беру общий...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'rootd')

    write_hub_unit(install_hub, install_rootd)
    write_rootd_unit(install_hub, install_rootd)

    if env.get('FRP_ENABLE', 'false') == 'true':
        frpc_bin = ensure_frpc_installed()
        write_frpc_conf(env)
        write_frpc_unit(frpc_bin)

    svc_daemon_reload()
    if install_hub:
        svc_enable_start(svc_hub_name(), UNIT_PATH_HUB)
    if install_rootd:
        svc_enable_start(svc_rootd_name(), UNIT_PATH_ROOTD)
        maybe_import_and_install_mcp_from_desktop_clients()
    if env.get('FRP_ENABLE', 'false') == 'true':
        svc_enable_start(svc_frpc_name(), UNIT_PATH_FRPC)

    env = env_read()
    print('\n=== Готово ===')
    if install_hub:
        print(f"Hub URL: {env.get('HUB_PUBLIC_URL', '—')}")
        print(f"API-Ключ (Bearer): {env['CTL_TOKEN']}")
    if install_rootd and not install_hub:
        print(f"HUB_URL для TermCP: {env.get('HUB_URL', '—')}")
    if install_rootd:
        print('TermCP agent установлен.')

    installed = [n for n, p in [
        ('gptadmin-hub' if not IS_MACOS else SVC_HUB_LABEL, UNIT_PATH_HUB),
        ('gptadmin-rootd' if not IS_MACOS else SVC_ROOTD_LABEL, UNIT_PATH_ROOTD),
        ('gptadmin-frpc' if not IS_MACOS else SVC_FRPC_LABEL,
         UNIT_PATH_FRPC if env.get('FRP_ENABLE', 'false') == 'true' else None)
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

4) Заменитие в "servers": "url": на свой Hub URL {env.get('HUB_URL', '—')}

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


def _mcp_slug(name: str) -> str:
    return re.sub(r'[^A-Za-z0-9_.-]+', '-', name.strip()).strip('-._') or 'mcp'


def _mcp_agent_id(name: str, server: dict) -> str:
    if server.get('agent_id'):
        return str(server['agent_id'])
    return f"{socket.gethostname()}-{_mcp_slug(name)}"


def _mcp_ensure_token_file():
    if MCP_TOKEN_FILE.exists():
        return
    env = env_read()
    token = env.get('CTL_TOKEN') or os.environ.get('GPTADMIN_MCP_RELAY_TOKEN') or gen_hex()
    MCP_TOKEN_FILE.parent.mkdir(parents=True, exist_ok=True)
    MCP_TOKEN_FILE.write_text(token.strip() + '\n', encoding='utf-8')
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
    print(f'Added MCP server {args.name}')
    print(f'Config: {MCP_CONFIG_FILE}')
    print(f'Agent config: {agent_config}')

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
    # Regenerate per-agent configs after edit.
    cfg = _mcp_config()
    for name in sorted((cfg.get('mcpServers') or {}).keys()):
        _mcp_write_agent_config(name, cfg)
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
    for name in sorted((cfg.get('mcpServers') or {}).keys()):
        _mcp_write_agent_config(name, cfg)
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

    Best-effort: a failure here must not break the main rootd install.
    """
    if os.environ.get('GPTADMIN_SKIP_MCP_IMPORT', '').strip().lower() in {'1', 'true', 'yes', 'on'}:
        return
    if not _mcp_manager_exists():
        return
    candidates = []
    try:
        p = _mcp_external_path('claude', None, None)
        if p.exists():
            candidates.append(('claude', p))
    except Exception:
        pass
    if not candidates:
        return
    default = os.environ.get('GPTADMIN_MCP_AUTO_IMPORT', '').strip().lower()
    do_it = default in {'1', 'true', 'yes', 'on'}
    if not do_it:
        try:
            do_it = ask('Найдены MCP servers в Claude. Импортировать и запустить через GPTAdmin/rootd?', 'y').lower().startswith('y')
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
    try:
        backend = 'launchd' if IS_MACOS else ('windows-task' if IS_WINDOWS else 'systemd')
        install_args = argparse.Namespace(name=None, backend=backend)
        cmd_mcp_install(install_args)
    except Exception as e:
        print(f'WARN: не удалось установить MCP services: {e}', file=sys.stderr)


def installed_units():
    res = []
    if UNIT_PATH_HUB.exists():   res.append((svc_hub_name(),   UNIT_PATH_HUB))
    if UNIT_PATH_ROOTD.exists(): res.append((svc_rootd_name(), UNIT_PATH_ROOTD))
    if UNIT_PATH_FRPC.exists():  res.append((svc_frpc_name(),  UNIT_PATH_FRPC))
    return res

def cmd_config_termcp(args):
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
        configure_rootd_transport(env, install_hub=False, install_rootd=True)
    else:
        hub = env['HUB_URL'].rstrip('/')
        env['ROOTD_TRANSPORT'] = transport
        if transport == 'polling':
            env['QUEUE_URL'] = hub + '/queue'
            env['ROOTD_URL'] = ''
            env.setdefault('ROOTD_BIND', '127.0.0.1')
        elif transport == 'webhook':
            env.pop('QUEUE_URL', None)
            env['ROOTD_URL'] = args.rootd_url or env.get('ROOTD_URL') or f"http://{first_ip()}:{env.get('ROOTD_PORT', '25900')}"
        elif transport == 'websocket':
            env.pop('QUEUE_URL', None)
            env['WS_URL'] = hub.replace('https://', 'wss://').replace('http://', 'ws://') + '/ws/rootd'
            env['ROOTD_URL'] = ''
    env_set_many(env)
    if UNIT_PATH_ROOTD.exists():
        svc_restart(svc_rootd_name(), UNIT_PATH_ROOTD)
    print('TermCP transport configured:')
    print(f"  ROOTD_TRANSPORT={env.get('ROOTD_TRANSPORT', 'polling')}")
    print(f"  HUB_URL={env.get('HUB_URL', '')}")
    if env.get('QUEUE_URL'):
        print(f"  QUEUE_URL={env['QUEUE_URL']}")
    if env.get('ROOTD_URL'):
        print(f"  ROOTD_URL={env['ROOTD_URL']}")


cmd_config_shell = cmd_config_termcp  # legacy internal alias
cmd_config_rootd = cmd_config_termcp  # legacy internal alias

def cmd_status(_):
    units = installed_units()
    if not units:
        print('Нет установленных сервисов. Запусти: gptadmin setup')
        return
    svc_status_multi(units)

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

def cmd_disable(_):
    need_root()
    for name, path in installed_units():
        svc_disable_stop(name, path)

def _log_file(label: str) -> Path:
    if IS_MACOS:
        return LOG_DIR / f'{label.split(".")[-1]}.log'
    return None  # journalctl handles it on Linux

def cmd_logs(args):
    svc = args.service
    if svc in ('termcp', 'shell', 'shell-mcp'):
        svc = 'rootd'
    if svc not in ('hub', 'rootd', 'frpc', 'all'):
        die('unknown service. Use: hub, termcp, frpc, all')
    if IS_MACOS:
        mapping = {
            'hub':   (SVC_HUB_LABEL,   UNIT_PATH_HUB,   _log_file(SVC_HUB_LABEL)),
            'rootd': (SVC_ROOTD_LABEL, UNIT_PATH_ROOTD, _log_file(SVC_ROOTD_LABEL)),
            'frpc':  (SVC_FRPC_LABEL,  UNIT_PATH_FRPC,  _log_file(SVC_FRPC_LABEL)),
        }
        if svc == 'all':
            svc_logs_all(list(mapping.values()))
        else:
            label, path, log_file = mapping[svc]
            svc_logs_one(label, log_file)
    else:
        name_map = {
            'hub':   SYSTEMD_HUB,
            'rootd': SYSTEMD_ROOTD,
            'frpc':  SYSTEMD_FRPC,
        }
        if svc == 'all':
            units = installed_units()
            svc_logs_all([(n, p, None) for n, p in units])
        else:
            svc_logs_one(name_map[svc])

def cmd_tokens(_):
    env = env_read()
    print(f"CTL_TOKEN={env.get('CTL_TOKEN','')}  # hub")
    print('TermCP token is stored as ROOTD_TOKEN and is intentionally not printed.')

def cmd_rotate(args):
    need_root()
    which = args.which
    if which in ('termcp', 'shell', 'shell-mcp'):
        which = 'rootd'
    if which not in ('hub', 'rootd'):
        die('unknown token target. Use: hub or termcp')
    newtok = gen_hex()
    if which == 'hub':
        env_set_many({'CTL_TOKEN': newtok})
        if UNIT_PATH_HUB.exists():
            svc_restart(svc_hub_name(), UNIT_PATH_HUB)
        print(f'New hub CTL_TOKEN: {newtok}')
    else:
        env_set_many({'ROOTD_TOKEN': newtok})
        if UNIT_PATH_ROOTD.exists():
            svc_restart(svc_rootd_name(), UNIT_PATH_ROOTD)
        print('TermCP token rotated (значение не выводится).')

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
        svc_restart(svc_frpc_name(), UNIT_PATH_FRPC)
    print(f'Локальный порт хаба изменён на {port}.')

def cmd_seturl(args):
    need_root()
    url = args.url
    ensure_https(url)
    env_set_many({'HUB_PUBLIC_URL': url, 'HUB_URL': url, 'FRP_ENABLE': 'false'})
    if UNIT_PATH_ROOTD.exists():
        svc_restart(svc_rootd_name(), UNIT_PATH_ROOTD)
    if UNIT_PATH_FRPC.exists():
        svc_disable_stop(svc_frpc_name(), UNIT_PATH_FRPC)
    print(f'HUB_PUBLIC_URL/HUB_URL = {url}; FRP отключён.')

# FRP subcommands

def cmd_tunnel_status(_):
    if UNIT_PATH_FRPC.exists():
        svc_status_multi([(svc_frpc_name(), UNIT_PATH_FRPC)])
    else:
        print('FRP не сконфигурирован. Запусти: gptadmin setup')

def cmd_tunnel_logs(_):
    if IS_MACOS:
        svc_logs_one(SVC_FRPC_LABEL, _log_file(SVC_FRPC_LABEL))
    else:
        run(['journalctl', '-u', SYSTEMD_FRPC, '-e', '-n', '200', '-f'], check=False)

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
    svc_enable_start(svc_frpc_name(), UNIT_PATH_FRPC)

    env['HUB_PUBLIC_URL'] = f"https://{env['FRP_SUBDOMAIN']}.{env['FRP_DOMAIN']}"
    env['HUB_URL'] = env['HUB_PUBLIC_URL']
    env_set_many(env)

    print('FRP tunnel enabled.')
    print(f"FRP URL: {env['HUB_PUBLIC_URL']}")

def cmd_tunnel_disable(_):
    need_root()
    svc_disable_stop(svc_frpc_name(), UNIT_PATH_FRPC)
    env = env_read(); env['FRP_ENABLE'] = 'false'; env_set_many(env)
    print('FRP tunnel disabled.')

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
    for name, path in [(svc_frpc_name(), UNIT_PATH_FRPC),
                       (svc_rootd_name(), UNIT_PATH_ROOTD),
                       (svc_hub_name(), UNIT_PATH_HUB)]:
        stopped = svc_disable_stop(name, path)
        if stopped is False:
            failures.append(f'не удалось остановить service {name}')
        safe_rm(path)
    svc_daemon_reload()

    local_frpc = BIN_DIR / 'frpc'
    if local_frpc.exists():
        safe_rm(local_frpc)

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

def main():
    # Backward-compatible command aliases, hidden from help.
    if len(sys.argv) > 1 and sys.argv[1] in ('config-shell', 'config-rootd', 'config-termcp'):
        legacy = sys.argv[1]
        sys.argv[1:2] = ['config', 'termcp']
        # Keep old --rootd-url accepted by the TermCP config parser.
    ap = argparse.ArgumentParser(prog='gptadmin', description='GPTAdmin manager (hub + shell agents)')
    ap.add_argument('--user', action='store_true', help='Use per-user install paths/services (default when not root)')
    ap.add_argument('--system', action='store_true', help='Use system install paths/services (default when root)')
    sub = ap.add_subparsers(dest='cmd')

    ap_setup = sub.add_parser('setup', help='Interactive installation & config')
    ap_setup.add_argument('--pkg-all')
    ap_setup.add_argument('--pkg-hub')
    ap_setup.add_argument('--pkg-rootd')
    ap_setup.add_argument('--user', action='store_true', help='Use per-user install paths/services')
    ap_setup.add_argument('--system', action='store_true', help='Use system install paths/services')
    ap_setup.set_defaults(func=setup_interactive)

    ap_config = sub.add_parser('config', help='Настроить компоненты GPTAdmin')
    config_sub = ap_config.add_subparsers(dest='config_target')
    ap_conf = config_sub.add_parser('termcp', help='Настроить TermCP transport: polling/webhook/websocket')
    ap_conf.add_argument('--transport', choices=['polling', 'webhook', 'websocket'])
    ap_conf.add_argument('--hub-url')
    ap_conf.add_argument('--termcp-url', '--shell-url', '--rootd-url', dest='rootd_url', help='URL TermCP agent для webhook режима')
    ap_conf.set_defaults(func=cmd_config_termcp)

    sub.add_parser('status').set_defaults(func=cmd_status)
    sub.add_parser('start').set_defaults(func=cmd_start)
    sub.add_parser('stop').set_defaults(func=cmd_stop)
    sub.add_parser('restart').set_defaults(func=cmd_restart)

    hub = sub.add_parser('hub')
    hub_sub = hub.add_subparsers(dest='hub_cmd')
    hub_sub.add_parser('status').set_defaults(func=cmd_status)
    hub_sub.add_parser('start').set_defaults(func=cmd_start)
    hub_sub.add_parser('stop').set_defaults(func=cmd_stop)
    hub_sub.add_parser('restart').set_defaults(func=cmd_restart)

    for alias in ('shell','termcp','rootd'):
        rp = sub.add_parser(alias)
        rs = rp.add_subparsers(dest='svc_cmd')
        rs.add_parser('status').set_defaults(func=cmd_status)

    sub.add_parser('enable').set_defaults(func=cmd_enable)
    sub.add_parser('disable').set_defaults(func=cmd_disable)

    ap_logs = sub.add_parser('logs', help='Журналы сервисов (по умолчанию — все; shell = shell agent)')
    ap_logs.add_argument('service', nargs='?', default='all', metavar='service', help='hub | shell | frpc | all')
    ap_logs.set_defaults(func=cmd_logs)

    sub.add_parser('tokens').set_defaults(func=cmd_tokens)

    ap_rot = sub.add_parser('rotate', help='Переиздать токен hub или shell agent')
    ap_rot.add_argument('which', metavar='which', help='hub | shell')
    ap_rot.set_defaults(func=cmd_rotate)

    ap_port = sub.add_parser('port', help='Сменить локальный порт хаба')
    ap_port.add_argument('port', type=int)
    ap_port.set_defaults(func=cmd_port)

    ap_url = sub.add_parser('set-url', help='Задать публичный HTTPS URL и отключить FRP')
    ap_url.add_argument('url')
    ap_url.set_defaults(func=cmd_seturl)

    ap_mcp = sub.add_parser('mcp', help='Управление stdio MCP relay agents')
    mcp_sub = ap_mcp.add_subparsers(dest='mcp_cmd')

    ap_mcp_list = mcp_sub.add_parser('list', help='List configured MCP servers')
    ap_mcp_list.add_argument('--json', action='store_true')
    ap_mcp_list.set_defaults(func=cmd_mcp_list)

    ap_mcp_add = mcp_sub.add_parser('add', help='Add MCP server, Claude/Codex-style')
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
    ap_mcp_add.set_defaults(func=cmd_mcp_add)

    ap_mcp_rm = mcp_sub.add_parser('remove', aliases=['rm'], help='Remove MCP server from config')
    ap_mcp_rm.add_argument('name')
    ap_mcp_rm.add_argument('--keep-service', action='store_true')
    ap_mcp_rm.add_argument('--backend', choices=['systemd', 'launchd', 'windows-task'])
    ap_mcp_rm.set_defaults(func=cmd_mcp_remove)

    ap_mcp_edit = mcp_sub.add_parser('edit', help='Edit /etc/gptadmin/mcp.json')
    ap_mcp_edit.set_defaults(func=cmd_mcp_edit)

    ap_mcp_cat = mcp_sub.add_parser('cat', help='Print GPTAdmin MCP config or generated agent config')
    ap_mcp_cat.add_argument('name', nargs='?')
    ap_mcp_cat.set_defaults(func=cmd_mcp_cat)

    for action_name, func, help_text in [
        ('import', cmd_mcp_import, 'Import MCP servers from Claude/Codex config'),
        ('export', cmd_mcp_export, 'Export MCP servers to Claude/Codex config'),
        ('sync', cmd_mcp_sync, 'Import then export merged Claude/Codex config'),
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
        ('render', cmd_mcp_render, 'Render supervisor config'),
        ('install', cmd_mcp_install, 'Install/start MCP service'),
        ('status', cmd_mcp_status, 'Show MCP service status'),
    ]:
        p = mcp_sub.add_parser(action_name, help=help_text)
        p.add_argument('name', nargs='?')
        p.add_argument('--backend', choices=['systemd', 'launchd', 'windows-task'])
        p.set_defaults(func=func)

    ap_tun = sub.add_parser('tunnel', help='Управление FRP-туннелем')
    tun_sub = ap_tun.add_subparsers(dest='tun_cmd')
    tun_sub.add_parser('status').set_defaults(func=cmd_tunnel_status)
    tun_sub.add_parser('logs').set_defaults(func=cmd_tunnel_logs)
    tun_sub.add_parser('enable').set_defaults(func=cmd_tunnel_enable)
    tun_sub.add_parser('disable').set_defaults(func=cmd_tunnel_disable)

    sub.add_parser('uninstall', help='Полное удаление GPTAdmin и всех сервисов').set_defaults(func=cmd_uninstall)

    args = ap.parse_args()
    if not getattr(args, 'cmd', None):
        ap.print_help(); return
    if args.cmd == 'mcp' and not getattr(args, 'mcp_cmd', None):
        ap_mcp.print_help(); return
    args.func(args)

if __name__ == '__main__':
    main()
