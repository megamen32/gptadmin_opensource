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
  shell_exec, tasks and task_edit. This lets GPT use one mental
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
import re
import secrets
import shlex
import socket as _socket_module
import subprocess
import sys
import time
import traceback
import uuid
from contextlib import asynccontextmanager
from contextvars import ContextVar
from pathlib import Path
from logging.handlers import WatchedFileHandler
from typing import Any, Dict, List, Optional
from urllib.parse import parse_qs, quote, urlencode, urlparse

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
    CONFIG_DIR = Path(__file__).resolve().parent / "config"

GPTADMIN_REPO_ROOT = Path(__file__).resolve().parent
GPTADMIN_CLI_PATH = Path(os.getenv("GPTADMIN_CLI_PATH", str(GPTADMIN_REPO_ROOT / "cli.py")))
GPTADMIN_PYTHON = Path(os.getenv("GPTADMIN_PYTHON", sys.executable))

LICENSE_FILE = Path(os.getenv("LICENSE_FILE") or str(CONFIG_DIR / "license.json"))
PUBLIC_KEY_FILE = Path(os.getenv("PUBLIC_KEY_FILE") or str(CONFIG_DIR / "public.pem"))


def _default_artifact_dir() -> Path:
    candidates = [
        Path.cwd() / "build",
        Path(__file__).resolve().parent / "build",
        Path(os.getenv("GPTADMIN_HOME", "/opt/gptadmin")) / "artifacts",
    ]
    for candidate in candidates:
        if candidate.exists():
            return candidate
    return candidates[0]


ARTIFACT_DIR = Path(os.getenv("GPTADMIN_ARTIFACT_DIR", str(_default_artifact_dir())))
APPROVED_SERVERS_FILE = Path(os.getenv("GPTADMIN_APPROVED_SERVERS_FILE", str(CONFIG_DIR / "approved_servers.json")))
PENDING_SERVERS_FILE = Path(os.getenv("GPTADMIN_PENDING_SERVERS_FILE", str(CONFIG_DIR / "pending_servers.json")))
HUB_TRANSFERS_DIR = Path(os.getenv("GPTADMIN_TRANSFERS_DIR", str(CONFIG_DIR / "transfers")))
TRANSFERS_DIR = HUB_TRANSFERS_DIR
HUB_PORT_FORWARDS_FILE = Path(os.getenv("GPTADMIN_PORT_FORWARDS_FILE", str(CONFIG_DIR / "port_forwards.json")))
PORT_FORWARDS_FILE = HUB_PORT_FORWARDS_FILE
HUB_HUB_FILE_TRANSFER_MAX_INLINE_BYTES = int(os.getenv("GPTADMIN_FILE_TRANSFER_MAX_INLINE_BYTES", "52428800"))
HUB_PRIVATE_KEY_FILE = Path(os.getenv("GPTADMIN_HUB_PRIVATE_KEY_FILE", str(CONFIG_DIR / "hub_ed25519")))
HUB_PUBLIC_KEY_FILE_ED25519 = Path(os.getenv("GPTADMIN_HUB_PUBLIC_KEY_FILE", str(CONFIG_DIR / "hub_ed25519.pub")))
HUB_ID = os.getenv("GPTADMIN_HUB_ID", "main-hub")

STATE_TTL_S = int(os.getenv("HUB_STATE_TTL_S", str(3 * 86400)))
MCP_RELAY_STALE_TTL_S = int(os.getenv("MCP_RELAY_STALE_TTL_S", str(3 * 86400)))
MCP_RELAY_STALE_RETENTION_S = int(os.getenv("MCP_RELAY_STALE_RETENTION_S", str(30 * 86400)))
HUB_DEFERRED_DISPATCH_INTERVAL_S = float(os.getenv("HUB_DEFERRED_DISPATCH_INTERVAL_S", "2"))
HUB_DEFERRED_DEFAULT_TTL_S = int(os.getenv("HUB_DEFERRED_DEFAULT_TTL_S", str(7 * 86400)))
HUB_DEFERRED_MAX_ATTEMPTS = int(os.getenv("HUB_DEFERRED_MAX_ATTEMPTS", "1000"))
SYNC_TIMEOUT_S = max(1, int(os.getenv("HUB_SYNC_TIMEOUT_S", "15")))
MCP_RELAY_SYNC_WAIT_MAX_S = int(os.getenv("MCP_RELAY_SYNC_WAIT_MAX_S", str(min(SYNC_TIMEOUT_S, 15))))
MCP_RELAY_REQUEST_TIMEOUT_MAX_S = int(os.getenv("MCP_RELAY_REQUEST_TIMEOUT_MAX_S", "3600"))
MCP_RELAY_RUNNING_REQUEUE_S = int(os.getenv("MCP_RELAY_RUNNING_REQUEUE_S", "300"))
MCP_RELAY_NO_RETRY_TTL_S = int(os.getenv("MCP_RELAY_NO_RETRY_TTL_S", "300"))
RETRY_POLICIES = {"none", "offline_queue", "at_least_once"}
DEFAULT_RETRY_POLICY = os.getenv("HUB_DEFAULT_RETRY_POLICY", "none").strip().lower()
if DEFAULT_RETRY_POLICY not in RETRY_POLICIES:
    DEFAULT_RETRY_POLICY = "none"
MCP_RELAY_DEFAULT_RETRY_POLICY = os.getenv("MCP_RELAY_DEFAULT_RETRY_POLICY", DEFAULT_RETRY_POLICY).strip().lower()
if MCP_RELAY_DEFAULT_RETRY_POLICY not in RETRY_POLICIES:
    MCP_RELAY_DEFAULT_RETRY_POLICY = DEFAULT_RETRY_POLICY
CHATGPT_RESPONSE_LIMIT = int(os.getenv("HUB_CHATGPT_RESPONSE_LIMIT", "24000"))
SPILL_FIELD_MIN_CHARS = int(os.getenv("HUB_SPILL_FIELD_MIN_CHARS", "4096"))
SPILL_PREVIEW_HEAD_CHARS = int(os.getenv("HUB_SPILL_PREVIEW_HEAD_CHARS", "600"))
SPILL_PREVIEW_TAIL_CHARS = int(os.getenv("HUB_SPILL_PREVIEW_TAIL_CHARS", "160"))
SPILL_HINT_STYLE = os.getenv("HUB_SPILL_HINT_STYLE", "compact").strip().lower()
HEADROOM_SPILL_ENABLED = os.getenv("HUB_HEADROOM_SPILL_ENABLED", "1").strip().lower() not in {"0", "false", "no", "off"}
HEADROOM_SPILL_MAX_CHARS = int(os.getenv("HUB_HEADROOM_SPILL_MAX_CHARS", "200000"))
HEADROOM_SPILL_PREVIEW_CHARS = int(os.getenv("HUB_HEADROOM_SPILL_PREVIEW_CHARS", "1200"))
HEADROOM_SITE_PACKAGES = os.getenv("HUB_HEADROOM_SITE_PACKAGES", "").strip()
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
QUEUE_LONG_POLL_MAX_TIMEOUT = int(os.getenv("QUEUE_LONG_POLL_MAX_TIMEOUT", "55"))
QUEUE_LONG_POLL_SLEEP_S = float(os.getenv("QUEUE_LONG_POLL_SLEEP_S", "0.5"))

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


async def _periodic_dispatch_deferred() -> None:
    while True:
        await asyncio.sleep(HUB_DEFERRED_DISPATCH_INTERVAL_S)
        try:
            await _dispatch_due_deferred_tasks()
        except Exception as e:
            log.warning("deferred: dispatch loop failed: %s", e)


@asynccontextmanager
async def _lifespan(app: FastAPI):
    _load_all_state()
    save_task = asyncio.ensure_future(_periodic_save())
    dispatch_task = asyncio.ensure_future(_periodic_dispatch_deferred())
    _sd_notify("READY=1")
    try:
        yield
    finally:
        for task in (save_task, dispatch_task):
            task.cancel()
            try:
                await task
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
sync_waiters: Dict[str, float] = {}
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


def _reconcile_approved_server_record(name: str, entry: Dict[str, Any]) -> Dict[str, Any]:
    """Keep live/state server identity aligned with approved registry.

    A reinstall can legitimately rotate server_id/public_key/fingerprint. Once that
    new identity is approved, stale hub_servers_state.json must not keep the old
    identity, otherwise signed /queue requests are checked against the old key.
    """
    approved = approved_servers.get(name) or {}
    if not approved:
        return entry
    out = dict(entry)
    for key in ("server_id", "public_key", "fingerprint"):
        if approved.get(key):
            out[key] = approved[key]
    if approved.get("base_url"):
        out["base_url"] = approved["base_url"]
    if approved.get("backend"):
        out["backend"] = approved["backend"]
    return out


def _load_all_state() -> None:
    now = time.time()
    cutoff = now - STATE_TTL_S

    for name, entry in _load_json_dict(HUB_SERVERS_STATE_FILE).items():
        if isinstance(entry, dict) and float(entry.get("time", 0)) >= cutoff:
            servers[name] = _reconcile_approved_server_record(name, entry)
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
            if task.get("status") in {"running", "dispatching"}:
                if _retry_policy_redelivers(task.get("retry_policy") or "at_least_once"):
                    task = {**task, "status": "queued_offline", "orphaned_at": int(now), "next_attempt_at": now, "updated_at": int(now)}
                else:
                    task = {**task, "status": "orphaned", "orphaned_at": int(now), "updated_at": int(now), "error": "hub restarted before result and retry_policy does not allow redelivery"}
            kept[tid] = task
        if kept:
            background_tasks[srv] = kept
    log.info("state: loaded task_servers=%s", len(background_tasks))

    mcp_agent_cutoff = now - max(MCP_RELAY_STALE_RETENTION_S, MCP_RELAY_STALE_TTL_S, DEAD_S)
    for agent_id, entry in _load_json_dict(HUB_MCP_AGENTS_STATE_FILE).items():
        if isinstance(entry, dict) and float(entry.get("last_seen", 0)) >= mcp_agent_cutoff:
            mcp_relay_agents[agent_id] = entry
    log.info("state: loaded mcp_agents=%s", len(mcp_relay_agents))

    for job_id, job in _load_json_dict(HUB_MCP_JOBS_STATE_FILE).items():
        if isinstance(job, dict) and float(job.get("created_at", 0)) >= cutoff:
            if job.get("status") == "running":
                if job.get("kind") == "real_mcp" and _retry_policy_redelivers(job.get("retry_policy") or "at_least_once"):
                    # Hub restart may happen after dispatch but before result. Requeue real MCP jobs
                    # only when the caller explicitly asked for at-least-once redelivery.
                    job = {**job, "status": "queued_offline", "orphaned_at": int(now), "updated_at": int(now)}
                else:
                    job = {**job, "status": "orphaned", "orphaned_at": int(now), "updated_at": int(now)}
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
    now = time.time()
    cutoff = now - STATE_TTL_S

    for name in [n for n, d in servers.items() if float(d.get("time", 0)) < cutoff]:
        servers.pop(name, None)

    for srv in list(background_tasks.keys()):
        for tid, task in list(background_tasks[srv].items()):
            if task.get("status") in QUEUED_TASK_STATUSES and _task_expired(task, now):
                task.update({"status": "expired", "completed_at": int(now), "updated_at": int(now), "error": "deferred task expired"})
            if float(task.get("created_at", 0)) < cutoff:
                background_tasks[srv].pop(tid, None)
        if not background_tasks[srv]:
            background_tasks.pop(srv, None)

    mcp_agent_cutoff = now - max(MCP_RELAY_STALE_RETENTION_S, MCP_RELAY_STALE_TTL_S, DEAD_S)
    for agent_id in [a for a, d in mcp_relay_agents.items() if float(d.get("last_seen", 0)) < mcp_agent_cutoff]:
        mcp_relay_agents.pop(agent_id, None)

    for job_id, job in list(mcp_relay_jobs.items()):
        if isinstance(job, dict):
            expires_at = job.get("expires_at")
            if job.get("status") in MCP_RELAY_QUEUED_STATUSES and expires_at is not None and float(expires_at) <= now:
                job.update({"status": "expired", "completed_at": int(now), "updated_at": int(now), "error": {"message": "MCP relay job expired before delivery"}})
        if float((job or {}).get("created_at", 0)) < cutoff:
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


def _maybe_add_headroom_path() -> None:
    """Make an optional standalone Headroom venv importable by the hub process."""

    candidates = []
    if HEADROOM_SITE_PACKAGES:
        candidates.append(Path(HEADROOM_SITE_PACKAGES))
    candidates.extend(Path("/home/admin/.venvs/headroom/lib").glob("python*/site-packages"))

    for candidate in candidates:
        try:
            if candidate.exists():
                path = str(candidate)
                if path not in sys.path:
                    sys.path.insert(0, path)
                return
        except Exception:
            continue


def _headroom_spill_summary(content: str) -> Optional[Dict[str, Any]]:
    """Return compact Headroom summary for a spilled field, best-effort only."""

    if not HEADROOM_SPILL_ENABLED or not content:
        return None
    try:
        _maybe_add_headroom_path()
        from headroom.compress import compress  # type: ignore

        source = content[:HEADROOM_SPILL_MAX_CHARS]
        result = compress([{"role": "tool", "content": source}], model=os.getenv("HUB_HEADROOM_MODEL", "claude-sonnet-4-5-20250929"))
        compressed = result.messages[0].get("content", source)
        if not isinstance(compressed, str):
            compressed = json.dumps(compressed, ensure_ascii=False, default=str)
        compressed = compressed.strip()
        if not compressed or compressed == source.strip():
            return None
        if len(compressed) > HEADROOM_SPILL_PREVIEW_CHARS:
            compressed = compressed[:HEADROOM_SPILL_PREVIEW_CHARS].rstrip() + "…"
        return _omit_none({
            "summary": compressed,
            "tokens_before": getattr(result, "tokens_before", None),
            "tokens_after": getattr(result, "tokens_after", None),
            "transforms": getattr(result, "transforms_applied", None),
            "truncated_input": len(content) > len(source),
        })
    except Exception as exc:
        log.debug("headroom spill summary failed: %s", exc, exc_info=True)
        return None


def _spill_hint(path: Path) -> str:
    if SPILL_HINT_STYLE in {"none", "off", "0"}:
        return ""
    if SPILL_HINT_STYLE in {"full", "verbose"}:
        return f"Read full output from file_path. Example: sed -n '1,120p' {shlex.quote(str(path))}"
    return "Full output is in file_path."


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
        "preview_head": content[:SPILL_PREVIEW_HEAD_CHARS],
        "preview_tail": content[-SPILL_PREVIEW_TAIL_CHARS:] if len(content) > SPILL_PREVIEW_HEAD_CHARS else "",
    }
    hint = _spill_hint(path)
    if hint:
        stub["hint"] = hint
    headroom = _headroom_spill_summary(content)
    if headroom:
        stub["headroom"] = headroom
    hub_srv = _find_hub_server()
    if hub_srv:
        stub["hub_server"] = _virtual_shell_agent_id(hub_srv)
    return stub


def _spill_json_field(output_id: str, srv: str, cmd: str, field: str, value: Any, returncode: Any = None) -> dict:
    try:
        content = json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True)
    except Exception:
        content = str(value)
    stub = _spill_field(output_id, srv, cmd, f"{field}.json", content, returncode)
    stub["field"] = field
    stub["content_type"] = "application/json"
    return stub


def _spill_large_fields(out: Dict[str, Any], cmd: str) -> Dict[str, Any]:
    raw = json.dumps({"results": out}, ensure_ascii=False, default=str)
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
        changed = False
        for field in ("stdout", "stderr"):
            val = modified.get(field)
            if isinstance(val, str) and len(val) > SPILL_FIELD_MIN_CHARS:
                modified[field] = _spill_field(output_id, srv, cmd, field, val, returncode)
                changed = True
        # Legacy/special commands and task APIs can return large structured JSON
        # without stdout/stderr. Spill those bulky fields too, otherwise the MCP
        # response itself can exceed the ChatGPT tool transport limit.
        for field in ("tasks", "servers", "pending", "response", "result"):
            if field not in modified:
                continue
            val = modified.get(field)
            try:
                val_len = len(json.dumps(val, ensure_ascii=False, default=str))
            except Exception:
                val_len = len(str(val))
            if val_len > SPILL_FIELD_MIN_CHARS:
                if isinstance(val, list):
                    modified[f"{field}_count"] = len(val)
                elif isinstance(val, dict):
                    modified[f"{field}_keys"] = sorted(str(k) for k in val.keys())[:50]
                modified[field] = _spill_json_field(output_id, srv, cmd, field, val, returncode)
                changed = True
        if not changed:
            try:
                res_len = len(json.dumps(modified, ensure_ascii=False, default=str))
            except Exception:
                res_len = len(str(modified))
            if res_len > CHATGPT_RESPONSE_LIMIT:
                result[srv] = {
                    "_spilled": True,
                    "result": _spill_json_field(output_id, srv, cmd, "result", modified, returncode),
                    "summary": {k: v for k, v in modified.items() if k not in {"tasks", "servers", "pending", "response", "result"}},
                }
                continue
        result[srv] = modified
    return result


def _spill_single_result(srv: str, result: Any, cmd: str) -> Any:
    if not isinstance(result, dict):
        return result
    return _spill_large_fields({srv: result}, cmd).get(srv, result)


def _spill_mcp_structured(srv: str, data: Dict[str, Any], cmd: str) -> Dict[str, Any]:
    """Apply the same long-output policy to direct MCP tool structuredContent."""
    if not isinstance(data, dict):
        return data
    return _spill_large_fields({srv: data}, cmd).get(srv, data)


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
        "default_cwd": payload.get("default_cwd"),
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
    default_cwd: Optional[str] = None
    os: str = "linux"
    mode: str = Field("webhook", pattern="^(webhook|polling|long_poll|websocket)$")
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
    outbox_dir: Optional[str] = None
    outbox_files: Optional[int] = None
    outbox_bytes: Optional[int] = None
    outbox_oldest_age_s: Optional[int] = None
    outbox_failed_attempts: Optional[int] = None
    outbox_last_error: Optional[str] = None


class BulkExec(BaseModel):
    servers: List[str]
    cmd: str
    timeout: Optional[int] = None
    cwd: Optional[str] = None
    env: Optional[Dict[str, Any]] = None
    background: bool = False
    not_before: Optional[Any] = None
    expires_at: Optional[Any] = None
    max_attempts: Optional[int] = None
    retry_policy: str = Field(DEFAULT_RETRY_POLICY, pattern="^(none|offline_queue|at_least_once)$")


class ExecReq(BaseModel):
    cmd: str
    env: Optional[dict] = None
    cwd: Optional[str] = None
    timeout: Optional[int] = None


