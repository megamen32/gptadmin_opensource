#!/usr/bin/env python3
import hashlib
import json
import os
import re
import secrets
import shutil
import threading
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote
from typing import Any, Dict

PUBLIC_ROOT = Path(os.environ.get("GPTADMIN_FILESHARE_PUBLIC_ROOT", "/var/www/gptadmin-downloads"))
STATE_DIR = Path(os.environ.get("GPTADMIN_FILESHARE_STATE_DIR", "/var/lib/gptadmin-file-share"))
BASE_URL = os.environ.get("GPTADMIN_FILESHARE_BASE_URL", "https://your-subdomain.t.gptadmin.bezrabotnyi.com/_files")
MAX_SIZE_MB_DEFAULT = int(os.environ.get("GPTADMIN_FILESHARE_MAX_SIZE_MB", "1024"))
HTTP_HOST = os.environ.get("GPTADMIN_FILESHARE_HTTP_HOST", "127.0.0.1")
HTTP_PORT = int(os.environ.get("GPTADMIN_FILESHARE_HTTP_PORT", "18082"))
# Background cleanup interval for expired links. Set to <=0 to disable the
# background sweeper (expired files then only removed when a client calls the
# cleanup_expired_public_file_links tool).
CLEANUP_INTERVAL = float(os.environ.get("GPTADMIN_FILESHARE_CLEANUP_INTERVAL", "3600"))
INDEX_FILE = STATE_DIR / "index.json"
SAFE_NAME_RE = re.compile(r"[^A-Za-z0-9._-]+")
# Serializes load -> modify -> save of INDEX_FILE. ThreadingHTTPServer serves
# downloads concurrently with tool calls (create/revoke/cleanup) that mutate the
# index; without this lock two concurrent mutations would lose each other's update.
INDEX_LOCK = threading.RLock()


class FileHandler(BaseHTTPRequestHandler):
    server_version = "GPTAdminFileShare/1.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"fileshare_http {self.address_string()} {fmt % args}", file=sys.stderr, flush=True)

    def do_HEAD(self) -> None:
        self._serve(send_body=False)

    def do_GET(self) -> None:
        self._serve(send_body=True)

    def _serve(self, send_body: bool) -> None:
        rel = unquote(self.path.split("?", 1)[0]).lstrip("/")
        parts = rel.split("/", 1)
        if len(parts) != 2:
            self.send_error(404)
            return
        token = re.sub(r"[^A-Za-z0-9_-]", "", parts[0])
        filename = sanitize_filename(parts[1])
        target = (PUBLIC_ROOT / token / filename).resolve()
        root = PUBLIC_ROOT.resolve()
        if root not in target.parents or not target.is_file():
            self.send_error(404)
            return
        data = load_index().get("links", {}).get(token)
        if not data or data.get("filename") != filename:
            self.send_error(404)
            return
        exp = data.get("expires_at_epoch")
        if exp and exp < time.time():
            self.send_error(410)
            return
        stat = target.stat()
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(stat.st_size))
        self.send_header("Content-Disposition", f'attachment; filename="{filename}"')
        self.send_header("Cache-Control", "private, no-store")
        self.end_headers()
        if send_body:
            with target.open("rb") as f:
                shutil.copyfileobj(f, self.wfile, length=1024 * 1024)


def start_http_server() -> "ThreadingHTTPServer | None":
    # Bind is best-effort: the MCP tool surface (create/list/revoke) must keep
    # working even if the HTTP download server cannot bind (port in use, etc.).
    # Downloads simply won't resolve until the bind issue is fixed.
    try:
        server = ThreadingHTTPServer((HTTP_HOST, HTTP_PORT), FileHandler)
    except OSError as e:
        print(f"fileshare_http NOT listening: cannot bind {HTTP_HOST}:{HTTP_PORT}: {e}", file=sys.stderr, flush=True)
        return None
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, name="fileshare-http", daemon=True)
    thread.start()
    print(f"fileshare_http listening http://{HTTP_HOST}:{HTTP_PORT}", file=sys.stderr, flush=True)
    return server


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def load_index() -> Dict[str, Any]:
    try:
        return json.loads(INDEX_FILE.read_text())
    except Exception:
        return {"links": {}}


