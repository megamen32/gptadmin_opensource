# Production rollout and live acceptance — 2026-08-10

## Original request

Deploy the scoped GPTAdmin change and test the live Plugin Flow and Custom GPT Flow after rollout.

## Objective

Install the exact scoped commit in the production Hub, preserve rollback material, and verify both parallel ChatGPT integration paths through the real BrowserClaw surface and public relay.

## Business canary

- Public Hub reports the deployed commit and remains healthy.
- Custom GPT completes Discover/tools/call relay activity with HTTP 200.
- Plugin Flow selects GPTADMIN and completes a bounded uptime response.

## Confirmed scope

- Production `gptadmin-hub.service` on server-100.
- Public origin `https://your-subdomain.t.became.bezrabotnyi.com`.
- BrowserClaw on Mac mini through `whitetransport-mac-mini-2012`.
- Both Plugin Flow and Custom GPT Flow remain parallel paths.

## Explicit exclusions

- No unrelated dirty worktree changes.
- No Custom GPT configuration changes during this rollout.
- No secret, token, cookie, or transcript capture.

## Initial active-minute estimate

20 minutes.

## Evidence

- Exact commit `85d9eaac1f6eaa3679828ef86c1dacebf7825f2c`, build `153`.
- Production binary SHA256 `39931290e7645e793d3722c718ad271a1ca175d8588cab4b9dc3b4469f165f60`.
- Rollback backup: `/opt/gptadmin/bin/gptadmin_hub.bak.85d9eaa-20260809T222213Z`.
- Service active after restart; public `/version`, `/healthz`, and `/actions/openapi.yaml` returned 200.
- Custom GPT post-deploy receipt: `trash/logs/admin-chat-post-deploy-20260810.json`; Discover 2, tools 2, calls 7, all observed call statuses 200.
- Plugin all-servers attempt produced public `servers/tools/call` and downstream result HTTP 200, but ChatGPT did not finish rendering within 240 seconds.
- Failure screenshot `trash/logs/plugin-flow-failure-20260810.png` showed the real cause: ChatGPT was still tracking 16 targets, with 7 stale and 1 awaiting approval, at `Проверка времени работы сервера`.
- Plugin short canary completed: GPTADMIN selected, uptime marker observed, response finished.

## Result

Production rollout passed. Both integration paths reach the live Hub; the bounded short Plugin Flow canary passed. The long all-servers Plugin response is blocked by unbounded waiting on stale and approval-gated targets, not by browser rendering.