class TaskResult(BaseModel):
    id: str
    result: dict


class TaskEdit(BaseModel):
    not_before: Optional[Any] = None
    expires_at: Optional[Any] = None
    max_attempts: Optional[int] = None
    next_attempt_at: Optional[Any] = None
    action: Optional[str] = Field(default=None, pattern="^(cancel|retry_now|pause)$")
    reason: Optional[str] = None


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
    retry_policy: str = Field(MCP_RELAY_DEFAULT_RETRY_POLICY, pattern="^(none|offline_queue|at_least_once)$")


class McpRelayCallReq(BaseModel):
    target: str
    tool_name: str
    arguments: Dict[str, Any] = Field(default_factory=dict)
    timeout: Optional[int] = Field(default=None, ge=1, le=MCP_RELAY_REQUEST_TIMEOUT_MAX_S)
    background: bool = False
    retry_policy: str = Field(MCP_RELAY_DEFAULT_RETRY_POLICY, pattern="^(none|offline_queue|at_least_once)$")


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


def _parse_time_value(value: Any, default: Optional[float] = None) -> Optional[float]:
    if value is None or value == "":
        return default
    if isinstance(value, (int, float)):
        # Small numbers are treated as relative seconds; large numbers as epoch seconds.
        return time.time() + float(value) if float(value) < 10_000_000 else float(value)
    if isinstance(value, str):
        text = value.strip()
        if not text:
            return default
        try:
            number = float(text)
            return time.time() + number if number < 10_000_000 else number
        except ValueError:
            pass
        try:
            if text.endswith("Z"):
                text = text[:-1] + "+00:00"
            return datetime.datetime.fromisoformat(text).timestamp()
        except Exception:
            return default
    return default


def _server_alive(srv: str, info: Optional[Dict[str, Any]] = None) -> bool:
    info = info or servers.get(srv) or {}
    return bool(info) and time.time() - float(info.get("time", 0)) <= DEAD_S


def _normalize_retry_policy(value: Any, default: Optional[str] = None) -> str:
    policy = str(value or default or DEFAULT_RETRY_POLICY).strip().lower()
    return policy if policy in RETRY_POLICIES else DEFAULT_RETRY_POLICY


def _retry_policy_queues_offline(policy: Any) -> bool:
    return _normalize_retry_policy(policy) in {"offline_queue", "at_least_once"}


def _retry_policy_redelivers(policy: Any) -> bool:
    return _normalize_retry_policy(policy) == "at_least_once"


def _retry_policy_max_attempts(policy: Any, explicit: Optional[int], existing: Any = None) -> int:
    if _retry_policy_redelivers(policy):
        return int(explicit or existing or HUB_DEFERRED_MAX_ATTEMPTS)
    return 1


def _task_due(task: Dict[str, Any], now: Optional[float] = None) -> bool:
    now = now or time.time()
    return float(task.get("not_before") or 0) <= now and float(task.get("next_attempt_at") or 0) <= now


def _task_expired(task: Dict[str, Any], now: Optional[float] = None) -> bool:
    now = now or time.time()
    expires_at = task.get("expires_at")
    return expires_at is not None and float(expires_at) <= now


def _deferred_backoff_s(attempts: int) -> float:
    return min(300.0, 2.0 * (2 ** min(max(attempts - 1, 0), 8)))


def _queue_deferred_task(srv: str, tid: str, payload: Dict[str, Any], *, cmd: str, cwd: Any = None, not_before: Any = None, expires_at: Any = None, max_attempts: Optional[int] = None, reason: str = "queued", retry_policy: Any = None) -> Dict[str, Any]:
    now = time.time()
    policy = _normalize_retry_policy(retry_policy)
    nb = _parse_time_value(not_before, now) or now
    exp = _parse_time_value(expires_at, now + HUB_DEFERRED_DEFAULT_TTL_S) or (now + HUB_DEFERRED_DEFAULT_TTL_S)
    task = _task_slot(srv, tid)
    status = "queued_deferred" if nb > now else "queued_offline"
    task.update({
        "status": status,
        "task_id": tid,
        "server": srv,
        "cmd": cmd,
        "cwd": cwd,
        "payload": payload,
        "not_before": nb,
        "expires_at": exp,
        "attempts": int(task.get("attempts") or 0),
        "max_attempts": _retry_policy_max_attempts(policy, max_attempts, task.get("max_attempts")),
        "retry_policy": policy,
        "next_attempt_at": max(nb, float(task.get("next_attempt_at") or 0)),
        "queued_reason": reason,
        "updated_at": int(now),
    })
    return task


TERMINAL_TASK_STATUSES = {"completed", "failed", "expired", "cancelled", "orphaned"}
QUEUED_TASK_STATUSES = {"queued_offline", "queued_deferred", "queued_ready", "dispatch_failed"}


def _resolve_legacy_task_id(srv: str, task_id: str) -> str:
    if task_id in background_tasks.get(srv, {}):
        return task_id
    job = mcp_relay_jobs.get(task_id)
    if job and job.get("kind") == "virtual_shell_task" and job.get("server") == srv:
        return str(job.get("task_id") or task_id)
    return task_id


def _edit_task(srv: str, task_id: str, edit: Dict[str, Any]) -> Dict[str, Any]:
    tid = _resolve_legacy_task_id(srv, task_id)
    task = background_tasks.get(srv, {}).get(tid)
    if not task:
        raise HTTPException(404, f"task not found: {task_id}")
    now = time.time()
    old = dict(task)
    status = str(task.get("status") or "")
    action = edit.get("action")
    reason = str(edit.get("reason") or action or "task_edit")

    if status in {"completed", "failed", "expired"} and action != "cancel":
        raise HTTPException(409, f"cannot edit terminal task status={status}")

    if "not_before" in edit and edit.get("not_before") is not None:
        task["not_before"] = _parse_time_value(edit.get("not_before"), now) or now
    if "expires_at" in edit and edit.get("expires_at") is not None:
        task["expires_at"] = _parse_time_value(edit.get("expires_at"), None)
    if "next_attempt_at" in edit and edit.get("next_attempt_at") is not None:
        task["next_attempt_at"] = _parse_time_value(edit.get("next_attempt_at"), now) or now
    if "max_attempts" in edit and edit.get("max_attempts") is not None:
        task["max_attempts"] = int(edit.get("max_attempts"))

    if action == "cancel":
        if status == "completed":
            raise HTTPException(409, "cannot cancel completed task")
        task.update({"status": "cancelled", "completed_at": int(now), "cancelled_at": int(now), "cancel_reason": reason})
    elif action == "retry_now":
        if status in {"running", "dispatching"}:
            raise HTTPException(409, f"cannot retry_now while task is {status}")
        task.update({"status": "queued_ready", "not_before": now, "next_attempt_at": now, "retry_reason": reason})
    elif action == "pause":
        if status in {"running", "dispatching"}:
            raise HTTPException(409, f"cannot pause while task is {status}")
        task.update({"status": "queued_deferred", "pause_reason": reason})

    if task.get("status") in QUEUED_TASK_STATUSES:
        nb = float(task.get("not_before") or 0)
        na = float(task.get("next_attempt_at") or 0)
        if nb > now or na > now:
            task["status"] = "queued_deferred"
        elif task.get("status") not in {"cancelled", "expired"}:
            task["status"] = "queued_ready"
    task["updated_at"] = int(now)
    task.setdefault("edit_history", []).append({
        "at": int(now),
        "action": action,
        "reason": reason,
        "fields": {k: edit.get(k) for k in ("not_before", "expires_at", "max_attempts", "next_attempt_at") if k in edit},
        "old_status": old.get("status"),
        "new_status": task.get("status"),
    })
    return {"ok": True, "server": srv, "task_id": tid, "requested_task_id": task_id, "task": {"server": srv, "task_id": tid, **task}}


def _polling_due_task(srv: str) -> Optional[Dict[str, Any]]:
    now = time.time()
    for tid, task in list((background_tasks.get(srv) or {}).items()):
        if task.get("status") not in {"queued_offline", "queued_deferred", "queued_ready", "dispatch_failed"}:
            continue
        if _task_expired(task, now):
            task.update({"status": "expired", "completed_at": int(now), "updated_at": int(now), "error": "deferred task expired"})
            continue
        if not _task_due(task, now):
            continue
        attempts = int(task.get("attempts") or 0) + 1
        if attempts > int(task.get("max_attempts") or HUB_DEFERRED_MAX_ATTEMPTS):
            task.update({"status": "failed", "completed_at": int(now), "updated_at": int(now), "error": "deferred task max attempts exceeded"})
            continue
        task.update({"status": "running", "attempts": attempts, "started_at": int(now), "updated_at": int(now)})
        payload = dict(task.get("payload") or {})
        return {"id": tid, **payload}
    return None


async def _dispatch_due_deferred_tasks() -> None:
    now = time.time()
    for srv, tasks in list(background_tasks.items()):
        info = servers.get(srv)
        if not info or info.get("mode") in {"polling", "long_poll"}:
            continue
        if not _server_alive(srv, info):
            continue
        for tid, task in list(tasks.items()):
            if task.get("status") not in {"queued_offline", "queued_deferred", "queued_ready", "dispatch_failed"}:
                continue
            if _task_expired(task, now):
                task.update({"status": "expired", "completed_at": int(now), "updated_at": int(now), "error": "deferred task expired"})
                continue
            if not _task_due(task, now):
                continue
            payload = task.get("payload") or {}
            if not payload:
                task.update({"status": "failed", "completed_at": int(now), "updated_at": int(now), "error": "deferred task missing payload"})
                continue
            task.update({"status": "dispatching", "updated_at": int(now)})
            await _queue_or_fire_background(srv, info, dict(payload), tid, retry_policy=task.get("retry_policy"), from_deferred=True)


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

    approved = approved_servers.get(b.name) or {}
    candidate_keys: List[str] = []
    if approved.get("public_key"):
        candidate_keys.append(str(approved["public_key"]))
    if b.public_key and b.public_key not in candidate_keys:
        candidate_keys.append(b.public_key)
    if not candidate_keys:
        raise HTTPException(401, "missing heartbeat public key")

    last_error: Optional[Exception] = None
    for pub in candidate_keys:
        try:
            verify_signature(pub, request.method, request.url.path, ts, nonce, body, sig)
            SIGNATURE_NONCES.check_and_store(f"rootd:{b.name}:{b.server_id}", nonce)
            return
        except Exception as e:
            last_error = e
    raise HTTPException(401, f"invalid signed heartbeat: {last_error}")


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
      description: SECOND STEP. List tools for an explicit agent_id returned by listMcpAgents; no default target.
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/ListMcpToolsRequest"
      responses:
        "200":
          description: Tool list or job
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
          description: Tool response or job
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
          description: Clear terminal job after reading.
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
              status: { type: string, enum: [online, offline, stale, pending] }
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
          description: Agent id from listMcpAgents; required; never use default.
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
          description: Agent id from listMcpAgents; required; never use default.
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

    servers[b.name] = _reconcile_approved_server_record(b.name, b.dict())
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
    # This legacy shim only owns two synthetic commands. Do not run shlex over
    # arbitrary shell scripts: large heredocs (JS/HTML/etc.) may legally contain
    # unmatched quotes inside the heredoc body, and shlex would reject them before
    # the command ever reaches bash.
    stripped = (cmd or "").strip()
    if not stripped:
        return None
    head = stripped.split(None, 1)[0]
    if head not in {"gptadmin_tasks", "gptadmin_pending"}:
        return None
    try:
        parts = shlex.split(stripped)
    except ValueError as e:
        return {"error": f"bad command syntax: {e}"}

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
    servers[name] = _reconcile_approved_server_record(name, payload)
    pending_servers.pop(name, None)
    _save_json_dict(PENDING_SERVERS_FILE, pending_servers)
    _save_json_dict(HUB_SERVERS_STATE_FILE, servers)
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


async def _queue_or_fire_background(srv: str, info: Dict[str, Any], payload: dict, tid: str, *, not_before: Any = None, expires_at: Any = None, max_attempts: Optional[int] = None, retry_policy: Any = None, from_deferred: bool = False) -> None:
    mode = info.get("mode", "webhook")
    policy = _normalize_retry_policy(retry_policy)
    task = _task_slot(srv, tid)
    if not from_deferred:
        _queue_deferred_task(srv, tid, payload, cmd=str(payload.get("cmd") or ""), cwd=payload.get("cwd"), not_before=not_before, expires_at=expires_at, max_attempts=max_attempts, reason="background", retry_policy=policy)
        task = _task_slot(srv, tid)
    else:
        policy = _normalize_retry_policy(task.get("retry_policy"), policy)
    if not _task_due(task) or _task_expired(task) or not _server_alive(srv, info):
        if _task_expired(task):
            task.update({"status": "expired", "completed_at": int(time.time()), "updated_at": int(time.time()), "error": "deferred task expired"})
        elif not _server_alive(srv, info) and not _retry_policy_queues_offline(policy):
            task.update({"status": "failed", "completed_at": int(time.time()), "updated_at": int(time.time()), "error": "server offline and retry_policy=none"})
        else:
            task.update({"status": "queued_deferred" if float(task.get("not_before") or 0) > time.time() else "queued_offline", "updated_at": int(time.time())})
        return

    if mode in {"polling", "long_poll"}:
        task.update({"status": "queued_ready", "updated_at": int(time.time())})
        return

    if mode == "websocket":
        ws = ws_sessions.get(srv)
        if not ws:
            attempts = int(task.get("attempts") or 0) + 1
            if _retry_policy_redelivers(policy):
                task.update({"status": "queued_offline", "attempts": attempts, "next_attempt_at": time.time() + _deferred_backoff_s(attempts), "last_error": "websocket not connected", "updated_at": int(time.time())})
            else:
                task.update({"status": "failed", "attempts": attempts, "completed_at": int(time.time()), "last_error": "websocket not connected", "error": "websocket not connected and retry_policy does not allow retry", "updated_at": int(time.time())})
            return
        attempts = int(task.get("attempts") or 0) + 1
        task.update({"status": "running", "attempts": attempts, "started_at": int(time.time()), "updated_at": int(time.time())})
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
                task = _task_slot(srv, tid)
                attempts = int(task.get("attempts") or 0) + 1
                if _retry_policy_redelivers(policy):
                    task.update({"status": "queued_offline", "attempts": attempts, "next_attempt_at": time.time() + _deferred_backoff_s(attempts), "last_error": started, "updated_at": int(time.time())})
                else:
                    task.update({"status": "failed", "attempts": attempts, "completed_at": int(time.time()), "last_error": started, "error": "callback start failed and retry_policy does not allow retry", "updated_at": int(time.time())})
                return
            task = _task_slot(srv, tid)
            attempts = int(task.get("attempts") or 0) + 1
            task.update({"status": "running", "attempts": attempts, "delivery": "callback_outbox", "rootd_start": started, "started_at": int(time.time()), "updated_at": int(time.time())})
        except Exception as e:
            task = _task_slot(srv, tid)
            attempts = int(task.get("attempts") or 0) + 1
            if _retry_policy_redelivers(policy):
                task.update({"status": "queued_offline", "attempts": attempts, "next_attempt_at": time.time() + _deferred_backoff_s(attempts), "last_error": str(e), "updated_at": int(time.time())})
            else:
                task.update({"status": "failed", "attempts": attempts, "completed_at": int(time.time()), "last_error": str(e), "error": "dispatch exception and retry_policy does not allow retry", "updated_at": int(time.time())})

    asyncio.create_task(runner())


async def _exec_single_server(srv: str, req: BulkExec) -> Dict[str, Any]:
    info = servers.get(srv)
    if not info:
        return {"error": "unknown server"}
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
    retry_policy = _normalize_retry_policy(req.retry_policy)
    now = time.time()
    not_before_ts = _parse_time_value(req.not_before, now) if req.not_before is not None else None
    expires_at_ts = _parse_time_value(req.expires_at, None) if req.expires_at is not None else None
    should_delay = not_before_ts is not None and not_before_ts > now
    is_offline = not _server_alive(srv, info)
    should_defer = should_delay or is_offline

    if is_offline and not should_delay and not _retry_policy_queues_offline(retry_policy):
        return {"error": "server offline", "status": "offline", "retry_policy": retry_policy, "message": "Pass retry_policy=offline_queue or at_least_once to queue for reconnect."}

    if req.background or should_defer:
        await _queue_or_fire_background(srv, info, payload, tid, not_before=not_before_ts, expires_at=expires_at_ts, max_attempts=req.max_attempts, retry_policy=retry_policy)
        task = _task_slot(srv, tid)
        return {"background": True, "task_id": tid, "status": task.get("status", "running"), "retry_policy": task.get("retry_policy", retry_policy), "not_before": task.get("not_before"), "expires_at": task.get("expires_at"), "message": "Command queued for deferred/background execution."}

    if mode in {"polling", "long_poll"}:
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
        sync_waiters[tid]=time.time()
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
    finally:
        sync_waiters.pop(tid, None)


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
        sync_waiters[tid]=time.time()
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
        sync_waiters.pop(tid, None)
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
async def queue_poll(request: Request, srv: str, timeout: int = Query(0, ge=0, le=QUEUE_LONG_POLL_MAX_TIMEOUT)):
    _verify_queue_signature(request, srv, b"")
    deadline = time.time() + min(max(int(timeout or 0), 0), QUEUE_LONG_POLL_MAX_TIMEOUT)
    while True:
        q = queues.get(srv)
        if q:
            return q.pop(0)
        job = _polling_due_task(srv)
        if job:
            return job
        if time.time() >= deadline:
            return {}
        await asyncio.sleep(QUEUE_LONG_POLL_SLEEP_S)


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
        stream_state = task.setdefault("progress_state", {}).setdefault(event_type, {"last_seq": 0, "bytes": 0, "duplicates": 0, "gaps": 0, "offset_mismatches": 0})
        data = str(progress.get("data") or "")
        seq = progress.get("seq")
        offset = progress.get("offset")
        if seq is not None:
            try:
                seq_i = int(seq)
            except Exception:
                seq_i = None
            if seq_i is not None:
                last_seq = int(stream_state.get("last_seq") or 0)
                if seq_i <= last_seq:
                    stream_state["duplicates"] = int(stream_state.get("duplicates") or 0) + 1
                    task.update({"updated_at": int(time.time())})
                    return {"ok": True, "duplicate": True, "last_seq": last_seq}
                if seq_i > last_seq + 1:
                    stream_state["gaps"] = int(stream_state.get("gaps") or 0) + (seq_i - last_seq - 1)
                stream_state["last_seq"] = seq_i
        current_len = len(str(result.get(event_type) or ""))
        if offset is not None:
            try:
                offset_i = int(offset)
                if offset_i != current_len:
                    stream_state["offset_mismatches"] = int(stream_state.get("offset_mismatches") or 0) + 1
                    stream_state["last_offset_mismatch"] = {"expected": current_len, "got": offset_i, "at": int(time.time())}
            except Exception:
                pass
        result[event_type] = str(result.get(event_type) or "") + data
        stream_state["bytes"] = len(str(result.get(event_type) or ""))
        stream_state["updated_at"] = int(time.time())
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
        sync_waiters.pop(res.id, None)
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
    if info.get("mode") in {"polling", "long_poll", "websocket"}:
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


