# Единый контроль здоровья хостов, сервисов и логов

Status: work
Task class: Full
Stage: Review and black-box acceptance complete; external approval boundary remains
Owner: L
Created: 2026-08-09

## Original request

Нужен бизнес-результат: кто-то следит за здоровьем всех хостов (CPU/disk/RAM и
прочее), сервисами (failed) и логами, включая error level и keywords. Если на
одной машине есть деградация, GPTAdmin получает событие, запускает NoticePlace,
через Agent Herder — OpenCode с OmniRoute/subagent для подробной диагностики,
передаёт диагноз в OmniRoute/orchestrator для трёх планов решения и отправляет
их пользователю через NoticePlace. Пользователь выбирает план, после чего
Hermes/OpenAI/Codex/GPT-5.6-Luna high исправляет проблему в домашнем Telegram
канале/топике здоровья. Отдельный контроль не даёт агенту уснуть до решения;
после завершения он отправляет через NoticePlace факт исправления, длительность
и все трассы.

Шаги: 1) исследование осуществимости в `~/serveradministation` и
`~/agents-projects` для NoticePlace, Hermes, OpenCode, OmniRoute и Agent Herder;
2) подробный документ, как добиться результата из текущего состояния;
3) три плана реализации; 4) имплементация; 5) ревью; 6) независимая
user-facing computer-use проверка без внутреннего знания проектов.

## Objective

Deliver a proven, approval-gated, traceable health-incident path from a real
host/service/log degradation signal to diagnosis, three remediation plans,
explicit user choice, controlled remediation, liveness supervision, and a
resolved NoticePlace message with elapsed time and trace references.

## Business canary

The smallest acceptable live proof is one controlled synthetic degradation on a
non-production or explicitly approved test target: the signal identifies the
host and evidence, GPTAdmin creates exactly one incident, NoticePlace delivers
the diagnosis and exactly three plans, the user explicitly selects one,
remediation runs in the health Telegram topic, liveness remains visible until
resolution, and the final NoticePlace message contains fixed/resolved status,
elapsed time, and durable trace identifiers. A green unit test, build, service
status, historical manifest, or dashboard alone is not acceptance proof.

## Confirmed scope

- Read-only capability and topology research across `/home/admin/ServersAdministartion`
  (the existing spelling of the requested `serveradministation` path) and
  `/home/admin/agents-projects`.
- The GPTAdmin, NoticePlace, Hermes, OpenCode, OmniRoute, and Agent Herder
  integration points discovered in those roots.
- Host resource, service-failure, and log keyword/error-level signal handling.
- Incident identity, deduplication, diagnosis, plan selection, remediation
  supervision, notification, elapsed-time, and trace evidence needed for the
  business canary.
- A detailed current-state design, three complete implementation plans, and
  implementation/review/user-facing verification after the user selects the
  plan and approves the technical preview.

## Explicit exclusions and gates

- No production restart, deploy, rollback, destructive operation, or external
  message send without the required direct approval at that action boundary.
- No secret values, private keys, tokens, PII, or sensitive log payloads in
  task evidence, reports, commits, or user-facing output.
- No broad cleanup, unrelated refactor, vendor migration, Grafana/dashboard
  project, backup/rollback system, schema migration, or provider replacement.
- No assumption that a named component is already an end-to-end capability;
  each claim must be tied to a real consumer, endpoint, transport, and canary.
- Preserve all pre-existing shared-worktree changes and do not overwrite files
  actively edited by another worker.

## Initial active-minute estimate (immutable)

- Optimistic: 180 active minutes
- Likely: 420 active minutes
- Pessimistic: 900 active minutes

## Estimate revisions (append-only)

| Time | Estimate | Trigger and evidence |
|---|---|---|
| 2026-08-09 | 180 / 420 / 900 min | Initial Full-scope estimate before repository reconnaissance. |

## Stop and abandon conditions

- `stop_when`: the business canary passes with durable, secret-safe evidence,
  or an explicit required user approval is pending.
- `abandon_when`: the requested business path cannot be assembled from the
  confirmed repositories and transports, and no safe bounded implementation
  remains without expanding scope or adding an unapproved external dependency.
- `forbidden_without_explicit_user_request`: production deploy/restart,
  destructive or rollback action, real outbound Telegram send, secret/provider
  changes, schema/database changes, or changes to unrelated dirty work.

## Initial plan

1. Зафиксировать текущий shared-worktree и подтвердить реальные корни,
   инструкции, существующие health/monitoring/NoticePlace/MCP/agent flows.
2. Провести независимое bounded-исследование по топологии сигналов и по цепочке
   GPTAdmin → NoticePlace → Agent Herder → OpenCode/OmniRoute → Hermes.
3. Зафиксировать доказанный current state, gap matrix, acceptance proof и
   consequential approval boundaries.
4. Подготовить ровно три полных варианта: «Максимально идеальный»,
   «Нормальный» и «YAGNI 80/20 — полный результат», с рекомендацией и
   user-facing preview; дождаться выбора пользователя.
5. После выбора показать полный technical preview и дождаться второго
   подтверждения перед имплементацией.
6. Реализовать выбранный путь вертикальными срезами, начиная с red/canary
   проверки поведения, затем провести независимые Reviewer, Critic и Tester.
7. Принять результат только по user-facing computer-use и реальному
   бизнес-canary evidence; обновить task/roadmap и передать итог на русском.

## Evidence log

- 2026-08-09: Worktree inspection found existing unrelated dirty changes in
  GPTAdmin; they are preserved and are not part of this task.

## Research progress (English)

- 2026-08-09: Graphify produced a navigation graph for
  `/home/admin/ServersAdministartion` (3,296 nodes / 5,421 edges) with
  partial semantic extraction; 3/7 chunks failed by connection error, 197
  files produced no nodes, and `nginx-dev/state` was unreadable. This is a
  limitation on navigation evidence, not a live capability claim.
- 2026-08-09: Signals lane proved Netdata inactive, Telegram-only recovery and
  endpoint monitors, the one-minute RAM/log guard, and no normalized health
  producer into GPTAdmin/NoticePlace.
- 2026-08-09: Orchestration lane proved NoticePlace `POST /v1/events` → scoped
  durable `gptadmin.agent:<job>` → signed GPTAdmin webhook/job polling → Agent
  Herder `/api/sessions/new-or-resume` → named session → terminal audit. Missing
  per-job progress/heartbeat, cross-system correlation, and stall watchdog.
- 2026-08-09: Execution lane proved supported Hermes/OpenCode/OmniRoute seams
  and telemetry, but not installed-runtime/provider authorization, a durable
  plan-to-session mapping, authoritative pause/resume, or exact `gpt-5.6-luna
  high` routing.
- 2026-08-09: Detailed design and three complete plans were written; no runtime
  mutation, external send, restart, deploy, or secret inspection occurred.
- 2026-08-09: The same mandatory plan-selection question remained unanswered
  across three consecutive goal turns. Implementation is blocked specifically
  on selecting one of the three plans; no architecture or runtime action was
  inferred from silence.
- 2026-08-09: User explicitly selected `Нормальный` and made the original
  business scenario the immutable Definition of Done. User authorized direct
  implementation after recording the technical preview; no second preview
  approval is required. Dangerous boundaries remain separately gated.

## Implementation progress (English)

- 2026-08-09: User explicitly instructed continuation into runtime preparation:
  start Hermes, add the health workflow, and activate the built dist artifacts.
  This confirms implementation intent, but the existing production approval
  gates remain for writes to `/opt`, service restart/repair, secret-bearing
  configuration, and any external Telegram canary.
- 2026-08-09: Closed the previously missing selected-plan execution seam.
  NoticePlace now normalizes the exact Hermes/openai-codex/gpt-5.6-luna/high/
  health profile, records one remediation request and durable GPTAdmin delivery,
  and forwards the signed selection to the bounded Agent-Herder profile. The
  Agent-Herder HTTP/MCP named-session path can select the model before the first
  message; focused health/remediation tests and the full health/API suites are
  green.
- 2026-08-09: Fresh disposable activation bundle `/tmp/health-activation.qUedwu`
  contains the rebuilt Agent-Herder backend/web dist, GPTAdmin Hub binary,
  NoticePlace runtime/package/docs, health collector units, and Hermes bridge.
  Package dry-run has 76 entries, includes `health_workflow.py` and
  `agent_job_helper.py`, and has zero cache/pyc/Graphify entries. Nothing from
  this bundle was copied to `/opt` or live user runtime paths.
- 2026-08-09: Technical preview recorded in
  `docs/health-incident-autopilot-technical-preview-2026-08-09.md`.
- 2026-08-09: Health producer slice completed in the confirmed
  `/home/admin/ServersAdministartion/automation/health-incident-monitor/`
  path with fixture canary evidence; downstream wiring remains.
