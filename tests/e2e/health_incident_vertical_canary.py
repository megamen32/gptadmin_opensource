"""Disposable black-box-ish vertical canary for the health incident contract.

The NoticePlace HTTP server and the health producer are real local code. The
GPTAdmin, OmniRoute, Agent-Herder, and Telegram transports are deterministic
test doubles so this canary never sends an external message or mutates a live
service.
"""

from __future__ import annotations

import json
import sys
import tempfile
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.error import HTTPError
from urllib.request import Request, urlopen


NOTICEPLACE_ROOT = Path("/home/admin/agents-projects/noticeplace")
HEALTH_ROOT = Path("/home/admin/ServersAdministartion/automation/health-incident-monitor")
for root in (NOTICEPLACE_ROOT, HEALTH_ROOT):
    if str(root) not in sys.path:
        sys.path.insert(0, str(root))

from health_monitor import HealthConfig, degraded_events, load_json, verify_source  # noqa: E402
from notification_center.core import NotificationCenter  # noqa: E402
from notification_center.health_workflow import HealthWorkflow, health_plan_keyboard  # noqa: E402
from notification_center.http_api import build_handler  # noqa: E402
from notification_center.telegram_interactions import TelegramActionCodec, TelegramInteractionPoller  # noqa: E402


class FakeAgentHerder:
    def __init__(self) -> None:
        self.session_id = "herder-session-canary"
        self.status = "completed"
        self.progress_events: list[dict[str, Any]] = []

    def diagnose(self, signal: dict[str, Any]) -> dict[str, Any]:
        self.progress_events.append({"step": "diagnose", "evidence_refs": [signal["health"]["signal_type"]]})
        return {"session_id": self.session_id, "status": self.status, "trace_ref": "trace:herder:diagnosis"}


class FakeOmniRoute:
    def propose_three_plans(self, signal: dict[str, Any]) -> list[dict[str, str]]:
        signal_type = str(signal["health"]["signal_type"])
        return [
            {"plan_id": "observe", "title": "Observe", "summary": f"Capture an independent {signal_type} snapshot", "step": "observe"},
            {"plan_id": "repair", "title": "Repair", "summary": "Apply the smallest reversible repair", "step": "repair"},
            {"plan_id": "verify", "title": "Verify", "summary": "Confirm the original source is healthy", "step": "verify"},
        ]


class FakeGPTAdmin:
    def __init__(self, herder: FakeAgentHerder, omniroute: FakeOmniRoute) -> None:
        self.herder = herder
        self.omniroute = omniroute
        self.job_id = "hub-job-health-canary"

    def diagnose_and_propose(self, signal: dict[str, Any]) -> dict[str, Any]:
        diagnosis = self.herder.diagnose(signal)
        return {
            "job_id": self.job_id,
            "session_id": diagnosis["session_id"],
            "status": diagnosis["status"],
            "correlation_id": signal["correlation_id"],
            "trace_refs": [diagnosis["trace_ref"], "trace:omniroute:plans"],
            "plans": self.omniroute.propose_three_plans(signal),
        }


def request_json(base: str, method: str, path: str, body: dict[str, Any], key: str) -> tuple[int, dict[str, Any]]:
    request = Request(
        base + path,
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer health-token", "Content-Type": "application/json", "Idempotency-Key": key},
        method=method,
    )
    try:
        with urlopen(request, timeout=3) as response:
            return response.status, json.loads(response.read())
    except HTTPError as error:
        return error.code, json.loads(error.read())