def _mcp_relay_agent_age_s(info: Dict[str, Any], now: Optional[float] = None) -> float:
    try:
        return max(0.0, float(now if now is not None else time.time()) - float(info.get("last_seen", 0)))
    except Exception:
        return float("inf")


def _mcp_relay_agent_status(info: Dict[str, Any], now: Optional[float] = None) -> str:
    age = _mcp_relay_agent_age_s(info, now)
    if age <= DEAD_S:
        return "online"
    if age <= MCP_RELAY_STALE_TTL_S:
        return "offline"
    return "stale"


def _mcp_relay_job_counts(agent_id: str) -> Dict[str, int]:
    counts = {"queued_jobs": 0, "running_jobs": 0, "completed_jobs": 0, "failed_jobs": 0}
    for job in mcp_relay_jobs.values():
        if not isinstance(job, dict) or job.get("kind") != "real_mcp" or job.get("agent_id") != agent_id:
            continue
        status = str(job.get("status") or "")
        if status in MCP_RELAY_QUEUED_STATUSES:
            counts["queued_jobs"] += 1
        elif status == "running":
            counts["running_jobs"] += 1
        elif status == "completed":
            counts["completed_jobs"] += 1
        elif status in {"failed", "expired", "orphaned"}:
            counts["failed_jobs"] += 1
    return counts


_SECRET_KEY_RE = re.compile(r"(token|secret|password|passwd|api[_-]?key|authorization|bearer|x-api-key)", re.I)
_SECRET_VALUE_RE = re.compile(
    r"(?i)(Authorization\s*:\s*(?:Bearer|ApiKey|Basic)\s+)[^\s,'\"\]]+|"
    r"((?:Bearer|ApiKey|Basic)\s+)[^\s,'\"\]]+|"
    r"((?:token|secret|password|passwd|api[_-]?key)\s*[=:]\s*)[^\s,'\"\]]+"
)


def _redact_secret_value(value: Any) -> Any:
    """Return a public-safe copy with obvious secrets redacted.

    Hub registry/meta is user-visible via listMcpAgents. Never expose bearer
    headers, API keys, passwords or token-like values there; rootd/relay may
    still keep the real values in local config files with filesystem ACLs.
    """
    if isinstance(value, dict):
        out: Dict[str, Any] = {}
        for k, v in value.items():
            key = str(k)
            out[key] = "***MASKED***" if _SECRET_KEY_RE.search(key) else _redact_secret_value(v)
        return out
    if isinstance(value, list):
        redacted = []
        skip_next = False
        for item in value:
            if skip_next:
                redacted.append("***MASKED***")
                skip_next = False
                continue
            if isinstance(item, str) and item.lower() in {"--header", "--token", "--api-key", "--password", "--secret"}:
                redacted.append(item)
                skip_next = True
                continue
            redacted.append(_redact_secret_value(item))
        return redacted
    if isinstance(value, str):
        return _SECRET_VALUE_RE.sub(lambda m: (m.group(1) or m.group(2) or m.group(3) or "") + "***MASKED***", value)
    return value


