# DEPRECATED: legacy Python shellmcp/rootd compatibility implementation.
# Primary GPTAdmin shell transport is go-shellmcp / rootd-go-canary.
# Keep this file only for compatibility with old/source installs.
# shellmcp.py / rootd.py compatibility entrypoint
"""
DEPRECATED legacy Python shellmcp/rootd compatibility implementation.
Use go-shellmcp / rootd-go-canary for new installs.

Голый root-API без Docker, но с:
 • /exec                             – любая команда
 • heartbeat → HUB_URL (если задан)

Env:
  ROOTD_TOKEN     bearer-токен (def: CHANGE_ME)
  LOG_LIMIT_B     макс. байт stdout/stderr (def: 8192)
  EXEC_TIMEOUT    таймаут /exec (def: 300)
  HUB_URL         http(s)://hub:9001/ (def: none)
  HB_INTERVAL_S   период heartbeat (def: 60)
  QUEUE_URL       если задан – включаем polling и берём HUB_URL с заменой
                   пути на /queue
  POLL_INTERVAL_S период опроса QUEUE_URL (def: 5)
  ROOTD_TRANSPORT websocket|webhook|polling|auto (def: webhook)
  ROOTD_URL       explicit externally reachable URL sent to hub
  ROOTD_NAME      explicit server name sent to hub
  ROOTD_PROXY_FOR/ROOTD_PROXY_VIA/ROOTD_BACKEND optional topology metadata
  ROOTD_AUTO_UPDATE       1/true to enable safe self-update loop (def: off)
  ROOTD_UPDATE_MANIFEST_URL manifest URL with build_version, sha256, url
  ROOTD_UPDATE_TOKEN optional Bearer token for update manifest/artifact
  ROOTD_UPDATE_INTERVAL_S update check interval (def: 3600)
"""

import os
import time
import json
import asyncio
import threading
import socket
import sys
import tempfile
import tarfile
import hashlib
import shutil
import subprocess
import uuid
import platform
from typing import Optional
from pathlib import Path
from logging.handlers import WatchedFileHandler

import requests
try:
    import websockets
except Exception:
    websockets = None
from fastapi import FastAPI, Body, Depends, HTTPException, Request
from fastapi.responses import JSONResponse, FileResponse
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from starlette.responses import StreamingResponse
from pydantic import BaseModel
import logging
import traceback
from urllib.parse import urlparse, urlunparse, urljoin

from gptadmin_security import (
    NonceCache,
    load_or_create_identity,
    load_public_key_b64,
    sign_request,
    verify_signature,
)

try:
    from gptadmin_build_info import BUILD_VERSION, BUILD_TS, GIT_COMMIT, build_info
except Exception:
    BUILD_VERSION = 0
    BUILD_TS = "unknown"
    GIT_COMMIT = "unknown"
    def build_info(component: str) -> dict:
        return {"component": component, "build_version": BUILD_VERSION, "build_ts": BUILD_TS, "git_commit": GIT_COMMIT}


def _env(name: str, default: str | None = None) -> str | None:
    """New shell MCP env names with ROOTD_* compatibility fallback."""
    return os.getenv(f"SHELL_{name}", os.getenv(f"ROOTD_{name}", default))


port = int(_env("PORT", os.getenv("PORT", "25900")))
# --- логирование ---
LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
        logging.FileHandler(f"shellmcp-{port}.log"),    # лог в файл
        logging.StreamHandler()              # лог в stdout (для systemd)
    ]
)
log = logging.getLogger("rootd")
audit_log = logging.getLogger("shellmcp.audit")
audit_log.setLevel(logging.INFO)
audit_log.propagate = False
# ------------------------------------------------------------------

TOKEN = _env("TOKEN", "srv_secret")
HUB_URL = os.getenv("HUB_URL", 'https://gptadmin.bezrabotnyi.com/')
HEARTBEAT_URL=HUB_URL+'/heartbeat' if '/heartbeat' not in HUB_URL else HUB_URL
ROOTD_URL = _env("URL")
ROOTD_NAME = _env("NAME")
ROOTD_PROXY_FOR = _env("PROXY_FOR")
ROOTD_PROXY_VIA = _env("PROXY_VIA")
ROOTD_BACKEND = _env("BACKEND") or ("ssh" if os.getenv("SSH_HOST") else "local")
ROOTD_IDENTITY_DIR = _env("IDENTITY_DIR") or ("/etc/gptadmin" if os.access("/etc", os.W_OK) else str(Path.home() / ".gptadmin"))
HUB_PUBLIC_KEY_FILE = os.getenv("HUB_PUBLIC_KEY_FILE", str(Path(ROOTD_IDENTITY_DIR) / "hub_ed25519.pub"))
HUB_PUBLIC_KEY_B64 = os.getenv("HUB_PUBLIC_KEY", "")
ROOTD_IDENTITY = load_or_create_identity(ROOTD_IDENTITY_DIR, ROOTD_NAME or socket.gethostname(), prefix="rootd")
ROOTD_SERVER_ID = ROOTD_IDENTITY["identity"]["server_id"]
ROOTD_PUBLIC_KEY_B64 = ROOTD_IDENTITY["public_key_b64"]
ROOTD_FINGERPRINT = ROOTD_IDENTITY["fingerprint"]
NONCES = NonceCache(ttl_s=int(os.getenv("ROOTD_NONCE_TTL_S", "300")))
TRANSPORT = _env("TRANSPORT", "webhook").lower()
HB_INT = int(os.getenv("HB_INTERVAL_S", "60"))
ROOTD_AUTO_UPDATE = _env("AUTO_UPDATE", "0").lower() in {"1", "true", "yes", "on"}
ROOTD_UPDATE_INTERVAL_S = int(_env("UPDATE_INTERVAL_S", "3600"))
ROOTD_SERVICE_NAME = _env("SERVICE_NAME", "shellmcp.service")

def _hub_artifact_url(path: str) -> str:
    if not HUB_URL:
        return ""
    parsed = urlparse(HUB_URL)
    if parsed.scheme and parsed.netloc:
        base = f"{parsed.scheme}://{parsed.netloc}"
    else:
        base = HUB_URL.rstrip("/")
        if base.endswith("/heartbeat"):
            base = base[: -len("/heartbeat")]
    return f"{base}{path}"


def _derive_hub_queue_url(raw_url: str) -> str | None:
    candidate = raw_url or HUB_URL
    parsed = urlparse(candidate)
    if not (parsed.scheme and parsed.netloc) and candidate != HUB_URL:
        parsed = urlparse(HUB_URL)
    if parsed.scheme and parsed.netloc:
        return urlunparse((parsed.scheme, parsed.netloc, "/queue", "", "", ""))
    return None

def _platform_id() -> str:
    if sys.platform.startswith("linux"):
        return "linux"
    if sys.platform == "darwin":
        return "darwin"
    if os.name == "nt" or sys.platform.startswith("win"):
        return "windows"
    return sys.platform


def _arch_id() -> str:
    arch = platform.machine().lower()
    return {"amd64": "x86_64", "x64": "x86_64", "aarch64": "arm64"}.get(arch, arch)


def _resolve_update_url(value: str | None, fallback: str | None = None) -> str:
    raw = (value or fallback or "").strip()
    if not raw:
        return ""
    parsed = urlparse(raw)
    if parsed.scheme and parsed.netloc:
        return raw
    base = ROOTD_UPDATE_MANIFEST_URL or _hub_artifact_url("/") or HUB_URL
    if base and not base.endswith("/"):
        base = base.rsplit("/", 1)[0] + "/"
    return urljoin(base, raw)


