# Production activation preview: health incident autopilot

Date: 2026-08-09, Europe/Moscow

This is a read-only activation preview. It records the exact current live
state and the remaining approval-bound actions. It is not a deployment receipt.

The repeatable preflight command is:

```bash
python3 tests/e2e/health_incident_production_preflight.py
```

It emits `health-incident-production-preflight.v1`, never reads secret values,
and must report `ready_for_external_canary: true` before the approved live
synthetic incident. The gate now requires the live NoticePlace health route,
all key stack units, authenticated OpenCode readiness, and an active health
timer/config; HTTP body contents and systemd stderr are not included in its
evidence.

## Current live evidence

| Seam | Current evidence | Meaning |
|---|---|---|
| NoticePlace | `notification-center.service` is active from `/opt/noticeplace`; `/opt/noticeplace/notification_center/health_workflow.py` is absent | Existing event/Telegram path is live, but the new health workflow is not deployed |
| GPTAdmin | `gptadmin-hub.service` is active from `/opt/gptadmin/bin/gptadmin_hub` | Existing hub is live; the source-level health progress changes are not proven in this binary |
| Agent Herder | user `agent-herder.service` is active on `127.0.0.1:18787`; live `dist` has no health-progress route | Existing session control is live, new supervisor endpoint is not deployed |
| OpenCode | user `opencode.service` process is listening on `127.0.0.1:4095`; unauthenticated health returns `401` | Process exists; authenticated readiness/provider proof is still missing |
| OmniRoute | `omniroute@20128.service` active; `/api/monitoring/health` returns `200`, version `3.8.49` | OmniRoute local/public health is proven |
| Hermes | user `hermes-gateway.service` failed since `2026-08-08 20:33:54`; CLI reports outdated unit and stale `gateway_state.json` | The requested remediation runtime is not currently available |
| Existing fleet monitoring | `fleet-health.timer` active every 3 hours; resource checks and NoticePlace notifications are visible | CPU/disk/RAM summary exists, but failed services/log keywords and GPTAdmin health handoff are absent |
| New producer | no installed `health-incident-monitor.service`, `health-incident-fleet.service`, or related timer found | Per-host/central service-log health collection is not activated |

## Host activation matrix

The existing fleet-health configuration currently names these targets:

| Target | Source ID to configure | Activation note |
|---|---|---|
| server-100 | `host:admin-server-100` | local target; use the local producer or central adapter, not both |
| admin-server-88 | `host:admin-server-88` | Linux target; local producer or central SSH adapter |
| server01 | `host:server01` | Linux target; local producer or central SSH adapter |
| router | `host:router` | OpenWrt has no Python; central SSH adapter uses bounded `logread` |
| haos | `host:haos` | central SSH adapter uses bounded `logread` |
| server01 | `host:server01` | central SSH adapter or local producer; current probe shows load violation |
| server01 | `host:server01` | central SSH adapter or local producer; current probe shows disk at 85% |

This list is an activation candidate derived from the current fleet-health
config, not permission to change every target. The per-host config must be
validated against the real target before enabling its timer.

Fresh bounded read-only probe evidence (2026-08-09) is:

| Target | CPU/load | Disk | RAM available | Failed units | Log backend |
|---|---:|---:|---:|---:|---|
| server-100 | load `37.37` / 104 CPUs | 82% | 55,577 MiB | 2 | local files/journal |
| admin-server-88 | load `65.98` / 88 CPUs | 82% | 48,067 MiB | 1 (`certbot.service`) | journal/files |
| server01 | load `54.83` / 88 CPUs | 59% | 30,605 MiB | 1 (`gptadmin-tunnel-frpc.service`) | journal |
| router | load `6.51` / 4 proc fallback | 25% | 11 MiB | n/a | `logread` |
| haos | load `0.85` / 4 CPUs | 68% | 1,535 MiB | n/a | `logread` |
| server01 | load `50.83` / 1 CPU | 79% | 413 MiB | 1 (`coturn.service`) | journal/files |
| server01 | load `0.00` / 1 CPU | 85% | 464 MiB | 0 | journal/files |

The probe is evidence for activation targeting, not a repair authorization.
The new central adapter was also exercised read-only against `router` and
`haos`; it produced normalized health events with `sent=0` and no raw log
lines.

The old fleet-health controller also has a separate correctness gap: a dry-run
filtered to `server01` emitted a `router` recovery because its state file is global
to all hosts. This is recorded in
`/home/admin/gptadmin/.agents/tasks/todo-20260809-fleet-health-dry-run-cross-host-state.md`;
the new producer must not reuse that filtered state path until reconciled.