def _mcp_relay_public_agent(agent_id: str, info: Dict[str, Any], now: Optional[float] = None) -> Dict[str, Any]:
    status = _mcp_relay_agent_status(info, now)
    meta = _redact_secret_value(dict(info.get("meta") or {}))
    meta.setdefault("age_s", int(_mcp_relay_agent_age_s(info, now)))
    meta.setdefault("transport_role", "capability_executor")
    meta.setdefault("rootd_transport_ready", True)
    for k, v in _mcp_relay_job_counts(agent_id).items():
        if v:
            meta[k] = v
    return {
        "agent_id": agent_id,
        "name": info.get("name") or agent_id,
        "kind": "real_mcp",
        "transport": info.get("transport", "stdio"),
        "status": status,
        "last_seen": info.get("last_seen"),
        "capabilities": info.get("capabilities") or [],
        "meta": meta,
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


def _normalize_agent_statuses(statuses: Optional[Any]) -> set[str]:
    if statuses is None:
        return {"online", "offline"}
    if isinstance(statuses, str):
        raw = [x.strip() for x in statuses.split(",")]
    elif isinstance(statuses, list):
        raw = [str(x).strip() for x in statuses]
    else:
        raw = []
    aliases = {"active": "online", "live": "online", "dead": "offline"}
    out = {aliases.get(x.lower(), x.lower()) for x in raw if x}
    if "all" in out:
        return {"online", "offline", "stale"}
    return out or {"online", "offline"}


def _all_public_agents(*, statuses: Optional[Any] = None) -> List[Dict[str, Any]]:
    wanted = _normalize_agent_statuses(statuses)
    now = time.time()
    agents = []
    for item in [_virtual_hub_agent(), *_virtual_shell_agents()]:
        if str(item.get("status")) in wanted:
            agents.append(item)
    for agent_id, info in mcp_relay_agents.items():
        item = _mcp_relay_public_agent(agent_id, info, now)
        if str(item.get("status")) in wanted:
            agents.append(item)
    rank = {"online": 0, "offline": 1, "stale": 2}
    agents.sort(key=lambda x: (rank.get(str(x.get("status")), 9), str(x.get("agent_id"))))
    return agents


def _stale_mcp_relay_agents() -> List[Dict[str, Any]]:
    now = time.time()
    out = []
    for agent_id, info in mcp_relay_agents.items():
        item = _mcp_relay_public_agent(agent_id, info, now)
        if item.get("status") == "stale":
            out.append(item)
    return out


def _purge_stale_mcp_relay_agents() -> Dict[str, Any]:
    stale = _stale_mcp_relay_agents()
    removed = []
    for item in stale:
        agent_id = str(item.get("agent_id") or "")
        if agent_id:
            mcp_relay_agents.pop(agent_id, None)
            mcp_relay_queues.pop(agent_id, None)
            removed.append(agent_id)
    _save_json_dict(HUB_MCP_AGENTS_STATE_FILE, mcp_relay_agents)
    return {"removed": removed, "count": len(removed), "stale": stale}


def _forget_mcp_relay_agent(agent_id: Optional[str]) -> bool:
    if not agent_id:
        return False
    removed = False
    if agent_id in mcp_relay_agents:
        mcp_relay_agents.pop(agent_id, None)
        removed = True
    if agent_id in mcp_relay_queues:
        mcp_relay_queues.pop(agent_id, None)
        removed = True
    if removed:
        _save_json_dict(HUB_MCP_AGENTS_STATE_FILE, mcp_relay_agents)
    return removed


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


def _run_gptadmin_mcp(argv: List[str], timeout: int = 60, check: bool = True) -> Dict[str, Any]:
    cmd = ["sudo", "-n", str(GPTADMIN_PYTHON), str(GPTADMIN_CLI_PATH), "mcp", *[str(x) for x in argv]]
    try:
        cp = subprocess.run(cmd, cwd=str(GPTADMIN_REPO_ROOT), text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    except subprocess.TimeoutExpired as e:
        raise HTTPException(504, f"gptadmin mcp timed out: {' '.join(shlex.quote(x) for x in cmd)}") from e
    data: Any = None
    if cp.stdout.strip().startswith("{") or cp.stdout.strip().startswith("["):
        try:
            data = json.loads(cp.stdout)
        except Exception:
            data = None
    result = {"ok": cp.returncode == 0, "returncode": cp.returncode, "stdout": cp.stdout, "stderr": cp.stderr, "json": data, "argv": ["gptadmin", "mcp", *argv]}
    if check and cp.returncode != 0:
        raise HTTPException(500, detail=result)
    return result


def _server_os_text(srv: str) -> str:
    return str((servers.get(srv) or {}).get("os") or "")


def _server_is_windows(srv: str) -> bool:
    os_text = _server_os_text(srv).lower()
    return ("win" in os_text or "mingw" in os_text or "msys" in os_text) and "darwin" not in os_text


def _ps_quote(value: Any) -> str:
    return "'" + str(value).replace("'", "''") + "'"


_MCP_RUNTIME_BOOTSTRAP_PY = "import hashlib\nimport json\nimport os\nimport pathlib\nimport stat\nimport sys\nimport tarfile\nimport tempfile\nimport urllib.parse\nimport urllib.request\n\n\ndef log(message):\n    print('gptadmin mcp bootstrap: ' + str(message), file=sys.stderr)\n\n\ndef hub_base():\n    raw = (os.environ.get('HUB_URL') or os.environ.get('GPTADMIN_MCP_RELAY_HUB') or 'https://gptadmin.bezrabotnyi.com').strip()\n    for suffix in ('/heartbeat', '/queue'):\n        if raw.endswith(suffix):\n            raw = raw[:-len(suffix)]\n    return raw.rstrip('/')\n\n\ndef headers():\n    token = (os.environ.get('ROOTD_UPDATE_TOKEN') or os.environ.get('SHELL_UPDATE_TOKEN') or os.environ.get('GPTADMIN_UPDATE_TOKEN') or os.environ.get('CTL_TOKEN') or '').strip()\n    return {'Authorization': 'Bearer ' + token} if token else {}\n\n\ndef artifact_url(base, value):\n    value = (value or '/artifacts/rootd.tar.gz').strip()\n    if value.startswith('/'):\n        return base + value\n    parsed = urllib.parse.urlparse(value)\n    b = urllib.parse.urlparse(base)\n    if parsed.scheme == 'http' and b.scheme == 'https' and parsed.netloc == b.netloc:\n        return urllib.parse.urlunparse(('https', parsed.netloc, parsed.path, parsed.params, parsed.query, parsed.fragment))\n    return value\n\n\ndef read_json(url, hdrs):\n    req = urllib.request.Request(url, headers=hdrs)\n    with urllib.request.urlopen(req, timeout=30) as r:\n        return json.loads(r.read().decode('utf-8'))\n\n\ndef download(url, hdrs, path):\n    req = urllib.request.Request(url, headers=hdrs)\n    h = hashlib.sha256(); total = 0\n    with urllib.request.urlopen(req, timeout=120) as r, open(path, 'wb') as f:\n        while True:\n            chunk = r.read(1024 * 1024)\n            if not chunk:\n                break\n            h.update(chunk); f.write(chunk); total += len(chunk)\n    return h.hexdigest(), total\n\n\ndef install_dir():\n    candidates = []\n    for key in ('GPTADMIN_HOME', 'ROOTD_HOME'):\n        val = os.environ.get(key)\n        if val:\n            candidates.append(pathlib.Path(val))\n    candidates += [pathlib.Path.cwd(), pathlib.Path.home() / 'gptadmin', pathlib.Path('/opt/gptadmin')]\n    for c in candidates:\n        try:\n            if c.exists() and ((c / 'rootd.py').exists() or (c / 'requirements.txt').exists() or c == pathlib.Path.cwd()):\n                return c.resolve()\n        except Exception:\n            pass\n    return pathlib.Path.cwd().resolve()\n\n\ndef safe_members(tf, dst):\n    root = dst.resolve()\n    for member in tf.getmembers():\n        name = member.name.replace('\\\\', '/')\n        if name.startswith('/') or '..' in pathlib.PurePosixPath(name).parts:\n            continue\n        wanted = name == 'cli' or name.startswith('cli/') or name == 'agents' or name.startswith('agents/generic_stdio_mcp_relay/')\n        if not wanted:\n            continue\n        target = (dst / name).resolve()\n        if target != root and not str(target).startswith(str(root) + os.sep):\n            continue\n        member.name = name\n        yield member\n\n\ndef chmod_runtime(dst):\n    for rel in ('cli/gptadmin.py', 'agents/generic_stdio_mcp_relay/mcp_agent_manager.py', 'agents/generic_stdio_mcp_relay/generic_stdio_mcp_relay.py'):\n        path = dst / rel\n        if path.exists():\n            try:\n                path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)\n            except Exception:\n                pass\n\n\ndef main():\n    dst = install_dir(); base = hub_base(); hdrs = headers()\n    manifest = read_json(base + '/artifacts/rootd.json', hdrs)\n    url = artifact_url(base, manifest.get('url'))\n    expected = (manifest.get('sha256') or '').lower().strip()\n    with tempfile.TemporaryDirectory(prefix='gptadmin-mcp-runtime-') as td:\n        archive = pathlib.Path(td) / 'rootd.tar.gz'\n        actual, total = download(url, hdrs, archive)\n        if expected and actual.lower() != expected:\n            raise SystemExit('sha256 mismatch for rootd artifact: got %s expected %s' % (actual, expected))\n        with tarfile.open(archive, 'r:gz') as tf:\n            members = list(safe_members(tf, dst))\n            names = {m.name for m in members}\n            if 'cli/gptadmin.py' not in names or 'agents/generic_stdio_mcp_relay/mcp_agent_manager.py' not in names:\n                raise SystemExit('rootd artifact does not contain cli/gptadmin.py and generic_stdio_mcp_relay runtime')\n            tf.extractall(dst, members)\n    chmod_runtime(dst)\n    if not (dst / 'cli' / 'gptadmin.py').exists():\n        raise SystemExit('bootstrap finished but cli/gptadmin.py is still missing')\n    log(json.dumps({'ok': True, 'install_dir': str(dst), 'artifact_build_version': manifest.get('build_version'), 'artifact_size': manifest.get('size') or total, 'runtime_payload': manifest.get('runtime_payload')}, ensure_ascii=False, sort_keys=True))\n\n\nif __name__ == '__main__':\n    main()\n"


def _mcp_runtime_bootstrap_b64() -> str:
    return base64.b64encode(_MCP_RUNTIME_BOOTSTRAP_PY.encode("utf-8")).decode("ascii")


def _target_gptadmin_mcp_command(srv: str, argv: List[str]) -> str:
    """Build a shell command that runs gptadmin mcp locally on the selected shell host.

    The hub should not guess how to install a service for another OS. It sends a
    normal shell job to that host, then the local gptadmin CLI/mcp_agent_manager
    chooses systemd, launchd, or Windows Scheduled Task. If the local MCP runtime
    is missing, the same command bootstraps cli/ + agents/ from the rootd update
    artifact first, using ROOTD_UPDATE_TOKEN like rootd auto-update.
    """
    mcp_args = ["mcp", *[str(x) for x in argv]]
    bootstrap_b64 = _mcp_runtime_bootstrap_b64()
    if _server_is_windows(srv):
        ps_args = "@(" + ", ".join(_ps_quote(x) for x in mcp_args) + ")"
        bootstrap_ps = _ps_quote(bootstrap_b64)
        candidates = "@($env:ProgramFiles + '\\GPTAdmin\\gptadmin.py', $env:USERPROFILE + '\\gptadmin\\cli\\gptadmin.py', (Join-Path (Get-Location) 'cli\\gptadmin.py'))"
        return "\n".join([
            "$ErrorActionPreference = 'Stop'",
            f"$mcpArgs = {ps_args}",
            f"$bootstrapB64 = {bootstrap_ps}",
            "function Invoke-GptAdminMcp {",
            "  $cmd = Get-Command gptadmin -ErrorAction SilentlyContinue",
            "  if ($cmd) { & $cmd.Source @mcpArgs; exit $LASTEXITCODE }",
            f"  foreach ($p in {candidates}) {{ if ($p -and (Test-Path $p)) {{ & python $p @mcpArgs; exit $LASTEXITCODE }} }}",
            "  return $false",
            "}",
            "Invoke-GptAdminMcp | Out-Null",
            "$py = Get-Command python -ErrorAction SilentlyContinue",
            "if (-not $py) { $py = Get-Command python3 -ErrorAction SilentlyContinue }",
            "if (-not $py) { Write-Error 'python/python3 required to bootstrap GPTAdmin MCP runtime'; exit 127 }",
            "$code = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($bootstrapB64))",
            "$tmp = Join-Path $env:TEMP ('gptadmin-mcp-bootstrap-' + [Guid]::NewGuid().ToString() + '.py')",
            "Set-Content -Path $tmp -Value $code -Encoding UTF8",
            "& $py.Source $tmp",
            "if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }",
            "Invoke-GptAdminMcp | Out-Null",
            "Write-Error 'gptadmin CLI not found after bootstrap'",
            "exit 127",
        ])
    tail = shlex.join(mcp_args)
    bootstrap_q = shlex.quote(bootstrap_b64)
    return "\n".join([
        "set -e",
        "PYBIN=$(command -v python3 || command -v python || true)",
        "if [ -z \"$PYBIN\" ]; then echo 'python3/python required to bootstrap GPTAdmin MCP runtime' >&2; exit 127; fi",
        "if [ \"$(id -u)\" -eq 0 ]; then ADMIN=\"\"; elif command -v sudo >/dev/null 2>&1; then ADMIN=\"sudo -n\"; else ADMIN=\"\"; fi",
        "run_gptadmin_mcp() {",
        f"  if command -v gptadmin >/dev/null 2>&1; then exec $ADMIN \"$(command -v gptadmin)\" {tail}; fi",
        f"  if [ -x /usr/local/bin/gptadmin ]; then exec $ADMIN /usr/local/bin/gptadmin {tail}; fi",
        f"  if [ -f ./cli/gptadmin.py ]; then exec $ADMIN \"$PYBIN\" ./cli/gptadmin.py {tail}; fi",
        f"  if [ -f \"$HOME/gptadmin/cli/gptadmin.py\" ]; then exec $ADMIN \"$PYBIN\" \"$HOME/gptadmin/cli/gptadmin.py\" {tail}; fi",
        "  return 1",
        "}",
        "if run_gptadmin_mcp; then exit $?; fi",
        f"printf %s {bootstrap_q} | $ADMIN \"$PYBIN\" -c 'import base64,sys; exec(base64.b64decode(sys.stdin.read()).decode(\"utf-8\"))'",
        "if run_gptadmin_mcp; then exit $?; fi",
        "echo 'gptadmin CLI not found after bootstrap' >&2",
        "exit 127",
    ])


def _mcp_json_from_stdout(stdout: str) -> Any:
    text = (stdout or "").strip()
    if text.startswith("{") or text.startswith("["):
        try:
            return json.loads(text)
        except Exception:
            return None
    return None


async def _rootd_get_json(srv: str, path: str, params: Optional[Dict[str, Any]] = None, timeout: int = 15) -> Dict[str, Any]:
    """Call a rootd read endpoint directly with the same signed transport as /exec."""
    info = servers.get(srv)
    if not info:
        return {"ok": False, "error": "unknown server", "server": srv}
    base_url = str(info.get("base_url") or "").rstrip("/")
    if not base_url:
        return {"ok": False, "error": "server has no base_url", "server": srv}
    if not path.startswith("/"):
        path = "/" + path
    url = f"{base_url}{path}"
    headers = _signed_rootd_headers("GET", path, b"")
    try:
        async with httpx.AsyncClient(follow_redirects=True, timeout=timeout) as client:
            r = await client.get(url, params=params or {}, headers=headers)
    except httpx.RequestError as e:
        return {"ok": False, "error": str(e), "server": srv, "path": path, "transport": "direct_signed_rootd"}
    if r.status_code == 404:
        return {"ok": False, "error": "rootd endpoint not found", "status_code": 404, "server": srv, "path": path, "transport": "direct_signed_rootd"}
    if r.status_code >= 400:
        return {"ok": False, "error": f"HTTP {r.status_code}", "status_code": r.status_code, "body": r.text[:500], "server": srv, "path": path, "transport": "direct_signed_rootd"}
    try:
        data = r.json()
    except Exception as e:
        return {"ok": False, "error": f"invalid JSON: {e}", "body": r.text[:500], "server": srv, "path": path, "transport": "direct_signed_rootd"}
    if isinstance(data, dict):
        data.setdefault("ok", True)
        data.setdefault("transport", "direct_signed_rootd")
        data.setdefault("server", srv)
        return data
    return {"ok": True, "data": data, "transport": "direct_signed_rootd", "server": srv}


async def _capability_registry_via_rootd(srv: str, *, include_status: bool = True) -> Dict[str, Any]:
    return await _rootd_get_json(srv, "/capabilities", {"include_status": str(bool(include_status)).lower()}, timeout=15)


async def _rootd_post_json(srv: str, path: str, payload: Optional[Dict[str, Any]] = None, timeout: int = 30) -> Dict[str, Any]:
    """POST JSON to rootd directly with the same signed transport as /exec."""
    info = servers.get(srv)
    if not info:
        return {"ok": False, "error": "unknown server", "server": srv}
    base_url = str(info.get("base_url") or "").rstrip("/")
    if not base_url:
        return {"ok": False, "error": "server has no base_url", "server": srv}
    if not path.startswith("/"):
        path = "/" + path
    body = json.dumps(payload or {}, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    headers = _signed_rootd_headers("POST", path, body, {"Content-Type": "application/json"})
    try:
        async with httpx.AsyncClient(follow_redirects=True, timeout=timeout) as client:
            r = await client.post(f"{base_url}{path}", content=body, headers=headers)
    except httpx.RequestError as e:
        return {"ok": False, "error": str(e), "server": srv, "path": path, "transport": "direct_signed_rootd"}
    if r.status_code >= 400:
        return {"ok": False, "error": f"HTTP {r.status_code}", "status_code": r.status_code, "body": r.text[:800], "server": srv, "path": path, "transport": "direct_signed_rootd"}
    try:
        data = r.json()
    except Exception as e:
        return {"ok": False, "error": f"invalid JSON: {e}", "body": r.text[:800], "server": srv, "path": path, "transport": "direct_signed_rootd"}
    if isinstance(data, dict):
        data.setdefault("ok", True)
        data.setdefault("transport", "direct_signed_rootd")
        data.setdefault("server", srv)
        return data
    return {"ok": True, "data": data, "transport": "direct_signed_rootd", "server": srv}


async def _mcp_lifecycle_via_rootd(srv: str, mcp_ref: str, action: str, backend: Optional[str] = None) -> Dict[str, Any]:
    safe_ref = quote(str(mcp_ref or ""), safe="")
    payload = {"action": action}
    if backend:
        payload["backend"] = backend
    return await _rootd_post_json(srv, f"/capabilities/mcp/{safe_ref}/lifecycle", payload, timeout=45)


async def _run_target_gptadmin_mcp(srv: str, argv: List[str], timeout: int = 60, check: bool = True) -> Dict[str, Any]:
    cmd = _target_gptadmin_mcp_command(srv, argv)
    req = BulkExec(servers=[srv], cmd=cmd, timeout=timeout, background=False)
    res = await _exec_single_server(srv, req)
    if not isinstance(res, dict):
        result = {"ok": False, "returncode": None, "stdout": "", "stderr": "", "json": None, "argv": ["gptadmin", "mcp", *argv], "target_server": srv, "response": res}
    else:
        result = {
            "ok": res.get("returncode") == 0 and not res.get("error"),
            "returncode": res.get("returncode"),
            "stdout": res.get("stdout") or "",
            "stderr": res.get("stderr") or "",
            "json": _mcp_json_from_stdout(str(res.get("stdout") or "")),
            "argv": ["gptadmin", "mcp", *argv],
            "target_server": srv,
            "run_as_user": res.get("run_as_user"),
            "cwd_effective": res.get("cwd_effective"),
        }
        if res.get("background"):
            result.update({"background": True, "task_id": res.get("task_id"), "status": res.get("status")})
        if res.get("error"):
            result["error"] = res.get("error")
    if check and not result.get("ok"):
        raise HTTPException(500, detail=result)
    return result


def _mcp_tools_add_argv(args: Dict[str, Any]) -> List[str]:
    name = str(args.get("name") or "")
    if not name:
        raise HTTPException(400, "mcp_tools add requires name")
    argv = ["add", name]
    if args.get("url"):
        argv += ["--url", str(args.get("url"))]
    if args.get("stdio_format"):
        argv += ["--stdio-format", str(args.get("stdio_format"))]
    if args.get("cwd"):
        argv += ["--cwd", str(args.get("cwd"))]
    env = args.get("env") or {}
    if env and not isinstance(env, dict):
        raise HTTPException(400, "mcp_tools add env must be an object")
    for k, v in env.items():
        argv += ["--env", f"{k}={v}"]
    if args.get("agent_id"):
        argv += ["--agent-id", str(args.get("agent_id"))]
    if args.get("run_as_user"):
        argv += ["--run-as-user", str(args.get("run_as_user"))]
    if args.get("hub_url"):
        argv += ["--hub-url", str(args.get("hub_url"))]
    if args.get("disabled"):
        argv.append("--disabled")
    if args.get("force"):
        argv.append("--force")
    command = args.get("command")
    command_args = args.get("args") or []
    if command:
        argv.append(str(command))
        if not isinstance(command_args, list):
            raise HTTPException(400, "mcp_tools add args must be a list")
        argv += [str(x) for x in command_args]
    elif not args.get("url"):
        raise HTTPException(400, "mcp_tools add requires url or command")
    return argv


async def _mcp_tools_manage_on_shell(srv: str, args: Dict[str, Any]) -> Dict[str, Any]:
    action = str(args.get("action") or "list")
    if action not in {"list", "add", "remove", "install", "status", "cat"}:
        raise HTTPException(400, f"unsupported mcp_tools action: {action}")
    verbose = bool(args.get("verbose") or args.get("include_raw"))
    target = {"target_server": srv, "target_agent": _virtual_shell_agent_id(srv), "target_os": _server_os_text(srv)}

    if action == "list":
        res = await _run_target_gptadmin_mcp(srv, ["list", "--json"], check=False)
        if not res.get("ok"):
            out = {"action": action, "count": 0, "servers": [], "result": _compact_cli_result(res), **target}
            return out
        cfg = res.get("json") if isinstance(res.get("json"), dict) else {}
        data = {"action": action, "servers": cfg.get("mcpServers", {}), "config": cfg, "raw": res, **target}
        out = _mcp_tools_compact(data, verbose=verbose)
        out.update(target)
        return out

    if action == "cat":
        name = str(args.get("name") or "")
        res = await _run_target_gptadmin_mcp(srv, ["cat", name] if name else ["cat"], check=False)
        if not res.get("ok"):
            return {"action": action, "name": name or None, "result": _compact_cli_result(res), **target}
        data = {"action": action, "name": name or None, "config": res.get("json"), "raw": res, **target}
        out = _mcp_tools_compact(data, verbose=verbose)
        out.update(target)
        return out

    if action == "status":
        name = str(args.get("name") or "")
        argv = ["status"] + ([name] if name else [])
        if args.get("backend"):
            argv += ["--backend", str(args.get("backend"))]
        data = {"action": action, "name": name or None, "raw": await _run_target_gptadmin_mcp(srv, argv, check=False), **target}
        out = _mcp_tools_compact(data, verbose=verbose)
        out.update(target)
        return out

    if action == "install":
        name = str(args.get("name") or "")
        argv = ["install"] + ([name] if name else [])
        if args.get("backend"):
            argv += ["--backend", str(args.get("backend"))]
        data = {"action": action, "name": name or None, "raw": await _run_target_gptadmin_mcp(srv, argv, timeout=120, check=False), **target}
        out = _mcp_tools_compact(data, verbose=verbose)
        out.update(target)
        return out

    if action == "remove":
        name = str(args.get("name") or "")
        if not name:
            raise HTTPException(400, "mcp_tools remove requires name")
        before = await _run_target_gptadmin_mcp(srv, ["list", "--json"], check=False)
        cfg = before.get("json") if isinstance(before.get("json"), dict) else {}
        server_cfg = (cfg.get("mcpServers") or {}).get(name) or {}
        agent_id = str(server_cfg.get("agent_id") or "")
        argv = ["remove", name]
        if args.get("keep_service"):
            argv.append("--keep-service")
        if args.get("backend"):
            argv += ["--backend", str(args.get("backend"))]
        raw = await _run_target_gptadmin_mcp(srv, argv, timeout=120, check=False)
        registry_removed = _forget_mcp_relay_agent(agent_id)
        data = {"action": action, "name": name, "agent_id": agent_id or None, "registry_removed": registry_removed, "stopped": None, "raw": raw, **target}
        out = _mcp_tools_compact(data, verbose=verbose)
        out.update(target)
        return out

    # add
    name = str(args.get("name") or "")
    add_res = await _run_target_gptadmin_mcp(srv, _mcp_tools_add_argv(args), timeout=120)
    install_res = None
    if bool(args.get("install", True)) and not args.get("disabled"):
        install_argv = ["install", name]
        if args.get("backend"):
            install_argv += ["--backend", str(args.get("backend"))]
        install_res = await _run_target_gptadmin_mcp(srv, install_argv, timeout=120)
    list_res = await _run_target_gptadmin_mcp(srv, ["list", "--json"], check=False)
    cfg = list_res.get("json") if isinstance(list_res.get("json"), dict) else {}
    data = {"action": action, "name": name, "added": add_res, "installed": install_res, "server": (cfg.get("mcpServers") or {}).get(name), "config": cfg, **target}
    out = _mcp_tools_compact(data, verbose=verbose)
    out.update(target)
    return out


def _compact_cli_result(res: Optional[Dict[str, Any]]) -> Optional[Dict[str, Any]]:
    if res is None:
        return None
    out: Dict[str, Any] = {"ok": bool(res.get("ok")), "returncode": res.get("returncode")}
    stderr = str(res.get("stderr") or "").strip()
    if stderr:
        out["stderr_tail"] = stderr[-1000:]
    stdout = str(res.get("stdout") or "").strip()
    if stdout and not isinstance(res.get("json"), (dict, list)):
        out["stdout_tail"] = stdout[-1000:]
    return out


def _mcp_server_summary(name: str, server: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "name": name,
        "enabled": bool(server.get("enabled", True)),
        "agent_id": server.get("agent_id"),
        "command": server.get("command"),
        "args": server.get("args") or [],
        "stdio_format": server.get("stdio_format"),
        "cwd": server.get("cwd"),
        "run_as_user": server.get("run_as_user"),
    }


def _mcp_servers_summary(servers_obj: Dict[str, Any]) -> List[Dict[str, Any]]:
    if not isinstance(servers_obj, dict):
        return []
    return [_mcp_server_summary(name, srv if isinstance(srv, dict) else {}) for name, srv in sorted(servers_obj.items())]


def _mcp_tools_compact(data: Dict[str, Any], *, verbose: bool = False) -> Dict[str, Any]:
    if verbose:
        return data
    action = data.get("action")
    out: Dict[str, Any] = {"action": action}
    if data.get("name") is not None:
        out["name"] = data.get("name")
    if action == "list":
        servers = data.get("servers") or {}
        out["count"] = len(servers) if isinstance(servers, dict) else 0
        out["servers"] = _mcp_servers_summary(servers if isinstance(servers, dict) else {})
        return out
    if action == "cat":
        cfg = data.get("config")
        if isinstance(cfg, dict) and data.get("name"):
            out["server"] = _mcp_server_summary(str(data.get("name")), cfg)
        else:
            servers = (cfg or {}).get("mcpServers", {}) if isinstance(cfg, dict) else {}
            out["count"] = len(servers) if isinstance(servers, dict) else 0
            out["servers"] = _mcp_servers_summary(servers if isinstance(servers, dict) else {})
        return out
    if action == "add":
        server = data.get("server") or {}
        out["server"] = _mcp_server_summary(str(data.get("name") or ""), server if isinstance(server, dict) else {})
        out["added"] = _compact_cli_result(data.get("added"))
        out["installed"] = _compact_cli_result(data.get("installed"))
        return out
    if action in {"install", "status"}:
        out["result"] = _compact_cli_result(data.get("raw"))
        return out
    if action == "remove":
        out["removed"] = _compact_cli_result(data.get("raw"))
        stopped = data.get("stopped") or {}
        if isinstance(stopped, dict) and stopped:
            out["unit"] = stopped.get("unit")
        return out
    return out


def _mcp_slug(value: str) -> str:
    out = re.sub(r"[^A-Za-z0-9_.-]+", "-", value.strip()).strip("-._")
    return out or "mcp-agent"


def _mcp_expected_unit_name(name: str, server: Optional[Dict[str, Any]] = None) -> str:
    server = server or {}
    agent_id = str(server.get("agent_id") or f"{_socket_module.gethostname()}-{_mcp_slug(name)}")
    return f"gptadmin-mcp-{_mcp_slug(agent_id)}.service"


def _mcp_stop_systemd_unit(name: str, server: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    unit = _mcp_expected_unit_name(name, server)
    cmd = ["sudo", "-n", "systemctl", "disable", "--now", unit]
    cp = subprocess.run(cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    rm_cmd = ["sudo", "-n", "rm", "-f", f"/etc/systemd/system/{unit}"]
    rm = subprocess.run(rm_cmd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    dr = subprocess.run(["sudo", "-n", "systemctl", "daemon-reload"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    return {
        "unit": unit,
        "disable_now": {"returncode": cp.returncode, "stdout": cp.stdout, "stderr": cp.stderr},
        "remove_unit": {"returncode": rm.returncode, "stdout": rm.stdout, "stderr": rm.stderr},
        "daemon_reload": {"returncode": dr.returncode, "stdout": dr.stdout, "stderr": dr.stderr},
    }


def _mcp_tools_manage(args: Dict[str, Any]) -> Dict[str, Any]:
    action = str(args.get("action") or "list")
    if action not in {"list", "add", "remove", "install", "status", "cat"}:
        raise HTTPException(400, f"unsupported mcp_tools action: {action}")
    verbose = bool(args.get("verbose") or args.get("include_raw"))
    if action == "list":
        res = _run_gptadmin_mcp(["list", "--json"])
        cfg = res.get("json") if isinstance(res.get("json"), dict) else {}
        data = {"action": action, "servers": cfg.get("mcpServers", {}), "config": cfg, "raw": res}
        return _mcp_tools_compact(data, verbose=verbose)
    if action == "cat":
        name = str(args.get("name") or "")
        res = _run_gptadmin_mcp(["cat", name] if name else ["cat"])
        data = {"action": action, "name": name or None, "config": res.get("json"), "raw": res}
        return _mcp_tools_compact(data, verbose=verbose)
    if action == "status":
        name = str(args.get("name") or "")
        argv = ["status"] + ([name] if name else [])
        if args.get("backend"):
            argv += ["--backend", str(args.get("backend"))]
        data = {"action": action, "name": name or None, "raw": _run_gptadmin_mcp(argv)}
        return _mcp_tools_compact(data, verbose=verbose)
    if action == "install":
        name = str(args.get("name") or "")
        argv = ["install"] + ([name] if name else [])
        if args.get("backend"):
            argv += ["--backend", str(args.get("backend"))]
        data = {"action": action, "name": name or None, "raw": _run_gptadmin_mcp(argv, timeout=120)}
        return _mcp_tools_compact(data, verbose=verbose)
    if action == "remove":
        name = str(args.get("name") or "")
        if not name:
            raise HTTPException(400, "mcp_tools remove requires name")
        before = _run_gptadmin_mcp(["list", "--json"])
        cfg = before.get("json") if isinstance(before.get("json"), dict) else {}
        server_cfg = (cfg.get("mcpServers") or {}).get(name) or {}
        agent_id = str(server_cfg.get("agent_id") or "")
        # gptadmin.py mcp remove now stops/uninstalls the supervisor service itself.
        # Do not pre-stop here; pre-stopping races with CLI removal and causes noisy
        # "unit file does not exist" stderr even when removal succeeds.
        stopped = None
        argv = ["remove", name]
        if args.get("keep_service"):
            argv.append("--keep-service")
        if args.get("backend"):
            argv += ["--backend", str(args.get("backend"))]
        raw = _run_gptadmin_mcp(argv, timeout=120, check=False)
        registry_removed = _forget_mcp_relay_agent(agent_id)
        data = {"action": action, "name": name, "agent_id": agent_id or None, "registry_removed": registry_removed, "stopped": stopped, "raw": raw}
        return _mcp_tools_compact(data, verbose=verbose)

    # add
    name = str(args.get("name") or "")
    if not name:
        raise HTTPException(400, "mcp_tools add requires name")
    # IMPORTANT: gptadmin.py mcp add uses argparse.REMAINDER for command args.
    # Therefore every gptadmin option must be placed BEFORE positional command/args.
    argv = ["add", name]
    if args.get("url"):
        argv += ["--url", str(args.get("url"))]
    if args.get("stdio_format"):
        argv += ["--stdio-format", str(args.get("stdio_format"))]
    if args.get("cwd"):
        argv += ["--cwd", str(args.get("cwd"))]
    env = args.get("env") or {}
    if env and not isinstance(env, dict):
        raise HTTPException(400, "mcp_tools add env must be an object")
    for k, v in env.items():
        argv += ["--env", f"{k}={v}"]
    if args.get("agent_id"):
        argv += ["--agent-id", str(args.get("agent_id"))]
    if args.get("run_as_user"):
        argv += ["--run-as-user", str(args.get("run_as_user"))]
    if args.get("hub_url"):
        argv += ["--hub-url", str(args.get("hub_url"))]
    if args.get("disabled"):
        argv.append("--disabled")
    if args.get("force"):
        argv.append("--force")
    command = args.get("command")
    command_args = args.get("args") or []
    if command:
        argv.append(str(command))
        if not isinstance(command_args, list):
            raise HTTPException(400, "mcp_tools add args must be a list")
        argv += [str(x) for x in command_args]
    elif not args.get("url"):
        raise HTTPException(400, "mcp_tools add requires url or command")
    add_res = _run_gptadmin_mcp(argv)
    install_res = None
    if bool(args.get("install", True)) and not args.get("disabled"):
        install_argv = ["install", name]
        if args.get("backend"):
            install_argv += ["--backend", str(args.get("backend"))]
        install_res = _run_gptadmin_mcp(install_argv, timeout=120)
    list_res = _run_gptadmin_mcp(["list", "--json"])
    cfg = list_res.get("json") if isinstance(list_res.get("json"), dict) else {}
    data = {"action": action, "name": name, "added": add_res, "installed": install_res, "server": (cfg.get("mcpServers") or {}).get(name), "config": cfg}
    return _mcp_tools_compact(data, verbose=verbose)



def _hub_job_id(prefix: str) -> str:
    return f"{prefix}-{int(time.time())}-{uuid.uuid4().hex[:8]}"


def _now_iso() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def _canonical_shell_agent(agent: str) -> str:
    agent = str(agent or "")
    if not agent:
        raise HTTPException(400, "agent is required")
    if agent.startswith(VIRTUAL_SHELL_PREFIX):
        _server_from_virtual_shell_agent(agent)
        return agent
    if agent in servers:
        return _virtual_shell_agent_id(agent)
    raise HTTPException(404, f"unknown shell agent/server: {agent}")


def _hub_task(status: str, tool: str, args: Dict[str, Any], *, job_id: Optional[str] = None, result: Optional[Dict[str, Any]] = None, error: Optional[str] = None) -> Dict[str, Any]:
    jid = job_id or _hub_job_id(tool.replace("_", "-"))
    now = time.time()
    job = {"kind": "hub_tool_task", "job_id": jid, "tool": tool, "args": args, "status": status, "created_at": now, "updated_at": now, "created_at_iso": _now_iso(), "updated_at_iso": _now_iso()}
    if result is not None:
        job["result"] = result
    if error:
        job["error"] = error
    mcp_relay_jobs[jid] = job
    try:
        _save_all_state()
    except Exception:
        pass
    return job


def _hub_task_update(job_id: str, **updates: Any) -> Dict[str, Any]:
    job = mcp_relay_jobs.get(job_id) or {"kind": "hub_tool_task", "job_id": job_id}
    job.update(updates)
    job["updated_at"] = time.time()
    job["updated_at_iso"] = _now_iso()
    mcp_relay_jobs[job_id] = job
    try:
        _save_all_state()
    except Exception:
        pass
    return job


async def _shell_tool(agent: str, tool_name: str, args: Dict[str, Any], *, background: bool = False) -> Dict[str, Any]:
    return await _virtual_shell_tool_call(_canonical_shell_agent(agent), tool_name, args, request_background=background)


def _extract_shell_result(envelope: Dict[str, Any], agent: str) -> Dict[str, Any]:
    sc = envelope.get("structuredContent") if isinstance(envelope, dict) else None
    if not isinstance(sc, dict):
        return {"returncode": None, "stdout": "", "stderr": "", "raw": envelope}
    if "result" in sc:
        return sc["result"] if isinstance(sc.get("result"), dict) else sc
    results = sc.get("results")
    if isinstance(results, dict):
        srv = _server_from_virtual_shell_agent(_canonical_shell_agent(agent))
        val = results.get(srv) or results.get(agent) or {}
        if isinstance(val, dict):
            return val
    return sc


def _stdout_text(result: Dict[str, Any]) -> str:
    out = result.get("stdout") if isinstance(result, dict) else ""
    if isinstance(out, dict):
        if out.get("_spilled"):
            fp = str(out.get("file_path") or "")
            try:
                return Path(fp).read_text(errors="replace") if fp else str(out)
            except Exception:
                return str(out.get("preview_head") or "") + str(out.get("preview_tail") or "")
        return json.dumps(out, ensure_ascii=False)
    return str(out or "")


def _stderr_text(result: Dict[str, Any]) -> str:
    err = result.get("stderr") if isinstance(result, dict) else ""
    return json.dumps(err, ensure_ascii=False) if isinstance(err, (dict, list)) else str(err or "")


async def _hub_file_transfer(args: Dict[str, Any]) -> Dict[str, Any]:
    source_agent = _canonical_shell_agent(str(args.get("source_agent") or args.get("from_agent") or ""))
    target_agent = _canonical_shell_agent(str(args.get("target_agent") or args.get("to_agent") or ""))
    source_path = str(args.get("source_path") or "")
    target_path = str(args.get("target_path") or "")
    if not source_path or not target_path:
        raise HTTPException(400, "file_transfer requires source_path and target_path")
    overwrite = bool(args.get("overwrite", False))
    mkdirs = bool(args.get("mkdirs", True))
    verify_sha256 = bool(args.get("verify_sha256", True))
    job = _hub_task("running", "file_transfer", args)
    job_id = job["job_id"]
    HUB_TRANSFERS_DIR.mkdir(parents=True, exist_ok=True)
    try:
        qsrc = shlex.quote(source_path)
        src_cmd = "set -euo pipefail\n" + f"test -f {qsrc}\n" + f"stat -c '%s' {qsrc} 2>/dev/null || stat -f '%z' {qsrc}\n" + f"sha256sum {qsrc} 2>/dev/null | awk '{{print $1}}' || shasum -a 256 {qsrc} | awk '{{print $1}}'\n" + f"base64 < {qsrc}"
        src_env = await _shell_tool(source_agent, "shell_exec", {"cmd": src_cmd, "timeout": int(args.get("source_timeout") or 120)})
        src_res = _extract_shell_result(src_env, source_agent)
        if int(src_res.get("returncode") or 0) != 0:
            raise RuntimeError(f"source read failed rc={src_res.get('returncode')}: {_stderr_text(src_res)[:1000]}")
        lines = _stdout_text(src_res).splitlines()
        if len(lines) < 3:
            raise RuntimeError("source output too short; expected size, sha256, base64")
        size = int(lines[0].strip())
        source_sha = lines[1].strip()
        b64 = "\n".join(lines[2:]).strip()
        if len(b64) > int(args.get("max_inline_b64") or 48_000_000):
            raise RuntimeError("file too large for MVP inline relay; add chunked rootd transfer next")
        calc_sha = hashlib.sha256(base64.b64decode(b64.encode("ascii"))).hexdigest()
        if verify_sha256 and calc_sha != source_sha:
            raise RuntimeError(f"hub sha256 mismatch source={source_sha} hub={calc_sha}")
        qdst = shlex.quote(target_path)
        qdir = shlex.quote(str(Path(target_path).parent))
        exists_guard = "" if overwrite else f"test ! -e {qdst}\n"
        mkdir_cmd = f"mkdir -p {qdir}\n" if mkdirs else ""
        target_script = "set -euo pipefail\n" + mkdir_cmd + exists_guard + f"tmp={qdst}.gptadmin-tmp-{job_id}\n" + "cat > \"$tmp.b64\" <<'GPTADMIN_B64_EOF'\n" + b64 + "\nGPTADMIN_B64_EOF\n" + "base64 -d < \"$tmp.b64\" > \"$tmp\"\nrm -f \"$tmp.b64\"\n" + "sha256sum \"$tmp\" 2>/dev/null | awk '{print $1}' || shasum -a 256 \"$tmp\" | awk '{print $1}'\n" + f"mv \"$tmp\" {qdst}\n" + f"stat -c '%s' {qdst} 2>/dev/null || stat -f '%z' {qdst}\n"
        dst_env = await _shell_tool(target_agent, "shell_exec", {"cmd": target_script, "timeout": int(args.get("target_timeout") or 120)})
        dst_res = _extract_shell_result(dst_env, target_agent)
        if int(dst_res.get("returncode") or 0) != 0:
            raise RuntimeError(f"target write failed rc={dst_res.get('returncode')}: {_stderr_text(dst_res)[:1000]} {_stdout_text(dst_res)[:1000]}")
        dst_lines = _stdout_text(dst_res).splitlines()
        target_sha = (dst_lines[0].strip() if dst_lines else "")
        target_size = int(dst_lines[1].strip()) if len(dst_lines) > 1 and dst_lines[1].strip().isdigit() else None
        if verify_sha256 and target_sha != source_sha:
            raise RuntimeError(f"target sha256 mismatch source={source_sha} target={target_sha}")
        result = {"job_id": job_id, "source_agent": source_agent, "target_agent": target_agent, "source_path": source_path, "target_path": target_path, "bytes": size, "target_bytes": target_size, "sha256": source_sha, "verified": bool(verify_sha256)}
        _hub_task_update(job_id, status="completed", completed_at=time.time(), result=result)
        return _mcp_envelope_text(f"Transferred {size} bytes {source_agent}:{source_path} -> {target_agent}:{target_path}", result)
    except Exception as e:
        _hub_task_update(job_id, status="failed", error=str(e), failed_at=time.time())
        raise


async def _hub_port_forward(args: Dict[str, Any]) -> Dict[str, Any]:
    action = str(args.get("action") or "start")
    registry = _load_json_dict(HUB_PORT_FORWARDS_FILE)
    def _parse_dt_ts(value: Any) -> Optional[float]:
        if not value:
            return None
        try:
            if isinstance(value, (int, float)):
                return float(value)
            text = str(value).strip()
            if not text:
                return None
            if text.endswith("Z"):
                text = text[:-1] + "+00:00"
            return datetime.datetime.fromisoformat(text).timestamp()
        except Exception:
            return None

    def _prune_and_filter_forwards(show_stopped: bool = False, stopped_ttl_sec: Optional[int] = None) -> tuple[Dict[str, Any], int]:
        now = time.time()
        ttl = int(stopped_ttl_sec if stopped_ttl_sec is not None else os.getenv("GPTADMIN_PORT_FORWARD_STOPPED_TTL_SEC", "86400"))
        changed = False
        visible: Dict[str, Any] = {}
        for fid, item in list(registry.items()):
            if not isinstance(item, dict):
                continue
            if str(item.get("status") or "") == "stopped":
                stopped_ts = _parse_dt_ts(item.get("stopped_at"))
                if ttl >= 0 and stopped_ts and now - stopped_ts > ttl:
                    registry.pop(fid, None)
                    changed = True
                    continue
                if not show_stopped:
                    continue
            visible[fid] = item
        if changed:
            _save_json_dict(HUB_PORT_FORWARDS_FILE, registry)
        return visible, len(registry)

    if action == "list":
        visible, total = _prune_and_filter_forwards(bool(args.get("show_stopped", False)), args.get("stopped_ttl_sec"))
        return _mcp_envelope_text(f"{len(visible)} port forward(s)", {"forwards": visible, "count": len(visible), "total_count": total, "show_stopped": bool(args.get("show_stopped", False))})
    if action in {"stop", "status"}:
        forward_id = str(args.get("forward_id") or args.get("id") or "")
        if not forward_id:
            raise HTTPException(400, f"port_forward action={action} requires forward_id")
        item = registry.get(forward_id)
        if not item:
            raise HTTPException(404, f"unknown forward_id {forward_id}")
        if action == "stop":
            pid = str(item.get("pid") or "")
            pattern = str(item.get("pattern") or forward_id)
            cmd = f"[ -n {shlex.quote(pid)} ] && kill {shlex.quote(pid)} 2>/dev/null || true\npkill -f {shlex.quote(pattern)} 2>/dev/null || true\ntrue"
            await _shell_tool(str(item.get("from_agent")), "shell_exec", {"cmd": cmd, "timeout": 15})
            item["status"] = "stopped"; item["stopped_at"] = _now_iso(); registry[forward_id] = item; _save_json_dict(HUB_PORT_FORWARDS_FILE, registry)
            return _mcp_envelope_text(f"Stopped port forward {forward_id}", item)
        check_env = await _shell_tool(str(item.get("to_agent") or item.get("from_agent")), "shell_exec", {"cmd": str(item.get("check_cmd") or "true"), "timeout": 15})
        res = _extract_shell_result(check_env, str(item.get("to_agent") or item.get("from_agent")))
        item["last_check"] = {"returncode": res.get("returncode"), "stdout": _stdout_text(res), "stderr": _stderr_text(res), "at": _now_iso()}
        return _mcp_envelope_text(f"Port forward {forward_id} status rc={res.get('returncode')}", item)
    if action != "start":
        raise HTTPException(400, "port_forward action must be start, list, status or stop")
    from_agent = _canonical_shell_agent(str(args.get("from_agent") or ""))
    to_agent = _canonical_shell_agent(str(args.get("to_agent") or args.get("check_agent") or from_agent))
    kind = str(args.get("kind") or "reverse")
    local_host = str(args.get("local_host") or "localhost")
    local_port = int(args.get("local_port") or 0)
    remote_port = int(args.get("remote_port") or args.get("listen_port") or 0)
    bind_host = str(args.get("bind_host") or "127.0.0.1")
    ssh_host = str(args.get("ssh_host") or args.get("ssh_alias") or "")
    if kind not in {"reverse", "local"} or not local_port or not ssh_host:
        raise HTTPException(400, "port_forward requires kind reverse/local, local_port and ssh_host")
    forward_id = str(args.get("forward_id") or f"pf_{kind}_{uuid.uuid4().hex[:8]}")
    if kind == "reverse":
        if not remote_port: raise HTTPException(400, "reverse port_forward requires remote_port")
        spec = f"{bind_host}:{remote_port}:{local_host}:{local_port}" if bind_host else f"{remote_port}:{local_host}:{local_port}"
        flag = "-R"; check_agent = to_agent; check_port = remote_port
    else:
        check_port = int(args.get("listen_port") or remote_port or 0)
        if not check_port: raise HTTPException(400, "local port_forward requires listen_port or remote_port")
        spec = f"{bind_host}:{check_port}:{local_host}:{local_port}" if bind_host else f"{check_port}:{local_host}:{local_port}"
        flag = "-L"; check_agent = from_agent
    check_cmd = f"ss -ltn 2>/dev/null | grep -E '(:|\\]){check_port}\\b' || lsof -nP -iTCP:{check_port} -sTCP:LISTEN 2>/dev/null || nc -zv {shlex.quote(bind_host or '127.0.0.1')} {check_port}"
    pattern = f"gptadmin-port-forward-{forward_id}"
    extra = str(args.get("ssh_extra") or "")
    identity = str(args.get("identity_file") or "")
    ident = f"-i {shlex.quote(identity)} " if identity else ""
    restart = f"pkill -f {shlex.quote(pattern)} 2>/dev/null || true\n" if bool(args.get("restart_existing", True)) else ""
    cmd = "set -euo pipefail\n" + restart + f"nohup ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 {extra} {ident}{flag} {shlex.quote(spec)} {shlex.quote(ssh_host)} >/tmp/{forward_id}.log 2>&1 &\n" + "pid=$!\nsleep 1\nkill -0 $pid\necho $pid\n"
    job = _hub_task("running", "port_forward", args); job_id = job["job_id"]
    try:
        env = await _shell_tool(from_agent, "shell_exec", {"cmd": cmd, "timeout": 20})
        res = _extract_shell_result(env, from_agent)
        if int(res.get("returncode") or 0) != 0:
            raise RuntimeError(f"ssh tunnel start failed rc={res.get('returncode')}: {_stdout_text(res)} {_stderr_text(res)}")
        pid_line = (_stdout_text(res).splitlines() or [""])[-1].strip(); pid = int(pid_line) if pid_line.isdigit() else None
        check_env = await _shell_tool(check_agent, "shell_exec", {"cmd": check_cmd, "timeout": 15})
        check_res = _extract_shell_result(check_env, check_agent)
        item = {"forward_id": forward_id, "job_id": job_id, "status": "running" if int(check_res.get("returncode") or 0) == 0 else "started_unverified", "kind": kind, "from_agent": from_agent, "to_agent": to_agent, "ssh_host": ssh_host, "spec": spec, "pid": pid, "pattern": pattern, "check_cmd": check_cmd, "created_at": _now_iso(), "last_check": {"returncode": check_res.get("returncode"), "stdout": _stdout_text(check_res), "stderr": _stderr_text(check_res), "at": _now_iso()}}
        registry[forward_id] = item; _save_json_dict(HUB_PORT_FORWARDS_FILE, registry); _hub_task_update(job_id, status="completed", completed_at=time.time(), result=item)
        return _mcp_envelope_text(f"Port forward {forward_id}: {item['status']}", item)
    except Exception as e:
        _hub_task_update(job_id, status="failed", error=str(e), failed_at=time.time())
        raise

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
        {
            "name": "mcp_tools",
            "description": "Manage GPTAdmin stdio/remote MCP servers on the hub host: list, add, remove, install, status or cat. Backend is auto-detected by host unless explicitly overridden.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "action": {"type": "string", "enum": ["list", "add", "remove", "install", "status", "cat"], "default": "list"},
                    "name": {"type": ["string", "null"], "description": "MCP server name."},
                    "url": {"type": ["string", "null"], "description": "Remote MCP URL; wraps npx -y mcp-remote URL."},
                    "command": {"type": ["string", "null"], "description": "Local stdio command, e.g. npx."},
                    "args": {"type": ["array", "null"], "items": {"type": "string"}, "description": "Args for local stdio command."},
                    "env": {"type": ["object", "null"], "additionalProperties": {"type": "string"}},
                    "cwd": {"type": ["string", "null"]},
                    "stdio_format": {"type": ["string", "null"], "enum": ["auto", "framed", "ndjson", "jsonl", "content-length", None]},
                    "agent_id": {"type": ["string", "null"]},
                    "run_as_user": {"type": ["string", "null"]},
                    "hub_url": {"type": ["string", "null"]},
                    "backend": {"type": ["string", "null"], "enum": ["systemd", "launchd", "windows-task", None], "description": "Optional override. If omitted, mcp_agent_manager auto-detects backend from host OS."},
                    "force": {"type": "boolean", "default": False},
                    "disabled": {"type": "boolean", "default": False},
                    "install": {"type": "boolean", "default": True, "description": "After add, install/start relay service."},
                    "keep_service": {"type": "boolean", "default": False, "description": "Remove registry entry only; keep service/config files."},
                    "verbose": {"type": "boolean", "default": False, "description": "Return raw CLI stdout/json/config; default compact."},
                    "include_raw": {"type": "boolean", "default": False, "description": "Same as verbose."}
                },
                "additionalProperties": False,
            },
        },
    ]
    tools.extend([
        {
            "name": "file_transfer",
            "description": "Hub-orchestrated file copy between two shell agents. Uses existing shell_exec and returns a normal hub task/result.",
            "inputSchema": {"type": "object", "properties": {
                "source_agent": {"type": "string"},
                "source_path": {"type": "string"},
                "target_agent": {"type": "string"},
                "target_path": {"type": "string"},
                "overwrite": {"type": "boolean", "default": False},
                "mkdirs": {"type": "boolean", "default": True},
                "verify_sha256": {"type": "boolean", "default": True},
                "keep_relay_file": {"type": "boolean", "default": False}
            }, "required": ["source_agent", "source_path", "target_agent", "target_path"], "additionalProperties": False},
        },
        {
            "name": "port_forward",
            "description": "Hub-orchestrated SSH port forward between shell agents. Actions: start/list/status/stop.",
            "inputSchema": {"type": "object", "properties": {
                "action": {"type": "string", "enum": ["start", "list", "status", "stop"], "default": "start"},
                "kind": {"type": "string", "enum": ["reverse", "local"], "default": "reverse"},
                "from_agent": {"type": "string"},
                "to_agent": {"type": "string"},
                "ssh_host": {"type": "string"},
                "ssh_alias": {"type": "string"},
                "local_host": {"type": "string", "default": "localhost"},
                "local_port": {"type": "integer"},
                "remote_port": {"type": "integer"},
                "listen_port": {"type": "integer"},
                "bind_host": {"type": "string", "default": "127.0.0.1"},
                "identity_file": {"type": "string"},
                "ssh_extra": {"type": "string"},
                "restart_existing": {"type": "boolean", "default": True},
                "forward_id": {"type": "string"},
                "show_stopped": {"type": "boolean", "default": False},
                "stopped_ttl_sec": {"type": "integer", "default": 86400}
            }, "additionalProperties": False},
        },
    ])
    return {"tools": tools}


def _mask_secret_value(value: Any) -> Optional[str]:
    if value is None:
        return None
    text = str(value)
    if not text:
        return None
    digest = hashlib.sha256(text.encode("utf-8", "replace")).hexdigest()[:8]
    masked = f"{text[:3]}...{text[-3:]}" if len(text) > 8 else f"{text[:1]}...{text[-1:]}"
    return f"{masked} len={len(text)} sha256={digest}"


def _configured_secret_names() -> List[str]:
    names = ["CTL_TOKEN", "MCP_RELAY_AGENT_TOKEN", "OAUTH_CLIENT_SECRET", "ADMIN_PASSWORD", "OPENAI_API_KEY", "LITELLM_API_KEY"]
    return [name for name in names if os.getenv(name)]


def _shell_cli_info(srv: str, info: Dict[str, Any], verbose: bool = False) -> Dict[str, Any]:
    os_text = str(info.get("os") or "").lower()
    backend = str(info.get("backend") or "")
    is_macos = "darwin" in os_text or "mac" in os_text
    service = "com.gptadmin.rootd" if is_macos else "gptadmin-rootd.service"
    service_manager = "launchctl" if is_macos else "systemctl"
    status_cmd = "gptadmin status"
    logs_cmd = "gptadmin logs shell"
    if is_macos:
        service_status = f"sudo launchctl print system/{service}"
        service_restart = f"sudo launchctl kickstart -k system/{service}"
    else:
        service_status = f"systemctl status {service} --no-pager"
        service_restart = f"sudo systemctl restart {service}"
    info_out: Dict[str, Any] = {
        "cli": "/usr/local/bin/gptadmin",
        "status": status_cmd,
        "logs": logs_cmd,
        "install_dir": "/opt/gptadmin",
        "config": "/etc/gptadmin/gptadmin.env",
        "service_manager": service_manager,
        "shell_service": service,
        "shell_service_status": service_status,
        "shell_service_restart": service_restart,
        "mcp_config": "/etc/gptadmin/mcp.json",
        "mcp_agent_configs": "/etc/gptadmin/mcp-agents.d",
        "mcp_add_remote": "sudo gptadmin mcp add NAME --install --status --url https://example.com/mcp",
        "mcp_add_stdio": "sudo gptadmin mcp add NAME --install --status -- npx -y some-mcp-package --flag value",
        "mcp_status": "gptadmin mcp status NAME",
        "mcp_list": "gptadmin mcp list",
    }
    if backend == "ssh":
        info_out["note"] = "this shell is proxied; run local service commands on proxy host, not necessarily on the SSH target"
    if srv == "admin-server-100":
        info_out["repo_helper"] = "/home/admin/gptadmin/mcp-add NAME -- npx -y package"
    return info_out


def _hub_proxy_install_info(verbose: bool = False) -> Dict[str, Any]:
    unit = "hub_proxy.service"
    info: Dict[str, Any] = {
        "unit": unit,
        "file": "/etc/systemd/system/hub_proxy.service",
        "working_dir": str(GPTADMIN_REPO_ROOT),
        "python": str(GPTADMIN_PYTHON),
        "script": str(GPTADMIN_REPO_ROOT / "services" / "main_package" / "hub_proxy.py"),
        "user": "admin",
        "public_origin": PUBLIC_ORIGIN,
        "mcp_relay": "/mcp-relay/*",
        "openapi": "/actions/openapi.yaml",
    }
    if verbose:
        try:
            cp = subprocess.run(["systemctl", "show", unit, "-p", "FragmentPath", "-p", "DropInPaths", "-p", "WorkingDirectory", "-p", "ExecStart", "-p", "User", "-p", "ActiveState", "--no-pager"], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
            if cp.returncode == 0:
                shown: Dict[str, str] = {}
                for line in cp.stdout.splitlines():
                    if "=" in line:
                        k, v = line.split("=", 1)
                        if v:
                            shown[k] = v
                info["systemd"] = shown
        except Exception:
            pass
    env_names = ["CTL_TOKEN", "MCP_RELAY_AGENT_TOKEN", "PUBLIC_ORIGIN", "MCP_RESOURCE", "MCP_RELAY_DEFAULT_TIMEOUT", "MCP_RELAY_POLL_MAX_TIMEOUT", "OAUTH_CLIENT_SECRET", "ADMIN_PASSWORD"]
    info["env"] = {name: (_mask_secret_value(os.getenv(name)) if name in SENSITIVE_KEYS or "TOKEN" in name or "SECRET" in name or "PASSWORD" in name else os.getenv(name)) for name in env_names if os.getenv(name)}
    return _omit_none(info)


def _shell_help(srv: str, args: Dict[str, Any]) -> Dict[str, Any]:
    section = str(args.get("section") or "all").lower()
    verbose = bool(args.get("verbose") or args.get("include_raw"))
    info = servers.get(srv) or {}
    agent_id = _virtual_shell_agent_id(srv)
    is_hub_host = srv == _socket_module.gethostname() or srv == "admin-server-100"
    proxy_for = info.get("proxy_for")
    proxy_via = info.get("proxy_via")
    default_cwd = info.get("default_cwd") or "not reported"
    out: Dict[str, Any] = {"agent": agent_id, "kind": "GPTAdmin virtual shell MCP agent", "host": srv}
    if section in {"all", "summary", "routing"}:
        if is_hub_host:
            role = ["hub host", "shell executor", "MCP router host"]
            path = "ChatGPT/OpenAI Actions -> local public hub -> virtual shell agent -> shell on this host"
        elif proxy_for or proxy_via:
            role = ["proxied shell agent"]
            path = f"ChatGPT/OpenAI Actions -> public hub -> {agent_id} -> proxy host {proxy_via or 'unknown'} -> SSH target {proxy_for or srv}"
        else:
            role = ["shell executor"]
            path = f"ChatGPT/OpenAI Actions -> public hub -> {agent_id} -> shell on {srv}"
        out["summary"] = _omit_none({"role": role, "path": path, "hub_on_this_host": bool(is_hub_host), "mode": info.get("mode"), "backend": info.get("backend"), "cwd": default_cwd})
    if section in {"all", "params"}:
        out["parameter_notes"] = {
            "shell_exec": {
                "cmd": "shell command; quote carefully; avoid printing full secrets",
                "cwd": "optional; if omitted hub uses cached default_cwd when known, otherwise shell process cwd",
                "timeout": f"seconds; >{SYNC_TIMEOUT_S}s auto-switches to background",
                "background": "true returns job_id for getMcpJob polling",
                "env": "per-command env object; not persisted",
                "deferred": "not_before, expires_at and max_attempts control delayed or offline delivery; small numbers mean relative seconds",
                "retry_policy": "none (default, no offline queue), offline_queue (deliver once when reconnected), at_least_once (redeliver/retry until terminal/expired/max_attempts)",
            },
            "tasks/task_edit": {
                "task_id": "task id or mcp-shell job id; ack=true removes terminal task after reading",
                "include_result": "for task lists, set false to avoid large output",
                "control": "task_edit can retry_now, pause or cancel deferred/background work",
            },
            "mcp_tools": {
                "scope": "manages real stdio or remote MCP servers in hub config, not packages on this shell host",
                "url": "remote MCP shortcut via npx -y mcp-remote URL",
                "command_args": "stdio MCP process command and arguments",
                "stdio_format": "auto usually works; chrome-devtools uses ndjson here",
                "install": "add can also install and start the generated service",
                "pitfall": "GPTAdmin options must be before command args; hub tool handles that ordering",
            },
        }
    if section in {"all", "config"}:
        cfg = _omit_none({"version": info.get("version") or info.get("build_version"), "build": info.get("git_commit"), "public_hub": PUBLIC_ORIGIN, "openapi": "/actions/openapi.yaml", "mcp_relay": "/mcp-relay/*", "cwd": default_cwd, "outbox": info.get("outbox_dir"), "shell_cli": _shell_cli_info(srv, info, verbose)})
        if is_hub_host:
            cfg.update({"repo": str(GPTADMIN_REPO_ROOT), "config_dir": str(CONFIG_DIR), "mcp_config": "/etc/gptadmin/mcp.json", "mcp_agent_configs": "/etc/gptadmin/mcp-agents.d", "hub_proxy": _hub_proxy_install_info(verbose)})
        if proxy_for or proxy_via:
            cfg["proxy"] = _omit_none({"proxy_for": proxy_for, "proxy_via": proxy_via, "ssh_host": info.get("ssh_host"), "ssh_port": info.get("ssh_port"), "ssh_user": info.get("ssh_user")})
        out["config"] = cfg
    if section in {"all", "secrets"}:
        configured = _configured_secret_names()
        out["secrets"] = {"policy": "never print full secrets", "configured": configured, "mask_format": "abc...xyz len=N sha256=xxxxxxxx"}
        if verbose:
            out["secrets"]["masked"] = {name: _mask_secret_value(os.getenv(name)) for name in configured}
    if section in {"all", "architecture"}:
        out["architecture"] = {
            "hub": "public GPTAdmin entrypoint and dynamic MCP router; not present on every shell host",
            "rootd": "durable transport layer between hub and local capabilities on every platform",
            "shell": "local executor capability exposed through rootd as virtual MCP agent shell:<server>",
            "real_mcp": "stdio or remote MCP capability; migration target is supervision/transport behind rootd",
            "polling_vs_webhook": "transport detail owned by rootd; capabilities should not implement their own fragile hub transport",
        }
    if section in {"all", "rescue"}:
        out["rescue"] = {
            "principle": "If a shell agent on the hub host is broken, use hub.mcp_tools to add a temporary real MCP rescue shell on that same host, fix shellmcp/rootd, then remove the rescue MCP.",
            "safe_migration_rule": "Never stop/restart the current shell agent from a plain nohup/background command inside its own systemd cgroup; systemd may kill the migration with the old service. Use systemd-run transient units for self-migration.",
            "systemd_run_pattern": "sudo systemd-run --unit=gptadmin-shellmcp-migrate --collect /bin/bash /path/to/migrate.sh",
            "private_tmp_note": "If systemd-run cannot see /tmp script paths, put the script in a durable path visible outside the service namespace, e.g. /opt/gptadmin/migrate.sh.",
            "rescue_mcp_flow": [
                "hub.mcp_tools add name=rescue-shell-<host> command=python3 args=[minimal stdio MCP exposing shell_exec] agent_id=RescueShell<Host> run_as_user=root backend=systemd install=true",
                "call RescueShell<Host>.shell_exec to run systemctl start shellmcp.service || systemctl start rootd.service and inspect logs",
                "after normal shell:<host> works, hub.mcp_tools remove name=rescue-shell-<host> backend=systemd",
            ],
            "ssh_jump_fallback": "If the broken host is not the hub host, use a live shell agent with SSH/LAN reachability to run: sudo systemctl start shellmcp.service || sudo systemctl start rootd.service || sudo systemctl start gptadmin-rootd.service.",
        }
    return _omit_none(out)


def _shell_tools_list() -> Dict[str, Any]:
    tools = [
        {
            "name": "shell_exec",
            "description": "RUN FIRST for shell work. Execute a command on this host; use background=true for long jobs.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "cmd": {"type": "string"},
                    "cwd": {"type": ["string", "null"]},
                    "timeout": {"type": ["integer", "null"]},
                    "env": {"type": ["object", "null"], "additionalProperties": True},
                    "background": {"type": "boolean", "default": False},
                    "not_before": {"type": ["number", "string", "null"], "description": "Earliest run time: seconds from now, ISO, or epoch."},
                    "expires_at": {"type": ["number", "string", "null"], "description": "Expiry time: seconds from now, ISO, or epoch."},
                    "max_attempts": {"type": ["integer", "null"], "description": "Maximum deferred dispatch attempts."},
                    "retry_policy": {"type": "string", "enum": ["none", "offline_queue", "at_least_once"], "default": DEFAULT_RETRY_POLICY, "description": "none=no retry; offline_queue=run once after reconnect; at_least_once=retry until terminal/expired/max_attempts."},
                },
                "required": ["cmd"],
                "additionalProperties": False,
            },
        },
        {
            "name": "tasks",
            "description": "List/get shell jobs. task_id omitted=list; set task_id=get; ack=true clears terminal jobs.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "task_id": {"type": ["string", "null"]},
                    "ack": {"type": "boolean", "default": False},
                    "status": {"type": ["string", "null"], "description": "Optional status filter for list mode."},
                    "limit": {"type": ["integer", "null"], "default": 50},
                    "sort_by": {"type": ["string", "null"], "default": "updated_at", "description": "Sort field for list mode, e.g. updated_at, created_at, not_before, status."},
                    "order": {"type": ["string", "null"], "enum": ["asc", "desc", None], "default": "desc"},
                    "include_result": {"type": "boolean", "default": True},
                    "include_history": {"type": "boolean", "default": True},
                },
                "additionalProperties": False,
            },
        },
        {
            "name": "capability_registry",
            "description": "Show host capabilities and supervised MCP services behind rootd.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "include_status": {"type": "boolean", "default": True, "description": "Include supervisor status for MCP capabilities."},
                    "include_raw": {"type": "boolean", "default": False, "description": "Include raw command result used to build the registry."},
                },
                "additionalProperties": False,
            },
        },
        {
            "name": "mcp_lifecycle",
            "description": "Start/stop/restart/status a supervised MCP service on this host via rootd.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "name": {"type": "string", "description": "Capability name, agent_id, id, or legacy service name."},
                    "action": {"type": "string", "enum": ["status", "start", "stop", "restart"], "default": "status"},
                    "backend": {"type": ["string", "null"], "enum": ["systemd", "launchd", "windows-task", None], "default": None, "description": "Override supervisor backend; default follows OS."},
                    "include_raw": {"type": "boolean", "default": False}
                },
                "required": ["name"],
                "additionalProperties": False,
            },
        },
        {
            "name": "mcp_tools",
            "description": "Manage MCP servers on this host: list/add/remove/install/status/cat. Uses local GPTAdmin/rootd service backend.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "action": {"type": "string", "enum": ["list", "add", "remove", "install", "status", "cat"], "default": "list"},
                    "name": {"type": ["string", "null"], "description": "MCP server name."},
                    "url": {"type": ["string", "null"], "description": "Remote MCP URL; wraps npx -y mcp-remote URL."},
                    "command": {"type": ["string", "null"], "description": "Local stdio command, e.g. npx."},
                    "args": {"type": ["array", "null"], "items": {"type": "string"}, "description": "Args for local stdio command."},
                    "env": {"type": ["object", "null"], "additionalProperties": {"type": "string"}},
                    "cwd": {"type": ["string", "null"]},
                    "stdio_format": {"type": ["string", "null"], "enum": ["auto", "framed", "ndjson", "jsonl", "content-length", None]},
                    "agent_id": {"type": ["string", "null"]},
                    "run_as_user": {"type": ["string", "null"]},
                    "hub_url": {"type": ["string", "null"]},
                    "backend": {"type": ["string", "null"], "enum": ["systemd", "launchd", "windows-task", None]},
                    "force": {"type": "boolean", "default": False},
                    "disabled": {"type": "boolean", "default": False},
                    "install": {"type": "boolean", "default": True, "description": "After add, install/start relay service."},
                    "keep_service": {"type": "boolean", "default": False, "description": "Remove registry entry only; keep service/config files."},
                    "verbose": {"type": "boolean", "default": False, "description": "Return raw CLI stdout/json/config; default compact."},
                    "include_raw": {"type": "boolean", "default": False, "description": "Same as verbose."}
                },
                "additionalProperties": False,
            },
        },
        {
            "name": "help",
            "description": "Agent help: routing, key params, config paths, architecture, rescue hints, safe fingerprints.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "section": {"type": ["string", "null"], "enum": ["all", "summary", "params", "config", "secrets", "architecture", "rescue", None], "default": "all"},
                    "verbose": {"type": "boolean", "default": False},
                    "include_raw": {"type": "boolean", "default": False}
                },
                "additionalProperties": False,
            },
        },
        {
            "name": "task_edit",
            "description": "Edit queued/background job: schedule, retry_now, pause, cancel, expiry, attempts.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "task_id": {"type": "string"},
                    "not_before": {"type": ["number", "string", "null"], "description": "Earliest run time: seconds from now, ISO, or epoch."},
                    "expires_at": {"type": ["number", "string", "null"], "description": "Expiry time: seconds from now, ISO, or epoch."},
                    "next_attempt_at": {"type": ["number", "string", "null"], "description": "Next retry time. Prefer relative seconds from now; ISO timestamp is also accepted. Epoch seconds are accepted for machine callers."},
                    "max_attempts": {"type": ["integer", "null"]},
                    "action": {"type": ["string", "null"], "enum": ["cancel", "retry_now", "pause", None]},
                    "reason": {"type": ["string", "null"]},
                },
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
    if ack and out.get("status") in {"completed", "failed", "orphaned", "expired", "cancelled"}:
        background_tasks.get(srv, {}).pop(tid, None)
        results.get(srv, {}).pop(tid, None)
        out["acked"] = True
    else:
        out["acked"] = False
    return out