ROOTD_UPDATE_MANIFEST_URL = _env("UPDATE_MANIFEST_URL") or _hub_artifact_url("/artifacts/rootd.json")
ROOTD_UPDATE_URL = _env("UPDATE_URL") or _hub_artifact_url("/artifacts/rootd.tar.gz")
ROOTD_UPDATE_TOKEN = _env("UPDATE_TOKEN", "")
ROOTD_SERVICE_SCOPE = _env("SERVICE_SCOPE", "system" if os.name != "nt" and hasattr(os, "geteuid") and os.geteuid() == 0 else "user").lower()
ROOTD_RESTART_CMD = _env("RESTART_CMD", "")
ROOTD_OUTBOX_DIR = Path(_env("OUTBOX_DIR", str(Path(ROOTD_IDENTITY_DIR) / "outbox")))
ROOTD_OUTBOX_RETRY_MIN_S = float(_env("OUTBOX_RETRY_MIN_S", "2"))
ROOTD_OUTBOX_RETRY_MAX_S = float(_env("OUTBOX_RETRY_MAX_S", "300"))
ROOTD_OUTBOX_SCAN_S = float(_env("OUTBOX_SCAN_S", "2"))
ROOTD_AUDIT_LOG = _env("AUDIT_LOG") or ("/var/log/gptadmin/shellmcp-audit.log" if os.access("/var/log", os.W_OK) else str(Path.home() / ".gptadmin" / "shellmcp-audit.log"))
try:
    _rootd_audit_path = Path(ROOTD_AUDIT_LOG)
    _rootd_audit_path.parent.mkdir(parents=True, exist_ok=True)
    _rootd_audit_handler = WatchedFileHandler(_rootd_audit_path)
    _rootd_audit_handler.setFormatter(logging.Formatter("%(message)s"))
    audit_log.addHandler(_rootd_audit_handler)
except Exception as e:
    log.warning("shellmcp audit log disabled path=%s err=%s", ROOTD_AUDIT_LOG, e)


