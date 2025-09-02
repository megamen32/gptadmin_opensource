import os
import subprocess
import logging
import asyncio
import psutil
import socket
import time
import shutil
from pathlib import Path

log = logging.getLogger("rootd_linux")

TMO_DEF = int(os.getenv("EXEC_TIMEOUT", "300"))
LOG_MAX = int(os.getenv("LOG_LIMIT_B", "8192"))


def _truncate(s):
    if isinstance(s, bytes):
        s = s.decode(errors="ignore")
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s


def run(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running command (Linux): {cmd} (timeout={timeout}, cwd={cwd})")
    try:
        res = subprocess.run(
            cmd,
            cwd=cwd,
            text=True,
            timeout=timeout or TMO_DEF,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
            shell=True,
            executable="/bin/bash",
        )
        return {
            "returncode": res.returncode,
            "stdout": _truncate(res.stdout),
            "stderr": _truncate(res.stderr),
        }
    except subprocess.TimeoutExpired as e:
        return {"error": f"timeout {e.timeout}s", "stdout": _truncate(e.stdout or ""), "stderr": _truncate(e.stderr or "")}
    except Exception as e:
        return {"error": str(e)}


async def run_stream(cmd: str, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running streaming command (Linux): {cmd} (cwd={cwd})")
    proc = await asyncio.create_subprocess_shell(
        cmd,
        cwd=cwd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
        env=env,
        executable="/bin/bash",
    )

    async def generator():
        assert proc.stdout
        async for chunk in proc.stdout:
            yield chunk
        await proc.wait()

    return generator


def health():
    du = shutil.disk_usage("/")

    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
    except Exception:
        ip = "unavailable"

    vm = psutil.virtual_memory()
    swap = psutil.swap_memory()

    try:
        load = os.getloadavg()
    except Exception:
        load = []

    temp = psutil.sensors_temperatures()
    cpu_temp = None
    for sensor in temp.values():
        for entry in sensor:
            if "cpu" in entry.label.lower() or "package" in entry.label.lower():
                cpu_temp = entry.current
                break
        if cpu_temp is not None:
            break

    result = subprocess.run(
        ["systemctl", "list-units", "--state=failed", "--no-pager", "--plain", "--no-legend"],
        text=True,
        stdout=subprocess.PIPE,
    )
    failed_services = [
        line.split()[0]
        for line in result.stdout.splitlines()
        if line.strip() and ".service" in line.split()[0]
    ]

    apt_time = None
    stamp = Path("/var/lib/apt/periodic/update-success-stamp")
    if stamp.exists():
        apt_time = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(stamp.stat().st_mtime))

    return {
        "uptime_s": round(time.time() - psutil.boot_time()),
        "load_avg": load,
        "cpu_usage_pct": psutil.cpu_percent(interval=1),
        "memory": {
            "total": round(vm.total / 2**20),
            "available": round(vm.available / 2**20),
            "used": round(vm.used / 2**20),
            "free": round(vm.free / 2**20),
        },
        "swap": {
            "total": round(swap.total / 2**20),
            "used": round(swap.used / 2**20),
            "free": round(swap.free / 2**20),
        },
        "disk": {
            "total": round(du.total / 2**30, 2),
            "used": round(du.used / 2**30, 2),
            "free": round(du.free / 2**30, 2),
        },
        "failed_services": failed_services,
        "last_apt_update": apt_time,
        "cpu_temperature": cpu_temp,
        "ip_address": ip,
    }