def _task_public_row(srv: str, tid: str, task: Dict[str, Any], *, include_result: bool = True, include_history: bool = True) -> Dict[str, Any]:
    row = {"server": srv, "task_id": tid, **task}
    if not include_result:
        row.pop("result", None)
    if not include_history:
        row.pop("edit_history", None)
    return row


def _legacy_list_tasks(srv: str, status: Optional[str] = None, limit: Optional[int] = 50, sort_by: Optional[str] = "updated_at", order: Optional[str] = "desc", include_result: bool = True, include_history: bool = True) -> Dict[str, Any]:
    items = []
    for tid, task in (background_tasks.get(srv) or {}).items():
        row = _task_public_row(srv, tid, task, include_result=include_result, include_history=include_history)
        if status and row.get("status") != status:
            continue
        items.append(row)
    sort_field = sort_by or "updated_at"
    reverse = (order or "desc") != "asc"
    def sort_key(row: Dict[str, Any]):
        value = row.get(sort_field)
        if value is None and sort_field == "updated_at":
            value = row.get("completed_at") or row.get("created_at")
        if isinstance(value, (int, float, str)):
            return value
        return str(value)
    items.sort(key=sort_key, reverse=reverse)
    try:
        n = int(limit or 50)
    except Exception:
        n = 50
    if n > 0:
        items = items[:n]
    return {"server": srv, "status_filter": status, "sort_by": sort_field, "order": "desc" if reverse else "asc", "count": len(items), "tasks": items}