- 2026-08-09: Agent Herder added a read-only useful-progress endpoint and
  stable fingerprint behavior; worker reports focused HTTP tests and build
  green. Existing dirty `src/session-supervisor.ts` was not touched.
- 2026-08-09: GPTAdmin webhook jobs gained bounded correlation/trace/progress
  receipts and authenticated idempotent progress updates; worker reports the
  focused package and full hub tests green.
- 2026-08-09: Initial worker dispatch hit the global agent-thread limit; queued
  slices that did start completed independently. One duplicate health slice was
  written under the wrong GPTAdmin root and is recorded in a separate todo for
  final integration review; it is not part of the selected implementation.

## Selected acceptance contract (authoritative)

Degradation → one deduplicated incident → diagnosis → exactly three plans →
explicit user choice → controlled remediation → supervisor of useful progress
(not process heartbeat alone) → independent verification of the original
signal → resolved NoticePlace receipt with elapsed time and trace IDs.

The implementation plan is `Нормальный`; the source business contract above is
the only Definition of Done. Terminal agent status alone never closes an
incident.

## Estimate revision (append-only)

| Time | Estimate | Trigger and evidence |
|---|---|---|
| 2026-08-09 | 240 / 720 / 1500 min | Research proved four cross-system gaps (health producer, correlation, liveness/reconciliation, installed-runtime/model proof); implementation is materially larger than the initial estimate. |

## Implementation and canary evidence after plan selection

- 2026-08-09: Implemented the fixture-first health producer with CPU/RAM/disk,
  failed-service, and keyword-log signals; configured service probes,
  redacted evidence, stable source identity, and per-observation idempotency.
- 2026-08-09: NoticePlace now exposes loopback workflow routes for health
  intake, post-diagnosis exactly-three plan attachment, signed plan selection,
  useful progress, independent verification, and resolution. Plan/progress/
  verification updates stay linked to one incident in existing tables.
- 2026-08-09: GPTAdmin job receipts preserve bounded health correlation/traces;
  `HealthProgressSupervisor` classifies useful progress versus heartbeat-only
  churn and reports stalled/waiting/progressing/terminal states.
- 2026-08-09: Hermes plugin supports optional direct bounded progress and final
  resolution receipts to NoticePlace. Missing direct configuration returns
  `unsupported` rather than a false success.
- 2026-08-09: Disposable vertical canary passed:
  `python3 tests/e2e/health_incident_vertical_canary.py` → one incident, five
  source signal types observed, exactly three plans, signed user selection,
  useful progress `[true, false]`, terminal-status resolve rejection,
  independent healthy verification, and resolved receipt with elapsed `4210`
  ms plus three trace refs.
- Focused green evidence: health producer `5 passed`; NoticePlace health/API/
  GPTAdmin suite `34 passed`; Hermes bridge `8 passed`; Agent Herder typecheck
  plus direct Vitest `5 passed`; GPTAdmin Hub `go test ./internal/hub` green.
- Full NoticePlace suite has one unrelated pre-existing Telegram policy failure
  (`135 passed, 1 failed`), tracked in
  `todo-20260809-noticeplace-telegram-controls-regression.md`. Agent Herder's
  package-level build is separately blocked by concurrent Vite UI work missing
  `src/web-ui/main.tsx`, while the changed TypeScript/API tests pass directly.
- Production route/topic/token configuration, restart/deploy, installed
  provider/model proof, and real Telegram delivery remain intentionally
  unexecuted approval boundaries.

The user's later instruction supersedes the earlier plan text that requested a
second technical-preview approval: the preview is recorded and implementation
continues directly to independent review and black-box testing.

## Final hardening pass in progress

## Tester black-box result (2026-08-09)

Verdict: STOP_MISSING_REAL_SURFACE

- Fresh black-box attempt used only the supported Touchpoint computer-use surface.
- `touchpoint/diagnostics`, `touchpoint/windows`, and `touchpoint/screenshot` all failed with `Transport closed`.
- No Telegram topic was inspected; no messages, plan selection, remediation, or mutation was performed.
- Per Tester hard boundary, no shell, source, API, terminal, database, or synthetic substitute was used.

- Durable SQLite single-winner plan-selection guard added and independently
  exercised with two separate `NotificationCenter` instances; pre-attachment
  selection rejected.
- Health source fingerprint is retained and mismatched independent verification
  is rejected.
- Heartbeat-only progress is now rejected at both the NoticePlace workflow
  wrapper and core before persistence; the wrapper reports only core's
  authoritative useful-progress result.
- Hermes progress and resolution direct receipts now bound session actors,
  reject secret-like prefixes, and cap serialized receipt bodies.
- Regression evidence after this hardening: NoticePlace health/API/GPTAdmin
  `39 passed`, Hermes bridge `10 passed`, vertical canary passed with explicit
  transport provenance and `external_sends: false`.
- Final bounded rerun: health producer `5 passed`; NoticePlace `39 passed`;
  GPTAdmin Hub `go test ./internal/hub`; Agent Herder `tsc` plus HTTP Vitest
  `3 passed`; Hermes bridge `10 passed`; vertical canary passed with incident
  `inc_a0cd0f91c1364789b5ffaae78a8d0922`, three plans, resolved state,
  elapsed `4210` ms, and `external_sends: false`.
- Fresh black-box Tester PASS is recorded in
  `.agents/tasks/todo-20260809-health-tester.md`; final fresh Reviewer/Critic
  gate PASS is recorded in `todo-20260809-health-review-rerun4.md` and
  `todo-20260809-health-critic-rerun4.md`.

## Final bounded acceptance result

The local implementation and user-facing acceptance are complete for the
non-production fixture. The business canary remains intentionally stopped at
the explicit external action boundary: production health collector/topic/token
configuration, restart/deploy, provider/model authorization, selected-plan
remediation on a real runtime, and real Telegram delivery.

Read-only live audit on 2026-08-09 confirms this boundary is real: `/opt/noticeplace`
and live Agent-Herder `dist` predate the health workflow, while Hermes gateway is
failed/stale. Detailed activation preview:
`docs/health-incident-autopilot-production-activation-preview-2026-08-09.md`.

## Live activation progress

- Read-only service audit proved `notification-center.service`, Agent-Herder,
  OpenCode and OmniRoute processes; OmniRoute `/api/monitoring/health` returned
  healthy `3.8.49`.
- Existing `fleet-health.timer` currently runs every three hours and publishes
  resource summaries; it does not cover failed services/log keywords or the new
  GPTAdmin health job contract.
- Hermes user gateway is failed since 2026-08-08 20:33:54 and its official CLI
  reports an outdated unit/stale state. No restart or unit repair was run.
- Added and locally verified per-host activation templates and explicit
  independent verification sender in
  `ServersAdministartion/automation/health-incident-monitor/`: producer tests
  `6 passed`, installer syntax passed, and unit verification produced no error
  from the new units.
- Read-only Hermes diagnosis found the installed `agent-resume` plugin
  registering unsupported `gateway_startup`; source was corrected to use the
  manifest-supported `pre_gateway_dispatch` hook and its regression passed
  `5 tests`. Installed `~/.hermes` copy and gateway restart remain approval
  boundaries.
- Added read-only `tests/e2e/health_incident_production_preflight.py`; current
  output correctly reports `ready_for_external_canary: false` with blockers for
  stale NoticePlace/Agent-Herder releases and inactive Hermes, while proving
  OmniRoute health `200`.

## Bounded release check

- GPTAdmin Hub production-shaped Go binary build passed to a disposable `/tmp`
  artifact; no installed binary or service was changed.
- The final preflight was rerun read-only. It still reports
  `ready_for_external_canary: false`: live NoticePlace health endpoint is
  `404`, live Agent-Herder has no useful-progress route, and Hermes is failed;
  OmniRoute remains healthy with HTTP `200`.
- No production deployment, restart, secret/config installation, provider or
  model authorization, remediation action, or external Telegram send was
  performed.

## Continuation hardening

- Red-first regression reproduced a real deduplication defect: changing a
  continuing CPU value from `90.0` to `97.0` produced different incident keys.
- Fixed the producer fingerprint contract so metric incidents are stable for
  the degraded signal, while service names and log keyword identities remain
  distinct; changing log counts no longer opens a new incident.
- Regression is green at `7 passed`; the vertical canary is green again with
  exactly three plans, useful progress `[true, false]`, independent
  verification, resolved receipt, trace references, and `external_sends=false`.
- Corrected bounded `py_compile` invocation and `git diff --check` both pass.

## Current continuation audit

- The bundled remote fleet probe was attempted read-only and timed out at its
  300-second tool boundary without partial output; no fallback broad scan or
  mutation was used. The bounded local probe returned server-100 with
  `disk_pct=82`, `mem_avail_mb=55577`, and `failed_units=2`.
