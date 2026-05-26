#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
GPTAdmin Hub Proxy.

Public GPT Actions surface is now intentionally small and MCP-centric:
  • GET  /mcp-relay/agents
  • POST /mcp-relay/tools
  • POST /mcp-relay/call
  • GET  /mcp-relay/job/{job_id}

Old shell/server endpoints are kept as legacy/internal fallback:
  • /servers, /bulk/exec, /tasks/*, /srv/*, /queue/*, /ws/rootd

Important architectural decision:
  rootd servers are exposed to GPT as *virtual MCP agents* with tools such as
  shell_exec and task_status. This lets GPT use one mental
  model: list agents → list tools → call tool → poll job.

Env highlights:
  CTL_TOKEN                  Bearer token for GPT Actions / admin API
  DEAD_S                     Seconds before server/agent is offline
  HUB_SYNC_TIMEOUT_S          Synchronous wait window before returning background job
  GPTADMIN_CONFIG_DIR         Runtime config dir
  GPTADMIN_ARTIFACT_DIR       Directory with gptadmin-rootd.tar.gz
  MCP_RELAY_AGENT_TOKEN       Token for real local MCP relay agents
  PUBLIC_ORIGIN               Public origin for Apps SDK OAuth/MCP server
"""

from __future__ import annotations

import asyncio
import base64
import datetime
import hashlib
import hmac
import html
import json
import logging
import os
import secrets
import shlex
import socket as _socket_module
import sys
import time
import traceback
import uuid
from contextlib import asynccontextmanager
from contextvars import ContextVar
from pathlib import Path
from logging.handlers import WatchedFileHandler
from typing import Any, Dict, List, Optional
from urllib.parse import parse_qs, urlencode, urlparse

import httpx
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding
from fastapi import Body, Depends, FastAPI, HTTPException, Query, Request, WebSocket, WebSocketDisconnect
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from pydantic import BaseModel, Field
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import FileResponse, JSONResponse, PlainTextResponse, Response

from gptadmin_security import (
    NonceCache,
    fingerprint_public_key_b64,
    load_or_create_ed25519_private_key,
    public_key_to_b64,
    sign_request,
    verify_signature,
)

try:
    from gptadmin_build_info import BUILD_TS, BUILD_VERSION, GIT_COMMIT, build_info
except Exception:  # pragma: no cover - fallback for local dev / raw script mode
    BUILD_VERSION = 0
    BUILD_TS = "unknown"
    GIT_COMMIT = "unknown"

    def build_info(component: str) -> dict:
        return {
            "component": component,
            "build_version": BUILD_VERSION,
            "build_ts": BUILD_TS,
            "git_commit": GIT_COMMIT,
        }


# ---------------------------------------------------------------------------
# Logging / request id
# ---------------------------------------------------------------------------

LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format="%(asctime)s %(levelname)s [%(name)s] %(message)s",
)
log = logging.getLogger("hub")
audit_log = logging.getLogger("hub.audit")
audit_log.setLevel(logging.INFO)
audit_log.propagate = False
try:
    _audit_path = Path(os.getenv("GPTADMIN_AUDIT_LOG", "/var/log/gptadmin/audit.log"))
    _audit_path.parent.mkdir(parents=True, exist_ok=True)
    _audit_handler = WatchedFileHandler(_audit_path)
    _audit_handler.setFormatter(logging.Formatter("%(message)s"))
    audit_log.addHandler(_audit_handler)
except Exception as e:
    log.warning("audit log disabled path=%s err=%s", os.getenv("GPTADMIN_AUDIT_LOG", "/var/log/gptadmin/audit.log"), e)

_request_id: ContextVar[str] = ContextVar("request_id", default="-")
_audit_request_context: ContextVar[Dict[str, Any]] = ContextVar("audit_request_context", default={})


def rid() -> str:
    return _request_id.get("-")


def audit_request_context() -> Dict[str, Any]:
    ctx = _audit_request_context.get({})
    return dict(ctx) if isinstance(ctx, dict) else {}


SENSITIVE_KEYS = {"authorization", "rootd_token", "token", "ctl_token", "password", "client_secret"}


def _mask(v: Optional[str]) -> Optional[str]:
    if not v:
        return v
    if len(v) <= 8:
        return "***"
    return v[:2] + "…" * 3 + v[-2:]


def scrub_headers(headers: Dict[str, str]) -> Dict[str, str]:
    return {k: (_mask(v) if k.lower() in SENSITIVE_KEYS else v) for k, v in headers.items()}


def scrub_query(items: List[tuple[str, str]]) -> List[tuple[str, str]]:
    return [(k, _mask(v) if k.lower() in SENSITIVE_KEYS else v) for k, v in items]


def scrub_payload(obj: Any) -> Any:
    if isinstance(obj, dict):
        return {k: (_mask(v) if k.lower() in SENSITIVE_KEYS else scrub_payload(v)) for k, v in obj.items()}
    if isinstance(obj, list):
        return [scrub_payload(x) for x in obj]
    return obj


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

CTL_TOKEN = os.getenv("CTL_TOKEN", "chatgpt_secret")
DEAD_S = int(os.getenv("DEAD_S", "180"))

if os.getenv("GPTADMIN_CONFIG_DIR"):
    CONFIG_DIR = Path(os.environ["GPTADMIN_CONFIG_DIR"])
elif getattr(sys, "frozen", False):
    CONFIG_DIR = Path(sys.executable).parent / "config"
else:
    CONFIG_DIR = Path(__file__).resolve().parents[2] / "config"

LICENSE_FILE = Path(os.getenv("LICENSE_FILE") or str(CONFIG_DIR / "license.json"))
PUBLIC_KEY_FILE = Path(os.getenv("PUBLIC_KEY_FILE") or str(CONFIG_DIR / "public.pem"))


def _default_artifact_dir() -> Path:
    candidates = [
        Path.cwd() / "build",
        Path(__file__).resolve().parents[2] / "build",
        Path(os.getenv("GPTADMIN_HOME", "/opt/gptadmin")) / "artifacts",
    ]
    for candidate in candidates:
        if candidate.exists():
            return candidate
    return candidates[0]


ARTIFACT_DIR = Path(os.getenv("GPTADMIN_ARTIFACT_DIR", str(_default_artifact_dir())))
APPROVED_SERVERS_FILE = Path(os.getenv("GPTADMIN_APPROVED_SERVERS_FILE", str(CONFIG_DIR / "approved_servers.json")))
PENDING_SERVERS_FILE = Path(os.getenv("GPTADMIN_PENDING_SERVERS_FILE", str(CONFIG_DIR / "pending_servers.json")))
HUB_PRIVATE_KEY_FILE = Path(os.getenv("GPTADMIN_HUB_PRIVATE_KEY_FILE", str(CONFIG_DIR / "hub_ed25519")))
HUB_PUBLIC_KEY_FILE_ED25519 = Path(os.getenv("GPTADMIN_HUB_PUBLIC_KEY_FILE", str(CONFIG_DIR / "hub_ed25519.pub")))
HUB_ID = os.getenv("GPTADMIN_HUB_ID", "main-hub")

STATE_TTL_S = int(os.getenv("HUB_STATE_TTL_S", str(3 * 86400)))
SYNC_TIMEOUT_S = int(os.getenv("HUB_SYNC_TIMEOUT_S", "35"))
MCP_RELAY_SYNC_WAIT_MAX_S = int(os.getenv("MCP_RELAY_SYNC_WAIT_MAX_S", str(min(SYNC_TIMEOUT_S, 25))))
MCP_RELAY_REQUEST_TIMEOUT_MAX_S = int(os.getenv("MCP_RELAY_REQUEST_TIMEOUT_MAX_S", "3600"))
CHATGPT_RESPONSE_LIMIT = int(os.getenv("HUB_CHATGPT_RESPONSE_LIMIT", "350000"))
SPILL_FIELD_MIN_CHARS = int(os.getenv("HUB_SPILL_FIELD_MIN_CHARS", "120000"))
OUTPUT_STORE_DIR = Path(os.getenv("HUB_OUTPUT_STORE_DIR", str(CONFIG_DIR / "outputs")))
OUTPUT_STORE_MAX_BYTES = int(os.getenv("HUB_OUTPUT_STORE_MAX_BYTES", str(500 * 1024 * 1024)))
AUDIT_LOG_PATH = Path(os.getenv("GPTADMIN_AUDIT_LOG", "/var/log/gptadmin/audit.log"))
AUDIT_EXCLUDED_PATH_PREFIXES = ("/heartbeat", "/queue/", "/mcp-relay/poll/")

HUB_SERVERS_STATE_FILE = Path(os.getenv("GPTADMIN_SERVERS_STATE_FILE", str(CONFIG_DIR / "hub_servers_state.json")))
HUB_TASKS_STATE_FILE = Path(os.getenv("GPTADMIN_TASKS_STATE_FILE", str(CONFIG_DIR / "hub_tasks_state.json")))
HUB_MCP_AGENTS_STATE_FILE = Path(os.getenv("GPTADMIN_MCP_AGENTS_STATE_FILE", str(CONFIG_DIR / "hub_mcp_agents_state.json")))
HUB_MCP_JOBS_STATE_FILE = Path(os.getenv("GPTADMIN_MCP_JOBS_STATE_FILE", str(CONFIG_DIR / "hub_mcp_jobs_state.json")))

PUBLIC_ORIGIN = os.getenv("PUBLIC_ORIGIN", "https://gptadminmcp.bezrabotnyi.com")
MCP_RESOURCE = os.getenv("MCP_RESOURCE", PUBLIC_ORIGIN)
OAUTH_CLIENT_SECRET = os.getenv("OAUTH_CLIENT_SECRET", secrets.token_hex(32))
ADMIN_PASSWORD = os.getenv("ADMIN_PASSWORD", "changeme")
OAUTH_SCOPES = ["gptadmin.read", "gptadmin.exec"]
_oauth_codes: Dict[str, Dict[str, Any]] = {}

MCP_RELAY_AGENT_TOKEN = os.getenv("MCP_RELAY_AGENT_TOKEN", secrets.token_urlsafe(32))
MCP_RELAY_DEFAULT_TIMEOUT = int(os.getenv("MCP_RELAY_DEFAULT_TIMEOUT", "30"))
MCP_RELAY_POLL_MAX_TIMEOUT = int(os.getenv("MCP_RELAY_POLL_MAX_TIMEOUT", "55"))

LOCAL_HOSTS = {"localhost", "127.0.0.1", "::1", "0.0.0.0"}
VIRTUAL_SHELL_PREFIX = "shell:"
VIRTUAL_HUB_AGENT_ID = "hub"


# ---------------------------------------------------------------------------
# FastAPI app / state
# ---------------------------------------------------------------------------


async def _periodic_save() -> None:
    while True:
        await asyncio.sleep(30)
        _prune_state()
        _save_all_state()


@asynccontextmanager
async def _lifespan(app: FastAPI):
    _load_all_state()
    save_task = asyncio.ensure_future(_periodic_save())
    _sd_notify("READY=1")
    try:
        yield
    finally:
        save_task.cancel()
        try:
            await save_task
        except asyncio.CancelledError:
            pass
        _save_all_state()


app = FastAPI(title="gptadmin-hub", version=str(BUILD_VERSION), lifespan=_lifespan)
auth_ctl = HTTPBearer(auto_error=False)

HUB_PRIVATE_KEY = load_or_create_ed25519_private_key(HUB_PRIVATE_KEY_FILE)
HUB_PUBLIC_KEY_B64 = public_key_to_b64(HUB_PRIVATE_KEY.public_key())
HUB_FINGERPRINT = fingerprint_public_key_b64(HUB_PUBLIC_KEY_B64)
HUB_PUBLIC_KEY_FILE_ED25519.parent.mkdir(parents=True, exist_ok=True)
HUB_PUBLIC_KEY_FILE_ED25519.write_text(HUB_PUBLIC_KEY_B64 + "\n", encoding="utf-8")
os.chmod(HUB_PUBLIC_KEY_FILE_ED25519, 0o644)
SIGNATURE_NONCES = NonceCache(ttl_s=int(os.getenv("GPTADMIN_NONCE_TTL_S", "300")))

servers: Dict[str, Dict[str, Any]] = {}
approved_servers: Dict[str, Dict[str, Any]] = {}
pending_servers: Dict[str, Dict[str, Any]] = {}
queues: Dict[str, List[Dict[str, Any]]] = {}
results: Dict[str, Dict[str, Dict[str, Any]]] = {}
ws_sessions: Dict[str, WebSocket] = {}
ws_results: Dict[str, Dict[str, Any]] = {}
background_tasks: Dict[str, Dict[str, Dict[str, Any]]] = {}

# Real relay agents: laptops/local MCP bridges. Virtual shell agents are derived from `servers`.
mcp_relay_agents: Dict[str, Dict[str, Any]] = {}
mcp_relay_queues: Dict[str, List[Dict[str, Any]]] = {}
mcp_relay_results: Dict[str, Dict[str, Any]] = {}
mcp_relay_jobs: Dict[str, Dict[str, Any]] = {}


# ---------------------------------------------------------------------------
# Utilities: state, output spill, auth, license
# ---------------------------------------------------------------------------


def _load_json_dict(path: Path) -> Dict[str, Any]:
    try:
        if not path.exists():
            return {}
        data = json.loads(path.read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else {}
    except Exception as e:
        log.warning("state: failed to load %s: %s", path, e)
        return {}


def _save_json_dict(path: Path, data: Dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    tmp.replace(path)


def _sd_notify(msg: str) -> None:
    notify_sock = os.getenv("NOTIFY_SOCKET")
    if not notify_sock:
        return
    try:
        sock = _socket_module.socket(_socket_module.AF_UNIX, _socket_module.SOCK_DGRAM)
        with sock:
            addr: Any = ("\0" + notify_sock[1:]) if notify_sock.startswith("@") else notify_sock
            sock.sendto(msg.encode(), addr)
        log.info("sd_notify: %r", msg)
    except Exception as e:
        log.warning("sd_notify: failed: %s", e)


def _load_all_state() -> None:
    now = time.time()
    cutoff = now - STATE_TTL_S

    for name, entry in _load_json_dict(HUB_SERVERS_STATE_FILE).items():
        if isinstance(entry, dict) and float(entry.get("time", 0)) >= cutoff:
            servers[name] = entry
    log.info("state: loaded servers=%s", len(servers))

    for srv, tasks in _load_json_dict(HUB_TASKS_STATE_FILE).items():
        if not isinstance(tasks, dict):
            continue
        kept: Dict[str, Any] = {}
        for tid, task in tasks.items():
            if not isinstance(task, dict):
                continue
            if float(task.get("created_at", 0)) < cutoff:
                continue
            if task.get("status") == "running":
                task = {**task, "status": "orphaned", "orphaned_at": int(now)}
            kept[tid] = task
        if kept:
            background_tasks[srv] = kept
    log.info("state: loaded task_servers=%s", len(background_tasks))

    for agent_id, entry in _load_json_dict(HUB_MCP_AGENTS_STATE_FILE).items():
        if isinstance(entry, dict) and float(entry.get("last_seen", 0)) >= cutoff:
            mcp_relay_agents[agent_id] = entry
    log.info("state: loaded mcp_agents=%s", len(mcp_relay_agents))

    for job_id, job in _load_json_dict(HUB_MCP_JOBS_STATE_FILE).items():
        if isinstance(job, dict) and float(job.get("created_at", 0)) >= cutoff:
            if job.get("status") == "running":
                job = {**job, "status": "orphaned", "orphaned_at": int(now)}
            mcp_relay_jobs[job_id] = job
    log.info("state: loaded mcp_jobs=%s", len(mcp_relay_jobs))


def _save_all_state() -> None:
    try:
        _save_json_dict(HUB_SERVERS_STATE_FILE, servers)
        _save_json_dict(HUB_TASKS_STATE_FILE, background_tasks)
        _save_json_dict(HUB_MCP_AGENTS_STATE_FILE, mcp_relay_agents)
        _save_json_dict(HUB_MCP_JOBS_STATE_FILE, mcp_relay_jobs)
        log.info(
            "state: saved servers=%s task_servers=%s mcp_agents=%s mcp_jobs=%s",
            len(servers),
            len(background_tasks),
            len(mcp_relay_agents),
            len(mcp_relay_jobs),
        )
    except Exception as e:
        log.error("state: save failed: %s", e)


def _prune_state() -> None:
    cutoff = time.time() - STATE_TTL_S

    for name in [n for n, d in servers.items() if float(d.get("time", 0)) < cutoff]:
        servers.pop(name, None)

    for srv in list(background_tasks.keys()):
        for tid in [t for t, d in background_tasks[srv].items() if float(d.get("created_at", 0)) < cutoff]:
            background_tasks[srv].pop(tid, None)
        if not background_tasks[srv]:
            background_tasks.pop(srv, None)

    for agent_id in [a for a, d in mcp_relay_agents.items() if float(d.get("last_seen", 0)) < cutoff]:
        mcp_relay_agents.pop(agent_id, None)

    for job_id in [j for j, d in mcp_relay_jobs.items() if float(d.get("created_at", 0)) < cutoff]:
        mcp_relay_jobs.pop(job_id, None)
        mcp_relay_results.pop(job_id, None)


def _ensure_output_store() -> None:
    OUTPUT_STORE_DIR.mkdir(parents=True, exist_ok=True)


def _rotate_output_store() -> None:
    try:
        files = sorted([p for p in OUTPUT_STORE_DIR.iterdir() if p.is_file()], key=lambda p: p.stat().st_mtime)
        total = sum(f.stat().st_size for f in files)
        for f in files:
            if total <= OUTPUT_STORE_MAX_BYTES:
                break
            try:
                size = f.stat().st_size
                f.unlink()
                total -= size
            except FileNotFoundError:
                pass
    except Exception:
        log.debug("output_store: rotate failed", exc_info=True)


def _find_hub_server() -> str:
    for name, info in servers.items():
        try:
            host = urlparse(str(info.get("base_url", ""))).hostname or ""
            if host in LOCAL_HOSTS:
                return name
        except Exception:
            pass
    return ""


def _spill_field(output_id: str, srv: str, cmd: str, field: str, content: str, returncode: Any) -> dict:
    _ensure_output_store()
    path = OUTPUT_STORE_DIR / f"{output_id}.{field}"
    path.write_text(content, encoding="utf-8")
    meta_path = OUTPUT_STORE_DIR / f"{output_id}.meta.json"
    if not meta_path.exists():
        meta_path.write_text(
            json.dumps({"output_id": output_id, "srv": srv, "cmd": cmd, "returncode": returncode, "ts": int(time.time())}, ensure_ascii=False),
            encoding="utf-8",
        )
    _rotate_output_store()
    lines = content.count("\n") + (1 if content and not content.endswith("\n") else 0)
    stub: Dict[str, Any] = {
        "_spilled": True,
        "field": field,
        "bytes": len(content.encode("utf-8")),
        "lines": lines,
        "file_path": str(path),
        "preview_head": content[:1500],
        "preview_tail": content[-512:] if len(content) > 1500 else "",
        "hint": f"Use shell_exec on hub_server with sed/grep/jq, e.g.: sed -n '1,120p' {shlex.quote(str(path))}",
    }
    hub_srv = _find_hub_server()
    if hub_srv:
        stub["hub_server"] = _virtual_shell_agent_id(hub_srv)
    return stub


def _spill_large_fields(out: Dict[str, Any], cmd: str) -> Dict[str, Any]:
    raw = json.dumps({"results": out}, ensure_ascii=False)
    should_scan = len(raw) > CHATGPT_RESPONSE_LIMIT
    if not should_scan:
        for res in out.values():
            if isinstance(res, dict) and any(isinstance(res.get(field), str) and len(res.get(field) or "") > SPILL_FIELD_MIN_CHARS for field in ("stdout", "stderr")):
                should_scan = True
                break
    if not should_scan:
        return out

    result = {}
    for srv, res in out.items():
        if not isinstance(res, dict) or res.get("background") or "error" in res:
            result[srv] = res
            continue
        output_id = f"out-{int(time.time())}-{uuid.uuid4().hex[:8]}"
        modified = dict(res)
        returncode = res.get("returncode")
        for field in ("stdout", "stderr"):
            val = modified.get(field)
            if isinstance(val, str) and len(val) > SPILL_FIELD_MIN_CHARS:
                modified[field] = _spill_field(output_id, srv, cmd, field, val, returncode)
        result[srv] = modified
    return result


def _spill_single_result(srv: str, result: Any, cmd: str) -> Any:
    if not isinstance(result, dict):
        return result
    return _spill_large_fields({srv: result}, cmd).get(srv, result)


def _server_fingerprint(d: Dict[str, Any]) -> str:
    if d.get("public_key"):
        return fingerprint_public_key_b64(str(d["public_key"]))
    raw = json.dumps(
        {
            "name": d.get("name"),
            "server_id": d.get("server_id"),
            "base_url": d.get("base_url"),
            "backend": d.get("backend"),
            "proxy_via": d.get("proxy_via"),
            "ssh_host": d.get("ssh_host"),
            "ssh_port": d.get("ssh_port"),
            "ssh_user": d.get("ssh_user"),
        },
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return "SHA256:" + base64.urlsafe_b64encode(hashlib.sha256(raw).digest()).decode("ascii").rstrip("=")


def _sanitize_server(d: Dict[str, Any]) -> Dict[str, Any]:
    safe = dict(d)
    safe.pop("rootd_token", None)
    safe.pop("public_key", None)
    return safe


def _pending_record(b: "Beat", reason: str, existing: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    now = time.time()
    payload = b.dict()
    return {
        "status": "pending",
        "reason": reason,
        "name": b.name,
        "requested_at": now,
        "updated_at": now,
        "fingerprint": _server_fingerprint(payload),
        "payload": payload,
        "existing": _sanitize_server(existing or {}) if existing else None,
    }


def _remember_pending(record: Dict[str, Any]) -> None:
    pending_servers[record["name"]] = record
    _save_json_dict(PENDING_SERVERS_FILE, pending_servers)


def _approve_payload(
    name: str,
    payload: Dict[str, Any],
    approved_by: str = "api",
    *,
    approved_via: Optional[str] = None,
    approved_subject: Optional[str] = None,
    approval_context: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    now = time.time()
    ctx = dict(approval_context or audit_request_context())
    approved_via = approved_via or ctx.get("path") or "internal"
    approved_subject = approved_subject or approved_by
    approved_servers[name] = {
        "name": name,
        "status": "approved",
        "approved_at": now,
        "approved_by": approved_by,
        "approved_via": approved_via,
        "approved_subject": approved_subject,
        "approved_request": ctx,
        "base_url": payload.get("base_url"),
        "server_id": payload.get("server_id"),
        "public_key": payload.get("public_key"),
        "fingerprint": _server_fingerprint(payload),
        "backend": payload.get("backend"),
        "proxy_for": payload.get("proxy_for"),
        "proxy_via": payload.get("proxy_via"),
        "ssh_host": payload.get("ssh_host"),
        "ssh_port": payload.get("ssh_port"),
        "ssh_user": payload.get("ssh_user"),
    }
    _save_json_dict(APPROVED_SERVERS_FILE, approved_servers)
    _audit_event({
        "event": "server_approved",
        "name": name,
        "server_id": payload.get("server_id"),
        "fingerprint": approved_servers[name].get("fingerprint"),
        "approved_at": now,
        "approved_by": approved_by,
        "approved_via": approved_via,
        "approved_subject": approved_subject,
        "approval_context": ctx,
    })
    return approved_servers[name]


def _is_approved(name: str) -> bool:
    return name in approved_servers


approved_servers.update(_load_json_dict(APPROVED_SERVERS_FILE))
pending_servers.update(_load_json_dict(PENDING_SERVERS_FILE))
log.info("registry: loaded approved=%s pending=%s", len(approved_servers), len(pending_servers))

_expiry: Optional[str] = None
_max_servers: int = 1
try:
    with PUBLIC_KEY_FILE.open("rb") as f:
        _public_key = serialization.load_pem_public_key(f.read())
    _license = json.loads(LICENSE_FILE.read_text(encoding="utf-8"))
    _message = json.dumps(_license["data"], sort_keys=True, separators=(",", ":")).encode()
    _signature = base64.b64decode(_license["signature"])
    _public_key.verify(_signature, _message, padding.PKCS1v15(), hashes.SHA256())
    _expiry = _license["data"].get("expiry")
    _max_servers = int(_license["data"].get("max_servers", 1))
    log.info("license: OK file=%s pub=%s expiry=%s max_servers=%s", LICENSE_FILE, PUBLIC_KEY_FILE, _expiry, _max_servers)
except Exception as e:
    log.exception("license: load/verify failed file=%s pub=%s err=%s. Fallback: max_servers=1", LICENSE_FILE, PUBLIC_KEY_FILE, e)
    _expiry = None
    _max_servers = 1


def _check_license(current_servers: int) -> None:
    if _expiry:
        exp_date = datetime.datetime.strptime(_expiry, "%Y-%m-%d").date()
        if datetime.date.today() > exp_date:
            raise HTTPException(403, "license expired")
    if _max_servers and _max_servers > 0 and current_servers > _max_servers:
        raise HTTPException(403, f"too many servers ({current_servers}/{_max_servers})")


def ensure_license() -> None:
    _check_license(len(servers))


async def check_ctl_token(cred: HTTPAuthorizationCredentials = Depends(auth_ctl)) -> None:
    if not cred or cred.scheme.lower() != "bearer" or cred.credentials != CTL_TOKEN:
        log.warning("auth: bad/missing bearer rid=%s", rid())
        raise HTTPException(401, "bad token")


# ---------------------------------------------------------------------------
# Pydantic models
# ---------------------------------------------------------------------------



from fastapi.exceptions import RequestValidationError

@app.exception_handler(RequestValidationError)
async def validation_handler(request, exc):
    body=await request.body()
    log.error("VALIDATION 422 path=%s errors=%s body=%s", request.url.path, exc.errors(), body.decode(errors="ignore"))
    raise exc

class Beat(BaseModel):
    name: str
    server_id: str
    public_key: str
    fingerprint: Optional[str] = None
    base_url: str
    rootd_token: Optional[str] = None
    time: int
    cores: Optional[int] = None
    mem_mb: Optional[int] = None
    default_user: Optional[str] = None
    default_uid: Optional[int] = None
    default_home: Optional[str] = None
    os: str = "linux"
    mode: str = Field("webhook", pattern="^(webhook|polling|websocket)$")
    version: Optional[int] = None
    build_version: Optional[int] = None
    build_ts: Optional[str] = None
    git_commit: Optional[str] = None
    backend: Optional[str] = None
    proxy_for: Optional[str] = None
    proxy_via: Optional[str] = None
    ssh_host: Optional[str] = None
    ssh_port: Optional[str] = None
    ssh_user: Optional[str] = None


class BulkExec(BaseModel):
    servers: List[str]
    cmd: str
    timeout: Optional[int] = None
    cwd: Optional[str] = None
    env: Optional[Dict[str, Any]] = None
    background: bool = False


class ExecReq(BaseModel):
    cmd: str
    env: Optional[dict] = None
    cwd: Optional[str] = None
    timeout: Optional[int] = None


class TaskResult(BaseModel):
    id: str
    result: dict


class McpRelayRegister(BaseModel):
    agent_id: str
    name: Optional[str] = None
    transport: str = Field("stdio", pattern="^(stdio|http)$")
    command: Optional[str] = None
    capabilities: Optional[List[str]] = None
    meta: Optional[Dict[str, Any]] = None


class McpRelayResult(BaseModel):
    id: str
    ok: bool = True
    result: Optional[Dict[str, Any]] = None
    error: Optional[Dict[str, Any]] = None


class McpRelayToolsReq(BaseModel):
    target: str
    timeout: Optional[int] = Field(default=None, ge=1, le=MCP_RELAY_REQUEST_TIMEOUT_MAX_S)
    background: bool = False


class McpRelayCallReq(BaseModel):
    target: str
    tool_name: str
    arguments: Dict[str, Any] = Field(default_factory=dict)
    timeout: Optional[int] = Field(default=None, ge=1, le=MCP_RELAY_REQUEST_TIMEOUT_MAX_S)
    background: bool = False


# ---------------------------------------------------------------------------
# Audit log
# ---------------------------------------------------------------------------


def _audit_token_id(request: Request) -> Optional[str]:
    auth = request.headers.get("authorization") or request.headers.get("Authorization") or ""
    if not auth:
        return None
    parts = auth.split(None, 1)
    token = parts[1] if len(parts) == 2 else auth
    if not token:
        return None
    return "sha256:" + hashlib.sha256(token.encode("utf-8", "ignore")).hexdigest()[:16]


def _audit_auth_kind(request: Request) -> str:
    if request.headers.get("x-gptadmin-signature"):
        return "gptadmin-signature"
    auth = request.headers.get("authorization") or request.headers.get("Authorization") or ""
    if auth.lower().startswith("bearer "):
        return "bearer"
    if auth:
        return "authorization"
    return "none"


def _socket_client(request: Request) -> str:
    if not request.client:
        return ""
    host = request.client.host or ""
    port = request.client.port
    return f"{host}:{port}" if port else host


def _forwarded_for_chain(request: Request) -> List[str]:
    raw = request.headers.get("x-forwarded-for") or ""
    return [part.strip() for part in raw.split(",") if part.strip()]


def _client_ip(request: Request) -> str:
    chain = _forwarded_for_chain(request)
    return request.headers.get("x-real-ip") or (chain[0] if chain else "") or (request.client.host if request.client else "")


def _audit_request_fields(request: Request) -> Dict[str, Any]:
    return {
        "client_ip": _client_ip(request),
        "ip": _client_ip(request),  # Backward-compatible alias; prefer client_ip in new code.
        "socket_client": _socket_client(request),
        "x_real_ip": request.headers.get("x-real-ip"),
        "x_forwarded_for": request.headers.get("x-forwarded-for"),
        "forwarded_for_chain": _forwarded_for_chain(request),
        "x_forwarded_proto": request.headers.get("x-forwarded-proto"),
        "host": request.headers.get("host"),
        "user_agent": request.headers.get("user-agent"),
        "openai_ephemeral_user_id": request.headers.get("openai-ephemeral-user-id"),
        "openai_conversation_id": request.headers.get("openai-conversation-id"),
        "openai_gpt_id": request.headers.get("openai-gpt-id"),
        "auth_kind": _audit_auth_kind(request),
        "token_id": _audit_token_id(request),
    }


def _audit_should_skip(path: str) -> bool:
    return any(path == prefix.rstrip("/") or path.startswith(prefix) for prefix in AUDIT_EXCLUDED_PATH_PREFIXES)


def _audit_event(event: Dict[str, Any]) -> None:
    if not audit_log.handlers:
        return
    event.setdefault("ts", datetime.datetime.now(datetime.timezone.utc).isoformat())
    try:
        audit_log.info(json.dumps(event, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
    except Exception as e:
        log.warning("audit write failed err=%s", e)


# ---------------------------------------------------------------------------
# Middleware
# ---------------------------------------------------------------------------


class AccessLogMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request: Request, call_next):
        req_id = request.headers.get("x-request-id") or str(uuid.uuid4())
        _request_id.set(req_id)
        t0 = time.perf_counter()

        try:
            body = await request.body()
        except Exception:
            body = b""

        q_items = list(request.query_params.multi_items())
        _audit_request_context.set({
            "rid": rid(),
            "method": request.method,
            "path": request.url.path,
            "query_keys": sorted(request.query_params.keys()),
            **_audit_request_fields(request),
        })
        log.info(
            "REQ rid=%s %s %s%s ip=%s q=%s hdr=%s body_len=%s",
            rid(),
            request.method,
            request.url.path,
            ("?" + urlencode(scrub_query(q_items), doseq=True)) if q_items else "",
            request.client.host if request.client else "-",
            scrub_query(q_items),
            scrub_headers(dict(request.headers)),
            len(body),
        )

        try:
            response: Response = await call_next(request)
        except Exception as e:
            dt = (time.perf_counter() - t0) * 1000
            log.error("EXC rid=%s %s %s err=%s dt_ms=%.2f\n%s", rid(), request.method, request.url.path, e, dt, traceback.format_exc())
            raise

        dt = (time.perf_counter() - t0) * 1000
        log.info("RES rid=%s %s %s status=%s dt_ms=%.2f len=%s", rid(), request.method, request.url.path, response.status_code, dt, response.headers.get("content-length", "-"))
        if not _audit_should_skip(request.url.path):
            _audit_event({
                "event": "http_request",
                "rid": rid(),
                "method": request.method,
                "path": request.url.path,
                "query_keys": sorted(request.query_params.keys()),
                "status": response.status_code,
                "dt_ms": round(dt, 2),
                "body_len": len(body),
                "content_length": request.headers.get("content-length"),
                **_audit_request_fields(request),
            })
        return response


app.add_middleware(AccessLogMiddleware)


# ---------------------------------------------------------------------------
# Rootd signing / heartbeat / artifacts
# ---------------------------------------------------------------------------


def _task_slot(srv: str, tid: str) -> Dict[str, Any]:
    return background_tasks.setdefault(srv, {}).setdefault(
        tid,
        {"status": "running", "created_at": int(time.time()), "task_id": tid},
    )


def _signed_rootd_headers(method: str, path: str, body: bytes, extra: Optional[Dict[str, str]] = None) -> Dict[str, str]:
    signed = sign_request(HUB_PRIVATE_KEY, method, path, body)
    headers = {
        "X-GPTAdmin-Hub-ID": HUB_ID,
        "X-GPTAdmin-Timestamp": signed["timestamp"],
        "X-GPTAdmin-Nonce": signed["nonce"],
        "X-GPTAdmin-Signature": signed["signature"],
    }
    if extra:
        headers.update(extra)
    return headers


def _verify_heartbeat_signature(request: Request, b: Beat, body: bytes) -> None:
    ts = request.headers.get("X-GPTAdmin-Timestamp")
    nonce = request.headers.get("X-GPTAdmin-Nonce")
    sig = request.headers.get("X-GPTAdmin-Signature")
    server_header = request.headers.get("X-GPTAdmin-Server")
    server_id_header = request.headers.get("X-GPTAdmin-Server-ID")
    if server_header != b.name or server_id_header != b.server_id:
        raise HTTPException(401, "signed heartbeat identity headers mismatch")
    if not ts or not nonce or not sig:
        raise HTTPException(401, "missing signed heartbeat headers")

    pub = b.public_key
    approved = approved_servers.get(b.name) or {}
    if approved.get("public_key"):
        pub = approved["public_key"]
    try:
        SIGNATURE_NONCES.check_and_store(f"rootd:{b.name}:{b.server_id}", nonce)
        verify_signature(pub, request.method, request.url.path, ts, nonce, body, sig)
    except Exception as e:
        raise HTTPException(401, f"invalid signed heartbeat: {e}") from e


def _rootd_artifact_path() -> Path:
    return ARTIFACT_DIR / "gptadmin-rootd.tar.gz"


def _rootd_artifact_meta_path() -> Path:
    return ARTIFACT_DIR / "gptadmin-rootd.json"


def _sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


@app.get("/version")
def version():
    data = build_info("hub_proxy")
    data.update(
        {
            "artifact_dir": str(ARTIFACT_DIR),
            "hub_id": HUB_ID,
            "hub_fingerprint": HUB_FINGERPRINT,
            "hub_public_key": HUB_PUBLIC_KEY_B64,
            "public_actions": ["listMcpAgents", "listMcpTools", "callMcpTool", "getMcpJob"],
            "legacy_shell_api": True,
        }
    )
    return data


ACTIONS_OPENAPI_YAML = r'''
openapi: 3.1.0
info:
  title: GPTAdmin MCP Relay
  version: 4.0.0
  description: |
    Universal MCP relay for GPTAdmin.

    Workflow:
      1. listMcpAgents — choose a real local MCP relay agent or a virtual shell agent.
      2. listMcpTools — inspect tools available on that target.
      3. callMcpTool — call one tool on one target.
      4. If response has background:true and job_id, poll getMcpJob until completed/failed.

    Shell servers are exposed as virtual MCP agents with target ids like shell:<server_name>.
servers:
  - url: https://gptadmin.bezrabotnyi.com
security:
  - bearerAuth: []
paths:
  /mcp-relay/agents:
    get:
      operationId: listMcpAgents
      summary: List real MCP relay agents and virtual shell agents
      responses:
        "200":
          description: Available agents
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ListMcpAgentsResponse"
  /mcp-relay/tools:
    post:
      operationId: listMcpTools
      summary: List tools available on one MCP target
      description: Requests tools/list from an explicitly selected MCP agent. Call listMcpAgents first and pass one returned agent_id as target. There is no default target; never use target="default".
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/ListMcpToolsRequest"
      responses:
        "200":
          description: Tool list or background job
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/McpToolResponse"
  /mcp-relay/call:
    post:
      operationId: callMcpTool
      summary: Call a tool on one MCP target
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/CallMcpToolRequest"
      responses:
        "200":
          description: Tool response or background job
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/McpToolResponse"
  /mcp-relay/job/{job_id}:
    get:
      operationId: getMcpJob
      summary: Get MCP background job status and optionally consume it
      parameters:
        - name: job_id
          in: path
          required: true
          schema: { type: string }
        - name: ack
          in: query
          required: false
          schema: { type: boolean, default: false }
          description: If true, remove completed/failed job result after reading.
      responses:
        "200":
          description: Job status/result
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/McpJobResponse"
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
  schemas:
    ListMcpAgentsResponse:
      type: object
      additionalProperties: false
      required: [agents]
      properties:
        agents:
          type: array
          items:
            type: object
            additionalProperties: false
            required: [agent_id, name, status, kind]
            properties:
              agent_id: { type: string }
              name: { type: string }
              kind: { type: string, enum: [real_mcp, virtual_shell, virtual_hub] }
              transport: { type: string }
              status: { type: string, enum: [online, offline, pending] }
              last_seen: { type: [number, "null"] }
              capabilities:
                type: array
                items: { type: string }
              meta:
                type: object
                additionalProperties: true
    ListMcpToolsRequest:
      type: object
      additionalProperties: false
      required: [target]
      properties:
        target:
          type: string
          description: Explicit agent id from listMcpAgents. There is no default target. Never use "default".
        timeout:
          type: [integer, "null"]
          minimum: 1
          maximum: 35
          default: 30
        background:
          type: boolean
          default: false
    CallMcpToolRequest:
      type: object
      additionalProperties: false
      required: [target, tool_name]
      properties:
        target:
          type: string
          description: Explicit agent id from listMcpAgents. There is no default target. Never use "default".
        tool_name:
          type: string
          description: Tool name returned by listMcpTools.
        arguments:
          type: object
          additionalProperties: true
          default: {}
        timeout:
          type: [integer, "null"]
          minimum: 1
          maximum: 35
          default: 30
        background:
          type: boolean
          default: false
    McpToolResponse:
      type: object
      additionalProperties: false
      required: [agent_id, status]
      properties:
        agent_id: { type: string }
        status: { type: string }
        response:
          type: object
          additionalProperties: true
        background: { type: boolean }
        job_id: { type: string }
        message: { type: string }
    McpJobResponse:
      type: object
      additionalProperties: false
      required: [job_id, status]
      properties:
        job_id: { type: string }
        status: { type: string, enum: [queued, running, completed, failed, orphaned, running_or_unknown] }
        agent_id: { type: [string, "null"] }
        response:
          type: [object, "null"]
          additionalProperties: true
        error:
          type: [object, string, "null"]
          additionalProperties: true
        acked: { type: boolean }
'''


@app.get("/actions/openapi.yaml", include_in_schema=False)
def actions_openapi_yaml():
    # The public Actions spec is intentionally generated here so it cannot drift back
    # to legacy shell endpoints by accident.
    return PlainTextResponse(ACTIONS_OPENAPI_YAML.strip() + "\n", media_type="application/yaml")


@app.get("/artifacts/rootd.json", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def rootd_artifact_manifest(request: Request):
    artifact = _rootd_artifact_path()
    if not artifact.is_file():
        raise HTTPException(404, f"rootd artifact not found: {artifact}")
    meta = {
        "component": "rootd",
        "build_version": BUILD_VERSION,
        "build_ts": BUILD_TS,
        "git_commit": GIT_COMMIT,
    }
    meta_path = _rootd_artifact_meta_path()
    if meta_path.is_file():
        try:
            loaded = json.loads(meta_path.read_text(encoding="utf-8"))
            if isinstance(loaded, dict):
                meta.update({k: v for k, v in loaded.items() if k not in {"sha256", "size", "url"}})
        except Exception as e:
            log.warning("rootd artifact metadata ignored path=%s err=%s", meta_path, e)
    meta.update({
        "sha256": _sha256_file(artifact),
        "size": artifact.stat().st_size,
        "url": str(request.url_for("rootd_artifact_download")),
    })
    return meta


@app.get("/artifacts/rootd.tar.gz", name="rootd_artifact_download", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def rootd_artifact_download():
    artifact = _rootd_artifact_path()
    if not artifact.is_file():
        raise HTTPException(404, f"rootd artifact not found: {artifact}")
    return FileResponse(str(artifact), media_type="application/gzip", filename="gptadmin-rootd.tar.gz")


@app.post("/heartbeat")
async def heartbeat(request: Request, b: Beat = Body(...)):
    body = await request.body()
    _verify_heartbeat_signature(request, b, body)
    if b.fingerprint and b.fingerprint != _server_fingerprint(b.dict()):
        raise HTTPException(401, "heartbeat fingerprint does not match public key")

    prev = servers.get(b.name)
    known = prev is not None or _is_approved(b.name)
    if not known:
        current = len(servers) + (0 if b.name in servers else 1)
        _check_license(current)
        rec = _pending_record(b, reason="new_server")
        _remember_pending(rec)
        log.warning("heartbeat: PENDING new name=%s base_url=%s rid=%s", b.name, b.base_url, rid())
        return {"ok": False, "status": "pending", "reason": "new_server"}

    if _is_approved(b.name):
        approved = approved_servers.get(b.name, {})
        expected_fp = approved.get("fingerprint")
        current_fp = _server_fingerprint(b.dict())
        identity_changed = (
            (approved.get("public_key") and approved.get("public_key") != b.public_key)
            or (approved.get("server_id") and approved.get("server_id") != b.server_id)
            or (expected_fp and current_fp != expected_fp)
        )
        if identity_changed:
            rec = _pending_record(b, reason="fingerprint_changed", existing=approved)
            _remember_pending(rec)
            log.warning("heartbeat: PENDING changed identity name=%s base_url=%s rid=%s", b.name, b.base_url, rid())
            return {"ok": False, "status": "pending", "reason": "fingerprint_changed"}

    servers[b.name] = b.dict()
    servers[b.name]["time"] = time.time()
    servers[b.name]["status"] = "active"
    pending_servers.pop(b.name, None)
    _save_json_dict(PENDING_SERVERS_FILE, pending_servers)

    log.info("heartbeat: ACTIVE/UPDATE name=%s mode=%s base_url=%s rid=%s", b.name, b.mode, b.base_url, rid())
    return {"ok": True, "status": "active", "mcp_agent_id": _virtual_shell_agent_id(b.name)}


# ---------------------------------------------------------------------------
# Legacy shell API kept for fallback/internal use
# ---------------------------------------------------------------------------


def _public_server_record(name: str, d: Dict[str, Any]) -> Dict[str, Any]:
    now = time.time()
    alive = (now - float(d.get("time", 0))) < DEAD_S
    lag = round(now - float(d.get("time", now)))
    safe = _sanitize_server({**d, "status": "active", "alive": alive, "lag_s": lag})
    safe.pop("fingerprint", None)
    safe["mcp_agent_id"] = _virtual_shell_agent_id(name)
    return safe


@app.get("/servers", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def list_servers(include_pending: bool = True):
    out = [_public_server_record(name, data) for name, data in servers.items()]
    pending: List[Dict[str, Any]] = []
    if include_pending:
        for name, rec in pending_servers.items():
            payload = rec.get("payload", {}) or {}
            safe = _sanitize_server(
                {
                    **payload,
                    "status": "pending",
                    "alive": False,
                    "lag_s": None,
                    "pending_reason": rec.get("reason"),
                    "requested_at": rec.get("requested_at"),
                    "updated_at": rec.get("updated_at"),
                    "fingerprint": rec.get("fingerprint"),
                    "approve_command": f"gptadmin_pending approve {shlex.quote(name)}",
                    "reject_command": f"gptadmin_pending reject {shlex.quote(name)}",
                    "how_to_approve": f"Call hub tool approve_pending_server with name={name}, or run gptadmin_pending approve {shlex.quote(name)} on any active shell agent.",
                }
            )
            out.append(safe)
            pending.append(rec)
    return {"servers": out, "pending": pending}


def _handle_gptadmin_task_command(srv: str, cmd: str):
    try:
        parts = shlex.split(cmd.strip())
    except ValueError as e:
        return {"error": f"bad command syntax: {e}"}
    if not parts:
        return None

    if parts[0] == "gptadmin_tasks":
        if len(parts) >= 2 and parts[1] == "list":
            return {"ok": True, "tasks": list(background_tasks.get(srv, {}).values())}
        if len(parts) >= 3 and parts[1] == "status":
            tid = parts[2]
            task = _legacy_get_task(srv, tid)
            if not task:
                return {"error": f"task not found: {tid}"}
            return {"ok": True, "task": task}
        return {"error": "usage: gptadmin_tasks list | gptadmin_tasks status <task_id>"}

    if parts[0] == "gptadmin_pending":
        if len(parts) >= 2 and parts[1] == "list":
            return {"ok": True, "pending": list(pending_servers.values()), "count": len(pending_servers)}
        if len(parts) >= 3 and parts[1] == "approve":
            return _approve_pending_server(parts[2], approved_by=f"gptadmin_pending via {srv}", approved_via="virtual_shell:gptadmin_pending", approved_subject=srv)
        if len(parts) >= 3 and parts[1] == "reject":
            return _reject_pending_server(parts[2])
        return {"error": "usage: gptadmin_pending list | gptadmin_pending approve <name> | gptadmin_pending reject <name>"}

    return None


def _approve_pending_server(name: str, approved_by: str = "api", *, approved_via: Optional[str] = None, approved_subject: Optional[str] = None) -> Dict[str, Any]:
    rec = pending_servers.get(name)
    if not rec:
        return {"ok": False, "error": f"no pending server named {name}"}
    payload = rec.get("payload") or {}
    approved = _approve_payload(name, payload, approved_by=approved_by, approved_via=approved_via, approved_subject=approved_subject)
    payload["time"] = time.time()
    payload["status"] = "active"
    servers[name] = payload
    pending_servers.pop(name, None)
    _save_json_dict(PENDING_SERVERS_FILE, pending_servers)
    log.info("pending: approved name=%s by=%s rid=%s", name, approved_by, rid())
    return {"ok": True, "status": "approved", "name": name, "server": _sanitize_server(servers[name]), "approved": approved}


def _reject_pending_server(name: str) -> Dict[str, Any]:
    rec = pending_servers.pop(name, None)
    if not rec:
        return {"ok": False, "error": f"no pending server named {name}"}
    _save_json_dict(PENDING_SERVERS_FILE, pending_servers)
    log.info("pending: rejected name=%s rid=%s", name, rid())
    return {"ok": True, "status": "rejected", "name": name}


async def _webhook_exec(info: Dict[str, Any], payload: dict) -> dict:
    url = f"{str(info['base_url']).rstrip('/')}/exec"
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    headers = _signed_rootd_headers("POST", "/exec", body, {"Content-Type": "application/json"})
    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        r = await client.post(url, content=body, headers=headers)
        try:
            return r.json()
        except Exception:
            return {"stdout": r.text, "stderr": "", "returncode": 0 if r.status_code < 400 else r.status_code}


async def _webhook_exec_callback(srv: str, info: Dict[str, Any], payload: dict, tid: str) -> Dict[str, Any]:
    url = f"{str(info['base_url']).rstrip('/')}/exec/callback"
    callback_payload = dict(payload)
    callback_payload["job_id"] = tid
    body = json.dumps(callback_payload, separators=(",", ":")).encode("utf-8")
    headers = _signed_rootd_headers("POST", "/exec/callback", body, {"Content-Type": "application/json"})
    async with httpx.AsyncClient(follow_redirects=True, timeout=10) as client:
        r = await client.post(url, content=body, headers=headers)
        if r.status_code == 404:
            return {"ok": False, "fallback": "exec_live", "status_code": 404}
        if r.status_code >= 400:
            return {"ok": False, "status_code": r.status_code, "error": r.text[:1000]}
        try:
            data = r.json()
        except Exception:
            data = {"ok": True, "text": r.text[:1000]}
        data.setdefault("ok", True)
        return data


async def _webhook_exec_live(srv: str, info: Dict[str, Any], payload: dict, tid: str) -> dict:
    """Run webhook command through rootd /exec/live and update task stdout/stderr while it runs."""
    url = f"{str(info['base_url']).rstrip('/')}/exec/live"
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    headers = _signed_rootd_headers("POST", "/exec/live", body, {"Content-Type": "application/json"})
    slot = _task_slot(srv, tid)
    result: Dict[str, Any] = slot.setdefault("result", {})
    result.setdefault("stdout", "")
    result.setdefault("stderr", "")
    result.setdefault("returncode", None)
    slot.update({"status": "running", "updated_at": int(time.time())})

    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        async with client.stream("POST", url, content=body, headers=headers) as r:
            if r.status_code == 404:
                # Older rootd without /exec/live; fall back to legacy buffered exec.
                return await _webhook_exec(info, payload)
            if r.status_code >= 400:
                text = await r.aread()
                return {"returncode": r.status_code, "stdout": "", "stderr": text.decode("utf-8", "replace"), "error": f"/exec/live HTTP {r.status_code}"}
            saw_exit = False
            async for line in r.aiter_lines():
                if not line:
                    continue
                try:
                    event = json.loads(line)
                except Exception:
                    result["stdout"] = str(result.get("stdout") or "") + line + "\n"
                    slot["updated_at"] = int(time.time())
                    continue
                etype = event.get("type")
                if etype in {"stdout", "stderr"}:
                    field = etype
                    result[field] = str(result.get(field) or "") + str(event.get("data") or "")
                    slot["updated_at"] = int(time.time())
                elif etype == "exit":
                    saw_exit = True
                    result["returncode"] = event.get("returncode")
                    if event.get("error"):
                        result["error"] = event.get("error")
                    result["metadata_restore"] = event.get("metadata_restore")
                    result["run_as_user"] = event.get("run_as_user")
                    slot["updated_at"] = int(time.time())
                elif etype == "error":
                    result["error"] = event.get("error") or "live exec error"
                    if event.get("traceback"):
                        result["traceback"] = event.get("traceback")
                    result["returncode"] = result.get("returncode") if result.get("returncode") is not None else -1
                    slot["updated_at"] = int(time.time())
            if not saw_exit and result.get("returncode") is None:
                result["returncode"] = 0
    return result


async def _queue_or_fire_background(srv: str, info: Dict[str, Any], payload: dict, tid: str) -> None:
    mode = info.get("mode", "webhook")
    _task_slot(srv, tid).update({"cmd": payload.get("cmd"), "cwd": payload.get("cwd")})

    if mode == "polling":
        queues.setdefault(srv, []).append({"id": tid, **payload})
        return

    if mode == "websocket":
        ws = ws_sessions.get(srv)
        if not ws:
            background_tasks.setdefault(srv, {})[tid].update({"status": "failed", "error": "websocket not connected", "completed_at": int(time.time())})
            return
        await ws.send_json({"type": "exec", "id": tid, "payload": payload})
        return

    async def runner() -> None:
        try:
            started = await _webhook_exec_callback(srv, info, payload, tid)
            if started.get("fallback") == "exec_live":
                result = await _webhook_exec_live(srv, info, payload, tid)
                background_tasks.setdefault(srv, {})[tid].update({"status": "completed", "result": _spill_single_result(srv, result, str(payload.get("cmd") or "")), "completed_at": int(time.time()), "updated_at": int(time.time())})
                return
            if not started.get("ok"):
                background_tasks.setdefault(srv, {})[tid].update({"status": "failed", "error": started, "completed_at": int(time.time()), "updated_at": int(time.time())})
                return
            background_tasks.setdefault(srv, {})[tid].update({"status": "running", "delivery": "callback_outbox", "rootd_start": started, "updated_at": int(time.time())})
        except Exception as e:
            background_tasks.setdefault(srv, {})[tid].update({"status": "failed", "error": str(e), "completed_at": int(time.time()), "updated_at": int(time.time())})

    asyncio.create_task(runner())


async def _exec_single_server(srv: str, req: BulkExec) -> Dict[str, Any]:
    info = servers.get(srv)
    if not info:
        return {"error": "unknown server"}
    if time.time() - float(info.get("time", 0)) > DEAD_S:
        return {"error": "offline"}

    special = _handle_gptadmin_task_command(srv, req.cmd)
    if special is not None:
        return special

    payload: Dict[str, Any] = {"cmd": req.cmd}
    if req.timeout is not None:
        payload["timeout"] = req.timeout
    if req.cwd is not None:
        payload["cwd"] = req.cwd
    if req.env:
        payload["env"] = req.env

    mode = info.get("mode", "webhook")
    tid = f"task-{int(time.time())}-{uuid.uuid4().hex[:6]}"

    if req.background:
        await _queue_or_fire_background(srv, info, payload, tid)
        return {"background": True, "task_id": tid, "status": "running"}

    if mode == "polling":
        _task_slot(srv, tid).update({"cmd": req.cmd, "cwd": req.cwd})
        queues.setdefault(srv, []).append({"id": tid, **payload})
        deadline = time.time() + SYNC_TIMEOUT_S
        while time.time() < deadline:
            res = results.get(srv, {}).pop(tid, None)
            if res is not None:
                background_tasks.setdefault(srv, {})[tid] = {"status": "completed", "task_id": tid, "cmd": req.cmd, "cwd": req.cwd, "result": _spill_single_result(srv, res, req.cmd), "completed_at": int(time.time())}
                return res
            await asyncio.sleep(0.5)
        return {"background": True, "task_id": tid, "status": "running", "message": "Command continues in background."}

    if mode == "websocket":
        return await ws_exec(srv, payload)

    task = asyncio.create_task(_webhook_exec(info, payload))
    try:
        result = await asyncio.wait_for(asyncio.shield(task), timeout=SYNC_TIMEOUT_S)
        background_tasks.setdefault(srv, {})[tid] = {"status": "completed", "task_id": tid, "cmd": req.cmd, "cwd": req.cwd, "result": _spill_single_result(srv, result, req.cmd), "completed_at": int(time.time())}
        return result
    except asyncio.TimeoutError:
        _task_slot(srv, tid).update({"cmd": req.cmd, "cwd": req.cwd})

        async def finish_later() -> None:
            try:
                result = await task
                background_tasks.setdefault(srv, {})[tid].update({"status": "completed", "result": _spill_single_result(srv, result, str(payload.get("cmd") or "")), "completed_at": int(time.time())})
            except Exception as e:
                background_tasks.setdefault(srv, {})[tid].update({"status": "failed", "error": str(e), "completed_at": int(time.time())})

        asyncio.create_task(finish_later())
        return {"background": True, "task_id": tid, "status": "running", "message": "Command continues in background."}
    except Exception as e:
        return {"error": str(e)}


@app.post("/bulk/exec", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
async def bulk_exec(req: BulkExec):
    out: Dict[str, Dict[str, Any]] = {}
    tasks: Dict[str, asyncio.Task] = {}
    for srv in req.servers:
        tasks[srv] = asyncio.create_task(_exec_single_server(srv, req))
    for srv, task in tasks.items():
        try:
            out[srv] = await task
        except Exception as e:
            out[srv] = {"error": str(e)}
            log.error("bulk_exec: fail srv=%s err=%s rid=%s\n%s", srv, e, rid(), traceback.format_exc())
    out = _spill_large_fields(out, req.cmd)
    return {"results": out}


async def ws_exec(srv: str, payload: dict) -> dict:
    ws = ws_sessions.get(srv)
    if ws is None:
        raise HTTPException(503, "websocket session is not connected")
    tid = f"task-{int(time.time())}-{uuid.uuid4().hex[:6]}"
    _task_slot(srv, tid).update({"cmd": payload.get("cmd"), "cwd": payload.get("cwd")})
    ws_results[tid] = {"event": asyncio.Event(), "result": None}
    try:
        await ws.send_json({"type": "exec", "id": tid, "payload": payload})
        await asyncio.wait_for(ws_results[tid]["event"].wait(), timeout=SYNC_TIMEOUT_S)
        result = ws_results[tid]["result"] or {"error": "empty websocket result"}
        background_tasks.setdefault(srv, {})[tid].update({"status": "completed", "result": _spill_single_result(srv, result, str(payload.get("cmd") or "")), "completed_at": int(time.time())})
        return result
    except asyncio.TimeoutError:
        return {"background": True, "task_id": tid, "status": "running", "message": "Command continues in background."}
    except RuntimeError as e:
        ws_sessions.pop(srv, None)
        raise HTTPException(503, f"websocket send failed: {e}") from e
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
        beat = Beat(**(hello.get("payload") or {}))
        current = len(servers) + (0 if beat.name in servers else 1)
        _check_license(current)
        srv_name = beat.name
        if srv_name not in servers and not _is_approved(srv_name):
            rec = _pending_record(beat, reason="new_websocket_server")
            _remember_pending(rec)
            await websocket.send_json({"type": "hello_ack", "ok": False, "status": "pending"})
            await websocket.close(code=1008, reason="server pending approval")
            return
        servers[srv_name] = beat.dict()
        servers[srv_name].update({"mode": "websocket", "time": time.time(), "status": "active"})
        ws_sessions[srv_name] = websocket
        await websocket.send_json({"type": "hello_ack", "ok": True, "mcp_agent_id": _virtual_shell_agent_id(srv_name)})
        log.info("ws: connected srv=%s", srv_name)

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
                elif srv_name and tid in background_tasks.get(srv_name, {}):
                    background_tasks[srv_name][tid].update({"status": "completed", "result": msg.get("result"), "completed_at": int(time.time())})
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


def _verify_queue_signature(request: Request, srv: str, body: bytes) -> None:
    info = servers.get(srv) or {}
    approved = approved_servers.get(srv) or {}
    server_id = info.get("server_id") or approved.get("server_id")
    pub = info.get("public_key") or approved.get("public_key")
    ts = request.headers.get("X-GPTAdmin-Timestamp")
    nonce = request.headers.get("X-GPTAdmin-Nonce")
    sig = request.headers.get("X-GPTAdmin-Signature")
    server_header = request.headers.get("X-GPTAdmin-Server")
    server_id_header = request.headers.get("X-GPTAdmin-Server-ID")
    if server_header != srv or (server_id and server_id_header != server_id):
        raise HTTPException(401, "signed queue identity headers mismatch")
    if not pub:
        raise HTTPException(401, "missing approved public key")
    if not ts or not nonce or not sig:
        raise HTTPException(401, "missing signed queue headers")
    try:
        SIGNATURE_NONCES.check_and_store(f"queue:{srv}:{server_id_header}", nonce)
        verify_signature(pub, request.method, request.url.path, ts, nonce, body, sig)
    except Exception as e:
        raise HTTPException(401, f"invalid signed queue request: {e}") from e


@app.get("/queue/{srv}", dependencies=[Depends(ensure_license)])
async def queue_poll(request: Request, srv: str):
    _verify_queue_signature(request, srv, b"")
    q = queues.get(srv)
    if not q:
        return {}
    return q.pop(0)


@app.post("/queue/{srv}/progress", dependencies=[Depends(ensure_license)])
async def queue_progress(request: Request, srv: str):
    body = await request.body()
    _verify_queue_signature(request, srv, body)
    try:
        progress = json.loads(body or b"{}")
    except Exception as e:
        raise HTTPException(400, f"invalid progress json: {e}") from e
    if not isinstance(progress, dict):
        raise HTTPException(400, "invalid progress payload")

    task_id = str(progress.get("id") or "")
    event_type = str(progress.get("type") or "")
    if not task_id:
        raise HTTPException(400, "missing progress id")
    task = background_tasks.setdefault(srv, {}).setdefault(task_id, {"status": "running", "task_id": task_id, "created_at": int(time.time())})
    if task.get("status") == "completed":
        return {"ok": True, "ignored": "completed"}
    result = task.setdefault("result", {})
    result.setdefault("stdout", "")
    result.setdefault("stderr", "")
    if event_type in {"stdout", "stderr"}:
        result[event_type] = str(result.get(event_type) or "") + str(progress.get("data") or "")
    elif event_type == "event" and isinstance(progress.get("event"), dict):
        result.update(progress["event"])
    task.update({"status": "running", "updated_at": int(time.time())})
    return {"ok": True}


@app.post("/queue/{srv}/result", dependencies=[Depends(ensure_license)])
async def queue_result(request: Request, srv: str, res: TaskResult):
    body = await request.body()
    _verify_queue_signature(request, srv, body)
    results.setdefault(srv, {})[res.id] = res.result
    if res.id in background_tasks.get(srv, {}):
        background_tasks[srv][res.id].update({"status": "completed", "result": _spill_single_result(srv, res.result, str(background_tasks[srv][res.id].get("cmd") or "")), "completed_at": int(time.time()), "updated_at": int(time.time())})
    return {"ok": True}


@app.api_route(
    "/srv/{path:path}",
    methods=["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"],
    dependencies=[Depends(check_ctl_token), Depends(ensure_license)],
)
async def proxy(path: str, request: Request, srv: str = Query(..., alias="server")):
    info = servers.get(srv)
    if not info:
        raise HTTPException(404, f"server '{srv}' not registered")
    if info.get("mode") in {"polling", "websocket"}:
        if request.method != "POST" or path != "exec":
            raise HTTPException(501, f"{info.get('mode')} mode supports only POST /exec")
        data = ExecReq(**(await request.json()))
        return await _exec_single_server(srv, BulkExec(servers=[srv], cmd=data.cmd, cwd=data.cwd, env=data.env, timeout=data.timeout))

    target_url = f"{str(info['base_url']).rstrip('/')}/{path}"
    q = [(k, v) for k, v in request.query_params.multi_items() if k != "server"]
    if q:
        target_url += "?" + urlencode(q, doseq=True)

    body = await request.body()
    headers = dict(request.headers)
    headers.pop("authorization", None)
    for hk in list(headers):
        if hk.lower().startswith("x-gptadmin-"):
            headers.pop(hk, None)
    headers.update(_signed_rootd_headers(request.method, "/" + path, body))

    async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
        try:
            r = await client.request(request.method, target_url, content=body, headers=headers)
        except httpx.RequestError as e:
            raise HTTPException(502, f"proxy error: {e}") from e

    filtered_headers = {k: v for k, v in r.headers.items() if k.lower() not in {"content-encoding", "transfer-encoding", "connection"}}
    return Response(content=r.content, status_code=r.status_code, headers=filtered_headers, media_type=r.headers.get("content-type"))


# ---------------------------------------------------------------------------
# MCP relay: real agents + virtual shell agents
# ---------------------------------------------------------------------------


def _mcp_relay_agent_auth(request: Request) -> None:
    auth = request.headers.get("authorization", "")
    expected = f"Bearer {MCP_RELAY_AGENT_TOKEN}"
    if not MCP_RELAY_AGENT_TOKEN or not hmac.compare_digest(auth, expected):
        raise HTTPException(401, "bad relay token")


def _virtual_shell_agent_id(server_name: str) -> str:
    return f"{VIRTUAL_SHELL_PREFIX}{server_name}"


def _is_virtual_shell_agent(agent_id: str) -> bool:
    return agent_id.startswith(VIRTUAL_SHELL_PREFIX)


def _server_from_virtual_shell_agent(agent_id: str) -> str:
    if not _is_virtual_shell_agent(agent_id):
        raise HTTPException(400, f"not a virtual shell agent: {agent_id}")
    return agent_id[len(VIRTUAL_SHELL_PREFIX) :]


def _mcp_relay_public_agent(agent_id: str, info: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "agent_id": agent_id,
        "name": info.get("name") or agent_id,
        "kind": "real_mcp",
        "transport": info.get("transport", "stdio"),
        "status": "online" if time.time() - float(info.get("last_seen", 0)) <= DEAD_S else "offline",
        "last_seen": info.get("last_seen"),
        "capabilities": info.get("capabilities") or [],
        "meta": info.get("meta") or {},
    }


def _virtual_hub_agent() -> Dict[str, Any]:
    return {
        "agent_id": VIRTUAL_HUB_AGENT_ID,
        "name": "GPTAdmin Hub",
        "kind": "virtual_hub",
        "transport": "internal",
        "status": "online",
        "last_seen": int(time.time()),
        "capabilities": ["registry", "pending_servers"],
        "meta": {"pending_count": len(pending_servers), "server_count": len(servers)},
    }


def _virtual_shell_agents() -> List[Dict[str, Any]]:
    now = time.time()
    agents = []
    for name, info in servers.items():
        alive = (now - float(info.get("time", 0))) < DEAD_S
        agents.append(
            {
                "agent_id": _virtual_shell_agent_id(name),
                "name": f"Shell: {name}",
                "kind": "virtual_shell",
                "transport": str(info.get("mode", "webhook")),
                "status": "online" if alive else "offline",
                "last_seen": info.get("time"),
                "capabilities": ["shell", "system", "tasks", "logs"],
                "meta": _sanitize_server({**info, "server_name": name}),
            }
        )
    return agents


def _all_public_agents() -> List[Dict[str, Any]]:
    agents = [_virtual_hub_agent()]
    agents.extend(_virtual_shell_agents())
    agents.extend(_mcp_relay_public_agent(agent_id, info) for agent_id, info in mcp_relay_agents.items())
    agents.sort(key=lambda x: (x.get("status") != "online", str(x.get("agent_id"))))
    return agents


def _mcp_relay_select_agent(target: Optional[str] = None) -> str:
    """Validate and normalize an explicit MCP target.

    Args:
        target: Agent id returned by ``listMcpAgents``.

    Returns:
        The validated target id.

    Raises:
        HTTPException: If the target is missing, reserved, or unknown.
    """

    if not target or target == "default":
        raise HTTPException(
            400,
            "Explicit MCP target is required. Call listMcpAgents first and pass one returned agent_id. There is no default target.",
        )

    if target == VIRTUAL_HUB_AGENT_ID:
        return target

    if _is_virtual_shell_agent(target):
        srv = _server_from_virtual_shell_agent(target)
        if srv not in servers:
            raise HTTPException(404, f"unknown shell server {srv}")
        return target

    if target not in mcp_relay_agents:
        raise HTTPException(404, f"unknown MCP relay agent {target}")

    return target


def _mcp_envelope_text(text: str, structured: Dict[str, Any]) -> Dict[str, Any]:
    return {"content": [{"type": "text", "text": text}], "structuredContent": structured}


def _hub_tools_list() -> Dict[str, Any]:
    tools = [
        {
            "name": "list_servers",
            "description": "List legacy rootd servers exposed as virtual shell agents.",
            "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
        },
        {
            "name": "list_pending_servers",
            "description": "List rootd servers pending approval.",
            "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
        },
        {
            "name": "approve_pending_server",
            "description": "Approve a pending rootd server by name.",
            "inputSchema": {
                "type": "object",
                "properties": {"name": {"type": "string"}},
                "required": ["name"],
                "additionalProperties": False,
            },
        },
        {
            "name": "reject_pending_server",
            "description": "Reject a pending rootd server by name.",
            "inputSchema": {
                "type": "object",
                "properties": {"name": {"type": "string"}},
                "required": ["name"],
                "additionalProperties": False,
            },
        },
    ]
    return {"tools": tools}


def _shell_tools_list() -> Dict[str, Any]:
    tools = [
        {
            "name": "shell_exec",
            "description": "Execute a shell command on this server. Use background=true for long commands.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "cmd": {"type": "string"},
                    "cwd": {"type": ["string", "null"]},
                    "timeout": {"type": ["integer", "null"]},
                    "env": {"type": ["object", "null"], "additionalProperties": True},
                    "background": {"type": "boolean", "default": False},
                },
                "required": ["cmd"],
                "additionalProperties": False,
            },
        },
        {
            "name": "task_status",
            "description": "Get status/result for a shell background task returned by shell_exec.",
            "inputSchema": {
                "type": "object",
                "properties": {"task_id": {"type": "string"}, "ack": {"type": "boolean", "default": False}},
                "required": ["task_id"],
                "additionalProperties": False,
            },
        },
    ]
    return {"tools": tools}


def _legacy_get_task(srv: str, tid: str, ack: bool = False) -> Optional[Dict[str, Any]]:
    task = background_tasks.get(srv, {}).get(tid)
    if task is None:
        res = results.get(srv, {}).get(tid)
        if res is not None:
            task = {"status": "completed", "task_id": tid, "server": srv, "result": res}
    if task is None:
        return None
    out = {"server": srv, "task_id": tid, **task}
    if ack and out.get("status") in {"completed", "failed", "orphaned"}:
        background_tasks.get(srv, {}).pop(tid, None)
        results.get(srv, {}).pop(tid, None)
        out["acked"] = True
    else:
        out["acked"] = False
    return out


async def _hub_tool_call(tool_name: str, args: Dict[str, Any]) -> Dict[str, Any]:
    if tool_name == "list_servers":
        data = list_servers(include_pending=True)
        return _mcp_envelope_text(f"Found {len(data.get('servers', []))} servers", data)
    if tool_name == "list_pending_servers":
        data = {"pending": list(pending_servers.values()), "count": len(pending_servers)}
        return _mcp_envelope_text(f"Found {len(pending_servers)} pending servers", data)
    if tool_name == "approve_pending_server":
        name = str(args.get("name") or "")
        ctx = audit_request_context()
        data = _approve_pending_server(
            name,
            approved_by="mcp hub tool",
            approved_via="mcp_hub_tool:approve_pending_server",
            approved_subject=str(args.get("requested_by") or args.get("subject") or ctx.get("token_id") or ctx.get("rid") or "unknown"),
        )
        return _mcp_envelope_text(f"Approve result for {name}: {data.get('status') or data.get('error')}", data)
    if tool_name == "reject_pending_server":
        name = str(args.get("name") or "")
        data = _reject_pending_server(name)
        return _mcp_envelope_text(f"Reject result for {name}: {data.get('status') or data.get('error')}", data)
    raise HTTPException(404, f"unknown hub tool {tool_name}")


async def _virtual_shell_tool_call(agent_id: str, tool_name: str, args: Dict[str, Any], request_background: bool = False) -> Dict[str, Any]:
    srv = _server_from_virtual_shell_agent(agent_id)
    if tool_name == "shell_exec":
        cmd = args.get("cmd")
        if not cmd:
            raise HTTPException(400, "shell_exec requires cmd")
        requested_timeout = args.get("timeout")
        try:
            long_timeout = requested_timeout is not None and int(requested_timeout) > SYNC_TIMEOUT_S
        except Exception:
            long_timeout = False
        background = bool(args.get("background", False) or request_background or long_timeout)
        req = BulkExec(
            servers=[srv],
            cmd=str(cmd),
            cwd=args.get("cwd"),
            timeout=args.get("timeout"),
            env=args.get("env") if isinstance(args.get("env"), dict) else None,
            background=background,
        )
        data = await bulk_exec(req)
        result = (data.get("results") or {}).get(srv, {})
        if isinstance(result, dict) and result.get("background") and result.get("task_id"):
            job_id = f"mcp-shell-{int(time.time())}-{uuid.uuid4().hex[:8]}"
            mcp_relay_jobs[job_id] = {
                "job_id": job_id,
                "kind": "virtual_shell_task",
                "agent_id": agent_id,
                "server": srv,
                "task_id": result["task_id"],
                "status": "running",
                "created_at": int(time.time()),
                "tool_name": tool_name,
            }
            return {"background": True, "job_id": job_id, "status": "running", "message": "Shell command continues in background."}
        return _mcp_envelope_text(f"shell_exec completed on {srv}", {"server": srv, "result": result})

    if tool_name == "task_status":
        tid = str(args.get("task_id") or "")
        task = _legacy_get_task(srv, tid, ack=bool(args.get("ack")))
        if not task:
            return _mcp_envelope_text(f"Task not found: {tid}", {"server": srv, "task_id": tid, "status": "not_found"})
        return _mcp_envelope_text(f"Task {tid}: {task.get('status')}", task)

    raise HTTPException(404, f"unknown shell tool {tool_name}")


def _mcp_relay_tool_name(method: str, params: Optional[Dict[str, Any]] = None) -> str:
    params = params or {}
    if method == "tools/call":
        return str(params.get("name") or "")
    return method


def _mcp_relay_enqueue(agent_id: str, method: str, params: Optional[Dict[str, Any]] = None) -> str:
    info = mcp_relay_agents.get(agent_id)
    if not info:
        raise HTTPException(404, f"unknown MCP relay agent {agent_id}")
    if time.time() - float(info.get("last_seen", 0)) > DEAD_S:
        raise HTTPException(503, f"MCP relay agent {agent_id} is offline")
    job_id = f"mcp-{int(time.time())}-{uuid.uuid4().hex[:8]}"
    params = params or {}
    tool_name = _mcp_relay_tool_name(method, params)
    payload = {"id": job_id, "jsonrpc": "2.0", "method": method, "params": params, "created_at": int(time.time())}
    mcp_relay_jobs[job_id] = {
        "job_id": job_id,
        "kind": "real_mcp",
        "agent_id": agent_id,
        "method": method,
        "tool_name": tool_name,
        "params": params,
        "status": "queued",
        "created_at": int(time.time()),
    }
    mcp_relay_queues.setdefault(agent_id, []).append(payload)
    log.info(
        "mcp_relay: queued target=%s method=%s tool=%s job_id=%s queue_len=%s",
        agent_id, method, tool_name, job_id, len(mcp_relay_queues.get(agent_id, [])),
    )
    _audit_event({"event":"mcp_relay_queued","target":agent_id,"method":method,"tool_name":tool_name,"job_id":job_id,"queue_len":len(mcp_relay_queues.get(agent_id, []))})
    return job_id


async def _mcp_relay_wait(job_id: str, timeout: Optional[int] = None) -> Optional[Dict[str, Any]]:
    wait_s = min(int(timeout or MCP_RELAY_DEFAULT_TIMEOUT), SYNC_TIMEOUT_S, MCP_RELAY_SYNC_WAIT_MAX_S)
    deadline = time.time() + wait_s
    job = mcp_relay_jobs.get(job_id)
    if job:
        job["status"] = "running"
        log.info(
            "mcp_relay: wait start target=%s method=%s tool=%s job_id=%s timeout_s=%s",
            job.get("agent_id"), job.get("method"), job.get("tool_name"), job_id, wait_s,
        )
    while time.time() < deadline:
        result = mcp_relay_results.get(job_id)
        if result is not None:
            ok = bool(result.get("ok", True))
            if job:
                job.update({"status": "completed" if ok else "failed", "result": result, "completed_at": int(time.time())})
                log.info(
                    "mcp_relay: wait done target=%s method=%s tool=%s job_id=%s ok=%s",
                    job.get("agent_id"), job.get("method"), job.get("tool_name"), job_id, ok,
                )
            if ok:
                return result.get("result") or {}
            return {"error": result.get("error") or {"message": "MCP relay job failed"}, "job_id": job_id}
        await asyncio.sleep(0.25)
    if job:
        log.info(
            "mcp_relay: wait background target=%s method=%s tool=%s job_id=%s timeout_s=%s",
            job.get("agent_id"), job.get("method"), job.get("tool_name"), job_id, wait_s,
        )
        _audit_event({"event":"mcp_relay_background","target":job.get("agent_id"),"method":job.get("method"),"tool_name":job.get("tool_name"),"job_id":job_id,"timeout_s":wait_s})
    return None


@app.post("/mcp-relay/register", dependencies=[Depends(ensure_license)])
async def mcp_relay_register(req: McpRelayRegister, request: Request):
    _mcp_relay_agent_auth(request)
    if req.agent_id == VIRTUAL_HUB_AGENT_ID or _is_virtual_shell_agent(req.agent_id):
        raise HTTPException(400, "agent_id is reserved")
    mcp_relay_agents[req.agent_id] = {
        "agent_id": req.agent_id,
        "name": req.name or req.agent_id,
        "transport": req.transport,
        "command": req.command,
        "capabilities": req.capabilities or [],
        "meta": req.meta or {},
        "last_seen": time.time(),
    }
    return {"ok": True, "agent": _mcp_relay_public_agent(req.agent_id, mcp_relay_agents[req.agent_id])}


@app.get("/mcp-relay/poll/{agent_id}", dependencies=[Depends(ensure_license)])
async def mcp_relay_poll(agent_id: str, request: Request, timeout: int = Query(55)):
    _mcp_relay_agent_auth(request)
    if agent_id == VIRTUAL_HUB_AGENT_ID or _is_virtual_shell_agent(agent_id):
        raise HTTPException(400, "virtual agents do not poll")
    info = mcp_relay_agents.setdefault(agent_id, {"agent_id": agent_id, "name": agent_id, "transport": "stdio", "capabilities": [], "meta": {}})
    info["last_seen"] = time.time()
    deadline = time.time() + min(max(timeout, 1), MCP_RELAY_POLL_MAX_TIMEOUT)
    while time.time() < deadline:
        q = mcp_relay_queues.get(agent_id) or []
        if q:
            job = q.pop(0)
            if job.get("id") in mcp_relay_jobs:
                mcp_relay_jobs[job["id"]]["status"] = "running"
            return job
        await asyncio.sleep(0.5)
    return {}


@app.post("/mcp-relay/result/{agent_id}", dependencies=[Depends(ensure_license)])
async def mcp_relay_result(agent_id: str, res: McpRelayResult, request: Request):
    _mcp_relay_agent_auth(request)
    if agent_id in mcp_relay_agents:
        mcp_relay_agents[agent_id]["last_seen"] = time.time()
    payload = {"ok": res.ok, "result": res.result, "error": res.error, "completed_at": int(time.time()), "agent_id": agent_id}
    mcp_relay_results[res.id] = payload
    job = mcp_relay_jobs.get(res.id)
    if job:
        job.update({"status": "completed" if res.ok else "failed", "result": payload, "completed_at": int(time.time())})
        log.info(
            "mcp_relay: result target=%s method=%s tool=%s job_id=%s ok=%s",
            job.get("agent_id"), job.get("method"), job.get("tool_name"), res.id, res.ok,
        )
        _audit_event({"event":"mcp_relay_result","target":job.get("agent_id"),"method":job.get("method"),"tool_name":job.get("tool_name"),"job_id":res.id,"ok":res.ok})
    else:
        log.info("mcp_relay: result target=%s job_id=%s ok=%s job=unknown", agent_id, res.id, res.ok)
        _audit_event({"event":"mcp_relay_result","target":agent_id,"job_id":res.id,"ok":res.ok,"job":"unknown"})
    return {"ok": True}


@app.get("/mcp-relay/agents", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def mcp_relay_agents_list():
    agents = _all_public_agents()
    return {"agents": agents}


@app.post("/mcp-relay/tools", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
async def mcp_relay_tools(req: McpRelayToolsReq):
    target = _mcp_relay_select_agent(req.target)
    log.info("mcp_relay: tools/list request target=%s background=%s timeout=%s", target, req.background, req.timeout)
    if target == VIRTUAL_HUB_AGENT_ID:
        return {"agent_id": target, "status": "completed", "response": _hub_tools_list()}
    if _is_virtual_shell_agent(target):
        return {"agent_id": target, "status": "completed", "response": _shell_tools_list()}

    job_id = _mcp_relay_enqueue(target, "tools/list", {})
    if req.background or (req.timeout is not None and req.timeout > MCP_RELAY_SYNC_WAIT_MAX_S):
        return {"agent_id": target, "status": "running", "background": True, "job_id": job_id, "message": "tools/list queued"}
    data = await _mcp_relay_wait(job_id, req.timeout)
    if data is None:
        return {"agent_id": target, "status": "running", "background": True, "job_id": job_id, "message": "tools/list still running"}
    return {"agent_id": target, "status": "completed", "response": data}


@app.post("/mcp-relay/call", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
async def mcp_relay_call(req: McpRelayCallReq):
    target = _mcp_relay_select_agent(req.target)
    log.info(
        "mcp_relay: call request target=%s tool=%s background=%s timeout=%s",
        target, req.tool_name, req.background, req.timeout,
    )
    if target == VIRTUAL_HUB_AGENT_ID:
        data = await _hub_tool_call(req.tool_name, req.arguments or {})
        return {"agent_id": target, "status": "completed", "response": data}
    if _is_virtual_shell_agent(target):
        args = req.arguments or {}
        arg_timeout = args.get("timeout") if isinstance(args, dict) else None
        try:
            long_timeout = (req.timeout is not None and req.timeout > MCP_RELAY_SYNC_WAIT_MAX_S) or (arg_timeout is not None and int(arg_timeout) > SYNC_TIMEOUT_S)
        except Exception:
            long_timeout = bool(req.timeout is not None and req.timeout > MCP_RELAY_SYNC_WAIT_MAX_S)
        data = await _virtual_shell_tool_call(target, req.tool_name, args, request_background=bool(req.background or long_timeout))
        if isinstance(data, dict) and data.get("background"):
            return {"agent_id": target, "status": "running", **data}
        return {"agent_id": target, "status": "completed", "response": data}

    job_id = _mcp_relay_enqueue(target, "tools/call", {"name": req.tool_name, "arguments": req.arguments or {}})
    if req.background or (req.timeout is not None and req.timeout > MCP_RELAY_SYNC_WAIT_MAX_S):
        return {"agent_id": target, "status": "running", "background": True, "job_id": job_id, "message": "tool call queued"}
    data = await _mcp_relay_wait(job_id, req.timeout)
    if data is None:
        return {"agent_id": target, "status": "running", "background": True, "job_id": job_id, "message": "MCP relay job is still running"}
    return {"agent_id": target, "status": "completed", "response": data}


@app.get("/mcp-relay/job/{job_id}", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def mcp_relay_job_status(job_id: str, ack: bool = Query(False)):
    job = mcp_relay_jobs.get(job_id)
    acked = False

    if job and job.get("kind") == "virtual_shell_task":
        srv = str(job.get("server"))
        tid = str(job.get("task_id"))
        task = _legacy_get_task(srv, tid, ack=ack)
        status = task.get("status") if task else "running_or_unknown"
        response = task
        if task and status in {"completed", "failed", "orphaned"}:
            job["status"] = status
            job["result"] = task
            job["completed_at"] = task.get("completed_at") or int(time.time())
            if ack:
                mcp_relay_jobs.pop(job_id, None)
                acked = True
        return {"job_id": job_id, "status": status, "agent_id": job.get("agent_id"), "response": response, "error": None, "acked": acked}

    result = mcp_relay_results.get(job_id)
    if result is not None:
        status = "completed" if result.get("ok", True) else "failed"
        response = result.get("result")
        error = result.get("error")
        agent_id = result.get("agent_id") or (job or {}).get("agent_id")
        if ack and status in {"completed", "failed"}:
            mcp_relay_results.pop(job_id, None)
            mcp_relay_jobs.pop(job_id, None)
            acked = True
        return {"job_id": job_id, "status": status, "agent_id": agent_id, "response": response, "error": error, "acked": acked}

    if job:
        return {"job_id": job_id, "status": job.get("status", "running"), "agent_id": job.get("agent_id"), "response": job.get("result"), "error": job.get("error"), "acked": False}

    return {"job_id": job_id, "status": "running_or_unknown", "agent_id": None, "response": None, "error": None, "acked": False}


# ---------------------------------------------------------------------------
# OAuth / Apps SDK MCP endpoint retained from previous architecture
# ---------------------------------------------------------------------------


def _b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def _b64url_json(obj: Any) -> str:
    return _b64url(json.dumps(obj, separators=(",", ":")).encode())


def _sign_jwt(payload: Dict[str, Any]) -> str:
    header = {"alg": "HS256", "typ": "JWT"}
    now = int(time.time())
    body = {**payload, "iss": PUBLIC_ORIGIN, "aud": MCP_RESOURCE, "iat": now, "exp": now + 12 * 3600}
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
        u = urlparse(uri)
        if u.hostname in ("localhost", "127.0.0.1") and u.scheme in ("http", "https"):
            return True
    except Exception:
        pass
    try:
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
        headers={"WWW-Authenticate": f'Bearer resource_metadata="{PUBLIC_ORIGIN}/.well-known/oauth-protected-resource", scope="{" ".join(OAUTH_SCOPES)}"'},
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
    return {"client_id": "chatgpt-dynamic", "token_endpoint_auth_method": "none", "grant_types": ["authorization_code"], "response_types": ["code"]}


@app.get("/authorize")
def oauth_authorize_get(request: Request):
    q = request.query_params
    redirect_uri = q.get("redirect_uri")
    resource = (q.get("resource") or MCP_RESOURCE).rstrip("/")
    if not _is_chatgpt_redirect(redirect_uri) or resource != MCP_RESOURCE:
        return JSONResponse({"error": "invalid_request", "error_description": "invalid redirect_uri or resource"}, status_code=400)
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
        return JSONResponse({"error": "access_denied", "error_description": "invalid password"}, status_code=403)
    redirect_uri = params.get("redirect_uri")
    resource = (params.get("resource") or MCP_RESOURCE).rstrip("/")
    if not _is_chatgpt_redirect(redirect_uri) or resource != MCP_RESOURCE:
        return JSONResponse({"error": "invalid_request", "error_description": "invalid redirect_uri or resource"}, status_code=400)
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
    resource = params.get("resource") or ((data or {}).get("resource") or MCP_RESOURCE).rstrip("/")
    if not data or time.time() - data.get("created", 0) > 300 or resource != MCP_RESOURCE or resource != data.get("resource"):
        return JSONResponse({"error": "invalid_grant", "error_description": "code not found, expired, or resource mismatch"}, status_code=400)
    if not _pkce_ok(params.get("code_verifier", ""), data.get("challenge", "")):
        return JSONResponse({"error": "invalid_grant", "error_description": "PKCE verification failed"}, status_code=400)
    token = _sign_jwt({"sub": "admin", "scope": data.get("scope"), "client_id": data.get("client_id")})
    return {"access_token": token, "token_type": "Bearer", "expires_in": 43200}


def _apps_sdk_tools() -> List[Dict[str, Any]]:
    # Apps SDK surface mirrors the reduced MCP relay model.
    template_uri = "ui://widget/admin-v3.html"
    widget_domain = "https://widgets-gptadmin.bezrabotnyi.com"
    widget_csp = {"connectDomains": [PUBLIC_ORIGIN], "resourceDomains": [widget_domain]}
    legacy_widget_csp = {"connect_domains": [PUBLIC_ORIGIN], "resource_domains": [widget_domain]}
    base_meta = {
        "ui": {"resourceUri": template_uri, "domain": widget_domain, "csp": widget_csp},
        "openai/outputTemplate": template_uri,
        "openai/widgetDomain": widget_domain,
        "openai/widgetCSP": legacy_widget_csp,
    }
    return [
        {
            "name": "list_mcp_agents",
            "title": "List MCP agents",
            "description": "List real relay agents and virtual shell agents.",
            "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
            "outputSchema": {"type": "object", "properties": {"agents": {"type": "array", "items": {"type": "object", "additionalProperties": True}}}, "required": ["agents"], "additionalProperties": False},
            "annotations": {"readOnlyHint": True},
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.read"]}],
            "_meta": base_meta,
        },
        {
            "name": "list_mcp_tools",
            "title": "List tools",
            "description": "List tools available on a selected agent.",
            "inputSchema": {
                "type": "object",
                "properties": {"target": {"type": "string", "description": "Explicit agent id from list_mcp_agents. There is no default target."}},
                "required": ["target"],
                "additionalProperties": False,
            },
            "outputSchema": {"type": "object", "properties": {"agent_id": {"type": "string"}, "status": {"type": "string"}, "response": {"type": "object", "additionalProperties": True}}, "required": ["agent_id", "status"], "additionalProperties": True},
            "annotations": {"readOnlyHint": True},
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.read"]}],
            "_meta": base_meta,
        },
        {
            "name": "call_mcp_tool",
            "title": "Call tool",
            "description": "Call a tool on one explicitly selected agent. Use background=true for long operations.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "target": {"type": "string", "description": "Explicit agent id from list_mcp_agents. There is no default target."},
                    "tool_name": {"type": "string", "description": "Tool name returned by list_mcp_tools for the same target."},
                    "arguments": {"type": "object", "additionalProperties": True},
                    "background": {"type": "boolean", "default": False},
                    "timeout": {"type": ["integer", "null"]},
                },
                "required": ["target", "tool_name"],
                "additionalProperties": False,
            },
            "outputSchema": {"type": "object", "properties": {"agent_id": {"type": "string"}, "status": {"type": "string"}, "response": {"type": "object", "additionalProperties": True}, "background": {"type": "boolean"}, "job_id": {"type": "string"}}, "required": ["agent_id", "status"], "additionalProperties": True},
            "annotations": {"readOnlyHint": False, "openWorldHint": True, "destructiveHint": True},
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.exec"]}],
            "_meta": base_meta,
        },
        {
            "name": "get_mcp_job",
            "title": "Get job",
            "description": "Get status/result for a background MCP job.",
            "inputSchema": {"type": "object", "properties": {"job_id": {"type": "string"}, "ack": {"type": "boolean", "default": False}}, "required": ["job_id"], "additionalProperties": False},
            "outputSchema": {"type": "object", "properties": {"job_id": {"type": "string"}, "status": {"type": "string"}, "response": {"type": ["object", "null"], "additionalProperties": True}, "error": {"type": ["object", "string", "null"]}}, "required": ["job_id", "status"], "additionalProperties": True},
            "annotations": {"readOnlyHint": True},
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.read"]}],
            "_meta": base_meta,
        },
    ]


async def _apps_sdk_call_tool(name: str, args: Dict[str, Any]) -> Dict[str, Any]:
    if name == "list_mcp_agents":
        return mcp_relay_agents_list()
    if name == "list_mcp_tools":
        target = args.get("target")
        if not isinstance(target, str) or not target:
            raise HTTPException(400, "Explicit MCP target is required. Call list_mcp_agents first and pass one returned agent_id.")
        return await mcp_relay_tools(McpRelayToolsReq(target=target))
    if name == "call_mcp_tool":
        target = args.get("target")
        if not isinstance(target, str) or not target:
            raise HTTPException(400, "Explicit MCP target is required. Call list_mcp_agents first and pass one returned agent_id.")
        tool_name = args.get("tool_name") or args.get("name")
        if not isinstance(tool_name, str) or not tool_name:
            raise HTTPException(400, "tool_name is required")
        return await mcp_relay_call(
            McpRelayCallReq(
                target=target,
                tool_name=tool_name,
                arguments=args.get("arguments") or {},
                background=bool(args.get("background", False)),
                timeout=args.get("timeout"),
            )
        )
    if name == "get_mcp_job":
        return mcp_relay_job_status(str(args.get("job_id") or ""), ack=bool(args.get("ack", False)))
    raise HTTPException(404, f"unknown tool {name}")


@app.options("/mcp")
async def mcp_options():
    return Response(headers={"Access-Control-Allow-Origin": "*", "Access-Control-Allow-Headers": "authorization, content-type", "Access-Control-Allow-Methods": "GET, POST, OPTIONS"})


@app.get("/mcp")
async def mcp_get(request: Request):
    try:
        _mcp_auth(request)
    except HTTPException:
        return _mcp_unauthorized()
    return {"ok": True, "name": "GPTAdmin MCP", "tools": _apps_sdk_tools()}


@app.post("/mcp")
async def mcp_post(request: Request):
    try:
        _mcp_auth(request)
    except HTTPException:
        return _mcp_unauthorized()
    body = await request.json()
    method = body.get("method")
    params = body.get("params") or {}
    req_id = body.get("id")
    try:
        if method == "initialize":
            result = {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}}, "serverInfo": {"name": "gptadmin-hub", "version": str(BUILD_VERSION)}}
        elif method == "tools/list":
            result = {"tools": _apps_sdk_tools()}
        elif method == "tools/call":
            tool_name = params.get("name")
            args = params.get("arguments") or {}
            result = await _apps_sdk_call_tool(tool_name, args)
        else:
            return {"jsonrpc": "2.0", "id": req_id, "error": {"code": -32601, "message": f"unknown method {method}"}}
        return {"jsonrpc": "2.0", "id": req_id, "result": result}
    except HTTPException as e:
        return {"jsonrpc": "2.0", "id": req_id, "error": {"code": e.status_code, "message": str(e.detail)}}
    except Exception as e:
        log.exception("mcp_post failed")
        return {"jsonrpc": "2.0", "id": req_id, "error": {"code": -32000, "message": str(e)}}


# ---------------------------------------------------------------------------
# Legacy task endpoints, now mostly for humans/backward compatibility
# ---------------------------------------------------------------------------


@app.get("/tasks/{srv}/{tid}", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def get_task(srv: str, tid: str, ack: bool = Query(False)):
    task = _legacy_get_task(srv, tid, ack=ack)
    if not task:
        raise HTTPException(404, f"task not found: {tid}")
    return task


@app.post("/tasks/{srv}/{tid}/ack", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def ack_task(srv: str, tid: str):
    removed_task = background_tasks.get(srv, {}).pop(tid, None) is not None
    removed_result = results.get(srv, {}).pop(tid, None) is not None
    return {"ok": True, "status": "acknowledged" if removed_task or removed_result else "not_found", "server": srv, "task_id": tid, "removed_task": removed_task, "removed_result": removed_result}


@app.get("/tasks/{srv}", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def list_tasks(srv: str):
    return {"server": srv, "tasks": list(background_tasks.get(srv, {}).values())}


# ---------------------------------------------------------------------------
# Exception handlers / entry point
# ---------------------------------------------------------------------------


@app.exception_handler(HTTPException)
async def http_exception_handler(request: Request, exc: HTTPException):
    return Response(
        content=json.dumps({"detail": exc.detail, "status_code": exc.status_code}, ensure_ascii=False),
        status_code=exc.status_code,
        media_type="application/json",
    )


@app.exception_handler(Exception)
async def generic_exception_handler(request: Request, exc: Exception):
    log.error("unhandled error rid=%s path=%s err=%s\n%s", rid(), request.url.path, exc, traceback.format_exc())
    return Response(
        content=json.dumps({"detail": str(exc), "status_code": 500}, ensure_ascii=False),
        status_code=500,
        media_type="application/json",
    )


# ═══════════════════════════════════════════════════════════════════════════════
# ═══════════════════════════════════════════════════════════════════════════════
# MCP PROMPT BRIDGE — two endpoints only:
#   GET  /mcp-prompt/prompt?target=all  → compact (requires MCP_BRIDGE_KEY)
#   GET  /mcp-prompt/prompt?target=ID   → detailed (requires MCP_BRIDGE_KEY)
#   POST /mcp-prompt/call               → execute  (requires MCP_BRIDGE_KEY)
#
# /prompt exposes agent/tool inventory and is protected too.
# /call executes tools — MUST be protected. Set MCP_BRIDGE_KEY env var.
#   Default: MCP_BRIDGE_KEY = CTL_TOKEN  (locked down by default)
#   Set MCP_BRIDGE_KEY="" to open (DANGEROUS — anyone can run shell_exec).
#   Set MCP_BRIDGE_KEY="your-secret" for userscript to pass ?key=your-secret.
#
# Uses in-process functions directly — zero HTTP loopback, zero extra imports.
# ═══════════════════════════════════════════════════════════════════════════════

MCP_BRIDGE_KEY: str = os.getenv("MCP_BRIDGE_KEY", CTL_TOKEN)  # default: locked
MCP_PROMPT_CACHE_TTL: int = int(os.getenv("MCP_PROMPT_CACHE_TTL", "90"))

_bridge_cache: Dict[str, tuple[float, Any]] = {}


def _bridge_cached(key: str, factory, ttl: int = MCP_PROMPT_CACHE_TTL) -> Any:
    now = time.time()
    if key in _bridge_cache and _bridge_cache[key][0] + ttl > now:
        return _bridge_cache[key][1]
    data = factory()
    _bridge_cache[key] = (now, data)
    return data


async def _bridge_cached_async(key: str, factory, ttl: int = MCP_PROMPT_CACHE_TTL) -> Any:
    now = time.time()
    if key in _bridge_cache and _bridge_cache[key][0] + ttl > now:
        return _bridge_cache[key][1]
    data = await factory()
    _bridge_cache[key] = (now, data)
    return data


def _bridge_check_call_key(key: str) -> bool:
    """Auth for /call — must match MCP_BRIDGE_KEY."""
    return hmac.compare_digest(key, MCP_BRIDGE_KEY) if MCP_BRIDGE_KEY else True


def _tools_from_mcp_response(resp: Any) -> List[Dict[str, Any]]:
    if not resp:
        return []
    if isinstance(resp, dict):
        if "tools" in resp:
            return resp["tools"]
        for nk in ("response", "structuredContent", "result"):
            inner = resp.get(nk)
            if isinstance(inner, dict) and "tools" in inner:
                return inner["tools"]
    return []


# ── Prompt formatters ───────────────────────────────────────────────────────────

def _fmt_compact(tool: Dict[str, Any]) -> str:
    name = tool.get("name", "?")
    schema = tool.get("inputSchema") or tool.get("input_schema") or {}
    props = schema.get("properties", {})
    req = set(schema.get("required", []))
    params = []
    for p, d in props.items():
        ptype = d.get("type", "any")
        marker = "*" if p in req else "?"
        params.append(f"{p}{marker}:{ptype}")
    return f"{name}({', '.join(params)})" if params else f"{name}()"


def _fmt_detail(tool: Dict[str, Any]) -> str:
    name = tool.get("name", "?")
    desc = (tool.get("description") or "").split("\n")[0][:150]
    schema = tool.get("inputSchema") or tool.get("input_schema") or {}
    props = schema.get("properties", {})
    req = set(schema.get("required", []))
    params = []
    for p, d in props.items():
        ptype = d.get("type", "any")
        marker = "" if p in req else "?"
        params.append(f"{p}{marker}: {ptype}")
    ps = ", ".join(params) or "no args"
    return f"- {name}({ps}) — {desc}" if desc else f"- {name}({ps})"


# ── Tool fetching ───────────────────────────────────────────────────────────────

_SHELL_TOOLS: List[Dict[str, Any]] = [
    {
        "name": "shell_exec",
        "description": "Execute a shell command on the server.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "cmd": {"type": "string", "description": "Shell command to run"},
                "cwd": {"type": "string", "description": "Working directory"},
                "timeout": {"type": "integer", "description": "Timeout in seconds"},
                "env": {"type": "object", "description": "Environment variables"},
                "background": {"type": "boolean", "description": "Run in background"},
            },
            "required": ["cmd"],
        },
    },
    {
        "name": "task_status",
        "description": "Check status of a background task.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "task_id": {"type": "string", "description": "Task ID"},
                "ack": {"type": "boolean", "description": "Acknowledge and remove completed task"},
            },
            "required": ["task_id"],
        },
    },
]


async def _bridge_fetch_tools(agent_id: str) -> List[Dict[str, Any]]:
    try:
        if agent_id == VIRTUAL_HUB_AGENT_ID:
            return _hub_tools_list().get("tools", [])
        if _is_virtual_shell_agent(agent_id):
            return _SHELL_TOOLS
        async def _fetch():
            job_id = _mcp_relay_enqueue(agent_id, "tools/list", {})
            result = await _mcp_relay_wait(job_id, timeout=10)
            return _tools_from_mcp_response(result) if result else []
        return await _bridge_cached_async(f"bridge_tools:{agent_id}", _fetch)
    except Exception as e:
        log.debug("bridge: tools fetch failed for %s: %s", agent_id, e)
        return []


_BRIDGE_CORS = {
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type, Authorization",
}


# ── Endpoints ──────────────────────────────────────────────────────────────────

@app.api_route("/mcp-prompt/prompt", methods=["GET", "OPTIONS"])
async def mcp_prompt(request: Request, target: str = Query(default="all"), key: str = Query(default="")):
    """LLM-usable prompt. Requires ?key=MCP_BRIDGE_KEY because it exposes agent/tool inventory.
    target=all → compact; target=ID → detailed."""
    if request.method == "OPTIONS":
        return JSONResponse(status_code=200, headers=_BRIDGE_CORS)
    if not _bridge_check_call_key(key):
        return JSONResponse({"error": "unauthorized"}, status_code=401, headers=_BRIDGE_CORS)

    agents = _bridge_cached("bridge_agents", _all_public_agents)
    online = [a for a in agents if a.get("status") == "online"]

    if target == "all":
        results = await asyncio.gather(
            *[_bridge_fetch_tools(a["agent_id"]) for a in online])
        lines = [
            "You have MCP tools. To call a tool, output a JSON block "
            "inside ```mcp code fences like this:",
            "```mcp",
            '{"target":"AGENT_ID","tool":"TOOL_NAME","args":{...}}',
            "```",
            "Available agents and tools:",
        ]
        for a, tools in zip(online, results):
            aid = a["agent_id"]
            if tools:
                compact = ", ".join(_fmt_compact(t) for t in tools)
                lines.append(f"  {aid}: {compact}")
            else:
                lines.append(f"  {aid}: (unavailable)")
        return PlainTextResponse("\n".join(lines), headers=_BRIDGE_CORS)

    tools = await _bridge_fetch_tools(target)
    if not tools:
        return PlainTextResponse(
            f"No tools found for agent '{target}'. Is it online?",
            headers=_BRIDGE_CORS)
    lines = [
        f'You have MCP agent "{target}". To call a tool, output:',
        "```mcp",
        f'{{"target":"{target}","tool":"TOOL_NAME","args":{{...}}}}',
        "```",
        "Tools:",
    ]
    for t in tools:
        lines.append(_fmt_detail(t))
    return PlainTextResponse("\n".join(lines), headers=_BRIDGE_CORS)


@app.api_route("/mcp-prompt/call", methods=["POST", "OPTIONS"])
async def mcp_prompt_call(request: Request):
    """Execute a tool call. Requires ?key=MCP_BRIDGE_KEY (defaults to CTL_TOKEN).
    This endpoint hides CTL_TOKEN from the client — userscript only needs BRIDGE_KEY."""
    if request.method == "OPTIONS":
        return JSONResponse(status_code=200, headers=_BRIDGE_CORS)

    key = request.query_params.get("key", "")
    if not _bridge_check_call_key(key):
        return JSONResponse({"error": "unauthorized"}, status_code=401, headers=_BRIDGE_CORS)

    body = await request.json()
    target = body.get("target")
    tool = body.get("tool")
    args = body.get("args", {})
    if not target or not tool:
        return JSONResponse(
            {"error": "fields 'target' and 'tool' are required"},
            status_code=400, headers=_BRIDGE_CORS)

    try:
        validated = _mcp_relay_select_agent(target)

        if validated == VIRTUAL_HUB_AGENT_ID:
            result = await _hub_tool_call(tool, args)
        elif _is_virtual_shell_agent(validated):
            result = await _virtual_shell_tool_call(validated, tool, args)
        else:
            job_id = _mcp_relay_enqueue(
                validated, "tools/call", {"name": tool, "arguments": args})
            waited = await _mcp_relay_wait(job_id, timeout=MCP_RELAY_DEFAULT_TIMEOUT)
            if waited is not None:
                result = waited
            else:
                return JSONResponse(
                    {"status": "running", "job_id": job_id,
                     "message": "Running in background. Poll /mcp-relay/job/" + job_id},
                    headers=_BRIDGE_CORS)

        return JSONResponse({"status": "completed", "result": result}, headers=_BRIDGE_CORS)

    except HTTPException as e:
        return JSONResponse({"error": e.detail}, status_code=e.status_code, headers=_BRIDGE_CORS)
    except Exception as e:
        log.error("bridge: call failed target=%s tool=%s err=%s", target, tool, e)
        return JSONResponse({"error": str(e)}, status_code=500, headers=_BRIDGE_CORS)

if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("HUB_PORT", "9001"))

    # Standard systemd socket activation: LISTEN_PID + LISTEN_FDS
    fd: Optional[int] = None
    try:
        listen_pid = int(os.getenv("LISTEN_PID", "0"))
        listen_fds = int(os.getenv("LISTEN_FDS", "0"))
        if listen_pid == os.getpid() and listen_fds >= 1:
            fd = 3  # SD_LISTEN_FDS_START
    except (ValueError, TypeError):
        pass

    # Backward compat: custom SYSTEMD_SOCKET_FD env var
    if fd is None:
        fd_env = os.getenv("SYSTEMD_SOCKET_FD")
        if fd_env:
            try:
                fd = int(fd_env)
            except ValueError:
                pass

    kwargs: Dict[str, Any] = {"log_level": LOG_LEVEL.lower()}

    if fd is not None:
        log.info("starting hub via systemd socket fd=%s (dead_s=%s, log_level=%s)", fd, DEAD_S, LOG_LEVEL)
        kwargs["fd"] = fd
    else:
        log.info("starting hub on 0.0.0.0:%s (dead_s=%s, log_level=%s)", port, DEAD_S, LOG_LEVEL)
        kwargs["host"] = "0.0.0.0"
        kwargs["port"] = port

    uvicorn.run(app, **kwargs)