async def _hub_tool_call(tool_name: str, args: Dict[str, Any]) -> Dict[str, Any]:
    if tool_name == "file_transfer":
        return await _hub_file_transfer(args)
    if tool_name == "port_forward":
        return await _hub_port_forward(args)
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
    if tool_name == "mcp_tools":
        data = _mcp_tools_manage(args)
        action = data.get("action")
        name = data.get("name")
        if action == "list":
            count = len(data.get("servers") or {})
            return _mcp_envelope_text(f"{count} configured MCP tool(s) on hub host", data)
        return _mcp_envelope_text(f"MCP {action} {name or ''}: ok", data)
    raise HTTPException(404, f"unknown hub tool {tool_name}")


async def _virtual_shell_tool_call(agent_id: str, tool_name: str, args: Dict[str, Any], request_background: bool = False) -> Dict[str, Any]:
    srv = _server_from_virtual_shell_agent(agent_id)
    if tool_name == "shell_exec":
        cmd = args.get("cmd")
        if not cmd:
            raise HTTPException(400, "shell_exec requires cmd")
        # The command timeout is the subprocess limit on the agent, not a reason
        # to skip the hub's synchronous wait window.  A call becomes background
        # only when requested explicitly or after the sync wait expires.
        background = bool(args.get("background", False) or request_background)
        req = BulkExec(
            servers=[srv],
            cmd=str(cmd),
            cwd=args.get("cwd"),
            timeout=args.get("timeout"),
            env=args.get("env") if isinstance(args.get("env"), dict) else None,
            background=background,
            not_before=args.get("not_before"),
            expires_at=args.get("expires_at"),
            max_attempts=args.get("max_attempts"),
            retry_policy=str(args.get("retry_policy") or DEFAULT_RETRY_POLICY),
        )
        data = await bulk_exec(req)
        result = (data.get("results") or {}).get(srv, {})
        if not (isinstance(result, dict) and result.get("background")):
            result = _spill_single_result(srv, result, str(cmd))
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
            task = background_tasks.get(srv, {}).get(result["task_id"], {})
            return _omit_none({
                "background": True,
                "job_id": job_id,
                "status": "running",
                "agent": agent_id,
                "task_id": result["task_id"],
                "timing": _compact_timing("running", started_at=task.get("started_at") or time.time()),
                "command": str(cmd),
                "cwd": str(args.get("cwd")) if args.get("cwd") else str((servers.get(srv) or {}).get("default_cwd") or "agent process cwd (default cwd not reported by this rootd yet)"),
                "message": "Shell command continues in background.",
            })
        return _mcp_envelope_text(f"shell_exec completed on {srv}", {"server": srv, "result": result})

    if tool_name == "tasks":
        tid_raw = args.get("task_id")
        tid = str(tid_raw or "")
        if tid:
            real_tid = _resolve_legacy_task_id(srv, tid)
            task = _legacy_get_task(srv, real_tid, ack=bool(args.get("ack")))
            if not task:
                return _mcp_envelope_text(f"Task not found: {tid}", {"server": srv, "task_id": tid, "status": "not_found"})
            if real_tid != tid:
                task["requested_task_id"] = tid
            task = _spill_mcp_structured(srv, task, f"tasks status {real_tid}")
            return _mcp_envelope_text(f"Task {tid}: {task.get('status')}", task)
        data = _legacy_list_tasks(srv, status=args.get("status"), limit=args.get("limit"), sort_by=args.get("sort_by"), order=args.get("order"), include_result=bool(args.get("include_result", True)), include_history=bool(args.get("include_history", True)))
        data = _spill_mcp_structured(srv, data, "tasks list")
        return _mcp_envelope_text(f"{data.get('count', 0)} task(s) on {srv}", data)

    if tool_name == "capability_registry":
        include_status = bool(args.get("include_status", True))
        registry = await _capability_registry_via_rootd(srv, include_status=include_status)
        if registry.get("ok"):
            if args.get("include_raw"):
                registry["include_raw"] = True
            return _mcp_envelope_text(f"{registry.get('summary', {}).get('mcp_count', 0)} MCP capabilities on {srv}", registry)
        direct_error = dict(registry)
        py = 'import json, os, pathlib, subprocess, sys, socket\n\ndef red(v):\n    keys=("token","secret","password","passwd","api_key","apikey","authorization","bearer","x-api-key")\n    if isinstance(v, dict):\n        return {str(k): ("***MASKED***" if any(w in str(k).lower() for w in keys) else red(val)) for k,val in v.items()}\n    if isinstance(v, list):\n        out=[]; skip=False\n        for item in v:\n            if skip:\n                out.append("***MASKED***"); skip=False; continue\n            if isinstance(item,str) and item.lower() in {"--header","--token","--api-key","--password","--secret"}:\n                out.append(item); skip=True; continue\n            out.append(red(item))\n        return out\n    if isinstance(v,str) and ("authorization:" in v.lower() or v.lower().startswith(("bearer ","apikey ","basic "))):\n        return v.split(None,1)[0] + " ***MASKED***"\n    return v\n\ndef state(unit):\n    if not sys.platform.startswith("linux"):\n        return {"backend":"unsupported","unit":unit,"active":None}\n    try:\n        cp=subprocess.run(["systemctl","show",unit,"-p","LoadState","-p","ActiveState","-p","SubState","--value"],text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=3)\n        vals=[x.strip() for x in cp.stdout.splitlines()]\n        return {"backend":"systemd","unit":unit,"load_state":vals[0] if len(vals)>0 else None,"active_state":vals[1] if len(vals)>1 else None,"sub_state":vals[2] if len(vals)>2 else None,"active": (vals[1]=="active") if len(vals)>1 else False}\n    except Exception as e:\n        return {"backend":"systemd","unit":unit,"active":False,"error":str(e)}\n\ncfg_path=pathlib.Path(os.getenv("GPTADMIN_MCP_CONFIG","/etc/gptadmin/mcp.json"))\nagents_dir=pathlib.Path(os.getenv("GPTADMIN_MCP_AGENTS_DIR","/etc/gptadmin/mcp-agents.d"))\nhost=os.getenv("ROOTD_NAME") or os.getenv("SHELL_NAME") or socket.gethostname()\ntry:\n    cfg=json.loads(cfg_path.read_text()) if cfg_path.is_file() else {}\nexcept Exception as e:\n    cfg={"_error":str(e)}\nservers=cfg.get("mcpServers") if isinstance(cfg.get("mcpServers"),dict) else {}\nmcps=[]\nfor name,spec in sorted(servers.items()):\n    if not isinstance(spec,dict): continue\n    agent_id=str(spec.get("agent_id") or spec.get("name") or name)\n    item={"id":"mcp:"+agent_id,"name":name,"agent_id":agent_id,"kind":"mcp","role":"capability_executor","hosted_by":host,"supervised_by":"rootd","legacy_service":f"gptadmin-mcp-{agent_id}.service","enabled":bool(spec.get("enabled",True)),"transport":"stdio_or_remote","command":spec.get("command"),"args":red(spec.get("args") or []),"cwd":spec.get("cwd"),"run_as_user":spec.get("run_as_user") or spec.get("user"),"stdio_format":spec.get("stdio_format") or spec.get("transport") or "auto","config_file":str(agents_dir / f"{name}.json"),"migration_state":"legacy_relay_supervised; rootd_registry_visible"}\n    if INCLUDE_STATUS:\n        item["supervisor"]=state(f"gptadmin-mcp-{agent_id}.service")\n    mcps.append(item)\nout={"ok":True,"schema_version":1,"host":host,"transport_role":"rootd_transport_layer","capability_host":True,"capabilities":[{"id":"shell","kind":"shell","role":"local_executor","hosted_by":host},{"id":"tasks","kind":"task_store","role":"durable_queue_view","hosted_by":host},{"id":"logs","kind":"logs","role":"diagnostics","hosted_by":host},{"id":"system","kind":"system","role":"host_introspection","hosted_by":host},*mcps],"summary":{"mcp_count":len(mcps),"enabled_mcp_count":sum(1 for x in mcps if x.get("enabled"))}}\nprint(json.dumps(out,ensure_ascii=False))\n'
        py = py.replace("INCLUDE_STATUS", "True" if include_status else "False")
        req = BulkExec(servers=[srv], cmd="python3 - <<'PY'\n" + py + "\nPY", timeout=20, background=False)
        data = await bulk_exec(req)
        result = (data.get("results") or {}).get(srv, {})
        stdout = (result or {}).get("stdout") if isinstance(result, dict) else None
        try:
            registry = json.loads(stdout or "{}")
        except Exception:
            registry = {"ok": False, "error": "failed to parse capability registry JSON", "raw": result}
        registry.setdefault("transport", "fallback_shell_exec")
        registry["fallback_from"] = direct_error
        if args.get("include_raw"):
            registry["raw_result"] = result
        return _mcp_envelope_text(f"{registry.get('summary', {}).get('mcp_count', 0)} MCP capabilities on {srv}", registry)

    if tool_name == "mcp_lifecycle":
        mcp_ref = str(args.get("name") or "").strip()
        if not mcp_ref:
            raise HTTPException(400, "mcp_lifecycle requires name")
        action = str(args.get("action") or "status").strip().lower()
        backend = args.get("backend")
        backend = str(backend).strip().lower() if backend else None
        data = await _mcp_lifecycle_via_rootd(srv, mcp_ref, action, backend=backend)
        if not args.get("include_raw"):
            data.pop("body", None)
        unit = ((data.get("capability") or {}).get("legacy_service") or data.get("unit") or mcp_ref) if isinstance(data, dict) else mcp_ref
        return _mcp_envelope_text(f"MCP lifecycle {action} {unit}: {'ok' if data.get('ok') else 'failed'}", data)

    if tool_name == "mcp_tools":
        data = await _mcp_tools_manage_on_shell(srv, args)
        action = data.get("action")
        name = data.get("name")
        if action == "list":
            result = data.get("result") if isinstance(data.get("result"), dict) else None
            if result and not result.get("ok", True):
                return _mcp_envelope_text(f"MCP list on {srv}: failed", data)
            count = len(data.get("servers") or {})
            return _mcp_envelope_text(f"{count} configured MCP tool(s) on {srv}", data)
        return _mcp_envelope_text(f"MCP {action} {name or ''} on {srv}: ok", data)

    if tool_name == "help":
        data = _shell_help(srv, args)
        return _mcp_envelope_text(f"GPTAdmin shell help for {srv}", data)

    if tool_name == "task_edit":
        tid = str(args.get("task_id") or "")
        if not tid:
            raise HTTPException(400, "task_edit requires task_id")
        edit = {k: args.get(k) for k in ("not_before", "expires_at", "next_attempt_at", "max_attempts", "action", "reason") if k in args}
        data = _edit_task(srv, tid, edit)
        return _mcp_envelope_text(f"Task {tid} edited: {data['task'].get('status')}", data)

    raise HTTPException(404, f"unknown shell tool {tool_name}")


