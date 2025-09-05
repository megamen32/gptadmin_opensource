# rootd.py
"""
Голый root-API без Docker, но с:
 • /exec                             – любая команда
 • heartbeat → HUB_URL (если задан)

Env:
  ROOTD_TOKEN     bearer-токен (def: CHANGE_ME)
  LOG_LIMIT_B     макс. байт stdout/stderr (def: 8192)
  EXEC_TIMEOUT    таймаут /exec (def: 300)
  HUB_URL         http(s)://hub:9001/heartbeat (def: none)
  HB_INTERVAL_S   период heartbeat (def: 60)
  QUEUE_URL       если задан – включаем polling и берём HUB_URL с заменой
                   пути на /queue
  POLL_INTERVAL_S период опроса QUEUE_URL (def: 5)
"""

import os
import time
import threading
import platform
import socket
import sys
from typing import Optional

import psutil
import requests
from fastapi import FastAPI, Body, Depends, HTTPException
from fastapi.responses import JSONResponse, FileResponse
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from starlette.responses import StreamingResponse
from pydantic import BaseModel
import logging
import traceback
from urllib.parse import urlparse, urlunparse


# --- логирование ---
logging.basicConfig(
    level=logging.DEBUG,  # можно поставить INFO если слишком шумно
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
        logging.FileHandler("rootd.log"),    # лог в файл
        logging.StreamHandler()              # лог в stdout (для systemd)
    ]
)
log = logging.getLogger("rootd")
# ------------------------------------------------------------------
TOKEN = os.getenv("ROOTD_TOKEN", "srv_secret")
HUB_URL = os.getenv("HUB_URL", 'https://gptadmin.bezrabotnyi.com/heartbeat')
ROOTD_URL = os.getenv("ROOTD_URL")
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


if sys.platform.startswith("win"):
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
    return {
        "host": socket.gethostname(),
        "platform": platform.platform(),
        "cores": psutil.cpu_count(),
        "mem_mb": round(psutil.virtual_memory().total / 2**20),
        "uptime_s": round(time.time() - psutil.boot_time()),
    }


@app.get("/system/health", dependencies=[Depends(guard)])
def health():
    return backend.health()


# ---------------- HEARTBEAT ---------------------------------------
def heartbeat():
    if not HUB_URL:
        log.warning("HUB_URL not set, skipping heartbeat")
        return
    while True:
        payload = {
            "name": socket.gethostname(),
            "base_url": ROOTD_URL or f"http://{socket.gethostname()}:25900",
            "rootd_token": TOKEN,
            "cores": psutil.cpu_count(),
            "mem_mb": round(psutil.virtual_memory().total / 2**20),
            "time": int(time.time()),
            "mode": "polling" if QUEUE_URL else "webhook",
            "os":sys.platform
        }
        try:
            requests.post(HUB_URL, json=payload, timeout=3)
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


if HUB_URL:
    threading.Thread(target=heartbeat, daemon=True).start()
if QUEUE_URL:
    threading.Thread(target=poll_loop, daemon=True).start()


@app.get("/file")
def get_file(path: str):
    if not os.path.isfile(path):
        raise HTTPException(404, "File not found")
    return FileResponse(path)


if __name__ == "__main__":
    import uvicorn, os
    port = int(os.getenv("ROOTD_PORT", "25900"))
    # Внутри PyInstaller и в обычном питоне одинаково работает:
    uvicorn.run(app, host="0.0.0.0", port=port, log_config=None)


