import os, subprocess, logging, asyncio, psutil, socket, time, shutil, platform

log = logging.getLogger("shellmcp_win")

TMO_DEF = int(os.getenv("EXEC_TIMEOUT", "300"))
LOG_MAX = int(os.getenv("LOG_LIMIT_B", str(10 * 1024 * 1024)))

def _truncate(s):
    if s is None:
        s = ""
    if isinstance(s, bytes):
        s = s.decode(errors="replace")
    elif not isinstance(s, str):
        s = str(s)
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s


def run(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running command (Windows): {cmd} (timeout={timeout}, cwd={cwd})")
    try:
        res = subprocess.run(
            ["powershell", "-Command", cmd],
            cwd=cwd,
            text=False,
            timeout=timeout or TMO_DEF,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
        )
    except subprocess.TimeoutExpired as e:
        return {"error": f"timeout {e.timeout}s", "stdout": _truncate(e.stdout or ""), "stderr": _truncate(e.stderr or "")}
    except Exception:
        log.exception(f"Exception during command: {cmd}")
        raise

    return {
        "returncode": res.returncode,
        "stdout": _truncate(res.stdout),
        "stderr": _truncate(res.stderr),
    }


async def run_stream(cmd: str, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running streaming command (Windows): {cmd} (cwd={cwd})")
    proc = await asyncio.create_subprocess_exec(
        "powershell",
        "-Command",
        cmd,
        cwd=cwd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
        env=env,
    )

    async def generator():
        assert proc.stdout
        async for chunk in proc.stdout:
            yield chunk
        await proc.wait()

    return generator


def _pretty_platform() -> str:
    return f"{platform.system()} {platform.release()} {platform.version()} arch={platform.machine()}"


def info():
    return {
        "host": socket.gethostname(),
        "platform": _pretty_platform(),
        "cores": psutil.cpu_count(),
        "mem_mb": round(psutil.virtual_memory().total / 2**20),
        "uptime_s": round(time.time() - psutil.boot_time()),
    }


def health():
    du = shutil.disk_usage(os.environ.get("SystemDrive", "C:") + "\\")

    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
    except Exception:
        ip = "unavailable"

    vm = psutil.virtual_memory()
    swap = psutil.swap_memory()

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
        "failed_services": [],
        "last_apt_update": None,
        "cpu_temperature": cpu_temp,
        "ip_address": ip,
    }
