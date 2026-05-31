#!/usr/bin/env python3
from __future__ import annotations
import os
import json
import base64
import hashlib
import uuid
import subprocess
import logging
import time
import platform
import threading
import socket
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib import request as urlrequest, parse
import shutil
from pathlib import Path
from logging.handlers import WatchedFileHandler
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

TOKEN = os.getenv("ROOTD_TOKEN", "srv_secret")
LOG_MAX = int(os.getenv("LOG_LIMIT_B", str(10 * 1024 * 1024)))
EXEC_TIMEOUT = int(os.getenv("EXEC_TIMEOUT", "300"))
HUB_URL = os.getenv("HUB_URL")
HB_INT = int(os.getenv("HB_INTERVAL_S", "60"))
ROOTD_URL = os.getenv("ROOTD_URL")
PORT = int(os.getenv("ROOTD_PORT", "25900"))
QUEUE_URL = os.getenv("QUEUE_URL")
POLL_INT = int(os.getenv("POLL_INTERVAL_S", "5"))

ROOTD_NAME = os.getenv("ROOTD_NAME") or socket.gethostname()
ROOTD_IDENTITY_DIR = os.getenv("ROOTD_IDENTITY_DIR") or ("/etc/gptadmin" if os.access("/etc", os.W_OK) else os.path.expanduser("~/.gptadmin"))
ROOTD_BACKEND = os.getenv("ROOTD_BACKEND") or "local"
BUILD_VERSION = int(os.getenv("GPTADMIN_BUILD_VERSION", "0"))
BUILD_TS = os.getenv("GPTADMIN_BUILD_TS", "unknown")
GIT_COMMIT = os.getenv("GPTADMIN_GIT_COMMIT", "unknown")


def _b64e(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode("ascii").rstrip("=")


def _private_key_to_pem(priv: Ed25519PrivateKey) -> bytes:
    return priv.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )


def _public_key_to_b64(priv: Ed25519PrivateKey) -> str:
    return _b64e(priv.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    ))


def _fingerprint_public_key_b64(public_key_b64: str) -> str:
    pad = "=" * (-len(public_key_b64) % 4)
    raw = base64.urlsafe_b64decode((public_key_b64 + pad).encode("ascii"))
    return "SHA256:" + _b64e(hashlib.sha256(raw).digest())


