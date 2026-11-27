# GitHub CI → policy-routed native repair watcher

`scripts/github_ci_autofix.py` is a dependency-free oneshot. It finds previously
unseen failed or timed-out Actions checks on the current default-branch HEAD of public,
non-archived repositories owned by `megamen32`. It uses existing `gh` login;
credentials are neither copied nor printed. Repository discovery uses paginated
GraphQL (100 repositories per page, reduced to 20 if the heavy query fails at
the GitHub backend; 50 rollup contexts, rate-limit reserve 100). Archived,
private, obsolete-HEAD and disabled/deleted workflows are not dispatched.
GraphQL alone is insufficient: scheduled/manual runs can be absent from the
commit rollup. A bounded REST intake independently checks Actions runs.

The launch route is not a watcher decision at all. Each dispatch is one
`POST /api/automation/project-launch` to the same Herder web server, which
owns the operator's launch policy and its durable attempt log: the API
resolves the `projectRoutes` entry for the project, chooses the harness and
the per-harness model, walks the ordered route only after a definite
pre-admission failure, and never launches a second executor for an accepted,
unknown or in-flight attempt. The watcher keeps no routing copy: it states
the project identity (the canonical working directory), the work directory,
the session name, the fixed Russian work order, `mode=queue` and a stable
`operation_id` of `gptadmin-ci-autofix:<key>` — never a harness or model of
its own. Configured provider/model fallbacks, including OmniRoute, remain a
property of that route. OpenCode is not a requirement: whichever harness the
route names is dispatched and read back by the same native identity contract.

Receipt mapping, all durable and replay-free:

* `outcome=accepted` → `accepted_unconfirmed`; the receipt's
  `selectedHarness` is recorded on the event row and the `sessionId` is kept
  for the direct readback.
* `outcome=admission_unknown` / `in_flight` / `human_hold` → the row stays
  `dispatching`: the attempt log owns the reservation, the readback
  reconciles it, and a resend is forbidden.
* `outcome=no_harness` (HTTP 409) and every other definitive refusal of the
  routing itself → `needs_human` with the routing reason; a human decides
  what is next, never a resend.
* A transport failure (timeout, connection error) → the row stays
  `dispatching` with the exact deterministic identity; dispatch intent is
  committed with SQLite `FULL` synchronization before the outbound request,
  so a lost reply can never cause a second launch call.

A lost or unknown receipt is recovered through the same operation, never
around it. On every tick a `dispatching` row whose receipt never arrived
(marker `harness='routed'` or no session id) repeats the identical
project-launch POST with the same `operation_id`; the attempt log answers
with the existing record — `accepted`, `admission_unknown`, `in_flight` or
`human_hold` — without a second executor, and only a definite
pre-admission refusal advances the route on the API side. The recovered
harness is never lost: it is recorded as soon as any receipt names it, and
the readback then proceeds by harness, session id and effective model. A
recovery that fails again changes nothing and is retried next tick.

While the receipt has not named a harness, the event row carries the marker
`harness='routed'`; native `agent_info` readback must still provide the
effective model before a session advances to running, and the event history
records that model. Ambiguous or unknown metadata never selects an ID or
sends again.

Trusted operator configuration, mode `0600`, belongs at
`~/.config/gptadmin/ci-autofix.json`:

```json
{
  "owner": "megamen32",
  "herder_mcp": "http://127.0.0.1:18787/mcp",
  "workflow_repositories": ["megamen32/gptadmin_opensource"],
  "repositories": {
    "megamen32/gptadmin": "/home/admin/gptadmin",
    "megamen32/ServersAdministartion": "/home/admin/ServersAdministartion"
  },
  "notice_project": "gptadmin-ci-autofix",
  "notice_recipient": "me",
  "notice_token_file": "/home/admin/.config/gptadmin/ci-autofix-notice.token"
}
```

