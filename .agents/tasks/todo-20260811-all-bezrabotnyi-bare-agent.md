# Bare agent lane: `all.bezrabotnyi.com`

Role: Explorer

## Assignment

Investigate `all.bezrabotnyi.com` using ordinary read-only system tools only: `uptime`, `ss`, `systemctl status`, local nginx/HAProxy/container/process inspection, `rg --files`/bounded `rg`, and read-only git metadata. Do not use GrepMesh, MCP Inspector, or any other project search tool. Measure elapsed wall time for the lookup. Trace the actual endpoint to the process/unit/container and identify the absolute source project directory.

## Acceptance

Return exact commands/results with uptime, elapsed milliseconds, listener/ingress/upstream/process evidence, source path, and confidence. Report `NOT PROVEN` if only a hostname or stale manifest is found.

## Exclusions

No mutation, restart, deployment, DNS/TLS change, privilege escalation, secret access, or external HTTP mutation. Append the detailed report to this task file and return only TL;DR to Lead.

## Budget

Explorer, low effort, 10 / 20 / 40 active minutes, relative cost low.

## Explorer report (2026-08-11, measured read-only lookup)

- Host snapshot: `uptime` => `05:12:17 up 3 days, 19:58, 1 user, load average: 47.22, 45.89, 46.22`.
- Lookup timing: first bounded ingress scan `START_MS=1786414337960`, `END_MS=1786414338232`, `ELAPSED_MS=272`; focused trace command `ELAPSED_MS=6678`. Later process/container confirmation completed within the same investigation window.
- Ingress source of truth: `/etc/nginx/sites-available/all.bezrabotnyi.com.conf:2` declares `server_name all.bezrabotnyi.com`; `:7-15` proxies `/` to `http://203.0.113.10:8098`; `:18` listens on `127.0.0.1:8444 ssl`; `:31-34` handles port 80 redirect/404. `/etc/nginx/sites-enabled/all.bezrabotnyi.com.conf` resolves to that file. `nginx.service` is active (master PID 2160020), active since 2026-08-08 03:06:39 MSK.
- Network/process evidence: `ip route get 203.0.113.10` routes via `enp28s0f2np2` from `203.0.113.10`; neighbor is `REACHABLE`; TCP `203.0.113.10:8098` is `OPEN`. Thus the actual upstream is a remote LAN host, not a local nginx/container listener.
- Local same-port exclusion: `127.0.0.1:8098` is listening, but `fuser -v 8098/tcp` identifies PID `434725`; `ps` shows `/usr/local/bin/smartdns`, cwd `/usr/local/bin/smartdns`. This is not the configured upstream for `all.bezrabotnyi.com`.
- Nginx access evidence: `/var/log/nginx/all.bezrabotnyi.com.access.log` contains recent `GET /` => `302`, `GET /login` => `200 1043`, confirming traffic reaches the configured proxy path.
- Source project directory: **NOT PROVEN**. No local systemd unit, Docker container, local process, or local checkout was linked to `203.0.113.10:8098`; local `docker ps` has no 8098 mapping. The source owner must be inspected on host `203.0.113.10` (or via an explicitly authorized fleet/SSH read-only path).
- Checked: nginx config/enabled link/status, `ss`, route/neighbour, local port owner, recent nginx logs, systemd unit list, Docker container list, bounded local source/config references. Excluded: GrepMesh/MCP Inspector, external mutation, DNS/TLS changes, restart/deploy, SSH or privilege escalation.
- Confidence: high for ingress and remote upstream; high that local PID 434725 is unrelated; low/none for source path because the upstream host boundary was not crossed.

### TL;DR for Lead

`all.bezrabotnyi.com` is served by active nginx via `/etc/nginx/sites-available/all.bezrabotnyi.com.conf:7-8` to `http://203.0.113.10:8098`. The remote endpoint is reachable and recent logs show 302/200 responses. Local `127.0.0.1:8098` is unrelated `smartdns` PID 434725. Absolute source project directory is **NOT PROVEN**; next highest-value probe is read-only inspection on `203.0.113.10` for listener 8098 and its unit/container/process cwd.
