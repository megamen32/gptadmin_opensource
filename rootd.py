# rootd.py
"""
Голый root-API без Docker, но с:
 • /exec                             – любая команда
 • /file, /dir                       – файлы/каталоги
 • /systemd/units, /systemd/unit     – статус и управление
 • /systemd/log                      – поиск по journalctl
 • /venv/create, /venv/pip, /venv/exec
 • heartbeat → HUB_URL (если задан)

Env:
  ROOTD_TOKEN     bearer-токен (def: CHANGE_ME)
  LOG_LIMIT_B     макс. байт stdout/stderr (def: 8192)
  EXEC_TIMEOUT    таймаут /exec (def: 300)
  HUB_URL         http(s)://hub:9001/heartbeat (def: none)
  HB_INTERVAL_S   период heartbeat (def: 60)
"""
import os, subprocess, time, threading, platform, socket, json, shlex
from pathlib import Path
from typing import List, Optional, Literal

import psutil, requests, asyncio
from fastapi import FastAPI, Body, Query, Depends, HTTPException
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from starlette.responses import StreamingResponse
from pydantic import BaseModel
import logging
import shutil

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
TOKEN   = os.getenv("ROOTD_TOKEN", "CHANGE_ME")
LOG_MAX = int(os.getenv("LOG_LIMIT_B", "8192"))
TMO_DEF = int(os.getenv("EXEC_TIMEOUT", "300"))

HUB_URL = os.getenv("HUB_URL")
ROOTD_URL = os.getenv("ROOTD_URL")
HB_INT  = int(os.getenv("HB_INTERVAL_S", "60"))

app = FastAPI(title="rootd", version="2.0")
auth = HTTPBearer(auto_error=False)

def guard(cred: HTTPAuthorizationCredentials = Depends(auth)):
    if not cred or cred.scheme.lower() != "bearer" or cred.credentials != TOKEN:
        raise HTTPException(401, "bad token")

def _truncate(s: str) -> str:
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s

def _run(cmd: List[str], timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running command: {' '.join(cmd)} (timeout={timeout}, cwd={cwd})")
    try:
        res = subprocess.run(
            cmd,
            cwd=cwd,
            text=True,
            timeout=timeout or TMO_DEF,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
        )
        log.debug(f"Command finished: return={res.returncode}")
        return {
            "returncode": res.returncode,
            "stdout": _truncate(res.stdout),
            "stderr": _truncate(res.stderr),
        }
    except subprocess.TimeoutExpired as e:
        log.warning(f"Command timeout after {e.timeout}s: {' '.join(cmd)}")
        return {"error": f"timeout {e.timeout}s", "stdout": _truncate(e.stdout or ""), "stderr": _truncate(e.stderr or "")}
    except Exception as e:
        log.exception(f"Exception during command: {' '.join(cmd)}")
        raise

# ------------------------------------------------------------------
class ExecReq(BaseModel):
    cmd: str
    cwd: Optional[str] = None
    timeout: Optional[int] = None


# ---------------- EXEC --------------------------------------------
@app.post("/exec", dependencies=[Depends(guard)])
def exec_cmd(body: ExecReq = Body(...)):
    log.info(f"EXEC: {body.cmd} (cwd={body.cwd})")

    parts = shlex.split(body.cmd)
    env_vars = {}
    cmd_parts = []
    for p in parts:
        if not cmd_parts and '=' in p and not p.startswith('='):
            key, val = p.split('=', 1)
            env_vars[key] = val
        else:
            cmd_parts.append(p)

    env = os.environ.copy()
    if env_vars:
        env.update(env_vars)

    try:
        return _run(cmd_parts, body.timeout, body.cwd, env)
    except Exception as e:
        log.exception("Error in /exec")
        raise


@app.post("/exec/stream", dependencies=[Depends(guard)])
async def exec_stream(body: ExecReq = Body(...)):
    """Run command and stream combined stdout/stderr."""
    log.info(f"EXEC_STREAM: {body.cmd} (cwd={body.cwd})")

    parts = shlex.split(body.cmd)
    env_vars = {}
    cmd_parts = []
    for p in parts:
        if not cmd_parts and '=' in p and not p.startswith('='):
            key, val = p.split('=', 1)
            env_vars[key] = val
        else:
            cmd_parts.append(p)

    env = os.environ.copy()
    if env_vars:
        env.update(env_vars)

    proc = await asyncio.create_subprocess_exec(
        *cmd_parts,
        cwd=body.cwd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
        env=env,
    )

    async def generator():
        assert proc.stdout
        async for chunk in proc.stdout:
            yield chunk
        await proc.wait()

    return StreamingResponse(generator(), media_type="text/plain")




# ---------------- INFO --------------------------------------------
@app.get("/system/info", dependencies=[Depends(guard)])
def sys_info():
    return {
        "host": socket.gethostname(),
        "kernel": platform.release(),
        "cores": psutil.cpu_count(),
        "mem_mb": round(psutil.virtual_memory().total/2**20),
        "uptime_s": round(time.time()-psutil.boot_time()),
    }
# rootd.py
# ... [existing imports above] ...
import shutil

@app.get("/system/health", dependencies=[Depends(guard)])
def health():
    # Disk usage
    du = shutil.disk_usage("/")

    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
    except:
        ip = "unavailable"

    # Failed services
    result = subprocess.run(["systemctl", "list-units", "--state=failed", "--no-pager", "--plain", "--no-legend"], text=True, stdout=subprocess.PIPE)
    failed_services = [line.split()[0] for line in result.stdout.splitlines() if line.strip() and ".service" in line.split()[0]]

    # APT last update
    apt_time = None
    stamp = Path("/var/lib/apt/periodic/update-success-stamp")
    if stamp.exists():
        apt_time = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(stamp.stat().st_mtime))

    return {
        "uptime_s": round(time.time() - psutil.boot_time()),
        "load_avg": load,
        "cpu_usage_pct": psutil.cpu_percent(interval=1),
        "memory": {
            "total": round(vm.total / 2**20),
            "available": round(vm.available / 2**20),
            "used": round(vm.used / 2**20),
            "free": round(vm.free / 2**20),
        },
        "swap": {
            "total": round(swap.total / 2**20),
            "used": round(swap.used / 2**20),
            "free": round(swap.free / 2**20),
        },
        "disk": {
            "total": round(du.total / 2**30, 2),
            "used": round(du.used / 2**30, 2),
            "free": round(du.free / 2**30, 2),
        },
        "failed_services": failed_services,
        "last_apt_update": apt_time,
        "cpu_temperature": cpu_temp,
        "ip_address": ip
    }



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
        }
        try:
            res = requests.post(HUB_URL, json=payload, timeout=3)
            #log.debug(f"Heartbeat sent to HUB ({res.status_code})")
        except Exception as e:
            log.warning(f"Heartbeat failed: {e}")
        time.sleep(HB_INT)

if HUB_URL:
    threading.Thread(target=heartbeat, daemon=True).start()

# ---------------- MAIN --------------------------------------------
if __name__ == "__main__":
    import uvicorn
    uvicorn.run("rootd:app", host="0.0.0.0", port=25900)
