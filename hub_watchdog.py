#!/usr/bin/env python3
"""Dependency-free watchdog for GPTAdmin hub_proxy.

Modes:
  --check-once
      Probe the health URL once. If unhealthy, restart an existing service using
      --restart-command. This is intended for systemd timer/cron.

  --supervise -- <command...>
      Start the hub command as a child process and keep probing the health URL.
      If the child exits or the health check fails repeatedly, restart it. This
      mode uses only Python stdlib and works on Linux/macOS/Windows.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import json
import os
import pathlib
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from typing import Iterable, Optional


def utc_ts() -> str:
    return _dt.datetime.now(_dt.timezone.utc).isoformat()


def local_ts() -> str:
    return _dt.datetime.now().astimezone().isoformat(timespec="seconds")


def append_log(path: Optional[str], message: str) -> None:
    line = f"{local_ts()} {message}"
    print(line, flush=True)
    if path:
        try:
            p = pathlib.Path(path)
            p.parent.mkdir(parents=True, exist_ok=True)
            with p.open("a", encoding="utf-8") as f:
                f.write(line + "\n")
        except Exception as exc:  # pragma: no cover - logging must not kill watchdog
            print(f"{local_ts()} log_write_failed path={path!r} err={exc}", file=sys.stderr, flush=True)


def json_log(path: Optional[str], event: str, **fields: object) -> None:
    fields.setdefault("event", event)
    fields.setdefault("ts", utc_ts())
    append_log(path, json.dumps(fields, ensure_ascii=False, sort_keys=True, separators=(",", ":")))


def healthy(url: str, timeout: float) -> tuple[bool, str]:
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "gptadmin-hub-watchdog/1.0"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status = getattr(resp, "status", resp.getcode())
            if 200 <= int(status) < 300:
                return True, f"http_{status}"
            return False, f"http_{status}"
    except urllib.error.HTTPError as exc:
        return False, f"http_{exc.code}"
    except Exception as exc:
        return False, f"{type(exc).__name__}:{exc}"


def run_command(cmd: str, timeout: float, log_file: Optional[str]) -> int:
    json_log(log_file, "restart_command_start", command=cmd)
    try:
        completed = subprocess.run(cmd, shell=True, timeout=timeout, text=True, capture_output=True)
    except subprocess.TimeoutExpired:
        json_log(log_file, "restart_command_timeout", command=cmd, timeout=timeout)
        return 124
    if completed.stdout:
        append_log(log_file, completed.stdout.rstrip())
    if completed.stderr:
        append_log(log_file, completed.stderr.rstrip())
    json_log(log_file, "restart_command_done", command=cmd, returncode=completed.returncode)
    return int(completed.returncode)


def read_stamp(path: str) -> int:
    try:
        return int(pathlib.Path(path).read_text(encoding="utf-8").strip() or "0")
    except Exception:
        return 0


def write_stamp(path: str, value: int) -> None:
    p = pathlib.Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(str(value), encoding="utf-8")


def check_once(args: argparse.Namespace) -> int:
    ok, detail = healthy(args.health_url, args.timeout)
    if ok:
        json_log(args.log_file, "health_ok", url=args.health_url, detail=detail)
        return 0

    now = int(time.time())
    last = read_stamp(args.stamp_file)
    if now - last < args.min_restart_interval:
        json_log(
            args.log_file,
            "restart_suppressed",
            url=args.health_url,
            reason=detail,
            last_restart=last,
            min_restart_interval=args.min_restart_interval,
        )
        return 1

    json_log(args.log_file, "health_failed", url=args.health_url, reason=detail)
    write_stamp(args.stamp_file, now)
    rc = run_command(args.restart_command, args.restart_timeout, args.log_file)
    if rc != 0:
        return rc
    time.sleep(args.post_restart_delay)
    ok, detail = healthy(args.health_url, args.timeout)
    json_log(args.log_file, "post_restart_health", url=args.health_url, ok=ok, detail=detail)
    return 0 if ok else 2


def terminate_process(proc: subprocess.Popen[object], log_file: Optional[str]) -> None:
    if proc.poll() is not None:
        return
    json_log(log_file, "child_terminate", pid=proc.pid)
    try:
        if os.name == "nt":
            proc.terminate()
        else:
            proc.send_signal(signal.SIGTERM)
        proc.wait(timeout=10)
    except Exception:
        try:
            proc.kill()
        except Exception:
            pass


def supervise(args: argparse.Namespace, command: list[str]) -> int:
    if not command:
        append_log(args.log_file, "error: --supervise requires command after --")
        return 64

    stop = False
    proc: Optional[subprocess.Popen[object]] = None
    consecutive_failures = 0
    last_restart = 0

    def _signal_handler(signum: int, _frame: object) -> None:
        nonlocal stop
        json_log(args.log_file, "signal", signum=signum)
        stop = True
        if proc is not None:
            terminate_process(proc, args.log_file)

    if os.name != "nt":
        signal.signal(signal.SIGTERM, _signal_handler)
        signal.signal(signal.SIGINT, _signal_handler)

    while not stop:
        now = int(time.time())
        if proc is None or proc.poll() is not None:
            if proc is not None:
                json_log(args.log_file, "child_exited", pid=proc.pid, returncode=proc.returncode)
                if now - last_restart < args.min_restart_interval:
                    time.sleep(max(1, args.min_restart_interval - (now - last_restart)))
            json_log(args.log_file, "child_start", command=command)
            proc = subprocess.Popen(command)
            last_restart = int(time.time())
            consecutive_failures = 0
            time.sleep(args.startup_grace)

        ok, detail = healthy(args.health_url, args.timeout)
        if ok:
            consecutive_failures = 0
            json_log(args.log_file, "health_ok", url=args.health_url, detail=detail, pid=proc.pid)
        else:
            consecutive_failures += 1
            json_log(
                args.log_file,
                "health_failed",
                url=args.health_url,
                reason=detail,
                failures=consecutive_failures,
                max_failures=args.max_failures,
                pid=proc.pid,
            )
            if consecutive_failures >= args.max_failures:
                terminate_process(proc, args.log_file)
                proc = None
                continue

        time.sleep(args.interval)

    if proc is not None:
        terminate_process(proc, args.log_file)
    return 0


def parse_args(argv: Iterable[str]) -> tuple[argparse.Namespace, list[str]]:
    parser = argparse.ArgumentParser(description="Dependency-free GPTAdmin hub watchdog")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check-once", action="store_true", help="probe health once and restart via command on failure")
    mode.add_argument("--supervise", action="store_true", help="run and supervise a hub command after --")
    parser.add_argument("--health-url", default=os.getenv("GPTADMIN_HUB_HEALTH_URL", "http://127.0.0.1:9001/version"))
    parser.add_argument("--timeout", type=float, default=float(os.getenv("GPTADMIN_HUB_WATCHDOG_TIMEOUT", "5")))
    parser.add_argument("--interval", type=float, default=float(os.getenv("GPTADMIN_HUB_WATCHDOG_INTERVAL", "30")))
    parser.add_argument("--startup-grace", type=float, default=float(os.getenv("GPTADMIN_HUB_WATCHDOG_STARTUP_GRACE", "3")))
    parser.add_argument("--max-failures", type=int, default=int(os.getenv("GPTADMIN_HUB_WATCHDOG_MAX_FAILURES", "2")))
    parser.add_argument("--min-restart-interval", type=int, default=int(os.getenv("GPTADMIN_HUB_WATCHDOG_MIN_RESTART_INTERVAL", "60")))
    parser.add_argument("--restart-command", default=os.getenv("GPTADMIN_HUB_RESTART_COMMAND", "systemctl restart hub_proxy.service"))
    parser.add_argument("--restart-timeout", type=float, default=float(os.getenv("GPTADMIN_HUB_RESTART_TIMEOUT", "30")))
    parser.add_argument("--post-restart-delay", type=float, default=float(os.getenv("GPTADMIN_HUB_POST_RESTART_DELAY", "3")))
    parser.add_argument("--stamp-file", default=os.getenv("GPTADMIN_HUB_WATCHDOG_STAMP", "/run/gptadmin-hub-watchdog.last-restart"))
    parser.add_argument("--log-file", default=os.getenv("GPTADMIN_HUB_WATCHDOG_LOG", "/var/log/gptadmin/hub-watchdog.log"))

    if "--" in argv:
        idx = list(argv).index("--")
        ns = parser.parse_args(list(argv)[:idx])
        cmd = list(argv)[idx + 1 :]
    else:
        ns = parser.parse_args(list(argv))
        cmd = []
    return ns, cmd


def main(argv: Optional[list[str]] = None) -> int:
    args, command = parse_args(sys.argv[1:] if argv is None else argv)
    if args.check_once:
        return check_once(args)
    return supervise(args, command)


if __name__ == "__main__":
    raise SystemExit(main())
