#!/usr/bin/env python3
"""Bounded watchdog for an existing GPTAdmin FRP systemd unit.

The watchdog restarts only the configured unit when it is inactive or when a
configured minimum number of child processes is missing. A cooldown prevents
restart storms; the normal systemd Restart=always policy remains the primary
recovery mechanism.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import time
from pathlib import Path


def run(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, capture_output=True, check=False)


def active(unit: str) -> bool:
    return run("systemctl", "is-active", "--quiet", unit).returncode == 0


def process_count(pattern: str) -> int:
    result = run("pgrep", "-fc", pattern)
    if result.returncode not in (0, 1):
        return 0
    try:
        return int(result.stdout.strip() or "0")
    except ValueError:
        return 0


def load_state(path: Path) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return {}
    return value if isinstance(value, dict) else {}


def save_state(path: Path, state: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(state, sort_keys=True) + "\n", encoding="utf-8")
    temporary.replace(path)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--unit", default="gptadmin-tunnel-frpc.service")
    parser.add_argument("--process-pattern", default="/opt/gptadmin/bin/frpc -c")
    parser.add_argument("--expected-processes", type=int, default=0)
    parser.add_argument("--state", default="/run/gptadmin/frp-watchdog.json")
    parser.add_argument("--cooldown", type=int, default=60)
    args = parser.parse_args(argv)

    state_path = Path(args.state)
    state = load_state(state_path)
    count = process_count(args.process_pattern) if args.expected_processes else None
    reason = ""
    if not active(args.unit):
        reason = "unit_inactive"
    elif count is not None and count < args.expected_processes:
        reason = f"child_processes_below_minimum:{count}/{args.expected_processes}"

    now = int(time.time())
    if not reason:
        state.update({"last_check": now, "last_result": "healthy", "unit": args.unit})
        save_state(state_path, state)
        print(json.dumps({"ok": True, "result": "healthy", "unit": args.unit, "processes": count}))
        return 0

    last_restart = int(state.get("last_restart", 0) or 0)
    if now - last_restart < max(1, args.cooldown):
        state.update({"last_check": now, "last_result": "restart_suppressed", "reason": reason})
        save_state(state_path, state)
        print(json.dumps({"ok": False, "result": "restart_suppressed", "unit": args.unit, "reason": reason}))
        return 2

    restarted = run("systemctl", "restart", args.unit)
    state.update({"last_check": now, "last_restart": now, "last_result": "restart_requested", "reason": reason})
    save_state(state_path, state)
    ok = restarted.returncode == 0
    print(json.dumps({"ok": ok, "result": "restart_requested", "unit": args.unit, "reason": reason}))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
