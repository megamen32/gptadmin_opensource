# Dual-agent speed and `all.bezrabotnyi.com` source trace

Role: Lead

## Original request

> а какя скорость? запусти 2 сабагента(один с нашим инструментом, второй голый) сделав аптайм и пусть найдут кто запускает all.bezrabotnyi.com (какая исходная папка проекта)

## Objective

Run two independent read-only investigators in parallel: one through the temporary GrepMesh MCP and one with ordinary shell/system inspection only. Compare lookup latency and identify the real process/ingress owner and source project directory for `all.bezrabotnyi.com`.

## Business canary

Both agents must return an evidence-backed chain for `all.bezrabotnyi.com`, including uptime snapshot, elapsed lookup time, upstream/process owner, and absolute source project path. If either cannot prove the path, it must state the exact missing link.

## Confirmed scope

- Current host: `/home/admin/gptadmin` / server-100.
- Read-only diagnosis and temporary local GrepMesh MCP process only.
- Two independent agents: GrepMesh Inspector path and bare system-tools path.
- Compare elapsed wall time, not claim that parallel wall time equals individual search cost.

## Explicit exclusions

- No deployment, restart, stop, config edit, DNS/TLS change, firewall/ACL change, or external mutation.
- Do not inspect or echo secrets, `.env`, cookies, SSH keys, or credentials.
- Do not use the bare agent's result as MCP proof or the MCP agent's result as proof of service ownership without the real consumer chain.

## Estimate

- Initial active-minute estimate: 15 / 35 / 75 active minutes (optimistic / likely / pessimistic).
- Estimate revisions: none yet.

## Stop conditions

- `stop_when`: both agents return their timing, uptime, and source-owner evidence, or an exact blocker.
- `abandon_when`: the real owner cannot be established from read-only local evidence without privileged/secrets/external mutation.
- `forbidden_without_explicit_user_request`: any restart, deployment, DNS/ingress mutation, privilege/ACL change, or secret access.

## Progress

- [x] Start temporary GrepMesh MCP canary.
- [x] Run GrepMesh-tool agent and bare agent.
- [x] Compare speed and reconcile source-owner evidence.
- [x] Stop temporary canary and report.

## Evidence — 2026-08-11

- GrepMesh Inspector agent: `tools/list` 5110 ms; bounded `search_text`/`read_text` calls were about 5271/5222 ms. `uptime`: `up 3 days, 20:02`, load `49.00, 48.20, 47.17`. It found `/etc/nginx/sites-available/all.bezrabotnyi.com.conf` and `proxy_pass http://203.0.113.10:8098`, but correctly returned `NOT PROVEN` for the remote source path.
- Bare agent: first bounded ingress scan 272 ms; focused local trace 6678 ms. `uptime`: `up 3 days, 19:58`, load `47.22, 45.89, 46.22`. It independently found the same nginx route and excluded unrelated local `smartdns` PID 434725.
- The direct route lookup is about `19.38x` slower through this cold temporary MCP path (`5271 ms` vs `272 ms`, +4999 ms). This includes MCP/Inspector and a cold `rg` root scan, so it is not a warmed steady-state benchmark. Two MCP calls (tools/list + read_text) were 10332 ms versus the bare focused trace 6678 ms.
- Lead read-only remote probe on SSH alias `88` completed the chain: `203.0.113.10:8098` is PID `3389`, `/usr/bin/node /opt/mobile-browser/src/index.js`; cgroup `/system.slice/mobile-browser.service`; unit `/etc/systemd/system/mobile-browser.service`; `WorkingDirectory=/opt/mobile-browser`; `User=mobileproxy`; `ActiveEnterTimestamp=2026-08-07 05:47:14 MSK`. `/opt/mobile-browser/src/index.js:184-185` defaults the port to `8098`.
- The source project directory is proven: `/opt/mobile-browser` on server-88. `bookstream.service` and `swarm.service` were false candidates; their ports/working directories differ.
