# MCP connection migration — proposed canary contract

Status: **PROPOSED, canary only**. This document does not establish deployment,
release readiness, or completed migration. No client configuration rollout is
authorized by source tests alone. Root owns integrated delivery and configuration.

The existing consumer inventory is an explicitly dated 2026-10-07 private
operator snapshot at `.agents/tasks/gptadmin-mcp-consumer-inventory-20261007.md`.
It is ignored local evidence, not a repository document or a cloud-CI dependency.
Infrastructure ownership and host access remain in the canonical
`/home/admin/ServersAdministartion` checkout. Preserve existing Herder
POST-only stdio bridging until the real native consumer proves a replacement.

## Connection contract

The new `/connection/{configured-child-slug}/mcp` endpoint exposes tools only.
Authenticate every request through the existing verified credential/profile
path. Initialize obtains an opaque downstream session ID; requests and DELETE
are bound to that credential, profile revision, and child. Preserve downstream
request IDs and tool names; upstream RPC IDs belong to their separate client.

Mutable upstream connections require separate downstream sessions. An audited
`connection_mode=stateless` definition can share only within its authenticated
owner/target group. Sharing is an explicit exception requiring a real behavioral
proof, not an inference from transport or a healthy status response.

All legacy child facades remain **unscoped**, including bearer/JWT and ordinary
remote facades on upgraded connectors. They retain their existing pool/lifecycle
semantics and do not silently promise per-downstream mutable continuity. Source
review found that assigning legacy calls authenticated partitions without a
complete close/expiry lifecycle exhausted the eight slots after credential
refreshes. The integration decision removes that accidental migration rather
than expanding the pool or changing saved legacy client entries.

Only explicit opt-in `/connection/.../mcp` HTTP/SSE sessions enter the managed
scoped registry. Its eight-partition ceiling is a managed-session admission
limit, not a limit on all enabled legacy consumers. Preserve every existing
legacy stdio/HTTP bridge and disabled entry. Neither source tests nor deployment
opts native clients into managed sessions.

The endpoint does not advertise resources, prompts, subscriptions, sampling,
roots, or upstream cancellation forwarding. Do not claim these are migrated.
Resource/prompt support in the shell implementation is below this consumer
contract and is not acceptance evidence. Request and teardown deadlines must
remain bounded. Expired/closed/mode-invalid sessions require initialize again;
teardown must not acknowledge disposal without an observed confirmed close.
The final downstream session retains its capacity reservation while teardown
is running; initialization cannot reuse that closing group. Failed teardown
retains the reservation for retry rather than restoring it above the registry
ceiling after another caller took its slot.
Concurrent admitted requests, mode changes, reinitialize, and failed-close
retry are mandatory lifecycle review cases before live acceptance.

The connector publishes a per-process/per-definition instance fence. Requests
carry its expected value and a one-minute admission deadline; callback execution
checks the fence before acquiring the pool. A restart or definition replacement
requires initialize again. Scoped stdio uses only an explicit environment plus
runtime allowlist, never all Hub credentials, and requires a verified owner-local
Unix connector. **Scoped stdio is currently disabled before process start on all
platforms.** Source review found a PID/PGID reuse race between leader reaping and
publication of completion. The existing mutex is not an ownership-safe lifecycle.
Managed stdio requires an implementation that prevents this race, with real
descendant/exit/cleanup acceptance. Unscoped legacy stdio retains its existing path.
Leader exit alone does not prove group disposal. A remaining or unverifiable
group must retain its partition; a late group kill after leader reaping risks
PID/group reuse and is refused. After a connector-process restart, lost remote
session state also remains an explicit cleanup-proof gap. No missing in-memory
record is evidence that an upstream session was closed.
Same-process old-instance close is allowed only for an exact retained partition
or a separately confirmed successful disposition; an admission tombstone alone
cannot prove cleanup. Stateless stdio replacement also requires confirmed prior
group disposition. Scoped `WaitDelay=2s` bounds inherited internal I/O waiting
after leader exit; this does not prove disposal of children that escaped the group.
These source guards do not remove the stdio capability gate. Legacy facades remain unscoped and retain their existing stdio path. The managed
stdio capability gate therefore blocks only explicit opt-in managed stdio,
not existing legacy stdio consumers; HTTP/SSE opt-in is the selected new path.

## Source validation and canonical admission

The authoritative complete-source validation for this cycle runs in bounded
GitHub-hosted CI, including package/server compilation and lifecycle tests.
Deploy the exact successful artifact of the integrated published commit, not a
local rebuild. Linux product jobs have 30-minute deadlines, packaging 90 minutes,
Mac/Windows 30 minutes, with fixed Go/Rust/Node parallelism in the owning
workflow. See [runtime budget](GPTADMIN_RUNTIME_BUDGET.md) for project limits
and artifact/consumer acceptance.

The optional local wrapper
[`check-mcp-relay-limited.py`](../scripts/check-mcp-relay-limited.py) supports:

```sh
python3 -B scripts/check-mcp-relay-limited.py --check-gate
python3 -B scripts/check-mcp-relay-limited.py --plan
```

