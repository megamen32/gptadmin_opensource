#!/usr/bin/env python3
"""Read-only preflight for the live health-incident activation boundary.

It never reads secret values, writes runtime state, starts services, or sends
an event.  A post-deploy run is the gate before an approved external canary.
"""

from __future__ import annotations

import json
import base64
import grp
import hashlib
import os
import stat
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


def _systemd_show(unit: str, *, user: bool = False) -> dict[str, str]:
    command = ["systemctl"]
    environment = os.environ.copy()
    if user:
        command.append("--user")
        environment.setdefault("XDG_RUNTIME_DIR", "/run/user/1000")
        environment.setdefault("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
    command.extend([
        "show",
        unit,
        "-p",
        "LoadState",
        "-p",
        "ActiveState",
        "-p",
        "SubState",
        "-p",
        "MainPID",
        "-p",
        "Result",
        "-p",
        "ExecMainStatus",
        "-p",
        "ExecMainStartTimestamp",
        "-p",
        "ExecMainStartTimestampMonotonic",
    ])
    try:
        result = subprocess.run(command, capture_output=True, text=True, check=False, env=environment, timeout=5)
    except (OSError, subprocess.TimeoutExpired):
        return {"probe_error": "systemd_probe_failed"}
    values = {line.split("=", 1)[0]: line.split("=", 1)[1] for line in result.stdout.splitlines() if "=" in line}
    if result.returncode != 0:
        values["probe_error"] = "systemd_probe_failed"
        values["probe_returncode"] = str(result.returncode)
    return values


def runtime_config_readable(path: Path, group: str) -> bool:
    """Check group-read metadata without opening the config or env file."""
    try:
        metadata = path.stat()
        expected_gid = grp.getgrnam(group).gr_gid
    except (OSError, KeyError):
        return False
    mode = stat.S_IMODE(metadata.st_mode)
    return metadata.st_gid == expected_gid and bool(mode & stat.S_IRGRP)


def runtime_secret_file_safe(path: Path) -> bool:
    """Check owner/mode metadata without opening a secret-bearing env file."""
    try:
        metadata = path.stat()
    except OSError:
        return False
    return metadata.st_uid == 0 and stat.S_IMODE(metadata.st_mode) == 0o600


def _direct_open(request: urllib.request.Request, timeout: float) -> Any:
    """Keep localhost health evidence off ambient proxy routes."""
    return urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=timeout)


def _http_probe(url: str, *, method: str = "GET", body: bytes | None = None, headers: dict[str, str] | None = None) -> dict[str, Any]:
    request = urllib.request.Request(
        url,
        data=body,
        headers={"Accept": "application/json", "Content-Type": "application/json", **(headers or {})},
        method=method,
    )

    def response_meta(response: Any, body: bytes) -> dict[str, Any]:
        headers = getattr(response, "headers", {})
        if hasattr(headers, "get_content_type"):
            content_type = headers.get_content_type()
        else:
            content_type = str(getattr(headers, "get", lambda *_args: "")("content-type", ""))
        return {"status": response.status, "content_type": content_type[:80], "body_bytes": len(body)}

    try:
        with _direct_open(request, timeout=4) as response:
            return response_meta(response, response.read(512))
    except urllib.error.HTTPError as error:
        return {"status": error.code, "content_type": str(error.headers.get_content_type() if error.headers else "")[:80], "body_bytes": len(error.read(512))}
    except Exception as error:  # noqa: BLE001 - bounded diagnostic output
        return {"status": None, "error_type": type(error).__name__}


def _env_value(path: Path, key: str) -> str:
    """Read one approved local credential for an internal probe only."""
    try:
        for line in path.read_text(encoding="utf-8").splitlines():
            if line.startswith(key + "="):
                value = line.split("=", 1)[1].strip()
                if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                    value = value[1:-1]
                return value
    except OSError:
        pass
    return ""


def _authenticated_http_probe(url: str, credential: str, *, basic_user: str | None = None) -> dict[str, Any]:
    """Probe an authenticated endpoint while returning only response metadata."""
    if not credential:
        return _http_probe(url)
    if basic_user is None:
        headers = {"Authorization": f"Bearer {credential}"}
    else:
        encoded = base64.b64encode(f"{basic_user}:{credential}".encode()).decode()
        headers = {"Authorization": f"Basic {encoded}"}
    return _http_probe(url, headers=headers)


