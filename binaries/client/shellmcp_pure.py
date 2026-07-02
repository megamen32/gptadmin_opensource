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
try:
    import pwd
    import grp
except Exception:
    pwd = None
    grp = None
import socket
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib import request as urlrequest, parse
import shutil
from pathlib import Path
from logging.handlers import WatchedFileHandler
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

TOKEN = os.getenv("SHELLMCP_TOKEN", "srv_secret")
LOG_MAX = int(os.getenv("LOG_LIMIT_B", str(10 * 1024 * 1024)))
EXEC_TIMEOUT = int(os.getenv("EXEC_TIMEOUT", "300"))
HUB_URL = os.getenv("HUB_URL")
HB_INT = int(os.getenv("HB_INTERVAL_S", "60"))
SHELLMCP_URL = os.getenv("SHELLMCP_URL")
PORT = int(os.getenv("SHELLMCP_PORT", "25900"))
QUEUE_URL = os.getenv("QUEUE_URL")
POLL_INT = int(os.getenv("POLL_INTERVAL_S", "5"))
QUEUE_TRANSPORT = os.getenv("QUEUE_TRANSPORT", "long_poll").strip().lower()
QUEUE_LONG_POLL_TIMEOUT_S = int(os.getenv("QUEUE_LONG_POLL_TIMEOUT_S", "55"))
QUEUE_IS_LONG_POLL = QUEUE_TRANSPORT in {"long_poll", "long-poll", "longpoll"}
QUEUE_HTTP_TIMEOUT_S = int(os.getenv(
    "QUEUE_HTTP_TIMEOUT_S",
    str(QUEUE_LONG_POLL_TIMEOUT_S + 10 if QUEUE_IS_LONG_POLL else 5),
))

def _queue_is_long_poll() -> bool:
    # Defensive guard for partially updated/source-patched installs: older code
    # crashed in heartbeat/poll when QUEUE_IS_LONG_POLL was referenced before it
    # existed. Keep the default safe for queue transports.
    return bool(globals().get("QUEUE_IS_LONG_POLL", bool(QUEUE_URL)))


def _queue_http_timeout_s() -> int:
    return int(globals().get("QUEUE_HTTP_TIMEOUT_S", 65 if _queue_is_long_poll() else 5))


def _queue_long_poll_timeout_s() -> int:
    return int(globals().get("QUEUE_LONG_POLL_TIMEOUT_S", 55))

SHELLMCP_NAME = os.getenv("SHELLMCP_NAME") or socket.gethostname()
SHELLMCP_IDENTITY_DIR = os.getenv("SHELLMCP_IDENTITY_DIR") or ("/etc/gptadmin" if os.access("/etc", os.W_OK) else os.path.expanduser("~/.gptadmin"))
SHELLMCP_BACKEND = os.getenv("SHELLMCP_BACKEND") or "local"
SHELLMCP_DEFAULT_USER = os.getenv("SHELL_DEFAULT_USER") or os.getenv("SHELLMCP_DEFAULT_USER") or ""
SHELLMCP_DEFAULT_HOME = os.getenv("SHELL_DEFAULT_HOME") or os.getenv("SHELLMCP_DEFAULT_HOME") or ""
SHELLMCP_DEFAULT_CWD = os.getenv("SHELL_DEFAULT_CWD") or os.getenv("SHELLMCP_DEFAULT_CWD") or SHELLMCP_DEFAULT_HOME
try:
    from gptadmin_build_info import BUILD_VERSION as _PKG_BUILD_VERSION, BUILD_TS as _PKG_BUILD_TS, GIT_COMMIT as _PKG_GIT_COMMIT
except Exception:
    _PKG_BUILD_VERSION, _PKG_BUILD_TS, _PKG_GIT_COMMIT = 0, "unknown", "unknown"
