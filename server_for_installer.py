# install_proxy.py
"""
Simple FastAPI service that serves installation scripts for the hub and agents.
Clients can download the scripts from this service running on port 22554.

Endpoints:
  * /install.sh        – hub installer
  * /install_shellmcp.sh  – Linux agent installer
  * /install_win.ps1   – Windows agent installer
  * /api.json          – OpenAPI schema
"""
import logging
import hashlib
from pathlib import Path
from fastapi import FastAPI, Response, HTTPException
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles

log = logging.getLogger("hub")
logging.basicConfig(level=logging.INFO)

BASE_DIR = Path(__file__).resolve().parent
REPO_DIR = BASE_DIR
DEPLOY_DIR = REPO_DIR / "deploy"
PUBLIC_DIR = REPO_DIR / "public"
BUILD_DIR = REPO_DIR / "build"
WEBSITE_DIR = REPO_DIR / "website"

app = FastAPI(title="hub-install-proxy", version="1.0")




def _sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def _artifact_meta(path: Path, route: str) -> dict:
    if not path.exists():
        raise HTTPException(404, "artifact not found")
    return {
        "name": path.name,
        "size": path.stat().st_size,
        "sha256": _sha256_file(path),
        "url": route,
    }


def _artifact_manifest() -> dict:
    candidates = {
        "gptadmin.py": (BUILD_DIR / "cli" / "gptadmin.py", "/gptadmin.py"),
        "shellmcp.py": (REPO_DIR / "client" / "shellmcp.py", "/shellmcp.py"),
        "shellmcp_pure.py": (REPO_DIR / "client" / "shellmcp_pure.py", "/shellmcp_pure.py"),
        "gptadmin.tar.gz": (BUILD_DIR / "gptadmin.tar.gz", "/gptadmin.tar.gz"),
        "gptadmin-hub.tar.gz": (BUILD_DIR / "gptadmin-hub.tar.gz", "/gptadmin-hub.tar.gz"),
        "gptadmin-shellmcp.tar.gz": (BUILD_DIR / "gptadmin-shellmcp.tar.gz", "/gptadmin-shellmcp.tar.gz"),
        "gptadmin-win.zip": (PUBLIC_DIR / "gptadmin-win.zip", "/gptadmin-win.zip"),
    }
    artifacts = {}
    for name, (path, route) in candidates.items():
        if path.exists():
            artifacts[name] = _artifact_meta(path, route)
    return {"artifacts": artifacts}


def load_script(path: Path) -> str:
    """Return contents of a local script."""
    if not path.exists():
        raise HTTPException(404, "script not found")
    log.info("serve %s", path)
    return path.read_text()


@app.get("/install.sh")
async def get_install_sh():
    content = load_script(DEPLOY_DIR / "install.sh")
    return Response(content, media_type="text/plain")


@app.get("/install_shellmcp.sh")
async def get_install_shellmcp_sh():
    content = load_script(DEPLOY_DIR / "install_shellmcp.sh")
    return Response(content, media_type="text/plain")


@app.get("/install_win.ps1")
async def get_install_win_ps1():
    content = load_script(DEPLOY_DIR / "install_win.ps1")
    return Response(content, media_type="text/plain")

@app.get("/api.json")
async def get_openapi_json():
    content = load_script(PUBLIC_DIR / "openapi.yaml")
    return Response(content, media_type="application/json")

def _bin(path: Path, filename: str, media_type: str):
    if not path.exists():
        raise HTTPException(404, 'artifact not found')
    log.info('serve %s', path)
    return FileResponse(path, media_type=media_type, filename=filename)


@app.get('/gptadmin.tar.gz')
async def get_all():
    return _bin(BUILD_DIR / "gptadmin.tar.gz", "gptadmin.tar.gz", "application/gzip")


@app.get('/gptadmin-hub.tar.gz')
async def get_hub():
    return _bin(BUILD_DIR / "gptadmin-hub.tar.gz", "gptadmin-hub.tar.gz", "application/gzip")


@app.get('/gptadmin-shellmcp.tar.gz')
async def get_shellmcp():
    return _bin(BUILD_DIR / "gptadmin-shellmcp.tar.gz", "gptadmin-shellmcp.tar.gz", "application/gzip")


@app.get('/gptadmin-win.zip')
async def get_shellmcp_win():
    return _bin(PUBLIC_DIR / "gptadmin-win.zip", "gptadmin-win.zip", "application/zip")


@app.get('/gptadmin.py')
async def get_cli_py():
    return _bin(BUILD_DIR / "cli" / "gptadmin.py", "gptadmin.py", "text/x-python")


@app.get('/shellmcp_pure.py')
async def get_shellmcp_pure_py():
    return _bin(REPO_DIR / "client" / "shellmcp_pure.py", "shellmcp_pure.py", "text/x-python")


@app.get('/shellmcp.py')
async def get_shellmcp_py():
    return _bin(REPO_DIR / "client" / "shellmcp.py", "shellmcp.py", "text/x-python")


@app.get('/shellmcp.py.json')
async def get_shellmcp_py_meta():
    return _artifact_meta(REPO_DIR / "client" / "shellmcp.py", "/shellmcp.py")


@app.get('/shellmcp_pure.py.json')
async def get_shellmcp_pure_py_meta():
    return _artifact_meta(REPO_DIR / "client" / "shellmcp_pure.py", "/shellmcp_pure.py")


@app.get('/manifest.json')
async def get_manifest_json():
    return _artifact_manifest()


# --- MCP Bridge: userscript + help page ---

def _read_mcp_bridge_userscript() -> str:
    path = PUBLIC_DIR / "mcp-bridge.user.js"
    if not path.exists():
        raise HTTPException(404, "userscript not found")
    return path.read_text(encoding="utf-8")


def _mcp_bridge_userscript_response() -> Response:
    # Tampermonkey and iOS Userscripts recognize installable userscripts best
    # when the URL ends with .user.js and Content-Disposition stays inline.
    return Response(
        _read_mcp_bridge_userscript(),
        media_type="text/javascript",
        headers={
            "Content-Disposition": "inline; filename=mcp-bridge.user.js",
            "X-Content-Type-Options": "nosniff",
        },
    )


@app.api_route("/mcp-bridge.user.js", methods=["GET", "HEAD"])
async def get_mcp_bridge_userscript():
    return _mcp_bridge_userscript_response()


@app.api_route("/userscript.js", methods=["GET", "HEAD"])
async def get_userscript():
    # Backward-compatible URL; kept installable with the same headers.
    return _mcp_bridge_userscript_response()


@app.api_route("/mcp-help", methods=["GET", "HEAD"])
async def mcp_help_page():
    html_path = WEBSITE_DIR / "mcp-help.html"
    if html_path.exists():
        return Response(html_path.read_text(encoding="utf-8"), media_type="text/html")
    return Response("<h1>MCP Bridge</h1><a href=/mcp-bridge.user.js>Install</a>", media_type="text/html")

app.mount("/", StaticFiles(directory=WEBSITE_DIR, html=True), name="website")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("server_for_installer:app", host="0.0.0.0", port=22554)