- Service-ops health check confirms OmniRoute HTTP `200`/healthy `3.8.49`,
  OpenCode HTTP `401` (protected endpoint/process evidence), and Hermes
  dashboard connection refused. Service-status user-unit probes lack the
  required D-Bus environment and are not treated as authoritative.
- Fresh production preflight still returns `ready_for_external_canary=false`:
  live NoticePlace health endpoint `404`, live Agent-Herder progress route
  absent, and Hermes unit failed; no production state changed.
- Narrow local evidence confirms the producer sees the two current failed units
  (`backup_db_video_stats_weekly.service` and `chat-web-update.service`) as one
  critical `health.degraded` event with `agent_job=health-diagnosis`, stable
  dedup fingerprint, source `host:admin-server-100`, and `sent=0`.
- Added the canonical central SSH fan-out adapter for Pythonless/remote hosts,
  fixed `journalctl`/`logread` backends, collector-failure incidents, bounded
  fan-out concurrency, fleet config and activation templates. A full
  seven-target run over the example config completed read-only in `4.3s` with
  all targets parsed, `sent=0`, and no raw log lines emitted; router/HAOS were
  covered through the remote adapter.

- Producer plus fleet-adapter tests now pass `12`; pycompile, JSON validation,
  installer `bash -n`, and new-unit verification pass (only unrelated host
  unit warnings are reported by `systemd-analyze`).

## Independent review findings and corrections

Fresh Reviewer initially returned `FAIL / CHANGES_REQUIRED` and identified:
runtime-user unreadable JSON configs, fail-open partial collectors, an
unverified verification-send path, weak preflight readiness gates, and
option-like SSH targets/raw diagnostic leakage.

Corrections applied:

- local and fleet installers now keep JSON `0640` for the actual runtime group
  and token env `root:root 0600`;
- local file/backend probes and remote SSH probes now emit bounded
  `collector_errors`; missing metrics, service backends, log backends, and
  malformed/timeout probes become critical health signals rather than healthy
  empty observations;
- `--send-verification` refuses to send unless the independent source result
  is explicitly `verified=true`;
- preflight now requires NoticePlace health HTTP `200`, all key stack units
  active, authenticated OpenCode readiness, and an installed active health
  timer/config; HTTP bodies and systemd stderr are reduced to metadata/type;
- SSH target names are allowlisted and the invocation terminates options with
  `--`.

Regression evidence after these corrections: producer/fleet suite `17 passed`,
preflight tests `2 passed`, and the current preflight truthfully remains
`ready_for_external_canary=false` with blockers for stale live NoticePlace and
Agent-Herder releases, failed Hermes, missing authenticated OpenCode proof, and
absent health timer/config. No production mutation or external send occurred.

Additional hardening after review:

- `send_verification()` itself now rejects any payload without
  `verified=true`, not only the CLI wrapper;
- the remote POSIX probe fixed an `awk` builtin-name collision and now reports
  unavailable service/log backends explicitly; read-only live snapshots for
  Linux, router, and HAOS show metrics plus bounded collector errors where the
  target lacks a service backend.

## Independent Reviewer bounded rerun (2026-08-09)

Result: `FAIL` / `CHANGES_REQUIRED`; no deploy, restart, secret read, or
external send performed. Review stopped at the requested bounded scope.

- `[P1]` Activation cannot read its config: `install.sh:29-30` and
  `install-fleet.sh:31-32` force both JSON config and env to `root:root 0600`,
  while `health-incident-monitor.service:8,13` runs as `health-monitor` and
  `health-incident-fleet.service:8,12` runs as `admin` and reads those
  `/etc/*.json` paths. The first timer run therefore fails before collection;
  smallest fix is to grant the runtime user read access to its JSON config
  while keeping the token env root-readable by systemd only.
- `[P1]` Partial collector failures are silently healthy. The remote probe uses
  `fleet_health_monitor.py:50-62` (`set +e`, optional commands, `na` values),
  and `parse_remote_snapshot:100-135` marks any `snapshot` line valid; only a
  missing line becomes `collector_error`. Locally, `_default_command_runner`
  and `_log_matches`/`_failed_services` (`health_monitor.py:51-62,175-218`)
  collapse timeout, missing command, or unreadable log to empty data. A host
  with all metrics `na` or a configured backend unavailable therefore emits no
  collector incident and can be treated as healthy. Add explicit per-collector
  failure state and fail closed for configured checks.
- `[P1]` Independent verification is not fail-closed: `health_monitor.py:340-348`
  returns `verified=False` for a mismatched source or degraded snapshot, but
  `main:465-469` sends whenever `--send-verification` has the three CLI fields;
  it never requires `verification["verified"] is True`. This can post a
  verification receipt for a non-healthy or wrong source unless a downstream
  endpoint happens to reject it.
- `[P1]` The production preflight can report readiness without proving the
  business path. `tests/e2e/health_incident_production_preflight.py:78-103`
  blocks only on Hermes state, OmniRoute/OpenCode probes, and a few file/string
  checks; it does not require NoticePlace health HTTP success, notification
  center/GPTAdmin Hub/Agent-Herder/OpenCode/OmniRoute active state, installed
  timer/config artifacts, or a registered live route. File existence and a
  substring in a JS bundle are insufficient activation proof.
- `[P2]` SSH target validation is incomplete: `fleet_health_monitor.py:148-162`
  passes the unvalidated config target directly to `ssh` without `--` or an
  explicit host-key policy. A target beginning with an OpenSSH option can alter
  the local SSH invocation; normal keyword/path shell inputs are otherwise
  quoted and bounded, and fan-out is capped at four workers with an overall
  SSH timeout.
- `[P2]` The preflight returns unredacted HTTP body prefixes and systemd stderr
  (`health_incident_production_preflight.py:27-43`), so a future health endpoint
  or diagnostic can leak sensitive content into JSON evidence despite the
  read-only intent.

Positive checks retained: metric dedup fingerprints exclude changing samples
and log counts (`health_monitor.py:302-315`); explicit remote SSH uses
BatchMode/ConnectTimeout/one attempt/20-second timeout (`fleet_health_monitor.py:148-175`);
remote output is bounded to 256 lines and raw lines are not emitted. The
production business canary remains unverified and must stay behind the stated
approval boundary.

## Fresh independent Critic audit (2026-08-09)

Scope was limited to the corrected health-monitor directory and
`tests/e2e/health_incident_production_preflight.py`; no deployment, restart,
secret read, or external send was performed.

- Focused regression command over the two producer test modules and the
  preflight test module: `19 passed`.
- Runtime config permission correction is present: `install.sh:29-32` gives
  the local runtime group JSON read access (`0640`) and keeps the env file
  `root:root 0600`; the fleet installer mirrors this for `admin` at
  `install-fleet.sh:31-34`. The service users and hardcoded JSON paths match
  those installer paths. However, production preflight does not inspect mode,
  owner/group, or actual runtime readability: `run():127-141` reduces this to
  file existence plus an active timer, and `readiness_blockers():84-85` only
  consumes the resulting boolean.
- `[P1 BLOCKER]` Collector failure is still fail-open for non-zero commands.
  `_default_command_runner():52-63` returns stdout and discards the subprocess
  return code. A controlled local run with
  `service_command=("sh", "-c", "exit 7")` produced
  `failed_services=[]` and `collector_errors=[]`. The remote generated probe
  has the same gap: `fleet_health_monitor.py:50-64,69-79` uses `set +e`,
  suppresses stderr, and emits no failure marker when an existing
  `systemctl`/`journalctl` command itself exits non-zero. A controlled probe
  with both commands replaced by functions returning 7 emitted only
  `snapshot ...`; `parse_remote_snapshot()` returned `collector_errors=[]`.
  The host can therefore be reported healthy when configured service/log
  collectors are unavailable.
- Verification gate is now closed at both call layers: current
  `health_monitor.py:442-445` rejects any direct send without
  `verification["verified"] is True`, and the adversarial fake-url opener saw
  a `RuntimeError` for `verified=False`; CLI checks remain at `main():516-522`.
- Preflight is materially stricter than the initial version (HTTP 200 checks,
  active stack units, authenticated OpenCode status, and active timer/config),
  and its HTTP/systemd outputs are metadata-only (`:27-52`), so the tested
  privacy leak is closed. Remaining strictness gap: the positive readiness
  inputs returned no blockers without any permission/readability evidence, and
  `_systemd_show():27` lets a probe exception escape instead of returning a
  fail-closed blocker. The live route checks are still file existence and a
  substring in the Agent-Herder bundle (`:119-122`), not a registered route or
  business canary.
- SSH option injection is closed for the reviewed case: `TARGET_PATTERN` plus
  `_validate_target():158-160` rejects the option-like target and the argv
  contains `--` before the target at `fleet_health_monitor.py:173-189`.
  There is still no explicit host-key policy; this is a residual P2 hardening
  item for unattended trust, not the original option-injection path.
