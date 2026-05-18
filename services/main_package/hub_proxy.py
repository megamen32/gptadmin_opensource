#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
Hub-прокси: принимает heartbeat'ы, держит карту <name → (base_url, rootd_token)>,
            а все клиентские вызовы /srv/{path}?server=name проксирует к нужному rootd.

ENV:
  CTL_TOKEN        – Bearer-токен для ChatGPT-клиента            (def: CHANGE_ME)
  DEAD_S           – через сколько секунд считать сервер offline (def: 180)
  HUB_PORT         – порт uvicorn                                (def: 9001)
  LICENSE_FILE     – путь к подписанному license.json             (def: config/license.json)
  PUBLIC_KEY_FILE  – путь к public.pem для проверки подписи       (def: config/public.pem)
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
import hmac
import hashlib
import secrets
import html
from pathlib import Path
from contextvars import ContextVar
from typing import List, Optional, Dict, Any

import httpx
from fastapi import FastAPI, Request, Body, HTTPException, Depends, Query, WebSocket, WebSocketDisconnect
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from pydantic import BaseModel, Field
from starlette.responses import Response
from starlette.middleware.base import BaseHTTPMiddleware
from urllib.parse import urlencode, parse_qs

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
CONFIG_DIR = Path(__file__).resolve().parents[2] / "config"
LICENSE_FILE = os.getenv("LICENSE_FILE") or str(CONFIG_DIR / "license.json")
PUBLIC_KEY_FILE = os.getenv("PUBLIC_KEY_FILE") or str(CONFIG_DIR / "public.pem")

# MCP / ChatGPT Apps OAuth config
PUBLIC_ORIGIN = os.getenv("PUBLIC_ORIGIN", "https://gptadminmcp.bezrabotnyi.com")
MCP_RESOURCE = os.getenv("MCP_RESOURCE", PUBLIC_ORIGIN)
OAUTH_CLIENT_SECRET = os.getenv("OAUTH_CLIENT_SECRET", secrets.token_hex(32))
ADMIN_PASSWORD = os.getenv("ADMIN_PASSWORD", "changeme")
OAUTH_SCOPES = ["gptadmin.read", "gptadmin.exec"]
_oauth_codes: Dict[str, Dict[str, Any]] = {}

# ----------------------------- FASTAPI ---------------------------------------

app = FastAPI(title="root-hub", version="1.1")
auth_ctl = HTTPBearer(auto_error=False)

# ------ память: name → dict(base_url, rootd_token, last_seen, meta…) ----------
servers: Dict[str, Dict[str, Any]] = {}
queues: Dict[str, List[Dict[str, Any]]] = {}
results: Dict[str, Dict[str, Dict[str, Any]]] = {}
ws_sessions: Dict[str, WebSocket] = {}
ws_results: Dict[str, Dict[str, Any]] = {}

# ----------------------------- ЛИЦЕНЗИЯ --------------------------------------

_expiry: Optional[str] = None
_max_servers: int = 1

try:
    with open(PUBLIC_KEY_FILE, "rb") as f:
        _public_key = serialization.load_pem_public_key(f.read())
    with open(LICENSE_FILE) as f:
        _license = json.load(f)
    _message = json.dumps(_license["data"], sort_keys=True, separators=(",",":")).encode()
    _signature = base64.b64decode(_license["signature"])
    _public_key.verify(_signature, _message, padding.PKCS1v15(), hashes.SHA256())
    _expiry = _license["data"].get("expiry")  # YYYY-MM-DD
    _max_servers = int(_license["data"].get("max_servers", 1))
    log.info("license: OK file=%s pub=%s (expiry=%s, max_servers=%s)", LICENSE_FILE, PUBLIC_KEY_FILE, _expiry, _max_servers)
