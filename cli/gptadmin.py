#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import argparse
import os
import sys
import tarfile
import tempfile
import subprocess
import shutil
import socket
import re
import secrets
from pathlib import Path

# ===== Platform =====
IS_MACOS = sys.platform == 'darwin'

# ===== Paths & constants =====
INSTALL_DIR = Path('/opt/gptadmin')
BIN_DIR = INSTALL_DIR / 'bin'
ETC_DIR = Path('/etc/gptadmin')
ETC_DIR.mkdir(parents=True, exist_ok=True)
ENV_FILE = ETC_DIR / 'gptadmin.env'
CLI_PATH = Path('/usr/local/bin/gptadmin')  # для uninstall

if IS_MACOS:
    SERVICES_DIR = Path('/Library/LaunchDaemons')
    LOG_DIR = Path('/var/log/gptadmin')
    SVC_HUB_LABEL   = 'com.gptadmin.hub'
    SVC_ROOTD_LABEL = 'com.gptadmin.rootd'
    SVC_FRPC_LABEL  = 'com.gptadmin.frpc'
    UNIT_PATH_HUB   = SERVICES_DIR / f'{SVC_HUB_LABEL}.plist'
    UNIT_PATH_ROOTD = SERVICES_DIR / f'{SVC_ROOTD_LABEL}.plist'
    UNIT_PATH_FRPC  = SERVICES_DIR / f'{SVC_FRPC_LABEL}.plist'
    FRPC_CONF = ETC_DIR / 'frpc.toml'
else:
    SYSTEMD_DIR = Path('/etc/systemd/system')
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
    if os.geteuid() != 0:
        die('run as root (sudo)')

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
    run(['curl', '-fsSL', url, '-o', str(dest)])

def extract_tgz(tgz_path: Path, target_dir: Path):
    with tarfile.open(tgz_path, 'r:gz') as tar:
        tar.extractall(path=target_dir)

# package install