def _mcp_relay_tool_name(method: str, params: Optional[Dict[str, Any]] = None) -> str:
    params = params or {}
    if method == "tools/call":
        return str(params.get("name") or "")
    return method


MCP_RELAY_QUEUED_STATUSES = {"queued", "queued_offline", "queued_ready", "dispatch_failed"}


def _mcp_relay_agent_alive(agent_id: str, info: Optional[Dict[str, Any]] = None) -> bool:
    info = info or mcp_relay_agents.get(agent_id) or {}
    return bool(info) and time.time() - float(info.get("last_seen", 0)) <= DEAD_S


def _mcp_relay_payload_from_job(job: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "id": job.get("job_id"),
        "jsonrpc": "2.0",
        "method": job.get("method"),
        "params": job.get("params") or {},
        "created_at": int(job.get("created_at") or time.time()),
    }


def _mcp_relay_job_deliverable(job: Dict[str, Any], now: float) -> bool:
    status = str(job.get("status") or "")
    if status in MCP_RELAY_QUEUED_STATUSES:
        return True
    if status == "running" and _retry_policy_redelivers(job.get("retry_policy")):
        started = float(job.get("started_at") or job.get("updated_at") or job.get("created_at") or 0)
        return started > 0 and (now - started) >= MCP_RELAY_RUNNING_REQUEUE_S
    return False


def _mcp_relay_next_queued_job(agent_id: str) -> Optional[Dict[str, Any]]:
    now = time.time()
    candidates = []
    for job_id, job in mcp_relay_jobs.items():
        if not isinstance(job, dict) or job.get("agent_id") != agent_id:
            continue
        if not _mcp_relay_job_deliverable(job, now):
            continue
        expires_at = job.get("expires_at")
        if expires_at is not None and float(expires_at) <= now:
            job.update({"status": "expired", "completed_at": int(now), "updated_at": int(now), "error": {"message": "MCP relay job expired before delivery"}})
            continue
        candidates.append((float(job.get("created_at") or 0), job_id, job))
    if not candidates:
        # Compatibility with pre-durable in-memory queue entries that may exist before
        # a hub restart. New jobs are persisted in mcp_relay_jobs and do not use this.
        q = mcp_relay_queues.get(agent_id) or []
        if q:
            return q.pop(0)
        return None
    _, job_id, job = sorted(candidates, key=lambda x: (x[0], x[1]))[0]
    attempts = int(job.get("delivery_attempts") or 0) + 1
    previous_status = job.get("status")
    job.update({"status": "running", "delivery_attempts": attempts, "started_at": int(now), "updated_at": int(now)})
    if previous_status == "running":
        job.setdefault("redeliveries", 0)
        job["redeliveries"] = int(job.get("redeliveries") or 0) + 1
        job["redelivered_at"] = int(now)
    _audit_event({"event":"mcp_relay_dispatch","target":agent_id,"method":job.get("method"),"tool_name":job.get("tool_name"),"job_id":job_id,"attempts":attempts,"previous_status":previous_status})
    return _mcp_relay_payload_from_job(job)


def _mcp_relay_enqueue(agent_id: str, method: str, params: Optional[Dict[str, Any]] = None, *, retry_policy: Any = None) -> str:
    info = mcp_relay_agents.get(agent_id)
    if not info:
        raise HTTPException(404, f"unknown MCP relay agent {agent_id}")
    policy = _normalize_retry_policy(retry_policy, MCP_RELAY_DEFAULT_RETRY_POLICY)
    job_id = f"mcp-{int(time.time())}-{uuid.uuid4().hex[:8]}"
    params = params or {}
    tool_name = _mcp_relay_tool_name(method, params)
    alive = _mcp_relay_agent_alive(agent_id, info)
    if not alive and not _retry_policy_queues_offline(policy):
        raise HTTPException(503, f"MCP relay agent {agent_id} is offline; pass retry_policy=offline_queue or at_least_once to queue for reconnect")
    status = "queued" if alive else "queued_offline"
    mcp_relay_jobs[job_id] = {
        "job_id": job_id,
        "kind": "real_mcp",
        "agent_id": agent_id,
        "method": method,
        "tool_name": tool_name,
        "params": params,
        "status": status,
        "created_at": int(time.time()),
        "updated_at": int(time.time()),
        "queued_reason": "online" if alive else "offline",
        "retry_policy": policy,
        "expires_at": int(time.time()) + (HUB_DEFERRED_DEFAULT_TTL_S if _retry_policy_queues_offline(policy) else MCP_RELAY_NO_RETRY_TTL_S),
    }
    log.info(
        "mcp_relay: queued target=%s method=%s tool=%s job_id=%s status=%s",
        agent_id, method, tool_name, job_id, status,
    )
    _audit_event({"event":"mcp_relay_queued","target":agent_id,"method":method,"tool_name":tool_name,"job_id":job_id,"status":status})
    try:
        _save_all_state()
    except Exception as e:
        log.warning("mcp_relay: immediate state save failed job_id=%s err=%s", job_id, e)
    return job_id


