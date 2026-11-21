# GPTAdmin when one node is unavailable

## Client contract

The client keeps its public HTTPS MCP URL and credentials. After the loss of one
server it can open a session and run a new command on a reachable executor,
including Android. Election and reconnection can briefly delay admission.
Each task has a storage owner. When that owner is offline, reading its output
reports that the result is unavailable. The client can run an unrelated
command immediately. If the owner returns with its data, the same handle can
read the result again, subject to the original access policy and retention.
For a known offline owner the API returns `owner_unavailable`. A legacy ID
without a known owner returns `history_unavailable` and accepts the owner
hint from the original receipt. Both responses tell the caller it can send a
new command to an available executor.

## What belongs on each machine

| Data | Placement | Behavior on loss of the owner |
| --- | --- | --- |
| Enrollment, access policy, service routes and authority term | Three server voters on independent machines. Any two can elect a leader and commit. | The majority continues to authorize work. The isolated former leader loses admission rights. |
| Request reservation | When the client supplies an idempotency key, voters keep its scoped digest, a digest of the arguments, the owner's identity and the original task handle when known. | Retrying with the same credential and key refers to the original operation during the retention window. An unresolved delivery keeps its reservation. |
| Arguments, pending input, task history, stdout, stderr, files and content indexes | The admitting Hub owns its local task data. An executor receives its own work. Large results are fetched from the owner as needed. | A failed read identifies an unavailable owner when known. A disconnected executor's command cannot be dispatched to a different machine merely because the original machine is down. |
| Undelivered execution receipt | Only the executor that performed the operation keeps the receipt in its durable outbox until the owner acknowledges it. | It can deliver the result when its owner returns. Independent new commands can finish in the meantime. |

A leader is elected at runtime. Server 44, server 88 and server 100 have the
same voting rights. Android, Macs and other executors can use the service
without storing any other machine's history. Each ingress needs a direct route
to the remaining majority. The public name also needs a working route when a
machine hosting an ingress is the one that failed.

The recovery HAProxy checks a healthy voter every two seconds. After one
missed check it keeps the route and probes again after one second. A second
failure withdraws the route. The Hub still verifies majority authority when
admitting each request. The proxy never resends an ambiguous command.

On OpenWrt the public TLS entry point runs as `haproxy-became`. Its supervisor
waits five seconds after an unexpected exit and keeps retrying for as long
as the service is enabled. The usual procd retry limit can leave the public
port closed even while all three voters are healthy. The init script belongs
at `/etc/init.d/haproxy-became`. [OpenWrt service definition](../deploy/server01/haproxy-became)

For planned maintenance, a service manager can drain its own voter through its
loopback listener using that node's control credential. Draining a follower
this way keeps the leader available for new commands on the other executors.

An unkeyed submission creates no cluster deduplication entry. On a connection
loss after submission, use the original task handle to check the outcome. A
client that needs to retry a submission must choose its key before sending it.
The existing key budget is 1,024 reservations per owning Hub. Completed
reservations expire after 15 minutes. An unresolved claim keeps its slot until
the outcome is known. The separate budgets let a surviving Hub accept new
commands when another owner's slots are full.

After a completed reservation expires, a fresh submission with the same key
can execute again. To inspect an older operation, use its original task ID.

## Submission, execution and readback

1. Authorize the command. Before dispatching a retryable request, commit the
   reservation. A different command using the same scoped key is a conflict.
2. Persist the input on its owner and publish work only after admission. A
   transport failure after sending the command can leave its outcome unknown.
   Preserve that operation's identity through election, reconnect and restart.
3. Deliver the result to its owner. Queries and retries must apply the current
   caller's rights at the owner. The cluster can return a short receipt even
   when the body is unavailable. An uncertain delivery never triggers another
   execution simply because a client retries.
4. Use the owner in the task handle or its existing owner hint to locate the
   result. A legacy ID whose owner cannot be established yields an unavailable
   history response. The number of nodes queried must stay bounded. Apply the
   same ownership rule to exported content handles and search indexes.

## Switching an existing installation

Keep each machine's task database, content store, local encryption key and
executor outbox on that machine. Bootstrap coordination from the active
authorization and membership state. Fence pending dispatches at the previous
writer and retain deduplication evidence for unresolved requests. An opaque
legacy operation can remain unavailable while its owner is down.

A fresh cluster defaults to `owner_local`. A new voter catches up from
control records and a bounded control snapshot.
Adding a voter, rejoining, promoting a leader or enabling failover must work
with an existing large local history, an empty local history or a temporarily
offline former owner. An existing cluster that already uses replicated task
storage must retain access to its previously acknowledged data when upgraded.
Changing that cluster's storage policy is an explicit operator operation.

The availability requirement does not require a backup of command output. A
separate request to read selected results during an owner's outage could justify
an opt-in replica with an explicit destination, quota and retention policy.

## Acceptance on installed processes and in CI

- Stop each voter in turn, including the current leader. A fresh client at the
  original URL reads a result held by a surviving owner and runs a new command
  on another reachable executor. Verify the effect on that executor.
- Stop the owner of a previous result and read the same handle. Check the
  unavailable response, submit a new operation while the owner is still down,
  and repeat an earlier keyed operation. Count the actual external effects.
  After rejoin, fetch the old result from its owner.
- Exercise the physical S21 over the normal public route while the writer is
  down. Verify its installed client and run a new command on the handset.
- Partition the old leader from the other voters while its listener remains
  reachable. The minority refuses admission. Heal the partition and reconcile
  the original key. Lose the majority only in isolation.
- Seed large, distinguishable historical output only on its owner. Compare the
  bytes sent and stored when a voter joins and rejoins, then repeat after adding
  a large result. Control traffic and snapshots should scale with bounded
  metadata. Inspect peer state and the mobile executor for leaked payloads.
- Fill a polling executor's dispatch journal with unresolved claims. Submit
  through public MCP and read the same job ID. Expect a terminal
  `executor_dispatch_capacity` response with `executed: false`. Retry with the
  same idempotency key and verify the original handle and absence of side effects.
