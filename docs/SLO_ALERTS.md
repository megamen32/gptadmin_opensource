# GPTAdmin SLO and alert runbook

This is the operator contract for the self-hosted control plane. It defines
targets and response steps; it does not claim that an installation currently
meets a target until its probes have produced the required observation window.

## Service-level objectives

Measure the objectives with a scheduled, authenticated probe from the same
network boundary as the MCP clients. The probe must store only timestamps,
HTTP status, latency, build version and a request/result reference. It must not
store passwords, bearer values, tool arguments or file contents.

| Indicator | Target | Measurement | Initial alert |
| --- | --- | --- | --- |
| Hub availability | 99.9% per calendar month | `GET /healthz` succeeds with HTTP 200 and a valid JSON body | 3 consecutive failures or 5 minutes unavailable |
| Authenticated control-plane readiness | 99.5% per calendar month | `gptadmin doctor --json` has `ok=true`, including `remote_health`, `remote_auth` when configured, service runtime and permissions | 2 failed checks in 10 minutes |
| MCP request success | 99% per calendar month | Safe `tools/list` and `demo` calls complete without transport, policy or durable-job errors | 3 failures in 10 minutes |
| Durable job completion | 99% per calendar month | Queued MCP/Shell jobs reach a terminal result within the configured timeout | 5 overdue jobs or a growing retry queue |

The availability objectives exclude an explicitly recorded maintenance window.
When no external probe is available, `doctor --json` is the local diagnostic
source, not proof of an external SLO.

The minimum machine-readable signal set is:

- `gptadmin doctor --json` for service runtime, local version, Hub URL,
  health, authenticated readiness, clock and file permissions;
- `GET /healthz` for the unauthenticated liveness check;
- `GET /admin/api/overview` for authenticated service and client inventory;
- `GET /admin/api/audit` for policy decisions, job references and incident
  reconstruction. Audit entries contain digests and references, not raw
  arguments.

## Error budget

For a 30-day month, the approximate budget is 43 minutes for the 99.9% Hub
availability objective, 3 hours 36 minutes for 99.5% readiness, and 7 hours
12 minutes for 99% MCP/job success. Calculate consumed budget from failed
probe intervals, not from process uptime alone.

When an objective consumes more than 50% of its monthly budget, pause optional
rollouts and collect a focused incident record. At 100%, ship only fixes that
restore the affected objective or reduce security risk. Record the exact
probe/report path and the single next action in `docs/WORKLOG.md`.

## Alert runbook

Every alert has one primary owner: the GPTAdmin operator responsible for the
affected installation. The owner may hand off a deployment-specific action,
but the handoff must include the immutable probe/report path and must not
include secrets.

### Hub unavailable

- **Owner:** GPTAdmin operator.
- **Symptom:** `/healthz` fails, times out, or returns a non-200 response.
- **Diagnosis:** Run `gptadmin doctor --json`; compare `service_runtime:*`,
  `remote_health`, local version and the process/unit status. Check the Hub
  logs and listener ownership without printing environment files.
- **Recovery:** Restart the managed Hub service once, re-run the health and
  authenticated readiness probes, then use the documented backup/rollback
  procedure if the current build remains unhealthy. Do not rotate
  `AdminPassword` as a first response.

### Authentication or policy failures

- **Owner:** GPTAdmin operator/security owner.
- **Symptom:** `/admin/api/overview` is unauthorized, MCP calls return policy
  denial, or the audit stream shows an unexpected decision.
- **Diagnosis:** Verify the client/profile binding, access mode, required
  scopes and clock. Query `/admin/api/audit` by the bounded event name and
  result reference; use the arguments digest for correlation, never the raw
  request body.
- **Recovery:** Correct the profile or re-authenticate the intended client;
  use fresh sensitive-setting re-authentication for security changes. Revoke
  only the affected client/token after recording the incident evidence.

### Relay or durable job failures

- **Owner:** GPTAdmin operator/MCP transport owner.
- **Symptom:** MCP discovery works but a tool call stays queued, retries, or
  returns a transport error.
- **Diagnosis:** Compare `/admin/api/overview`, the bounded job endpoint and
  audit `mcp_enqueue`/`mcp_result` references. Check relay registration,
  heartbeat/transport mode and the target policy; do not execute shell probes
  as a health check.
- **Recovery:** Restore the managed relay/agent transport, allow the job to
  reach a terminal state or cancel it through the supported endpoint, then
  repeat the harmless `tools/list` and `demo` probes. Preserve idempotency
  keys when retrying a write-capable operation.

### Backup or failover failure

- **Owner:** GPTAdmin operator/recovery owner.
- **Symptom:** Backup verification fails, a fallback is selected unexpectedly,
  or reclaim/health fencing does not converge.
- **Diagnosis:** Run the versioned backup verifier and the failover black-box
  drill in an isolated environment. Compare node rank, lease/reclaim evidence,
  build provenance and the last known-good health probe.
- **Recovery:** Fence the unhealthy node, restore only from a verified archive,
  promote the configured fallback, and validate `/healthz`, authenticated
  overview and a safe MCP action before reclaiming the primary. If the issue
  depends on a physical deployment or Supervisor job, record it as an
  external blocker rather than changing unrelated local state.

## Evidence and review cadence

Retain aggregated probe results for the agreed operational period and review
the error budget monthly. A release or configuration change is accepted only
when the focused probe, security audit, backup/failover check and relevant
client contract evidence are linked from the worklog. Never paste raw logs or
credentials into the worklog.

