# GPTAdmin node-100 budget and acceptance

## HTTP migration continuation, 2026-10-12

The historical candidate/refusal sections below are retained. The selected
managed-peer release is installed. At12October acceptance Hub used3/10GiB RAM, swap0,
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

## 15 October 2026: measured MCP latency repair

The running parent plus existing children reached 3.27GB current / 3.51GB
observed peak against MemoryHigh=3GiB. Its cgroup memory full avg60 was
15.5%, with 12.6 million high events, zero swap and zero OOM. Exact native
MCP initialize and calls timed out at 30s while the allocation-free health
endpoint still returned200. Preserve accepted tasks and existing children.

Raise only MemoryHigh to4GiB, above this measured aggregate peak with margin.
MemoryMax10GiB, swap0, CPU2, tasks512 and I/O caps stay enforced. Fresh host
MemAvailable exceeds35GiB: even the full remaining hard allowance preserves
the20GiB host reserve. Apply the supported live systemd property with the
matching persistent drop-in, without restarting parent or children. Recheck
memory events/pressure and actual MCP initialize plus tools read; this is a
latency hypothesis until that consumer succeeds. Evidence and RED probes:
agent-herder/.tmp/zcode-communication-resume-20261014/hub-memory-before.json.

Acceptance: same PID3158, native caller initialize41–67ms, tools/list131ms,
actual Codex GPTAdmin ui/schema/execute, one new empty ZCode session and its
native Herder→Codex marker acknowledgement passed. Same configured connection
initialize→agent_info→DELETE204 also passed. Cgroup full avg10 fell from16% to0.
This restores this MCP consumer; it does not prove bounded heap retention:
metrics show223570shell records, mostly completed, retained in runtime maps.


Final15October follow-up: a later valid aggregate workload reached4,337,532,928B
at the4GiB soft threshold. Exact memory.events.local.high grew13,080,822→13,092,583
in10s, while cgroup full avg10 rose1.84→6.22%. This confirms the4GiB margin was
insufficient for the later active children, rather than an HTTP health failure.
Current tasks434/512, swap0, OOM0; anon4,280,684,544B dominates file cache6,258,688B.

The user already explicitly requested a RAM-limit increase. Raise only the soft
threshold4→6GiB under that same authorization; retain hard10GiB/GOMEMLIMIT2GiB,
CPU2/tasks512/swap0 and I/O/file caps. Fresh host MemAvailable35,179MiB leaves over
28GiB even if the remaining hard allowance is used, preserving20GiB for neighbours.
No parent/child restart, task deletion or other project-cap increase is required.
Recheck exact high events and native Codex tools-read after application. The
historical3→4 repair above remains valid for its earlier measured workload.
Evidence: agent-herder/.tmp/zcode-desktop-visibility-20261015/
gptadmin-final-memory-samples.json. Core heap/result-retention optimization is
not proven or silently substituted for retained task results.

Post6GiB acceptance: samePID3158, actual native Codex GPTAdmin fleet_node_info
succeeded in1445ms. Exact10s sample retained high-events13142630 and unchanged
some/full pressure totals; fullavg10 fell0.01→0.00, current4.29→4.43GB. No swap,
OOM, parent/child restart or task-result deletion. This proves the measured
consumer recovery, not a repair of retained-history heap usage. New direct
human mandate now separately covers source-level RAM root-cause repair.

## 15 October: expired task retention in RAM

Direct human request extends recovery to fixing the source cause. Live parent
RSS3.42GiB and229259 shell tasks contrasted with87574 durable shell rows, of
which87004 were within the configured24h completed-task retention. Persisted
results occupied180MB; decoded maps, copied arguments/results and Go allocation
overhead remain much larger than the JSON bytes. Expired rows were pruned from
SQLite on commit but never removed from runtime shellJobs/relayJobs maps. The
GC cannot reclaim still-referenced jobs, regardless of GOMEMLIMIT. Service
memory additionally includes native child MCP/Chrome processes.

The existing10s maintenance loop now evicts only committed terminal tasks older
than completed_job_retention_hours from RAM, including during idle periods.
No TTL change or new durable deletion; active/UNKNOWN and uncommitted results
are retained. Real SQLite regressionRED in both idle/active cases→GREEN; final
33 focused task/token/metrics checks passed3.888s, peak709443584B, swap/OOM0.
The configured48h case preserves a36h receipt via actual mcpTaskGet/storage.

Source checks use gptadmin-focused100, fresh adaptive1,600,126,976B high=max,
swap0, CPU1/GOMAXPROCS2/-p1, tasks128, temp512MiB/20000files, IO10 and20GiB host
reserve. Expected90s/emergency900s including cold compilation; measured initial
compile peak1278107648B, no OOM. An incorrectly constructed test selector was
cancelled and cleaned rather than accepted as coverage; the final exact focused
selector passed. No broad-suite/nightly result claimed.