async def _mcp_relay_wait(job_id: str, timeout: Optional[int] = None) -> Optional[Dict[str, Any]]:
    wait_s = min(int(timeout or MCP_RELAY_DEFAULT_TIMEOUT), SYNC_TIMEOUT_S, MCP_RELAY_SYNC_WAIT_MAX_S)
    deadline = time.time() + wait_s
    job = mcp_relay_jobs.get(job_id)
    if job:
        log.info(
            "mcp_relay: wait start target=%s method=%s tool=%s job_id=%s timeout_s=%s status=%s",
            job.get("agent_id"), job.get("method"), job.get("tool_name"), job_id, wait_s, job.get("status"),
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
        job = _mcp_relay_next_queued_job(agent_id)
        if job:
            try:
                _save_all_state()
            except Exception as e:
                log.warning("mcp_relay: state save after dispatch failed agent=%s job=%s err=%s", agent_id, job.get("id"), e)
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
        if job.get("status") in {"completed", "failed"}:
            job.setdefault("duplicate_results", 0)
            job["duplicate_results"] = int(job.get("duplicate_results") or 0) + 1
        job.update({"status": "completed" if res.ok else "failed", "result": payload, "completed_at": int(time.time()), "updated_at": int(time.time())})
        log.info(
            "mcp_relay: result target=%s method=%s tool=%s job_id=%s ok=%s",
            job.get("agent_id"), job.get("method"), job.get("tool_name"), res.id, res.ok,
        )
        _audit_event({"event":"mcp_relay_result","target":job.get("agent_id"),"method":job.get("method"),"tool_name":job.get("tool_name"),"job_id":res.id,"ok":res.ok})
    else:
        log.info("mcp_relay: result target=%s job_id=%s ok=%s job=unknown", agent_id, res.id, res.ok)
        _audit_event({"event":"mcp_relay_result","target":agent_id,"job_id":res.id,"ok":res.ok,"job":"unknown"})
    try:
        _save_all_state()
    except Exception as e:
        log.warning("mcp_relay: state save after result failed job_id=%s err=%s", res.id, e)
    return {"ok": True}


@app.get("/mcp-relay/agents", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def mcp_relay_agents_list(statuses: Optional[List[str]] = Query(default=None), purge_stale: bool = Query(False)):
    purged = _purge_stale_mcp_relay_agents() if purge_stale else None
    agents = _all_public_agents(statuses=statuses)
    out = {"agents": agents}
    if purged is not None:
        out["purged"] = purged
    return out


@app.post("/mcp-relay/tools", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
async def mcp_relay_tools(req: McpRelayToolsReq):
    target = _mcp_relay_select_agent(req.target)
    log.info("mcp_relay: tools/list request target=%s background=%s timeout=%s", target, req.background, req.timeout)
    if target == VIRTUAL_HUB_AGENT_ID:
        return {"agent_id": target, "status": "completed", "response": _hub_tools_list()}
    if _is_virtual_shell_agent(target):
        return {"agent_id": target, "status": "completed", "response": _shell_tools_list()}

    job_id = _mcp_relay_enqueue(target, "tools/list", {}, retry_policy=req.retry_policy)
    job = mcp_relay_jobs.get(job_id) or {}
    if job.get("status") == "queued_offline":
        return {"agent_id": target, "status": "queued_offline", "background": True, "job_id": job_id, "message": "tools/list queued for delivery when MCP relay agent reconnects"}
    if req.background:
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
        args = dict(req.arguments or {})
        if req.retry_policy and "retry_policy" not in args:
            args["retry_policy"] = req.retry_policy
        data = await _virtual_shell_tool_call(target, req.tool_name, args, request_background=bool(req.background))
        if isinstance(data, dict) and data.get("background"):
            return {"agent_id": target, "status": "running", **data}
        status = "completed"
        if isinstance(data, dict):
            sc = data.get("structuredContent") if isinstance(data.get("structuredContent"), dict) else {}
            res = sc.get("result") if isinstance(sc.get("result"), dict) else {}
            if res.get("status") in {"offline", "expired", "cancelled"}:
                status = str(res.get("status"))
            elif res.get("error"):
                status = "failed"
        return {"agent_id": target, "status": status, "response": data}

    job_id = _mcp_relay_enqueue(target, "tools/call", {"name": req.tool_name, "arguments": req.arguments or {}}, retry_policy=req.retry_policy)
    job = mcp_relay_jobs.get(job_id) or {}
    if job.get("status") == "queued_offline":
        return {"agent_id": target, "status": "queued_offline", "background": True, "job_id": job_id, "message": "tool call queued for delivery when MCP relay agent reconnects"}
    if req.background:
        return {"agent_id": target, "status": "running", "background": True, "job_id": job_id, "message": "tool call queued"}
    data = await _mcp_relay_wait(job_id, req.timeout)
    if data is None:
        return {"agent_id": target, "status": "running", "background": True, "job_id": job_id, "message": "MCP relay job is still running"}
    return {"agent_id": target, "status": "completed", "response": data}



def _fmt_ts(value: Any) -> Optional[str]:
    if value is None or value == "":
        return None
    try:
        ts = float(value)
    except Exception:
        return None
    if ts <= 0:
        return None
    # Hub timezone is the server local timezone; currently MSK on admin-server-100.
    return datetime.datetime.fromtimestamp(ts).astimezone().strftime("%Y-%m-%d %H:%M:%S %Z")


def _fmt_duration(seconds: Optional[float]) -> Optional[str]:
    if seconds is None:
        return None
    try:
        s = max(0.0, float(seconds))
    except Exception:
        return None
    if s < 1:
        return f"{int(round(s * 1000))}ms"
    if s < 10:
        return f"{s:.1f}s"
    if s < 60:
        return f"{int(round(s))}s"
    m, sec = divmod(int(round(s)), 60)
    if m < 60:
        return f"{m}m {sec}s" if sec else f"{m}m"
    h, m = divmod(m, 60)
    return f"{h}h {m}m" if m else f"{h}h"


def _compact_timing(status: str, created_at: Any = None, started_at: Any = None, completed_at: Any = None) -> Dict[str, Any]:
    now_ts = time.time()
    timing: Dict[str, Any] = {}
    if started_at:
        timing["started"] = _fmt_ts(started_at)
        if completed_at:
            timing["elapsed"] = _fmt_duration(float(completed_at) - float(started_at))
        else:
            timing["running"] = _fmt_duration(now_ts - float(started_at))
    elif created_at:
        timing["created"] = _fmt_ts(created_at)
        timing["queued"] = _fmt_duration(now_ts - float(created_at))
    return _omit_none(timing)


def _omit_none(obj: Dict[str, Any]) -> Dict[str, Any]:
    return {k: v for k, v in obj.items() if v is not None}


def _task_result_fields(result: Any) -> Dict[str, Any]:
    if not isinstance(result, dict):
        return {"response": result} if result is not None else {}
    out: Dict[str, Any] = {}
    for key in (
        "returncode", "stdout", "stderr", "error",
        "_spilled", "file_path", "preview_head", "preview_tail",
        "stdout_spilled", "stderr_spilled", "stdout_file", "stderr_file",
        "stdout_preview_head", "stdout_preview_tail", "stderr_preview_head", "stderr_preview_tail",
        "cwd_effective", "run_as_user",
    ):
        if key in result and result.get(key) is not None:
            out[key] = result.get(key)
    return out


def _resolve_cwd(srv: Optional[str], task: Dict[str, Any], result_fields: Dict[str, Any]) -> Tuple[str, str]:
    if task.get("cwd"):
        return str(task.get("cwd")), "request.cwd"
    payload = task.get("payload") if isinstance(task.get("payload"), dict) else {}
    if payload.get("cwd"):
        return str(payload.get("cwd")), "payload.cwd"
    if result_fields.get("cwd_effective"):
        return str(result_fields.get("cwd_effective")), "result.cwd_effective"
    info = servers.get(str(srv or "")) or {}
    if info.get("default_cwd"):
        return str(info.get("default_cwd")), "heartbeat.default_cwd"
    return "agent process cwd (default cwd not reported by this rootd yet)", "unknown"


def _compact_virtual_shell_task(job_id: str, job: Dict[str, Any], task: Optional[Dict[str, Any]], *, acked: bool) -> Dict[str, Any]:
    now_ts = time.time()
    task = task or {}
    status = str(task.get("status") or job.get("status") or "running_or_unknown")
    result_fields = _task_result_fields(task.get("result"))
    created_at = task.get("created_at") or job.get("created_at")
    started_at = task.get("started_at") or job.get("started_at")
    completed_at = task.get("completed_at") or job.get("completed_at")
    cwd, cwd_source = _resolve_cwd(str(job.get("server") or ""), task, result_fields)
    base = {
        "status": status,
        "agent": job.get("agent_id"),
        "task_id": job.get("task_id") or task.get("task_id"),
        "timing": _compact_timing(status, created_at=created_at, started_at=started_at, completed_at=completed_at),
        "command": task.get("cmd") or (task.get("payload") or {}).get("cmd"),
        "cwd": cwd,
        "acked": bool(acked),
    }
    result_fields.pop("cwd_effective", None)
    base.update(result_fields)
    return _omit_none(base)


def _compact_real_mcp_job(job_id: str, job: Optional[Dict[str, Any]], result: Optional[Dict[str, Any]], *, acked: bool) -> Dict[str, Any]:
    now_ts = time.time()
    job = job or {}
    result = result or {}
    status = "completed" if result.get("ok", True) else "failed" if result else str(job.get("status") or "running_or_unknown")
    created_at = job.get("created_at") or result.get("created_at")
    completed_at = job.get("completed_at") or result.get("completed_at")
    out = {
        "status": status,
        "agent": result.get("agent_id") or job.get("agent_id"),
        "tool": job.get("tool_name") or job.get("method"),
        "timing": _compact_timing(status, created_at=created_at, completed_at=completed_at),
        "acked": bool(acked),
    }
    if result:
        if result.get("ok", True):
            out["response"] = result.get("result")
        else:
            out["error"] = result.get("error") or {"message": "MCP relay job failed"}
    else:
        out["response"] = job.get("result")
        out["error"] = job.get("error")
    return _omit_none(out)


def _compact_mcp_job_response(job_id: str, *, job: Optional[Dict[str, Any]], task: Optional[Dict[str, Any]] = None, result: Optional[Dict[str, Any]] = None, acked: bool = False, verbose: bool = False, legacy_response: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    if verbose:
        return legacy_response or {"job_id": job_id, "job": job, "task": task, "result": result, "acked": acked}
    if job and job.get("kind") == "virtual_shell_task":
        return _compact_virtual_shell_task(job_id, job, task, acked=acked)
    return _compact_real_mcp_job(job_id, job, result, acked=acked)

@app.get("/mcp-relay/job/{job_id}", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def mcp_relay_job_status(job_id: str, ack: bool = Query(False), verbose: bool = Query(False), include_raw: bool = Query(False)):
    job = mcp_relay_jobs.get(job_id)
    acked = False
    want_verbose = bool(verbose or include_raw)

    if job and job.get("kind") == "virtual_shell_task":
        srv = str(job.get("server"))
        tid = str(job.get("task_id"))
        task = _legacy_get_task(srv, tid, ack=ack)
        status = task.get("status") if task else "running_or_unknown"
        if task and status in {"completed", "failed", "orphaned"}:
            job["status"] = status
            job["result"] = task
            job["completed_at"] = task.get("completed_at") or int(time.time())
            if ack:
                mcp_relay_jobs.pop(job_id, None)
                acked = True
        legacy = {"job_id": job_id, "status": status, "agent_id": job.get("agent_id"), "response": task, "error": None, "acked": acked}
        return _compact_mcp_job_response(job_id, job=job, task=task, acked=acked, verbose=want_verbose, legacy_response=legacy)

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
        legacy = {"job_id": job_id, "status": status, "agent_id": agent_id, "response": response, "error": error, "acked": acked}
        return _compact_mcp_job_response(job_id, job=job, result=result, acked=acked, verbose=want_verbose, legacy_response=legacy)

    if job:
        legacy = {"job_id": job_id, "status": job.get("status", "running"), "agent_id": job.get("agent_id"), "response": job.get("result"), "error": job.get("error"), "acked": False}
        return _compact_mcp_job_response(job_id, job=job, result=None, acked=False, verbose=want_verbose, legacy_response=legacy)

    legacy = {"job_id": job_id, "status": "running_or_unknown", "agent_id": None, "response": None, "error": None, "acked": False}
    if want_verbose:
        return legacy
    return {"status": "running_or_unknown", "acked": False}


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
async def oauth_register(request: Request):
    # RFC 7591 dynamic client registration. Echo the client's submitted metadata
    # (redirect_uris especially) and include client_id_issued_at — strict OAuth
    # clients (e.g. Codex's rmcp client) reject a response missing these.
    try:
        body = await request.json()
    except Exception:
        body = {}
    if not isinstance(body, dict):
        body = {}
    redirect_uris = body.get("redirect_uris") or []
    resp = {
        "client_id": "chatgpt-dynamic",
        "client_id_issued_at": int(time.time()),
        "token_endpoint_auth_method": "none",
        "grant_types": body.get("grant_types") or ["authorization_code"],
        "response_types": body.get("response_types") or ["code"],
        "redirect_uris": redirect_uris,
    }
    if body.get("client_name"):
        resp["client_name"] = body["client_name"]
    if body.get("scope"):
        resp["scope"] = body["scope"]
    return resp


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
    # Normalize trailing slash: the stored resource (authorize step) was rstrip'd,
    # but a client (e.g. Claude Code) may send resource with a trailing slash in
    # the token request. Without rstrip here the comparison spuriously mismatches.
    resource = (params.get("resource") or (data or {}).get("resource") or MCP_RESOURCE).rstrip("/")
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
            "description": "FIRST STEP. List MCP agents and statuses; choose explicit agent_id for later calls.",
            "inputSchema": {"type": "object", "properties": {"statuses": {"type": "array", "items": {"type": "string", "enum": ["online", "active", "offline", "stale", "all"]}, "default": ["online", "offline"], "description": "Real MCP statuses to include. active is accepted as alias for online."}, "purge_stale": {"type": "boolean", "default": False, "description": "Drop stale registry entries before listing."}}, "additionalProperties": False},
            "outputSchema": {"type": "object", "properties": {"agents": {"type": "array", "items": {"type": "object", "additionalProperties": True}}}, "required": ["agents"], "additionalProperties": True},
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
                "properties": {"target": {"type": "string", "description": "Explicit agent id from list_mcp_agents. There is no default target."}, "retry_policy": {"type": "string", "enum": ["none", "offline_queue", "at_least_once"], "default": MCP_RELAY_DEFAULT_RETRY_POLICY}},
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
            "description": "THIRD STEP. Call a tool on an explicit agent_id; use background=true for long jobs.",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "target": {"type": "string", "description": "Explicit agent id from list_mcp_agents. There is no default target."},
                    "tool_name": {"type": "string", "description": "Tool name returned by list_mcp_tools for the same target."},
                    "arguments": {"type": "object", "additionalProperties": True},
                    "background": {"type": "boolean", "default": False},
                    "timeout": {"type": ["integer", "null"]},
                    "retry_policy": {"type": "string", "enum": ["none", "offline_queue", "at_least_once"], "default": MCP_RELAY_DEFAULT_RETRY_POLICY},
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
            "description": "Poll/read a background MCP job result; ack=true clears terminal result.",
            "inputSchema": {"type": "object", "properties": {"job_id": {"type": "string"}, "ack": {"type": "boolean", "default": False}, "verbose": {"type": "boolean", "default": False}, "include_raw": {"type": "boolean", "default": False}}, "required": ["job_id"], "additionalProperties": False},
            "outputSchema": {"type": "object", "properties": {"job_id": {"type": "string"}, "status": {"type": "string"}, "response": {"type": ["object", "null"], "additionalProperties": True}, "error": {"type": ["object", "string", "null"]}}, "required": ["job_id", "status"], "additionalProperties": True},
            "annotations": {"readOnlyHint": True},
            "securitySchemes": [{"type": "oauth2", "scopes": ["gptadmin.read"]}],
            "_meta": base_meta,
        },
    ]


async def _apps_sdk_call_tool(name: str, args: Dict[str, Any]) -> Dict[str, Any]:
    if name == "list_mcp_agents":
        return mcp_relay_agents_list(statuses=args.get("statuses"), purge_stale=bool(args.get("purge_stale", False)))
    if name == "list_mcp_tools":
        target = args.get("target")
        if not isinstance(target, str) or not target:
            raise HTTPException(400, "Explicit MCP target is required. Call list_mcp_agents first and pass one returned agent_id.")
        return await mcp_relay_tools(McpRelayToolsReq(target=target, retry_policy=str(args.get("retry_policy") or MCP_RELAY_DEFAULT_RETRY_POLICY)))
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
                retry_policy=str(args.get("retry_policy") or MCP_RELAY_DEFAULT_RETRY_POLICY),
            )
        )
    if name == "get_mcp_job":
        return mcp_relay_job_status(str(args.get("job_id") or ""), ack=bool(args.get("ack", False)), verbose=bool(args.get("verbose", False)), include_raw=bool(args.get("include_raw", False)))
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
            tool_result = await _apps_sdk_call_tool(tool_name, args)
            # MCP tools/call results must carry a `content` array; clients (e.g.
            # Claude Code) render that, not a bare dict — without it the call
            # succeeds end-to-end but shows empty. Wrap the tool's dict as JSON
            # text content and keep it as structuredContent too.
            if isinstance(tool_result, dict) and isinstance(tool_result.get("content"), list):
                result = tool_result
            else:
                text = json.dumps(tool_result, ensure_ascii=False, indent=2, default=str)
                result = {
                    "content": [{"type": "text", "text": text}],
                    "structuredContent": tool_result if isinstance(tool_result, dict) else {"result": tool_result},
                    "isError": bool(isinstance(tool_result, dict) and tool_result.get("status") in {"failed", "offline", "expired", "cancelled"}),
                }
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


@app.post("/tasks/{srv}/{tid}/edit", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def edit_task_endpoint(srv: str, tid: str, edit: TaskEdit):
    return _edit_task(srv, tid, edit.model_dump(exclude_unset=True))


@app.get("/tasks/{srv}", dependencies=[Depends(check_ctl_token), Depends(ensure_license)])
def list_tasks(srv: str, status: Optional[str] = Query(None), limit: Optional[int] = Query(50), sort_by: Optional[str] = Query("updated_at"), order: Optional[str] = Query("desc"), include_result: bool = Query(True), include_history: bool = Query(True)):
    return _legacy_list_tasks(srv, status=status, limit=limit, sort_by=sort_by, order=order, include_result=include_result, include_history=include_history)


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

_SHELL_TOOLS: List[Dict[str, Any]] = _shell_tools_list().get("tools", [])


async def _bridge_fetch_tools(agent_id: str) -> List[Dict[str, Any]]:
    try:
        if agent_id == VIRTUAL_HUB_AGENT_ID:
            return _hub_tools_list().get("tools", [])
        if _is_virtual_shell_agent(agent_id):
            return _SHELL_TOOLS
        async def _fetch():
            job_id = _mcp_relay_enqueue(agent_id, "tools/list", {}, retry_policy="none")
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
                validated, "tools/call", {"name": tool, "arguments": args}, retry_policy=str(body.get("retry_policy") or "none"))
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
        host = os.getenv("HUB_BIND") or os.getenv("HUB_HOST") or "0.0.0.0"
        log.info("starting hub on %s:%s (dead_s=%s, log_level=%s)", host, port, DEAD_S, LOG_LEVEL)
        kwargs["host"] = host
        kwargs["port"] = port

    uvicorn.run(app, **kwargs)