def save_index(data: Dict[str, Any]) -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    tmp = INDEX_FILE.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2, sort_keys=True))
    os.replace(tmp, INDEX_FILE)


def sanitize_filename(name: str) -> str:
    name = os.path.basename(name.strip() or "download.bin")
    name = SAFE_NAME_RE.sub("_", name).strip("._")
    return name or "download.bin"


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def create_public_file_link(args: Dict[str, Any]) -> Dict[str, Any]:
    src_s = args.get("path") or args.get("file")
    if not src_s or not isinstance(src_s, str):
        raise ValueError("path is required")
    src = Path(src_s).expanduser().resolve()
    if not src.exists():
        raise FileNotFoundError(f"file not found: {src}")
    if not src.is_file():
        raise ValueError(f"path is not a regular file: {src}")
    max_size_mb = int(args.get("max_size_mb") or MAX_SIZE_MB_DEFAULT)
    size = src.stat().st_size
    if size > max_size_mb * 1024 * 1024:
        raise ValueError(f"file too large: {size} bytes > {max_size_mb} MB")
    token = secrets.token_urlsafe(18)
    filename = sanitize_filename(str(args.get("name") or src.name))
    target_dir = PUBLIC_ROOT / token
    target_dir.mkdir(parents=True, exist_ok=False)
    dst = target_dir / filename
    shutil.copy2(src, dst)
    os.chmod(dst, 0o644)
    digest = sha256_file(dst)
    ttl_days = args.get("ttl_days", 14)
    try:
        ttl_days = int(ttl_days)
    except Exception:
        ttl_days = 14
    expires_at = None if ttl_days <= 0 else time.time() + ttl_days * 86400
    url = f"{BASE_URL.rstrip('/')}/{token}/{filename}"
    with INDEX_LOCK:
        data = load_index()
        data.setdefault("links", {})[token] = {
            "token": token,
            "url": url,
            "filename": filename,
            "source_path": str(src),
            "public_path": str(dst),
            "size": size,
            "sha256": digest,
            "created_at": now_iso(),
            "expires_at_epoch": expires_at,
            "expires_at": None if expires_at is None else datetime.fromtimestamp(expires_at, timezone.utc).isoformat(),
            "note": str(args.get("note") or ""),
        }
        save_index(data)
    return data["links"][token]


def revoke_public_file_link(args: Dict[str, Any]) -> Dict[str, Any]:
    token = str(args.get("token") or "")
    url = str(args.get("url") or "")
    if not token and url:
        # A public link ends with /<token>/<filename>. Extract the second-to-last
        # path segment regardless of the URL prefix (/_files/, /_services/.../files/, etc.).
        path = url.split("?", 1)[0].rstrip("/")
        segs = [s for s in path.split("/") if s]
        if len(segs) >= 2:
            token = segs[-2]
    if not token:
        raise ValueError("token or url is required")
    token = re.sub(r"[^A-Za-z0-9_-]", "", token)
    with INDEX_LOCK:
        data = load_index()
        meta = data.get("links", {}).pop(token, None)
        removed_files = []
        target_dir = PUBLIC_ROOT / token
        if target_dir.exists() and target_dir.is_dir():
            for p in target_dir.iterdir():
                if p.is_file():
                    removed_files.append(str(p))
            shutil.rmtree(target_dir)
        save_index(data)
    return {"token": token, "revoked": bool(meta or removed_files), "removed_files": removed_files, "meta": meta}


def list_public_file_links(args: Dict[str, Any]) -> Dict[str, Any]:
    data = load_index()
    links = list(data.get("links", {}).values())
    links.sort(key=lambda x: x.get("created_at", ""), reverse=True)
    limit = int(args.get("limit") or 50)
    return {"count": len(links), "links": links[:limit]}


def cleanup_expired_public_file_links(args: Dict[str, Any]) -> Dict[str, Any]:
    with INDEX_LOCK:
        data = load_index(); links = data.get("links", {})
        now = time.time(); removed = []
        for token, meta in list(links.items()):
            exp = meta.get("expires_at_epoch")
            if exp and exp < now:
                target_dir = PUBLIC_ROOT / token
                if target_dir.exists() and target_dir.is_dir():
                    shutil.rmtree(target_dir)
                removed.append(meta)
                links.pop(token, None)
        save_index(data)
    return {"removed_count": len(removed), "removed": removed}


