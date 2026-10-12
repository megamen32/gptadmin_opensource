# GPTAdmin node-100 budget and acceptance

## HTTP migration continuation, 2026-10-12

The historical candidate/refusal sections below are retained. The selected
managed-peer release is installed: Hub uses the accepted 3/10GiB RAM, swap0,
CPU2 and Tasks512 envelope. A 12.10 readback of the unchanged Hub measured
RSS1,085,124KiB, swap0 and10threads. The unchanged128/256MiB/CPU1/Tasks128/swap0
Node88 completed the managedHTTP and real native Codex/ZCode acceptance. These
are narrow samples and do not establish the full migration's peak or savings.

The next source candidate separates lightweight HTTP namespace cardinality
from execution:128isolated namespaces match the existing Hub128session bound;
at most8scoped HTTP requests are admitted before any upstream dispatch. A busy
admission neither creates a partition nor dispatches a payload. ManagedSTDIO
remains disabled and retains its independent8namespace ceiling. No kernel
RAM/CPU/tasks/swap or host/UID/native limits change. The constructor-allocation
unit records actual bytes for128namespaces in hosted CI; it is not transport
RSS evidence. Before activation, retain that result and measure actual Hub
RSS/peak/swap/tasks/events during the real HTTP calls and cleanup.

AgentMemory's HTTP adapter runs inside the existing Go owner and reaches the
already-running authenticated REST backend. It owns no process, native session
header or local memory fallback. Each credential/profile/ref/definition and
consumer partition remains separate. Requests/responses are bounded to1MiB,
HTTP attempt deadline15seconds, no redirects/retries; synthesized export uses
two sequential bounded reads. UserIO's dedicated HTTP bindings retain separate
client/upstream credentials. Personal refs are admitted only for their exact
verified profile on the managed initialize/DELETE path; the generic legacy
facade and parent ShellMCP route cannot bypass that lifecycle.

Ordinary focused units: expected130/max180seconds including setup, existing
public hosted finite CPU1/512MiB high/1GiB max/swap0/Tasks128/IO10 and14GiB
ephemeral disk envelope; the entire selected Go payload has150seconds. Unit
fixtures assert logic only. Passing units still require exact accepted artifact,
fresh runtime admission and native consumer proof before any client rollout.

Server44 currently has only the legacy ShellMCP process, whose live limits were
unbounded at the12.10 readback. Do not describe that process as an enrolled
finite Node, or infer managedHTTP support from discovery. Its measured service
budget and supported owner upgrade are a separate rollout step; retain SSH,
GPU/AVD and other owners' processes.

GPTAdmin owns its Hub/service and consumer checks. Canonical infrastructure,
shared resource policy and admission authority remain in
`/home/admin/ServersAdministartion/templates/server100-resource-guard/README.md`.
This candidate changes only the GPTAdmin system service. It does not raise
UID/native limits, move processes or alter another project's quota.

## Verified live baseline, 2026-10-08

Read-only measurements on `admin-node-100` at
18:00:06–08 UTC identified **system** `gptadmin-hub.service`, PID 3152,
`/system.slice/gptadmin-hub.service`, started 2026-10-01. Querying
`systemctl --user` would inspect an absent unit and cannot establish live limits.

Three samples measured memory.current 2,036,076,544 → 2,034,733,056 bytes
(about 1.90 GiB), swap 178,610,176 → 179,077,120 bytes, 120 tasks, and
CPU increments equivalent to about 0.93 and 0.73 cores over one-second windows.
The lifetime memory.peak remains **8,837,713,920 bytes (8.23 GiB)**. That is
historical service/child/cache use, not the peak of these three samples.
Its workload is not classified; a 4 GiB cap would not cover the observed peak.

At that observation MemoryHigh, MemoryMax, MemorySwapMax and CPU quota were
unlimited; TasksMax was 154049 and I/O weight 100 with no bandwidth cap.
The proposed project drop-in was absent from effective DropInPaths. These
observations prove the then-live defect, not the state after Root deployment.
No secret-bearing Environment or credentials were printed.