Deployment boundary: native drainGET showed644unfinished executions and2active
callbacks. Existing accepted/uncertain jobs must not be cancelled/replayed or
dropped to force a restart. The earlier token-presentation change is also in
current main and retains the same live installation boundary. A compiled
candidate is not an active primary. One-off online RAM relief uses only the
existing terminal-result ACK for already expired IDs absent from durable DB;
it cannot cancel active work or delete retained database results.

## Explicit restart and lazy audit, 15 October

The human subsequently instructed restarting GPTAdmin to activate the update
and requested lazy journal access. This authorizes the planned primary restart
after tests/build, superseding the earlier activation boundary; retain durable
receipts and no replay. The obsolete one-off ACK recovery was stopped in its
owned scope after~55k expired acknowledgements, no durable task writes.

A second source defect was found: constructor loadAuditState decoded the whole
2GiB audit journal before taking the last500 events, retaining discarded map
references in its backing array. A scan error skipped even the final trim.
Audit history serves the admin UI and is not needed for MCP startup. The update
removes startup history reads entirely. Audit GET reads at most4MiB from the file
tail and decodes only the configured recent window; temporary history is never
stored in Server. It merges unpersisted current events without duplicating
successful disk appends. Runtime append clears discarded references and keeps
capacity within2×the configured event bound, including setting reductions.
Existing access-operation durability/auth and journal contents stay intact.

The exact2GiB file also blocks new appends under LimitFSIZE. During the authorized
stop, preserve it by atomic rename to audit.jsonl.pre-ram-fix-20261015 and create
a new0600root-owned current journal. Preserve the archive inode/size, no truncate,
history deletion or higher file cap. No new automatic retention/deletion policy.

15Oct installed acceptance: clean source348a9a9/numeric1022 for Hub+executor,
primary1497697. Native hostname1031ms, paired file_editor623ms, auditGET35ms.
950/950oldIDs retained:900undispatched failed closed after the explicit restart,
15completed/35running-or-unknown. Full2GiB archive retained; new journal writes.
Warm service samples1.48–1.49GiB vs3.998GiB before stop, no high/max/OOM/swap.
This meets the earlier repair slice, not the new human target of~300MiB.
The latest extension requires lazy completed-task history and a bounded cache;
preserve receipts, active owner pointers and existing service safety budgets.

## Five-minute RAM cache and ~300MiB target
The latest human instruction sets completed-task RAM residency to five minutes. Durable completed_job_retention_hours stays unchanged. Startup selects active jobs and the last300seconds of completions, rather than decoding all~89k task results (~182MB JSON). Exact-ID job/task requests hydrate cold history; existing10s maintenance and requested-task refresh evict expired committed terminal cache entries. Active work, live group children and uncommitted results stay resident. Admin history uses temporary SQLite metadata projection and displayed payload reads. Legacy GET /tasks/{server} is paged: limit default200/max500, offset default0, count is total and truncated indicates another page. Exact-ID output remains unchanged. Metrics job counts now describe the resident execution cache, with explicit300second cache metadata.

The Go soft memory target is reduced from2GiB to256MiB. This is separate from the outer service safety ceiling and from measured source-build budgets. Fresh host MemAvailable~33GiB preserves20GiB reserve; source checks retain the reviewed1.6GB/CPU1/no-swap budget. Measure the updated real process and whole service after authenticated native calls and history reads before accepting the target or lowering the outer emergency ceiling.

Installed five-minute cache acceptance: sourcea5aba55/primary1658673, native hostname and Herder tools-read completed. Actual old receipt200/33ms, full history89244rows/200displayed with no cache refill. Process/service peak290500608/280117248B (277/267MiB), no swap/high/max/OOM. The200-row admin page took11.45s because its temporary metadata inventory still scanned all history; owned follow-up moves counts and pagination to SQLite expression indexes and batches displayed payload reads. A real SQLite missing-index regression is RED. Fresh available host~33GiB and measured277MiB process peak justify reviewed outer MemoryHigh512MiB/MemoryMax1GiB, swap0; CPU2/tasks512/I/O40Mread20Mwrite unchanged. Go soft256MiB and source-test budget unchanged.

Indexed paging final80focused checks GREEN10.025s, peak656859136B/no swap/OOM. Derived SQLite inventory/status/node-page indexes initialize atomically via the existing cold schema writer; version2/history/rollback stay compatible. Page reads explicitly select the matching index, return only requested metadata and batch displayed payloads, never all-history Go maps. Final runtime activation and same native acceptance follow.

