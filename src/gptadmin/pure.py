#!/usr/bin/env python3
import os
import json
import subprocess
import logging
import time
import platform
import threading
import socket
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib import request as urlrequest, parse
import shutil

TOKEN = os.getenv("ROOTD_TOKEN", "srv_secret")
LOG_MAX = int(os.getenv("LOG_LIMIT_B", "8192"))
EXEC_TIMEOUT = int(os.getenv("EXEC_TIMEOUT", "300"))
HUB_URL = os.getenv("HUB_URL")
HB_INT = int(os.getenv("HB_INTERVAL_S", "60"))
ROOTD_URL = os.getenv("ROOTD_URL")
PORT = int(os.getenv("ROOTD_PORT", "25900"))
QUEUE_URL = os.getenv("QUEUE_URL")
POLL_INT = int(os.getenv("POLL_INTERVAL_S", "5"))

from logging.handlers import RotatingFileHandler

log = logging.getLogger("rootd_pure")

def setup_logging():
    port = int(os.getenv("ROOTD_PORT", os.getenv("PORT", "25900")))
    log_file = f"rootd-pure-{port}.log"
    logging.basicConfig(
        level=logging.INFO,
        format='%(asctime)s [%(levelname)s] %(message)s',
        handlers=[
            RotatingFileHandler(log_file, maxBytes=10*1024*1024, backupCount=5),  # 10 MB, 5 backups
            logging.StreamHandler()  # лог в stdout (для systemd)
        ]
    )


def _truncate(s: str) -> str:
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s


def _get_mem_mb() -> int | None:
    try:
        if sys.platform == 'darwin':
            res = subprocess.run(['sysctl', '-n', 'hw.memsize'], stdout=subprocess.PIPE, text=True)
            return int(res.stdout.strip()) // (1024 * 1024)
        elif sys.platform.startswith('linux'):
            with open('/proc/meminfo') as f:
                for line in f:
                    if line.startswith('MemTotal:'):
                        return int(line.split()[1]) // 1024
    except Exception:
        pass
    return None


def _get_uptime_s() -> int | None:
    try:
        if sys.platform.startswith('linux'):
            with open('/proc/uptime') as f:
                return int(float(f.readline().split()[0]))
        elif sys.platform == 'darwin':
            res = subprocess.run(['sysctl', '-n', 'kern.boottime'], stdout=subprocess.PIPE, text=True)
            sec = int(res.stdout.split('sec =')[1].split(',')[0].strip())
            return int(time.time() - sec)
    except Exception:
        pass
    return None


def system_info():
    return {
        'host': socket.gethostname(),
        'platform': platform.platform(),
        'cores': os.cpu_count(),
        'mem_mb': _get_mem_mb(),
        'uptime_s': _get_uptime_s(),
    }


def system_health():
    info = system_info()
    try:
        info['load_avg'] = os.getloadavg()
    except Exception:
        info['load_avg'] = []
    du = shutil.disk_usage('/')
    info['disk'] = {
        'total': round(du.total / 2**30, 2),
        'used': round(du.used / 2**30, 2),
        'free': round(du.free / 2**30, 2),
    }
    return info


