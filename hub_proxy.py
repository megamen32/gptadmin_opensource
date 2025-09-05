#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
Hub-прокси: принимает heartbeat'ы, держит карту <name → (base_url, rootd_token)>,
            а все клиентские вызовы /srv/{path}?server=name проксирует к нужному rootd.

ENV:
  CTL_TOKEN        – Bearer-токен для ChatGPT-клиента            (def: CHANGE_ME)
  DEAD_S           – через сколько секунд считать сервер offline (def: 180)
  HUB_PORT         – порт uvicorn                                (def: 9001)
  LICENSE_FILE     – путь к подписанному license.json             (def: license.json)
  PUBLIC_KEY_FILE  – путь к public.pem для проверки подписи       (def: public.pem)
  LOG_LEVEL        – уровень логов (DEBUG/INFO/WARNING/ERROR)     (def: INFO)

Зависимости: fastapi, uvicorn[standard], httpx, pydantic, cryptography
"""

from __future__ import annotations

import os
import time
import json
import base64
import asyncio
import datetime
import logging
import traceback
import uuid
from contextvars import ContextVar
from typing import List, Optional, Dict, Any

import httpx
from fastapi import FastAPI, Request, Body, HTTPException, Depends, Query
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from pydantic import BaseModel, Field
from starlette.responses import Response
from starlette.middleware.base import BaseHTTPMiddleware
from urllib.parse import urlencode

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding


# ----------------------------- ЛОГИРОВАНИЕ -----------------------------------

LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format="%(asctime)s %(levelname)s [%(name)s] %(message)s",
)
log = logging.getLogger("hub")

# request-id для корреляции
_request_id: ContextVar[str] = ContextVar("request_id", default="-")


def rid() -> str:
    return _request_id.get("-")


SENSITIVE_KEYS = {"authorization", "rootd_token", "token", "ctl_token"}


def _mask(v: Optional[str]) -> Optional[str]:
    if not v:
        return v
    if len(v) <= 8:
        return "***"
    # первые 2 и последние 2 символа оставляем
    return v[:2] + "…" * 3 + v[-2:]


def scrub_headers(headers: Dict[str, str]) -> Dict[str, str]:
    out = {}
    for k, v in headers.items():
        if k.lower() in SENSITIVE_KEYS:
            out[k] = _mask(v)
        else:
            out[k] = v
    return out


def scrub_query(items: List[tuple[str, str]]) -> List[tuple[str, str]]:
    return [(k, _mask(v) if k.lower() in SENSITIVE_KEYS else v) for k, v in items]


def scrub_payload(obj: Any) -> Any:
    # Маскируем чувствительные поля в словарях/списках
    if isinstance(obj, dict):
        return {k: (_mask(v) if k.lower() in SENSITIVE_KEYS else scrub_payload(v)) for k, v in obj.items()}
    if isinstance(obj, list):
        return [scrub_payload(x) for x in obj]
    return obj


# ----------------------------- КОНФИГ ----------------------------------------

CTL_TOKEN = os.getenv("CTL_TOKEN", "chatgpt_secret")
DEAD_S = int(os.getenv("DEAD_S", "180"))
LICENSE_FILE = os.getenv("LICENSE_FILE", "license.json")
PUBLIC_KEY_FILE = os.getenv("PUBLIC_KEY_FILE", "public.pem")

# ----------------------------- FASTAPI ---------------------------------------

app = FastAPI(title="root-hub", version="1.1")
auth_ctl = HTTPBearer(auto_error=False)

# ------ память: name → dict(base_url, rootd_token, last_seen, meta…) ----------
servers: Dict[str, Dict[str, Any]] = {}
queues: Dict[str, List[Dict[str, Any]]] = {}
results: Dict[str, Dict[str, Dict[str, Any]]] = {}

# ----------------------------- ЛИЦЕНЗИЯ --------------------------------------

_expiry: Optional[str] = None
_max_servers: int = 1

try:
    with open(PUBLIC_KEY_FILE, "rb") as f:
        _public_key = serialization.load_pem_public_key(f.read())
    with open(LICENSE_FILE) as f:
        _license = json.load(f)
    _message = json.dumps(_license["data"]).encode()
    _signature = base64.b64decode(_license["signature"])
    _public_key.verify(_signature, _message, padding.PKCS1v15(), hashes.SHA256())
    _expiry = _license["data"].get("expiry")  # YYYY-MM-DD
    _max_servers = int(_license["data"].get("max_servers", 1))
    log.info("license: OK (expiry=%s, max_servers=%s)", _expiry, _max_servers)
except Exception as e:
    log.warning("license: load/verify failed (%s). Fallback: max_servers=1, no expiry.", e)
    _expiry = None
    _max_servers = 1


def _check_license(current_servers: int):
    if _expiry:
        exp_date = datetime.datetime.strptime(_expiry, "%Y-%m-%d").date()
        if datetime.date.today() > exp_date:
            log.error("license: expired (today>%s) rid=%s", exp_date, rid())
            raise HTTPException(403, "license expired")
    if _max_servers and _max_servers > 0 and current_servers > _max_servers:
        log.error("license: too many servers (%s/%s) rid=%s", current_servers, _max_servers, rid())
        raise HTTPException(403, f"too many servers ({current_servers}/{_max_servers})")


def ensure_license():
    _check_license(len(servers))


async def check_ctl_token(cred: HTTPAuthorizationCredentials = Depends(auth_ctl)):
    if not cred or cred.scheme.lower() != "bearer":
        log.warning("auth: missing/invalid scheme rid=%s", rid())
        raise HTTPException(401, "bad token")
    if cred.credentials != CTL_TOKEN:
        log.warning("auth: bad credentials rid=%s", rid())
        raise HTTPException(401, "bad token")


# ----------------------------- МОДЕЛИ ----------------------------------------

class Beat(BaseModel):
    name: str  # человеко-читаемое
    base_url: str  # http://ip:port  (или https://…)
    rootd_token: str
    time: int  # unixtime
    cores: Optional[int] = None
    mem_mb: Optional[int] = None
    os: str = "linux"
    mode: str = Field("webhook", pattern="^(webhook|polling)$")


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


# -------------------------- MIDDLEWARE ЛОГИ ----------------------------------

class AccessLogMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request: Request, call_next):
        # новый request-id
        req_id = request.headers.get("x-request-id") or str(uuid.uuid4())
        _request_id.set(req_id)

        t0 = time.perf_counter()
        try:
            body = await request.body()
        except Exception:
            body = b""

        q_items = list(request.query_params.multi_items())
        q_scrubbed = scrub_query(q_items)
        hdr_scrubbed = scrub_headers(dict(request.headers))

        log.info(
            "REQ rid=%s %s %s%s ip=%s q=%s hdr=%s body_len=%s",
            rid(),
            request.method,
            request.url.path,
            ("?" + urlencode(q_scrubbed, doseq=True)) if q_scrubbed else "",
            request.client.host if request.client else "-",
            q_scrubbed,
            hdr_scrubbed,
            len(body),
        )

        try:
            response: Response = await call_next(request)
        except Exception as e:
            dt = (time.perf_counter() - t0) * 1000
            log.error("EXC rid=%s %s %s err=%s dt_ms=%.2f\n%s",
                      rid(), request.method, request.url.path, e, dt, traceback.format_exc())
            raise

        dt = (time.perf_counter() - t0) * 1000
        # размер ответа может отсутствовать в заголовке — логируем known length если есть
        resp_len = response.headers.get("content-length", "-")
        log.info(
            "RES rid=%s %s %s status=%s dt_ms=%.2f len=%s",
            rid(),
            request.method,
            request.url.path,
            response.status_code,
            dt,
            resp_len,
        )
        return response


app.add_middleware(AccessLogMiddleware)


# ------------------------------ ЭНДПОИНТЫ ------------------------------------

@app.post("/heartbeat")
def heartbeat(b: Beat = Body(...)):
    # лицензию проверяем на потенциальное количество после апдейта
    current = len(servers) + (0 if b.name in servers else 1)
    _check_license(current)

    prev = servers.get(b.name)
    servers[b.name] = b.dict()
    servers[b.name]["time"] = time.time()

    if prev is None:
        log.info(
            "heartbeat: NEW name=%s base_url=%s mode=%s os=%s cores=%s mem_mb=%s rid=%s",
            b.name, b.base_url, b.mode, b.os, b.cores, b.mem_mb, rid()
        )
    else:
        # сравним ключевые поля (без токена)
        changed = {
            k: (prev.get(k), servers[b.name].get(k))
            for k in ("base_url", "mode", "os", "cores", "mem_mb")
            if prev.get(k) != servers[b.name].get(k)
        }
        log.info(
            "heartbeat: UPDATE name=%s lag_s=%s changed=%s rid=%s",
            b.name, round(time.time() - prev.get("time", servers[b.name]["time"])), changed, rid()
        )
    return {"ok": True}


@app.get("/servers", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def list_servers():
    now = time.time()
    out = []
    for n, d in servers.items():
        alive = (now - d["time"]) < DEAD_S
        lag = round(now - d["time"])
        # не возвращаем rootd_token
        safe = {**d, "alive": alive, "lag_s": lag, "rootd_token": None}
        out.append(safe)
    log.info("servers: list count=%s rid=%s", len(out), rid())
    return {"servers": out}


@app.post("/bulk/exec", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
async def bulk_exec(req: BulkExec):
    """Execute a command on multiple servers concurrently."""
    log.info(
        "bulk_exec: start servers=%s cmd=%s timeout=%s cwd=%s rid=%s",
        req.servers, req.cmd, req.timeout, req.cwd, rid()
    )

    out: Dict[str, Dict[str, Any]] = {}
    modes: Dict[str, str] = {}

    async def wait_polling(srv: str, payload: dict):
        tid = str(time.time_ns())
        queues.setdefault(srv, []).append({"id": tid, **payload})
        log.debug("bulk_exec: queued polling tid=%s srv=%s payload=%s rid=%s",
                  tid, srv, scrub_payload(payload), rid())
        deadline = time.time() + (payload.get("timeout") or 300)
        while time.time() < deadline:
            res = results.get(srv, {}).pop(tid, None)
            if res is not None:
                log.info("bulk_exec: polling result srv=%s tid=%s ok rid=%s", srv, tid, rid())
                return res
            await asyncio.sleep(0.5)
        log.warning("bulk_exec: polling timeout srv=%s tid=%s rid=%s", srv, tid, rid())
        return {"error": "task timeout"}

    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        tasks: Dict[str, asyncio.Task] = {}
        for srv in req.servers:
            info = servers.get(srv)
            if not info:
                out[srv] = {"error": "unknown server"}
                log.warning("bulk_exec: unknown server srv=%s rid=%s", srv, rid())
                continue
            if time.time() - info["time"] > DEAD_S:
                out[srv] = {"error": "offline"}
                log.warning("bulk_exec: server offline srv=%s rid=%s", srv, rid())
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
                log.debug("bulk_exec: webhook POST srv=%s url=%s rid=%s", srv, url, rid())
                tasks[srv] = asyncio.create_task(
                    client.post(url, json=payload, headers=headers)
                )

        for srv, task in tasks.items():
            try:
                r = await task
                if modes[srv] == "polling":
                    out[srv] = r
                else:
                    out[srv] = r.json()
                log.info("bulk_exec: done srv=%s status=ok rid=%s", srv, rid())
            except Exception as e:
                out[srv] = {"error": str(e)}
                log.error("bulk_exec: fail srv=%s err=%s rid=%s\n%s",
                          srv, e, rid(), traceback.format_exc())

    log.info("bulk_exec: finished total=%s rid=%s", len(out), rid())
    return {"results": out}


# ------------------------- QUEUE / POLL ----------------------------

@app.get("/queue/{srv}", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def queue_poll(srv: str, token: str = Query(...)):
    info = servers.get(srv)
    if not info or info.get("rootd_token") != token:
        log.warning("queue_poll: bad token srv=%s rid=%s", srv, rid())
        raise HTTPException(401, "bad token")
    q = queues.get(srv)
    if not q:
        log.debug("queue_poll: empty srv=%s rid=%s", srv, rid())
        return {}
    task = q.pop(0)
    log.info("queue_poll: pop srv=%s id=%s q_left=%s rid=%s", srv, task.get("id"), len(q), rid())
    # маскируем чувствительные поля в ответе логов (в API возвращаем как есть)
    return task


@app.post("/queue/{srv}/result", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def queue_result(srv: str, res: TaskResult, token: str = Query(...)):
    info = servers.get(srv)
    if not info or info.get("rootd_token") != token:
        log.warning("queue_result: bad token srv=%s rid=%s", srv, rid())
        raise HTTPException(401, "bad token")
    results.setdefault(srv, {})[res.id] = res.result
    log.info("queue_result: push srv=%s id=%s rid=%s", srv, res.id, rid())
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
        log.warning("proxy: unknown server srv=%s rid=%s", srv, rid())
        raise HTTPException(404, f"server '{srv}' not registered")

    if info.get("mode") == "polling":
        if request.method != "POST" or path != "exec":
            log.error("proxy: polling supports only POST /exec srv=%s rid=%s", srv, rid())
            raise HTTPException(501, "polling mode supports only POST /exec")
        data = ExecReq(**(await request.json()))
        tid = str(time.time_ns())
        payload = data.dict()
        queues.setdefault(srv, []).append({"id": tid, **payload})
        log.info("proxy: queued polling srv=%s tid=%s payload=%s rid=%s",
                 srv, tid, scrub_payload(payload), rid())

        deadline = time.time() + (data.timeout or 300)
        while time.time() < deadline:
            res = results.get(srv, {}).pop(tid, None)
            if res is not None:
                log.info("proxy: polling result srv=%s tid=%s ok rid=%s", srv, tid, rid())
                return res
            await asyncio.sleep(0.5)
        log.warning("proxy: polling timeout srv=%s tid=%s rid=%s", srv, tid, rid())
        raise HTTPException(504, "task timeout")

    # ---- webhook-прокси ------------------------------------------------------
    target_url = f"{info['base_url'].rstrip('/')}/{path}"
    q = [(k, v) for k, v in request.query_params.multi_items() if k != "server"]
    if q:
        target_url += "?" + urlencode(q, doseq=True)

    headers = dict(request.headers)
    # убираем авторизацию клиента; ставим rootd-токен
    headers.pop("authorization", None)
    headers["authorization"] = f"Bearer {info['rootd_token']}"

    body = await request.body()

    log.info(
        "proxy: -> %s %s hdr=%s q=%s body_len=%s srv=%s rid=%s",
        request.method,
        target_url,
        scrub_headers(headers),
        scrub_query(q),
        len(body),
        srv,
        rid(),
    )

    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        try:
            r = await client.request(
                request.method,
                target_url,
                content=body,
                headers=headers,
            )
        except httpx.RequestError as e:
            log.error("proxy: httpx error srv=%s err=%s rid=%s\n%s", srv, e, rid(), traceback.format_exc())
            raise HTTPException(502, f"proxy error: {e}")

    # ----- пробрасываем ответ как есть ---------------------------------------
    filtered_headers = {
        k: v
        for k, v in r.headers.items()
        if k.lower() not in {"content-encoding", "transfer-encoding", "connection"}
    }
    log.info(
        "proxy: <- %s status=%s len=%s hdr=%s srv=%s rid=%s",
        target_url,
        r.status_code,
        len(r.content) if r.content is not None else "-",
        filtered_headers,
        srv,
        rid(),
    )
    return Response(
        content=r.content,
        status_code=r.status_code,
        headers=filtered_headers,
        media_type=r.headers.get("content-type"),
    )


# ----------------------------- ОБРАБОТЧИКИ ОШИБОК ---------------------------

@app.exception_handler(HTTPException)
async def http_exc_handler(request: Request, exc: HTTPException):
    log.warning("http_exc: %s %s status=%s detail=%s rid=%s",
                request.method, request.url.path, exc.status_code, exc.detail, rid())
    return Response(
        content=json.dumps({"detail": exc.detail}),
        status_code=exc.status_code,
        media_type="application/json",
    )


@app.exception_handler(Exception)
async def unhandled_exc(request: Request, exc: Exception):
    log.error("unhandled_exc: %s %s err=%s rid=%s\n%s",
              request.method, request.url.path, exc, rid(), traceback.format_exc())
    return Response(
        content=json.dumps({"detail": "internal error"}),
        status_code=500,
        media_type="application/json",
    )


# ----------------------------- MAIN ------------------------------------------

if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("HUB_PORT", "9001"))
    log.info("starting hub on 0.0.0.0:%s (dead_s=%s, log_level=%s)", port, DEAD_S, LOG_LEVEL)
    # Включаем стандартные access-логи uvicorn + наши middleware-логи
    uvicorn.run(
        app,
        host="0.0.0.0",
        port=port,
        log_level=LOG_LEVEL.lower(),
    )
