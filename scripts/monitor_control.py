#!/usr/bin/env python3
"""Start, stop, and describe the project-local Custom GPT monitor."""
from __future__ import annotations

import os
import signal
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TRASH = ROOT / "trash"
PID = TRASH / "monitor.pid"
LOG = TRASH / "logs" / "custom-gpt-monitor.jsonl"


def running() -> int | None:
    try:
        pid = int(PID.read_text().strip())
        os.kill(pid, 0)
        return pid
    except (FileNotFoundError, ProcessLookupError, ValueError):
        PID.unlink(missing_ok=True)
        return None


def show(pid: int) -> None:
    print(f"monitor: running (pid {pid})")
    print(f"logs: {LOG}")
    print("stop: make monitor-stop")


def main() -> int:
    action = sys.argv[1] if len(sys.argv) > 1 else "start"
    pid = running()
    if action == "stop":
        if pid is None:
            print("monitor: stopped")
            return 0
        os.kill(pid, signal.SIGTERM)
        PID.unlink(missing_ok=True)
        print("monitor: stopped")
        return 0
    if pid is not None:
        show(pid)
        return 0
    LOG.parent.mkdir(parents=True, exist_ok=True)
    with LOG.open("a", encoding="utf-8") as output:
        process = subprocess.Popen([sys.executable, str(ROOT / "scripts/monitor_public_hub.py"), "--output", str(LOG), "--max-seconds", "86400"], cwd=ROOT, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
    PID.write_text(f"{process.pid}\n", encoding="utf-8")
    show(process.pid)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
