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

import psutil, requests
from fastapi import FastAPI, Body, Query, Depends, HTTPException
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
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

def _run(cmd: List[str], timeout: int | None = None, cwd: str | None = None):
    log.debug(f"Running command: {' '.join(cmd)} (timeout={timeout}, cwd={cwd})")
    try:
        res = subprocess.run(
            cmd, cwd=cwd, text=True, timeout=timeout or TMO_DEF,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
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
    cmd: List[str]
    cwd: Optional[str] = None
    timeout: Optional[int] = None

class FileWrite(BaseModel):
    content: str
    mode: Literal["w", "a", "wb", "ab"] = "w"

class PkgReq(BaseModel):
    action: Literal["install", "uninstall", "list"]
    pkgs: Optional[List[str]] = None
    flags: Optional[List[str]] = None

class VenvReq(BaseModel):
    path: str
    python: Optional[str] = None  # e.g. /usr/bin/python3.12

class VenvExec(BaseModel):
    path: str
    cmd: List[str]
    timeout: Optional[int] = None
    cwd: Optional[str] = None

# ---------------- EXEC --------------------------------------------
@app.post("/exec", dependencies=[Depends(guard)])
def exec_cmd(body: ExecReq = Body(...)):
    log.info(f"EXEC: {body.cmd} (cwd={body.cwd})")
    try:
        return _run(body.cmd, body.timeout, body.cwd)
    except Exception as e:
        log.exception("Error in /exec")
        raise

# ---------------- FILES / DIRS ------------------------------------
@app.get("/file", dependencies=[Depends(guard)])
def file_read(path: str = Query(...)):
    fp = Path(path)
    if not fp.exists(): raise HTTPException(404, "no such file")
    if fp.is_dir():     raise HTTPException(400, "is dir")
    return {"content": fp.read_text(errors="replace")}

@app.put("/file", dependencies=[Depends(guard)])
def file_write(path: str = Query(...), body: FileWrite = Body(...)):
    fp = Path(path); fp.parent.mkdir(parents=True, exist_ok=True)
    with fp.open(body.mode) as f: f.write(body.content)
    return {"ok": True, "size": fp.stat().st_size}

@app.delete("/file", dependencies=[Depends(guard)])
def file_del(path: str = Query(...)):
    fp = Path(path)
    if fp.is_file(): fp.unlink(); return {"deleted": True}
    raise HTTPException(404, "not file")

@app.get("/dir", dependencies=[Depends(guard)])
def dir_ls(path: str = Query(".")):
    dp = Path(path)
    if not dp.is_dir(): raise HTTPException(404, "no dir")
    return {"entries": [
        {"name": p.name, "dir": p.is_dir(), "size": None if p.is_dir() else p.stat().st_size}
        for p in dp.iterdir()
    ]}

# ---------------- SYSTEMD -----------------------------------------
@app.get("/systemd/units", dependencies=[Depends(guard)])
def units():
    return _run(["systemctl", "list-units", "--type=service", "--no-pager"])

@app.post("/systemd/unit/{name}", dependencies=[Depends(guard)])
def unit_ctl(name: str, action: Literal["start","stop","restart","status"] = Body(...)):
    return _run(["systemctl", action, name])

@app.get("/systemd/log", dependencies=[Depends(guard)])
def unit_log(
    unit: str = Query(...),
    grep: Optional[str] = Query(None),
    max_lines: int = Query(100)
):
    base_cmd = ["journalctl", "-u", unit, "-n", "5000", "--no-pager"]
    
    try:
        raw = subprocess.run(
            base_cmd,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=10
        )
    except subprocess.TimeoutExpired as e:
        return {"error": f"timeout {e.timeout}s", "stdout": _truncate(e.stdout or ""), "stderr": _truncate(e.stderr or "")}

    lines = raw.stdout.splitlines()

    if grep:
        matching_lines = [line for line in lines if grep in line]
        output_lines = matching_lines[-max_lines:]  # ← последние N
    else:
        output_lines = lines[-max_lines:]  # ← без фильтра тоже последние N

    return {
        "returncode": raw.returncode,
        "stdout": _truncate("\n".join(output_lines)),
        "stderr": _truncate(raw.stderr),
    }

# ---------------- VENV --------------------------------------------
@app.post("/venv/create", dependencies=[Depends(guard)])
def venv_create(body: VenvReq = Body(...)):
    path = Path(body.path)
    if path.exists(): return {"exists": True}
    cmd = [body.python or "python3", "-m", "venv", str(path)]
    return _run(cmd)

@app.post("/venv/pip", dependencies=[Depends(guard)])
def venv_pip(req: PkgReq = Body(...), path: str = Query(...)):
    pip = Path(path)/"bin/pip"
    if not pip.exists(): raise HTTPException(404, "venv not found")
    cmd = [str(pip), req.action] + (req.pkgs or []) + (req.flags or [])
    return _run(cmd)

@app.post("/venv/exec", dependencies=[Depends(guard)])
def venv_exec(body: VenvExec = Body(...)):
    activate = Path(body.path)/"bin/activate"
    if not activate.exists(): raise HTTPException(404, "venv not found")
    sh_cmd = f"source {activate} && {' '.join(map(shlex.quote, body.cmd))}"
    return _run(["bash","-c",sh_cmd], body.timeout, body.cwd)

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

    # Memory
    vm = psutil.virtual_memory()
    swap = psutil.swap_memory()

    # Load average
    try:
        load = os.getloadavg()
    except:
        load = []

    # Temperature
    temp = psutil.sensors_temperatures()
    cpu_temp = None
    for sensor in temp.values():
        for entry in sensor:
            if "cpu" in entry.label.lower() or "package" in entry.label.lower():
                cpu_temp = entry.current
                break

    # IP address
    ip = None
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
