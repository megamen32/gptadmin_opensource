# install_proxy.py
"""
Simple FastAPI service that serves installation scripts for the hub and agents.
Clients can download the scripts from this service running on port 22554.

Endpoints:
  * /install.sh        – hub installer
  * /install_rootd.sh  – Linux agent installer
  * /install_win.ps1   – Windows agent installer
  * /api.json          – OpenAPI schema
"""
import logging
from pathlib import Path
from fastapi import FastAPI, Response, HTTPException
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles

log = logging.getLogger("hub")
logging.basicConfig(level=logging.INFO)

BASE_DIR = Path(__file__).resolve().parent
REPO_DIR = BASE_DIR.parent
DEPLOY_DIR = REPO_DIR / "deploy"
PUBLIC_DIR = REPO_DIR / "public"
BUILD_DIR = REPO_DIR / "build"
WEBSITE_DIR = REPO_DIR / "website"

app = FastAPI(title="hub-install-proxy", version="1.0")


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


@app.get("/install_rootd.sh")
async def get_install_rootd_sh():
    content = load_script(DEPLOY_DIR / "install_rootd.sh")
    return Response(content, media_type="text/plain")


@app.get("/install_win.ps1")
async def get_install_win_ps1():
    content = load_script(DEPLOY_DIR / "install_win.ps1")
    return Response(content, media_type="text/plain")

@app.get("/api.json")
async def get_openapi_json():
    content = load_script(PUBLIC_DIR / "openapi.json")
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


@app.get('/gptadmin-rootd.tar.gz')
async def get_rootd():
    return _bin(BUILD_DIR / "gptadmin-rootd.tar.gz", "gptadmin-rootd.tar.gz", "application/gzip")


@app.get('/gptadmin.py')
async def get_cli_py():
    return _bin(BUILD_DIR / "cli" / "gptadmin.py", "gptadmin.py", "text/x-python")

app.mount("/", StaticFiles(directory=WEBSITE_DIR, html=True), name="website")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("server_for_installer:app", host="0.0.0.0", port=22554)
