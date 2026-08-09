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
- FRP investigation found duplicate ownership of the canonical subdomain on server-44 and Mac mini (`com.gptadmin.tunnel-frpc`); both duplicate tunnel services were stopped, while Mac ShellMCP/BrowserOS remained running. HAOS fallback was reclaimed and its generated failover config was disabled to stop the promotion loop while the canonical DNS target is unavailable.
- Server-100 primary and VPN2 FRP clients now register successfully without `router config conflict`; independent-child supervisor fix is pushed as `1169ce5`, with `23 passed` focused tunnel/failover tests.
- Canonical DNS still resolves `u-f1102930.t.gptadmin.bezrabotnyi.com` to unavailable VUSA `185.240.120.152`, so public URL `/version` and `/healthz` time out. The same Host routed directly to server-100 `95.165.165.65:443` returns build 147, proving the Hub/FRP primary route itself is live but DNS ingress is wrong.
- Real BrowserClaw UI E2E was exercised on Mac: authenticated ChatGPT opened the private Admin GPT, accepted `uptime`, and remained stuck on `Stop generating` for 30 seconds with no assistant/action result. This is a confirmed red Custom GPT E2E, not a passing canary.