MemAvailable in these three samples was 26.40–26.61 GB. A later read-only
admission snapshot measured 30.92 GB available, UID 54.65 GB below its
110 GiB soft boundary, and native scope 3.84 GB. These are changing samples,
not capacity grants. A subsequent read-only gate measured only 28.31 GB
available, below the proposed Hub admission requirement; no runtime application
was performed by this review. The 30.92 GB snapshot had host full memory PSI avg10 0.04%
and I/O PSI 0.72%; a strict zero-pressure source job must remain held.

Protected state metadata sampled on 2026-10-07 was about 1.55 GiB under
`/etc/gptadmin`; Shell spool about 3.3 MiB; logs empty. These older figures
must be refreshed through metadata only before deployment. Do not read/export
protected content to estimate disk use.

## Source candidate: Root-selected finite Hub envelope

[`gptadmin-hub-server100-budget.conf`](../deploy/systemd/gptadmin-hub-server100-budget.conf)
is a node-100 candidate, not evidence of an installed or accepted budget.

| Resource | Candidate | Basis and acceptance limit |
| --- | --- | --- |
| RAM | High 3 GiB, max 10 GiB | High is about 1.58× current steady use; hard is about 1.21× the observed lifetime peak. Never substitute a 2 GiB sample for the 8.23 GiB historical peak |
| Swap | 0 new swap allowance | Existing ~171 MiB swapped pages are real; reducing allowance does not prove they are already paged in. Measure effective limit, current swap and pressure |
| CPU / Go | 2 cores, GOMAXPROCS=2, GOMEMLIMIT=2GiB | Above sampled 0.93-core use; bounds runtime fan-out. GOMEMLIMIT guides Go GC and is not the process hard limit |
| Tasks | 512 | Above measured 120 tasks; measure actual consumer/child peaks before broader admission |
| Disk state | 4 GiB operating allowance | Older baseline 1.55 GiB. This is a metadata/retention admission allowance, not an enforced aggregate filesystem quota |
| Temp/spool | 500 MiB aggregate Shell allowance | Existing storagebudget mechanism must actually be invoked; preserve active/outbox paths and verify over-budget behavior |
| Per-file writes | 2 GiB | LimitFSIZE does not replace aggregate state/retention limits |
| I/O | Weight 50, read 40 MiB/s, write 20 MiB/s | Device `/dev/nvme0n1` is node-100-specific. Reconfirm deployment device and measure snapshot/callback latency |
| Parallelism | One small final consumer operation; at most 8 opt-in managed partitions | Legacy facades stay unscoped. No broad MCP rollout or pool increase follows from one canary |
| GPU | None | No GPU workload |

Before applying the drop-in, Root must refresh host/UID/Hub pressure, shared
reservation and service metadata. Preserve **20 GiB host reserve plus the
unused proposed Hub hard capacity**: for the current ~2.036 GB service,
10 GiB − current is about 8.70 GB; host MemAvailable must therefore exceed
about 30.17 GB before accounting for other concurrent growth. The later
30.92 GB sample leaves only a narrow margin; the earlier 26.4 GB sample
would fail. Concurrent service demand and an existing reservation are additional
constraints. Neither UID 110/120 GiB nor physical free RAM alone is a grant.
Do not alter global caps or co-tenants to pass this admission.

## Source checks and artifact provenance

Build/test the complete integrated published commit in the existing GitHub CI.
Linux jobs use isolated hosted Ubuntu VMs: private-repository allowance
2 CPU / 8 GiB RAM / 14 GiB SSD; product checks have 30-minute deadlines,
packaging 90 minutes, Darwin/Windows 30 minutes. Fixed Go/Rust/Node parallelism
belongs to the workflow. Use its exact successful candidate/release artifact
and verify its commit/digest before deployment; do not rebuild it locally with
an unregistered heavy job. A hosted run does not change fleet limits.