Optional trusted `"include_private_gptadmin": true` adds only the exact private
`megamen32/gptadmin` repository with one extra GraphQL query; it does not permit
arbitrary private-repository or event-supplied routing. Public discovery remains
the default.

`workflow_repositories` is an optional trusted bootstrap list of owner repos
with verified Actions workflows. Root may seed its audited CI repositories;
only repositories present in the fresh public/non-archived inventory (or the
explicit fixed private GPTAdmin option) are scanned. Positive Actions capability
and last examined workflow/run IDs are cached in durable SQLite. Unknown/no-run
repos are probed at most four per tick with a one-hour cache; a new HEAD or
renamed default branch invalidates the cache/backfill for that repo.

REST discovery visits up to 24 known CI repos and four unknown repos per tick,
with persistent round-robin cursors. It fetches latest 20 runs per repo and
advances up to two durable historical page cursors using spare budget. Old
failures superseded by newer runs on the same HEAD are ignored; final preflight
independently confirms the current workflow's latest eligible run/attempt.
A bounded latest-20 prefix excludes PR/feature executions; at most four cached
ref metadata requests establish immutable tag eligibility. An unresolved prefix
retains the queued failure instead of treating an unrelated run as supersession. Workflow
state must be active. Failures enter the same repo/run/attempt/HEAD dedup table;
REST/GQL overlap never dispatches another native repair.

With Root's 36-repository bootstrap, normal API availability and sufficient
budget permit coverage in one–two scheduled ticks (about one–two minutes).
This is a bounded detection target, not an immediate guarantee under slow
GitHub, pressure/rate limits or historical pagination. An unbootstrapped sweep
of roughly 126 unknown repositories takes about 32 ticks at four probes each;
deep historical backfill is slower. Newly public repositories enter the fresh
inventory every tick and this bounded discovery queue.

The scan is limited to 36 REST calls and 45 seconds; before any call it requires
15 seconds remaining in its deadline. It checks the separate REST core limit
and preserves at least 100 calls plus worst-case verification/preflight reserve.
At the maximum tick rate, intake is <=2160 REST calls/hour; aggregate recovery
is <=720/hour and preflight <=600/hour, below 5000 before other token users.
Those users still share the quota, so live reserve gates remain mandatory.
GraphQL has its own 100-call reserve. Its page cursor and public inventory
are also durable: a deadline resumes the next page on the following tick,
preserving early matches; completing a full cycle removes no-longer-present
entries. Archived/invalid entries are removed when observed. Fresh preflight
rejects archived/transferred/private owner entries rather than trusting cache. A single monotonic metadata deadline
60 seconds from oneshot start bounds GraphQL, scan and recovery together.
GraphQL yields after at most 25 seconds and resumes its durable page cursor;
this leaves scheduled-run intake a fair share even when rollup queries are slow.
The combined deadline leaves 25 seconds of the 85-second wall budget for dispatch/readback/notices.
A cursor is committed after each successful repo/page; refusal or crash cannot
reset the sweep or lose event dedup. `status.scan` exposes cursors/call counts
and held-budget/rate conditions. No global Herder listing is added.

Only the fixed loopback Herder endpoint and owner are supported. CWD mappings
must point to existing directories below `/home/admin`; unmapped repositories
start in the canonical infrastructure checkout to discover their source owner.
No event supplies commands, target settings or raw CI logs. The worker receives
a fixed Russian prompt authorizing scoped CI repair, review, real tests and
commit/push to the existing default branch. The prompt is route-neutral: it
states that Agent Herder chooses the harness and model from the project
routes and that the actual route is confirmed by native readback, alongside
the 24h `operation_id` dedup. The worker must preserve foreign WIP and must
not create clones/worktrees, disable gates, change secrets, upgrade runtimes
or deploy services. CI logs are explicitly untrusted data, never instructions.