def _authenticated_env_probe(url: str, path: Path, key: str, *, basic_user: str | None = None) -> dict[str, Any]:
    """Let a bounded root helper read a protected env file and return metadata only."""
    helper = """
import base64, json, sys, urllib.error, urllib.request
request_data = json.loads(sys.stdin.read())
value = ''
for line in open(request_data['path'], encoding='utf-8'):
    if line.startswith(request_data['key'] + '='):
        value = line.split('=', 1)[1].strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in \"\\\"'\":
            value = value[1:-1]
        break
headers = {'Accept': 'application/json', 'Content-Type': 'application/json'}
if value:
    if request_data.get('basic_user') is None:
        import base64
        headers['Authorization'] = 'Bearer ' + value
    else:
        headers['Authorization'] = 'Basic ' + base64.b64encode((request_data['basic_user'] + ':' + value).encode()).decode()
request = urllib.request.Request(request_data['url'], headers=headers)
try:
    with urllib.request.urlopen(request, timeout=4) as response:
        print(json.dumps({'status': response.status, 'content_type': response.headers.get_content_type(), 'body_bytes': len(response.read(512))}))
except urllib.error.HTTPError as error:
    print(json.dumps({'status': error.code, 'content_type': error.headers.get_content_type() if error.headers else '', 'body_bytes': len(error.read(512))}))
except Exception as error:
    print(json.dumps({'status': None, 'error_type': type(error).__name__}))
"""
    request_data = json.dumps({"url": url, "path": str(path), "key": key, "basic_user": basic_user})
    try:
        result = subprocess.run(
            ["sudo", "-n", "python3", "-c", helper],
            input=request_data,
            capture_output=True,
            text=True,
            check=False,
            timeout=6,
        )
        value = json.loads(result.stdout) if result.returncode == 0 else {}
        return value if isinstance(value, dict) else {"status": None, "error_type": "invalid_helper_result"}
    except (OSError, subprocess.TimeoutExpired, json.JSONDecodeError):
        return {"status": None, "error_type": "credential_probe_failed"}


def readiness_blockers(
    artifact_alignment: dict[str, bool],
    units: dict[str, dict[str, str]],
    endpoints: dict[str, dict[str, Any]],
    runtime_artifacts: dict[str, bool],
) -> list[str]:
    blockers: list[str] = []
    if not artifact_alignment["noticeplace_live_health_workflow"]:
        blockers.append("live NoticePlace release lacks health workflow")
    if not artifact_alignment["agent_herder_live_progress_route"]:
        blockers.append("live Agent-Herder dist lacks useful-progress route")
    if endpoints.get("agent_herder_health_route", {}).get("status") != 405:
        blockers.append("live Agent-Herder health remediation route is not HTTP 405 for the read-only probe")
    for name, label in (
        ("health_source_producer", "health producer source is missing"),
        ("health_source_fleet_adapter", "health fleet adapter source is missing"),
        ("health_source_fleet_config", "health fleet config template is missing"),
    ):
        if not artifact_alignment[name]:
            blockers.append(label)
    noticeplace_status = endpoints.get("noticeplace_health", {}).get("status")
    noticeplace_authenticated = endpoints.get("noticeplace_health_authenticated", {}).get("status")
    if noticeplace_authenticated == 200:
        pass
    elif noticeplace_status in {401, 403}:
        blockers.append("NoticePlace health route exists but authenticated readiness is not proven")
    elif noticeplace_status != 200:
        blockers.append("live NoticePlace health endpoint is not HTTP 200")
    for name in ("notification-center", "gptadmin-hub", "agent-herder", "opencode", "omniroute", "hermes"):
        if units.get(name, {}).get("ActiveState") != "active":
            blockers.append("Hermes gateway is not active" if name == "hermes" else f"{name} is not active")
    if endpoints.get("opencode_health", {}).get("status") == 401:
        blockers.append("OpenCode authenticated readiness is not proven")
    elif endpoints.get("opencode_health", {}).get("status") != 200:
        blockers.append("OpenCode health endpoint is not HTTP 200")
    if endpoints.get("omniroute_health", {}).get("status") != 200:
        blockers.append("OmniRoute health endpoint is not HTTP 200")
    if not runtime_artifacts.get("collector_installed"):
        blockers.append("health collector runtime is not installed with an active timer/config")
    return list(dict.fromkeys(blockers))


def live_herder_route_present(text: str) -> bool:
    """Accept the current web route bundle and the legacy progress marker."""
    return "health/progress" in text or "health/remediation" in text or "useful_progress" in text