def run_canary() -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="health-canary-") as tempdir:
        center = NotificationCenter(
            Path(tempdir) / "notify.sqlite3",
            {"health-token": {"project": "health", "max_severity": "critical", "agent_jobs": ["health-diagnosis"]}},
            default_quiet_hours=[],
        )
        server = ThreadingHTTPServer(("127.0.0.1", 0), build_handler(center, "health-read-token", "mcp-token"))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{server.server_port}"
        try:
            config = HealthConfig(project="health", recipient="health", host_id="fixture", source_id="host:fixture")
            degraded = load_json(str(HEALTH_ROOT / "fixtures" / "degraded.json"))
            source_events = degraded_events(config, degraded)
            signal = next(event for event in source_events if event["health"]["signal_type"] == "disk")

            status, created = request_json(base, "POST", "/v1/health/signals", signal, "canary-signal-1")
            assert status == 202, created
            incident_id = str(created["incident_id"])
            assert len(center.list_incidents()) == 1
            assert center.latest_health_plans(incident_id) == []

            gptadmin = FakeGPTAdmin(FakeAgentHerder(), FakeOmniRoute())
            diagnosis = gptadmin.diagnose_and_propose(signal)
            status, attached = request_json(
                base,
                "POST",
                f"/v1/incidents/{incident_id}/health/plans",
                {"plans": diagnosis["plans"], "actor": "omniroute", "correlation_id": diagnosis["correlation_id"]},
                "canary-plans-1",
            )
            assert status == 200, attached
            assert len(center.latest_health_plans(incident_id)) == 3

            health_workflow = HealthWorkflow(center, "x" * 32)
            buttons = [
                button
                for row in health_plan_keyboard(health_workflow.codec, incident_id, diagnosis["plans"])["inline_keyboard"]
                for button in row
            ]
            assert len(buttons) == 3
            assert {health_workflow.codec.decode(button["callback_data"])[1] for button in buttons} == {"observe", "repair", "verify"}

            action_codec = TelegramActionCodec("x" * 32)
            health_codec = health_workflow.codec
            updates = [{"update_id": 1, "callback_query": {"id": "canary-callback", "from": {"id": 42}, "data": health_codec.encode(incident_id, "repair")}}]

            def telegram_api(method: str, _payload: dict[str, Any]) -> dict[str, Any]:
                return {"ok": True, "result": updates if method == "getUpdates" else True}

            poller = TelegramInteractionPoller(center, "test-token", {"42"}, action_codec, health_plan_codec=health_codec, api=telegram_api)
            assert poller.poll_once() == 1
            assert center.latest_health_selection(incident_id)["plan_id"] == "repair"

            progress_body = {"plan_id": "repair", "step": "repair", "evidence_refs": ["trace:repair:1"], "progress_fingerprint": "fp-repair-1", "actor": gptadmin.herder.session_id}
            status, first_progress = request_json(base, "POST", f"/v1/incidents/{incident_id}/health/progress", progress_body, "canary-progress-1")
            assert status == 200 and first_progress["useful_progress"] is True, first_progress
            status, heartbeat = request_json(base, "POST", f"/v1/incidents/{incident_id}/health/progress", {**progress_body, "heartbeat_at": 2}, "canary-progress-2")
            assert status == 200 and heartbeat["useful_progress"] is False, heartbeat

            # A completed terminal agent receipt cannot close the incident.
            status, blocked = request_json(base, "POST", f"/v1/incidents/{incident_id}/health/resolve", {"source_id": "host:fixture", "verification_id": "verify-1", "actor": "api"}, "canary-resolve-before-verify")
            assert status == 400 and "verification" in str(blocked.get("error")), blocked

            healthy = load_json(str(HEALTH_ROOT / "fixtures" / "healthy.json"))
            independent = verify_source(config, healthy, "host:fixture")
            assert independent["verified"] is True
            status, verification = request_json(
                base,
                "POST",
                f"/v1/incidents/{incident_id}/health/verification",
                {"source_id": "host:fixture", "verification_id": independent["verification_id"], "observed_state": independent["observed_state"], "fingerprint": signal["health"]["fingerprint"], "evidence_refs": independent["evidence_refs"], "actor": "probe-b"},
                "canary-verification-1",
            )
            assert status == 200 and verification["observed_state"] == "healthy", verification
            status, resolved = request_json(
                base,
                "POST",
                f"/v1/incidents/{incident_id}/health/resolve",
                {"source_id": "host:fixture", "verification_id": independent["verification_id"], "actor": "api", "elapsed_ms": 4210, "trace_refs": diagnosis["trace_refs"] + ["trace:verification"]},
                "canary-resolve-1",
            )
            assert status == 200 and resolved["state"] == "resolved", resolved
            assert len(center.list_incidents()) == 1
            receipt = center.latest_health_event(incident_id, "health.resolved")
            assert receipt is not None
            assert receipt["payload"]["elapsed_ms"] == 4210
            assert receipt["payload"]["trace_refs"] == diagnosis["trace_refs"] + ["trace:verification"]
            return {
                "incident_id": incident_id,
                "signals_seen": len(source_events),
                "plans": 3,
                "useful_progress": [True, False],
                "resolved": True,
                "elapsed_ms": 4210,
                "trace_refs": receipt["payload"]["trace_refs"],
                "transports": {
                    "health_producer": "real_local",
                    "noticeplace_http": "real_local",
                    "gptadmin": "fake",
                    "omniroute": "fake",
                    "agent_herder": "fake",
                    "telegram": "fake_no_send",
                },
                "external_sends": False,
            }
        finally:
            server.shutdown()
            thread.join()
            server.server_close()


def main() -> int:
    print(json.dumps(run_canary(), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
