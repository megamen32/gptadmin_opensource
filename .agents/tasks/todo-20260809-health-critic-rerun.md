# Fresh Critic rerun: health incident autopilot

Role: Critic
Status: todo
Owner: fresh independent Critic rerun
Parent task: work-20260809-health-monitoring-orchestration.md

Adversarial read-only gate after remediation. Try to falsify the contract with
bounded local evidence: race two plan callbacks, submit progress before and
after selection, resolve after heartbeat-only versus useful progress, inject
secret-like allowed fields into Hermes receipts, and issue GET on the Hub
progress path. Check that any remaining production/Telegram boundary is stated
as pending rather than claimed green.

Use only the selected health paths in the Reviewer rerun task. Do not edit
product code, secrets, runtime state, or external systems; do not send
Telegram. Append a concise PASS/BLOCKED verdict and exact reproductions here.

## Critic rerun evidence (English)

- 2026-08-09: Read-only focused checks completed. `noticeplace` health/API tests: `22 passed`; health producer tests: `5 passed`; Hermes bridge tests: `9 passed`; Agent Herder direct `vitest` invocation: `3 passed`; GPTAdmin Hub webhook focused tests: passed; disposable vertical canary: passed with `signals_seen=5`, `plans=3`, `useful_progress=[true,false]`, `resolved=true`, `elapsed_ms=4210`, and three trace refs. The initial Herder command used the Vite UI root and found no tests; rerunning with `--root .../agent-herder --dir .../agent-herder/tests` passed.
- 2026-08-09: The canary is truthful about transport boundaries in its module contract: NoticePlace HTTP and the health producer are real local code, while GPTAdmin, OmniRoute, Agent-Herder, and Telegram are deterministic test doubles; it therefore is not production or Telegram delivery proof (`tests/e2e/health_incident_vertical_canary.py:1-6`).
- 2026-08-09: Hub progress GET is read-only by construction: `/webhook-jobs/{id}/progress` returns `405` before locking or reading job state for non-POST methods (`go-hub/internal/hub/webhook_gateway.go:296-309`); the focused test issues that GET and then confirms the two prior receipts and useful flags are unchanged (`go-hub/internal/hub/webhook_gateway_test.go:554-567`).
- 2026-08-09: Same-instance NoticePlace tests prove one winner, selected-plan progress gating, useful-vs-repeat behavior, independent verifier rejection, matching-source resolution, and secret-safe bounded receipts (`noticeplace/tests/test_health_workflow.py:132-230`).
- 2026-08-09: Fresh selection-gate probe returned `before_selection: 'health progress requires the selected plan'` and `after_selection_useful: True`; this sub-gate passes for sequential callbacks.

### BLOCKER 1: heartbeat-only receipt can resolve an incident

Exact bounded reproduction (temporary SQLite DB, no external systems):

Shared setup for the two snippets below:

```python
import json, tempfile
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from notification_center.core import NotificationCenter
from notification_center.health_workflow import HealthWorkflow

PROFILE = {"producer-token": {"project": "hermes", "max_severity": "critical"}}
PLANS = [
    {"plan_id": "observe", "title": "Observe", "summary": "Observe", "step": "observe"},
    {"plan_id": "repair", "title": "Repair", "summary": "Repair", "step": "repair"},
    {"plan_id": "verify", "title": "Verify", "summary": "Verify", "step": "verify"},
]
SIGNAL = {
    "project": "hermes", "recipient": "health", "severity": "critical", "title": "degraded",
    "body": "bounded", "dedup_key": "health:heartbeat-only", "source_id": "source-a",
    "host_id": "host-a", "signal_type": "disk",
}
```

```python
# PYTHONPATH=/home/admin/agents-projects/noticeplace python3 -
with tempfile.TemporaryDirectory() as d:
    c = NotificationCenter(Path(d) / "notify.sqlite3", PROFILE, default_quiet_hours=[])
    w = HealthWorkflow(c)
    i = w.intake_signal("producer-token", "health-create", SIGNAL)["incident_id"]
    w.attach_plans(i, "plans", PLANS, actor="omniroute")
    c.record_health_plan_selection(i, "observe", "telegram", "selection")
    hb = c.record_health_progress(i, "worker", {
        "plan_id": "observe", "step": "heartbeat", "evidence_refs": [],
        "progress_fingerprint": "heartbeat-only", "heartbeat_at": 1,
    }, "heartbeat")
    c.record_health_verification(i, "worker", {
        "source_id": "source-a", "verifier_id": "probe-b", "healthy": True,
        "verification_id": "verify-1", "evidence": "independent",
    }, "verification")
    print(hb["useful_progress"], c.resolve(i, "api")["state"])
```

