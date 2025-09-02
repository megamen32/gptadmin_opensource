import subprocess, logging

log = logging.getLogger("rootd_win")

TMO_DEF = 60
LOG_MAX = 8192

def _truncate(s):
    if isinstance(s, bytes):
        s = s.decode(errors="ignore")
    return s[:LOG_MAX] + f"\n…<truncated to {LOG_MAX}B>…" if len(s) > LOG_MAX else s

def run(cmd: str, timeout: int | None = None, cwd: str | None = None, env: dict | None = None):
    log.debug(f"Running command (Windows): {cmd} (timeout={timeout}, cwd={cwd})")
    try:
        res = subprocess.run(
            ["powershell", "-Command", cmd],
            cwd=cwd,
            text=True,
            timeout=timeout or TMO_DEF,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
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
