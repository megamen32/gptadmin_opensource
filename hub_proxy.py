# hub_proxy.py
"""
Hub-прокси: принимает heartbeat'ы, держит карту <name → (base_url, rootd_token)>,
            а все клиентские вызовы /srv/{path}?server=name проксирует к нужному rootd.

Env:
  CTL_TOKEN   – Bearer-токен для ChatGPT-клиента      (def: CHANGE_ME)
  DEAD_S      – через сколько секунд считать сервер offline (def: 180)

Зависимости: fastapi, uvicorn[standard], httpx, pydantic
"""
import os, time, httpx, asyncio
from fastapi import FastAPI, Request, Body, HTTPException, Depends, Query
from urllib.parse import urlencode
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from starlette.responses import Response, StreamingResponse
from pydantic import BaseModel
from typing import List, Optional, Dict

CTL_TOKEN = os.getenv("CTL_TOKEN", "CHANGE_ME")
DEAD_S    = int(os.getenv("DEAD_S", "180"))

app      = FastAPI(title="root-hub", version="1.0")
auth_ctl = HTTPBearer(auto_error=False)

# ------ память: name → dict(base_url, rootd_token, last_seen, meta…) ----------
servers: dict[str, dict] = {}
queues: Dict[str, List[Dict]] = {}
results: Dict[str, Dict[str, Dict]] = {}

# ------------------------------------------------------------------------------
class Beat(BaseModel):
    name: str          # человеко-читаемое
    base_url: str      # http://ip:port  (или https://…)
    rootd_token: str
    time: int          # unixtime
    cores: int | None = None
    mem_mb: int | None = None
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
    servers[b.name] = b.dict()
    return {"ok": True}

@app.get("/servers")
def list_servers():
    now = time.time()
    out = []
    for n, d in servers.items():
        alive = (now - d["time"]) < DEAD_S
        out.append({**d, "alive": alive, "lag_s": round(now-d["time"])})
    return {"servers": out}

async def check_ctl_token(cred: HTTPAuthorizationCredentials = Depends(auth_ctl)):
    if not cred or cred.scheme.lower() != "bearer" or cred.credentials != CTL_TOKEN:
        raise HTTPException(401, "bad token")
    
@app.post("/bulk/exec", dependencies=[Depends(check_ctl_token)])
async def bulk_exec(req: BulkExec):
    """Execute a command on multiple servers concurrently."""
    results: dict[str, dict] = {}
    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        tasks = {}
        for srv in req.servers:
            info = servers.get(srv)
            if not info or time.time() - info["time"] > DEAD_S:
                results[srv] = {"error": "offline"}
                continue
            url = f"{info['base_url'].rstrip('/')}/exec"
            headers = {"Authorization": f"Bearer {info['rootd_token']}"}
            payload = {"cmd": req.cmd}
            if req.timeout is not None:
                payload["timeout"] = req.timeout
            if req.cwd is not None:
                payload["cwd"] = req.cwd
            tasks[srv] = client.post(url, json=payload, headers=headers)

        for srv, task in tasks.items():
            try:
                r = await task
                results[srv] = r.json()
            except Exception as e:
                results[srv] = {"error": str(e)}

    return {"results": results}


# ------------------------- QUEUE / POLL ----------------------------
@app.get("/queue/{srv}")
def queue_poll(srv: str, token: str = Query(...)):
    info = servers.get(srv)
    if not info or info.get("rootd_token") != token:
        raise HTTPException(401, "bad token")
    q = queues.get(srv)
    if not q:
        return {}
    return q.pop(0)


@app.post("/queue/{srv}/result")
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
    dependencies=[Depends(check_ctl_token)],
)
async def proxy(path: str, request: Request, srv: str = Query(..., alias="server")):
    info = servers.get(srv)
    if not info:
        raise HTTPException(404, f"server '{srv}' not registered")
    if time.time() - info["time"] > DEAD_S:
        raise HTTPException(503, f"server '{srv}' appears offline")
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
