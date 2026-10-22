# Explicit node peer routing

## Topology choice: central hub or hubless

One code base runs three topologies; the operator's configuration alone selects
the shape, and `discover` reports the active one in its `mesh` summary:

- `mode: "writer"` — central membership hub (`GPTADMIN_AUTH_MODE=writer`).
  The writer owns membership; members join via a one-time grant, heartbeat,
  replicate the snapshot and build their routes from it.
- `mode: "member"` — a joined node (`GPTADMIN_NODE_IDENTITY`, optionally
  `GPTADMIN_NODE_SEED`). Routes come from the replicated membership view.
- `mode: "standalone"` — hubless. No writer, no identity; every node that
  should be reachable is listed in `GPTADMIN_NODE_PEERS` by hand. All nodes
  know each other statically; there is no central point of failure and no
  dynamic membership.

There is one source of truth for routes: `membership` wins, static
`GPTADMIN_NODE_PEERS` entries are only the bootstrap fallback, and a live
member connection (writer side) is only a transport preference over the same
route's advertised origin. The summary always shows `static_peers` and the
writer's `live_connections`, so an operator can see where each route comes
from. "Everyone knows everyone" (standalone) trades onboarding convenience for
independence from the hub; pick per deployment, not per code path.

## Static peer configuration

`GPTADMIN_NODE_PEERS` maps an execution target to an operator-configured origin:

```json
{
  "shell:nodeB": {
    "url": "https://node-b.example",
    "connect_to": "192.0.2.20"
  },
  "shell:nodeC": "https://node-c.example"
}
```

String origins remain compatible. Optional `connect_to` accepts only a literal
IP and requires HTTPS. It overrides the TCP destination, preserving the URL
hostname for TLS certificate verification and SNI. Connections are pooled per
configured URL/IP pair, so two physical nodes sharing a logical hostname cannot
share a pool. No DNS or hosts-file changes are required.

Submit to a peer through the authenticated relay route, which forwards to the
peer and lets the peer execute locally:

```sh
curl -X POST https://hub.example/mcp-relay/call \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"target":"shell:nodeB","tool":"shell_exec","args":{"cmd":"printf hello"},"background":true,"idempotency_key":"example-1"}'
```

Read the returned job through the **public** `job` facade, using the same
target as the submission (also available as `result._meta.owner_target`):

```json
{"name":"job","arguments":{"id":"<returned-job-id>","owner_target":"shell:nodeB"}}
```

The public MCP facade no longer carries a direct routing form: `execute` is
retired for every caller, so the only public way to address another node is
the `job` reader. Submission to a peer stays on the authenticated relay route
above, which is also the route the retired public spelling used internally.

Job IDs remain unchanged. Existing local `job {id}` reads remain compatible.
The owner hint selects only an explicitly configured route, never a caller URL.
A local record takes priority; a conflicting hint fails. The receiving node
requires its pinned local executor to own the stored job. Unknown jobs and
unconfigured owners fail; there is no peer scan.

The original bearer is forwarded and independently authorized at the owner.
Forwarding permits one hop, rejects redirects, and never retries an uncertain
execution at another executor. Jobs, receipts and idempotency stay in the
owner's existing SQLite store. The ingress keeps no persistent routing index;
clients retain the owner target across reconnects. After ingress loss, the
same credential can read the receipt or reuse the same execution idempotency
key directly at the owner. Receipt reads do not trigger execution.

This slice adds MCP receipt routing; the REST execution endpoint remains
supported, but REST receipt reads still use the owner's endpoint. It does not
provide job replication, automatic promotion, discovery or stable DNS failover.

Real local acceptance:

```sh
python3 tests/e2e/node/peer_run.py .tmp/path/to/gptadmin-node
```

The MCP canary runs two actual Node processes, provisions an ordinary managed
credential using auth snapshot export/apply, forwards through verified HTTPS
with a physical IP pin, reads repeatedly through the ingress, stops that
ingress and verifies direct owner receipts and a single execution side effect.
It also invokes `scripts/gptadmin_node_probe.py` against the ingress. Optional
`--legacy-probe PATH` checks a preserved old probe fails the missing-owner read.
