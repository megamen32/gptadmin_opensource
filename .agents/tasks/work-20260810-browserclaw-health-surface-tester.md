# Tester: Mac BrowserClaw health surface

## Role

Fresh black-box Tester. Do not inspect source code, repository task files, internal architecture, service logs, databases, or hidden configuration. Use only the real user-facing BrowserClaw MCP on the Mac mini through the SSH alias `whitetransport-mac-mini-2012`.

## User-facing objective

Check whether a normal user can reach the Health topic/surface and observe its current state without sending a message. This is the user-facing gate for the health business path: degradation → diagnosis → exactly three plans → explicit choice → controlled remediation → independent verification → resolved receipt.

## Safety boundary

- Do not send Telegram, ChatGPT prompts, or any external message.
- Do not start Hermes egress.
- Do not click remediation, restart, resolve, or other mutating controls.
- Do not use Touchpoint, direct HTTP application APIs, databases, shell internals, or source inspection as a substitute for UI.
- Do not bypass login, MFA, consent, or any operator approval.

## BrowserClaw transport

Use the Mac BrowserClaw MCP endpoint at loopback `127.0.0.1:9010/mcp` through a temporary localhost-only SSH port forward. Initialize MCP with protocol `2025-03-26`, retain the returned session id, call `tools/list`, then use only semantic BrowserClaw tools (`tabs`, `snapshot`, `read`, `grep`, `wait`, and read-only `act` if needed). Keep page ownership within the one session. Do not print session ids, cookies, headers, prompts, transcripts, or page raw dumps.

## Method

1. List existing tabs and identify only a visible user-facing Telegram/Health surface already open or a safe public page.
2. Inspect rendered UI with `snapshot`/`read`/`grep`.
3. Do not type or submit anything; no outbound call is allowed.
4. PASS only if Health is visibly reachable and its state can be observed in the UI. If the BrowserClaw transport, paired browser, login, or required user-facing Health surface is unavailable, return exactly `STOP_MISSING_REAL_SURFACE` with the bounded reason.

## Acceptance

The verdict must reflect what a fresh normal user can see. A successful MCP handshake alone is not PASS. No mutation is required or permitted.

