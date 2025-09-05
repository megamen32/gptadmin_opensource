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

# ===== Paths & constants =====
INSTALL_DIR = Path('/opt/gptadmin')
BIN_DIR = INSTALL_DIR / 'bin'
ETC_DIR = Path('/etc/gptadmin')
ETC_DIR.mkdir(parents=True, exist_ok=True)
ENV_FILE = ETC_DIR / 'gptadmin.env'
FRPC_CONF = ETC_DIR / 'frpc.toml'
CLI_PATH = Path('/usr/local/bin/gptadmin')  # для uninstall

SYSTEMD_DIR = Path('/etc/systemd/system')
SYSTEMD_HUB = 'gptadmin-hub.service'
SYSTEMD_ROOTD = 'gptadmin-rootd.service'
SYSTEMD_FRPC = 'gptadmin-frpc.service'
UNIT_PATH_HUB = SYSTEMD_DIR / SYSTEMD_HUB
UNIT_PATH_ROOTD = SYSTEMD_DIR / SYSTEMD_ROOTD
UNIT_PATH_FRPC = SYSTEMD_DIR / SYSTEMD_FRPC

# Package URLs can be overridden by env or args
PKG_ALL_URL_DEFAULT = os.environ.get('PKG_ALL_URL', 'https://became.bezrabotnyi.com/gptadmin.tar.gz')
PKG_HUB_URL_DEFAULT = os.environ.get('PKG_HUB_URL', 'https://became.bezrabotnyi.com/gptadmin-hub.tar.gz')
PKG_ROOTD_URL_DEFAULT = os.environ.get('PKG_ROOTD_URL', 'https://became.bezrabotnyi.com/gptadmin-rootd.tar.gz')

REQUIRED_CMDS = ['curl', 'systemctl']

# ===== FRPC defaults =====
FRPC_VERSION = os.environ.get('FRPC_VERSION', '0.64.0')
FRPC_SERVER_ADDR_DEFAULT = 't.gptadmin.bezrabotnyi.com'
FRPC_SERVER_PORT_DEFAULT = '7000'
FRPC_TOKEN_DEFAULT = 'E10WCLE7ZFT+0NDgOFWwyPV8fb7hG7cLn320aHL0fVk='
FRPC_DOMAIN_DEFAULT = FRPC_SERVER_ADDR_DEFAULT

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
    # u-<8 hex>, например u-2c3b2e4f
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

# http(s) URL validator (very light)
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
    """
    component: 'hub' | 'rootd'
    Accepts structures: hub_proxy/dist/hub_proxy or build/hub_proxy/dist/hub_proxy (same for rootd).
    """
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

