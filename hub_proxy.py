# hub_proxy.py
"""
Hub-прокси: принимает heartbeat'ы, держит карту <name → (base_url, rootd_token)>,
            а все клиентские вызовы /srv/{path}?server=name проксирует к нужному rootd.

Env:
  CTL_TOKEN   – Bearer-токен для ChatGPT-клиента      (def: CHANGE_ME)
  DEAD_S      – через сколько секунд считать сервер offline (def: 180)

Зависимости: fastapi, uvicorn[standard], httpx, pydantic
"""
import os, time, httpx, asyncio, json, base64, datetime
from fastapi import FastAPI, Request, Body, HTTPException, Depends, Query
from urllib.parse import urlencode
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from starlette.responses import Response, StreamingResponse
from pydantic import BaseModel
from typing import List, Optional, Dict
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding

CTL_TOKEN = os.getenv("CTL_TOKEN", "chatgpt_secret")
DEAD_S    = int(os.getenv("DEAD_S", "180"))

app      = FastAPI(title="root-hub", version="1.0")
auth_ctl = HTTPBearer(auto_error=False)

# ------ память: name → dict(base_url, rootd_token, last_seen, meta…) ----------
servers: dict[str, dict] = {}
queues: Dict[str, List[Dict]] = {}
results: Dict[str, Dict[str, Dict]] = {}

# ------------------------- LICENSE -------------------------------------------
LICENSE_FILE = os.getenv("LICENSE_FILE", "license.json")
PUBLIC_KEY_FILE = os.getenv("PUBLIC_KEY_FILE", "public.pem")

try:
    with open(PUBLIC_KEY_FILE, "rb") as f:
        _public_key = serialization.load_pem_public_key(f.read())
    with open(LICENSE_FILE) as f:
        _license = json.load(f)
    _message = json.dumps(_license["data"]).encode()
    _signature = base64.b64decode(_license["signature"])
    _public_key.verify(_signature, _message, padding.PKCS1v15(), hashes.SHA256())
    _expiry = _license["data"].get("expiry")
    _max_servers = _license["data"].get("max_servers", 1)
except Exception as e:  # fallback: one server, no expiry
    print(f"License load failed: {e}")
    _expiry = None
    _max_servers = 1


def check_license(current_servers: int):
    if _expiry:
        exp_date = datetime.datetime.strptime(_expiry, "%Y-%m-%d").date()
        if datetime.date.today() > exp_date:
            raise HTTPException(403, "license expired")
    if _max_servers and _max_servers > 0 and current_servers > _max_servers:
        raise HTTPException(403, f"too many servers ({current_servers}/{_max_servers})")


def ensure_license():
    check_license(len(servers))
    
    
async def check_ctl_token(cred: HTTPAuthorizationCredentials = Depends(auth_ctl)):
    if not cred or cred.scheme.lower() != "bearer" or cred.credentials != CTL_TOKEN:
        raise HTTPException(401, "bad token")

# ------------------------------------------------------------------------------
class Beat(BaseModel):
    name: str          # человеко-читаемое
    base_url: str      # http://ip:port  (или https://…)
    rootd_token: str
    time: int          # unixtime
    cores: int | None = None
    mem_mb: int | None = None
    os: str = 'linux'
    mode: str = "webhook"

class BulkExec(BaseModel):
    servers: List[str]
    cmd: str
    timeout: Optional[int] = None
    cwd: Optional[str] = None


class ExecReq(BaseModel):
    cmd: str
    env: Optional[dict] = None
    cwd: Optional[str] = None
    timeout: Optional[int] = None


class TaskResult(BaseModel):
    id: str
    result: dict

@app.post("/heartbeat")
def heartbeat(b: Beat = Body(...)):
    current = len(servers)
    if b.name not in servers:
        current += 1
    check_license(current)
    servers[b.name] = b.dict()
    servers[b.name]["time"] = time.time()
    return {"ok": True}

@app.get("/servers", dependencies=[  Depends(check_ctl_token),Depends(ensure_license)])
def list_servers():
    now = time.time()
    out = []
    for n, d in servers.items():
        alive = (now - d["time"]) < DEAD_S
        b=d.copy()
        b['rootd_token'] = None
        out.append({**d, "alive": alive, "lag_s": round(now-d["time"])})
    return {"servers": out}


    