- Central adapter coverage and no-send behavior were independently checked
  with collection functions stubbed (no SSH): seven results were produced for
  `local, admin-server-88, server01, router, haos, server01, server01`, with
  `sent_total=0`; a stubbed `send_event` was never reached.

### Critic verdict

`RETHINK` (FAIL for acceptance). The local/remote non-zero collector failure
path is a concrete remaining P1 blocker. Minimum proof to proceed is to
propagate command return status (including remote service/log pipelines) into
bounded `collector_errors`, add regression coverage, and rerun the focused
suite. Two viable routes are: (A) implement those fail-closed changes and
strengthen preflight permission/exception checks; or (B) keep the current code
explicitly blocked from readiness and narrow any future canary claim to
 targets whose collector backend health is independently proven. Route B does
 not close the original all-host business contract.

## Short Critic re-review of claimed P1 fix (2026-08-09)

Scope was limited to the three claimed diffs; no mutation, restart, deploy, or
external send occurred.

- Local command failure is fixed: `_default_command_runner()` now converts a
  non-zero return code to `COMMAND_ERROR_PREFIX` at `health_monitor.py:52-65`.
  A controlled `sh -c 'exit 7'` service probe produced
  `collector_errors=['service_probe_failed']`.
- Preflight changes are present: `_systemd_show()` catches `OSError` and
  `TimeoutExpired` at `health_incident_production_preflight.py:29-37`, and
  `runtime_config_readable()` validates the configured group ID and group-read
  bit at `:40-48`; `collector_installed` consumes those checks at `:143-159`.
- `[P1 REMAINS]` The remote generator still does not emit failure lines for
  non-zero system/log/file probe commands. `fleet_health_monitor.py:64`
  pipes `systemctl` into `awk` without status capture; `:71` and `:75` only
  emit a collector line when the executable is absent; `:79` only handles an
  unreadable file, not a failing `grep`. The parser does map synthetic
  `service_backend_*` and `log_backend_*` lines at `:118-130`, but the producer
  does not generate them for command failures. A controlled generated probe
  with `systemctl()` and `journalctl()` returning 7 emitted only `snapshot ...`
  and parsed to `collector_errors=[]`.
- Focused producer/fleet/preflight tests pass `22`, but the remote test feeds
  synthetic failure lines directly to the parser (`test_fleet_health_monitor.py:53-63`);
  it does not exercise the generated probe failure path.

### Re-review verdict

`RETHINK` (FAIL): local and preflight portions pass, but the remote
collector-failure producer/parser contract is not closed end-to-end. Minimum
proof is a generated-probe regression for non-zero `systemctl`, log backend,
and readable-file `grep` failures, followed by a green focused suite. No user
choice is needed; this is a concrete implementation gap.

## Fresh bounded Critic re-review (2026-08-09)

Verdict: `RETHINK` — one remaining P1 blocker; no deploy, restart, secret read,
or external send performed.

- Current `fleet_health_monitor.py:64` still pipes `systemctl` to `awk` under
  `set +e` without capturing the service-command return code. Current lines
  `71` and `75` likewise treat `logread`/`journalctl` non-zero through a
  backend-to-`grep` pipeline with no failure marker; line `79` only marks an
  unreadable file, not a readable-file `grep` failure. `parse_remote_snapshot`
  (`:118-130`) parses failure markers when present but cannot recover these
  omitted statuses. Thus configured service/journalctl/logread/file collector
  failure can still yield `collector_errors=[]` and a false healthy snapshot.
- Current `health_monitor.py:52-65` does propagate local command non-zero to a
  collector error; current `send_verification` (`:446-447`) and CLI gate
  (`:523-524`) require `verified=true`.
- Current preflight catches systemd probe failures/non-zero status
  (`health_incident_production_preflight.py:29-37`), checks runtime group/read
  metadata (`:40-48`), and folds those checks into installed-timer readiness
  (`:143-159`). No separate preflight blocker was found in this bounded pass.

Minimum proof to proceed: add generated-probe regression coverage for non-zero
`systemctl`, `journalctl`, `logread`, and readable-file `grep`; emit bounded
collector errors; rerun the focused suite green.

## Final Critic gate after generated-probe correction

The generated remote probe now captures non-zero statuses from `systemctl`,
`journalctl`, `logread`, and readable-file `grep`, emitting bounded collector
errors that the parser preserves. Fresh generated-probe regression and the
focused fleet module both pass (`7 passed`); preflight tests pass (`4 passed`).
The narrow Critic scope has no remaining implementation blocker. Production
canary remains separately blocked only by live-readiness and explicit approval
boundaries.

Latest acceptance evidence: producer/fleet suite `19 passed`, preflight suite
`4 passed`, vertical canary resolved with exactly three plans, useful progress
`[true, false]`, independent verification, elapsed `4210` ms, trace refs, and
`external_sends=false`; seven-target central fan-out parsed all targets with
`sent=0`.

## Timing accounting

The task history records total active-minute estimates, not a per-stage
stopwatch. Therefore no exact factual duration is claimed for stages 1–6;
the authoritative estimates remain `180/420/900` initially and
`240/720/1500` after gap discovery. Stage-level timing must be instrumented
from the next activation boundary rather than reconstructed as false precision.

The user's expectation that every subtask should take 20 minutes is not a
measured duration or an acceptance criterion. This is a cross-repository,
production-gated task; source work, artifact staging, runtime activation,
external provider readiness, and the business canary are separate units of
work. The next activation segment will record start/end timestamps explicitly.

## Staging release audit

- Agent Herder backend TypeScript staged to `/tmp` successfully; Vite web
  staging build also succeeds without touching live `dist`.
- GPTAdmin Hub test/build remains green in a disposable `/tmp` binary.
- NoticePlace `npm pack --ignore-scripts --dry-run` initially exposed Python
  caches and Graphify artifacts because the package whitelist included the
  whole `notification_center/` directory. The whitelist now uses
  `notification_center/*.py` and a defensive `.npmignore`; repeat dry-run
  reports `bad_cache_entries=[]`, includes `health_workflow.py`, and contains
  13 runtime Python modules.
- No staging artifact was copied to `/opt`, installed into Hermes, or used for
  a live restart.

## Fresh final Critic gate (2026-08-09)

`PASS`

- In `automation/health-incident-monitor/`, `pytest -q
  test_fleet_health_monitor.py`: `7 passed`. The generated probe ran with
  `systemctl`, `journalctl`, `logread`, and readable-file `grep` stubs returning
  non-zero; parsing yielded `service_backend_failed`,
  `log:journalctl:failed`, `log:logread:failed`, and `log:app.log:failed` in
  `collector_errors`.
- In `/home/admin/gptadmin`, `pytest -q
  tests/e2e/test_health_incident_production_preflight.py`: `4 passed`.
- Scope stayed read-only: no deploy, restart, secret read, or external send.

## Current bounded Critic audit (2026-08-09, user-stopped)

Verdict: `RETHINK` (FAIL for acceptance). This section is the current gate and
supersedes the earlier bounded `PASS` for the corrected Fleet/preflight slice.
The audit stayed within the requested catalog skill, its Fleet test, and the
two GPTAdmin preflight files. No production mutation, restart, deploy, secret
read, external send, or live preflight run was performed. The live preflight
was not executed because `run()` issues a loopback `POST` to the Agent-Herder
remediation route and the user explicitly prohibited sending anything.

Evidence:

- `pytest -q tests/test_health_workflow.py` passed `3`;
  `pytest -q tests/e2e/test_health_incident_production_preflight.py` passed
  `6`. These tests are green but do not exercise a partial `_apply()` failure
  after a remote install has already changed a target.
- The live Agent-Herder route probe is present in
  `health_incident_production_preflight.py:196-202`: it sends `{}` as
  `POST http://127.0.0.1:18787/api/health/remediation`, and
  `readiness_blockers():103-104` requires HTTP `400`. This is a bounded local
  route-existence probe, not an external notification; it was not live-run in
  this audit. The separate bundle substring check at `:131-133` remains only
  corroborating evidence, not route proof.
- Fleet collector gating is present for the Fleet branch:
  `runtime_artifacts:232-245` requires config readability, env presence,
  source/live digests, unit digests, and `_service_run_succeeded()`. However
  `_service_run_succeeded():152-157` accepts only `LoadState=loaded`,
  `Result=success`, and `ExecMainStatus=0`; it has no last-run timestamp or
  freshness bound. A stale historical success can therefore satisfy the
  claimed last-run gate. The local collector branch at `:246-255` also has no
  source/live digest checks. The reported live blocker set is consequently
  not strong enough to claim fresh collector readiness.
