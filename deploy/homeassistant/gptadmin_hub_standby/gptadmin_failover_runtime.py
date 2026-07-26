#!/usr/bin/env python3
"""Run the HAOS standby Hub and its independent takeover loop.

HAOS has no systemd, so the add-on owns the fallback proxy and watchdog
processes directly. The watchdog starts FRP only after the primary public route
has failed its configured threshold; the proxy remains ready to accept signed
reclaim requests once FRP is promoted.
"""

from __future__ import annotations

import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any


OPTIONS_FILE = Path("/data/options.json")
FAILOVER_DIR = Path("/opt/gptadmin/failover")
RUNTIME_STATE = Path("/data/config/failover_watchdog_state.json")
FRPC_CONFIG = Path("/data/config/frpc-failover.toml")
FRPC_PID = Path("/data/config/failover_frpc.pid")
RECLAIM_COMMAND = Path("/data/config/failover_reclaim_command.json")


def load_options() -> dict[str, Any]:
    """Load the Supervisor-provided add-on options."""

    return json.loads(OPTIONS_FILE.read_text(encoding="utf-8"))


def command(options: dict[str, Any]) -> list[str]:
    """Build one systemd-free watchdog invocation from add-on options."""

    node_id = str(options.get("failover_node_id") or "shell:haos")
    return [
        sys.executable,
        "/usr/local/bin/gptadmin_failover_watchdog.py",
        "--check-once",
        "--config",
        str(FAILOVER_DIR / "failover_config.json"),
        "--state",
        str(FAILOVER_DIR / "failover_state.json"),
        "--runtime-state",
        str(RUNTIME_STATE),
        "--node-id",
        node_id,
        "--hub-service",
        "none",
        "--frpc-service",
        "none",
        "--frpc-bin",
        "/usr/local/bin/frpc",
        "--frpc-config",
        str(FRPC_CONFIG),
        "--frpc-pid-file",
        str(FRPC_PID),
        "--reclaim-command-file",
        str(RECLAIM_COMMAND),
    ]


def start_proxy(options: dict[str, Any]) -> subprocess.Popen[bytes]:
    """Start the reclaim-aware local proxy used by the failover FRP route."""

    listen = str(options.get("failover_proxy_listen") or "0.0.0.0:9101")
    return subprocess.Popen(
        [
            sys.executable,
            "/usr/local/bin/gptadmin_failover_proxy.py",
            "--listen",
            listen,
            "--upstream",
            "http://127.0.0.1:9001",
            "--command-file",
            str(RECLAIM_COMMAND),
            "--node-id",
            str(options.get("failover_node_id") or "shell:haos"),
        ]
    )


def stop_frpc() -> None:
    """Stop a FRP process started by the system-free watchdog."""

    try:
        pid = int(FRPC_PID.read_text(encoding="utf-8").strip())
    except (FileNotFoundError, ValueError):
        return
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    FRPC_PID.unlink(missing_ok=True)


def frpc_is_alive() -> bool:
    """Return whether at least one fallback FRP client still owns its PID."""

    try:
        pids = [int(line) for line in FRPC_PID.read_text(encoding="utf-8").splitlines() if line.strip()]
    except (FileNotFoundError, ValueError):
        return False
    for pid in pids:
        try:
            os.kill(pid, 0)
        except (ProcessLookupError, PermissionError):
            continue
        return True
    return False


def reset_dead_frpc_cooldown() -> None:
    """Allow a fresh promotion when every fallback FRP client has exited."""

    if not FRPC_PID.exists() or frpc_is_alive():
        return
    try:
        runtime = json.loads(RUNTIME_STATE.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        return
    if not runtime.get("last_promotion_at"):
        return
    runtime["last_promotion_at"] = 0
    runtime["last_decision"] = "frpc_exited"
    RUNTIME_STATE.write_text(json.dumps(runtime, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    """Keep proxy and watchdog alive until the add-on receives SIGTERM."""

    options = load_options()
    if not (FAILOVER_DIR / "failover_config.json").is_file():
        raise SystemExit("missing /opt/gptadmin/failover/failover_config.json")
    if not (FAILOVER_DIR / "failover_state.json").is_file():
        raise SystemExit("missing /opt/gptadmin/failover/failover_state.json")

    interval = max(5.0, float(options.get("failover_check_interval_sec") or 15))
    stop = False

    def request_stop(_signum: int, _frame: Any) -> None:
        nonlocal stop
        stop = True

    signal.signal(signal.SIGTERM, request_stop)
    signal.signal(signal.SIGINT, request_stop)
    proxy = start_proxy(options)
    try:
        while not stop:
            if proxy.poll() is not None:
                proxy = start_proxy(options)
            reset_dead_frpc_cooldown()
            result = subprocess.run(command(options), check=False)
            if result.returncode != 0:
                print(json.dumps({"ok": False, "failover_runtime": "watchdog_failed", "returncode": result.returncode}), flush=True)
            deadline = time.monotonic() + interval
            while not stop and time.monotonic() < deadline:
                time.sleep(min(1.0, max(0.0, deadline - time.monotonic())))
    finally:
        stop_frpc()
        proxy.terminate()
        try:
            proxy.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proxy.kill()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