The source-level implementation remains in:

- `/home/admin/ServersAdministartion/automation/health-incident-monitor/`
- `/home/admin/agents-projects/noticeplace/`
- `/home/admin/gptadmin/go-hub/`
- `/home/admin/agents-projects/agent-herder/`
- `/home/admin/agents-projects/hermes-config/plugins/agent-herder-bridge/`
- `/home/admin/agents-projects/agent-resume/` (compatibility fix: stop
  registering unsupported `gateway_startup`; local regression `5 passed`)

The selected-plan execution seam is now source-complete as well: NoticePlace
creates one `gptadmin.agent:health-remediation` delivery carrying the signed
plan and the fixed `Hermes/openai-codex/gpt-5.6-luna/high/health` profile;
Agent-Herder's health remediation endpoint and named-session path select the
model before the first message; the allowlisted helper rejects any altered
profile. This remains uninstalled in live `/opt`, Agent-Herder, and Hermes
runtime paths.

Staging release checks are also green: Agent Herder TypeScript and Vite output
build into disposable `/tmp` directories, GPTAdmin Hub builds into `/tmp`, and
NoticePlace dry-run packaging includes `health_workflow.py` without
`__pycache__`, `.pyc`, or Graphify artifacts. None of these staging artifacts
has been installed into a live path.

## Safe artifacts now prepared

The health producer now has an explicit per-host activation template:

- `health-incident-monitor.service`
- `health-incident-monitor.timer`
- `health-incident-monitor.env.example`
- `health-incident-monitor.json.example`
- `install.sh`
- `fleet_health_monitor.py`
- `health-incident-fleet.json.example`
- `health-incident-fleet.service`
- `health-incident-fleet.timer`
- `install-fleet.sh`
- explicit `--send-verification` for an independent recovery receipt

`install.sh` refuses activation when the scoped token or host config is absent.
It has not been executed. The installer makes the JSON config `0640` for the
runtime group and keeps the token env `root:root 0600`, so the first timer run
can read its config without exposing the token file.

## Required activation sequence

1. Build/release the source-level NoticePlace health workflow to the live
   `/opt/noticeplace` release and verify the health HTTP routes before restart.
2. Build/release GPTAdmin Hub and Agent Herder so their live binaries expose
   bounded correlation/progress paths.
3. Install the Hermes plugin through the canonical Hermes plugin/config path.
4. Create or validate a dedicated NoticePlace health producer scope with the
   `health-diagnosis` agent-job permission. Store its token only in the target
   host's `/etc/health-incident-monitor.env`.
   Also add the separate `health-remediation` job profile and GPTAdmin route;
   it is the only route allowed to consume a signed user plan selection.
5. Configure the explicit `health` Telegram mode/topic in NoticePlace. The
   target chat/topic must be supplied by the operator; no topic is guessed.
6. Reconcile the existing `fleet-health` resource notifier with the new
   per-host producer to avoid duplicate resource alerts. Do not enable both
   routes for the same signal without a dedup/routing decision.
7. Install the updated `agent-resume` plugin source and repair the Hermes service definition using the official Hermes control
   plane, then verify the active unit, dashboard/health endpoint, Agent-Herder
   adapter path, and selected provider/model. The official CLI currently
   recommends `hermes gateway restart`, which is a restart boundary and was not
   run.
8. Select exactly one collection mode per target: install the local producer on
   approved Linux hosts, or install the central fleet adapter on the controller
   using `health-incident-fleet.json`. The central adapter is the path for
   Pythonless OpenWrt/HAOS targets and uses the same canonical event contract.
9. Run one controlled synthetic degraded canary. This is the first action that
   may send an external Telegram message and start a real remediation agent.
   Capture: incident ID, exactly three plans, user callback, selected runtime
   session, useful progress, independent verification, elapsed time, and final
   trace bundle.

## Approval boundaries

The following actions have not been performed and require direct approval at
the moment of action:

- writing `/opt` releases or production service/plugin files;
- installing or changing secret-bearing env files;
- `systemctl daemon-reload`, enable, restart, or Hermes gateway repair;
- changing Telegram topic/mode routing;
- starting a real GPTAdmin/Agent-Herder/Hermes remediation job;
- sending a real Telegram canary message;
- any production restart, deploy, rollback, or destructive action.

Until those actions are approved and verified, the truthful state is
“source-level implementation complete; production business canary not yet
activated.”

## Fleet-managed preparation receipt (2026-08-09)