The inline canary created one health incident, attached three plans, selected `observe`, submitted only one receipt with `heartbeat_at=1`, empty `evidence_refs`, step `heartbeat`, and fingerprint `heartbeat-only`, then recorded a healthy independent `probe-b` verification. Observed:

```text
{'heartbeat_useful': True, 'verification_healthy': True,
 'resolved_after_heartbeat_only': True, 'state': 'resolved'}
```

`record_health_progress` computes usefulness only from step/evidence/fingerprint change and does not exclude a first heartbeat-only receipt (`noticeplace/notification_center/core.py:1181-1215`); resolution accepts any durable useful receipt for the selected plan (`core.py:1287-1311`, `core.py:1367-1373`). This falsifies the required “useful progress, not heartbeat alone” gate.

### BLOCKER 2: plan selection is not atomic across store instances

Exact bounded reproduction (two `NotificationCenter` instances sharing one temporary SQLite DB, 100-iteration budget, first iteration failed):

```python
# PYTHONPATH=/home/admin/agents-projects/noticeplace python3 -
with tempfile.TemporaryDirectory() as d:
    db = Path(d) / "notify.sqlite3"
    a = NotificationCenter(db, PROFILE, default_quiet_hours=[])
    b = NotificationCenter(db, PROFILE, default_quiet_hours=[])
    wa, wb = HealthWorkflow(a), HealthWorkflow(b)
    i = wa.intake_signal("producer-token", "health-create-0", SIGNAL)["incident_id"]
    wa.attach_plans(i, "plans-0", PLANS, actor="omniroute")
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(
            lambda item: item[0].select_plan(i, item[1], item[2], "telegram"),
            [(wa, "selection-observe", "observe"), (wb, "selection-repair", "repair")],
        ))
    rows = a._connection.execute(
        "SELECT payload_json FROM events WHERE incident_id = ? AND event_type = 'health.plan_selected'",
        (i,),
    ).fetchall()
    print([json.loads(row["payload_json"])["plan_id"] for row in rows])
```

Two concurrent callbacks selected `observe` and `repair` using different instances and idempotency keys. Observed:

```text
{'multi_instance_atomic': False,
 'failure': {'iteration': 0,
  'results': [{'plan_id': 'observe', ...}, {'plan_id': 'repair', ...}],
  'selection_rows': ['observe', 'repair']}}
```

The existing test covers only one shared Python lock (`noticeplace/tests/test_health_workflow.py:132-150`). The implementation checks for an existing selection under `self._lock` and then inserts by idempotency key, but `events` has no uniqueness constraint for `(incident_id, event_type)` or plan selection (`noticeplace/notification_center/core.py:79-87`, `1127-1149`). Separate workers/processes can both observe “not selected” and commit.

### Excluded hypotheses

- The two blockers are current local NoticePlace behavior, not stale manifests, external Telegram behavior, provider authorization, or a failed real transport.
- The multi-instance race uses distinct idempotency keys, so it is not an idempotency-key replay artifact.
- The heartbeat-only result uses a temporary database and an independent verifier, so it is not caused by production state or a verification mismatch.
- Hub GET mutation and Hermes secret/raw-log leakage were not observed in the focused checks; they are not the reason for this block.

## Critic verdict

`RETHINK` — `BLOCKED` for completion. The current evidence is insufficient for the immutable acceptance contract even though the local focused suites, Hub GET gate, Hermes sanitization, and fake/real canary boundary pass.

### QUESTIONS_FOR_L

1. Is a single NoticePlace process/connection a proved deployment invariant? If not, plan selection needs a database-level or equivalent single-writer atomic guarantee; the in-process lock is not enough.
2. Should a first receipt explicitly marked heartbeat-only be unable to satisfy useful-progress supervision? The selected contract says yes; the current implementation says no.

### Alternatives

1. Add a bounded product fix that rejects heartbeat-only receipts as useful and makes plan selection conditional/unique at the durable store boundary, then rerun the two fresh reproductions with separate store instances.
2. If single-writer topology is intentional, enforce and document that boundary at the service layer, prove no second writer can open the store, and still fix/retest heartbeat-only resolution semantics; do not claim general atomicity from the current test.

### Minimum proof to proceed

- First heartbeat-only progress returns `useful_progress=false` and resolution remains rejected; a changed useful receipt plus independent matching healthy verification resolves.
- Two separate store instances racing signed selections produce exactly one accepted plan and exactly one `health.plan_selected` event across repeated runs.
- Rerun the focused suites and canary, preserving the explicit statement that real production routing, provider/model authorization, and Telegram delivery remain pending approval and unexecuted.
