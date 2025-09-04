# install_proxy.py
"""
Simple FastAPI service that serves installation scripts for the hub and agents.
Clients can download the scripts from this service running on port 22554.

Endpoints:
  * /install.sh        – hub installer
  * /install_rootd.sh  – Linux agent installer
  * /install_win.ps1   – Windows agent installer
"""
import logging
from pathlib import Path
from fastapi import FastAPI, Response, HTTPException

log = logging.getLogger("hub")
logging.basicConfig(level=logging.INFO)

BASE_DIR = Path(__file__).resolve().parent

app = FastAPI(title="hub-install-proxy", version="1.0")


def load_script(name: str) -> str:
    """Return contents of a local script."""
    path = BASE_DIR / name
    if not path.exists():
        raise HTTPException(404, "script not found")
    log.info("serve %s", path)
    return path.read_text()


@app.get("/install.sh")
async def get_install_sh():
    content = load_script("install.sh")
    return Response(content, media_type="text/plain")


@app.get("/install_rootd.sh")
async def get_install_rootd_sh():
    content = load_script("install_rootd.sh")
    return Response(content, media_type="text/plain")


@app.get("/install_win.ps1")
async def get_install_win_ps1():
    content = load_script("install_win.ps1")
    return Response(content, media_type="text/plain")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("install_proxy:app", host="0.0.0.0", port=22554)