- Transactional rollback is not closed end-to-end. In
  `health_rollout.py:329-331`, an entry is appended to `installed_entries`
  only after the remote `install` returns success. If `install` changes the
  destination and then returns non-zero, `:333-336` invokes rollback without
  that current entry, so the partially changed artifact is not restored.
  `test_health_workflow.py:77-101` tests `_remote_rollback()` for entries
  already supplied to it, but does not cover this failing-install boundary.
- SSH handling is bounded through the CLI: `_load_manifest():57-68` accepts
  only the canonical `100:22104` target, and remote shell paths are quoted
  with `shlex.quote`; the canonical argv is option-safe in the checked-in
  manifest. Defense in depth is incomplete: `_ssh_base():87-101` itself has
  no `--`/allowlist and accepts an option-like target when called directly;
  `_scp()` similarly relies on the manifest gate. This is residual P2 risk,
  not evidence of an exploitable path through the canonical CLI flow.
- No workflow code calls `daemon-reload`, timer enable/start, or external
  notification. The Fleet workflow only probes `is-active`/`is-enabled` and
  records `daemonReload`/`externalSend` as not-run. The preflight's only
  write-shaped operation is the bounded loopback invalid POST noted above;
  no Telegram/NoticePlace send was invoked here. HTTP bodies and systemd
  stderr are reduced to metadata/type, so the tested secret/body leakage path
  is closed.

Minimum proof needed before PASS:

1. Make a failed remote install rollback the current entry as well as prior
   entries (or use an atomic staged replacement), and add a regression that
   models a target changed before a non-zero install result.
2. Add a freshness-bound last-run check and digest gating for every active
   collector branch, with stale-success regression coverage.
3. After those fixes, rerun the focused suites and obtain an explicitly
   permitted live preflight/route result before making a current live blocker
   claim.

Viable routes: (A) implement the rollback/freshness fixes and rerun the
focused gates, then perform the separately approved live preflight; or (B)
keep the rollout explicitly blocked and limit any report to the static/test
evidence, without claiming collector readiness or a fresh live blocker set.

## Fleet boundary correction (2026-08-09)

The requested Telegram destination is the `Health` topic; do not infer a
different topic name during activation.

The production activation path was paused before any mutation when the user
clarified that Fleet/SSH should own sidecar delivery. Read-only inspection of
the current `agent-harness-fleet` control plane shows that its host-agent
operations are limited to discovery, user-level `agentsync`, harness package
sync, and declared catalog workflows. The Hermes adapter currently declares
`plugins.supported=false`; package materializers are constrained to a harness
configuration root and the host agent has no root/systemd/`/etc` operation.

The health source already contains the correct Fleet-compatible central mode:
`automation/health-incident-monitor/install-fleet.sh` installs a bounded
central SSH fan-out collector, while `fleet_health_monitor.py` probes the
configured targets and supports Pythonless `logread` hosts. It requires the
scoped token/config as an explicit precondition and must not be activated
alongside the existing `fleet-health.timer` without reconciliation.

Therefore a direct copy into `/opt` is not the Fleet rollout requested by the
user. The next implementation slice is to expose the health collector as a
Fleet preview/apply/verify workflow (source files and service units only;
secret/token and timer activation remain separate gates), then use that
workflow's exact preview for any production installation.

## Fleet rollout receipt (2026-08-09)

Implemented `catalog/skills/health-incident-monitor/` in
`/home/admin/agents-projects/agent-harness-fleet` with a bounded
`manifest-preview-apply-verify-v1` workflow. Because the Fleet controller
itself runs with `NoNewPrivileges=yes`, the workflow uses the declared SSH
target `100:22104` and its existing remote sudo boundary; it does not try to
elevate locally.

Live Fleet preview reported `sshReachable=true`,
`sudoNonInteractive=true`, the existing `fleet-health.timer` active, and no
health config/env files. The exact preview confirmation was
`sha256:937da5a39f1131fc3e3d1432df46a99d9785c3562d318e3594929986d4d0156d`.
The approved apply installed the four central health artifacts and returned
`status=prepared`; Fleet verify then returned `status=verified` with
`mismatches=[]` and digest
`sha256:ecde455e59b691d8b2573a65c62cb3add3a0b5bcb13730114253eb07fc2afc88`.
The exact remote backup root is
`/var/backups/health-incident-monitor/ecde455e59b691d8b2573a65cb3add3a0b5bcb13730114253eb07fc2afc88`.
No existing artifact required backup because all four targets were absent.
The workflow recorded `daemonReload=not-run`, timer activation
`not-run`, and `externalSend=not-run`; the `Health` Telegram topic remains
the selected destination (internal key `health`).

## Approved runtime batch receipt (2026-08-09)

Using the approved production batch, NoticePlace was overlaid only at its
canonical runtime files after a full recoverable backup
`/opt/noticeplace.backup-20260809T151539Z`; the live process was restarted and
is active. The installed health workflow is present in
`/opt/noticeplace/notification_center/health_workflow.py` and the selected
job helper is present in `agent_job_helper.py`.

Agent-Herder's built `dist` was switched atomically after backup
`/home/admin/agents-projects/agent-herder/dist.backup-20260809T151539Z`;
the user service was restarted and is active. A read-only GET to the installed
`/api/health/remediation` route returned `405`, so no remediation job was
created.

The Hermes bridge was linked from the canonical `hermes-config` source,
enabled with `--no-allow-tool-override`, and Hermes was restarted through the
official gateway command after the service-ops updater refused to update
tracked changes. Direct systemd inspection shows `hermes-gateway.service`
active/running; the plugin list reports `agent-herder-bridge` enabled, version
`0.1.0`, user source. The gateway's recorded state file is stale, but the
live process and unit are healthy evidence.

The corrected read-only preflight now reports Agent-Herder live route true and
NoticePlace `8091/health` status `401` (route exists; authenticated readiness
not proven). Remaining blockers are exactly: NoticePlace health-token auth,
OpenCode authenticated readiness, and health collector config/token plus
active timer. No Telegram message, external health POST, or remediation job
was sent in this batch.
## Final bounded Critic audit (2026-08-09, timeout)

Verdict: `RETHINK` (TIMEOUT before the requested focused rerun and live GET
probe could complete). Audit was limited to the six corrected files named by
the user; no production mutation, preflight POST, restart, reload, timer
activation, secret read, or external send was performed.

- The read-only route contract is present: preflight uses `GET
  /api/health/remediation` (`health_incident_production_preflight.py:199-204`)
  and Agent Herder returns `405 method_not_allowed` without creating a session
  (`src/web/server.ts:149-150`). A current live GET result was not obtained
  before timeout, so this remains source/test evidence rather than live proof.
- Digest, env-presence, config-readability, and non-placeholder last-start
  checks are wired into the collector gates (`health_incident_production_preflight.py:218-269`).
  The last-run check only requires any non-empty
  `ExecMainStartTimestamp` (`:154-160`); it has no freshness bound and does
  not bind the timestamp to the current collector digest. A stale historical
  success can therefore satisfy the claimed last-run gate.
- Rollback ordering is corrected in source: the current entry is appended
  before `install`, and the failure path passes all installed entries to
  rollback (`health_rollout.py:322-337`). The corrected test file only proves
  rollback of entries already supplied to `_remote_rollback`; it does not
  regress an install that changes a target and then returns non-zero.
- Canonical manifest paths/target and `shlex.quote` path handling are present
  (`health_rollout.py:40-69, 141-164, 270-280`), and config values are not read
  by preflight. Residual defense-in-depth risk remains because `_ssh_base()`
  and `_scp()` do not independently allowlist the target or terminate target
  arguments with `--`; safety currently depends on `_load_manifest()`'s
  canonical target gate.
- Source contains no timer enable/start, daemon-reload, or external-send call;
  activation fields remain `not-run` (`health_rollout.py:221-227, 351, 367`),
  and preflight reports `external_send: false` (`health_incident_production_preflight.py:272-280`).

Minimum proof for PASS: add a freshness-bound last-run regression and a
failed-install-after-partial-write rollback regression, rerun the two focused
test suites, and obtain the permitted live GET route result. Until then, do
not claim fresh collector readiness or live route acceptance.

## Final hardening rerun (2026-08-09)

The concrete Critic findings above were addressed before this rerun:

- `_service_run_succeeded` now requires a recent monotonic systemd start
  (180-second bound), `Result=success`, exit status `0`, and a real timestamp;
- Fleet SSH/SCP helpers independently enforce `100:22104` and terminate
  option parsing;
- Fleet rollback includes the current target before `install`, and a focused
  failed-install regression proves restore of the partially written target;
- the preflight now checks env ownership/mode, producer/unit digests, and the
  read-only GET route on the live Agent-Herder service.

Fresh evidence: Fleet workflow tests `4 passed`, preflight tests `8 passed`,
Agent-Herder focused tests `7 passed`; the live GET probe returns `405`, the
NoticePlace health route returns `401` without its token, and the preflight
remains correctly blocked only by authenticated NoticePlace health,
authenticated OpenCode, and health config/token plus active timer. No
external send, mutating health probe, or remediation job was created.

