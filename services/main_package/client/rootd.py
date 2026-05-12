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
"""

import os
import time
import json
import asyncio
import threading
import socket
import sys
from typing import Optional

import requests
try:
    import websockets
except Exception:
    websockets = None
from fastapi import FastAPI, Body, Depends, HTTPException
from fastapi.responses import JSONResponse, FileResponse
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from starlette.responses import StreamingResponse
from pydantic import BaseModel
import logging
import traceback
from urllib.parse import urlparse, urlunparse

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
# ------------------------------------------------------------------

TOKEN = os.getenv("ROOTD_TOKEN", "srv_secret")
HUB_URL = os.getenv("HUB_URL", 'https://gptadmin.bezrabotnyi.com/')
HEARTBEAT_URL=HUB_URL+'/heartbeat' if '/heartbeat' not in HUB_URL else HUB_URL
ROOTD_URL = os.getenv("ROOTD_URL")
TRANSPORT = os.getenv("ROOTD_TRANSPORT", "webhook").lower()
HB_INT = int(os.getenv("HB_INTERVAL_S", "60"))

if os.getenv("QUEUE_URL"):
    parsed = urlparse(HUB_URL)
    QUEUE_URL = urlunparse((parsed.scheme, parsed.netloc, "/queue", "", "", ""))
else:
    QUEUE_URL = None

POLL_INT = int(os.getenv("POLL_INTERVAL_S", "5"))

app = FastAPI(title="rootd", version="2.0")
auth = HTTPBearer(auto_error=False)


def guard(cred: HTTPAuthorizationCredentials = Depends(auth)):
    if not cred or cred.scheme.lower() != "bearer" or cred.credentials != TOKEN:
        raise HTTPException(401, "bad token")


if os.getenv("SSH_HOST"):
    import rootd_ssh as backend
elif sys.platform.startswith("win"):
    import rootd_win as backend
else:
    import rootd_linux as backend


class ExecReq(BaseModel):
    cmd: str
    env: Optional[dict] = None
    cwd: Optional[str] = None
    timeout: Optional[int] = None


# ---------------- EXEC --------------------------------------------
@app.post("/exec", dependencies=[Depends(guard)])
def exec_cmd(body: ExecReq = Body(...)):
    log.info(f"EXEC: {body.cmd} (cwd={body.cwd})")

    env = os.environ.copy()
    if body.env:
        env.update(body.env)

    try:
        return backend.run(body.cmd, body.timeout, body.cwd, env)
    except Exception as e:
        log.exception("Error in /exec")
        return JSONResponse(
            status_code=500,
            content={"error": str(e), "traceback": traceback.format_exc()},
        )


@app.post("/exec/stream", dependencies=[Depends(guard)])
async def exec_stream(body: ExecReq = Body(...)):
    """Run command and stream combined stdout/stderr."""
    log.info(f"EXEC_STREAM: {body.cmd} (cwd={body.cwd})")

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
        "name": info.get("host", socket.gethostname()),
        "base_url": ROOTD_URL or f"http://{get_local_ip()}:{port}",
        "rootd_token": TOKEN,
        "cores": info.get("cores"),
        "mem_mb": info.get("mem_mb"),
        "time": int(time.time()),
        "mode": mode,
        "os": info.get("platform", sys.platform)
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
                        try:
                            result = backend.run(payload["cmd"], payload.get("timeout"), payload.get("cwd"), env)
                        except Exception as e:
                            log.exception("websocket exec failed")
                            result = {"error": str(e), "traceback": traceback.format_exc()}
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
            requests.post(HEARTBEAT_URL, json=payload, timeout=3)
        except Exception as e:
            log.warning(f"Heartbeat failed: {e}")
        time.sleep(HB_INT)


def poll_loop():
    if not QUEUE_URL:
        return
    while True:
        try:
            r = requests.get(
                f"{QUEUE_URL}/{socket.gethostname()}",
                params={"token": TOKEN},
                timeout=5,
            )
            if r.status_code == 200:
                job = r.json()
                if job.get("cmd"):
                    log.info(f"POLL: {job['cmd']} (cwd={job.get('cwd')})")
                    env = os.environ.copy()
                    if job.get("env"):
                        env.update(job["env"])
                    res = backend.run(job["cmd"], job.get("timeout"), job.get("cwd"), env)
                    try:
                        requests.post(
                            f"{QUEUE_URL}/{socket.gethostname()}/result",
                            params={"token": TOKEN},
                            json={"id": job.get("id"), "result": res},
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


@app.get("/file")
def get_file(path: str):
    if not os.path.isfile(path):
        raise HTTPException(404, "File not found")
    return FileResponse(path)


if __name__ == "__main__":
    import uvicorn, os
    
    # Внутри PyInstaller и в обычном питоне одинаково работает:
    uvicorn.run(app, host="0.0.0.0", port=port, log_config=None)