The optional local source-check wrapper
[`check-mcp-relay-limited.py`](../scripts/check-mcp-relay-limited.py)
now delegates execution to the existing canonical foreground helper. Its own
unsafe sibling scope/late verification and duplicate lifecycle logic were
removed. The read-only `--check-gate` uses actual canonical snapshot/validation
and a short atomic lock probe; it is not a workload grant. A held result starts
no Go, no child scope and no new session.

The default canonical policy still pins UID 36/44 GiB and original scope
6/8 GiB, while the current native control plane has 90%-RAM/120 GiB and UID
110/120 GiB. Thus the 2026-10-08 check returned **75: outer/original boundary
mismatch**. Canonical owner must independently reconcile/register the exact
GPTAdmin case and review its changed source/admission receipt before local
execution could be admitted. That source-policy work is outside GPTAdmin;
do not borrow AutoSeller/Exmanager profiles or edit canonical guard pins.
Cloud validation is the authorized route in this cycle; no local Go execution
is required or claimed.

## Admission and final native consumer canary

1. Obtain complete passing cloud checks/artifact for the published main SHA.
   Preserve all auth/configuration and source history. Inventory the actual
   system Hub and ShellMCP identities; an HTTP health result is insufficient.
2. Refresh the reserve arithmetic above, UID/host memory and I/O pressure,
   Hub current/peak/swap/tasks, storage metadata, effective unit limits and
   existing reservation. If unsafe or unknown, hold application and record
   the exact failing condition; never raise another owner's cap.
3. Save only task-owned executable/drop-in/client-entry backups and protected
   auth references with owner-only permissions. Root installs only this
   project drop-in and the exact cloud artifact, performs all planned restarts,
   then checks effective **system** service controls. Do not log Environment.
4. After the last change, run one harmless actual tool from the real installed
   native Codex consumer, through its preserved enabled name. For GrepMesh,
   `read_text` of the first eight lines of the canonical GPTAdmin `AGENTS.md`
   is an exact read-only consumer operation; use the installed tool schema and
   `node-100` identity. Capture returned text/IDs/session and verify against
   the known file. A raw Python HTTP replay is API diagnosis, not native proof.
5. For an opt-in `/connection/.../mcp` claim, select an already enabled real
   HTTP/SSE child and preserve a protected backup of exactly one native entry.
   Reuse its real auth/profile. Verify native initialize → actual harmless
   tool → session continuity → confirmed DELETE/close → reinitialize with the
   expected owner/credential fence. Managed stdio remains disabled. Legacy
   native GrepMesh success does not prove this new endpoint.
6. Through real installed ZCode, perform the same harmless actual operation
   on an existing common enabled HTTP/SSE server, using its current schema
   (for AskSecret, a nonexistent-name read-only status check only). Do not add
   servers or expose a secret merely to manufacture a common canary. Record
   native-client identity, exact returned result and bound credential/profile.
7. Root observes Hub/ShellMCP peak/tasks, pressure/events, callback latency and
   consumer-session cleanup while these sequential tiny operations run. No
   heavy payload, broad suite, new unregistered scope or extra model repair
   job is needed. If a separate tiny Python protocol probe is useful, it must
   run under the explicitly bounded own project service and remains API proof.
   Never modify runtime/config or restart during the final canary until its
   result is captured and evaluated.

Failure starts another fix/review/check cycle and a new final canary. Rollback
restores only task-owned artifact/drop-in/native entry and the same auth
binding, retaining intervening work. Record failed acceptance rather than
claiming a migration. Broad deployment waits for per-consumer evidence,
measured project storage/child peaks and confirmed bounded teardown.

Runtime binary identity verified 2026-10-08: `go version -m /opt/gptadmin/bin/gptadmin-node` identifies Go1.26.4, module `github.com/megamen32/gptadmin/go-hub`, command `cmd/gptadmin-node`. The name Node does not mean Node.js; the Go heap/parallelism settings apply to this executable.
