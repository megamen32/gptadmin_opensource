import os
import time
import logging
import asyncio
import shlex
import json
import threading
from typing import Dict, Optional

import paramiko

log = logging.getLogger("rootd_ssh")

TMO_DEF = int(os.getenv("EXEC_TIMEOUT", "300"))
LOG_MAX = int(os.getenv("LOG_LIMIT_B", "8192"))

SSH_HOST = os.getenv("SSH_HOST", "localhost")
SSH_PORT = int(os.getenv("SSH_PORT", "22"))
SSH_USER = os.getenv("SSH_USER", "root")
SSH_PASSWORD = os.getenv("SSH_PASSWORD")
SSH_KEY = os.getenv("SSH_KEY") or os.getenv("SSH_KEY_PATH")


def _truncate(s: bytes | str) -> str:
    if isinstance(s, bytes):
        s = s.decode(errors="ignore")
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s


def _connect() -> paramiko.SSHClient:
    if not SSH_HOST or not SSH_USER:
        raise RuntimeError("SSH_HOST and SSH_USER must be set")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    kwargs: Dict[str, Optional[str | int]] = {
        "hostname": SSH_HOST,
        "port": SSH_PORT,
        "username": SSH_USER,
    }
    if SSH_KEY:
        kwargs["key_filename"] = SSH_KEY
    else:
        kwargs["password"] = SSH_PASSWORD
    client.connect(timeout=10, auth_timeout=10, banner_timeout=10,
                   allow_agent=False, look_for_keys=False, **kwargs)
    client.get_transport().set_keepalive(30)
    return client


def _compose_cmd(cmd: str, cwd: Optional[str], env: Optional[dict]) -> str:
    if env:
        exports = " ".join(f"{k}={shlex.quote(str(v))}" for k, v in env.items())
        cmd = f"export {exports}; {cmd}"
    if cwd:
        cmd = f"cd {shlex.quote(cwd)} && {cmd}"
    return cmd