def run_cmd(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.info("EXEC: %s (cwd=%s)", cmd, cwd)
    env_full = os.environ.copy()
    if env:
        env_full.update(env)
    try:
        if os.name == 'nt':
            cmd_list = ['powershell', '-Command', cmd]
        else:
            shell_path = '/bin/bash' if os.path.exists('/bin/bash') else '/bin/sh'
            cmd_list = [shell_path, '-lc', cmd]
        res = subprocess.run(
            cmd_list,
            cwd=cwd,
            env=env_full,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout or EXEC_TIMEOUT,
            text=True,
        )
        return {
            'returncode': res.returncode,
            'stdout': _truncate(res.stdout),
            'stderr': _truncate(res.stderr),
        }
    except subprocess.TimeoutExpired as e:
        return {
            'error': f'timeout {e.timeout}s',
            'stdout': _truncate(e.stdout or ''),
            'stderr': _truncate(e.stderr or ''),
        }


def heartbeat_loop():
    if not HUB_URL:
        return
    hb_url = HUB_URL + '/heartbeat' if '/heartbeat' not in HUB_URL else HUB_URL
    while True:
        payload = {
            'name': socket.gethostname(),
            'base_url': ROOTD_URL or f'http://{socket.gethostname()}:{PORT}',
            'rootd_token': TOKEN,
            'cores': os.cpu_count(),
            'mem_mb': _get_mem_mb(),
            'time': int(time.time()),
            'mode': 'polling' if QUEUE_URL else 'webhook',
            'os': sys.platform,
        }
        try:
            data = json.dumps(payload).encode()
            req = urlrequest.Request(hb_url, data=data, headers={'Content-Type':'application/json'})
            urlrequest.urlopen(req, timeout=3)
        except Exception as e:
            log.warning('Heartbeat failed: %s', e)
        time.sleep(HB_INT)


def poll_loop():
    if not QUEUE_URL:
        return
    while True:
        try:
            url = f"{QUEUE_URL}/{socket.gethostname()}?token={TOKEN}"
            with urlrequest.urlopen(url, timeout=5) as r:
                if r.getcode() == 200:
                    try:
                        job = json.loads(r.read() or b'{}')
                    except Exception:
                        job = {}
                    if job.get('cmd'):
                        log.info("POLL: %s (cwd=%s)", job['cmd'], job.get('cwd'))
                        res = run_cmd(job.get('cmd', ''), job.get('timeout'), job.get('cwd'), job.get('env'))
                        try:
                            result_url = f"{QUEUE_URL}/{socket.gethostname()}/result?token={TOKEN}"
                            data = json.dumps({'id': job.get('id'), 'result': res}).encode()
                            req = urlrequest.Request(result_url, data=data, headers={'Content-Type': 'application/json'})
                            urlrequest.urlopen(req, timeout=5)
                        except Exception as e:
                            log.warning('Result send failed: %s', e)
                else:
                    log.warning('Unexpected status %s', r.getcode())
        except Exception as e:
            log.warning('Poll failed: %s', e)
        time.sleep(POLL_INT)


class Handler(BaseHTTPRequestHandler):
    server_version = 'rootd_pure/1.0'
    # Нужно для chunked-стриминга
    protocol_version = 'HTTP/1.1'

    def _auth(self):
        auth = self.headers.get('Authorization', '')
        if auth != f'Bearer {TOKEN}':
            self.send_response(401)
            self.send_header('WWW-Authenticate', 'Bearer')
            self.end_headers()
            return False
        return True

    def _json(self, obj, code=200):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if not self._auth():
            return
        parsed = parse.urlparse(self.path)
        if parsed.path == '/system/info':
            self._json(system_info())
        elif parsed.path == '/system/health':
            self._json(system_health())
        elif parsed.path == '/file':
            params = parse.parse_qs(parsed.query)
            path = params.get('path', [None])[0]
            if not path or not os.path.isfile(path):
                self.send_response(404)
                self.end_headers()
                return
            try:
                with open(path, 'rb') as f:
                    data = f.read()
                self.send_response(200)
                self.send_header('Content-Type', 'application/octet-stream')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            except Exception:
                self.send_response(500)
                self.end_headers()
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        if not self._auth():
            return
        length = int(self.headers.get('Content-Length', 0))
        raw = self.rfile.read(length) if length else b''
        try:
            body = json.loads(raw or b'{}')
        except Exception:
            body = {}
        if self.path == '/exec':
            res = run_cmd(body.get('cmd', ''), body.get('timeout'), body.get('cwd'), body.get('env'))
            self._json(res)
        elif self.path == '/exec/stream':
            cmd = body.get('cmd', '')
            if not cmd:
                self._json({'error': 'cmd required'}, 400)
                return
            cwd = body.get('cwd')
            env = os.environ.copy()
            if isinstance(body.get('env'), dict):
                env.update(body['env'])
            if os.name == 'nt':
                cmd_list = ['powershell', '-Command', cmd]
            else:
                shell_path = '/bin/bash' if os.path.exists('/bin/bash') else '/bin/sh'
                cmd_list = [shell_path, '-lc', cmd]
            proc = subprocess.Popen(
                cmd_list,
                cwd=cwd,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
                universal_newlines=True
            )
            self.send_response(200)
            self.send_header('Content-Type', 'text/plain; charset=utf-8')
            self.send_header('Transfer-Encoding', 'chunked')
            self.end_headers()
            try:
                assert proc.stdout is not None
                for line in proc.stdout:
                    data = line.encode()
                    self.wfile.write(f"{len(data):X}\r\n".encode())
                    self.wfile.write(data)
                    self.wfile.write(b"\r\n")
                    self.wfile.flush()
                proc.wait(timeout=5)
            except Exception:
                try:
                    proc.kill()
                except Exception:
                    pass
            finally:
                self.wfile.write(b"0\r\n\r\n")
        else:
            self.send_response(404)
            self.end_headers()


class ThreadedHTTPServer(HTTPServer):
    daemon_threads = True


def main():
    setup_logging()
    setup_logging()
    if HUB_URL:
        threading.Thread(target=heartbeat_loop, daemon=True).start()
    if QUEUE_URL:
        log.info('Polling queue %s', QUEUE_URL)
        try:
            poll_loop()
        except KeyboardInterrupt:
            pass
    else:
        server = ThreadedHTTPServer(('', PORT), Handler)
        log.info('Listening on port %s', PORT)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass


if __name__ == '__main__':
    main()