Latest human correction: "10гигов это ок сервис важный". Retain the existing service MemoryHigh6GiB/MemoryMax10GiB and swap0; the proposed1GiB cap was never applied. Lower actual usage through lazy history/five-minute cache and Go soft256MiB, without restricting the important service to1GiB. Source/tests for indexed paging are unchanged.

Actual indexed200-row history latency remained RED (>3s): WITHOUT ROWID + JSON-expression GROUP BY still seeks each task payload, despite the expression index. Read-only EXPLAIN/timing isolated the count scan; page selection itself8–9ms. The follow-up keeps only derived per-kind/status aggregate counts in a tiny SQLite table, maintained by INSERT/UPDATE/DELETE triggers inside the same task transaction, including older/rollback writers. It initializes once atomically, has no result payloads and no Go lifetime cache. Tests cover committed updates/deletes and rollback, in addition to five-minute/history/native contracts. This removes the remaining full-history count read from every admin request. Service6GiB high/10GiB max remain per latest human instruction.

Final durable counter regression and integrated81focused cases GREEN, peak655212544B, swap/OOM0. Counter migration is derived/atomic and rollback-compatible; count/page requests no longer read all task results. Latest service10GiB emergency ceiling remains unchanged.

Final aggregates include owner/server, so the legacy node-filtered total also avoids the slow JSON-expression count.81focused cases GREEN6.588s, peak673710080B/no swap/OOM. Only derived counters may be atomically rebuilt during format upgrade; all task records, IDs/results and version2 rollback compatibility stay intact.

## Final installed acceptance
Source052a13a installed as numeric1022 Hub+executor, primary1822345, binary SHA256 58df9de2304d581661a78404a6ce82d6f19524315bb30c4d33cf8345fe05dc75. Native hostname jobb8bd8055af593dba6e5ed8bb22e84cf7 completed; native Herder tools-read job86a71036ceeed69808cd0bbb428f760a completed. Real old receipt200/58ms, full89399-task history200-row page281ms (previous11.45s), older offset10000 page610ms, legacy cold server page137ms, audit81ms. Derived per-status/server counts exactly match a real read-only durable snapshot. Runtime cache remains small rather than refilling history. Warm20s measured GoRSS peak89.48MiB/final70.17MiB and service peak80.64MiB/final61.33MiB, no swap/high/max/OOM and no automatic restarts. These are observed canary measurements, not a promise that every future MCP child workload fits300MiB. Human-selected high6GiB/emergency10GiB retained, Go soft256MiB.81focused checks GREEN6.588s. No task replay/manual cancellation; durable receipts and2GiB audit archive preserved. Original unrelated fleet milestone remains open.
Evidence: .tmp/ram-retention-20261015/history-final-acceptance.json and installed-primary-history-final.json; final guard e7bb41d... completed with cleanup.

## Human-requested60second terminal cache
Latest direct request: shorten completed task RAM TTL from300 to60seconds and update GPTAdmin, retaining10GiB emergency ceiling. Existing real SQLite history regression now uses90second completions at cold startup and61second reloaded receipts. RED proves the old300second startup cache retains them. One shared taskCacheSeconds=60 controls cold startup selection, maintenance expiry and public metrics. Active/group/uncommitted protections, durable history retention, aggregate counters and limits are unchanged. Existing10second maintenance cadence handles expiry; exact-ID reads still hydrate durable history.

60second TTL: real SQLite RED (old startup retained64ninety-second jobs) to GREEN with cold90s exclusion,61s cache eviction and durable result reload.81focused integrated cases pass; source guard peak645775360B, swap/OOM0. Primary update authorized; outer6GiB high/10GiB max and all other budgets unchanged.

60second update accepted: clean source0d98db9 installed as1022 Hub+executor, PID1891568, binary SHA25623476a69f6978870a82b2b7a4fd859eec8a6a565733efa5203bcd8634ebb5b7b. Actual runtime metric completed_task_cache_seconds=60. Native hostname jobb8275439b6a0957fe58426cb172eedd0 completed with correct host. Old durable receipt12ms,89517task history200page192ms, legacy page70ms; derived counters equal an independent durable snapshot. Warm20s process peak137.84MiB/service131.89MiB, swap/high/max/OOM0, no automatic restarts. Source81focused GREEN9.592s after real SQLite RED. Effective high6GiB/max10GiB/swap0 verified unchanged; Go soft256MiB and CPU/task/I/O budgets unchanged. No task replay/manual cancellation or history deletion. Evidence: .tmp/ram-retention-20261015/cache60-acceptance.json, installed-primary-cache60.json and completed guardcc68367e... . This supersedes the prior300second RAM TTL only.
