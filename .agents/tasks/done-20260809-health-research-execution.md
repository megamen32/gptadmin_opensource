# Research lane: Hermes, OpenCode, OmniRoute execution and supervision

Role: Explorer
Status: complete
Owner: Explorer
Parent task: work-20260809-health-monitoring-orchestration.md

## Goal

Establish how a selected remediation plan can currently start and control an
OpenCode/Codex/Hermes execution through OmniRoute, how GPT-5.6-Luna high or
equivalent routing is represented, and what durable progress/trace/completion
signals exist. This is read-only research only.

## Allowed paths

- `/home/admin/agents-projects/hermes-config`
- `/home/admin/agents-projects/opencode-omniroute-models`
- `/home/admin/agents-projects/omniroute-zcode`
- `/home/admin/agents-projects/LastHumanCommit/adapters/hermes`
- `/home/admin/agents-projects/LastHumanCommit/adapters/opencode`
- Directly referenced docs/tests in those roots.

## Excluded paths and actions

- Do not edit source, configs, task files, generated files, or runtime state.
- Do not start agents, send Telegram messages, restart, deploy, change
  providers/secrets, or inspect secret values.
- Do not claim that an installed runtime is configured merely because a plugin
  or model alias exists.

## Acceptance check

Return an evidence table for plan selection → execution start → session
supervision → progress/heartbeat → stop/resume/failure → resolved report,
including exact files/lines, model/provider routing, transport, trace fields,
and missing links. State what can be implemented through supported plugin/config
seams without forking Hermes or overwriting official OpenCode.

## Budget and stop conditions

- Model: gpt-5.6-luna explorer, low reasoning
- Active minutes: 30 / 55 / 110
- Relative cost: low; read-only search and bounded file inspection
- Stop when the execution/supervision evidence and gaps are proven or when the
  next fact requires runtime mutation or provider authorization.
- Return `NEEDS_REDECOMPOSITION` if source ownership or runtime topology is
  ambiguous beyond the allowed paths.

## Report contract

Append detailed evidence and result to this file. Return L only a TL;DR with
status, proven execution path, supported extension seams, and exact blockers.

## Explorer evidence (2026-08-09)

### Findings