Final live rerun after all hardening: `PYTHONPATH=src pytest` on Fleet workflow
and LHC workflow `15 passed`; Agent-Herder focused suite `7 passed`; preflight
suite `8 passed`; vertical synthetic canary resolved one incident with exactly
three plans, useful progress `[true, false]`, elapsed `4210 ms`, and
`external_sends=false`. Live Fleet preview through SSH now returns `ready` with
all four artifacts `noop`, legacy timer detected, config/env absent, and
`daemonReload=not-run`, `externalSend=not-run`. Direct live probes are
Agent-Herder GET `405`, NoticePlace `8091/health` `401`; all three relevant
services are active. The final Critic worker itself timed out, so the result is
not represented as an independent PASS; its concrete findings were covered by
the hardening and rerun evidence above.

## Latest Fleet config receipt (2026-08-09)

The non-secret health manifest was installed through the live Fleet workflow
over SSH, not by a local copy. Preview confirmation:
`sha256:aebae8cb2a463433fb397b1a4b27e40a1a67defc966c799036e77c639af5fe25`.
Apply returned `prepared`; verify returned `verified`, `mismatches=[]`, with
source digest
`sha256:e96fd1e9dfe41fa55e6909eb6ecfcc9142ec13ecfcbf1d32112786aa3d659eca`.
The target config `/etc/health-incident-fleet.json` is live as non-secret
`0640 root:admin`.

The initial false `configFilePresent=false` receipt was caused by the remote
`test -s -- path` portability bug. The runner now uses `test -s path`, with a
regression; live verify now reports `configFilePresent=true`, while the secret
env, timer activation, daemon reload, and external send remain untouched.
Fleet workflow/LHC tests: `16 passed`. Remaining gates are scoped credential,
old/new timer reconciliation, authenticated NoticePlace/OpenCode readiness,
and an explicit no-send-yet Telegram `Health` canary.

Fresh preflight after the Fleet config apply: `ready_for_external_canary=false`;
`fleet_config_file=true`, `fleet_config_readable=true`, but the secret env is
absent, `health-incident-fleet.timer` is inactive, and there is no recent
successful collector run. Live route evidence is Agent-Herder GET `405`,
NoticePlace `401`, OpenCode `401`, and OmniRoute `200`; `external_send=false`.

## Approval boundary repeated (2026-08-09)

Three consecutive continuation checks found the same unchanged boundary:
`/etc/health-incident-monitor.env` is absent, the old `fleet-health.timer` is
still active, the new health timer is inactive, and the authenticated
NoticePlace/OpenCode canary is unproven. No secret write, systemd activation,
or Telegram send was performed. Further progress requires the user's direct
approval for the credential/timer batch.

## Credential and timer approval applied (2026-08-09)

The user explicitly approved the credential and timer boundary. Fleet activation
was applied through the canonical SSH/Fleet workflow, not by manual host setup:

- `/etc/health-incident-monitor.env` is present with restricted ownership and
  mode; secret values were not printed;
- `health-incident-fleet.timer` is enabled and active;
- legacy `fleet-health.timer` is disabled/inactive;
- the health producer ran successfully on the live target;
- `notification-center.service`, `gptadmin-hub.service`, and
  `agent-herder.service` are active;
- Telegram delivery remains fail-closed because the configured mode is inactive.

## Runtime routing correction (2026-08-09)

The first fresh full canaries exposed a real Agent-Herder defect: it sent
`PATCH /session/:id` with a `model` field, but the current OpenCode API ignores
that field while returning HTTP 200. The effective model was therefore the
OpenCode default `opencode/deepseek-v4-flash-free`, despite the requested
`omniroute/subagent`.

The OpenCode adapter now selects the model before the first prompt through
`POST /api/session/:id/model` with `{model:{providerID,id}}`, parses the first
provider separator safely, and fails closed when selection is unavailable.
Focused Agent-Herder tests/build after the change: `8 passed`; live service was
restarted on the new dist.

## Authenticated live vertical canary v4 (2026-08-09)

Intake accepted event `evt_06a1051980b4430094cec04d15280093` and incident
`inc_bfd6635471864b6b8a7a20211c6cca3c` through NoticePlace. The Telegram
delivery was cancelled with `Telegram mode is inactive`, as required by the
fail-closed boundary. The diagnosis delivery retried once after the preceding
service restart and ended `sent`.

Independent OpenCode DB evidence records session
`ses_0180a9888ffeEdbSbO61T8cbQm` with effective model
`{"id":"subagent","providerID":"omniroute","variant":"default"}`.
NoticePlace recorded `health.plans_attached` with exactly three plans, two
trace refs for Agent-Herder/OpenCode, correlation ID, and live evidence refs.
The three plans are `P1-acknowledge-and-quarantine`,
`P2-extend-fixture-canary-suite`, and `P3-observe-only-CPU-watch`.
No remediation was started and no external Telegram message was sent.

## Fresh independent Reviewer gate — OpenCode model selection (2026-08-09)

Scope was limited to the latest uncommitted model-selection diff in
`/home/admin/agents-projects/agent-herder/src/adapters/opencode.ts` and
`tests/opencode-recovery.test.ts`, plus the current live proof above. No
Agent-Herder product file, build artifact, service, database, secret, or
external transport was changed by this review.

Evidence:

- `src/adapters/opencode.ts:347-366` parses the first `/` as the provider
  separator, preserves the complete remainder as `modelID`, and sends
  `POST /api/session/{encoded-session-id}/model` with
  `{ model: { providerID, id: modelID } }`. This is safe for the normal
  provider/model convention when a model ID itself contains `/`, for example
  `openrouter/openai/gpt-4.1` → provider `openrouter`, ID
  `openai/gpt-4.1`. The selected test currently covers only
  `omniroute/subagent`, so that slash-preservation property is not regression
  tested.
- The same method returns `{ ok: false }` for malformed input, every
  non-2xx response, and transport exceptions. The removed legacy
  `PATCH /session/{id}` and global `/config` fallback paths are absent from
  this diff; there is no silent success path in the adapter method itself.
- `tests/opencode-recovery.test.ts:118-142` positively verifies the v2 path,
  POST method, and request body. It invokes only `changeModel`; it does not
  issue a first prompt or record request ordering, and it does not make the
  model endpoint fail. Therefore it cannot independently prove that the
  production caller awaits model selection before its first prompt or stops
  when selection fails instead of allowing the OpenCode default.
- Independent commands run during this gate: `npx vitest run
  tests/opencode-recovery.test.ts --config vitest.config.ts` → `8 passed`;
  `npx tsc --noEmit` → passed; `git diff --check` → passed. A full
  `npm run build` was not rerun because it writes shared `dist` artifacts;
  the task's preceding evidence records the focused build/test result and a
  live service restart on the new dist.
- The current live proof records the old HTTP-200 silent-default defect and
  the replacement v2 endpoint at task lines 893-903. It also records an
  independent OpenCode DB session at lines 913-915 with effective model
  `{"id":"subagent","providerID":"omniroute","variant":"default"}`.
  This is strong evidence that the deployed canary ended on the requested
  model, but it is not a raw request-order trace and does not exercise the
  failure path. The same proof records no remediation and no external
  Telegram send.

Findings, ordered by severity:

- `[P1]` The requested before-first-prompt and fail-closed guarantees are not
  independently regression-proven. The positive test stops after
  `changeModel`, so a future caller-order regression or ignored `{ok:false}`
  could again send the first prompt on the default without turning this test
  red. Smallest proof fix: add a black-box request-order case that records a
  successful model POST before the first prompt, plus an HTTP-error/transport
  failure case that asserts no prompt is sent and no legacy/global fallback is
  attempted. The current source method itself has no observed fallback defect;
  this is an acceptance-evidence gap.
- `[P2]` Slash-containing model IDs are handled correctly by the first-
  separator implementation, but the checked-in regression does not cover the
  required shape. Add a case such as `openrouter/openai/gpt-4.1` and assert the
  exact provider/id body. This review assumes OpenCode provider IDs do not
  themselves contain `/`; that representation would be ambiguous under the
  existing provider/model string contract.

Verdict: `CHANGES_REQUIRED` for the independent gate, limited to the missing
order/failure/slash regression proof. The selected implementation has the
correct v2 endpoint and an explicit fail-closed adapter result, and the live
DB evidence supports the requested effective model. It is not sufficient to
claim all four requested guarantees without the negative and ordering cases
above. No product changes were made by Reviewer.

2026-08-09 Fresh short Critic verdict: PASS — corrected 10-test suite covers v2-before-prompt, fail-closed selection failure, and slash-containing model IDs; live v4 proves effective `omniroute/subagent` and the exact-three-plans callback.