# systemd units
UNIT_HUB = f"""
[Unit]
Description=GPTAdmin Hub Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
# hub_proxy читает CTL_TOKEN, HUB_BIND, HUB_PORT
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
Description=GPTAdmin rootd Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile={ENV_FILE}
# rootd читает ROOTD_TOKEN; HUB_URL — адрес хаба для heartbeat
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
    """
    Return full path to frpc. If not found, download v{FRPC_VERSION} for current arch to BIN_DIR.
    """
    existing = shutil.which('frpc')
    if existing:
        return existing

    arch = detect_arch()
    tarname = f"frp_{FRPC_VERSION}_linux_{arch}.tar.gz"
    url = f"https://github.com/fatedier/frp/releases/download/v{FRPC_VERSION}/{tarname}"

    BIN_DIR.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as td:
        tdp = Path(td)
        pkg = tdp / tarname
        download(url, pkg)
        extract_tgz(pkg, tdp)
        frpc_src = tdp / f"frp_{FRPC_VERSION}_linux_{arch}" / "frpc"
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

def setup_interactive(args):
    need_root()
    for c in REQUIRED_CMDS:
        if not have(c):
            die(f'required: {c}')

    print('=== GPTAdmin setup ===')
    print('Что устанавливать?')
    print('  1) hub_proxy и rootd')
    print('  2) только hub_proxy')
    print('  3) только rootd')
    ch = ask('Ваш выбор', '1')
    install_hub = ch in ('1', '2')
    install_rootd = ch in ('1', '3')

    env = env_read()

    # tokens (only (re)generate if absent)
    env.setdefault('CTL_TOKEN', gen_hex())
    env.setdefault('ROOTD_TOKEN', gen_hex())

    # defaults
    env['HUB_BIND'] = '127.0.0.1'  # всегда локально
    env.setdefault('HUB_PORT', '9001')
    env.setdefault('ROOTD_BIND', '127.0.0.1')
    env.setdefault('ROOTD_PORT', '25900')

    # ---------- Choose external access mode ----------
    if install_hub:
        print('\nДоступ к хабу из Интернета:')
        print('  1) Авто-туннель через наш FRP (без вашего домена). Быстрый старт.')
        print('  2) У меня есть свой домен + HTTPS. Я настрою reverse-proxy (nginx/caddy/traefik)')
        print('     на 127.0.0.1:%s (его можно позже сменить: gptadmin port <port>)' % env['HUB_PORT'])
        mode = ask('Ваш выбор', '1')

        if mode == '1':
            # Включаем FRP
            env['FRP_ENABLE'] = 'true'
            env['FRP_SERVER_ADDR'] = FRPC_SERVER_ADDR_DEFAULT
            env['FRP_SERVER_PORT'] = FRPC_SERVER_PORT_DEFAULT
            env['FRP_DOMAIN'] = FRPC_DOMAIN_DEFAULT
            env['FRP_SUBDOMAIN'] = gen_subdomain()
            env['FRP_TOKEN'] = FRPC_TOKEN_DEFAULT
            env['HUB_PUBLIC_URL'] = f"https://{env['FRP_SUBDOMAIN']}.{env['FRP_DOMAIN']}"
            # Если ставим rootd на этой же машине — пусть стучится на внешний URL
            if install_rootd:
                env['HUB_URL'] = env['HUB_PUBLIC_URL']
        else:
            # Ручной режим (reverse-proxy), FRP не ставим вовсе
            url = ask('Введите публичный HTTPS URL хаба (например, https://gptadmin.example.com)')
            ensure_https(url)
            env['FRP_ENABLE'] = 'false'
            env['HUB_PUBLIC_URL'] = url
            env['HUB_URL'] = url  # чтобы rootd (если есть) знал, куда коннектиться
    else:
        # Ставим только rootd — спросим HUB_URL
        print('\nУстановка только rootd.')
        url = ask('Введите HUB_URL (публичный HTTPS адрес вашего хаба, например, https://gptadmin.example.com)')
        ensure_https(url)
        env['FRP_ENABLE'] = 'false'
        env['HUB_URL'] = url

    # persist config (and remember components)
    env['INSTALL_HUB'] = 'true' if install_hub else 'false'
    env['INSTALL_ROOTD'] = 'true' if install_rootd else 'false'
    env_set_many(env)

    # download and install components
    BIN_DIR.mkdir(parents=True, exist_ok=True)
    pkg_all = args.pkg_all or PKG_ALL_URL_DEFAULT
    pkg_hub = args.pkg_hub or PKG_HUB_URL_DEFAULT
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
            print('\n[Загрузка] rootd...')
            pkg = tdp / 'rootd.tgz'
            try:
                download(pkg_rootd, pkg)
            except subprocess.CalledProcessError:
                print('  Нет компонентного архива, беру общий...')
                download(pkg_all, pkg)
            install_component_from_pkg(pkg, 'rootd')

    # write units
    if install_hub:
        UNIT_PATH_HUB.write_text(UNIT_HUB)
    if install_rootd:
        UNIT_PATH_ROOTD.write_text(UNIT_ROOTD)

    # ----- FRP install & unit (only if enabled) -----
    if env.get('FRP_ENABLE', 'false') == 'true':
        frpc_bin = ensure_frpc_installed()
        write_frpc_conf(env)
        UNIT_PATH_FRPC.write_text(FRPC_UNIT_TPL.format(frpc_bin=frpc_bin, frpc_conf=FRPC_CONF))

    # enable + restart
    run(['systemctl', 'daemon-reload'])
    if install_hub:
        run(['systemctl', 'enable', SYSTEMD_HUB])
        run(['systemctl', 'restart', SYSTEMD_HUB])
    if install_rootd:
        run(['systemctl', 'enable', SYSTEMD_ROOTD])
        run(['systemctl', 'restart', SYSTEMD_ROOTD])
    if env.get('FRP_ENABLE', 'false') == 'true':
        run(['systemctl', 'enable', SYSTEMD_FRPC])
        run(['systemctl', 'restart', SYSTEMD_FRPC])

    # summary (only CTL_TOKEN is shown)
    env = env_read()
    print('\n=== Готово ===')
    if install_hub:
        print(f"Hub URL: {env.get('HUB_PUBLIC_URL', '—')}")
    if install_rootd:
        print(f"HUB_URL для rootd: {env.get('HUB_URL', '—')}")
    print(f"CTL_TOKEN: {env['CTL_TOKEN']}")
    if install_rootd:
        print('rootd установлен (токен не отображается).')
    if env.get('FRP_ENABLE', 'false') == 'true':
        print(f"FRP subdomain: {env['FRP_SUBDOMAIN']}")
        print("Сервис туннеля: gptadmin-frpc")
    print("Сервисы: " + ", ".join([n for n, p in [
        ('gptadmin-hub', UNIT_PATH_HUB),
        ('gptadmin-rootd', UNIT_PATH_ROOTD),
        ('gptadmin-frpc', UNIT_PATH_FRPC if env.get('FRP_ENABLE','false')=='true' else None)
    ] if p and p.exists()]))

# ===== Commands =====

def installed_units():
    res = []
    if UNIT_PATH_HUB.exists(): res.append(SYSTEMD_HUB)
    if UNIT_PATH_ROOTD.exists(): res.append(SYSTEMD_ROOTD)
    if UNIT_PATH_FRPC.exists(): res.append(SYSTEMD_FRPC)
    return res

def cmd_status(_):
    units = installed_units()
    if not units:
        print('Нет установленных сервисов. Запусти: gptadmin setup')
        return
    run(['systemctl','--no-pager','status', *units], check=False)

def cmd_start(_):
    need_root(); units = installed_units()
    if units: run(['systemctl','start', *units])

def cmd_stop(_):
    need_root(); units = installed_units()[::-1]
    if units: run(['systemctl','stop', *units])

def cmd_restart(_):
    need_root(); units = installed_units()
    if units: run(['systemctl','restart', *units])

def cmd_enable(_):
    need_root(); units = installed_units()
    if units: run(['systemctl','enable', *units])

def cmd_disable(_):
    need_root(); units = installed_units()
    if units: run(['systemctl','disable', *units])

def cmd_logs(args):
    svc = args.service
    name = {
        'hub': SYSTEMD_HUB,
        'rootd': SYSTEMD_ROOTD,
        'frpc': SYSTEMD_FRPC,
        'all': None
    }[svc]
    if name:
        run(['journalctl','-u', name, '-e', '-n', '200', '-f'], check=False)
    else:
        units = installed_units()
        if units:
            run(['journalctl', *sum([['-u', u] for u in units], []), '-e', '-n', '200', '-f'], check=False)
        else:
            print('Журналы пусты: сервисы не установлены.')

def cmd_tokens(_):
    env = env_read()
    print(f"CTL_TOKEN={env.get('CTL_TOKEN','')}")

def cmd_rotate(args):
    need_root()
    which = args.which
    newtok = gen_hex()
    if which == 'hub':
        env_set_many({'CTL_TOKEN': newtok})
        if UNIT_PATH_HUB.exists():
            run(['systemctl','restart', SYSTEMD_HUB])
        print(f'New hub CTL_TOKEN: {newtok}')
    else:
        env_set_many({'ROOTD_TOKEN': newtok})
        if UNIT_PATH_ROOTD.exists():
            run(['systemctl','restart', SYSTEMD_ROOTD])
        print('rootd token rotated (значение не выводится).')

def cmd_port(args):
    # меняем локальный порт хаба; FRP перегенерим и перезапустим при необходимости
    need_root()
    port = str(args.port)
    env = env_read()
    env['HUB_PORT'] = port
    env_set_many(env)
    if UNIT_PATH_HUB.exists():
        run(['systemctl','restart', SYSTEMD_HUB])
    if UNIT_PATH_FRPC.exists() and env.get('FRP_ENABLE','false') == 'true':
        write_frpc_conf(env)
        run(['systemctl','restart', SYSTEMD_FRPC])
    print(f'Локальный порт хаба изменён на {port}.')

def cmd_seturl(args):
    # вручную задать публичный URL (если используешь внешний реверс-прокси)
    need_root()
    url = args.url
    ensure_https(url)
    env_set_many({'HUB_PUBLIC_URL': url, 'HUB_URL': url, 'FRP_ENABLE': 'false'})
    if UNIT_PATH_ROOTD.exists():
        run(['systemctl','restart', SYSTEMD_ROOTD], check=False)
    if UNIT_PATH_FRPC.exists():
        run(['systemctl','disable','--now', SYSTEMD_FRPC], check=False)
    print(f'HUB_PUBLIC_URL/HUB_URL = {url}; FRP отключён.')

# FRP subcommands

def cmd_tunnel_status(_):
    if UNIT_PATH_FRPC.exists():
        run(['systemctl','--no-pager','status', SYSTEMD_FRPC], check=False)
    else:
        print('FRP не сконфигурирован. Запусти: gptadmin setup')

def cmd_tunnel_logs(_):
    run(['journalctl','-u', SYSTEMD_FRPC, '-e', '-n', '200', '-f'], check=False)

def cmd_tunnel_enable(args):
    # Делает FRP включённым, подставляя дефолты, если их нет
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
    UNIT_PATH_FRPC.write_text(FRPC_UNIT_TPL.format(frpc_bin=frpc_bin, frpc_conf=FRPC_CONF))
    run(['systemctl','daemon-reload'])
    run(['systemctl','enable', SYSTEMD_FRPC])
    run(['systemctl','restart', SYSTEMD_FRPC])

    env['HUB_PUBLIC_URL'] = f"https://{env['FRP_SUBDOMAIN']}.{env['FRP_DOMAIN']}"
    env['HUB_URL'] = env['HUB_PUBLIC_URL']
    env_set_many(env)

    print('FRP tunnel enabled.')
    print(f"FRP URL: {env['HUB_PUBLIC_URL']}")

def cmd_tunnel_disable(_):
    need_root()
    run(['systemctl','disable','--now', SYSTEMD_FRPC], check=False)
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
    # stop & disable services
    for name in (SYSTEMD_FRPC, SYSTEMD_ROOTD, SYSTEMD_HUB):
        run(['systemctl','disable','--now', name], check=False)
    # remove unit files
    for p in (UNIT_PATH_FRPC, UNIT_PATH_ROOTD, UNIT_PATH_HUB):
        safe_rm(p)
    run(['systemctl','daemon-reload'], check=False)

    # remove our files
    # удаляем наш локальный frpc (не системный в PATH вне /opt/gptadmin/bin)
    local_frpc = BIN_DIR / 'frpc'
    if local_frpc.exists():
        safe_rm(local_frpc)

    safe_rm(INSTALL_DIR)
    safe_rm(ETC_DIR)

    # попробовать удалить сам CLI
    removed_cli = False
    if CLI_PATH.exists():
        try:
            # удаление файла CLI не мешает текущему процессу
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

    # setup (interactive)
    ap_setup = sub.add_parser('setup', help='Interactive installation & config')
    ap_setup.add_argument('--pkg-all')
    ap_setup.add_argument('--pkg-hub')
    ap_setup.add_argument('--pkg-rootd')
    ap_setup.set_defaults(func=setup_interactive)

    # basic ops
    sub.add_parser('status').set_defaults(func=cmd_status)
    sub.add_parser('start').set_defaults(func=cmd_start)
    sub.add_parser('stop').set_defaults(func=cmd_stop)
    sub.add_parser('restart').set_defaults(func=cmd_restart)
    sub.add_parser('enable').set_defaults(func=cmd_enable)
    sub.add_parser('disable').set_defaults(func=cmd_disable)

    ap_logs = sub.add_parser('logs', help='Журналы сервисов (по умолчанию — все)')
    ap_logs.add_argument('service', nargs='?', default='all', choices=['hub','rootd','frpc','all'])
    ap_logs.set_defaults(func=cmd_logs)

    sub.add_parser('tokens').set_defaults(func=cmd_tokens)

    ap_rot = sub.add_parser('rotate', help='Переиздать токен hub или rootd')
    ap_rot.add_argument('which', choices=['hub','rootd'])
    ap_rot.set_defaults(func=cmd_rotate)

    ap_port = sub.add_parser('port', help='Сменить локальный порт хаба (повлияет на FRP localPort)')
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
