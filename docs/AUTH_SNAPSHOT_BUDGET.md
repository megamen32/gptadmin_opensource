# Reader snapshot filesystem admission

Reader snapshot apply now checks available filesystem space **before** setting
`Pending` or writing any snapshot files. Writer initialization/export and normal
authority mutations are outside this change. The check performs no eviction and
does not touch jobs, outbox, command history or the active generation.

`GPTADMIN_AUTH_SNAPSHOT_RESERVE_BYTES` is a positive integer, default1048576
(1 MiB). It is an operation headroom allowance **in addition to** the calculated
snapshot writes, not a claim that the entire operating system or arbitrary
command output is covered. Explicit invalid/nonpositive values reject reader
apply. Existing `GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES` and per-store limits remain.

## Calculation

The reader has two mandatory store files (managed tokens and OAuth clients),
plus profiles when the manifest declares them present. For each store and the
pending/final manifests, calculate the actual `json.MarshalIndent` bytes plus
the trailing newline used by the existing atomic writer. Round each file to the
filesystem allocation block. This is four or five atomic file writes, not the
compact HTTP snapshot length.

The estimate does not credit old inactive-slot or manifest bytes/inodes freed
by replacement. Atomic temporary files become their destination by rename;
there is no second data copy to charge. Add one allocation block per missing
slot directory or lock file as a conservative metadata allowance. Require one
inode per planned atomic file, missing lock and missing directory (at most nine
for an absent three-store slot). The active manifest, both existing slots and
any stale profile lock already consume filesystem capacity and are never
subtracted as reclaimable space.

Inspect the manifest directory and, if present, the inactive-slot directory;
check both conservatively when they are on different mounts. Available bytes
must cover calculated writes plus reserve. Where the filesystem exposes inode
counts, free inodes must cover the operation too. Statistics failure, invalid
allocation units, insufficient bytes/inodes or invalid reserve reject the apply
with HTTP507 and an `auth snapshot storage unavailable` detail. No generation
is acknowledged, so the existing sync runner must not advance freshness.

This preflight does not reserve blocks. Another process can consume space after
the check; later filesystem errors retain the existing interrupted-apply
fail-closed behavior. Nor does the preflight claim to detect every predictable
path/permission error: existing lock-file/path validation remains in the write
path. It does not repair an already pending generation or make a physically
full disk capable of running new commands.

## Platforms

Linux and Darwin use native filesystem available-block/free-inode statistics.
Windows uses `GetDiskFreeSpaceEx` for bytes available to the caller and
`GetDiskFreeSpaceW` for allocation units; it has no Unix-style free-inode gate.
Other platforms build with an explicit unsupported-statistics error for reader
apply rather than silently bypassing admission. Writer/export is unchanged.
Cross-compilation is build evidence only; the real filesystem canary below was
executed on Linux.

## Real filesystem acceptance

```sh
mkdir -p .tmp/auth-budget
(cd go-hub && GOTMPDIR="$PWD/../.tmp/auth-budget" go build -o ../.tmp/auth-budget/gptadmin-node ./cmd/gptadmin-node)
sudo -n python3 tests/e2e/node/auth_budget_run.py .tmp/auth-budget/gptadmin-node
```

The harness creates a128 MiB ext4 image under the project's `.tmp`, reexecutes
in a private mount namespace, attaches only that image, and starts two actual
Node processes with fixture credentials and separate directories. It explicitly
sets the fixture root executor user because the mount test runs under sudo;
production environment/configuration is not inherited. It issues two ordinary
managed credentials via the real API, seeds B, executes a command and captures
its receipt. Actual allocated filler extents reduce available disk space below
the default reserve; this is neither a sparse-file trick nor mocked Statfs.

A real revocation snapshot from A is rejected with507 before Pending. All auth
JSON hashes remain unchanged. B restarts under pressure and still accepts the
previous credential and returns the exact prior receipt. After removing only
the filler, a newer generation applies, the revoked credential gets401, the
surviving ordinary credential executes, and another restart preserves revocation
and the original receipt. At most two slots remain. Both Nodes are joined and
the private filesystem unmounted; the image/report/logs remain under `.tmp`.

Recorded RED: old e754316 accepted the snapshot with520192 available bytes.
Recorded GREEN: candidate rejected with507 at the same availability, preserved
reader auth/receipt across restart, then applied generation4 after freeing
space. Evidence: `.tmp/auth-budget/real-{red,green}.log`.
The focused estimator/refusal tests are separate logic/filesystem checks;
they do not substitute for this real two-process ext4 canary.

Final committed artifact8736611 passed the same real ext4 canary
(`.tmp/resource-root/auth-final.log`) and was deployed sequentially to VUSA and
the primary on2026-09-09. Both retained their identity and unexpired receipts;
ordinary public primary execution and bidirectional physical-origin TLS peer
execution passed. VUSA periodic synchronization advanced to generation297 with
no error after deployment. These observations do not extend the reader-only
preflight to writer admission or guarantee writes against concurrent exhaustion.
