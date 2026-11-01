"""Disposable local NoticePlace operator surface for black-box acceptance.

This fixture deliberately uses the real NoticePlace SQLite/workflow/admin renderer
with a temporary database and no delivery adapter.  The auth header is bypassed
only inside this disposable process so a context-free browser session can open
the same operator HTML without knowing the reverse-proxy contract.
"""

from __future__ import annotations

import argparse
import json
import signal
import sys
import tempfile
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path

NOTICEPLACE_ROOT = Path("/home/roomhacker/agents-projects/noticeplace")
if str(NOTICEPLACE_ROOT) not in sys.path:
    sys.path.insert(0, str(NOTICEPLACE_ROOT))

from notification_center.admin import AdminConfigStore  # noqa: E402
from notification_center.admin_http import build_admin_handler  # noqa: E402
from notification_center.core import NotificationCenter  # noqa: E402
from notification_center.health_workflow import HealthWorkflow  # noqa: E402


def _seed(database: Path) -> None:
    center = NotificationCenter(database, {"health-token": {"project": "health-monitor", "max_severity": "critical", "agent_jobs": ["health-diagnosis"]}}, default_quiet_hours=[])
    workflow = HealthWorkflow(center, callback_secret="blackbox-health-callback-secret")
    created = workflow.intake_signal(
        "health-token",
        "blackbox-health-intake-1",
        {
            "project": "health-monitor",
            "recipient": "health-operator",
            "severity": "critical",
            "title": "Host node-blackbox is degraded",
            "body": "Disk pressure crossed the configured threshold.",
            "dedup_key": "health:node-blackbox:disk",
            "source_id": "node-blackbox",
            "host_id": "node-blackbox",
            "signal_type": "disk",
            "summary": "Disk pressure crossed the configured threshold.",
            "evidence_refs": ["probe:disk:2026-08-09T00:00:00Z"],
            "agent_job": "health-diagnosis",
        },
    )
    incident_id = str(created["incident_id"])
    workflow.attach_plans(
        incident_id,
        "blackbox-health-plans-1",
        [
            {"plan_id": "observe", "title": "Observe", "summary": "Capture a fresh bounded disk snapshot.", "step": "observe"},
            {"plan_id": "repair", "title": "Repair", "summary": "Apply the smallest reversible disk cleanup.", "step": "repair"},
            {"plan_id": "verify", "title": "Verify", "summary": "Confirm the original source is healthy independently.", "step": "verify"},
        ],
        actor="omniroute",
        correlation_id="corr:blackbox-health-1",
    )
    workflow.select_plan(incident_id, "blackbox-health-selection-1", "repair", "blackbox-operator")
    workflow.record_progress(
        incident_id,
        "blackbox-health-progress-1",
        plan_id="repair",
        step="cleanup",
        evidence_refs=["trace:cleanup-1", "probe:disk:after-cleanup"],
        progress_fingerprint="fp:blackbox-cleanup-1",
        actor="remediation-agent",
    )
    workflow.record_verification(
        incident_id,
        "blackbox-health-verification-1",
        source_id="node-blackbox",
        verification_id="verify:blackbox:1",
        observed_state="healthy",
        evidence_refs=["probe:disk:healthy", "trace:verify-1"],
        actor="independent-verifier",
    )
    workflow.resolve(
        incident_id,
        "node-blackbox",
        "verify:blackbox:1",
        "noticeplace-reconciler",
        elapsed_ms=4210,
        trace_refs=["trace:diagnosis-1", "trace:cleanup-1", "trace:verify-1"],
    )


def run(port: int) -> None:
    with tempfile.TemporaryDirectory(prefix="health-blackbox-") as temporary:
        root = Path(temporary)
        database = root / "notify.sqlite3"
        primary = root / "notification-center.env"
        routes = root / "routes.env"
        primary.write_text(
            "NOTIFY_CENTER_DB=" + str(database) + "\n"
            + 'NOTIFY_CENTER_TOKENS_JSON={"health-token":{"project":"health-monitor","max_severity":"critical","agent_jobs":["health-diagnosis"]}}\n',
            encoding="utf-8",
        )
        routes.write_text("TELEGRAM_SEVERITY_ROUTES_JSON={}\n", encoding="utf-8")
        _seed(database)
        store = AdminConfigStore(
            primary,
            routes,
            root / "state",
            restart=lambda: None,
            calls_override_path=root / "calls.conf",
            daemon_reload=lambda: None,
        )
        base_handler = build_admin_handler(store, "blackbox-csrf-secret")

        class FixtureHandler(base_handler):
            def _authorized(self) -> bool:  # noqa: PLR6301 - disposable local fixture only
                return True

        server = ThreadingHTTPServer(("127.0.0.1", port), FixtureHandler)
        stopping = threading.Event()

        def stop(_signum: int, _frame: object) -> None:
            if not stopping.is_set():
                stopping.set()
                threading.Thread(target=server.shutdown, daemon=True).start()

        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        print(json.dumps({"url": f"http://127.0.0.1:{server.server_port}/admin/", "incident": "node-blackbox", "external_send": False}), flush=True)
        server.serve_forever()
        server.server_close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=0)
    run(parser.parse_args().port)