The health collector preparation was executed through the live Agent Harness
Fleet workflow `health-incident-monitor`, using its exact SSH target `100`
(`22104`) and the preview confirmation
`sha256:937da5a39f1131fc3e3d1432df46a99d9785c3562d318e3594929986d4d0156d`.
This is the canonical Fleet/SSH path; no local root copy was used.

Fleet installed and independently verified these four artifacts on the
controller:

- `/opt/health-incident-monitor/health_monitor.py`
- `/opt/health-incident-monitor/fleet_health_monitor.py`
- `/etc/systemd/system/health-incident-fleet.service`
- `/etc/systemd/system/health-incident-fleet.timer`

The Fleet verify result was `verified` with `mismatches=[]` and digest
`sha256:ecde455e59b691d8b2573a65c62cb3add3a0b5bcb13730114253eb07fc2afc88`.
The workflow created a recoverable backup root at
`/var/backups/health-incident-monitor/ecde455e59b691d8b2573a65cb3add3a0b5bcb13730114253eb07fc2afc88`.
No existing artifact needed backup because all four targets were absent.

The workflow is deliberately `prepare-only`: `daemon-reload`, timer
enable/start, config/token installation, migration of the existing
`fleet-health.timer`, and external Telegram delivery all remain unperformed.
The preflight still cannot be ready for the business canary until the scoped
config/token and the explicit migration/timer boundary are completed. The
Telegram destination remains the `Health` topic (internal key `health`).

## Post-batch live evidence

After the approved artifact batch and controlled restarts:

- `notification-center.service`, `agent-herder.service`, and
  `hermes-gateway.service` are `active/running` by direct systemd inspection;
- NoticePlace is listening on its configured local port `8091`; `/health`
  returns `401` without the dedicated token, proving the route exists while
  leaving authenticated readiness unclaimed;
- Agent-Herder serves `/api/health/remediation`; a read-only local GET returns
  `405 method_not_allowed`, proving the installed route without creating a
  job or issuing a mutating probe;
- Hermes reports `agent-herder-bridge` enabled, version `0.1.0`, user source;
- OmniRoute health remains HTTP `200`; OpenCode remains process-active but
  authenticated readiness is still unproven;
- the final read-only preflight is `ready_for_external_canary=false` with only
  these blockers: NoticePlace authenticated readiness, OpenCode authenticated
  readiness, and health collector config/token plus active timer.

The preflight was corrected during this slice to probe NoticePlace on `8091`
instead of the unrelated GPTAdmin hub port `9001`, and to inspect the current
Agent-Herder web bundle (`dist/web/server.js`) for the remediation route.

The final Agent-Herder route hardening is also live: the service now answers a
read-only `GET /api/health/remediation` with `405 method_not_allowed`; the
latest atomic dist switch preserved the prior runtime at
`/home/admin/agents-projects/agent-herder/dist.backup-20260809T154620Z`.

## Fleet config receipt and probe correction (2026-08-09)

The non-secret central health manifest was then installed through the same live
Fleet workflow over SSH; no local `/opt` copy was used. The exact preview
confirmation was `sha256:aebae8cb2a463433fb397b1a4b27e40a1a67defc966c799036e77c639af5fe25`.
Fleet apply returned `prepared`, and verify returned `verified` with
`mismatches=[]`, source digest
`sha256:e96fd1e9dfe41fa55e6909eb6ecfcc9142ec13ecfcbf1d32112786aa3d659eca`.
The verified config is `/etc/health-incident-fleet.json`, mode `0640`, owner
`root:admin`; its values are non-secret.

The first verify receipt falsely reported `configFilePresent=false` because
the remote probe used the non-portable `test -s -- path` form. This was fixed
in the Fleet runner to `test -s path`, covered by a regression, and re-verified
live: `configFilePresent=true`, `envFilePresent=false`, legacy
`fleet-health.timer` active, new health timer inactive, and both
`daemonReload` and `externalSend` still `not-run`. Fleet workflow/LHC tests are
now `16 passed`.

This leaves the expected activation gates only: create/install the scoped
health producer credential, reconcile the old timer and activate the new timer,
prove authenticated NoticePlace/OpenCode readiness, then prepare an explicit
Telegram `Health` topic canary. No external message or remediation job has
been sent.

The fresh production preflight remains `ready_for_external_canary=false`:
`fleet_config_file=true` and `fleet_config_readable=true`, but
`fleet_env_file=false`, `health-incident-fleet.timer=inactive`, and no
successful recent collector run exists. NoticePlace is `401` without its
credential, OpenCode is `401` without authenticated readiness, Agent-Herder's
read-only route is `405`, and OmniRoute health is `200`.

