import os
import subprocess
import logging
import asyncio
import psutil
import socket
import time
import shutil
import platform
import shlex
import pwd
from pathlib import Path
from typing import Iterable

log = logging.getLogger("rootd_linux")

TMO_DEF = int(os.getenv("EXEC_TIMEOUT", "300"))
LOG_MAX = int(os.getenv("LOG_LIMIT_B", "8192"))
PRESERVE_FILE_METADATA = os.getenv("ROOTD_PRESERVE_FILE_METADATA", "1").lower() not in {"0", "false", "no", "off"}
PRESERVE_METADATA_MAX_FILES = int(os.getenv("ROOTD_PRESERVE_METADATA_MAX_FILES", "50000"))
DEFAULT_RUN_USER = os.getenv("ROOTD_DEFAULT_USER", "")
DEFAULT_RUN_UID = os.getenv("ROOTD_DEFAULT_UID", "")


def _metadata_root(cwd: str | None) -> Path | None:
    """Return the directory whose existing files should keep owner/group/mode after exec."""

    if not cwd:
        return None
    try:
        root = Path(cwd).resolve()
    except Exception:
        return None
    if not root.exists() or not root.is_dir():
        return None
    return root


def _iter_metadata_files(root: Path) -> Iterable[Path]:
    skip_dirs = {".git", ".venv", "venv", "node_modules", "__pycache__", ".pytest_cache", ".mypy_cache"}
    yielded = 0
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [name for name in dirnames if name not in skip_dirs]
        for filename in filenames:
            path = Path(dirpath) / filename
            yield path
            yielded += 1
            if yielded >= PRESERVE_METADATA_MAX_FILES:
                return


def _command_needs_root(cmd: str) -> bool:
    """Keep root privileges only when the command explicitly contains sudo."""

    try:
        tokens = shlex.split(cmd, comments=False, posix=True)
    except Exception:
        tokens = cmd.replace("\n", " ").split()
    return any(token == "sudo" for token in tokens)


def _uid_to_user(uid: int) -> str | None:
    try:
        return pwd.getpwuid(uid).pw_name
    except KeyError:
        return None


def _install_owner_user() -> str | None:
    """Best-effort fallback: user that owns the installed rootd source tree."""

    try:
        uid = Path(__file__).resolve().stat().st_uid
    except Exception:
        return None
    if uid == 0:
        return None
    return _uid_to_user(uid)


def _cwd_owner_user(cwd: str | None) -> str | None:
    """Prefer the owner of cwd so files are created like local user work."""

    if not cwd:
        return None
    try:
        st = Path(cwd).resolve().stat()
    except Exception:
        return None
    if st.st_uid == 0:
        return None
    return _uid_to_user(st.st_uid)


def _default_run_user(cwd: str | None) -> str | None:
    """Resolve default non-root user without hardcoding product-specific usernames."""

    configured = (DEFAULT_RUN_USER or "").strip()
    if configured and configured != "root":
        return configured
    configured_uid = (DEFAULT_RUN_UID or "").strip()
    if configured_uid.isdigit() and int(configured_uid) != 0:
        user = _uid_to_user(int(configured_uid))
        if user:
            return user
    return _cwd_owner_user(cwd) or _install_owner_user()


def _wrap_default_user_command(cmd: str, cwd: str | None = None, env: dict | None = None) -> tuple[str, str | None]:
    """Run normal commands as resolved non-root user; keep root for explicit sudo commands."""

    if env and str(env.get("ROOTD_RUN_AS_ROOT", "")).lower() in {"1", "true", "yes", "on"}:
        return cmd, None
    if _command_needs_root(cmd):
        return cmd, None
    run_user = _default_run_user(cwd)
    if not run_user:
        return cmd, None
    return f"sudo -H -u {shlex.quote(run_user)} /bin/bash -lc {shlex.quote(cmd)}", run_user


def _snapshot_file_metadata(cwd: str | None):
    """Snapshot owner/group/mode of files that already exist before a root command.

    rootd runs as root. When a command rewrites a file via temp-file+rename,
    the file can become root:root or lose executable/setgid bits. This snapshot
    lets us restore metadata for existing files after the command finishes.
    """

    if not PRESERVE_FILE_METADATA:
        return None, {}
    root = _metadata_root(cwd)
    if root is None:
        return None, {}
    snapshot = {}
    try:
        for path in _iter_metadata_files(root):
            try:
                st = path.lstat()
            except FileNotFoundError:
                continue
            if not path.is_file() and not path.is_symlink():
                continue
            snapshot[path] = (st.st_uid, st.st_gid, st.st_mode & 0o7777)
    except Exception:
        log.exception("Failed to snapshot file metadata for cwd=%s", cwd)
        return root, {}
    return root, snapshot


def _restore_file_metadata(root: Path | None, snapshot: dict[Path, tuple[int, int, int]]) -> dict:
    """Restore owner/group/mode for files that existed before command execution."""

    if not snapshot:
        return {"restored": 0, "failed": 0, "root": str(root) if root else None}
    restored = 0
    failed = 0
    for path, (uid, gid, mode) in snapshot.items():
        try:
            if not path.exists() and not path.is_symlink():
                continue
            st = path.lstat()
            current_mode = st.st_mode & 0o7777
            if (st.st_uid, st.st_gid) != (uid, gid):
                os.lchown(path, uid, gid)
            if not path.is_symlink() and current_mode != mode:
                os.chmod(path, mode)
            restored += 1
        except Exception:
            failed += 1
            log.exception("Failed to restore metadata for %s", path)
    return {"restored": restored, "failed": failed, "root": str(root) if root else None}


def _truncate(s):
    if isinstance(s, bytes):
        s = s.decode(errors="ignore")
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s


def run(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running command (Linux): {cmd} (timeout={timeout}, cwd={cwd})")
    exec_cmd, run_as_user = _wrap_default_user_command(cmd, cwd, env)
    if run_as_user:
        log.debug("Running command as default user %s", run_as_user)
    metadata_root, metadata_snapshot = _snapshot_file_metadata(cwd)
    try:
        res = subprocess.run(
            exec_cmd,
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
        metadata_restore = _restore_file_metadata(metadata_root, metadata_snapshot)
        return {
            "error": f"timeout {e.timeout}s",
            "stdout": _truncate(e.stdout or ""),
            "stderr": _truncate(e.stderr or ""),
            "metadata_restore": metadata_restore,
            "run_as_user": run_as_user,
        }
    except Exception:
        _restore_file_metadata(metadata_root, metadata_snapshot)
        log.exception(f"Exception during command: {cmd}")
        raise

    metadata_restore = _restore_file_metadata(metadata_root, metadata_snapshot)
    return {
        "returncode": res.returncode,
        "stdout": _truncate(res.stdout),
        "stderr": _truncate(res.stderr),
        "metadata_restore": metadata_restore,
        "run_as_user": run_as_user,
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


def info():
    return {
        "host": socket.gethostname(),
        "platform": platform.platform(),
        "cores": psutil.cpu_count(),
        "mem_mb": round(psutil.virtual_memory().total / 2**20),
        "uptime_s": round(time.time() - psutil.boot_time()),
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