| Stage | Proven mechanism | Evidence | Gap / qualification |
|---|---|---|---|
| Plan selection | Hermes can select a configured provider/model and has an enabled `agent-resume` plugin; OpenCode adapter supports native Markdown profiles and adapter-dependent model/resume transport. | `hermes-config/runtime/config.yaml:1-10,390-415`; `LastHumanCommit/adapters/hermes/adapter.yaml:9-15`; `adapters/opencode/adapter.yaml:7-13`. | No allowed-path contract maps a health-plan ID to a durable OpenCode/Hermes execution record. |
| Execution start | Hermes's external Agent-Herder bridge is update-safe and uses supported `register_tool`/`register_hook`; lifecycle hooks export `session_identity`, `subagent_start`, and `subagent_stop`. | `hermes-config/plugins/agent-herder-bridge/README.md:3-14,29-31`; `__init__.py:121-145`. | Bridge observes Hermes lifecycle and creates only a human-request record; it does not start/control a turn. |
| OmniRoute transport | OmniRoute exposes OpenAI-compatible `POST /api/v1/chat/completions`; WebSocket supports request frames and `{type:"cancel"}`. | `omniroute-zcode/public/openapi.yaml:1079-1094`; `:1156-1169`. | This is provider/completion transport, not a durable agent-session control plane. |
| Model/provider routing | Hermes config has an OmniRoute base URL and a Hermes100-OmniRoute preset with direct models plus `auto/best-fast`; catalog code fetches `/models`, filters `auto`/`best` selectors by default, and preserves provider/model IDs. | `hermes-config/runtime/config.yaml:5-10,198-211`; `opencode-omniroute-models/src/catalog.js:29-43,95-143,161-190`. | Presence of aliases/config does not prove installed runtime/provider authorization. No exact `gpt-5.6-luna` OmniRoute catalog entry was proven. The config's fallback reference was observed but its secret-bearing line was intentionally not reproduced. |
| GPT-5.6-Luna high/equivalent | The only exact allowed-path representation found is Hermes fallback `provider: openai-codex`, `model: gpt-5.6-luna`. | `hermes-config/runtime/config.yaml:8-10`. | No allowed-path evidence defines a separate “high” field, reasoning level, or OmniRoute mapping for Luna. |
| Supervision / progress | Agent-Herder bridge emits timestamped schema `agent-herder.bridge.v1` events with stable session/task/turn/platform and child status/duration fields. | `hermes-config/plugins/agent-herder-bridge/__init__.py:23-35,56-63`; README `:8-12,29-31`. | Hooks are fail-open and observer-grade; no append-only local trace or durable progress checkpoint is defined in these paths. |
| Heartbeat | Hermes config enables `agent-heartbeat` and points at a heartbeat prompt file. OmniRoute service-log SSE emits snapshot/live log/15-second heartbeat; WebSocket live channel also documents a 15-second heartbeat. | `hermes-config/runtime/config.yaml:399-415,431-439`; `omniroute-zcode/public/openapi.yaml:1166-1169,3447-3486`. | Heartbeat proves liveness of a transport/service stream, not execution progress or business completion. |
| Stop / resume / failure | OmniRoute WebSocket has cancel. OpenCode overlay patch proposes `POST /api/session/:sessionID/resume`; resilience patch proposes retries, fallback models, and auto-resume. | `opencode-omniroute-models/overlay-kit/patches/opencode/0001-feat-add-runtime-resilience-and-direct-tool-calls.patch:330-345,420-425`; `0001-feat-add-OpenCode-resilience-controls.patch:346-365,392-442,633-688`. | These are overlay patches/proposals, not proof of installed upstream runtime. Hermes bridge explicitly does not pause/resume/approve/resolve (`README.md:22-27`). |
| Resolved report | OmniRoute response headers expose resolved model/provider, request ID, version, latency, token counts, fallback attempts, and cost; usage API exposes analytics and call logs. | `omniroute-zcode/public/openapi.yaml:1093-1143,2495-2515`; `docs/reference/API_REFERENCE.md:78-87`. | No end-to-end join from a health plan/session ID to Hermes lifecycle, OmniRoute request ID, OpenCode session, and final business resolution was found. |

### Supported extension seams

- Hermes external plugin hooks/tools (`register_tool`, `register_hook`) are the supported seam; retain the bridge's secret-safe opaque references and observer semantics. Do not fork Hermes (`agent-herder-bridge/README.md:3-5,22-31`).
- OpenCode adapter seams are native Markdown profiles, frontmatter/permissions, and adapter-owned resume metadata; model override and resume remain adapter-dependent (`adapters/opencode/instructions.md:3-17`, `adapter.yaml:7-13`).
- OmniRoute/OpenCode integration can use the OpenAI-compatible base URL, live model catalog, response telemetry headers, and (if actually installed) the overlay-kit patches. Do not infer patch installation from patch presence.

### Excluded / not proven

No runtime calls, agent starts, provider tests, restarts, deployments, secret inspection, or Telegram delivery were performed. No evidence in the allowed paths proves a durable execution registry, plan-to-session identity, progress percentage/step contract, authenticated stop/resume control, or resolved business report. `NEEDS_REDECOMPOSITION` is not required: source ownership is clear, but the missing cross-system correlation contract is an implementation blocker.

### Highest-value next probe

Lead should inspect the actual installed Hermes `agent-resume` implementation, Agent-Herder consumer, and OpenCode runtime installation/version outside this read-only lane, then define a correlation envelope containing `plan_id`, Hermes `session_id`/`turn_id`, OpenCode `sessionID`, OmniRoute `X-OmniRoute-Request-Id`, selected/resolved provider-model, heartbeat timestamp, terminal state, and opaque resolution reference. Runtime mutation/provider authorization is required before claiming execution canary proof.

### Result

Status: COMPLETE (research evidence sufficient). Proven path is configuration-selected Hermes/OpenCode work using OmniRoute's OpenAI-compatible completion transport, with observer lifecycle hooks and response/request telemetry. Exact blockers are durable plan/session correlation, installed-runtime proof, authoritative pause/resume ownership, and a business-level completion/resolution signal.