## Current superseding activation receipt (2026-08-09)

The earlier tables in this document are the pre-activation snapshot. The
approved Fleet/SSH credential-and-timer boundary has since been applied to
server-100 and verified:

- health scope and Agent Job profiles are present;
- `health-incident-fleet.timer` is active/enabled;
- legacy `fleet-health.timer` is inactive/disabled;
- `notification-center.service` is active;
- `NOTIFY_CENTER_HEALTH_TOKEN` is present and matches the health scope token;
- external Telegram remains `not-run`/fail-closed.

NoticePlace, including the health workflow and two-stage diagnosis helper, was
deployed with backup-first copies and matching source/live hashes. A real
synthetic v8 canary reached `health.plans_attached`:

| Receipt | Value |
|---|---|
| Incident | `inc_46980135c4e643f0bb3464727beb3e0d` |
| Correlation | `corr:synthetic-two-stage-v8:1786307063` |
| Diagnosis | `omniroute/subagent`, `6288 ms` |
| Logical orchestrator | requested `omniroute/orchestrator`; effective `omniroute/free-stack` |
| Orchestrator | `12775 ms` |
| Stage total | `19063 ms` before callback |
| Trace proof | both Agent-Herder and OpenCode refs for both sessions |
| Telegram | both deliveries cancelled: `Telegram mode is inactive` |

This proves the diagnosis/planning vertical slice only. It intentionally does
not select a plan, start remediation, send Telegram externally, or claim
resolution. The remaining acceptance boundary is an explicit user choice of
one of the three plans, followed by controlled remediation, useful-progress
supervision, independent source verification, and a resolved receipt with
elapsed time and trace IDs.

## Hermes remediation activation update (2026-08-10)

The local Agent-Herder service now runs the installed Hermes CLI for the
remediation profile. Its explicit service environment is provider
`openai-codex`, reasoning `high`, terminal-only toolset, and the installed
Hermes binary path. The health Fleet profile was previewed and applied through
the controller with confirmation
`sha256:b55c2e15b4ce88199876f2650bec00b9b3fb91bb80e4ace04125bfb9cf0bd5b3`,
then verified remotely as `harness=hermes`, `gpt-5.6-luna`, `high`, topic
`health`.

The live synthetic canary reached terminal `stopped` through the actual
Agent-Herder → Hermes path and returned
`HERMES_AGENT_HERDER_CANARY_OK`; native Hermes trace ID:
`20260810_000540_1484a9`. It was no-tool, no-file-mutation, and no-send.
External Telegram remains fail-closed. The production business canary still
requires the user-facing Health-topic plan selection, controlled repair,
independent source verification, and the final resolved NoticePlace receipt.

## Hermes review correction (2026-08-10)

The Hermes remediation source now has a request-bound canonical execution
profile, bounded redacted CLI trace export, real CLI-output progress, and a
20-minute SIGTERM/SIGKILL watchdog. The tracked Agent-Herder systemd unit
contains the same non-secret Hermes environment as the local drop-in. Build,
focused tests (`11 passed`), and the full Agent-Herder suite (`105 passed`)
are green. The previous live PID predates this build; restart and a fresh
post-restart canary are therefore still deployment-gated. No Telegram send was
performed.

## Current-dist Hermes canary (2026-08-10)

The fresh isolated `dist` canary completed with accepted-to-stopped Hermes CLI
delivery, native session `20260810_003911_bcf369`, observable progress and 10
evidence refs. A parser correction now supports the CLI's progress-visible
`Session: <id>` receipt. No production restart or Telegram send occurred; the
live unit still requires the explicit restart boundary.

## NoticePlace Hermes profile correction (2026-08-10)

The source helper was corrected to accept the applied Fleet remediation
profile (`hermes`, `gpt-5.6-luna`, `high`, `health`) and reject the legacy
OpenCode/model shape. NoticePlace focused health/job tests pass (`37 passed`).
The correction is committed as `1ee7365`; the live `/opt/noticeplace` copy has
not been replaced and notification-center has not been restarted.

## Isolated NoticePlace to Hermes handoff (2026-08-10)

The corrected source helper successfully dispatched a no-send health-remediation
canary to a fresh isolated Agent-Herder process. Native Hermes session:
`20260810_005140_cc3024`; terminal `stopped`; progress and 10 evidence refs
were present. This does not authorize or prove the live `/opt/noticeplace`
deployment, notification-center restart, Telegram delivery, or user-facing
plan selection.