except Exception as e:
    log.exception("license: load/verify failed file=%s pub=%s err=%s. Fallback: max_servers=1, no expiry.", LICENSE_FILE, PUBLIC_KEY_FILE, e)
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
    default_user: Optional[str] = None
    default_uid: Optional[int] = None
    default_home: Optional[str] = None
    os: str = "linux"
    mode: str = Field("webhook", pattern="^(webhook|polling|websocket)$")


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
            for k in ("base_url", "mode", "os", "cores", "mem_mb", "default_user", "default_uid", "default_home")
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
            elif mode == "websocket":
                tasks[srv] = asyncio.create_task(ws_exec(srv, payload, req.timeout))
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
                    # websocket backend already returns dict/json payload
                    out[srv] = r if isinstance(r, dict) else r.json()
                log.info("bulk_exec: done srv=%s status=ok rid=%s", srv, rid())
            except Exception as e:
                out[srv] = {"error": str(e)}
                log.error("bulk_exec: fail srv=%s err=%s rid=%s\n%s",
                          srv, e, rid(), traceback.format_exc())

    log.info("bulk_exec: finished total=%s rid=%s", len(out), rid())
    return {"results": out}


# ------------------------- WEBSOCKET AGENT -------------------------

async def ws_exec(srv: str, payload: dict, timeout: int | None = None) -> dict:
    """Execute a task through an already connected rootd websocket session."""

    ws = ws_sessions.get(srv)
    if ws is None:
        raise HTTPException(503, "websocket session is not connected")
    tid = str(time.time_ns())
    ws_results[tid] = {"event": asyncio.Event(), "result": None}
    try:
        await ws.send_json({"type": "exec", "id": tid, "payload": payload})
        wait_s = timeout or payload.get("timeout") or 300
        await asyncio.wait_for(ws_results[tid]["event"].wait(), timeout=wait_s)
        return ws_results[tid]["result"] or {"error": "empty websocket result"}
    except asyncio.TimeoutError:
        raise HTTPException(504, "websocket task timeout")
    except RuntimeError as e:
        ws_sessions.pop(srv, None)
        raise HTTPException(503, f"websocket send failed: {e}")
    finally:
        ws_results.pop(tid, None)


@app.websocket("/ws/rootd")
async def rootd_ws(websocket: WebSocket):
    await websocket.accept()
    srv_name = None
    try:
        hello = await websocket.receive_json()
        if hello.get("type") != "hello":
            await websocket.close(code=1008, reason="expected hello")
            return
        beat = Beat(**hello.get("payload", {}))
        current = len(servers) + (0 if beat.name in servers else 1)
        _check_license(current)
        srv_name = beat.name
        servers[srv_name] = beat.dict()
        servers[srv_name]["mode"] = "websocket"
        servers[srv_name]["time"] = time.time()
        ws_sessions[srv_name] = websocket
        log.info("ws: connected srv=%s os=%s cores=%s mem_mb=%s", srv_name, beat.os, beat.cores, beat.mem_mb)
        await websocket.send_json({"type": "hello_ack", "ok": True})

        while True:
            msg = await websocket.receive_json()
            msg_type = msg.get("type")
            if msg_type == "heartbeat":
                if srv_name in servers:
                    servers[srv_name]["time"] = time.time()
                await websocket.send_json({"type": "heartbeat_ack", "time": int(time.time())})
            elif msg_type == "result":
                tid = msg.get("id")
                slot = ws_results.get(tid)
                if slot is not None:
                    slot["result"] = msg.get("result")
                    slot["event"].set()
            else:
                log.warning("ws: unknown message srv=%s msg=%s", srv_name, scrub_payload(msg))
    except WebSocketDisconnect:
        log.info("ws: disconnected srv=%s", srv_name)
    except Exception as e:
        log.error("ws: error srv=%s err=%s\n%s", srv_name, e, traceback.format_exc())
        try:
            await websocket.close(code=1011, reason="server error")
        except Exception:
            pass
    finally:
        if srv_name and ws_sessions.get(srv_name) is websocket:
            ws_sessions.pop(srv_name, None)
            if srv_name in servers and servers[srv_name].get("mode") == "websocket":
                servers[srv_name]["time"] = 0


# ------------------------- QUEUE / POLL ----------------------------

