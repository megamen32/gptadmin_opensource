# Tester: Telegram Health topic discovery

## Role

Fresh black-box Tester. Use only the canonical BrowserOS real-user surface. Do
not inspect source code, task files, databases, service logs, environment
files, or hidden configuration.

## User-facing objective

As a normal user, inspect the already-open Telegram web surface and determine
whether the home Telegram channel has a visible Health topic, possibly named
`Health`, `Хил`, or a clearly equivalent label. Confirm only what is visible
in the rendered UI and whether an existing health incident/card can be opened.

## Safety boundary

- Do not send, edit, delete, forward, react to, or reply to any message.
- Do not create a topic or open a composer.
- Do not click remediation-plan controls or callback buttons.
- Do not start Hermes egress or any external delivery.
- If Telegram is not already visible, report the blocker; do not log in or
  navigate through a human-owned authentication boundary.

## Method

Use BrowserOS `tabs`, `snapshot`, and `read` first. If the current TChat
home/all-chat view is visible, one semantic click may open the already-visible
home channel `БЕЗРАБОТНЫЙ NEWS` (or its visible chat entry), followed only by
read/snapshot inspection of that channel's topic list. No second click is
allowed unless it opens an already-visible topic list or existing Health topic;
stop before any message mutation. Report the exact visible label, channel
context, and whether a health card/three-plan receipt is present. Do not infer
a topic from backend configuration.

## Acceptance

PASS only if the visible user-facing Telegram UI proves the Health topic and
an existing health receipt/card can be read without sending anything.
Otherwise return `STOP_MISSING_REAL_SURFACE` with the smallest safe unblock.

## Tester evidence — 2026-08-10

- Surface: canonical BrowserOS MCP, existing user tab `[21]` at `https://tgb.bezrabotnyi.com/`.
- Actions performed: `tabs(action=list)`, `snapshot(page=21)`, `read(page=21, format=text, viewportOnly=true)`.
- Visible context: Telegram TChat home/all-chat view. The rendered list includes `БЕЗРАБОТНЫЙ NEWS` and `Chat БЕЗРАБОТНЫЙ NEWS`, plus category counts (`All 74`, `БОТ 2`, `Работа ТГ 3`, `PrWork 1`, `Личные 2`, `Новые 65`, `Bloger 2`, `Не контакты`, `News 56`).
- Observed result: no visible topic list and no visible topic/card labeled `Health`, `Хил`, or a clearly equivalent label. No existing health receipt/card or three-plan receipt was readable from the current surface.
- Safety: no click performed; no composer, plan control, callback, or mutation was opened. No send/edit/delete/forward/react/reply/create action occurred.
- Verdict: `STOP_MISSING_REAL_SURFACE`.
- Smallest safe unblock: operator must leave the home Telegram channel's already-visible topic list or existing Health topic open in BrowserOS; then rerun bounded `snapshot`/`read` (and at most one click on that visible list/topic).

## Tester amendment — 2026-08-10

- A bounded follow-up may open the already-visible `БЕЗРАБОТНЫЙ NEWS` home
  channel once, read-only, to expose its topic list. It must not open a
  composer or interact with messages or callbacks.

## Tester amendment 2 — 2026-08-10

- Because `БЕЗРАБОТНЫЙ NEWS` is visibly a broadcast channel without topics, a
  fresh bounded follow-up may instead open the already-visible `Chat
  БЕЗРАБОТНЫЙ NEWS` entry once, read-only, and inspect its visible topic list.
  No message or composer interaction is allowed.

## Tester bounded follow-up — 2026-08-10

- Surface: canonical BrowserOS MCP, existing user tab `[21]` at `https://tgb.bezrabotnyi.com/`.
- Actions: `tabs(action=list)`, `snapshot(page=21)`, `read(page=21, format=text, viewportOnly=true)`; one and only one semantic click on the already-visible `БЕЗРАБОТНЫЙ NEWS` link `[ref=e7]`; then `snapshot` and `read` again.
- Result: the click opened the `БЕЗРАБОТНЫЙ NEWS` channel context (`14,198 subscribers`, visible channel posts). The post view exposed message content/reactions and the `Broadcast` composer controls, but no visible topic list and no visible topic/card labeled `Health`, `Хил`, or equivalent. No existing health receipt/card or three-plan receipt was readable.
- Safety: no send, edit, delete, forward, react, reply, create, plan-control, callback, or composer interaction occurred. No second click occurred. No Hermes egress or external delivery was started.
- Verdict: `STOP_MISSING_REAL_SURFACE`.
- Smallest safe unblock: provide an already-open visible topic list or existing Health topic/card in BrowserOS, then rerun snapshot/read only.

## Amendment 2 bounded follow-up — 2026-08-10

- Surface: canonical BrowserOS MCP, existing user tab `[21]` at `https://tgb.bezrabotnyi.com/#-1001665657457`.
- Actions: `tabs(action=list)`, `snapshot(page=21)`, `read(page=21, format=text, viewportOnly=true)`; exactly one semantic click on the already-visible `Chat БЕЗРАБОТНЫЙ NEWS` entry (`ref=e11`); then `snapshot` and `read` again.
- Result: the click opened `Chat БЕЗРАБОТНЫЙ NEWS` with visible context `49 members`, group messages, and a `Send anonymously` composer. No visible topic list and no visible topic/card labeled `Health`, `Хил`, or an equivalent label. No existing health receipt/card or three-plan receipt was readable.
- Safety: no second click; no send, edit, delete, forward, react, reply, create, plan-control, callback, or composer interaction occurred. No external delivery was started.
- Verdict: `STOP_MISSING_REAL_SURFACE`.
- Smallest safe unblock: provide an already-open visible topic list or existing Health topic/card in BrowserOS, then rerun bounded `snapshot`/`read` only.