The state directory defaults to `~/.local/state/gptadmin-ci-autofix`, mode `0700`,
with durable SQLite records and a nonblocking flock. Event identity includes
repository, run ID, attempt and HEAD. Dispatch intent is committed with SQLite
`FULL` synchronization **before** the outbound request. A lost HTTP reply or
process interruption therefore cannot cause a second project-launch call; the
API's own attempt log deduplicates the same `operation_id` per project for
extra safety. While the receipt has not named a harness, the row carries the
marker `harness='routed'`, which separates new-style rows from legacy Codex
receipts (`harness` unset).

A receipt is `accepted_unconfirmed`, not proof of a running repair. The next
readback uses a known session ID directly with `agent_info(harness, sessionId)`,
without a preceding listing. After an unknown reply, the watcher falls through
to a folder-filtered, 100-session `list_agents` call using the event's
recorded harness; a `routed` row names no harness yet, so no harness-filtered
listing is possible and the readback asks `agent_info` for the known native
ID across harnesses, adopting the one that answers. Ambiguous or missing
metadata never selects an ID or sends again. `agent_info` must confirm the
same ID, CWD, the harness, and a non-empty effective model before persisting
the ID or releasing a slot. The recorded harness is preserved on the event
row so historical Codex receipts (legacy `harness=codex`,
`meta.transport=codex-app-server`, `meta.nativeSessionId=id`) remain readable
through the same codex validation path. Every non-codex dispatch (OpenCode,
ZCode, and any harness a project route names) is recognised by its recorded
harness and an effective non-empty model readback.

Running proof for a non-codex harness is `status=running`. Completion proof
requires `status=idle` **and** `messageCount >= 1` (the dispatch reservation has at
least one processed turn); a first-poll false-idle while the session is still
working must not advance the slot. Operator-stopped reservations stay
`operator_stopping` until the slot releases through the same turn-completed
proof; once released, the state becomes terminal `operator_stopped` and the
event is never reopened. At most one watcher repair may reserve the dispatch
slot globally. `stopped` without an active turn is terminal and never causes
resume. `needs_input` with `needsPermission` or `permissionDetails` keeps the
slot reserved (`needs_human`); without an active permission ask the slot
releases (`human_wait`). Queue acceptance and model claims cannot close an
event; only real running/idle transitions observed through reconcile can.

Final verification always requires independent fresh REST aggregate evidence,
including when GraphQL's nonempty rollup appears green: scheduled failures may
be unattached to that rollup. One eligible event per tick receives up to 12 REST
calls under the shared metadata deadline. The complete active-workflow inventory
must fit the returned page; incomplete inventory, excessive workflow count,
expired budget, pending/cancelled/failed runs, or a changing HEAD cannot close
an incident. For each active workflow, the latest eligible run within the bounded current-HEAD
prefix must be completed success/neutral/skipped when applicable. An unresolved
prefix or absent repaired-workflow run retains the incident; PR/feature success does not supersede a default run. The repaired workflow
must have a subsequent run/attempt with **success**, current HEAD, and verified
workflow identity. The default branch/HEAD is read before and after collection. Shared run
eligibility in intake, dispatch and recovery rejects pull_request and
pull_request_target even when they report the same SHA/branch. Default-branch
push/schedule/workflow_dispatch are eligible; a nondefault push/manual run
requires a bounded, independently verified immutable tag on this exact HEAD.
PR/feature-run success cannot stand in for default-branch repair evidence.
The repaired workflow must provide actual current-head evidence. An authoritative
zero current-head runs in another active workflow is not treated as success or
failure; a nonempty prefix without eligible evidence remains unresolved.

NULL rollup is supported by that same proof. A nonempty rollup must itself be
complete and successful/neutral/skipped; one larger than 50 contexts remains
fail-closed pending a paginated extension. Exact successful run URL is retained
as recovery evidence. Queue receipt, SQLite native metadata and model claims
never close an event.