@app.get("/queue/{srv}", dependencies=[ Depends(ensure_license)])
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


@app.post("/queue/{srv}/result", dependencies=[ Depends(ensure_license)])
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

    if info.get("mode") == "websocket":
        if request.method != "POST" or path != "exec":
            log.error("proxy: websocket supports only POST /exec srv=%s rid=%s", srv, rid())
            raise HTTPException(501, "websocket mode supports only POST /exec")
        data = ExecReq(**(await request.json()))
        return await ws_exec(srv, data.dict(), data.timeout)

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


# ----------------------------- MCP / OAUTH -----------------------------------

def _b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def _b64url_json(obj: Any) -> str:
    return _b64url(json.dumps(obj, separators=(",", ":")).encode())


def _sign_jwt(payload: Dict[str, Any]) -> str:
    header = {"alg": "HS256", "typ": "JWT"}
    now = int(time.time())
    body = {
        **payload,
        "iss": PUBLIC_ORIGIN,
        "aud": MCP_RESOURCE,
        "iat": now,
        "exp": now + 12 * 3600,
    }
    signing_input = f"{_b64url_json(header)}.{_b64url_json(body)}".encode()
    sig = hmac.new(OAUTH_CLIENT_SECRET.encode(), signing_input, hashlib.sha256).digest()
    return signing_input.decode() + "." + _b64url(sig)


def _verify_jwt(token: str) -> Dict[str, Any]:
    try:
        h, p, sig = token.split(".")
        signing_input = f"{h}.{p}".encode()
        expected = _b64url(hmac.new(OAUTH_CLIENT_SECRET.encode(), signing_input, hashlib.sha256).digest())
        if not hmac.compare_digest(expected, sig):
            raise ValueError("bad signature")
        padded = p + "=" * (-len(p) % 4)
        payload = json.loads(base64.urlsafe_b64decode(padded.encode()))
        if payload.get("iss") != PUBLIC_ORIGIN or payload.get("aud") != MCP_RESOURCE:
            raise ValueError("bad iss/aud")
        if int(payload.get("exp", 0)) < int(time.time()):
            raise ValueError("expired")
        return payload
    except Exception as e:
        raise HTTPException(401, "unauthorized") from e


def _pkce_ok(verifier: str, challenge: str) -> bool:
    if not verifier or not challenge:
        return False
    digest = hashlib.sha256(verifier.encode()).digest()
    return hmac.compare_digest(_b64url(digest), challenge)


def _is_chatgpt_redirect(uri: Optional[str]) -> bool:
    if not uri:
        return False
    try:
        from urllib.parse import urlparse
        u = urlparse(uri)
        return u.scheme == "https" and (u.hostname == "chatgpt.com" or (u.hostname or "").endswith(".chatgpt.com")) and u.path.startswith("/connector/oauth/")
    except Exception:
        return False


def _mcp_auth(request: Request) -> Dict[str, Any]:
    auth = request.headers.get("authorization", "")
    if not auth.lower().startswith("bearer "):
        raise HTTPException(401, "unauthorized")
    return _verify_jwt(auth.split(None, 1)[1])


def _mcp_unauthorized() -> Response:
    return Response(
        content=json.dumps({"error": "unauthorized"}),
        status_code=401,
        media_type="application/json",
        headers={
            "WWW-Authenticate": f'Bearer resource_metadata="{PUBLIC_ORIGIN}/.well-known/oauth-protected-resource", scope="{" ".join(OAUTH_SCOPES)}"'
        },
    )


@app.get("/.well-known/oauth-protected-resource")
def oauth_protected_resource():
    return {
        "resource": MCP_RESOURCE,
        "authorization_servers": [PUBLIC_ORIGIN],
        "scopes_supported": OAUTH_SCOPES,
        "resource_documentation": f"{PUBLIC_ORIGIN}/",
    }


