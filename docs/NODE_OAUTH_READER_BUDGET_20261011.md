# Enrolled OAuth reader 88: bounded delivery candidate

Owner: GPTAdmin/Codex55. Scope: two existing gptadmin-node@ instances on
server01; not legacy ShellMCP, Herder, SSH or native Codex.

Observed before delivery at 20:05Z, three one-second samples: instance RSS/cgroup
working sets 34.8/35.9 MB; lifetime memory peaks 58.9/61.9 MB; 40/39 tasks;
CPU about 0.3–0.4 percent of one core per instance; swap/current OOM events zero.
Host MemAvailable 93.75 GB, disk free 142.4 GB. Host memory PSI was high;
that alone is not admission. Check actual service latency/pressure, trend and
fresh host reserves before apply. Current instances have unlimited limits:
this is an owning deployment defect, not an accepted runtime budget.

Initial per-instance candidate: MemoryHigh128MiB, MemoryMax512MiB, swap0,
CPU1, Tasks128, IOWeight10, root-device read32MiB/s/write8MiB/s. RAM soft is
over twice measured lifetime peak; hard protects room for new auth metadata
and one small child call. Tasks is over three times observed native threads.
No heavy upstream operation is included in the initial acceptance workload.
Both instances reserve the full unused 1GiB RAM/CPU2/tasks256 aggregate.
Use finite private /tmp64MiB. Candidate/backup files <=128MiB total on disk;
existing authority/task stores are preserved and are not truncated to meet it.

Apply only accepted cloud artifact, SHA-verified, as a versioned tree with
the prior current symlink and exact per-instance drop-ins saved. First fresh
host reserve20GiB plus full unused candidate hard, disk>=2GiB, current service
health, no new OOM and no active task across both instances. Preserve separate
identities/credentials/configs and node generation; never merge authority.
Only existing two GPTAdmin node units may stop/start under this window. On
failed apply restore their old symlink/drop-in bytes; do not stop other units.
Node restart deadline30s, whole owned apply<=120s, bounded readiness/readback.

Consumer: same safe existing OAuth command/idempotency key on shell88,
returned owner read/no-hint/repeated read equality plus durable terminal,
identity of physical88 and actual running source/artifact. Measure candidate
steady/peak/tasks/swap/CPU/I/O after the real call. If it exceeds this candidate,
keep evidence and repair the owning working set; never silently raise caps.
This candidate is not an unrestricted fleet compute/browser budget and does
not claim whole shared MCP migration or Herder cabinet acceptance.
