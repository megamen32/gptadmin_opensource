# Fresh Reviewer rerun: health incident autopilot

Role: Reviewer
Status: todo
Owner: fresh independent Reviewer rerun
Parent task: work-20260809-health-monitoring-orchestration.md

Read-only gate after remediation. Inspect the current selected code and run
only bounded local checks as needed. Verify specifically:

1. plan selection is atomic under two concurrent signed callbacks;
2. progress requires the selected plan and resolution requires at least one
   useful receipt plus independent matching healthy verification;
3. `/webhook-jobs/{id}/progress` GET cannot mutate state;
4. Hermes bridge fields reject secrets/raw logs and direct resolution receipts
   remain bounded;
5. the local canary output is truthful about fake versus real transports.

Use these paths only: `/home/admin/ServersAdministartion/automation/health-incident-monitor`,
`/home/admin/agents-projects/noticeplace`,
`/home/admin/gptadmin/go-hub/internal/hub`,
`/home/admin/agents-projects/agent-herder`,
`/home/admin/agents-projects/hermes-config/plugins/agent-herder-bridge`,
and `/home/admin/gptadmin/tests/e2e/health_incident_vertical_canary.py`.

Do not edit product code, secrets, runtime state, or external systems; do not
send Telegram. Append a concise PASS/BLOCKED verdict with exact evidence here.

## Independent review evidence (2026-08-09, English)

Scope reviewed: the selected health-monitor, NoticePlace, GPTAdmin Hub,
Agent-Herder, Hermes bridge, and vertical-canary paths listed above. No product
code, secrets, runtime state, external system, or Telegram transport was
changed or contacted.

Checks completed:

- `python3 tests/e2e/health_incident_vertical_canary.py` exited 0 and reported
  one incident, five source signals, three plans, useful progress `[true,
  false]`, resolved status, elapsed time, and trace refs.
- Focused local checks passed: health monitor `5 passed`, NoticePlace health/API
  and agent suite `35 passed`, Hermes bridge `9 passed`, GPTAdmin Hub `go test
  ./internal/hub` passed, and Agent-Herder `npx vitest run --root .
  tests/http-api.test.ts tests/session-cache.test.ts` passed (`6 tests`).
- GPTAdmin progress GET is method-gated at
  `go-hub/internal/hub/webhook_gateway.go:301-309`: it returns 405 before the
  job lock/update path; POST is the only path reaching
  `handleWebhookJobProgress` at lines 334-405. This contract is verified by
  code review; no state-mutating GET path was found.

Findings, ordered by severity:

1. `CHANGES_REQUIRED` — plan selection is not durable-atomic across concurrent
   signed callbacks handled by independent consumers. The signed callback
   decoder reaches `notification_center/health_workflow.py:219-226`, while
   `notification_center/core.py:1127-1147` checks the selected plan before the
   insert but has no database uniqueness/transaction guard. A bounded test with
   two `NotificationCenter` instances sharing one SQLite database accepted both
   different signed callbacks and persisted two `health.plan_selected` rows
   (`count=2`). Smallest fix: make the check-and-insert one database-serialized
   operation with a durable one-selection constraint and map the loser to the
   existing winner.

2. `CHANGES_REQUIRED` — an empty progress receipt is classified as useful and
   can satisfy the resolution gate. `notification_center/health_workflow.py:240-255`
   and `notification_center/core.py:1181-1217` allow empty step, fingerprint,
   and evidence; the first receipt is useful solely because no previous receipt
   exists. A bounded test posted that empty receipt, then an independent healthy
   verification, and the incident resolved. Smallest fix: reject an empty
   receipt or require a non-empty step/fingerprint/evidence before marking it
   useful.

3. `CHANGES_REQUIRED` — matching verification loses the original health
   fingerprint. `health_workflow.py:48-94` normalizes source/host/type but drops
   the producer's nested `health.fingerprint`; the optional matching check in
   `notification_center/core.py:1330-1358` therefore falls back to source-only.
   A bounded test with original fingerprint `fp-bad` and a healthy verification
   carrying a different fingerprint resolved successfully. Smallest fix:
   preserve `source_fingerprint` through normalization/delivery and require it
   when the original signal supplied one.

4. `CHANGES_REQUIRED` — a signed callback can select a plan before any
   three-plan attachment. `notification_center/core.py:1035-1048` falls back to
   the built-in `observe/repair/verify` IDs when `health.plans_attached` is
   absent. A bounded callback test selected `repair` while
   `latest_health_plans()` was still empty. Smallest fix: reject selection until
   one validated exactly-three plan bundle exists.

5. `CHANGES_REQUIRED` — Hermes direct resolution receipts are not bounded in
   their session actor. `plugins/agent-herder-bridge/__init__.py:161-191`
   copies `session_id` into `actor`, and `record_health_resolution` passes it
   from lines 289-300 without a bound. A patched local transport captured a
   10,000-character session ID in a 10,093-byte direct resolution body. The
   same `_opaque_value` allowlist at lines 83-87 accepts secret-like
   `token:value` strings in health fields because `:` is allowed. Smallest fix:
   validate/bound actor/session identity, reject secret-key prefixes as well as
   `key=value`, and enforce a final bounded serialized receipt size.

6. `CHANGES_REQUIRED` — the canary source is explicit that GPTAdmin,
   OmniRoute, Agent-Herder, and Telegram are fakes at
   `tests/e2e/health_incident_vertical_canary.py:1-6`, but stdout returned at
   line 177 has no fake/real transport provenance. Its green result cannot be
   mistaken for live downstream delivery from the output alone. Smallest fix:
   include a `transports` map (real local NoticePlace/health producer,
   deterministic fakes for the four downstream transports) and
   `external_sends: false` in the emitted result.

Verdict: `CHANGES_REQUIRED` (review gate blocked pending Worker fixes). The
focused tests and disposable canary are green, but they do not cover the
cross-consumer selection race, empty useful receipt, fingerprint mismatch,
pre-attachment selection, unbounded Hermes actor, or canary provenance gap.
