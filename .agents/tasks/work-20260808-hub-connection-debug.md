# Hub connection debug and Custom GPT E2E

Status: in_progress

## Исходный запрос

Починить текущий Custom GPT/GPTAdmin путь и добавить нормальный сбор всей информации о подключениях в Hub для диагностики.

## Objective

Дать оператору единый secret-safe snapshot всех Hub/virtual/real/child MCP connections с topology, heartbeat age, jobs, trace-linked audit и build/runtime evidence; затем использовать его для доведения Custom GPT browser E2E до подтверждённого результата.

## Business canary

Canonical Hub отвечает на `/version` и `/healthz`; authenticated connection-debug snapshot возвращает BrowserClaw и relay/job evidence; ChatGPT browser opens the user's GPT and a real `uptime` Action call completes.

## Explicit exclusions

Не выводить токены, пароли, bearer values, command arguments или содержимое файлов; не подменять реальный Custom GPT E2E изолированным HTTP smoke-тестом.

## Initial active-minute estimate

60 active minutes.

## План

1. Зафиксировать текущую registry/relay/auth границу отказа.
2. Добавить red-тест и secret-safe `/admin/api/connection-debug` snapshot.
3. Собрать, раскатать и проверить оба Hub runtime.
4. Повторить browser Custom GPT → Action → `uptime` и закрыть оставшийся relay/failover дефект.

## Evidence

- Added `GET /admin/api/connection-debug?limit=200&server_id=...`, ctl-protected, with Hub build/resource/transport, canonical published connection graph, heartbeat age, status/kind/transport counts, safe job summaries, and recent redacted audit fields including trace IDs.
- Added focused tests for complete snapshot, bearer/authorization/command redaction, ctl authorization, and limit validation.
- `go test ./...` passed before rollout.
- Commits pushed: `a23cb87` (implementation), `c75003a` (build 147 metadata), `6c116b6` (API docs).
- Server-100 live `/version`: build 147 / `a23cb87`; authenticated debug snapshot: 34 connections, 10 online, 24 stale, BrowserClaw child online, no stuck jobs after bounded relay calls.
- HAOS standby live `/version`: build 147 / `a23cb87`; authenticated debug snapshot: 9 connections, BrowserClaw child stale with process `stopped` and protocol `unknown`. This proves failover registry/transport state is not converged with server-100; blindly marking it online would be false because Mac polls the primary Hub.
- GPTADMIN `discover` recovered after rollout and returned Hub plus BrowserClaw online.
- BrowserClaw direct local MCP `initialize` returned HTTP 200, protocol `2025-03-26`, server `browserclaw 0.0.14`, and tools list.
- BrowserClaw opened authenticated ChatGPT `/gpts` and `/gpts/mine`; browser snapshot showed account `Nic Rozanov Pro` and the private GPT `Admin write code / control servers`.
- Real Custom GPT conversation and its `uptime` Action have not yet been confirmed: relay `tabs` calls intermittently become bounded background jobs, while direct local BrowserClaw UI calls work. The remaining defect is the primary/standby relay/session convergence boundary, not TLS or BrowserClaw process death.
- FRP investigation found duplicate ownership of the canonical subdomain on server01 and Mac mini (`com.gptadmin.tunnel-frpc`); both duplicate tunnel services were stopped, while Mac ShellMCP/BrowserOS remained running. HAOS fallback was reclaimed and its generated failover config was disabled to stop the promotion loop while the canonical DNS target is unavailable.
- Server-100 primary and VPN2 FRP clients now register successfully without `router config conflict`; independent-child supervisor fix is pushed as `1169ce5`, with `23 passed` focused tunnel/failover tests.
- Canonical DNS still resolves `your-subdomain.t.became.bezrabotnyi.com` to unavailable VUSA `185.240.120.152`, so public URL `/version` and `/healthz` time out. The same Host routed directly to server-100 `203.0.113.10:443` returns build 147, proving the Hub/FRP primary route itself is live but DNS ingress is wrong.
- Real BrowserClaw UI E2E was exercised on Mac: authenticated ChatGPT opened the private Admin GPT, accepted `uptime`, and remained stuck on `Stop generating` for 30 seconds with no assistant/action result. This is a confirmed red Custom GPT E2E, not a passing canary.
- Root cause of the relay red state was confirmed in the live Mac child definition: BrowserClaw was configured as `/usr/local/bin/npx -y mcp-remote http://127.0.0.1:9210/mcp`, while the live BrowserClaw MCP endpoint is `http://127.0.0.1:9010/mcp`. `mcp_tools` returned `EOF` before correction.
- Corrected only the live child definition through `mcp_manage upsert` to use `http://127.0.0.1:9010/mcp`. Follow-up GPTAdmin calls succeeded: `mcp_tools` returned the BrowserClaw catalog (17 tools), and `mcp_call tabs {action:list}` returned the authenticated ChatGPT tabs.
- Final real browser canary passed through the full path: BrowserClaw via GPTAdmin opened the private Admin Custom GPT, entered `uptime`, ChatGPT displayed the explicit Action consent for `shell:admin-server-100`, consent was accepted, and the assistant returned `admin-server-100: 17:22:18 up 1 day, 8:08, 1 user` plus load averages. The response was read from the rendered ChatGPT page via BrowserClaw, not from Hub internals.
- Post-canary GPTAdmin discovery shows `hub`, `shell:admin-server-100`, `shell:mac-mini-2012.lan`, `BrowserOS on admin-server-100`, and `mcp:shell:mac-mini-2012.lan:mac-mini-browserclaw` online. Child status reports BrowserClaw enabled/running with PID 19817 and the corrected 9010 endpoint.
- Public authoritative DNS now returns only `203.0.113.10` for the canonical hostname; direct HTTPS `--resolve` to that address returns HTTP 200 and build 147. The live certificate is the newly issued public certificate for the canonical hostname, replacing the incident-fallback certificate. Source TLS-root handling fix is pushed as `f9d4cf3` and deployed to Mac ShellMCP.
