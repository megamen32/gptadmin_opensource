# Tester: BrowserOS user-facing health surface

## Role

Fresh black-box Tester. Do not inspect source code, project task files, internal architecture, service logs, databases, or hidden configuration. Use only the user-facing computer-use surface exposed by canonical BrowserOS MCP at `http://127.0.0.1:9000/mcp`; this is the Linux equivalent of the Mac BrowserClaw surface.

## User-facing objective

Determine whether a real user can reach a visible health/operations surface and observe a safe no-send health canary through the browser UI. The underlying business goal is host/service/log health → diagnosis → exactly three plans → explicit choice → controlled remediation → verification → resolved receipt.

## Safety boundary

- Do not send Telegram or any external message.
- Do not start Hermes egress.
- Do not click irreversible controls, choose a remediation plan, restart services, or mutate infrastructure.
- Do not use Touchpoint, direct APIs, databases, shell internals, or source inspection as a substitute for computer use.

## Method

Use the real BrowserOS MCP tools in a fresh session: list current tabs, open only a safe read-only user-facing page if necessary, inspect the rendered UI, and report only what a normal user can see. If the required surface is unavailable or the transport cannot perform semantic UI actions, return exactly `STOP_MISSING_REAL_SURFACE` and include a concise safe reason.

## Acceptance

PASS only with observable user-facing UI evidence for a safe no-send health path. A healthy MCP handshake alone is not PASS. Do not infer business completion from backend/service status.

## Tester evidence — 2026-08-10

- Surface required by this task: canonical BrowserOS MCP at `http://127.0.0.1:9000/mcp`, in a fresh session.
- Attempt: inspected the computer-use tools exposed to this Tester session before taking any UI action. No BrowserOS MCP/tab/session/semantic browser tools were exposed. The only exposed desktop computer-use namespace was `mcp__touchpoint__*`, which this task explicitly forbids.
- Result: no page was opened and no rendered health/operations UI or safe no-send health canary was observable. No external message, Hermes egress, remediation choice, restart, or infrastructure mutation occurred.
- Verdict: `STOP_MISSING_REAL_SURFACE`.
- Safe reason: the required canonical BrowserOS real-user transport is unavailable to this Tester session; using Touchpoint, shell, direct APIs, or source inspection would violate the black-box boundary. Smallest unblock is to expose an authenticated canonical BrowserOS MCP session/tool surface, then rerun this Tester.

## Tester evidence — 2026-08-10 (fresh rerun)

- Surface required by this task: canonical BrowserOS MCP at `http://127.0.0.1:9000/mcp`, in a fresh session.
- Attempt: inspected the tools exposed to this Tester session before taking any UI action. The available computer-use tools were limited to `mcp__touchpoint__*`; no canonical BrowserOS MCP, browser tab/session, or semantic BrowserOS tools were exposed.
- Result: no page was opened and no rendered health/operations UI or safe no-send health canary was observable. No external message, Hermes egress, remediation choice, restart, or infrastructure mutation occurred.
- Verdict: `STOP_MISSING_REAL_SURFACE`.
- Safe reason: the required canonical BrowserOS real-user transport is unavailable to this Tester session; using Touchpoint, shell, direct APIs, or source inspection as a substitute would violate the black-box boundary. Smallest unblock is to expose an authenticated canonical BrowserOS MCP session/tool surface, then rerun this Tester.

## Tester evidence — 2026-08-10 (fresh black-box run, bounded)

- Surface/tool: canonical BrowserOS MCP; existing page 23 at `https://syncllm.bezrabotnyi.com/#hosts`.
- Journey: listed existing tabs, inspected the rendered Fleet “Устройства” operations surface, then performed one safe semantic read action by clicking the first visible “Инвентаризация” control. No new tab was opened; no wait, external site, mutation, remediation, restart, Telegram send, or Hermes egress was used.
- Observed: the UI visibly exposes Fleet navigation, device inventory cards, “Предпросмотр”, “Инвентаризация”, and device-management controls. BrowserOS returned `ok (click)` but the rendered page diff reported “no change since last snapshot”; no health result, diagnosis, or safe no-send health receipt became visible.
- Verdict: `STOP_MISSING_REAL_SURFACE`.
- Safe reason: the canonical browser and operations surface were reachable, but the requested user-visible health/no-send canary could not be observed within the single permitted read action; no success may be inferred from the unchanged UI. Smallest unblock is a visible health/operations path that exposes a read-only health result/receipt and rerun the bounded Tester.
