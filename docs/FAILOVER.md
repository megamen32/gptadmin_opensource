# Failover and degraded recovery

GPTAdmin is designed so one dead machine does not have to mean a dead control plane. A fallback node can keep a standby hub reachable through the same public tunnel path, so the service keeps living in a degraded mode long enough to inspect logs, reach surviving servers, and repair the primary.

This is not magic multi-master replication. Some fresh in-memory state can be stale or temporarily unavailable while the primary is down. The important part is that GPTAdmin stores the operational trail on disk: queued/background jobs, large stdout/stderr spill files, shellmcp spool files, outbox responses, registry snapshots, failover runtime state, and service logs. That means the system may lose a little immediacy, but recovery work is not blind and most job evidence is not gone forever.

## Roles

| Role | What it does |
|------|--------------|
| **Primary hub** | Normal active GPTAdmin hub, admin UI, MCP relay, Action/OpenAPI endpoints, queues and routing. |
| **Fallback node** | Runs a standby hub or local proxy, watches the public health URL, and can promote itself when the primary stops answering. |
| **Tunnel** | Public ingress. In an incident it is repointed to the fallback, so clients still have an HTTPS entry point. |
| **Reclaim path** | When the primary comes back, it sends a signed reclaim/demote request to the active fallback so the fallback becomes a client/standby again. |

## What happens when the primary dies

1. The fallback watchdog checks the primary public health endpoint on an interval.
2. After the configured failure threshold, the fallback confirms the public URL is still unhealthy.
3. The fallback promotes itself by starting its local hub/proxy and tunnel client.
4. Existing AI clients and admins continue to reach GPTAdmin through the public URL, but the system is in degraded mode.
5. Surviving shell/MCP servers reconnect or continue polling. Dead servers are shown as offline/stale and can be repaired from the fallback control plane.
6. When the primary is healthy again, the primary sends a signed reclaim message. The active fallback demotes and returns to standby/client mode.

## What still works in degraded mode

- Admin UI and health endpoints on the fallback.
- Shell/MCP operations against servers that are still reachable from the fallback or already connected through long polling.
- Reading queued/background job state that was persisted to disk on the node handling those jobs.
- Large command output via spool files instead of losing it in chat context.
- Outbox-based delivery for queued responses when the transport reconnects.
- Incident triage: logs, service status, tunnel status, and recovery commands.

## What can be stale or missing temporarily

- Very recent in-memory counters, active request lists, or last-seen data that had not yet been persisted or synced.
- Running jobs on the machine that died at the exact moment of failure. Their last persisted spool/log state is still useful, but the process itself may need to be re-run.
- Local-only resources that existed only on the dead server.
- MCP tools hosted on the dead server until that server is back or another node hosts the same tool.

GPTAdmin treats this as graceful degradation: keep a smaller control plane alive first, then use it to restore the larger one.

## Operator checklist

On the active node:

```bash
gptadmin urls
systemctl status gptadmin-hub gptadmin-tunnel-frpc --no-pager
journalctl -u gptadmin-hub -n 120 --no-pager
```

On the fallback node, check the watchdog/proxy runtime:

```bash
ps aux | grep -E 'gptadmin|failover|frpc' | grep -v grep
curl -fsS http://127.0.0.1:9001/healthz
curl -fsS http://127.0.0.1:9101/healthz
```

Then inspect durable state before deleting or restarting aggressively:

```bash
find /var/lib/gptadmin -maxdepth 4 -type f | sort | tail -100
find /data/gptadmin-failover -maxdepth 4 -type f | sort | tail -100
```

Look for spool, outbox, runtime and journal files. They are the recovery trail.

## Current HAOS-style layout

A compact fallback can run a loop like this:

```text
/data/gptadmin-failover/loop.sh
/data/gptadmin-failover/gptadmin-failover-proxy.py --listen 127.0.0.1:9101 --upstream http://127.0.0.1:9001
/data/gptadmin-failover/runtime.json
/data/gptadmin-failover/failover_state.json
/data/gptadmin-failover/logs/watchdog.log
```

The exact paths are deployment-specific, but the logic is the same: watchdog checks primary, promotes a fallback only after threshold/confirmation, keeps disk state, and accepts a signed reclaim when primary returns.

## Design rule

Prefer "alive and degraded" over "perfect and down". During an outage, GPTAdmin should keep enough of itself reachable to answer: what is alive, what died, what jobs were running, where the logs are, and how to restore the primary.
