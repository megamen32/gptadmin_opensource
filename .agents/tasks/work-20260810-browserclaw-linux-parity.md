# BrowserClaw-equivalent Linux surface parity

Status: complete

## Original request

User reports that Touchpoint performed poorly and asks to install the same
browser surface used on Mac, and to explain how the Mac surface is checked.

## Objective

Provide a supported Linux browser-control surface with the same acceptance
properties as the Mac BrowserClaw surface: MCP handshake, tool discovery,
tab/page control, and a bounded read-only user-facing canary.

## Business canary

From a fresh context, the tester can connect to the Linux surface, complete
MCP initialize/tools-list, inspect the real TChat or Fleet page, and complete
one safe read-only action without Telegram send, Hermes egress, or destructive
browser mutation.

## Confirmed scope

- Inspect the existing Mac BrowserClaw runtime and its verification method.
- Inspect/install the supported Linux equivalent without copying a macOS app.
- Verify the Linux MCP/CDP surface using fresh-session, read-only evidence.

## Explicit exclusions

- No Telegram send, topic creation, message edit/delete, or callback.
- No Hermes egress, production deploy/restart, secret changes, or MFA/login.
- No Touchpoint substitution when the supported BrowserOS/BrowserClaw surface
  is available.

## Initial active-minute estimate

- Optimistic: 20 minutes
- Likely: 40 minutes
- Pessimistic: 90 minutes

## Estimate revisions

- 2026-08-10: likely estimate remains 40 minutes (evidence: Linux already
  has a running BrowserOS user-service and a working MCP handshake, so work is
  an in-place supported-stack refresh plus canary rather than a greenfield
  installation; the download/restart boundary can still add time).

## Acceptance boundary

Do not claim parity from a package name or a listening port alone. Require the
same protocol and bounded browser canary evidence used for Mac; if the real
surface is missing, record the exact blocker and stop before external actions.

## Evidence and result (2026-08-10)

- Mac check: SSH to `whitetransport-mac-mini-2012`, read the installed
  `BrowserClaw.app` version, then use one localhost-only tunnel to its MCP
  `127.0.0.1:9010/mcp`; POST `initialize` with protocol `2025-03-26`, POST
  `tools/list` in the same session, then perform a safe `tabs(list)` read.
  Observed BrowserClaw `0.48.1`, MCP `200`, session established, 17 tools.
- Linux result: the supported BrowserOS AppImage was already installed at
  `/home/admin/Applications/BrowserOS.AppImage` and owned by the active
  `browseros-shared.service`; the official download endpoint matched the
  installed artifact metadata, so no duplicate install or profile restart was
  justified.
- Linux parity check: MCP `http://127.0.0.1:9000/mcp` returned `initialize
  200`, protocol `2025-03-26`, server `browseros_mcp`, then `tools/list 200`
  with 23 tools including `tabs`, `snapshot`, `act`, `read`, `grep`, and
  `wait`. Fresh BrowserOS-only testers completed safe page inspection without
  Telegram or Hermes egress.
- The Codex registration now uses `http://127.0.0.1:9000/mcp`; the previous
  configuration is retained at
  `/home/admin/.codex/config.toml.bak.browseros-9000-20260810`.
- Exact macOS `.app` installation on Linux is unsupported; the installed
  BrowserOS AppImage is the platform-supported equivalent. The separate
  BrowserClaw server artifact was inspected but not added: upstream Linux x64
  `browseros-claw-server` v0.0.26 requires glibc `2.38`/`2.39`, while this
  Ubuntu host cannot execute it, and the sidecar would still create a second
  MCP/CDP owner without providing the Mac desktop UI.

Result: Linux has the Mac-equivalent supported browser-control contract. The
remaining health business blocker is the absent Telegram Health/Хил topic and
receipt, not browser transport.