def install_component_from_pkg(pkg_tgz: Path, component: str):
    if IS_MACOS and component == 'rootd':
        # Linux PyInstaller rootd cannot run on macOS. Install the cross-platform
        # Python fallback instead; _wrapper_script will run it with a Python that
        # has cryptography available.
        BIN_DIR.mkdir(parents=True, exist_ok=True)
        download(ROOTD_PURE_URL_DEFAULT, BIN_DIR / 'rootd')
        os.chmod(BIN_DIR / 'rootd', 0o755)
        return

    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        extract_tgz(pkg_tgz, tdp)
        if component == 'hub':
            candidates = [tdp / 'hub_proxy' / 'dist' / 'hub_proxy', tdp / 'build' / 'hub_proxy' / 'dist' / 'hub_proxy']
        else:
            candidates = [tdp / 'rootd' / 'dist' / 'rootd', tdp / 'build' / 'rootd' / 'dist' / 'rootd']
        for c in candidates:
            if c.exists():
                BIN_DIR.mkdir(parents=True, exist_ok=True)
                shutil.copy2(c, BIN_DIR / c.name)
                os.chmod(BIN_DIR / c.name, 0o755)
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

    def svc_disable_stop(_label: str, unit_path: Path):
        run(['launchctl', 'unload', '-w', str(unit_path)], check=False)

    def svc_status_multi(labels_and_paths):
        for label, path in labels_and_paths:
            if path.exists():
                run(['launchctl', 'list', label], check=False)

    def svc_start_multi(labels_and_paths):
        for _label, path in labels_and_paths:
            if path.exists():
                run(['launchctl', 'load', '-w', str(path)], check=False)

    def svc_stop_multi(labels_and_paths):
        for _label, path in reversed(labels_and_paths):
            if path.exists():
                run(['launchctl', 'unload', str(path)], check=False)

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
    # Linux systemd

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
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
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
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
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
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
"""

    def svc_daemon_reload():
        run(['systemctl', 'daemon-reload'])

    def svc_enable_start(name: str, _unit_path: Path):
        run(['systemctl', 'enable', name])
        run(['systemctl', 'restart', name])

    def svc_restart(name: str, _unit_path: Path):
        run(['systemctl', 'restart', name])

    def svc_disable_stop(name: str, _unit_path: Path):
        run(['systemctl', 'disable', '--now', name], check=False)

    def svc_status_multi(names_and_paths):
        names = [n for n, p in names_and_paths if p.exists()]
        if names:
            run(['systemctl', '--no-pager', 'status', *names], check=False)

    def svc_start_multi(names_and_paths):
        names = [n for n, p in names_and_paths if p.exists()]
        if names:
            run(['systemctl', 'start', *names])

    def svc_stop_multi(names_and_paths):
        names = [n for n, p in reversed(names_and_paths) if p.exists()]
        if names:
            run(['systemctl', 'stop', *names])

    def svc_logs_one(name: str, _log_file=None):
        run(['journalctl', '-u', name, '-e', '-n', '200', '-f'], check=False)

    def svc_logs_all(names_paths_logs):
        names = [n for n, p, _ in names_paths_logs if p.exists()]
        if names:
            run(['journalctl', *sum([['-u', u] for u in names], []), '-e', '-n', '200', '-f'], check=False)
        else:
            print('Журналы пусты: сервисы не установлены.')

    def write_hub_unit(install_hub: bool, _install_rootd: bool):
        if install_hub:
            UNIT_PATH_HUB.write_text(UNIT_HUB)

    def write_rootd_unit(_install_hub: bool, install_rootd: bool):
        if install_rootd:
            UNIT_PATH_ROOTD.write_text(UNIT_ROOTD)

    def write_frpc_unit(frpc_bin: str):
        UNIT_PATH_FRPC.write_text(FRPC_UNIT_TPL.format(frpc_bin=frpc_bin, frpc_conf=FRPC_CONF))

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
    print('\nКак Shell MCP будет подключаться к хабу?')
    print('  1) long-polling / polling — рекомендуется, работает за NAT/firewall')
    print('  2) webhook — только если хаб может напрямую достучаться до Shell MCP')
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
    print('Что устанавливать?')
    print('  1) hub_proxy и Shell MCP agent')
    print('  2) только hub_proxy')
    print('  3) только Shell MCP agent')
    ch = ask('Ваш выбор', '1')
    install_hub = ch in ('1', '2')
    install_rootd = ch in ('1', '3')

    env = env_read()

    env.setdefault('CTL_TOKEN', gen_hex())
    env.setdefault('ROOTD_TOKEN', gen_hex())
    rootd_default_uid = os.environ.get('ROOTD_DEFAULT_UID')
    if rootd_default_uid and rootd_default_uid.isdigit() and rootd_default_uid != '0':
        env.setdefault('ROOTD_DEFAULT_UID', rootd_default_uid)

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
        print('\nУстановка только Shell MCP agent.')
        url = ask('Введите HUB_URL (публичный HTTPS адрес вашего хаба, например, https://gptadmin.example.com)')
        ensure_https(url)
        env['FRP_ENABLE'] = 'false'
        env['HUB_URL'] = url

    configure_rootd_transport(env, install_hub, install_rootd)

    env['INSTALL_HUB'] = 'true' if install_hub else 'false'
    env['INSTALL_ROOTD'] = 'true' if install_rootd else 'false'
    env_set_many(env)

    BIN_DIR.mkdir(parents=True, exist_ok=True)
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
            print('\n[Загрузка] Shell MCP agent...')
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
    if env.get('FRP_ENABLE', 'false') == 'true':
        svc_enable_start(svc_frpc_name(), UNIT_PATH_FRPC)

    env = env_read()
    print('\n=== Готово ===')
    if install_hub:
        print(f"Hub URL: {env.get('HUB_PUBLIC_URL', '—')}")
        print(f"API-Ключ (Bearer): {env['CTL_TOKEN']}")
    if install_rootd and not install_hub:
        print(f"HUB_URL для Shell MCP: {env.get('HUB_URL', '—')}")
    if install_rootd:
        print('Shell MCP agent установлен.')

    installed = [n for n, p in [
        ('gptadmin-hub' if not IS_MACOS else SVC_HUB_LABEL, UNIT_PATH_HUB),
        ('gptadmin-rootd' if not IS_MACOS else SVC_ROOTD_LABEL, UNIT_PATH_ROOTD),
        ('gptadmin-frpc' if not IS_MACOS else SVC_FRPC_LABEL,
         UNIT_PATH_FRPC if env.get('FRP_ENABLE', 'false') == 'true' else None)
    ] if p and Path(p).exists()]
    print("Сервисы: " + ", ".join(installed))

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

def installed_units():
    res = []
    if UNIT_PATH_HUB.exists():   res.append((svc_hub_name(),   UNIT_PATH_HUB))
    if UNIT_PATH_ROOTD.exists(): res.append((svc_rootd_name(), UNIT_PATH_ROOTD))
    if UNIT_PATH_FRPC.exists():  res.append((svc_frpc_name(),  UNIT_PATH_FRPC))
    return res

def cmd_config_shell(args):
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
    print('Shell MCP transport configured:')
    print(f"  ROOTD_TRANSPORT={env.get('ROOTD_TRANSPORT', 'polling')}")
    print(f"  HUB_URL={env.get('HUB_URL', '')}")
    if env.get('QUEUE_URL'):
        print(f"  QUEUE_URL={env['QUEUE_URL']}")
    if env.get('ROOTD_URL'):
        print(f"  ROOTD_URL={env['ROOTD_URL']}")


cmd_config_rootd = cmd_config_shell  # legacy internal alias

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
    if svc in ('shell', 'shell-mcp'):
        svc = 'rootd'
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
    print('Shell MCP token is stored as ROOTD_TOKEN and is intentionally not printed.')

def cmd_rotate(args):
    need_root()
    which = args.which
    if which in ('shell', 'shell-mcp'):
        which = 'rootd'
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
        print('Shell MCP token rotated (значение не выводится).')

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
    for name, path in [(svc_frpc_name(), UNIT_PATH_FRPC),
                       (svc_rootd_name(), UNIT_PATH_ROOTD),
                       (svc_hub_name(), UNIT_PATH_HUB)]:
        svc_disable_stop(name, path)
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

    print('GPTAdmin полностью удалён: службы, конфиги и бинарники.')
    if not removed_cli and CLI_PATH.exists():
        print(f'Чтобы удалить CLI, выполните: rm -f {CLI_PATH}')

# ===== Main =====

def main():
    ap = argparse.ArgumentParser(prog='gptadmin', description='GPTAdmin manager (FRP auto or reverse-proxy)')
    sub = ap.add_subparsers(dest='cmd')

    ap_setup = sub.add_parser('setup', help='Interactive installation & config')
    ap_setup.add_argument('--pkg-all')
    ap_setup.add_argument('--pkg-hub')
    ap_setup.add_argument('--pkg-rootd')
    ap_setup.set_defaults(func=setup_interactive)

    ap_conf = sub.add_parser('config-shell', help='Настроить транспорт Shell MCP: polling/webhook/websocket')
    ap_conf.add_argument('--transport', choices=['polling', 'webhook', 'websocket'])
    ap_conf.add_argument('--hub-url')
    ap_conf.add_argument('--shell-url', '--rootd-url', dest='rootd_url', help='URL Shell MCP agent для webhook режима')
    ap_conf.set_defaults(func=cmd_config_shell)

    ap_conf_legacy = sub.add_parser('config-rootd', help=argparse.SUPPRESS)
    ap_conf_legacy.add_argument('--transport', choices=['polling', 'webhook', 'websocket'])
    ap_conf_legacy.add_argument('--hub-url')
    ap_conf_legacy.add_argument('--rootd-url', dest='rootd_url')
    ap_conf_legacy.set_defaults(func=cmd_config_shell)

    sub.add_parser('status').set_defaults(func=cmd_status)
    sub.add_parser('start').set_defaults(func=cmd_start)
    sub.add_parser('stop').set_defaults(func=cmd_stop)
    sub.add_parser('restart').set_defaults(func=cmd_restart)
    sub.add_parser('enable').set_defaults(func=cmd_enable)
    sub.add_parser('disable').set_defaults(func=cmd_disable)

    ap_logs = sub.add_parser('logs', help='Журналы сервисов (по умолчанию — все; shell = Shell MCP)')
    ap_logs.add_argument('service', nargs='?', default='all', choices=['hub', 'shell', 'shell-mcp', 'rootd', 'frpc', 'all'])
    ap_logs.set_defaults(func=cmd_logs)

    sub.add_parser('tokens').set_defaults(func=cmd_tokens)

    ap_rot = sub.add_parser('rotate', help='Переиздать токен hub или Shell MCP')
    ap_rot.add_argument('which', choices=['hub', 'shell', 'shell-mcp', 'rootd'])
    ap_rot.set_defaults(func=cmd_rotate)

    ap_port = sub.add_parser('port', help='Сменить локальный порт хаба')
    ap_port.add_argument('port', type=int)
    ap_port.set_defaults(func=cmd_port)

    ap_url = sub.add_parser('set-url', help='Задать публичный HTTPS URL и отключить FRP')
    ap_url.add_argument('url')
    ap_url.set_defaults(func=cmd_seturl)

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
    args.func(args)

if __name__ == '__main__':
    main()
