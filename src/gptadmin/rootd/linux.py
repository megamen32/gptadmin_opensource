import os
import subprocess
import logging
import asyncio
import socket
import time
import shutil
import platform
from pathlib import Path

try:
    import psutil
    HAS_PSUTIL = True
except ImportError:
    HAS_PSUTIL = False

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


# --- Fallback functions for when psutil is not installed ---
def _fallback_cpu_count():
    return os.cpu_count() or 1

def _fallback_uptime_s():
    try:
        with open('/proc/uptime', 'r') as f:
            return round(float(f.readline().split()[0]))
    except Exception:
        return 0

def _fallback_boot_time():
    try:
        return time.time() - _fallback_uptime_s()
    except Exception:
        return time.time()

def _fallback_virtual_memory():
    meminfo = {}
    try:
        with open('/proc/meminfo', 'r') as f:
            for line in f:
                parts = line.split()
                if len(parts) >= 2:
                    key = parts[0].rstrip(':')
                    val = int(parts[1]) * 1024  # Convert kB to bytes
                    meminfo[key] = val
    except Exception:
        pass
    
    total = meminfo.get('MemTotal', 0)
    free = meminfo.get('MemFree', 0)
    available = meminfo.get('MemAvailable', free)
    buffers = meminfo.get('Buffers', 0)
    cached = meminfo.get('Cached', 0)
    used = total - free - buffers - cached
    
    class MemInfo:
        def __init__(self, t, a, u, f):
            self.total = t
            self.available = a
            self.used = u
            self.free = f
            self.percent = round((t - a) / t * 100, 1) if t > 0 else 0.0
            
    return MemInfo(total, available, used, free)

def _fallback_swap_memory():
    meminfo = {}
    try:
        with open('/proc/meminfo', 'r') as f:
            for line in f:
                parts = line.split()
                if len(parts) >= 2:
                    key = parts[0].rstrip(':')
                    val = int(parts[1]) * 1024
                    meminfo[key] = val
    except Exception:
        pass
    
    total = meminfo.get('SwapTotal', 0)
    free = meminfo.get('SwapFree', 0)
    used = total - free
    
    class SwapInfo:
        def __init__(self, t, u, f):
            self.total = t
            self.used = u
            self.free = f
            self.percent = round(u / t * 100, 1) if t > 0 else 0.0
            
    return SwapInfo(total, used, free)

def _fallback_cpu_percent(interval=1):
    def read_stat():
        with open('/proc/stat', 'r') as f:
            line = f.readline()
        parts = line.split()
        # user, nice, system, idle, iowait, irq, softirq, steal
        idle = int(parts[4]) + int(parts[5])
        total = sum(int(x) for x in parts[1:])
        return idle, total

    try:
        idle1, total1 = read_stat()
        time.sleep(interval)
        idle2, total2 = read_stat()
        
        idle_delta = idle2 - idle1
        total_delta = total2 - total1
        
        if total_delta == 0:
            return 0.0
        return round((1.0 - idle_delta / total_delta) * 100, 1)
    except Exception:
        return 0.0

def _fallback_sensors_temperatures():
    temps = {}
    try:
        for zone in Path('/sys/class/thermal').glob('thermal_zone*'):
            try:
                temp_file = zone / 'temp'
                type_file = zone / 'type'
                if temp_file.exists() and type_file.exists():
                    t = int(temp_file.read_text().strip()) / 1000.0
                    label = type_file.read_text().strip()
                    if label not in temps:
                        temps[label] = []
                    
                    class TempEntry:
                        def __init__(self, l, c):
                            self.label = l
                            self.current = c
                    
                    temps[label].append(TempEntry(label, t))
            except Exception:
                continue
    except Exception:
        pass
    return temps


def info():
    if HAS_PSUTIL:
        cores = psutil.cpu_count()
        mem_mb = round(psutil.virtual_memory().total / 2**20)
        uptime_s = round(time.time() - psutil.boot_time())
    else:
        cores = _fallback_cpu_count()
        mem_mb = round(_fallback_virtual_memory().total / 2**20)
        uptime_s = _fallback_uptime_s()

    return {
        "host": socket.gethostname(),
        "platform": platform.platform(),
        "cores": cores,
        "mem_mb": mem_mb,
        "uptime_s": uptime_s,
    }


def health():
    du = shutil.disk_usage("/")

    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
    except Exception:
        ip = "unavailable"

    if HAS_PSUTIL:
        vm = psutil.virtual_memory()
        swap = psutil.swap_memory()
        cpu_pct = psutil.cpu_percent(interval=1)
        uptime_s = round(time.time() - psutil.boot_time())
        temp = psutil.sensors_temperatures()
    else:
        vm = _fallback_virtual_memory()
        swap = _fallback_swap_memory()
        cpu_pct = _fallback_cpu_percent(interval=1)
        uptime_s = _fallback_uptime_s()
        temp = _fallback_sensors_temperatures()

    try:
        load = os.getloadavg()
    except Exception:
        load = []

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
        "uptime_s": uptime_s,
        "load_avg": load,
        "cpu_usage_pct": cpu_pct,
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
