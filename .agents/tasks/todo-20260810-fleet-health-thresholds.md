# Fleet health threshold leads

Status: todo
Observed: 2026-08-10

Signals from the compact read-only probe:

- `server01`: `cpus=1`, `load1=52.91`, `runnable=78/320`; this is an actionable
  overload lead under the server-health interpretation.
- `server01`: `disk_pct=85`, `disk_avail_mb=1349`; this reaches the actionable disk
  threshold.

Blocker: these are monitoring/incident candidates, not authorization to
  restart services, clean disks, or change routing. Investigate each target
  separately through the health workflow or with explicit operator approval.