def _sha256_file(path: Path) -> str | None:
    try:
        digest = hashlib.sha256()
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(131_072), b""):
                digest.update(chunk)
        return digest.hexdigest()
    except OSError:
        return None


def _same_digest(source: Path, live: Path) -> bool:
    source_digest = _sha256_file(source)
    return source_digest is not None and source_digest == _sha256_file(live)


def _service_run_succeeded(unit: dict[str, str]) -> bool:
    raw_timestamp = unit.get("ExecMainStartTimestampMonotonic")
    try:
        started_at = int(raw_timestamp or "")
    except ValueError:
        return False
    age = (time.monotonic_ns() // 1_000) - started_at
    return (
        unit.get("LoadState") == "loaded"
        and unit.get("Result") == "success"
        and unit.get("ExecMainStatus") == "0"
        and unit.get("ExecMainStartTimestamp") not in {None, "", "-", "n/a", "N/A"}
        and 0 <= age <= 180_000_000
    )


def run() -> dict[str, Any]:
    source_notice = Path("/home/roomhacker/agents-projects/noticeplace/notification_center/health_workflow.py")
    live_notice = Path("/opt/noticeplace/notification_center/health_workflow.py")
    source_herder = Path("/home/roomhacker/agents-projects/agent-herder/src/health-progress.ts")
    live_herder = Path("/home/roomhacker/agents-projects/agent-herder/dist/index.js")
    live_herder_server = Path("/home/roomhacker/agents-projects/agent-herder/dist/web/server.js")
    live_herder_progress = Path("/home/roomhacker/agents-projects/agent-herder/dist/health-progress.js")
    live_herder_remediation = Path("/home/roomhacker/agents-projects/agent-herder/dist/health-remediation.js")
    source_health_monitor = Path("/home/roomhacker/ServersAdministartion/automation/health-incident-monitor/health_monitor.py")
    source_fleet_adapter = Path("/home/roomhacker/ServersAdministartion/automation/health-incident-monitor/fleet_health_monitor.py")
    source_fleet_config = Path("/home/roomhacker/ServersAdministartion/automation/health-incident-monitor/health-incident-fleet.json.example")
    live_health_monitor = Path("/opt/health-incident-monitor/health_monitor.py")
    live_fleet_adapter = Path("/opt/health-incident-monitor/fleet_health_monitor.py")
    live_fleet_service = Path("/etc/systemd/system/health-incident-fleet.service")
    live_fleet_timer = Path("/etc/systemd/system/health-incident-fleet.timer")
    local_timer_path = Path("/etc/systemd/system/health-incident-monitor.timer")
    fleet_timer_path = Path("/etc/systemd/system/health-incident-fleet.timer")
    local_config_path = Path("/etc/health-incident-monitor.json")
    fleet_config_path = Path("/etc/health-incident-fleet.json")
    live_herder_text = "\n".join(
        path.read_text(encoding="utf-8", errors="replace")
        for path in (live_herder, live_herder_server, live_herder_progress, live_herder_remediation)
        if path.exists()
    )
    units = {
        "notification-center": _systemd_show("notification-center.service"),
        "gptadmin-hub": _systemd_show("gptadmin-hub.service"),
        "agent-herder": _systemd_show("agent-herder.service", user=True),
        "opencode": _systemd_show("opencode.service", user=True),
        "hermes": _systemd_show("hermes-gateway.service", user=True),
        "omniroute": _systemd_show("omniroute@20128.service"),
        "health-incident-fleet.service": _systemd_show("health-incident-fleet.service"),
        "health-incident-monitor.timer": _systemd_show("health-incident-monitor.timer"),
        "health-incident-fleet.timer": _systemd_show("health-incident-fleet.timer"),
        "health-incident-monitor.service": _systemd_show("health-incident-monitor.service"),
    }
    endpoints = {
        "noticeplace_health": _http_probe("http://127.0.0.1:8091/health"),
        "noticeplace_health_authenticated": _authenticated_env_probe(
            "http://127.0.0.1:8091/health",
            Path("/etc/notification-center.env"),
            "NOTIFY_CENTER_HEALTH_TOKEN",
        ),
        "agent_herder_health_route": _http_probe(
            "http://127.0.0.1:18787/api/health/remediation",
            method="GET",
        ),
        "opencode_health": _authenticated_env_probe(
            "http://127.0.0.1:4095/global/health",
            Path("/home/roomhacker/.config/openchamber/startup.env"),
            "OPENCODE_SERVER_PASSWORD",
            basic_user="opencode",
        ),
        "omniroute_health": _http_probe("http://127.0.0.1:20128/api/monitoring/health"),
        "hermes_dashboard": _http_probe("http://127.0.0.1:9119/"),
    }
    artifact_alignment = {
        "noticeplace_source_health_workflow": source_notice.exists(),
        "noticeplace_live_health_workflow": live_notice.exists(),
        "agent_herder_source_progress": source_herder.exists(),
        "agent_herder_live_progress_route": live_herder_route_present(live_herder_text),
        "health_source_producer": source_health_monitor.exists(),
        "health_source_fleet_adapter": source_fleet_adapter.exists(),
        "health_source_fleet_config": source_fleet_config.exists(),
    }
    runtime_artifacts = {
        "local_timer_file": local_timer_path.exists(),
        "fleet_timer_file": fleet_timer_path.exists(),
        "local_config_file": local_config_path.exists(),
        "fleet_config_file": fleet_config_path.exists(),
        "local_config_readable": runtime_config_readable(local_config_path, "health-monitor"),
        "fleet_config_readable": runtime_config_readable(fleet_config_path, "roomhacker"),
        "local_env_file": Path("/etc/health-incident-monitor.env").is_file(),
        "fleet_env_file": Path("/etc/health-incident-monitor.env").is_file(),
        "local_env_file_safe": runtime_secret_file_safe(Path("/etc/health-incident-monitor.env")),
        "fleet_env_file_safe": runtime_secret_file_safe(Path("/etc/health-incident-monitor.env")),
        "local_health_monitor_digest_matches": _same_digest(source_health_monitor, live_health_monitor),
        "local_service_unit_matches": _same_digest(source_health_monitor.parent / "health-incident-monitor.service", Path("/etc/systemd/system/health-incident-monitor.service")),
        "local_timer_unit_matches": _same_digest(source_health_monitor.parent / "health-incident-monitor.timer", Path("/etc/systemd/system/health-incident-monitor.timer")),
        "fleet_health_monitor_digest_matches": _same_digest(source_health_monitor, live_health_monitor),
        "fleet_source_digest_matches": _same_digest(source_fleet_adapter, live_fleet_adapter),
        "fleet_service_unit_matches": _same_digest(source_health_monitor.parent / "health-incident-fleet.service", live_fleet_service),
        "fleet_timer_unit_matches": _same_digest(source_health_monitor.parent / "health-incident-fleet.timer", live_fleet_timer),
        "fleet_service_last_run_succeeded": _service_run_succeeded(units["health-incident-fleet.service"]),
        "local_service_last_run_succeeded": _service_run_succeeded(units["health-incident-monitor.service"]),
    }
    runtime_artifacts["fleet_artifacts_aligned"] = all(
        runtime_artifacts[key]
        for key in (
            "fleet_timer_file",
            "fleet_config_file",
            "fleet_config_readable",
            "fleet_env_file",
            "fleet_env_file_safe",
            "fleet_health_monitor_digest_matches",
            "fleet_source_digest_matches",
            "fleet_service_unit_matches",
            "fleet_timer_unit_matches",
            "fleet_service_last_run_succeeded",
        )
    )
    runtime_artifacts["local_artifacts_aligned"] = all(
        runtime_artifacts[key]
        for key in (
            "local_timer_file",
            "local_config_file",
            "local_config_readable",
            "local_env_file",
            "local_env_file_safe",
            "local_health_monitor_digest_matches",
            "local_service_unit_matches",
            "local_timer_unit_matches",
            "local_service_last_run_succeeded",
        )
    )
    runtime_artifacts["collector_installed"] = (
        runtime_artifacts["local_artifacts_aligned"]
        and units["health-incident-monitor.timer"].get("ActiveState") == "active"
    ) or (
        runtime_artifacts["fleet_artifacts_aligned"]
        and units["health-incident-fleet.timer"].get("ActiveState") == "active"
    )
    blockers = readiness_blockers(artifact_alignment, units, endpoints, runtime_artifacts)
    return {
        "schema": "health-incident-production-preflight.v1",
        "artifact_alignment": artifact_alignment,
        "units": units,
        "endpoints": endpoints,
        "runtime_artifacts": runtime_artifacts,
        "ready_for_external_canary": not blockers,
        "blockers": blockers,
        "external_send": False,
    }


if __name__ == "__main__":
    print(json.dumps(run(), ensure_ascii=False, sort_keys=True))