@app.post(
    "/bulk/exec",
    dependencies=[Depends(check_ctl_token), Depends(ensure_license)],
)
async def bulk_exec(req: BulkExec):
    """Execute a command on multiple servers concurrently."""

    out: dict[str, dict] = {}
    modes: dict[str, str] = {}

    async def wait_polling(srv: str, payload: dict):
        tid = str(time.time_ns())
        queues.setdefault(srv, []).append({"id": tid, **payload})
        deadline = time.time() + (payload.get("timeout") or 300)
        while time.time() < deadline:
            res = results.get(srv, {}).pop(tid, None)
            if res is not None:
                return res
            await asyncio.sleep(0.5)
        return {"error": "task timeout"}

    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        tasks = {}
        for srv in req.servers:
            info = servers.get(srv)
            if not info or time.time() - info["time"] > DEAD_S:
                out[srv] = {"error": "offline"}
                continue
            mode = info.get("mode", "webhook")
            modes[srv] = mode
            payload = {"cmd": req.cmd}
            if req.timeout is not None:
                payload["timeout"] = req.timeout
            if req.cwd is not None:
                payload["cwd"] = req.cwd

            if mode == "polling":
                tasks[srv] = asyncio.create_task(wait_polling(srv, payload))
            else:
                url = f"{info['base_url'].rstrip('/')}/exec"
                headers = {"Authorization": f"Bearer {info['rootd_token']}"}
                tasks[srv] = asyncio.create_task(client.post(url, json=payload, headers=headers))

        for srv, task in tasks.items():
            try:
                r = await task
                if modes[srv] == "polling":
                    out[srv] = r
                else:
                    out[srv] = r.json()
            except Exception as e:
                out[srv] = {"error": str(e)}

    return {"results": out}


# ------------------------- QUEUE / POLL ----------------------------
@app.get("/queue/{srv}", dependencies=[Depends(check_ctl_token),Depends(ensure_license)])
def queue_poll(srv: str, token: str = Query(...)):
    info = servers.get(srv)
    if not info or info.get("rootd_token") != token:
        raise HTTPException(401, "bad token")
    q = queues.get(srv)
    if not q:
        return {}
    return q.pop(0)


@app.post("/queue/{srv}/result", dependencies=[ Depends(check_ctl_token),Depends(ensure_license)])
def queue_result(srv: str, res: TaskResult, token: str = Query(...)):
    info = servers.get(srv)
    if not info or info.get("rootd_token") != token:
        raise HTTPException(401, "bad token")
    results.setdefault(srv, {})[res.id] = res.result
    return {"ok": True}

# ------------------------- ПРОКСИ -------------------------------------------


@app.api_route(
    "/srv/{path:path}",
    methods=["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"],
    dependencies=[Depends(check_ctl_token), Depends(ensure_license)],
)
async def proxy(path: str, request: Request, srv: str = Query(..., alias="server")):
    info = servers.get(srv)
    if not info:
        raise HTTPException(404, f"server '{srv}' not registered")
    if info.get("mode") == "polling":
        if request.method != "POST" or path != "exec":
            raise HTTPException(501, "polling mode supports only POST /exec")
        data = ExecReq(**await request.json())
        tid = str(time.time_ns())
        queues.setdefault(srv, []).append({"id": tid, **data.dict()})
        deadline = time.time() + (data.timeout or 300)
        while time.time() < deadline:
            res = results.get(srv, {}).pop(tid, None)
            if res is not None:
                return res
            await asyncio.sleep(0.5)
        raise HTTPException(504, "task timeout")

    # ---- формируем исходящий запрос -----------------------------------------
    target_url = f"{info['base_url'].rstrip('/')}/{path}"
    q = [(k, v) for k, v in request.query_params.multi_items() if k != "server"]
    if q:
        target_url += "?" + urlencode(q, doseq=True)

    headers = dict(request.headers)
    # убираем авторизацию клиента; ставим rootd-токен
    headers.pop("authorization", None)
    headers["authorization"] = f"Bearer {info['rootd_token']}"

    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        try:
            r = await client.request(
                request.method,
                target_url,
                content=await request.body(),
                headers=headers,
            )
        except httpx.RequestError as e:
            raise HTTPException(502, f"proxy error: {e}")

    # ----- пробрасываем всё как есть ----------------------------------------
    return Response(
        content=r.content,
        status_code=r.status_code,
        headers={
            k: v for k, v in r.headers.items()
            if k.lower() not in {"content-encoding", "transfer-encoding", "connection"}
        },
        media_type=r.headers.get("content-type"),
    )

# ------------------------------------------------------------------------------
if __name__ == "__main__":
    import uvicorn
    uvicorn.run("hub_proxy:app", host="0.0.0.0", port=9001)
