# rootd.py
"""
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
from urllib.parse import urlparse, urlunparse

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

port = int(os.getenv("ROOTD_PORT", os.getenv("PORT","25900")))
# --- логирование ---
logging.basicConfig(
    level=logging.DEBUG,  # можно поставить INFO если слишком шумно
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
        logging.FileHandler(f"rootd-{port}.log"),    # лог в файл
        logging.StreamHandler()              # лог в stdout (для systemd)
    ]
)
log = logging.getLogger("rootd")
audit_log = logging.getLogger("rootd.audit")
audit_log.setLevel(logging.INFO)
audit_log.propagate = False
# ------------------------------------------------------------------

TOKEN = os.getenv("ROOTD_TOKEN", "srv_secret")
HUB_URL = os.getenv("HUB_URL", 'https://gptadmin.bezrabotnyi.com/')
HEARTBEAT_URL=HUB_URL+'/heartbeat' if '/heartbeat' not in HUB_URL else HUB_URL
ROOTD_URL = os.getenv("ROOTD_URL")
ROOTD_NAME = os.getenv("ROOTD_NAME")
ROOTD_PROXY_FOR = os.getenv("ROOTD_PROXY_FOR")
ROOTD_PROXY_VIA = os.getenv("ROOTD_PROXY_VIA")
ROOTD_BACKEND = os.getenv("ROOTD_BACKEND") or ("ssh" if os.getenv("SSH_HOST") else "local")
ROOTD_IDENTITY_DIR = os.getenv("ROOTD_IDENTITY_DIR") or ("/etc/gptadmin" if os.access("/etc", os.W_OK) else str(Path.home() / ".gptadmin"))
HUB_PUBLIC_KEY_FILE = os.getenv("HUB_PUBLIC_KEY_FILE", str(Path(ROOTD_IDENTITY_DIR) / "hub_ed25519.pub"))
HUB_PUBLIC_KEY_B64 = os.getenv("HUB_PUBLIC_KEY", "")
ROOTD_IDENTITY = load_or_create_identity(ROOTD_IDENTITY_DIR, ROOTD_NAME or socket.gethostname(), prefix="rootd")
ROOTD_SERVER_ID = ROOTD_IDENTITY["identity"]["server_id"]
ROOTD_PUBLIC_KEY_B64 = ROOTD_IDENTITY["public_key_b64"]
ROOTD_FINGERPRINT = ROOTD_IDENTITY["fingerprint"]
NONCES = NonceCache(ttl_s=int(os.getenv("ROOTD_NONCE_TTL_S", "300")))
TRANSPORT = os.getenv("ROOTD_TRANSPORT", "webhook").lower()
HB_INT = int(os.getenv("HB_INTERVAL_S", "60"))
ROOTD_AUTO_UPDATE = os.getenv("ROOTD_AUTO_UPDATE", "0").lower() in {"1", "true", "yes", "on"}
ROOTD_UPDATE_INTERVAL_S = int(os.getenv("ROOTD_UPDATE_INTERVAL_S", "3600"))
ROOTD_SERVICE_NAME = os.getenv("ROOTD_SERVICE_NAME", "gptadmin-rootd.service")

def _hub_artifact_url(path: str) -> str:
    base = (HUB_URL or "").rstrip("/")
    return f"{base}{path}" if base else ""

ROOTD_UPDATE_MANIFEST_URL = os.getenv("ROOTD_UPDATE_MANIFEST_URL") or _hub_artifact_url("/artifacts/rootd.json")
ROOTD_UPDATE_URL = os.getenv("ROOTD_UPDATE_URL") or _hub_artifact_url("/artifacts/rootd.tar.gz")
ROOTD_UPDATE_TOKEN = os.getenv("ROOTD_UPDATE_TOKEN", "")
ROOTD_AUDIT_LOG = os.getenv("ROOTD_AUDIT_LOG") or ("/var/log/gptadmin/rootd-audit.log" if os.access("/var/log", os.W_OK) else str(Path.home() / ".gptadmin" / "rootd-audit.log"))
try:
    _rootd_audit_path = Path(ROOTD_AUDIT_LOG)
    _rootd_audit_path.parent.mkdir(parents=True, exist_ok=True)
    _rootd_audit_handler = WatchedFileHandler(_rootd_audit_path)
    _rootd_audit_handler.setFormatter(logging.Formatter("%(message)s"))
    audit_log.addHandler(_rootd_audit_handler)
except Exception as e:
    log.warning("rootd audit log disabled path=%s err=%s", ROOTD_AUDIT_LOG, e)


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
        log.warning("rootd audit write failed: %s", e)


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
    event = {"event": "rootd_exec", "source": source, "job_id": job_id, "cmd": cmd, "cmd_sha256": _cmd_sha256(cmd), "cwd": cwd, "timeout": timeout}
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

if os.getenv("QUEUE_URL"):
    parsed = urlparse(HUB_URL)
    QUEUE_URL = urlunparse((parsed.scheme, parsed.netloc, "/queue", "", "", ""))
else:
    QUEUE_URL = None

POLL_INT = int(os.getenv("POLL_INTERVAL_S", "5"))

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
        "mode": mode,
        "os": info.get("platform", sys.platform),
        "version": BUILD_VERSION,
        "build_version": BUILD_VERSION,
        "build_ts": BUILD_TS,
        "git_commit": GIT_COMMIT,
        "backend": ROOTD_BACKEND,
        "proxy_for": ROOTD_PROXY_FOR,
        "proxy_via": ROOTD_PROXY_VIA,
        "ssh_host": os.getenv("SSH_HOST"),
        "ssh_port": os.getenv("SSH_PORT"),
        "ssh_user": os.getenv("SSH_USER"),
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


def poll_loop():
    if not QUEUE_URL:
        return
    while True:
        try:
            srv_name = ROOTD_NAME or socket.gethostname()
            queue_path = f"/queue/{srv_name}"
            r = requests.get(
                f"{QUEUE_URL}/{srv_name}",
                headers=_signed_json_headers("GET", queue_path, b""),
                timeout=5,
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
                        res = backend.run(job["cmd"], job.get("timeout"), job.get("cwd"), env)
                        _audit_exec("polling", job.get("cmd"), job.get("cwd"), job.get("timeout"), result=res, job_id=job.get("id"), started_at=started)
                    except Exception as e:
                        log.exception("poll exec failed")
                        res = {"error": str(e), "traceback": traceback.format_exc()}
                        _audit_exec("polling", job.get("cmd"), job.get("cwd"), job.get("timeout"), error=str(e), job_id=job.get("id"), started_at=started)
                    try:
                        result_path = f"/queue/{srv_name}/result"
                        result_body = json.dumps({"id": job.get("id"), "result": res}, separators=(",", ":")).encode("utf-8")
                        requests.post(
                            f"{QUEUE_URL}/{srv_name}/result",
                            data=result_body,
                            headers=_signed_json_headers("POST", result_path, result_body),
                            timeout=5,
                        )
                    except Exception as e:
                        log.warning(f"Result send failed: {e}")
                else:
                    log.debug("poll: no jobs")
            else:
                log.warning(f"Unexpected status {r.status_code}")
        except Exception as e:
            log.warning(f"Poll failed: {e}")
        time.sleep(POLL_INT)


if HUB_URL and TRANSPORT in {"auto", "websocket"} and not QUEUE_URL:
    start_websocket_thread()
if HUB_URL and (TRANSPORT == "webhook" or QUEUE_URL or websockets is None):
    threading.Thread(target=heartbeat, daemon=True).start()
if QUEUE_URL or TRANSPORT == "polling":
    threading.Thread(target=poll_loop, daemon=True).start()


@app.get("/version")
def version():
    data = build_info("rootd")
    data.update({
        "transport": TRANSPORT,
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


def rootd_update_once() -> dict:
    if not ROOTD_UPDATE_MANIFEST_URL:
        return {"ok": False, "reason": "no manifest url"}
    manifest = requests.get(ROOTD_UPDATE_MANIFEST_URL, timeout=15, headers=_update_headers()).json()
    latest = int(manifest.get("build_version") or manifest.get("version") or 0)
    if latest <= BUILD_VERSION:
        return {"ok": True, "updated": False, "current": BUILD_VERSION, "latest": latest}
    url = manifest.get("url") or ROOTD_UPDATE_URL
    expected_sha = (manifest.get("sha256") or "").lower().strip()
    if not url or not expected_sha:
        raise RuntimeError("manifest must include url and sha256")
    current_exe = Path(sys.executable).resolve()
    with tempfile.TemporaryDirectory(prefix="rootd-update-") as td:
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
        new_bin = Path(_find_rootd_binary(str(extract_dir)))
        backup = current_exe.with_name(current_exe.name + f".bak.{BUILD_VERSION}")
        shutil.copy2(current_exe, backup)
        shutil.copy2(new_bin, current_exe)
        current_exe.chmod(0o755)
    log.info("rootd auto-update installed build %s over %s, backup=%s", latest, BUILD_VERSION, backup)
    subprocess.Popen(["/bin/sh", "-c", f"sleep 1; systemctl restart {ROOTD_SERVICE_NAME}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return {"ok": True, "updated": True, "previous": BUILD_VERSION, "latest": latest, "backup": str(backup)}


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


if __name__ == "__main__":
    import uvicorn, os
    
    # Внутри PyInstaller и в обычном питоне одинаково работает:
    uvicorn.run(app, host="0.0.0.0", port=port, log_config=None)