These read actual canonical policy/snapshot and probe the existing heavy lock;
no Go process, child scope, daemon or new native session starts. The command
returns 75 when the canonical policy or current pressure refuses admission.
On 2026-10-08 it returned `outer/original boundary mismatch`: default canonical
UID 36/44 GiB and original scope 6/8 GiB do not match live UID 110/120 GiB and
native emergency control-plane 90%-RAM/120 GiB. This is current policy evidence,
not a replay of the older insufficient-UID-reserve observation.

The wrapper no longer creates its own sibling scope, launches a payload before
checking child limits, or duplicates reservation/cleanup. Its execution modes
(`client`, `hub`, `server`) delegate only to the canonical foreground helper's
OFD lock, waiting shim, actual child verification and generation-bound cleanup.
A passed observational check is not a grant: the helper independently rechecks
all boundaries before payload release. No local execution occurs in this cycle.

| Resource | Proposed narrow source check | Admission |
| --- | --- | --- |
| RAM | High 384 MiB / hard 512 MiB | Canonical original soft/hard accounting, no control-plane cap adoption |
| Swap | 0 | No new swap capacity |
| CPU/tasks | 1 core, Go concurrency 1, 128 tasks | Original plus child charged together |
| Storage | Total project temp/cache ≤256 MiB /20000 objects | Existing retained storage deducted from each lease allowance; no foreign cleanup |
| I/O/time | Weight 10, scope120s, Go90s | Canonical guard owns monitoring and cleanup |
| Reserves | UID2GiB, original128MiB, host20GiB, strict PSI0 | Refresh actual metrics; finite envelope does not issue admission |

Go flags are `-p1`, `-parallel1`, `-mod=readonly`, `GOMAXPROCS=1`,
`GOMEMLIMIT=384MiB`, `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`.
Cache/temp/log stay inside the canonical helper's protected monitored TMPDIR
lease; external caches/daemonization are not allowed. Canonical resource-owner
must review/reconcile an exact GPTAdmin case before the changed policy could
admit a local run. This project must not edit the canonical pins or borrow
AutoSeller/Exmanager profiles. Cloud validation avoids this unadmitted local
payload; it does not bypass any product CI gate.

## Individual consumer matrix — historical inventory, acceptance still separate

| Host | Codex explicit global definitions | ZCode explicit global definitions | Acceptance state |
| --- | --- | --- | --- |
|100 |4enabled; plugins add enabled definitions |15enabled,5disabled | Source inventory only; native canary and effective project/plugin enumeration pending |
|44 |5enabled | Config and executable absent at investigated locations | Native Codex canary pending; resolve actual ZCode installation before migration claim |
|88 | No global MCP entries |7enabled; no executable on PATH | Plugin/project effective set and runnable ZCode route unresolved |
|Mac |9enabled,3disabled |9enabled,1disabled | Read-only inventory; native locality and actual canaries pending |

Mac identity was verified through reverse SSH2222 using the known host key,
LocalHostName`MacBook-Pro-User`, arm64 and modelMacBookPro18,2. Canonical
policy correction bae00cb accepts that identity despite DHCP DNS name
`MBP-User.lan`; no hostname rename is pending. No phone/S21 operation belongs
to this migration.

Mac `node_repl` uses a native executable inside ChatGPT.app and native
pipe/service environment references. Central server100 execution is not a
substitute. Local browser, desktop, and Telegram session adapters likewise
need execution-owner and credential-binding proof. Do not move them merely
because their MCP transport can be wrapped.

Other trusted project overrides and enabled plugin MCP definitions are not
fully enumerated. Consequently there is no all-enabled delivery claim for any
host. Disabled entries remain disabled; their credentials, names, and unrelated
configuration must survive unchanged.

## Canary, rollout, and rollback

Before applying one proposed entry, save its exact protected configuration and
native auth reference in an owner-only backup with mode0600. Never put raw
credentials in Git, logs, examples, or this document. Preserve surrounding
configuration and every disabled entry. Rollback restores that same entry and
its auth binding rather than replacing the whole client file.

Root must finish reviewed implementation, integrated cloud checks, same-history
main commit/push, and deployment of that exact successful cloud artifact before
a final native canary. Install and verify only the project-specific finite Hub
budget after fresh reserve admission; see the exact sequence in
[GPTADMIN_RUNTIME_BUDGET.md](GPTADMIN_RUNTIME_BUDGET.md).
Through real installed Codex and ZCode, call a harmless actual upstream tool
under its existing name; verify the output and bound session/credential owner.
For100, select a server already enabled in both consumers, such as AskSecret,
using a read-only nonexistent-name status check. Do not introduce GrepMesh into
ZCode merely to manufacture a common canary: its effective enabled registration
has not been demonstrated there. GrepMesh Codex transport compatibility is a
separate real read_text canary through the preserved POST stdio bridge.
Initialization, schema listing, HTTP200, mocks, shell handler tests, and process
presence are not final proof. A failed canary starts another repair cycle;
avoid runtime/config changes during the final canary until its result is saved.

Then update the matrix individually for each enabled consumer and connection
kind. Broader opt-in rollout remains blocked until the measured runtime Hub/ShellMCP
budget, eight-managed-partition capacity impact, complete effective inventories,
stateful isolation/continuity, confirmed bounded teardown, Mac locality, and
real native paths are accepted. Do not raise the pool or host limits to make
the canary pass. Clean, synchronized main and real live evidence remain root's
completion gates.