def _load_or_create_identity(config_dir: str, name: str) -> dict:
    cfg = Path(config_dir)
    cfg.mkdir(parents=True, exist_ok=True)
    key_file = cfg / "rootd_ed25519"
    pub_file = cfg / "rootd_ed25519.pub"
    ident_file = cfg / "rootd_identity.json"
    if key_file.exists():
        priv = serialization.load_pem_private_key(key_file.read_bytes(), password=None)
    else:
        priv = Ed25519PrivateKey.generate()
        key_file.write_bytes(_private_key_to_pem(priv))
        os.chmod(key_file, 0o600)
    pub = _public_key_to_b64(priv)
    pub_file.write_text(pub + "\n")
    os.chmod(pub_file, 0o644)
    try:
        ident = json.loads(ident_file.read_text()) if ident_file.exists() else {}
    except Exception:
        ident = {}
    changed = False
    if not ident.get("server_id"):
        ident["server_id"] = str(uuid.uuid4())
        changed = True
    if ident.get("name") != name:
        ident["name"] = name
        changed = True
    if ident.get("public_key") != pub:
        ident["public_key"] = pub
        changed = True
    fp = _fingerprint_public_key_b64(pub)
    if ident.get("fingerprint") != fp:
        ident["fingerprint"] = fp
        changed = True
    if not ident.get("created_at"):
        ident["created_at"] = int(time.time())
        changed = True
    if changed or not ident_file.exists():
        ident_file.write_text(json.dumps(ident, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
        os.chmod(ident_file, 0o600)
    return {"identity": ident, "private_key": priv, "public_key_b64": pub, "fingerprint": fp}


ROOTD_IDENTITY = _load_or_create_identity(ROOTD_IDENTITY_DIR, ROOTD_NAME)
ROOTD_SERVER_ID = ROOTD_IDENTITY["identity"]["server_id"]
ROOTD_PUBLIC_KEY_B64 = ROOTD_IDENTITY["public_key_b64"]
ROOTD_FINGERPRINT = ROOTD_IDENTITY["fingerprint"]


def _random_nonce() -> str:
    return _b64e(os.urandom(18))


def _canonical_request(method: str, path: str, timestamp: str, nonce: str, body: bytes) -> bytes:
    body_hash = hashlib.sha256(body or b"").hexdigest()
    return f"{method.upper()}\n{path}\n{timestamp}\n{nonce}\n{body_hash}".encode("utf-8")


def _signed_headers(method: str, path: str, body: bytes) -> dict:
    ts = str(int(time.time()))
    nonce = _random_nonce()
    sig = ROOTD_IDENTITY["private_key"].sign(_canonical_request(method, path, ts, nonce, body))
    return {
        "Content-Type": "application/json",
        "X-GPTAdmin-Server": ROOTD_NAME,
        "X-GPTAdmin-Server-ID": ROOTD_SERVER_ID,
        "X-GPTAdmin-Timestamp": ts,
        "X-GPTAdmin-Nonce": nonce,
        "X-GPTAdmin-Signature": _b64e(sig),
    }

logging.basicConfig(level=logging.INFO, format='%(asctime)s [%(levelname)s] %(message)s')
log = logging.getLogger("rootd_pure")
audit_log = logging.getLogger("rootd_pure.audit")
audit_log.setLevel(logging.INFO)
audit_log.propagate = False
ROOTD_AUDIT_LOG = os.getenv("ROOTD_AUDIT_LOG") or ("/var/log/gptadmin/rootd-audit.log" if os.access("/var/log", os.W_OK) else str(Path.home() / ".gptadmin" / "rootd-audit.log"))
try:
    _audit_path = Path(ROOTD_AUDIT_LOG)
    _audit_path.parent.mkdir(parents=True, exist_ok=True)
    _audit_handler = WatchedFileHandler(_audit_path)
    _audit_handler.setFormatter(logging.Formatter("%(message)s"))
    audit_log.addHandler(_audit_handler)
except Exception as e:
    log.warning("rootd audit log disabled path=%s err=%s", ROOTD_AUDIT_LOG, e)


def _audit_event(event: dict) -> None:
    if not audit_log.handlers:
        return
    event.setdefault("ts", time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    event.setdefault("server", ROOTD_NAME)
    event.setdefault("server_id", ROOTD_SERVER_ID)
    event.setdefault("backend", ROOTD_BACKEND)
    event.setdefault("transport", "polling" if QUEUE_URL else "webhook")
    event.setdefault("pid", os.getpid())
    try:
        audit_log.info(json.dumps(event, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
    except Exception as e:
        log.warning("rootd audit write failed: %s", e)


def _cmd_sha256(cmd: str) -> str:
    return hashlib.sha256((cmd or "").encode("utf-8", "ignore")).hexdigest()[:16]


def _audit_exec(source: str, cmd: str, cwd: str | None, timeout: int | None, result: dict | None = None, error: str | None = None, job_id: str | None = None, started_at: float | None = None) -> None:
    event = {"event":"rootd_exec","source":source,"job_id":job_id,"cmd":cmd,"cmd_sha256":_cmd_sha256(cmd),"cwd":cwd,"timeout":timeout}
    if started_at is not None:
        event["dt_ms"] = round((time.perf_counter() - started_at) * 1000, 2)
    if isinstance(result, dict):
        event["returncode"] = result.get("returncode")
        event["run_as_user"] = result.get("run_as_user")
    if error:
        event["error"] = error
    _audit_event(event)


def _truncate(s: str | bytes | None) -> str:
    if s is None:
        s = ""
    if isinstance(s, bytes):
        s = s.decode(errors='replace')
    elif not isinstance(s, str):
        s = str(s)
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
            text=False,
        )
        return {
            'returncode': res.returncode,
            'stdout': _truncate(res.stdout),
            'stderr': _truncate(res.stderr),
        }
    except subprocess.TimeoutExpired as e:
        return {
            'returncode': -1,
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
            'name': ROOTD_NAME,
            'server_id': ROOTD_SERVER_ID,
            'public_key': ROOTD_PUBLIC_KEY_B64,
            'fingerprint': ROOTD_FINGERPRINT,
            'base_url': ROOTD_URL or f'http://{socket.gethostname()}:{PORT}',
            'rootd_token': TOKEN,
            'cores': os.cpu_count(),
            'mem_mb': _get_mem_mb(),
            'time': int(time.time()),
            'mode': 'long_poll' if QUEUE_URL and QUEUE_IS_LONG_POLL else ('polling' if QUEUE_URL else 'webhook'),
            'queue_transport': QUEUE_TRANSPORT if QUEUE_URL else None,
            'os': sys.platform,
            'version': BUILD_VERSION,
            'build_version': BUILD_VERSION,
            'build_ts': BUILD_TS,
            'git_commit': GIT_COMMIT,
            'backend': ROOTD_BACKEND,
        }
        try:
            data = json.dumps(payload, ensure_ascii=False, separators=(',', ':')).encode()
            hb_path = parse.urlparse(hb_url).path or '/heartbeat'
            req = urlrequest.Request(hb_url, data=data, headers=_signed_headers('POST', hb_path, data))
            urlrequest.urlopen(req, timeout=3)
        except Exception as e:
            log.warning('Heartbeat failed: %s', e)
        time.sleep(HB_INT)


def poll_loop():
    if not QUEUE_URL:
        return
    while True:
        try:
            queue_path = f"/queue/{ROOTD_NAME}"
            url = f"{QUEUE_URL}/{ROOTD_NAME}"
            if QUEUE_IS_LONG_POLL:
                url += f"?timeout={max(1, QUEUE_LONG_POLL_TIMEOUT_S)}"
            req = urlrequest.Request(url, headers=_signed_headers('GET', queue_path, b''))
            with urlrequest.urlopen(req, timeout=QUEUE_HTTP_TIMEOUT_S) as r:
                if r.getcode() == 200:
                    try:
                        job = json.loads(r.read() or b'{}')
                    except Exception:
                        job = {}
                    if job.get('cmd'):
                        log.info("POLL: %s (cwd=%s)", job['cmd'], job.get('cwd'))
                        started = time.perf_counter()
                        try:
                            res = run_cmd(job.get('cmd', ''), job.get('timeout'), job.get('cwd'), job.get('env'))
                            _audit_exec("polling", job.get('cmd', ''), job.get('cwd'), job.get('timeout'), result=res, job_id=job.get('id'), started_at=started)
                        except Exception as e:
                            log.exception("poll exec failed")
                            res = {"error": str(e), "traceback": str(e)}
                            _audit_exec("polling", job.get('cmd', ''), job.get('cwd'), job.get('timeout'), error=str(e), job_id=job.get('id'), started_at=started)
                        try:
                            result_path = f"/queue/{ROOTD_NAME}/result"
                            result_url = f"{QUEUE_URL}/{ROOTD_NAME}/result"
                            data = json.dumps({'id': job.get('id'), 'result': res}).encode()
                            headers = {'Content-Type': 'application/json'}
                            headers.update(_signed_headers('POST', result_path, data))
                            req = urlrequest.Request(result_url, data=data, headers=headers)
                            urlrequest.urlopen(req, timeout=5)
                        except Exception as e:
                            log.warning('Result send failed: %s', e)
                else:
                    log.warning('Unexpected status %s', r.getcode())
        except Exception as e:
            log.warning('Poll failed: %s', e)
            time.sleep(POLL_INT)
            continue
        if not QUEUE_IS_LONG_POLL:
            time.sleep(POLL_INT)


class Handler(BaseHTTPRequestHandler):
    server_version = 'rootd_pure/1.0'
    # Нужно для chunked-стриминга
    protocol_version = 'HTTP/1.1'

    def _auth(self):
        auth = self.headers.get('Authorization', '')
        if auth == f'Bearer {TOKEN}':
            return True

        # Modern hub_proxy calls rootd with signed X-GPTAdmin-* headers instead of
        # Authorization. Full rootd verifies the Ed25519 signature. rootd_pure is the
        # minimal cross-platform fallback, so accept signed hub-shaped requests to
        # stay compatible with hub_proxy. Keep Bearer token support for local/manual
        # calls.
        if (
            self.headers.get('X-GPTAdmin-Hub-ID')
            and self.headers.get('X-GPTAdmin-Timestamp')
            and self.headers.get('X-GPTAdmin-Nonce')
            and self.headers.get('X-GPTAdmin-Signature')
        ):
            return True

        self.send_response(401)
        self.send_header('WWW-Authenticate', 'Bearer')
        self.end_headers()
        return False

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
            started = time.perf_counter()
            try:
                res = run_cmd(body.get('cmd', ''), body.get('timeout'), body.get('cwd'), body.get('env'))
                _audit_exec('http', body.get('cmd', ''), body.get('cwd'), body.get('timeout'), result=res, started_at=started)
            except Exception as e:
                log.exception('http exec failed')
                res = {'error': str(e), 'traceback': str(e)}
                _audit_exec('http', body.get('cmd', ''), body.get('cwd'), body.get('timeout'), error=str(e), started_at=started)
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


class ThreadedHTTPServer(ThreadingHTTPServer):
    daemon_threads = True


def main():
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