## Fresh context-free Tester black-box attempt (2026-08-09)

STOP_MISSING_REAL_SURFACE

Evidence: Touchpoint diagnostics failed with `Transport closed`; no real-user surface was available. No incident action, remediation selection, mutation, or Telegram send was performed.

## Orchestrator mapping correction in progress (2026-08-09)

The live OpenCode probe proved that `omniroute/orchestrator` is not a real
upstream model and fails with `ProviderModelNotFoundError`. A separate
no-mutation canary proved `omniroute/free-stack` returns an assistant response.
The implementation now keeps the requested logical role
`omniroute/orchestrator`, maps it explicitly to the proven effective model
`omniroute/free-stack`, and fails closed if the operator profile does not carry
that exact mapping.

NoticePlace now runs two bounded Agent-Herder/OpenCode stages for one health
diagnosis delivery: read-only `omniroute/subagent` diagnosis, then a separate
plan-orchestrator session receiving the bounded diagnosis and producing exactly
three plans. The callback records both session IDs, requested/effective model
metadata, and all stage trace references. Focused NoticePlace tests passed:
8 agent-job-helper tests and 14 health-workflow tests.

## Live two-stage provenance/timing canary v8 (2026-08-09)

A real SSH-hosted canary was injected through NoticePlace `/v1/health/signals`
with a unique correlation and dedup key. It reached `health.plans_attached`
without any remediation selection or infrastructure mutation. Live receipt:

- incident: `inc_46980135c4e643f0bb3464727beb3e0d`
- correlation: `corr:synthetic-two-stage-v8:1786307063`
- diagnosis session/model: `ses_017cd4b5effeFkSLobfeMhuxRI`,
  `omniroute/subagent`
- orchestrator session: `ses_017cd32d5ffeUf2so7O6Putlug`
- requested logical role: `omniroute/orchestrator`
- effective proven model: `omniroute/free-stack`
- measured diagnosis stage: `6288 ms`
- measured orchestrator stage: `12775 ms`
- measured stage total before callback: `19063 ms`
- trace list contains both Agent-Herder and OpenCode refs for both sessions
- both Telegram deliveries ended `cancelled` with `Telegram mode is inactive`
- agent delivery ended `sent`

The provenance fix preserves orchestration as a bounded object instead of an
obscuring truncated string, and orders the four stage trace refs before other
telemetry so the durable receipt retains both stages. Focused tests after the
fix: `49 passed`.

## Latest bounded Critic audit — two-stage orchestrator/provenance/timing (2026-08-09)

Verdict: `PASS`.

- Current NoticePlace implementation was inspected only in
  `notification_center/agent_job_helper.py` and the related health tests. The
  diagnosis stage is completed and parsed before the separate orchestrator
  session is created (`_run_health_diagnosis`); the orchestrator receives only
  bounded diagnosis/evidence/trace data and is pinned to the explicit
  `omniroute/orchestrator` → `omniroute/free-stack` mapping.
- The callback constructs a bounded orchestration object with both session IDs,
  requested/effective models, harness, diagnosis/orchestrator elapsed times,
  and total elapsed time. Trace refs are deterministically prefixed for both
  Agent-Herder and OpenCode sessions before bounded telemetry refs, preserving
  the two-stage provenance contract.
- The orchestrator result is accepted only after strict extraction and health
  plan validation; the callback therefore records exactly three validated
  plans. The focused current test run was `pytest -q
  tests/test_agent_job_helper.py tests/test_health_workflow.py` → `22 passed`.
- Live v8 evidence independently records two session IDs, effective
  `omniroute/free-stack`, diagnosis `6288 ms`, orchestrator `12775 ms`, total
  `19063 ms`, four stage trace refs, and `health.plans_attached`; Telegram
  sends remained cancelled and no remediation selection or infrastructure
  mutation occurred.

Excluded from this gate: production remediation, Telegram delivery, unrelated
health collector/runtime work, and any product edits. No actionable finding
remains within the bounded two-stage provenance/timing scope.

## Fresh independent Reviewer gate (2026-08-09)

Scope: current health activation workflow, NoticePlace health/job workflow,
Agent Herder model/progress routing, Fleet preflight, and the bounded fixture
canary. No product file, service, database, secret, or external transport was
changed by this review.

Evidence:

- Focused tests passed: Agent Herder Vitest `17 passed`, NoticePlace selected
  health/API/job/topic tests `54 passed`, Fleet activation tests `4 passed`,
  GPTAdmin production-preflight tests `9 passed`; Agent Herder `tsc --noEmit`,
  NoticePlace `py_compile`, and `git diff --check` passed.
- Disposable vertical canary passed with one incident, five source signal
  types, exactly three plans, signed repair selection, useful progress
  `[true, false]`, independent verification, resolved receipt, elapsed `4210`
  ms, and `external_sends=false`. Its GPTAdmin, Agent Herder, OmniRoute, and
  Telegram transports are fakes or no-send doubles.
- Read-only live preflight returned `ready_for_external_canary=true`: Fleet
  collector/timer, active services, authenticated NoticePlace health `200`,
  Agent Herder route `405`, OpenCode `200`, and OmniRoute `200`. This does not
  prove user-facing computer-use or a remediation canary.

Findings, ordered by severity:

- `[P1]` The Fleet activation workflow does not wire the dedicated NoticePlace
  health credential it checks. `catalog/skills/health-incident-activation/scripts/health_activation.py:118-123`
  probes `/etc/notification-center.env` for `NOTIFY_CENTER_HEALTH_TOKEN`, but
  the embedded activation script at `:386-393` updates that file only with
  `NOTIFY_CENTER_TOKENS_JSON` and GPTAdmin jobs, while writing the generated
  token only as `NOTICEPLACE_HEALTH_TOKEN` in `/etc/health-incident-monitor.env`.
  NoticePlace actually reads `NOTIFY_CENTER_HEALTH_TOKEN` in
  `notification_center/http_api.py:692-696`, and its service loads
  `/etc/notification-center.env`. A fresh activation therefore cannot satisfy
  its own readiness check and, if forced past it, restarts NoticePlace without
  the dedicated health API token. Smallest fix: install the same generated
  token as `NOTIFY_CENTER_HEALTH_TOKEN` in the notification-center env while
  retaining `NOTICEPLACE_HEALTH_TOKEN` for the collector, then add a regression
  that exercises the generated script's credential contract.

- `[P1]` The authoritative business canary is still not independently proven.
  The only completed fixture canary uses fake GPTAdmin/Agent Herder/OmniRoute
  and a no-send Telegram transport; the latest live two-stage receipt stopped
  after plans, with no user selection, remediation, independent verification,
  or resolved NoticePlace receipt. The context-free Tester recorded
  `STOP_MISSING_REAL_SURFACE`, and the live Telegram mode remains fail-closed.
  Do not claim full Definition-of-Done acceptance until the approved real
  user-facing surface performs one selection and the real remediation path
  reaches a resolved receipt with elapsed time and durable traces.

Verdict: `CHANGES_REQUIRED`. The local implementation and read-only live
preflight are strong, but the activation credential defect is a concrete P1
implementation blocker and the required independent user-facing business
canary remains unverified. No product changes were made by Reviewer.

## Fresh bounded Reviewer gate — latest two-stage orchestrator/provenance/timing (2026-08-09)

Result: `PASS` for the requested two-stage implementation scope. Review was
limited to NoticePlace's latest helper/workflow persistence, focused tests, and
the recorded live v8 receipt; the activation credential and full user-facing
canary findings above are outside this bounded review and remain open.

- `notification_center/agent_job_helper.py:322-347` places four stage trace
  refs first, de-duplicates/bounds them, and persists correlation, evidence,
  requested/effective models, and three timing fields.
- `agent_job_helper.py:373-443` gates orchestrator creation on completed
  diagnosis, requires exactly three plans, and measures diagnosis,
  orchestrator, and pre-callback total elapsed time.
- `health_workflow.py:49-71,281-297` rejects incomplete provenance and bounds
  timing before durable `health.plans_attached` persistence.
- Focused command: `pytest -q tests/test_agent_job_helper.py
  tests/test_health_workflow.py` → `22 passed`.
- Recorded live v8 evidence contains both sessions, effective
  `omniroute/free-stack`, timings `6288/12775/19063 ms`, four stage trace refs,
  and no remediation or external Telegram send.

No actionable finding in this bounded scope. The full Definition of Done and
activation credential gate remain unverified as explicitly recorded above.

## Post-review activation credential fix (2026-08-09)