@app.get("/.well-known/oauth-authorization-server")
def oauth_authorization_server():
    return {
        "issuer": PUBLIC_ORIGIN,
        "authorization_endpoint": f"{PUBLIC_ORIGIN}/authorize",
        "token_endpoint": f"{PUBLIC_ORIGIN}/token",
        "registration_endpoint": f"{PUBLIC_ORIGIN}/register",
        "response_types_supported": ["code"],
        "grant_types_supported": ["authorization_code"],
        "code_challenge_methods_supported": ["S256"],
        "token_endpoint_auth_methods_supported": ["none"],
        "client_id_metadata_document_supported": True,
        "scopes_supported": OAUTH_SCOPES,
    }


@app.post("/register")
async def oauth_register():
    return {
        "client_id": "chatgpt-dynamic",
        "token_endpoint_auth_method": "none",
        "grant_types": ["authorization_code"],
        "response_types": ["code"],
    }


@app.get("/authorize")
def oauth_authorize_get(request: Request):
    q = request.query_params
    redirect_uri = q.get("redirect_uri")
    resource = q.get("resource") or MCP_RESOURCE
    if not _is_chatgpt_redirect(redirect_uri) or resource != MCP_RESOURCE:
        raise HTTPException(400, "invalid redirect_uri or resource")
    fields = {
        "redirect_uri": redirect_uri,
        "state": q.get("state", ""),
        "code_challenge": q.get("code_challenge", ""),
        "client_id": q.get("client_id", ""),
        "resource": resource,
        "scope": q.get("scope", " ".join(OAUTH_SCOPES)),
    }
    hidden = "".join(f'<input type="hidden" name="{html.escape(k)}" value="{html.escape(v or "")}">' for k, v in fields.items())
    page = f"""<!doctype html><html><body>
<h2>GPTAdmin MCP Authorization</h2>
<p>Scopes: {html.escape(fields['scope'])}</p>
<form method="POST" action="/authorize">
{hidden}
<input type="password" name="password" placeholder="Admin password" autofocus>
<button type="submit">Authorize</button>
</form>
</body></html>"""
    return Response(page, media_type="text/html")


@app.post("/authorize")
async def oauth_authorize_post(request: Request):
    body = (await request.body()).decode()
    params = {k: v[0] for k, v in parse_qs(body).items()}
    if params.get("password") != ADMIN_PASSWORD:
        raise HTTPException(403, "invalid password")
    redirect_uri = params.get("redirect_uri")
    resource = params.get("resource") or MCP_RESOURCE
    if not _is_chatgpt_redirect(redirect_uri) or resource != MCP_RESOURCE:
        raise HTTPException(400, "invalid redirect_uri or resource")
    code = secrets.token_urlsafe(32)
    _oauth_codes[code] = {
        "created": time.time(),
        "challenge": params.get("code_challenge", ""),
        "client_id": params.get("client_id", ""),
        "resource": resource,
        "scope": params.get("scope", " ".join(OAUTH_SCOPES)),
    }
    location = redirect_uri + ("&" if "?" in redirect_uri else "?") + urlencode({"code": code, "state": params.get("state", "")})
    return Response(status_code=302, headers={"Location": location})


@app.post("/token")
async def oauth_token(request: Request):
    body = (await request.body()).decode()
    params = {k: v[0] for k, v in parse_qs(body).items()}
    data = _oauth_codes.pop(params.get("code", ""), None)
    resource = params.get("resource") or (data or {}).get("resource") or MCP_RESOURCE
    if not data or time.time() - data.get("created", 0) > 300 or resource != MCP_RESOURCE or resource != data.get("resource"):
        raise HTTPException(400, "invalid_grant")
    if not _pkce_ok(params.get("code_verifier", ""), data.get("challenge", "")):
        raise HTTPException(400, "invalid_grant")
    token = _sign_jwt({"sub": "admin", "scope": data.get("scope"), "client_id": data.get("client_id")})
    return {"access_token": token, "token_type": "Bearer", "expires_in": 43200}