def _audit_event(event: dict) -> None:
    if not audit_log.handlers:
        return
    event.setdefault("ts", time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    event.setdefault("server", ROOTD_NAME or socket.gethostname())
    event.setdefault("server_id", ROOTD_SERVER_ID)
    event.setdefault("backend", ROOTD_BACKEND)
    event.setdefault("transport", TRANSPORT)
    event.setdefault("pid", os.getpid())
    try:
        audit_log.info(json.dumps(event, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
    except Exception as e:
        log.warning("shellmcp audit write failed: %s", e)


def _cmd_sha256(cmd: str) -> str:
    return hashlib.sha256((cmd or "").encode("utf-8", "ignore")).hexdigest()[:16]


def _request_peer(request: Optional[Request]) -> dict:
    if not request:
        return {}
    return {
        "ip": request.headers.get("x-real-ip") or (request.headers.get("x-forwarded-for") or "").split(",")[0].strip() or (request.client.host if request.client else None),
        "x_forwarded_for": request.headers.get("x-forwarded-for"),
        "user_agent": request.headers.get("user-agent"),
        "auth_kind": "gptadmin-signature" if request.headers.get("x-gptadmin-signature") else "none",
        "endpoint": request.url.path,
        "method": request.method,
    }


def _audit_exec(source: str, cmd: str, cwd: Optional[str], timeout: Optional[int], result: Optional[dict] = None, error: Optional[str] = None, job_id: Optional[str] = None, request: Optional[Request] = None, started_at: Optional[float] = None) -> None:
    event = {"event": "shell_exec", "source": source, "job_id": job_id, "cmd": cmd, "cmd_sha256": _cmd_sha256(cmd), "cwd": cwd, "timeout": timeout}
    event.update(_request_peer(request))
    if started_at is not None:
        event["dt_ms"] = round((time.perf_counter() - started_at) * 1000, 2)
    if isinstance(result, dict):
        event["returncode"] = result.get("returncode")
        event["run_as_user"] = result.get("run_as_user")
    if error:
        event["error"] = error
    _audit_event(event)


def _update_headers() -> dict:
    return {"Authorization": f"Bearer {ROOTD_UPDATE_TOKEN}"} if ROOTD_UPDATE_TOKEN else {}


def _signed_json_headers(method: str, path: str, body: bytes) -> dict:
    signed = sign_request(ROOTD_IDENTITY["private_key"], method, path, body)
    return {
        "Content-Type": "application/json",
        "X-GPTAdmin-Server": ROOTD_NAME or socket.gethostname(),
        "X-GPTAdmin-Server-ID": ROOTD_SERVER_ID,
        "X-GPTAdmin-Timestamp": signed["timestamp"],
        "X-GPTAdmin-Nonce": signed["nonce"],
        "X-GPTAdmin-Signature": signed["signature"],
    }


_QUEUE_OFFLINE_UNTIL = 0.0


def _outbox_init() -> None:
    try:
        ROOTD_OUTBOX_DIR.mkdir(parents=True, exist_ok=True)
    except Exception as e:
        log.warning("queue outbox disabled path=%s err=%s", ROOTD_OUTBOX_DIR, e)


def _outbox_metrics() -> dict:
    now = time.time()
    metrics = {
        "outbox_dir": str(ROOTD_OUTBOX_DIR),
        "outbox_files": 0,
        "outbox_bytes": 0,
        "outbox_oldest_age_s": 0,
        "outbox_failed_attempts": 0,
        "outbox_last_error": None,
    }
    try:
        if not ROOTD_OUTBOX_DIR.exists():
            return metrics
        oldest = None
        last_error_at = 0
        for path in ROOTD_OUTBOX_DIR.glob("*.json"):
            try:
                st = path.stat()
                metrics["outbox_files"] += 1
                metrics["outbox_bytes"] += st.st_size
                oldest = st.st_mtime if oldest is None else min(oldest, st.st_mtime)
                try:
                    entry = _outbox_load(path)
                    attempts = int(entry.get("attempts") or 0)
                    metrics["outbox_failed_attempts"] += attempts
                    if entry.get("last_error") and float(entry.get("updated_at") or st.st_mtime) >= last_error_at:
                        last_error_at = float(entry.get("updated_at") or st.st_mtime)
                        metrics["outbox_last_error"] = str(entry.get("last_error"))[:500]
                except Exception:
                    pass
            except FileNotFoundError:
                continue
        if oldest is not None:
            metrics["outbox_oldest_age_s"] = max(0, int(now - oldest))
    except Exception as e:
        metrics["outbox_last_error"] = f"metrics failed: {e}"
    return metrics


def _outbox_file(message_id: str) -> Path:
    return ROOTD_OUTBOX_DIR / f"{message_id}.json"


def _outbox_write(entry: dict) -> Path:
    _outbox_init()
    path = _outbox_file(str(entry["id"]))
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(entry, ensure_ascii=False, sort_keys=True, separators=(",", ":")), encoding="utf-8")
    os.replace(tmp, path)
    return path


def _outbox_load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _queue_post_payload(srv_name: str, endpoint: str, payload: dict, timeout: float = 5.0) -> None:
    if not CALLBACK_QUEUE_URL:
        raise RuntimeError("CALLBACK_QUEUE_URL is not set")
    queue_path = f"/queue/{srv_name}/{endpoint}"
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    r = requests.post(
        f"{CALLBACK_QUEUE_URL}/{srv_name}/{endpoint}",
        data=body,
        headers=_signed_json_headers("POST", queue_path, body),
        timeout=timeout,
    )
    if r.status_code < 200 or r.status_code >= 300:
        raise RuntimeError(f"queue {endpoint} HTTP {r.status_code}: {r.text[:200]}")


def _outbox_try_send(path: Path) -> bool:
    entry = _outbox_load(path)
    _queue_post_payload(str(entry["srv_name"]), str(entry["endpoint"]), dict(entry["payload"]), float(entry.get("timeout") or 5))
    try:
        path.unlink()
    except FileNotFoundError:
        pass
    return True


def _outbox_backoff(path: Path, entry: dict, err: Exception) -> None:
    attempts = int(entry.get("attempts") or 0) + 1
    delay = min(ROOTD_OUTBOX_RETRY_MAX_S, ROOTD_OUTBOX_RETRY_MIN_S * (2 ** min(attempts - 1, 8)))
    entry.update({
        "attempts": attempts,
        "last_error": str(err),
        "next_at": time.time() + delay,
        "updated_at": int(time.time()),
    })
    try:
        _outbox_write(entry)
    except Exception as write_err:
        log.warning("queue outbox backoff write failed path=%s err=%s", path, write_err)


def _queue_send_or_spool(srv_name: str, endpoint: str, payload: dict, kind: str, job_id: str, timeout: float = 5.0) -> None:
    global _QUEUE_OFFLINE_UNTIL
    if not CALLBACK_QUEUE_URL or not job_id:
        return
    now = time.time()
    message_id = f"{int(now * 1000)}-{kind}-{job_id}-{uuid.uuid4().hex[:8]}"
    entry = {
        "id": message_id,
        "srv_name": srv_name,
        "endpoint": endpoint,
        "kind": kind,
        "job_id": job_id,
        "payload": payload,
        "timeout": timeout,
        "attempts": 0,
        "created_at": int(now),
        "updated_at": int(now),
        "next_at": now,
    }
    path = _outbox_write(entry)
    if now < _QUEUE_OFFLINE_UNTIL:
        return
    try:
        _outbox_try_send(path)
    except Exception as e:
        _QUEUE_OFFLINE_UNTIL = time.time() + min(ROOTD_OUTBOX_RETRY_MAX_S, ROOTD_OUTBOX_RETRY_MIN_S * 2)
        _outbox_backoff(path, entry, e)
        log.debug("queue %s spooled job=%s path=%s err=%s", kind, job_id, path, e)


def outbox_retry_loop() -> None:
    if not CALLBACK_QUEUE_URL:
        return
    _outbox_init()
    while True:
        try:
            now = time.time()
            for path in sorted(ROOTD_OUTBOX_DIR.glob("*.json")):
                try:
                    entry = _outbox_load(path)
                    if float(entry.get("next_at") or 0) > now:
                        continue
                    _outbox_try_send(path)
                    log.debug("queue outbox delivered path=%s", path)
                except FileNotFoundError:
                    continue
                except Exception as e:
                    try:
                        entry = _outbox_load(path)
                        _outbox_backoff(path, entry, e)
                    except Exception as e2:
                        log.warning("queue outbox retry failed path=%s err=%s", path, e2)
        except Exception as e:
            log.warning("queue outbox loop failed: %s", e)
        time.sleep(ROOTD_OUTBOX_SCAN_S)

if os.getenv("QUEUE_URL"):
    QUEUE_URL = _derive_hub_queue_url(os.getenv("QUEUE_URL") or HUB_URL)
else:
    QUEUE_URL = None
CALLBACK_QUEUE_URL = os.getenv("ROOTD_CALLBACK_QUEUE_URL") or QUEUE_URL or _derive_hub_queue_url(HUB_URL)

POLL_INT = int(os.getenv("POLL_INTERVAL_S", "5"))
QUEUE_TRANSPORT = os.getenv("QUEUE_TRANSPORT", "long_poll").strip().lower()
QUEUE_LONG_POLL_TIMEOUT_S = int(os.getenv("QUEUE_LONG_POLL_TIMEOUT_S", "55"))
QUEUE_HTTP_TIMEOUT_S = int(os.getenv("QUEUE_HTTP_TIMEOUT_S", str(QUEUE_LONG_POLL_TIMEOUT_S + 10 if QUEUE_TRANSPORT in {"long_poll", "long-poll", "longpoll"} else 5)))
QUEUE_IS_LONG_POLL = QUEUE_TRANSPORT in {"long_poll", "long-poll", "longpoll"}

app = FastAPI(title="rootd", version=str(BUILD_VERSION))
auth = HTTPBearer(auto_error=False)


async def guard(request: Request):
    public_key = HUB_PUBLIC_KEY_B64 or (load_public_key_b64(HUB_PUBLIC_KEY_FILE) if Path(HUB_PUBLIC_KEY_FILE).exists() else "")
    if not public_key:
        raise HTTPException(500, "hub public key is not configured")
    ts = request.headers.get("X-GPTAdmin-Timestamp")
    nonce = request.headers.get("X-GPTAdmin-Nonce")
    sig = request.headers.get("X-GPTAdmin-Signature")
    hub_id = request.headers.get("X-GPTAdmin-Hub-ID", "hub")
    if not ts or not nonce or not sig:
        raise HTTPException(401, "missing signed request headers")
    body = await request.body()
    try:
        NONCES.check_and_store(f"hub:{hub_id}", nonce)
        verify_signature(public_key, request.method, request.url.path, ts, nonce, body, sig)
    except Exception as e:
        raise HTTPException(401, f"invalid signed request: {e}")
    return True


if os.getenv("SSH_HOST"):
    import rootd_ssh as backend
elif sys.platform.startswith("win"):
    import rootd_win as backend
elif sys.platform == "darwin":
    import rootd_mac as backend
else:
    import rootd_linux as backend


class ExecReq(BaseModel):
    cmd: str
    env: Optional[dict] = None
    cwd: Optional[str] = None
    timeout: Optional[int] = None


class ExecCallbackReq(ExecReq):
    job_id: str


def _callback_job_runner(job_id: str, cmd: str, timeout: Optional[int], cwd: Optional[str], env: dict, request_source: str = "http_callback") -> None:
    srv_name = ROOTD_NAME or socket.gethostname()
    started = time.perf_counter()
    try:
        if hasattr(backend, "run_live"):
            res = asyncio.run(_poll_run_live_job(srv_name, {"id": job_id, "cmd": cmd, "timeout": timeout, "cwd": cwd}, env))
        else:
            res = backend.run(cmd, timeout, cwd, env)
        _audit_exec(request_source, cmd, cwd, timeout, result=res, job_id=job_id, started_at=started)
    except Exception as e:
        log.exception("callback exec failed")
        res = {"error": str(e), "traceback": traceback.format_exc()}
        _audit_exec(request_source, cmd, cwd, timeout, error=str(e), job_id=job_id, started_at=started)
    _queue_send_or_spool(srv_name, "result", {"id": job_id, "result": res}, kind="result", job_id=job_id, timeout=5)


# ---------------- EXEC --------------------------------------------
@app.post("/exec", dependencies=[Depends(guard)])
def exec_cmd(request: Request, body: ExecReq = Body(...)):
    log.info(f"EXEC: {body.cmd} (cwd={body.cwd})")

    env = os.environ.copy()
    if body.env:
        env.update(body.env)

    started = time.perf_counter()
    try:
        result = backend.run(body.cmd, body.timeout, body.cwd, env)
        _audit_exec("http", body.cmd, body.cwd, body.timeout, result=result, request=request, started_at=started)
        return result
    except Exception as e:
        log.exception("Error in /exec")
        _audit_exec("http", body.cmd, body.cwd, body.timeout, error=str(e), request=request, started_at=started)
        return JSONResponse(
            status_code=500,
            content={"error": str(e), "traceback": traceback.format_exc()},
        )


@app.post("/exec/callback", dependencies=[Depends(guard)])
def exec_callback(request: Request, body: ExecCallbackReq = Body(...)):
    """Start command and deliver stdout/stderr/result back to hub through durable callback outbox."""
    log.info(f"EXEC_CALLBACK: job={body.job_id} cmd={body.cmd} (cwd={body.cwd})")
    _audit_exec("http_callback_start", body.cmd, body.cwd, body.timeout, job_id=body.job_id, request=request)
    env = os.environ.copy()
    if body.env:
        env.update(body.env)
    threading.Thread(target=_callback_job_runner, args=(body.job_id, body.cmd, body.timeout, body.cwd, env), daemon=True).start()
    return {"ok": True, "status": "running", "job_id": body.job_id, "delivery": "callback_outbox"}


@app.post("/exec/stream", dependencies=[Depends(guard)])
async def exec_stream(request: Request, body: ExecReq = Body(...)):
    """Run command and stream combined stdout/stderr."""
    log.info(f"EXEC_STREAM: {body.cmd} (cwd={body.cwd})")
    _audit_exec("http_stream_start", body.cmd, body.cwd, body.timeout, request=request)

    env = os.environ.copy()
    if body.env:
        env.update(body.env)

    try:
        generator = await backend.run_stream(body.cmd, body.cwd, env)
        return StreamingResponse(generator(), media_type="text/plain")
    except Exception as e:
        log.exception("Error in /exec/stream")
        return JSONResponse(
            status_code=500,
            content={"error": str(e), "traceback": traceback.format_exc()},
        )


@app.post("/exec/live", dependencies=[Depends(guard)])
async def exec_live(request: Request, body: ExecReq = Body(...)):
    """Run command and stream NDJSON events: stdout/stderr chunks and final exit."""
    log.info(f"EXEC_LIVE: {body.cmd} (cwd={body.cwd})")
    _audit_exec("http_live_start", body.cmd, body.cwd, body.timeout, request=request)

    env = os.environ.copy()
    if body.env:
        env.update(body.env)

    async def ndjson_generator():
        started = time.perf_counter()
        try:
            if not hasattr(backend, "run_live"):
                yield (json.dumps({"type": "error", "error": "backend does not support live exec"}, ensure_ascii=False) + "\n").encode("utf-8")
                return
            generator = await backend.run_live(body.cmd, body.timeout, body.cwd, env)
            async for event in generator():
                if event.get("type") == "exit":
                    _audit_exec("http_live", body.cmd, body.cwd, body.timeout, result=event, request=request, started_at=started)
                yield (json.dumps(event, ensure_ascii=False) + "\n").encode("utf-8")
        except Exception as e:
            log.exception("Error in /exec/live")
            _audit_exec("http_live", body.cmd, body.cwd, body.timeout, error=str(e), request=request, started_at=started)
            yield (json.dumps({"type": "error", "error": str(e), "traceback": traceback.format_exc()}, ensure_ascii=False) + "\n").encode("utf-8")

    return StreamingResponse(ndjson_generator(), media_type="application/x-ndjson")


# ---------------- INFO --------------------------------------------
@app.get("/system/info", dependencies=[Depends(guard)])
def sys_info():
    try:
        return backend.info()
    except Exception as e:
        log.exception("Error in /system/info")
        return JSONResponse(status_code=500, content={"error": str(e)})


@app.get("/system/health", dependencies=[Depends(guard)])
def health():
    return backend.health()

# ---------------- CAPABILITIES -------------------------------------
def _redact_public(value):
    secret_words = ("token", "secret", "password", "passwd", "api_key", "apikey", "authorization", "bearer", "x-api-key")
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            key = str(k)
            out[key] = "***MASKED***" if any(w in key.lower() for w in secret_words) else _redact_public(v)
        return out
    if isinstance(value, list):
        out = []
        skip_next = False
        for item in value:
            if skip_next:
                out.append("***MASKED***")
                skip_next = False
                continue
            if isinstance(item, str) and item.lower() in {"--header", "--token", "--api-key", "--password", "--secret"}:
                out.append(item)
                skip_next = True
                continue
            out.append(_redact_public(item))
        return out
    if isinstance(value, str):
        lowered = value.lower()
        if "authorization:" in lowered or lowered.startswith(("bearer ", "apikey ", "basic ")):
            parts = value.split(None, 1)
            return (parts[0] + " ***MASKED***") if parts else "***MASKED***"
    return value


def _load_json_file(path: Path) -> dict:
    try:
        if path.is_file():
            data = json.loads(path.read_text(encoding="utf-8"))
            return data if isinstance(data, dict) else {}
    except Exception as e:
        log.warning("capability registry: failed to read %s: %s", path, e)
    return {}


def _mcp_supervisor_backend() -> str:
    override = os.getenv("ROOTD_MCP_SUPERVISOR_BACKEND") or os.getenv("GPTADMIN_MCP_BACKEND")
    if override:
        return override.strip().lower()
    if sys.platform == "darwin":
        return "launchd"
    if sys.platform.startswith("win"):
        return "windows-task"
    return "systemd"


def _mcp_service_name(agent_id: str, *, backend: Optional[str] = None) -> str:
    backend = backend or _mcp_supervisor_backend()
    if backend == "launchd":
        return f"com.gptadmin.mcp.{agent_id}"
    if backend == "windows-task":
        return f"GPTAdmin MCP {agent_id}"
    return f"gptadmin-mcp-{agent_id}.service"


def _mcp_service_file(agent_id: str, *, backend: Optional[str] = None) -> Optional[str]:
    backend = backend or _mcp_supervisor_backend()
    if backend == "launchd":
        return f"/Library/LaunchDaemons/com.gptadmin.mcp.{agent_id}.plist"
    if backend == "systemd":
        return f"/etc/systemd/system/gptadmin-mcp-{agent_id}.service"
    return None


def _run_lifecycle_cmd(argv: list, timeout: int = 30) -> dict:
    try:
        cp = subprocess.run(argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        return {"returncode": cp.returncode, "stdout": cp.stdout[-4000:], "stderr": cp.stderr[-4000:], "argv": argv}
    except Exception as e:
        return {"returncode": None, "stdout": "", "stderr": str(e), "argv": argv}


def _mcp_supervisor_state(agent_id: str, *, backend: Optional[str] = None) -> dict:
    backend = backend or _mcp_supervisor_backend()
    name = _mcp_service_name(agent_id, backend=backend)
    service_file = _mcp_service_file(agent_id, backend=backend)
    if backend == "systemd":
        if not sys.platform.startswith("linux"):
            return {"backend": backend, "unit": name, "active": None, "supported": False, "error": "systemd is only available on linux"}
        try:
            cp = subprocess.run(["systemctl", "show", name, "-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "--value"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=3)
            vals = [x.strip() for x in cp.stdout.splitlines()]
            return {"backend": backend, "unit": name, "service_name": name, "service_file": service_file, "load_state": vals[0] if len(vals)>0 else None, "active_state": vals[1] if len(vals)>1 else None, "sub_state": vals[2] if len(vals)>2 else None, "active": (vals[1] == "active") if len(vals)>1 else False, "supported": True}
        except Exception as e:
            return {"backend": backend, "unit": name, "service_name": name, "service_file": service_file, "active": False, "supported": True, "error": str(e)}
    if backend == "launchd":
        if sys.platform != "darwin":
            return {"backend": backend, "label": name, "service_name": name, "service_file": service_file, "active": None, "supported": False, "error": "launchd is only available on macOS"}
        cp = _run_lifecycle_cmd(["launchctl", "print", f"system/{name}"], timeout=5)
        active = cp.get("returncode") == 0
        return {"backend": backend, "label": name, "service_name": name, "service_file": service_file, "active": active, "load_state": "loaded" if active else "not-loaded", "sub_state": "running" if active else "unknown", "supported": True, "result": cp}
    if backend == "windows-task":
        if not sys.platform.startswith("win"):
            return {"backend": backend, "task_name": name, "service_name": name, "active": None, "supported": False, "error": "windows-task is only available on Windows"}
        cp = _run_lifecycle_cmd(["schtasks", "/Query", "/TN", name, "/V", "/FO", "LIST"], timeout=10)
        out = (cp.get("stdout") or "") + "\n" + (cp.get("stderr") or "")
        exists = cp.get("returncode") == 0
        active = exists and ("Status: Running" in out or "Status:  Running" in out or "State: Running" in out)
        return {"backend": backend, "task_name": name, "service_name": name, "active": active if exists else False, "load_state": "loaded" if exists else "not-found", "sub_state": "running" if active else ("ready" if exists else "not-found"), "supported": True, "result": cp}
    return {"backend": backend, "service_name": name, "active": None, "supported": False, "error": f"unknown MCP supervisor backend: {backend}"}


def _mcp_supervisor_action(agent_id: str, action: str, *, backend: Optional[str] = None) -> dict:
    action = (action or "status").strip().lower()
    allowed = {"status", "start", "stop", "restart"}
    if action not in allowed:
        raise HTTPException(400, f"unsupported action: {action}; expected one of {sorted(allowed)}")
    backend = backend or _mcp_supervisor_backend()
    name = _mcp_service_name(agent_id, backend=backend)
    service_file = _mcp_service_file(agent_id, backend=backend)
    before = _mcp_supervisor_state(agent_id, backend=backend)
    result = None
    if action != "status":
        if backend == "systemd":
            result = _run_lifecycle_cmd(["systemctl", action, name], timeout=30) if sys.platform.startswith("linux") else {"returncode": None, "stdout": "", "stderr": "systemd is only available on linux"}
        elif backend == "launchd":
            if sys.platform != "darwin":
                result = {"returncode": None, "stdout": "", "stderr": "launchd is only available on macOS"}
            elif action == "start":
                result = _run_lifecycle_cmd(["launchctl", "bootstrap", "system", service_file or f"/Library/LaunchDaemons/{name}.plist"], timeout=30)
            elif action == "stop":
                result = _run_lifecycle_cmd(["launchctl", "bootout", "system", service_file or f"/Library/LaunchDaemons/{name}.plist"], timeout=30)
            else:
                stop_res = _run_lifecycle_cmd(["launchctl", "bootout", "system", service_file or f"/Library/LaunchDaemons/{name}.plist"], timeout=30)
                start_res = _run_lifecycle_cmd(["launchctl", "bootstrap", "system", service_file or f"/Library/LaunchDaemons/{name}.plist"], timeout=30)
                result = {"returncode": start_res.get("returncode"), "stdout": (stop_res.get("stdout") or "") + (start_res.get("stdout") or ""), "stderr": (stop_res.get("stderr") or "") + (start_res.get("stderr") or ""), "steps": {"stop": stop_res, "start": start_res}}
        elif backend == "windows-task":
            if not sys.platform.startswith("win"):
                result = {"returncode": None, "stdout": "", "stderr": "windows-task is only available on Windows"}
            elif action == "start":
                result = _run_lifecycle_cmd(["schtasks", "/Run", "/TN", name], timeout=30)
            elif action == "stop":
                result = _run_lifecycle_cmd(["schtasks", "/End", "/TN", name], timeout=30)
            else:
                stop_res = _run_lifecycle_cmd(["schtasks", "/End", "/TN", name], timeout=30)
                start_res = _run_lifecycle_cmd(["schtasks", "/Run", "/TN", name], timeout=30)
                result = {"returncode": start_res.get("returncode"), "stdout": (stop_res.get("stdout") or "") + (start_res.get("stdout") or ""), "stderr": (stop_res.get("stderr") or "") + (start_res.get("stderr") or ""), "steps": {"stop": stop_res, "start": start_res}}
        else:
            result = {"returncode": None, "stdout": "", "stderr": f"unknown MCP supervisor backend: {backend}"}
    after = _mcp_supervisor_state(agent_id, backend=backend)
    ok = True if action == "status" else (bool(after.get("active")) if action in {"start", "restart"} else not bool(after.get("active")))
    if result and result.get("returncode") not in {0, None}:
        ok = False
    if result and result.get("returncode") is None and action != "status":
        ok = False
    return {"ok": ok, "action": action, "backend": backend, "service_name": name, "service_file": service_file, "before": before, "after": after, "result": result, "supervisor": "rootd_cross_platform_facade"}

def _mcp_capabilities(include_status: bool = True) -> list:
    cfg_path = Path(os.getenv("GPTADMIN_MCP_CONFIG", os.getenv("ROOTD_MCP_CONFIG", "/etc/gptadmin/mcp.json")))
    agents_dir = Path(os.getenv("GPTADMIN_MCP_AGENTS_DIR", os.getenv("ROOTD_MCP_AGENTS_DIR", "/etc/gptadmin/mcp-agents.d")))
    cfg = _load_json_file(cfg_path)
    servers = cfg.get("mcpServers") if isinstance(cfg.get("mcpServers"), dict) else {}
    out = []
    for name, spec in sorted(servers.items()):
        if not isinstance(spec, dict):
            continue
        agent_id = str(spec.get("agent_id") or spec.get("name") or name)
        item = {
            "id": f"mcp:{agent_id}",
            "name": name,
            "agent_id": agent_id,
            "kind": "mcp",
            "role": "capability_executor",
            "hosted_by": ROOTD_NAME or socket.gethostname(),
            "supervised_by": "rootd",
            "legacy_service": _mcp_service_name(agent_id),
            "supervisor_backend": _mcp_supervisor_backend(),
            "service_file": _mcp_service_file(agent_id),
            "enabled": bool(spec.get("enabled", True)),
            "transport": "stdio_or_remote",
            "command": spec.get("command"),
            "args": _redact_public(spec.get("args") or []),
            "cwd": spec.get("cwd"),
            "run_as_user": spec.get("run_as_user") or spec.get("user"),
            "stdio_format": spec.get("stdio_format") or spec.get("transport") or "auto",
            "config_file": str(agents_dir / f"{name}.json"),
            "migration_state": "legacy_relay_supervised; rootd_registry_visible",
        }
        if include_status:
            item["supervisor"] = _mcp_supervisor_state(agent_id)
        out.append(item)
    return out


def _capability_registry(include_status: bool = True) -> dict:
    mcp = _mcp_capabilities(include_status=include_status)
    return {
        "ok": True,
        "schema_version": 1,
        "host": ROOTD_NAME or socket.gethostname(),
        "server_id": ROOTD_SERVER_ID,
        "transport_role": "rootd_transport_layer",
        "capability_host": True,
        "capabilities": [
            {"id": "shell", "kind": "shell", "role": "local_executor", "hosted_by": ROOTD_NAME or socket.gethostname()},
            {"id": "tasks", "kind": "task_store", "role": "durable_queue_view", "hosted_by": ROOTD_NAME or socket.gethostname()},
            {"id": "logs", "kind": "logs", "role": "diagnostics", "hosted_by": ROOTD_NAME or socket.gethostname()},
            {"id": "system", "kind": "system", "role": "host_introspection", "hosted_by": ROOTD_NAME or socket.gethostname()},
            *mcp,
        ],
        "summary": {"mcp_count": len(mcp), "enabled_mcp_count": sum(1 for x in mcp if x.get("enabled"))},
    }


@app.get("/capabilities", dependencies=[Depends(guard)])
def capabilities(include_status: bool = True):
    return _capability_registry(include_status=include_status)


@app.get("/capabilities/mcp", dependencies=[Depends(guard)])
def capabilities_mcp(include_status: bool = True):
    return {"ok": True, "host": ROOTD_NAME or socket.gethostname(), "capabilities": _mcp_capabilities(include_status=include_status)}

def _find_mcp_capability(ref: str) -> Optional[dict]:
    ref_norm = (ref or "").strip()
    if not ref_norm:
        return None
    if ref_norm.startswith("mcp:"):
        ref_norm = ref_norm[4:]
    for item in _mcp_capabilities(include_status=False):
        if ref_norm in {str(item.get("name")), str(item.get("agent_id")), str(item.get("id")), str(item.get("legacy_service"))}:
            return item
    return None


def _systemd_unit_action(unit: str, action: str) -> dict:
    action = (action or "status").strip().lower()
    allowed = {"status", "start", "stop", "restart"}
    if action not in allowed:
        raise HTTPException(400, f"unsupported action: {action}; expected one of {sorted(allowed)}")
    if not sys.platform.startswith("linux"):
        return {"ok": False, "error": "systemd lifecycle is supported only on linux for now", "unit": unit, "action": action}
    before = _systemd_unit_state(unit)
    result = None
    if action != "status":
        try:
            cp = subprocess.run(
                ["systemctl", action, unit],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=30,
            )
            result = {"returncode": cp.returncode, "stdout": cp.stdout[-4000:], "stderr": cp.stderr[-4000:]}
        except Exception as e:
            result = {"returncode": None, "stdout": "", "stderr": str(e)}
    after = _systemd_unit_state(unit)
    ok = bool(after.get("active")) if action in {"start", "restart"} else (not bool(after.get("active")) if action == "stop" else True)
    if result and result.get("returncode") not in {0, None}:
        ok = False
    return {"ok": ok, "action": action, "unit": unit, "before": before, "after": after, "result": result, "supervisor": "rootd_systemd_facade"}


@app.post("/capabilities/mcp/{mcp_ref}/lifecycle", dependencies=[Depends(guard)])
def capabilities_mcp_lifecycle(mcp_ref: str, payload: dict = Body(default_factory=dict)):
    action = str((payload or {}).get("action") or "status").strip().lower()
    cap = _find_mcp_capability(mcp_ref)
    if not cap:
        raise HTTPException(404, f"MCP capability not found: {mcp_ref}")
    backend = str((payload or {}).get("backend") or cap.get("supervisor_backend") or _mcp_supervisor_backend()).strip().lower()
    res = _mcp_supervisor_action(str(cap.get("agent_id")), action, backend=backend)
    res.update({"capability": cap, "host": ROOTD_NAME or socket.gethostname(), "server_id": ROOTD_SERVER_ID})
    return res

def get_local_ip():

    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.connect(("8.8.8.8", 80))  # можно любой внешний IP
    ip = s.getsockname()[0]
    s.close()

    return ip or socket.gethostname()+'.local'


def _hub_ws_url() -> str:
    parsed = urlparse(HUB_URL)
    scheme = "wss" if parsed.scheme == "https" else "ws"
    return urlunparse((scheme, parsed.netloc, "/ws/rootd", "", "", ""))


def _beat_payload(mode: str):
    try:
        info = backend.info()
    except Exception as e:
        log.warning(f"Failed to get system info: {e}")
        info = {}
    return {
        "name": ROOTD_NAME or info.get("host", socket.gethostname()),
        "server_id": ROOTD_SERVER_ID,
        "public_key": ROOTD_PUBLIC_KEY_B64,
        "fingerprint": ROOTD_FINGERPRINT,
        "base_url": ROOTD_URL or f"http://{get_local_ip()}:{port}",
        "cores": info.get("cores"),
        "mem_mb": info.get("mem_mb"),
        "time": int(time.time()),
        "mode": "long_poll" if mode == "polling" and QUEUE_IS_LONG_POLL else mode,
        "queue_transport": QUEUE_TRANSPORT if mode == "polling" else None,
        "transport_role": "rootd_transport_layer",
        "capability_host": True,
        "capability_model": "rootd transports hub jobs to local executors/capabilities",
        "local_capabilities": ["shell", "tasks", "logs", "system", "mcp_supervision"],
        "capability_registry": _capability_registry(include_status=False).get("summary"),
        "os": info.get("platform", sys.platform),
        "version": BUILD_VERSION,
        "build_version": BUILD_VERSION,
        "build_ts": BUILD_TS,
        "git_commit": GIT_COMMIT,
        "default_cwd": os.getcwd(),
        "backend": ROOTD_BACKEND,
        "proxy_for": ROOTD_PROXY_FOR,
        "proxy_via": ROOTD_PROXY_VIA,
        "ssh_host": os.getenv("SSH_HOST"),
        "ssh_port": os.getenv("SSH_PORT"),
        "ssh_user": os.getenv("SSH_USER"),
        **_outbox_metrics(),
    }


async def websocket_loop():
    if not HUB_URL or websockets is None:
        if websockets is None:
            log.warning("websocket transport requested but websockets package is unavailable")
        return
    ws_url = _hub_ws_url()
    while True:
        try:
            log.info(f"websocket: connecting to {ws_url}")
            async with websockets.connect(ws_url, ping_interval=30, ping_timeout=20, max_size=None) as ws:
                await ws.send(json.dumps({"type": "hello", "payload": _beat_payload("websocket")}))
                ack = json.loads(await ws.recv())
                if not ack.get("ok"):
                    raise RuntimeError(f"websocket hello rejected: {ack}")
                log.info("websocket: connected")

                async def hb_sender():
                    while True:
                        await asyncio.sleep(HB_INT)
                        await ws.send(json.dumps({"type": "heartbeat", "time": int(time.time())}))

                hb_task = asyncio.create_task(hb_sender())
                try:
                    async for raw in ws:
                        msg = json.loads(raw)
                        if msg.get("type") != "exec":
                            continue
                        tid = msg.get("id")
                        payload = msg.get("payload") or {}
                        log.info(f"WS_EXEC: {payload.get('cmd')} (cwd={payload.get('cwd')})")
                        env = os.environ.copy()
                        if payload.get("env"):
                            env.update(payload["env"])
                        started = time.perf_counter()
                        try:
                            result = backend.run(payload["cmd"], payload.get("timeout"), payload.get("cwd"), env)
                            _audit_exec("websocket", payload.get("cmd"), payload.get("cwd"), payload.get("timeout"), result=result, job_id=tid, started_at=started)
                        except Exception as e:
                            log.exception("websocket exec failed")
                            result = {"error": str(e), "traceback": traceback.format_exc()}
                            _audit_exec("websocket", payload.get("cmd"), payload.get("cwd"), payload.get("timeout"), error=str(e), job_id=tid, started_at=started)
                        await ws.send(json.dumps({"type": "result", "id": tid, "result": result}))
                finally:
                    hb_task.cancel()
        except Exception as e:
            log.warning(f"websocket: disconnected/failed: {e}")
            await asyncio.sleep(5)


def start_websocket_thread():
    def runner():
        asyncio.run(websocket_loop())
    threading.Thread(target=runner, daemon=True).start()

# ---------------- HEARTBEAT ---------------------------------------
def heartbeat():
    if not HEARTBEAT_URL:
        log.warning("HEARTBEAT_URL not set, skipping heartbeat")
        return
    while True:
        payload = _beat_payload("polling" if QUEUE_URL else "webhook")
        try:
            body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
            requests.post(HEARTBEAT_URL, data=body, headers=_signed_json_headers("POST", "/heartbeat", body), timeout=3)
        except Exception as e:
            log.warning(f"Heartbeat failed: {e}")
        time.sleep(HB_INT)


def _send_queue_progress(srv_name: str, job_id: str, event_type: str, data: str = "", event: Optional[dict] = None, seq: Optional[int] = None, offset: Optional[int] = None) -> None:
    payload = {"id": job_id, "type": event_type, "data": data, "event": event}
    if seq is not None:
        payload["seq"] = int(seq)
    if offset is not None:
        payload["offset"] = int(offset)
    _queue_send_or_spool(
        srv_name,
        "progress",
        payload,
        kind=f"progress-{event_type}",
        job_id=job_id,
        timeout=1,
    )



async def _poll_run_live_job(srv_name: str, job: dict, env: dict) -> dict:
    stdout = ""
    stderr = ""
    seq = {"stdout": 0, "stderr": 0}
    offsets = {"stdout": 0, "stderr": 0}
    result = {"returncode": None, "stdout": stdout, "stderr": stderr}
    generator = await backend.run_live(job["cmd"], job.get("timeout"), job.get("cwd"), env)
    async for event in generator():
        etype = event.get("type")
        if etype == "stdout":
            chunk = str(event.get("data") or "")
            seq["stdout"] += 1
            offset = offsets["stdout"]
            stdout += chunk
            offsets["stdout"] += len(chunk)
            _send_queue_progress(srv_name, str(job.get("id") or ""), "stdout", chunk, seq=seq["stdout"], offset=offset)
        elif etype == "stderr":
            chunk = str(event.get("data") or "")
            seq["stderr"] += 1
            offset = offsets["stderr"]
            stderr += chunk
            offsets["stderr"] += len(chunk)
            _send_queue_progress(srv_name, str(job.get("id") or ""), "stderr", chunk, seq=seq["stderr"], offset=offset)
        elif etype == "exit":
            result.update(event)
        elif etype == "error":
            result.update({"returncode": -1, "error": event.get("error"), "traceback": event.get("traceback")})
    result["stdout"] = stdout
    result["stderr"] = stderr
    if result.get("returncode") is None:
        result["returncode"] = 0
    return result


def poll_loop():
    if not QUEUE_URL:
        return
    while True:
        try:
            srv_name = ROOTD_NAME or socket.gethostname()
            queue_path = f"/queue/{srv_name}"
            queue_url = f"{QUEUE_URL}/{srv_name}"
            if QUEUE_IS_LONG_POLL:
                queue_url += f"?timeout={max(1, QUEUE_LONG_POLL_TIMEOUT_S)}"
            r = requests.get(
                queue_url,
                headers=_signed_json_headers("GET", queue_path, b""),
                timeout=QUEUE_HTTP_TIMEOUT_S,
            )
            if r.status_code == 200:
                job = r.json()
                if job.get("cmd"):
                    log.info(f"POLL: {job['cmd']} (cwd={job.get('cwd')})")
                    env = os.environ.copy()
                    if job.get("env"):
                        env.update(job["env"])
                    started = time.perf_counter()
                    try:
                        if hasattr(backend, "run_live"):
                            res = asyncio.run(_poll_run_live_job(srv_name, job, env))
                        else:
                            res = backend.run(job["cmd"], job.get("timeout"), job.get("cwd"), env)
                        _audit_exec("polling", job.get("cmd"), job.get("cwd"), job.get("timeout"), result=res, job_id=job.get("id"), started_at=started)
                    except Exception as e:
                        log.exception("poll exec failed")
                        res = {"error": str(e), "traceback": traceback.format_exc()}
                        _audit_exec("polling", job.get("cmd"), job.get("cwd"), job.get("timeout"), error=str(e), job_id=job.get("id"), started_at=started)
                    _queue_send_or_spool(
                        srv_name,
                        "result",
                        {"id": job.get("id"), "result": res},
                        kind="result",
                        job_id=str(job.get("id") or ""),
                        timeout=5,
                    )
                else:
                    log.debug("poll: no jobs")
            else:
                log.warning(f"Unexpected status {r.status_code}")
        except Exception as e:
            log.warning(f"Poll failed: {e}")
            time.sleep(POLL_INT)
            continue
        if not QUEUE_IS_LONG_POLL:
            time.sleep(POLL_INT)


if HUB_URL and TRANSPORT in {"auto", "websocket"} and not QUEUE_URL:
    start_websocket_thread()
if HUB_URL and (TRANSPORT == "webhook" or QUEUE_URL or websockets is None):
    threading.Thread(target=heartbeat, daemon=True).start()
if CALLBACK_QUEUE_URL:
    threading.Thread(target=outbox_retry_loop, daemon=True).start()
if QUEUE_URL or TRANSPORT == "polling":
    threading.Thread(target=poll_loop, daemon=True).start()


@app.get("/version")
def version():
    data = build_info("rootd")
    data.update({
        "transport": TRANSPORT,
        "deprecated": PYTHON_SHELLMCP_DEPRECATED,
        "deprecation_notice": "legacy Python shellmcp/rootd is deprecated; use go-shellmcp/rootd-go-canary",
        "replacement": PYTHON_SHELLMCP_REPLACEMENT,
        "queue_transport": QUEUE_TRANSPORT if QUEUE_URL else None,
        "queue_long_poll_timeout_s": QUEUE_LONG_POLL_TIMEOUT_S if QUEUE_URL and QUEUE_IS_LONG_POLL else None,
        "rootd_url": ROOTD_URL or f"http://{get_local_ip()}:{port}",
        "name": ROOTD_NAME or socket.gethostname(),
        "server_id": ROOTD_SERVER_ID,
        "fingerprint": ROOTD_FINGERPRINT,
        "public_key": ROOTD_PUBLIC_KEY_B64,
        "backend": ROOTD_BACKEND,
        "proxy_for": ROOTD_PROXY_FOR,
        "proxy_via": ROOTD_PROXY_VIA,
        "ssh_host": os.getenv("SSH_HOST"),
        "ssh_port": os.getenv("SSH_PORT"),
        "ssh_user": os.getenv("SSH_USER"),
        "auto_update": ROOTD_AUTO_UPDATE,
        "update_manifest_url": ROOTD_UPDATE_MANIFEST_URL if ROOTD_AUTO_UPDATE else None,
        **_outbox_metrics(),
    })
    return data


def _sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def _find_rootd_binary(extract_dir: str) -> str:
    candidates = [
        Path(extract_dir) / "rootd" / "dist" / "rootd",
        Path(extract_dir) / "build" / "rootd" / "dist" / "rootd",
        Path(extract_dir) / "rootd",
    ]
    for c in candidates:
        if c.is_file():
            return str(c)
    for c in Path(extract_dir).rglob("rootd"):
        if c.is_file() and c.stat().st_size > 1024 * 1024:
            return str(c)
    raise RuntimeError("rootd binary not found in update archive")


def _current_source_root() -> Path:
    return Path(__file__).resolve().parent


def _is_source_install() -> bool:
    return Path(__file__).suffix == ".py" and "python" in Path(sys.executable).name.lower()


def _copy_tree_contents(src: Path, dst: Path) -> None:
    for item in src.iterdir():
        target = dst / item.name
        if item.is_dir():
            if target.exists():
                shutil.rmtree(target)
            shutil.copytree(item, target)
        else:
            shutil.copy2(item, target)


def _find_rootd_source_tree(extract_dir: str) -> Path:
    roots = [Path(extract_dir), Path(extract_dir) / "services" / "main_package" / "client", Path(extract_dir) / "client"]
    for root in roots:
        if (root / "rootd.py").is_file():
            return root
    for c in Path(extract_dir).rglob("rootd.py"):
        return c.parent
    raise RuntimeError("rootd.py source tree not found in update archive")


def _validate_source_tree(src: Path) -> None:
    required = ["rootd.py", "rootd_linux.py", "gptadmin_security.py", "gptadmin_build_info.py"]
    missing = [name for name in required if not (src / name).is_file()]
    if missing:
        raise RuntimeError(f"source update missing files: {missing}")
    subprocess.run([sys.executable, "-m", "py_compile", *[str(src / name) for name in required]], check=True, timeout=30)


def _canary_source_tree(src: Path) -> None:
    port_probe = str(35000 + (os.getpid() % 10000))
    env = os.environ.copy()
    env.update({"PORT": port_probe, "ROOTD_PORT": port_probe, "HUB_URL": "", "ROOTD_AUTO_UPDATE": "0", "ROOTD_TRANSPORT": "webhook"})
    proc = subprocess.Popen([sys.executable, str(src / "rootd.py")], cwd=str(src), env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        deadline = time.time() + 10
        ok = False
        while time.time() < deadline:
            try:
                r = requests.get(f"http://127.0.0.1:{port_probe}/version", timeout=1)
                if r.status_code == 200:
                    ok = True
                    break
            except Exception:
                time.sleep(0.2)
        if not ok:
            raise RuntimeError("source canary did not pass /version health check")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=3)
        except Exception:
            proc.kill()


def _install_source_update(extract_dir: str, latest: int) -> dict:
    src = _find_rootd_source_tree(extract_dir)
    _validate_source_tree(src)
    _canary_source_tree(src)
    dst = _current_source_root()
    backup = dst.with_name(dst.name + f".bak.{BUILD_VERSION}.{int(time.time())}")
    ignore = shutil.ignore_patterns("__pycache__", "*.pyc", "*.log")
    shutil.copytree(dst, backup, ignore=ignore)
    _copy_tree_contents(src, dst)
    log.info("rootd source auto-update staged build %s over %s, backup=%s", latest, BUILD_VERSION, backup)
    subprocess.Popen(["/bin/sh", "-c", f"sleep 1; systemctl restart {ROOTD_SERVICE_NAME}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return {"ok": True, "updated": True, "mode": "source", "previous": BUILD_VERSION, "latest": latest, "backup": str(backup)}


def _schedule_restart_after_update() -> None:
    if ROOTD_RESTART_CMD:
        cmd = f"sleep 1; {ROOTD_RESTART_CMD}"
    elif sys.platform == "darwin":
        label = ROOTD_SERVICE_NAME or "com.gptadmin.rootd"
        uid = os.getuid() if hasattr(os, "getuid") else 0
        if ROOTD_SERVICE_SCOPE == "user":
            cmd = f"sleep 1; launchctl kickstart -k gui/{uid}/{label} || launchctl unload ~/Library/LaunchAgents/{label}.plist; launchctl load -w ~/Library/LaunchAgents/{label}.plist"
        else:
            cmd = f"sleep 1; launchctl kickstart -k system/{label} || launchctl unload /Library/LaunchDaemons/{label}.plist; launchctl load -w /Library/LaunchDaemons/{label}.plist"
    elif os.name == "nt":
        # Windows package normally restarts via Scheduled Task wrapper after process exit.
        cmd = "ping -n 2 127.0.0.1 >NUL & exit"
    else:
        if ROOTD_SERVICE_SCOPE == "user":
            cmd = f"sleep 1; systemctl --user restart {ROOTD_SERVICE_NAME} || kill -TERM {os.getpid()}"
        else:
            cmd = f"sleep 1; systemctl restart {ROOTD_SERVICE_NAME} || kill -TERM {os.getpid()}"
    subprocess.Popen(["/bin/sh", "-c", cmd], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def rootd_update_once() -> dict:
    if not ROOTD_UPDATE_MANIFEST_URL:
        return {"ok": False, "reason": "no manifest url"}
    manifest = requests.get(ROOTD_UPDATE_MANIFEST_URL, timeout=15, headers=_update_headers()).json()
    latest = int(manifest.get("build_version") or manifest.get("version") or 0)
    if latest <= BUILD_VERSION:
        return {"ok": True, "updated": False, "current": BUILD_VERSION, "latest": latest}
    target_platform = str(manifest.get("platform") or "linux").lower()
    target_arch = str(manifest.get("arch") or "x86_64").lower()
    current_platform = _platform_id()
    current_arch = _arch_id()
    if target_platform != current_platform or target_arch != current_arch:
        return {"ok": False, "updated": False, "current": BUILD_VERSION, "latest": latest, "reason": "no compatible binary artifact", "artifact_platform": target_platform, "artifact_arch": target_arch, "platform": current_platform, "arch": current_arch}
    url = _resolve_update_url(manifest.get("url"), ROOTD_UPDATE_URL)
    expected_sha = (manifest.get("sha256") or "").lower().strip()
    if not url or not expected_sha:
        raise RuntimeError("manifest must include url and sha256")
    current_exe = Path(sys.executable).resolve()
    with tempfile.TemporaryDirectory(prefix="shellmcp-update-") as td:
        archive = Path(td) / "rootd.tar.gz"
        with requests.get(url, timeout=60, stream=True, headers=_update_headers()) as r:
            r.raise_for_status()
            with open(archive, "wb") as f:
                for chunk in r.iter_content(1024 * 1024):
                    if chunk:
                        f.write(chunk)
        actual_sha = _sha256_file(str(archive))
        if actual_sha.lower() != expected_sha:
            raise RuntimeError(f"sha256 mismatch: got {actual_sha}, expected {expected_sha}")
        extract_dir = Path(td) / "x"
        extract_dir.mkdir()
        with tarfile.open(archive, "r:gz") as tf:
            tf.extractall(extract_dir)
        if _is_source_install():
            raise RuntimeError("source-installed rootd cannot self-update from public artifacts; reinstall the binary package")
        new_bin = Path(_find_rootd_binary(str(extract_dir)))
        backup = current_exe.with_name(current_exe.name + f".bak.{BUILD_VERSION}")
        shutil.copy2(current_exe, backup)
        shutil.copy2(new_bin, current_exe)
        current_exe.chmod(0o755)
    log.info("rootd binary auto-update installed build %s over %s, backup=%s", latest, BUILD_VERSION, backup)
    _schedule_restart_after_update()
    return {"ok": True, "updated": True, "mode": "binary", "previous": BUILD_VERSION, "latest": latest, "backup": str(backup), "restart": "scheduled"}


@app.post("/update/check", dependencies=[Depends(guard)])
def update_check():
    return rootd_update_once()


def auto_update_loop():
    if not ROOTD_AUTO_UPDATE:
        return
    while True:
        try:
            res = rootd_update_once()
            log.info("auto_update: %s", res)
        except Exception as e:
            log.warning("auto_update failed: %s", e)
        time.sleep(ROOTD_UPDATE_INTERVAL_S)


if ROOTD_AUTO_UPDATE:
    threading.Thread(target=auto_update_loop, daemon=True).start()


@app.get("/file")
def get_file(path: str):
    if not os.path.isfile(path):
        raise HTTPException(404, "File not found")
    return FileResponse(path)


def _polling_foreground_loop() -> None:
    log.info("rootd polling mode: HTTP listener disabled; waiting for queue jobs")
    while True:
        time.sleep(3600)


if __name__ == "__main__":
    if QUEUE_URL or TRANSPORT == "polling":
        _polling_foreground_loop()
    else:
        import uvicorn, os
        # Внутри PyInstaller и в обычном питоне одинаково работает:
        uvicorn.run(app, host="0.0.0.0", port=port, log_config=None)