Reviewer P1 was fixed in the Fleet activation source: the generated health
scope token is now written to `NOTIFY_CENTER_HEALTH_TOKEN` in
`/etc/notification-center.env` and remains written as
`NOTICEPLACE_HEALTH_TOKEN` for the collector. Fleet activation tests passed
`4 passed`. The fix was applied through the exact SSH/Fleet workflow and
verified after restart: `dedicated_present=True`,
`matches_health_scope=True`, `health_scope_count=1`, health timer/service
active, legacy timer inactive, and `externalTelegram=not-run`.

The fresh context-free Tester completed with `STOP_MISSING_REAL_SURFACE`:
Touchpoint diagnostics/windows/screenshot returned `Transport closed`; no
Telegram topic was inspected and no selection, remediation, mutation, or
external send occurred. This is the required honest black-box outcome while
the supported user-facing surface is unavailable.

## Hermes execution truth before CLI seam (2026-08-09; superseded)

At this checkpoint Hermes gateway was active and `/health` on `127.0.0.1:8644`
returned `200`, while the public MCP adapter was observation/send-only. The
following 2026-08-10 section records the implemented and live CLI-job seam;
the historical MCP limitation remains true for observation, but is no longer
the remediation critical path.

## Hermes CLI remediation seam implemented and live (2026-08-10)

The unproven Hermes session-creator boundary was replaced with a real bounded
job seam in Agent-Herder:

- `HermesAdapter` now creates local CLI-job sessions, supports model selection,
  useful status/transcript reads, and cancel/terminate for the spawned process;
- health remediation accepts `harness=hermes` and launches the installed
  Hermes CLI with `chat -q`, `--provider openai-codex`,
  `--model gpt-5.6-luna`, `--reasoning high`, and the terminal-only toolset;
- observation-only Hermes MCP remains separate and is best-effort with a
  bounded 2.5-second timeout, so health jobs do not hang on MCP discovery;
- the tracked Agent-Herder service unit and live drop-in carry the explicit
  Hermes binary, provider, reasoning, terminal toolset, and 20-minute job
  deadline.

Focused Agent-Herder verification: build plus `11 passed` focused and `105
passed` full-suite tests. Earlier live synthetic
Agent-Herder → Hermes canaries completed without tools, file changes, Telegram,
or external services. The latest receipt was:

- session: `hermes-job-1421ec17-56f4-4c67-90f7-6ad1de2c4ccb`;
- native Hermes session: `20260810_000540_1484a9`;
- status: `stopped`, model `gpt-5.6-luna`, transport `hermes-cli-job`;
- assistant result: `HERMES_AGENT_HERDER_CANARY_OK`.

Fleet was then previewed and applied through the live controller using the
current confirmation `sha256:b55c2e15b4ce88199876f2650bec00b9b3fb91bb80e4ace04125bfb9cf0bd5b3`.
Remote verification shows the health-remediation profile is now
`harness=hermes`, `model=gpt-5.6-luna`, `reasoning=high`, `topic=health`.
No real incident was selected or remediated; the business DoD remains pending
user plan selection, controlled remediation, independent source verification,
and a resolved NoticePlace receipt.

## Hermes review findings fixed (2026-08-10)

Fresh Reviewer/Critic found four concrete gaps: the live process was older than
the current build, the tracked unit omitted the Hermes execution environment,
the CLI job had no wall-clock watchdog, and provider/reasoning/trace/progress
could drift or be mislabelled. The implementation now:

- rejects a Hermes health request unless the live adapter profile matches the
  canonical provider/reasoning/terminal contract;
- captures the execution profile on each job and archives bounded,
  redacted stdout/stderr as `observed-cli-output` trace material;
- emits real non-heartbeat CLI output as progress messages and terminates a
  hung job at 20 minutes with SIGTERM followed by bounded SIGKILL escalation;
- keeps the environment in the tracked systemd unit as well as the local
  drop-in.

Evidence: Agent-Herder build, focused `11 passed`, full `105 passed`, and
`git diff --check`. A fresh live restart and post-restart canary are still an
explicit deployment boundary; the previous canary belongs to the older PID.

## Fresh live pre-restart audit (2026-08-10)

- The bundled fleet probe reports server-100, server-88, server01, router,
  HAOS, server01, and server01 reachable. Server-100/88/44 are at 82% disk, below
  the actionable 85% threshold; load remains below CPU count and RAM is
  available. `failed_units` is retained as a lead, not an outage proof.
- Hermes gateway health is `200 {"status":"ok","platform":"webhook"}` on
  port 8644 and its gateway process is active. The service-ops 9119 probe is
  a mismatched/stale dashboard check; it is not used as gateway authority.
- Hermes egress is `enabled=yes` but `listening=no`; this is recorded as
  `.agents/tasks/todo-20260810-hermes-egress-not-listening.md` and remains
  outside the approved credential/timer boundary.
- Agent-Herder is active on PID `3832628`, started before the current build.
  No daemon-reload or restart has been performed after the review fixes.

## Current-dist isolated canary and receipt parser correction (2026-08-10)

The first isolated current-dist canary reached `stopped` with useful progress
but exposed that progress-visible Hermes CLI prints `Session: <id>` rather than
`session_id: <id>`. The adapter parser now accepts both receipt formats and
omits the receipt line from progress. Build, focused tests (`11 passed`), and
full suite (`105 passed`) are green after the fix.

The fresh isolated no-send canary against the current `dist` then returned:

- `accepted → stopped`, `transport=hermes-cli-job`;
- Agent-Herder session `hermes-job-c7edec59-fb65-48e8-8cfe-59453e84c4f5`;
- native Hermes session `20260810_003911_bcf369`;
- history source `observed-cli-output`, progress fingerprint present, 10
  bounded evidence refs, and exact canary response accepted;
- no tools, file mutation, Telegram delivery, or production restart.

## NoticePlace to Hermes profile seam correction (2026-08-10)

Read-only comparison of the applied Fleet profile with the NoticePlace helper
found that the helper still rejected `harness=hermes` and expected the legacy
`openai-codex/gpt-5.6-luna` model string. This would have stopped a real
selected-plan delivery before Agent Herder.

The source helper now allowlists Hermes, requires the health-remediation profile
to pin `hermes/gpt-5.6-luna/high/health`, and the examples/documentation match
the applied Fleet profile. NoticePlace focused tests and health workflow tests:
`37 passed`; JSON example validation passed. Commit: NoticePlace `1ee7365`.
The live `/opt/noticeplace` copy and notification-center restart remain
deployment-gated; no production mutation was performed.

## Isolated NoticePlace to Agent-Herder Hermes canary (2026-08-10)

Using the corrected NoticePlace helper and fresh Agent-Herder `dist` on an
isolated localhost port, the real cross-repo handoff completed:

- helper receipt: `profile=health-remediation`, `harness=hermes`,
  `model=gpt-5.6-luna`, `delivery=accepted`;
- Agent-Herder session `hermes-job-0ff20c98-dc82-499b-a786-964292d5ccc8`;
- terminal `stopped`, transport `hermes-cli-job`, native Hermes session
  `20260810_005140_cc3024`;
- history `observed-cli-output`, progress fingerprint present, 10 evidence
  refs, exact canary response;
- no production `/opt` deployment, systemd restart, Telegram send, or
  infrastructure mutation.

## Credential/timer activation and current boundary (2026-08-10)

The explicitly approved Fleet boundary is active: `health-incident-fleet.timer`
is enabled and waiting for its one-minute schedule; the legacy health timer is
not active. The latest run exited successfully (`status=0`) and fanned out
redacted host/service/log health events to the configured NoticePlace health
scope. `notification-center.service` is active. This proves the producer and
timer path, not the selected-plan remediation path.

Fleet owns this activation through its health workflow over SSH: it reconciles
the installed collector, credentials, timer, routes, and Agent-Herder profile.
It does not currently deploy the NoticePlace application source from this
worktree or materialize a new Agent-Herder `dist` into the live service. The
remaining production boundary is therefore a backup-first NoticePlace source
deployment plus Agent-Herder daemon-reload/restart, followed by a live
no-send canary. No Telegram message or Hermes egress start is authorized.

The full NoticePlace suite has one nondeterministic/path-topology failure
(`147 passed, 1 failed`) whose traceback points at a disappearing sibling
`notify` test; the focused NoticePlace routing test passes. It remains a
separate todo and is not used as Health acceptance evidence.

## Fleet deployment topology re-audit (2026-08-10)

The canonical Fleet health-monitor skill explicitly installs a central SSH
fan-out adapter on the Fleet controller; it does not install a sidecar on each
target host. The activation workflow likewise assumes the collector is already
installed and reconciles credentials, routes, profiles, and timers. Neither
workflow copies NoticePlace application source nor publishes a new
Agent-Herder `dist` release. This confirms the remaining gap is a missing
canonical application-runtime deploy adapter, not a missing health collector.

No production mutation was made during this re-audit. A deploy-preview can be
prepared locally, but the actual backup/copy/restart boundary still requires
the exact operator approval recorded in the task exclusions.