def run(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running command (SSH): {cmd} (timeout={timeout}, cwd={cwd})")
    client = _connect()
    try:
        full_cmd = _compose_cmd(cmd, cwd, env)
        channel = client.get_transport().open_session()
        channel.exec_command(full_cmd)
        stdout_chunks: list[bytes] = []
        stderr_chunks: list[bytes] = []
        start = time.time()
        deadline = timeout or TMO_DEF
        while True:
            if channel.recv_ready():
                stdout_chunks.append(channel.recv(4096))
            if channel.recv_stderr_ready():
                stderr_chunks.append(channel.recv_stderr(4096))
            if channel.exit_status_ready():
                break
            if time.time() - start > deadline:
                channel.close()
                return {"returncode": 124,
                        "error": f"timeout {deadline}s",
                        "stdout": _truncate(b"".join(stdout_chunks)),
                        "stderr": _truncate(b"".join(stderr_chunks))}
            time.sleep(0.05)
        rc = channel.recv_exit_status()
        return {
            "returncode": rc,
            "stdout": _truncate(b"".join(stdout_chunks)),
            "stderr": _truncate(b"".join(stderr_chunks)),
        }
    finally:
        client.close()


async def run_stream(cmd: str, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running streaming command (SSH): {cmd} (cwd={cwd})")
    client = _connect()
    full_cmd = _compose_cmd(cmd, cwd, env)
    channel = client.get_transport().open_session()
    channel.exec_command(full_cmd)

    loop = asyncio.get_running_loop()
    queue: asyncio.Queue[bytes] = asyncio.Queue(maxsize=1024)
    done = asyncio.Event()

    def reader():
        try:
            while True:
                if channel.recv_ready():
                    data = channel.recv(1024)
                    loop.call_soon_threadsafe(queue.put_nowait, data)
                if channel.recv_stderr_ready():
                    data = channel.recv_stderr(1024)
                    loop.call_soon_threadsafe(queue.put_nowait, data)
                if channel.exit_status_ready():
                    break
                time.sleep(0.05)
        finally:
            channel.close()
            client.close()
            loop.call_soon_threadsafe(done.set)

    threading.Thread(target=reader, daemon=True).start()

    async def generator():
        while not (done.is_set() and queue.empty()):
            chunk = await queue.get()
            yield chunk

    return generator

info_cache=None
def info():
    global info_cache
    if info_cache:
        return info_cache
    client = _connect()
    try:
        def _exec(cmd: str) -> str:
            stdin, stdout, stderr = client.exec_command(cmd)
            return stdout.read().decode().strip()

        host = (_exec("hostname")or "") + f' IP({SSH_HOST})'
        platform = _exec("uname -a")

        try:
            cores = int(_exec("nproc"))
        except Exception:
            cores = None

        try:
            mem_kb = int(_exec("grep MemTotal /proc/meminfo | awk '{print $2}'"))
            mem_mb = mem_kb // 1024
        except Exception:
            mem_mb = None

        try:
            uptime_s = int(float(_exec("cat /proc/uptime").split()[0]))
        except Exception:
            uptime_s = None

        info_cache= {
            "host": host,
            "platform": platform,
            "cores": cores,
            "mem_mb": mem_mb,
            "uptime_s": uptime_s,
        }
        return info_cache
    except Exception as e:
        log.warning(f"info via SSH failed: {e}")
        return {
            "host": SSH_HOST,
            "platform": None,
            "cores": None,
            "mem_mb": None,
            "uptime_s": None,
        }
    finally:
        client.close()


def health():
    client = _connect()
    try:
        def _exec(cmd: str) -> str:
            stdin, stdout, stderr = client.exec_command(cmd)
            return stdout.read().decode().strip()

        try:
            uptime_s = int(float(_exec("cat /proc/uptime" ).split()[0]))
        except Exception:
            uptime_s = 0

        try:
            load_avg = [float(x) for x in _exec("cut -d' ' -f1-3 /proc/loadavg").split()[:3]]
        except Exception:
            load_avg = []

        meminfo = _exec("cat /proc/meminfo")
        mem: dict[str, int] = {}
        for line in meminfo.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                mem[parts[0].rstrip(':').lower()] = int(parts[1])
        memory = {
            "total": mem.get("memtotal", 0) // 1024,
            "available": mem.get("memavailable", mem.get("memfree", 0)) // 1024,
            "used": (mem.get("memtotal", 0) - mem.get("memfree", 0)) // 1024,
            "free": mem.get("memfree", 0) // 1024,
        }
        swap = {
            "total": mem.get("swaptotal", 0) // 1024,
            "used": (mem.get("swaptotal", 0) - mem.get("swapfree", 0)) // 1024,
            "free": mem.get("swapfree", 0) // 1024,
        }

        df_line = _exec("df -k / 2>/dev/null | tail -1")
        parts = df_line.split()
        disk = {
            "total": round(int(parts[1]) / 1024 / 1024, 2) if len(parts) > 1 else 0,
            "used": round(int(parts[2]) / 1024 / 1024, 2) if len(parts) > 2 else 0,
            "free": round(int(parts[3]) / 1024 / 1024, 2) if len(parts) > 3 else 0,
        }

        ip = (
            _exec("ip route get 8.8.8.8 2>/dev/null | awk '{print $7;exit}'")
            or _exec("hostname -I 2>/dev/null | awk '{print $1}'")
            or "unavailable"
        ).strip()

        return {
            "uptime_s": uptime_s,
            "load_avg": load_avg,
            "cpu_usage_pct": None,
            "memory": memory,
            "swap": swap,
            "disk": disk,
            "failed_services": [],
            "last_apt_update": None,
            "cpu_temperature": None,
            "ip_address": ip,
        }
    except Exception as e:
        log.warning(f"health via SSH failed: {e}")
        return {"error": str(e)}
    finally:
        client.close()