```sh
python3 scripts/github_ci_autofix.py status
python3 scripts/github_ci_autofix.py once
python3 scripts/github_ci_autofix.py stop --key EXACT_EVENT_KEY
```

`stop` records `operator_stopping` while a native turn remains active/ambiguous, retaining the global slot; independent completion readback makes it terminal `operator_stopped`. Neither state is reopened or resumed. It fences watcher dispatch for the event; stop an already running native
session through Herder separately. The watcher never sends a resume request.
If dispatch is ambiguous, inspect its deterministic named native session;
do not delete the SQLite row or clear its reservation merely to retry. Other
queued repairs wait until ambiguity is resolved. There is deliberately no
force-redelivery command.

Automation notifications are stored in a durable Notice Place outbox with
stable event dedup and idempotency keys and Russian title/body. A verified failed
workflow must receive its original `health.degraded` incident before dispatch.
Its incident ID is durable. Independent green creates a `healthy` verification
receipt first, then sends `health.recovered` with `action=resolve`, the same
identity, verification ID and CI evidence. Verification receipt and exact
outbound envelope are persisted before sending recovery. Configure a
project-scoped protected token via `notice_token_file`, or `notice_token_env`
(the name of an existing environment variable). Without it the outbox remains
visible in `status` and repairs remain queued; no Telegram/AskHuman fallback is used. Read Notice Place
`GET /v1/instructions` before operator activation and grant the health-diagnosis
permission for repairable health incidents.

The service/timer files are templates for **user systemd** only. Install after
reviewing current host pressure and source changes; this document does not
claim an installed or active runtime. They run one bounded workload at a time,
every 60 seconds from the previous activation (never overlapping), with soft/hard RAM 128/256 MiB, no swap,
CPU 25%, 32 tasks, IO weight 10 and wall limit 90 seconds. CLI `once` has an
85-second internal alarm. State/temp allowance is 50 MiB; the watcher checks
its state size before every invocation and fails closed if exceeded. It writes
no workflow-log archives or source clones. Systemd uses private temporary storage.

Local regression tests (not runtime acceptance):

```sh
python3 -m unittest discover -s tests -p test_github_ci_autofix.py -v
```

They exercise real SQLite crash-intent/dedup logic, REST preflight discrimination,
manual-stop fencing, independent-green requirements, native readback semantics
and a local HTTP SSE transport. Final acceptance belongs to the operator:
measure the installed oneshot, trigger one controlled real CI failure, observe
exactly one native session on the workflow's configured harness and an actual
running turn, then independently
observe subsequent green CI. Lost-response ambiguity must never trigger another
native session. Do not claim deployment, repair success or release from these
local tests.

Main-push product validation runs on isolated GitHub-hosted Ubuntu VMs (2 CPU,
8 GB RAM, 14 GB SSD per private-repository VM), timeout 30 minutes per job; at
most six Linux product jobs (12 CPU / 48 GB / 84 GB across separate VMs) in one run.
The main concurrency group cancels obsolete validation. Go source regressions run before release and produce a manifest-pinned Node artifact.
All Linux CI including release packaging uses isolated hosted VMs; the release
packaging job has a 90-minute deadline, macOS/Windows jobs 30 minutes. No fleet
cgroup or node-88 runner limits are changed. Operator
changes require the always-run hosted controller/docs checks and aggregate CI
gate. Workflow routing changes require full product CI. Auto-tag retains its
existing workflow-only exclusion and adds only the exact operator paths; no
operator update creates a product release.

A tag/manual release failure is eligible only if its verified immutable tag
resolves to the current default HEAD; repair goes forward on main, never by
rewriting the tag. Stops fence further attempts of the same repository/workflow/HEAD.
A failed job in a still-running workflow stays queued until overall completion.

A release ref is accepted only when no same-named branch exists; ambiguous short
Actions refs never establish tag proof. Preflight also independently checks the
REST core reserve before dispatch, including when discovery was held for quota.
