"""Run a secret-safe, read-only deployment runtime probe over SSH."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from typing import Any


REMOTE_SCRIPTS = {
    "hub": r'''
set +e
unit='gptadmin-hub.service'
state=$(systemctl show "$unit" -p ActiveState --value 2>/dev/null || true)
sub=$(systemctl show "$unit" -p SubState --value 2>/dev/null || true)
printf 'unit|%s|%s|%s\n' "$unit" "$state" "$sub"
tunnel_state=$(systemctl show gptadmin-tunnel-frpc.service -p ActiveState --value 2>/dev/null || true)
tunnel_sub=$(systemctl show gptadmin-tunnel-frpc.service -p SubState --value 2>/dev/null || true)
printf 'unit|gptadmin-tunnel-frpc.service|%s|%s\n' "$tunnel_state" "$tunnel_sub"
port="${HUB_PORT:-9001}"
code=$(curl -sS --max-time 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/healthz" 2>/dev/null || true)
printf 'port|%s|%s\n' "$port" "$code"
tunnel_started=$(systemctl show gptadmin-tunnel-frpc.service -p ExecMainStartTimestamp --value 2>/dev/null || true)
if [ -n "$tunnel_started" ] && journalctl -u gptadmin-tunnel-frpc.service --since "$tunnel_started" --no-pager -o cat 2>/dev/null | grep -qi 'router config conflict'; then
  printf 'router_conflict|true\n'
else
  printf 'router_conflict|false\n'
fi
''',
    "shellmcp": r'''
set +e
unit='shellmcp.service'
state=$(systemctl show "$unit" -p ActiveState --value 2>/dev/null || true)
sub=$(systemctl show "$unit" -p SubState --value 2>/dev/null || true)
printf 'unit|%s|%s|%s\n' "$unit" "$state" "$sub"
if systemctl cat "$unit" 2>/dev/null | grep -Eq '/rootd-go(-canary)?([[:space:]]|$)'; then
  printf 'legacy_exec|true\n'
else
  printf 'legacy_exec|false\n'
fi
if journalctl -u "$unit" -n 200 --no-pager -o cat 2>/dev/null | grep -qi 'queue poll HTTP 401'; then
  printf 'queue_auth|true\n'
else
  printf 'queue_auth|false\n'
fi
''',
}


def _unit_observation(observations: dict[str, Any], service: str) -> tuple[str, str]:
    """Return a parsed unit state without exposing remote output."""
    value = observations.get(f"unit:{service}")
    if not isinstance(value, dict):
        return "", ""
    return str(value.get("state", "")), str(value.get("substate", ""))


def parse_probe_output(output: str, *, kind: str) -> dict[str, Any]:
    """Parse the probe's fixed, non-sensitive line protocol into a report."""
    if kind not in REMOTE_SCRIPTS:
        raise ValueError(f"unsupported probe kind: {kind}")

    observations: dict[str, Any] = {}
    for raw_line in output.splitlines():
        fields = raw_line.split("|")
        if not fields:
            continue
        if fields[0] == "unit" and len(fields) == 4:
            observations[f"unit:{fields[1]}"] = {"state": fields[2], "substate": fields[3]}
        elif fields[0] == "port" and len(fields) == 3:
            observations[f"port:{fields[1]}"] = fields[2]
        elif fields[0] in {"router_conflict", "legacy_exec", "queue_auth"} and len(fields) == 2:
            observations[fields[0]] = fields[1] == "true"

    issues: list[str] = []
    if kind == "hub":
        state, substate = _unit_observation(observations, "gptadmin-hub.service")
        tunnel_state, tunnel_substate = _unit_observation(observations, "gptadmin-tunnel-frpc.service")
        if (state, substate) != ("active", "running"):
            issues.append("hub_service_not_running")
        if (tunnel_state, tunnel_substate) != ("active", "running"):
            issues.append("tunnel_service_not_running")
        port_statuses = [value for key, value in observations.items() if key.startswith("port:")]
        if len(port_statuses) != 1 or port_statuses[0] != "200":
            issues.append("hub_health_failed")
        if observations.get("router_conflict") is True:
            issues.append("tunnel_router_conflict")
    else:
        state, substate = _unit_observation(observations, "shellmcp.service")
        if (state, substate) != ("active", "running"):
            issues.append("shellmcp_service_not_running")
        if observations.get("legacy_exec") is True:
            issues.append("legacy_shellmcp_binary")
        if observations.get("queue_auth") is True:
            issues.append("queue_auth_failed")

    return {
        "status": "passed" if not issues else "failed",
        "kind": kind,
        "issues": issues,
        "observations": observations,
    }


def _validate_target(host: str, port: int, user: str) -> None:
    """Reject malformed SSH target inputs before constructing the command."""
    if not host or any(char.isspace() for char in host):
        raise ValueError("host must be a non-empty token")
    if not user or any(char.isspace() for char in user):
        raise ValueError("user must be a non-empty token")
    if not 1 <= port <= 65535:
        raise ValueError("SSH port is outside the valid range")


def run_remote_probe(host: str, port: int, user: str, kind: str) -> dict[str, Any]:
    """Run one fixed read-only SSH probe and return only parsed safe fields."""
    _validate_target(host, port, user)
    if kind not in REMOTE_SCRIPTS:
        raise ValueError(f"unsupported probe kind: {kind}")
    target = f"{user}@{host}"
    command = [
        "ssh",
        "-o",
        "BatchMode=yes",
        "-o",
        "ConnectTimeout=10",
        "-o",
        "StrictHostKeyChecking=no",
        "-p",
        str(port),
        target,
        REMOTE_SCRIPTS[kind],
    ]
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=30, check=False)
    except (OSError, subprocess.SubprocessError):
        return {"status": "failed", "kind": kind, "issues": ["ssh_failed"], "observations": {}}
    if result.returncode != 0:
        return {"status": "failed", "kind": kind, "issues": ["ssh_failed"], "observations": {}}
    return parse_probe_output(result.stdout, kind=kind)


def main(argv: list[str] | None = None) -> int:
    """Parse a deployment target and print redacted JSON without changing it."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kind", choices=sorted(REMOTE_SCRIPTS), required=True)
    parser.add_argument("--host", default=os.environ.get("GPTADMIN_RUNTIME_HOST", ""))
    parser.add_argument("--port", type=int, default=int(os.environ.get("GPTADMIN_RUNTIME_SSH_PORT", "22")))
    parser.add_argument("--user", default=os.environ.get("GPTADMIN_RUNTIME_USER", "roomhacker"))
    args = parser.parse_args(argv)
    try:
        report = run_remote_probe(args.host, args.port, args.user, args.kind)
    except ValueError as exc:
        print(json.dumps({"status": "failed", "error": str(exc)}, ensure_ascii=False))
        return 2
    print(json.dumps(report, ensure_ascii=False, sort_keys=True))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