BUILD_VERSION = int(os.getenv("GPTADMIN_BUILD_VERSION", str(_PKG_BUILD_VERSION or 0)))
BUILD_TS = os.getenv("GPTADMIN_BUILD_TS", _PKG_BUILD_TS or "unknown")
GIT_COMMIT = os.getenv("GPTADMIN_GIT_COMMIT", _PKG_GIT_COMMIT or "unknown")


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
    key_file = cfg / "shellmcp_ed25519"
    pub_file = cfg / "shellmcp_ed25519.pub"
    ident_file = cfg / "shellmcp_identity.json"
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


SHELLMCP_IDENTITY = _load_or_create_identity(SHELLMCP_IDENTITY_DIR, SHELLMCP_NAME)
SHELLMCP_SERVER_ID = SHELLMCP_IDENTITY["identity"]["server_id"]
SHELLMCP_PUBLIC_KEY_B64 = SHELLMCP_IDENTITY["public_key_b64"]
SHELLMCP_FINGERPRINT = SHELLMCP_IDENTITY["fingerprint"]


def _random_nonce() -> str:
    return _b64e(os.urandom(18))


def _canonical_request(method: str, path: str, timestamp: str, nonce: str, body: bytes) -> bytes:
    body_hash = hashlib.sha256(body or b"").hexdigest()
    return f"{method.upper()}\n{path}\n{timestamp}\n{nonce}\n{body_hash}".encode("utf-8")


def _signed_headers(method: str, path: str, body: bytes) -> dict:
    ts = str(int(time.time()))
    nonce = _random_nonce()
    sig = SHELLMCP_IDENTITY["private_key"].sign(_canonical_request(method, path, ts, nonce, body))
    return {
        "Content-Type": "application/json",
        "X-GPTAdmin-Server": SHELLMCP_NAME,
        "X-GPTAdmin-Server-ID": SHELLMCP_SERVER_ID,
        "X-GPTAdmin-Timestamp": ts,
        "X-GPTAdmin-Nonce": nonce,
        "X-GPTAdmin-Signature": _b64e(sig),
    }

logging.basicConfig(level=logging.INFO, format='%(asctime)s [%(levelname)s] %(message)s')
log = logging.getLogger("shellmcp_pure")
audit_log = logging.getLogger("shellmcp_pure.audit")
audit_log.setLevel(logging.INFO)
audit_log.propagate = False
SHELLMCP_AUDIT_LOG = os.getenv("SHELLMCP_AUDIT_LOG") or ("/var/log/gptadmin/shellmcp-audit.log" if os.access("/var/log", os.W_OK) else str(Path.home() / ".gptadmin" / "shellmcp-audit.log"))
try:
    _audit_path = Path(SHELLMCP_AUDIT_LOG)
    _audit_path.parent.mkdir(parents=True, exist_ok=True)
    _audit_handler = WatchedFileHandler(_audit_path)
    _audit_handler.setFormatter(logging.Formatter("%(message)s"))
    audit_log.addHandler(_audit_handler)
except Exception as e:
    log.warning("shellmcp audit log disabled path=%s err=%s", SHELLMCP_AUDIT_LOG, e)