def cleanup_expired_loop(interval: float) -> None:
    """Daemon thread: periodically delete links whose TTL has elapsed so public
    URLs actually disappear on time even if no client calls the cleanup tool."""
    while True:
        time.sleep(interval)
        try:
            cleanup_expired_public_file_links({})
        except Exception as e:
            print(f"fileshare cleanup loop error: {e}", file=sys.stderr, flush=True)

TOOLS = {
    "create_public_file_link": {
        "description": "Copy a readable local file into a tokenized public download directory and return a public HTTPS URL. Does not expose the original path directly.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Absolute or ~ path to a regular local file readable by this MCP service."},
                "name": {"type": ["string", "null"], "description": "Optional public download filename."},
                "ttl_days": {"type": ["integer", "null"], "default": 14, "description": "Metadata expiry in days; <=0 means no expiry. Use cleanup_expired_public_file_links to remove expired files."},
                "max_size_mb": {"type": ["integer", "null"], "default": MAX_SIZE_MB_DEFAULT},
                "note": {"type": ["string", "null"]}
            },
            "required": ["path"],
            "additionalProperties": False,
        },
        "handler": create_public_file_link,
    },
    "revoke_public_file_link": {
        "description": "Delete a previously created public file link by token or URL.",
        "inputSchema": {"type": "object", "properties": {"token": {"type": ["string", "null"]}, "url": {"type": ["string", "null"]}}, "additionalProperties": False},
        "handler": revoke_public_file_link,
    },
    "list_public_file_links": {
        "description": "List recently created public file links.",
        "inputSchema": {"type": "object", "properties": {"limit": {"type": ["integer", "null"], "default": 50}}, "additionalProperties": False},
        "handler": list_public_file_links,
    },
    "cleanup_expired_public_file_links": {
        "description": "Remove public files whose metadata expiry time has passed.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
        "handler": cleanup_expired_public_file_links,
    },
}


def reply(req_id: Any, result: Any = None, error: Exception = None) -> None:
    if req_id is None:
        return
    if error is not None:
        payload = {"jsonrpc": "2.0", "id": req_id, "error": {"code": -32000, "message": str(error), "data": error.__class__.__name__}}
    else:
        payload = {"jsonrpc": "2.0", "id": req_id, "result": result}
    print(json.dumps(payload, ensure_ascii=False), flush=True)


def handle(req: Dict[str, Any]) -> None:
    method = req.get("method")
    req_id = req.get("id")
    try:
        if method == "initialize":
            reply(req_id, {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}}, "serverInfo": {"name": "gptadmin-file-share", "version": "1.0.0"}})
        elif method == "tools/list":
            reply(req_id, {"tools": [{"name": n, "description": t["description"], "inputSchema": t["inputSchema"]} for n, t in TOOLS.items()]})
        elif method == "tools/call":
            params = req.get("params") or {}
            name = params.get("name")
            args = params.get("arguments") or {}
            if name not in TOOLS:
                raise ValueError(f"unknown tool: {name}")
            result = TOOLS[name]["handler"](args)
            text = json.dumps(result, ensure_ascii=False, indent=2)
            reply(req_id, {"content": [{"type": "text", "text": text}], "structuredContent": result})
        elif method and method.startswith("notifications/"):
            return
        else:
            reply(req_id, {})
    except Exception as e:
        print(f"fileshare_mcp error: {e}", file=sys.stderr, flush=True)
        reply(req_id, error=e)


def main() -> None:
    PUBLIC_ROOT.mkdir(parents=True, exist_ok=True)
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    start_http_server()
    if CLEANUP_INTERVAL > 0:
        threading.Thread(target=cleanup_expired_loop, args=(CLEANUP_INTERVAL,), name="fileshare-cleanup", daemon=True).start()
        print(f"fileshare_cleanup interval={int(CLEANUP_INTERVAL)}s", file=sys.stderr, flush=True)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            handle(json.loads(line))
        except Exception as e:
            print(f"bad request: {e}", file=sys.stderr, flush=True)

if __name__ == "__main__":
    main()