def _mcp_tools() -> List[Dict[str, Any]]:
    return [
        {
            "name": "list_servers",
            "title": "List servers",
            "description": "List servers registered in GPTAdmin hub_proxy.",
            "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.read"]}],
            "_meta": {
                "openai/outputTemplate": "https://widgets-gptadmin.bezrabotnyi.com/admin.html",
                "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.read"]}],
            },
        },
        {
            "name": "exec_command",
            "title": "Execute command",
            "description": "Execute a shell command on a server through hub_proxy.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "server": {"type": "string"},
                    "cmd": {"type": "string"},
                    "timeout": {"type": "number", "default": 300},
                    "cwd": {"type": ["string", "null"], "default": None},
                },
                "required": ["server", "cmd"],
                "additionalProperties": False,
            },
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.exec"]}],
            "_meta": {
                "openai/outputTemplate": "https://widgets-gptadmin.bezrabotnyi.com/admin.html",
                "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.exec"]}],
            },
        },
    ]


async def _mcp_call_tool(name: str, args: Dict[str, Any]) -> Dict[str, Any]:
    if name == "list_servers":
        data = list_servers()
        return {
            "content": [{"type": "text", "text": f"Found {len(data.get('servers', []))} servers"}],
            "structuredContent": data,
        }
    if name == "exec_command":
        req = BulkExec(
            servers=[args.get("server")],
            cmd=args.get("cmd"),
            timeout=args.get("timeout", 300),
            cwd=args.get("cwd"),
        )
        data = await bulk_exec(req)
        return {
            "content": [{"type": "text", "text": f"Executed on {args.get('server')}"}],
            "structuredContent": data,
        }
    raise HTTPException(404, f"unknown tool {name}")


@app.options("/mcp")
def mcp_options():
    return Response(status_code=204, headers={
        "Access-Control-Allow-Origin": "*",
        "Access-Control-Allow-Methods": "POST, GET, OPTIONS, DELETE",
        "Access-Control-Allow-Headers": "content-type, authorization, mcp-session-id",
        "Access-Control-Expose-Headers": "Mcp-Session-Id",
    })


@app.get("/mcp")
def mcp_get(request: Request):
    try:
        _mcp_auth(request)
    except HTTPException:
        return _mcp_unauthorized()
    return Response(status_code=405, content=json.dumps({"error": "POST JSON-RPC to this endpoint"}), media_type="application/json")


@app.post("/mcp")
async def mcp_post(request: Request):
    try:
        _mcp_auth(request)
    except HTTPException:
        return _mcp_unauthorized()

    msg = await request.json()
    method = msg.get("method")
    mid = msg.get("id")
    params = msg.get("params") or {}

    try:
        if method == "initialize":
            result = {
                "protocolVersion": params.get("protocolVersion", "2025-06-18"),
                "capabilities": {"tools": {}, "resources": {}},
                "serverInfo": {"name": "gptadmin-hub", "version": "1.0.0"},
            }
        elif method == "tools/list":
            result = {"tools": _mcp_tools()}
        elif method == "tools/call":
            result = await _mcp_call_tool(params.get("name"), params.get("arguments") or {})
        elif method == "resources/list":
            result = {"resources": [{"uri": "https://widgets-gptadmin.bezrabotnyi.com/admin.html", "name": "GPTAdmin widget", "mimeType": "text/html"}]}
        elif method == "resources/read":
            widget_path = Path(__file__).resolve().parents[2] / "apps" / "chatgpt-admin-app" / "public" / "admin-widget.html"
            result = {"contents": [{"uri": params.get("uri"), "mimeType": "text/html", "text": widget_path.read_text() if widget_path.exists() else ""}]}
        elif method and method.startswith("notifications/"):
            return Response(status_code=202)
        else:
            return {"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": f"Method not found: {method}"}}
        return {"jsonrpc": "2.0", "id": mid, "result": result}
    except Exception as e:
        log.error("mcp error method=%s err=%s rid=%s\n%s", method, e, rid(), traceback.format_exc())
        return {"jsonrpc": "2.0", "id": mid, "error": {"code": -32000, "message": str(e)}}


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
