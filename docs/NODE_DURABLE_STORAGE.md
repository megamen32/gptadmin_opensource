# Node durable shell storage

Shell output and undelivered receipts are mandatory data, not a disposable
cache. Automatic cleanup retains the entire configured spill and outbox trees.
The same policy applies to the Shell embedded in Node and the standalone Shell.

## Delivery and cleanup

A queued shell command runs once. On a failed result POST, Shell writes a JSON
receipt under `SHELL_OUTBOX_DIR`; the queue loop retries its saved payload with
backoff. It removes that file only after a successful result response. HTTP404
is not acknowledgement and no longer deletes it. Malformed JSON is retained for
operator diagnosis rather than silently discarded.

Receipt files can refer to stdout/stderr spill paths. Without an index proving
that no pending or delivered receipt references a spill, neither its age nor its
size makes it disposable. Consequently:

- Shared server budget sweeps protect spill/outbox subtrees, including nested
  outboxes and files created by concurrent executions.
- The shell runner's post-execution sweep protects the whole spill tree, not
  just its current command's output.
- Queue-poll age cleanup no longer deletes spills. The existing retention setting
  does not authorize deletion of mandatory output.
- Audit/backup/checkpoint budget sweeps can reclaim other disposable owned files.
  Explicit owner file-management operations are not automatic retention.

Small, non-spilled capture files are removed only after their full output is
contained in the receipt. Retry metadata uses a0600 temporary file, file fsync,
and atomic rename; failure before replacement leaves the previous receipt
intact. Parent-directory fsync is not implemented: this is **not a power-loss
or all-platform filesystem durability guarantee**. Initial receipt creation can
still fail on a full disk; Shell logs that failure. This slice does not introduce
reserved receipt capacity or stop new executions when storage is exhausted.

## Budget and overflow

The automatic shared allowance remains `min(5% filesystem capacity, 500 MiB)`;
an explicit Hub `shell_storage_max_mb` overrides it. Mandatory files count
against this allowance, but are never evicted to satisfy it. Thus it is a
reclamation target, not a hard disk-usage ceiling. Retaining all spills can grow
without bound until reference-aware reclamation or explicit operator cleanup
is available.

The last successful server sweep exposes these Shell `/metrics` fields:

- `storage_effective_limit_bytes`: resolved allowance;
- `storage_protected_bytes`: retained mandatory/protected bytes;
- `storage_over_budget_bytes`: remaining bytes above the allowance.

Overflow also produces a `storage mandatory data retained` log with byte counts.
These fields describe the last sweep, not an admission guarantee or filesystem
free-space measurement. Existing `outbox_depth`, retry and delivery counters
remain available. Standalone queue-only Shell has no HTTP listener; its log is
available without enabling a new listener. This change adds no public Node
metrics route.

## Real acceptance canary

`tests/e2e/node/outbox_budget_run.py` takes a real unified Node executable:

```sh
python3 tests/e2e/node/outbox_budget_run.py \
  --node-binary .tmp/outbox-budget/node
```

It uses isolated configuration, random ports/test credentials and a128MiB ext4
image in project `.tmp`, mounted in its own mount and network namespaces. It
requires `sudo`, `unshare`, `mkfs.ext4`, `mount`, `ip`, and `iptables`; production
services and host network rules are never used. The command execution user is explicitly root inside this test
namespace; namespaces isolate mounts/network, not process privileges.

Two actual commands start in one Node process, then a private-namespace
iptables rule rejects traffic to its loopback listener port. The first command
emits an8MiB spill and cannot deliver its receipt over the real rejected TCP
connection. The Node process stays alive throughout. A second command and8MiB disposable backup trigger real cleanup
while that receipt is pending. The test ages the spill, verifies payload/hash
preservation, exhausts the private filesystem to force an actual retry-write
ENOSPC failure, and verifies the old payload again. It removes its own filler,
removes the private network rule, verifies both completed receipts with three reads
each, one side effect per command, empty outbox and unchanged spill hash.
Owned processes stop and the private mount is removed; the image and reports
remain in `.tmp` as evidence.

An alternative component mode takes `--shell-binary` and `--hub-binary` instead
of `--node-binary`. It runs a separate real Hub/Shell pair, stops only its Hub,
then restarts that Hub with the same state. Both modes preserve their logs,
payload and receipt evidence, artifact hashes and cleanup result.

Verified unified Node run: jobs `22506a668617e7713aec74961716439e` and
`1f4fa5792b151c4d51d0c6b68c891ae7`; same PID3150920 before/after network fault;
8,388,638-byte spill versus a5,448,704-byte allowance; disposable pressure
removed, ENOSPC retry preserved the original payload, effects `[1,1]`, outbox0,
three identical reads per receipt. `.tmp/outbox-budget/real-node.log` records the
run. These are isolated-runtime proofs, not a production rollout, power-loss
survival or distributed job replication. Root owns combined auth/storage
acceptance and deployment.

Combined release8736611 repeated the actual TCP/ENOSPC canary successfully
(`.tmp/resource-root/outbox-final.log`) and is deployed on VUSA and the primary.
The controlled updates preserved19 and1872 prior receipts respectively;17
primary receipts expired under the existing24-hour retention policy, with no
unexplained changes. Public peer jobs `8ba675e6432fa54a81254dcc0d579147`
and `7671e9746dfb0bcc368efd0fe60c8b13` each produced one physical effect and
three identical receipt reads. The primary public self-probe also passed.
After maintenance the tunnel watchdog and canonical DNS watcher were restored;
the latter retained its configuration binding, watching phase and zero failures,
with authoritative canonical DNS still95.165.165.65. This verifies the deployed
storage change; it is not a whole-machine outage or power-loss test.
