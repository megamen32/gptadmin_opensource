# Existing fleet-health cross-host state mismatch

Status: todo
Observed during health-monitoring activation audit on 2026-08-09.

Symptom: `python3 /opt/fleet-health/fleet_health.py --dry-run --hosts server01`
emitted a `fleet-health:router:recovery` notification while the requested
probe target was only `server01`. The output also showed server01 at `cpu_pct=100.0` and
`load1=45.53`.

Smallest evidence: the installed fleet-health state is global at
`/var/lib/fleet-health/state.json`; the dry-run subset compares that global
state against the filtered current set. No production state was changed and no
notification was sent by the dry-run.

Blocker: do not use filtered fleet-health runs as the new health incident
producer until state is target-scoped or the old fleet-health source is
reconciled with the new per-host producer. This remains outside the selected
vertical implementation and needs a separate bounded repair/review.