def _audit_event(event: dict) -> None:
    if not audit_log.handlers:
        return
    event.setdefault("ts", time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    event.setdefault("server", SHELLMCP_NAME)
    event.setdefault("server_id", SHELLMCP_SERVER_ID)
    event.setdefault("backend", SHELLMCP_BACKEND)
    event.setdefault("transport", "polling" if QUEUE_URL else "webhook")
    event.setdefault("pid", os.getpid())
    try:
        audit_log.info(json.dumps(event, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
    except Exception as e:
        log.warning("shellmcp audit write failed: %s", e)


def _cmd_sha256(cmd: str) -> str:
    return hashlib.sha256((cmd or "").encode("utf-8", "ignore")).hexdigest()[:16]


def _audit_exec(source: str, cmd: str, cwd: str | None, timeout: int | None, result: dict | None = None, error: str | None = None, job_id: str | None = None, started_at: float | None = None) -> None:
    event = {"event":"shellmcp_exec","source":source,"job_id":job_id,"cmd":cmd,"cmd_sha256":_cmd_sha256(cmd),"cwd":cwd,"timeout":timeout}
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
        'default_user': SHELLMCP_DEFAULT_USER or None,
        'default_home': SHELLMCP_DEFAULT_HOME or None,
        'default_cwd': SHELLMCP_DEFAULT_CWD or None,
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


def _redact_public(value):
    secret_words = ("token", "secret", "password", "passwd", "api_key", "apikey", "authorization", "bearer", "x-api-key")
    if isinstance(value, dict):
        return {str(k): ("***MASKED***" if any(w in str(k).lower() for w in secret_words) else _redact_public(v)) for k, v in value.items()}
    if isinstance(value, list):
        out, skip = [], False
        for item in value:
            if skip:
                out.append("***MASKED***"); skip = False; continue
            if isinstance(item, str) and item.lower() in {"--header", "--token", "--api-key", "--password", "--secret"}:
                out.append(item); skip = True; continue
            out.append(_redact_public(item))
        return out
    if isinstance(value, str):
        lowered = value.lower()
        if "authorization:" in lowered or lowered.startswith(("bearer ", "apikey ", "basic ")):
            parts = value.split(None, 1)
            return (parts[0] + " ***MASKED***") if parts else "***MASKED***"
    return value


def _load_json_file(path):
    try:
        p = Path(path)
        if p.is_file():
            data = json.loads(p.read_text(encoding="utf-8"))
            return data if isinstance(data, dict) else {}
    except Exception as e:
        log.warning("capability registry: failed to read %s: %s", path, e)
    return {}


def _mcp_cfg_paths():
    home = Path.home()
    cfg_path = Path(os.getenv("GPTADMIN_MCP_CONFIG") or os.getenv("SHELLMCP_MCP_CONFIG") or str(home / ".config/gptadmin/mcp.json"))
    agents_dir = Path(os.getenv("GPTADMIN_MCP_AGENTS_DIR") or os.getenv("SHELLMCP_MCP_AGENTS_DIR") or str(home / ".config/gptadmin/mcp-agents.d"))
    if not cfg_path.exists() and Path("/etc/gptadmin/mcp.json").exists(): cfg_path = Path("/etc/gptadmin/mcp.json")
    if not agents_dir.exists() and Path("/etc/gptadmin/mcp-agents.d").exists(): agents_dir = Path("/etc/gptadmin/mcp-agents.d")
    return cfg_path, agents_dir


def _mcp_supervisor_backend():
    override = os.getenv("SHELLMCP_MCP_SUPERVISOR_BACKEND") or os.getenv("GPTADMIN_MCP_BACKEND")
    if override: return override.strip().lower()
    if sys.platform == "darwin": return "launchd"
    if sys.platform.startswith("win"): return "windows-task"
    return "systemd"


def _mcp_service_name(agent_id, backend=None):
    backend = backend or _mcp_supervisor_backend()
    if backend == "launchd": return f"com.gptadmin.mcp.{agent_id}"
    if backend == "windows-task": return f"GPTAdmin MCP {agent_id}"
    return f"gptadmin-mcp-{agent_id}.service"


def _mcp_service_file(agent_id, backend=None):
    backend = backend or _mcp_supervisor_backend()
    if backend == "launchd": return f"/Library/LaunchDaemons/com.gptadmin.mcp.{agent_id}.plist"
    if backend == "systemd": return f"/etc/systemd/system/gptadmin-mcp-{agent_id}.service"
    return None


def _run_lifecycle_cmd(argv, timeout=30):
    try:
        cp = subprocess.run(argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        return {"returncode": cp.returncode, "stdout": cp.stdout[-4000:], "stderr": cp.stderr[-4000:], "argv": argv}
    except Exception as e:
        return {"returncode": None, "stdout": "", "stderr": str(e), "argv": argv}


def _mcp_supervisor_state(agent_id, backend=None):
    backend = backend or _mcp_supervisor_backend(); name = _mcp_service_name(agent_id, backend); service_file = _mcp_service_file(agent_id, backend)
    if backend == "systemd":
        if not sys.platform.startswith("linux"):
            return {"backend": backend, "service_name": name, "active": None, "supported": False, "error": "systemd is only available on linux"}
        cp = _run_lifecycle_cmd(["systemctl", "show", name, "-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "--value"], 3)
        vals = [x.strip() for x in (cp.get("stdout") or "").splitlines()]
        return {"backend": backend, "unit": name, "service_name": name, "service_file": service_file, "load_state": vals[0] if len(vals)>0 else None, "active_state": vals[1] if len(vals)>1 else None, "sub_state": vals[2] if len(vals)>2 else None, "active": (vals[1] == "active") if len(vals)>1 else False, "supported": True, "result": cp}
    if backend == "launchd":
        if sys.platform != "darwin":
            return {"backend": backend, "service_name": name, "active": None, "supported": False, "error": "launchd is only available on macOS"}
        cp = _run_lifecycle_cmd(["launchctl", "print", f"gui/{os.getuid()}/{name}"], 5)
        if cp.get("returncode") != 0: cp = _run_lifecycle_cmd(["launchctl", "print", f"system/{name}"], 5)
        active = cp.get("returncode") == 0
        return {"backend": backend, "label": name, "service_name": name, "service_file": service_file, "active": active, "load_state": "loaded" if active else "not-loaded", "sub_state": "running" if active else "unknown", "supported": True, "result": cp}
    if backend == "windows-task":
        if not sys.platform.startswith("win"):
            return {"backend": backend, "service_name": name, "active": None, "supported": False, "error": "windows-task is only available on Windows"}
        cp = _run_lifecycle_cmd(["schtasks", "/Query", "/TN", name, "/V", "/FO", "LIST"], 10)
        out = (cp.get("stdout") or "") + "\n" + (cp.get("stderr") or "")
        exists = cp.get("returncode") == 0; active = exists and ("Status: Running" in out or "State: Running" in out)
        return {"backend": backend, "task_name": name, "service_name": name, "active": active if exists else False, "load_state": "loaded" if exists else "not-found", "sub_state": "running" if active else ("ready" if exists else "not-found"), "supported": True, "result": cp}
    return {"backend": backend, "service_name": name, "active": None, "supported": False, "error": f"unknown MCP supervisor backend: {backend}"}


def _mcp_capabilities(include_status=True):
    cfg_path, agents_dir = _mcp_cfg_paths(); cfg = _load_json_file(cfg_path)
    servers = cfg.get("mcpServers") if isinstance(cfg.get("mcpServers"), dict) else {}; out = []
    for name, spec in sorted(servers.items()):
        if not isinstance(spec, dict): continue
        agent_id = str(spec.get("agent_id") or spec.get("name") or name)
        item = {"id": f"mcp:{agent_id}", "name": name, "agent_id": agent_id, "kind": "mcp", "role": "capability_executor", "hosted_by": SHELLMCP_NAME, "supervised_by": "shellmcp", "legacy_service": _mcp_service_name(agent_id), "supervisor_backend": _mcp_supervisor_backend(), "service_file": _mcp_service_file(agent_id), "enabled": bool(spec.get("enabled", True)), "transport": "stdio_or_remote", "command": spec.get("command"), "args": _redact_public(spec.get("args") or []), "cwd": spec.get("cwd"), "run_as_user": spec.get("run_as_user") or spec.get("user"), "stdio_format": spec.get("stdio_format") or spec.get("transport") or "auto", "config_file": str(agents_dir / f"{name}.json"), "migration_state": "legacy_relay_supervised; shellmcp_registry_visible"}
        if include_status: item["supervisor"] = _mcp_supervisor_state(agent_id)
        out.append(item)
    return out


def _capability_registry(include_status=True):
    mcp = _mcp_capabilities(include_status=include_status)
    return {"ok": True, "schema_version": 1, "host": SHELLMCP_NAME, "server_id": SHELLMCP_SERVER_ID, "transport_role": "shellmcp_transport_layer", "capability_host": True, "capabilities": [{"id":"shell","kind":"shell","role":"local_executor","hosted_by":SHELLMCP_NAME},{"id":"tasks","kind":"task_store","role":"durable_queue_view","hosted_by":SHELLMCP_NAME},{"id":"logs","kind":"logs","role":"diagnostics","hosted_by":SHELLMCP_NAME},{"id":"system","kind":"system","role":"host_introspection","hosted_by":SHELLMCP_NAME}, *mcp], "summary": {"mcp_count": len(mcp), "enabled_mcp_count": sum(1 for x in mcp if x.get("enabled"))}}

def _user_info(username: str):
    if not username or os.name == 'nt' or pwd is None:
        return None
    try:
        return pwd.getpwnam(username)
    except KeyError:
        return None


def _apply_default_user_env(env_full: dict, username: str | None, pw) -> dict:
    if not username or pw is None:
        return env_full
    env_full = dict(env_full)
    env_full.setdefault('USER', username)
    env_full.setdefault('LOGNAME', username)
    env_full.setdefault('HOME', pw.pw_dir)
    if pw.pw_shell:
        env_full.setdefault('SHELL', pw.pw_shell)
    return env_full


def _preexec_for_user(username: str | None, pw):
    if not username or pw is None or os.name == 'nt':
        return None
    if hasattr(os, 'geteuid') and os.geteuid() != 0:
        return None
    def demote():
        if grp is not None:
            try:
                os.initgroups(username, pw.pw_gid)
            except Exception:
                pass
        os.setgid(pw.pw_gid)
        os.setuid(pw.pw_uid)
    return demote


def _resolve_run_defaults(cwd: str | None, default_user: str | None = None, default_cwd: str | None = None):
    username = default_user or SHELLMCP_DEFAULT_USER or None
    pw = _user_info(username) if username else None
    if username and pw is None:
        raise ValueError(f'default user not found: {username}')
    final_cwd = cwd or default_cwd or SHELLMCP_DEFAULT_CWD or (pw.pw_dir if pw is not None else None)
    return username, pw, final_cwd


def run_cmd(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None, default_user: str | None = None, default_cwd: str | None = None):
    log.info("EXEC: %s (cwd=%s default_user=%s)", cmd, cwd, default_user or SHELLMCP_DEFAULT_USER or "")
    try:
        run_as_user, pw, final_cwd = _resolve_run_defaults(cwd, default_user, default_cwd)
    except Exception as e:
        return {'returncode': -1, 'error': str(e), 'stdout': '', 'stderr': str(e)}
    env_full = os.environ.copy()
    env_full = _apply_default_user_env(env_full, run_as_user, pw)
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
            cwd=final_cwd,
            env=env_full,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout or EXEC_TIMEOUT,
            text=False,
            preexec_fn=_preexec_for_user(run_as_user, pw),
        )
        return {
            'returncode': res.returncode,
            'stdout': _truncate(res.stdout),
            'stderr': _truncate(res.stderr),
            'run_as_user': run_as_user or None,
            'cwd_effective': final_cwd,
        }
    except subprocess.TimeoutExpired as e:
        return {
            'returncode': -1,
            'error': f'timeout {e.timeout}s',
            'stdout': _truncate(e.stdout or ''),
            'stderr': _truncate(e.stderr or ''),
            'run_as_user': run_as_user or None,
            'cwd_effective': final_cwd,
        }


def heartbeat_loop():
    if not HUB_URL:
        return
    hb_url = HUB_URL + '/heartbeat' if '/heartbeat' not in HUB_URL else HUB_URL
    while True:
        payload = {
            'name': SHELLMCP_NAME,
            'server_id': SHELLMCP_SERVER_ID,
            'public_key': SHELLMCP_PUBLIC_KEY_B64,
            'fingerprint': SHELLMCP_FINGERPRINT,
            'base_url': SHELLMCP_URL or f'http://{socket.gethostname()}:{PORT}',
            'shellmcp_token': TOKEN,
            'cores': os.cpu_count(),
            'mem_mb': _get_mem_mb(),
            'time': int(time.time()),
            'mode': 'long_poll' if QUEUE_URL and _queue_is_long_poll() else ('polling' if QUEUE_URL else 'webhook'),
            'queue_transport': QUEUE_TRANSPORT if QUEUE_URL else None,
            'transport_role': 'shellmcp_transport_layer',
            'capability_host': True,
            'capability_model': 'shellmcp transports hub jobs to local executors/capabilities',
            'local_capabilities': ['shell', 'tasks', 'logs', 'system', 'mcp_supervision'],
            'capability_registry': _capability_registry(include_status=False).get('summary'),
            'os': sys.platform,
            'version': BUILD_VERSION,
            'build_version': BUILD_VERSION,
            'build_ts': BUILD_TS,
            'git_commit': GIT_COMMIT,
            'backend': SHELLMCP_BACKEND,
            'default_user': SHELLMCP_DEFAULT_USER or None,
            'default_home': SHELLMCP_DEFAULT_HOME or None,
            'default_cwd': SHELLMCP_DEFAULT_CWD or None,
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
            queue_path = f"/queue/{SHELLMCP_NAME}"
            url = f"{QUEUE_URL}/{SHELLMCP_NAME}"
            if _queue_is_long_poll():
                url += f"?timeout={max(1, _queue_long_poll_timeout_s())}"
            req = urlrequest.Request(url, headers=_signed_headers('GET', queue_path, b''))
            with urlrequest.urlopen(req, timeout=_queue_http_timeout_s()) as r:
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
                            result_path = f"/queue/{SHELLMCP_NAME}/result"
                            result_url = f"{QUEUE_URL}/{SHELLMCP_NAME}/result"
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
        if not _queue_is_long_poll():
            time.sleep(POLL_INT)


class Handler(BaseHTTPRequestHandler):
    server_version = 'shellmcp_pure/1.0'
    # Нужно для chunked-стриминга
    protocol_version = 'HTTP/1.1'

    def _auth(self):
        auth = self.headers.get('Authorization', '')
        if auth == f'Bearer {TOKEN}':
            return True

        # Modern gptadmin_hub calls shellmcp with signed X-GPTAdmin-* headers instead of
        # Authorization. Full shellmcp verifies the Ed25519 signature. shellmcp_pure is the
        # minimal cross-platform fallback, so accept signed hub-shaped requests to
        # stay compatible with gptadmin_hub. Keep Bearer token support for local/manual
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
        elif parsed.path == '/capabilities':
            params = parse.parse_qs(parsed.query)
            include_status = params.get('include_status', ['true'])[0].lower() not in {'0', 'false', 'no'}
            self._json(_capability_registry(include_status=include_status))
        elif parsed.path == '/capabilities/mcp':
            params = parse.parse_qs(parsed.query)
            include_status = params.get('include_status', ['true'])[0].lower() not in {'0', 'false', 'no'}
            self._json({'ok': True, 'host': SHELLMCP_NAME, 'capabilities': _mcp_capabilities(include_status=include_status)})
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
                res = run_cmd(body.get('cmd', ''), body.get('timeout'), body.get('cwd'), body.get('env'), body.get('default_user') or body.get('run_as_user') or body.get('user'), body.get('default_cwd'))
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
            try:
                run_as_user, pw, cwd = _resolve_run_defaults(cwd, body.get('default_user') or body.get('run_as_user') or body.get('user'), body.get('default_cwd'))
            except Exception as e:
                self._json({'error': str(e)}, 400)
                return
            env = os.environ.copy()
            env = _apply_default_user_env(env, run_as_user, pw)
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
                universal_newlines=True,
                preexec_fn=_preexec_for_user(run_as_user, pw),
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
