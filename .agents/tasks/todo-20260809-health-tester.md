# Tester task: black-box health incident operator acceptance

Role bootstrap: Tester. This is a fresh context-free acceptance pass. Do not read source files, task indexes, implementation docs, or use a terminal/API/client to inspect internal state.

## User-facing target

Open this disposable local operator URL in the desktop browser:

`http://127.0.0.1:26959/admin/`

The page is a local fixture using a disposable database. It has no real delivery adapter, no production credentials, no Telegram send, and no infrastructure mutation.

## Black-box mission

Act as an operator who only knows the visible page. Using computer-use/browser interactions only:

1. Confirm the page identifies itself as NoticePlace and presents an operator console.
2. Find the visible event-history area and inspect the incident for `node-blackbox`.
3. Verify from visible text that the workflow shows a health signal, three plan choices/plan attachment, a selected plan, progress/evidence, independent verification, and a resolved outcome with correlation/trace information where exposed.
4. Use the visible history filter if useful, but do not submit any configuration, adapter-test, Telegram, call, or topic form.
5. Report PASS only if the visible operator experience is coherent and the final state is visibly resolved; otherwise report FAIL with the exact user-visible blocker.

Append an evidence-backed verdict to this task file. Do not change product code.

Status: done

Verdict: PASS — fresh BrowserOS computer-use session confirmed the user-facing
NoticePlace console and filtered `node-blackbox` history through health signal,
three plans, selection, progress, verification, and resolved state. No
mutating forms or external delivery were submitted.

## Tester evidence (2026-08-09)

- Surface/tool: BrowserOS desktop browser, fresh owned tab opened at `http://127.0.0.1:26959/admin/`; no source, API, shell, logs, or internal state inspected.
- Journey: confirmed the visible `NoticePlace` heading and `Protected operator console`; located `Event history`; clicked the visible incident link, then entered `node-blackbox` in the visible history filter and clicked `Filter`.
- Filter result: URL became `http://127.0.0.1:26959/admin/?history=node-blackbox`; the visible table retained six rows for incident `inc_5c1952c1dffd44f99d1111b352f74157`.
- Visible workflow evidence: `health.disk` shows `Host node-blackbox is degraded` from `health-monitor/health-incident-monitor`; `health.plans_attached` shows `omniroute / health-workflow` and `corr:blackbox-health-1`; `health.plan_selected`; `health.progress`; `health.verification_recorded`; and `health.resolved` are all visible for the same incident.
- Visible final state: outcome column shows `resolved`; the resolved event is accompanied by event id `evt_10c6c586dc374284a1322403431bbf65`, incident id, direct proxy metadata, and notification/cancellation details. The plan attachment row exposes correlation `corr:blackbox-health-1`, and the initial health row exposes `node-blackbox`.
- Side-effect boundary: did not submit producer, adapter-test, Telegram, call, live-settings, consumer, or topic forms.

Verdict: PASS

## Fresh post-deploy pass — authoritative current target (2026-08-10)

The earlier fixture evidence is historical. Run a new context-free Tester pass
against the disposable fixture at `http://127.0.0.1:26959/admin/`, which was
started after NoticePlace commit `3f3b771` was deployed. Use only the visible
browser surface and computer-use interactions; do not read source, task
indexes, implementation docs, shell output, APIs, databases, or service state.

Confirm only what a normal operator can see: NoticePlace identity, the
`node-blackbox` health incident, exactly three visible plans, selection,
progress/evidence, independent verification, resolved result, correlation and
trace information where displayed. Do not submit any mutating form or any
Telegram/topic/call/adapter action. Append a fresh evidence-backed PASS or
FAIL/STOP verdict to this file and return only a concise TL;DR.

## Fresh post-deploy Tester evidence (2026-08-10)

- Surface/tool: fresh owned BrowserOS desktop-browser tab opened at `http://127.0.0.1:26959/admin/`; only visible page snapshots, page text, visible-content search, and browser actions were used. No source, task indexes, implementation docs, shell output, API, database, or service state was inspected.
- Journey: confirmed the visible `NoticePlace` heading and `Protected operator console`; located `Event history`; clicked the visible incident link, then filled the visible history filter with `node-blackbox` and clicked `Filter`. The URL became `http://127.0.0.1:26959/admin/?history=node-blackbox`.
- Incident result: the visible history table retained seven rows for incident `inc_ba5181bf92354ab78a46a962efa63a6b`, including `health.disk`, `health.plans_attached`, `health.plan_selected`, `health.progress`, `health.verification_recorded`, `health.remediation_requested`, and `health.resolved`.
- Resolved-path evidence: `health.disk` visibly says `Host node-blackbox is degraded`; the final row visibly shows `health.resolved`, outcome `resolved`, `node-blackbox`, event id `evt_806e3f002adc444fa0ae9536e255f085`, incident id, `proxy: direct`, and notification/cancellation details. `health.plans_attached` visibly exposes correlation `corr:blackbox-health-1`; the selected, progress, and independent-verification event types are visible for the same incident.
- Blocker: the visible `health.plans_attached` row shows only one plan attachment, `omniroute / health-workflow`. A BrowserOS visible-content search for `plan` returned only `health.plan_selected` and `health.plans_attached`; no three distinct plan choices or three plan names are visible to the operator. The separate `Delivery profiles` entries (`Emergency`, `Important`, `Log`) are not presented as the incident's three plans and were not counted.
- Smallest in-scope repair: expose exactly three incident plan choices/attachments in the visible operator history or incident view, while retaining the selected-plan, progress, verification, resolved, correlation, and trace information.
- Side-effect boundary: no producer, adapter-test, Telegram, call, live-settings, consumer, or topic form was submitted.

Verdict: CHANGES_REQUIRED — the real browser surface is available and the incident resolves visibly, but the acceptance requirement for exactly three visible plans is not met.

## Fresh context-free BrowserOS acceptance pass (2026-08-10, current)

- Surface/tool: fresh owned BrowserOS desktop-browser tab opened at `http://127.0.0.1:26959/admin/`; all product observations came from rendered page snapshots and visible-content search through the canonical BrowserOS MCP surface. No Touchpoint, direct HTTP/API, shell, source, logs, databases, hidden configuration, or service state was used.
- Identity: rendered page visibly shows `NoticePlace`, an operator-console layout, and the `Event history` section.
- Main journey: inspected the rendered event-history table for the visible `node-blackbox` health incident. The current visible incident has one incident id, seven event rows, and a final `health.resolved` row with outcome `resolved`.
- Health signal: `health.disk` visibly shows `Host node-blackbox is degraded` from `health-monitor/health-incident-monitor`.
- Exactly three plans: each visible event-history row exposes `Health plans (3): observe: Observe repair: Repair verify: Verify`; the three visible plan names are exactly `observe`, `repair`, and `verify`.
- Workflow evidence: visible event types include `health.plans_attached`, `health.plan_selected`, `health.progress`, `health.verification_recorded`, and `health.resolved` for the same `node-blackbox` incident.
- Correlation/trace evidence: the visible plan-attachment row shows `corr:blackbox-health-1`; event rows visibly include event and incident identifiers plus `proxy: direct` metadata. The resolved row visibly shows `resolved` and `node-blackbox`.
- Side-effect boundary: no producer, adapter-test, Telegram, call, live-settings, consumer, or topic form was submitted; no remediation or infrastructure action was initiated.

Verdict: PASS — the fresh real-user BrowserOS surface is available and the visible `node-blackbox` history coherently shows the health signal, exactly three plans, selection, progress, independent verification, correlation/trace metadata, and resolved outcome.
