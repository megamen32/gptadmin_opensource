# Task persistence and shared-host Hub runtimes

Tasks are individual records in `GPTADMIN_CONFIG_DIR/tasks_state.sqlite`, not a
JSON snapshot rewritten on every transition. Transactions use WAL and
`synchronous=FULL`; acknowledgement follows commit. Configured history retention
and existing output compaction are unchanged. There is no debounce writer.

## Durable state versus execution ownership

The version-2 task schema stores record revisions, creator IDs, request
reservations and cancellation delivery state. A revision check rejects stale
writers instead of silently allowing an obsolete in-memory task to overwrite a
newer result. Identical records remain no-op writes. Readers reconcile selected
records in batches; they do not reload the complete history on every request.

A creator holds a uniquely named OS file lock under `task-owners/`. Starting a
standby does not fail the live creator's queued or approval-waiting tasks. The
lock is not a heartbeat timer: a paused live process does not lose ownership
just because a wall-clock deadline passed. Normal shutdown releases ownership
through `Server.Close`; process death releases the OS lock. Close the HTTP server
and finish in-flight requests before explicitly closing its task runtime.

A Hub reads current records before task inspection, dispatch, input transitions,
cancellation and result ingestion. Known active records are reconciled during
maintenance too. Maintenance cannot expire another live creator's tasks; it
preserves the requested execution timeout when restoring a running task.

**This is shared-host coordination, not distributed consensus.** Both Hubs must
use the same local configuration directory. SQLite WAL is not a cross-machine
shared-network-filesystem scheduler. Executable arguments still belong to the
creator's in-memory queue. If that creator exits before dispatch, the abandoned
queued/input-required task is recorded as failed, not silently executed again.
Already-running tasks can still report results through another Hub. Group
status is derived from its children rather than from the survival of the group
creator alone.

## Commit boundary

`task_mutation.go` snapshots selected tasks, group children, associated controls,
queue entries, approvals and completed in-memory idempotency responses. It
preserves existing object identity for waiters. Failed commits restore these
values before returning an error; dispatch is published only after commit.
Mutations replace maps/slices instead of editing reference contents in place.

Creation, dispatch, result ingestion, cancellation, approval consumption and
maintenance use checked persistence. A disk or transaction error is not reported
as a successful operation. A cancelled task is terminal for synchronous waiters.
Cancellation state and its delivery record share a transaction; a lost control
poll becomes eligible for redelivery after 30 seconds through either Hub. The original task's final result
acknowledges the cancellation delivery.

The broad `Server.mu` still protects in-memory state and is held during many
commits. The new ownership, request, mutation, control and reconciliation modules
separate responsibilities; they do not claim to have removed every coarse lock
or converted every existing subsystem to a storage interface.

## Idempotency and uncertain outcomes

A supplied idempotency key is durably reserved before execution. Creating its
task and linking the reservation are one database transaction. The same request
through a second Hub returns the original task rather than creating a duplicate.
A differing fingerprint returns a conflict. Completed request responses are
retained for the existing TTL; linked active tasks are not abandoned merely
because that TTL passes.

If a creator exits with no recorded task/outcome, the reservation remains
explicitly unresolved instead of being automatically executed again. This avoids
duplicating an external side effect but is not an exactly-once guarantee for
arbitrary external systems. Such an outcome requires inspection before a new
operation is submitted. A multi-target operation may already have committed
children when a later child or group commit fails; its response must retain the
available child results rather than pretending the whole external operation was
rolled back.

## Migration, dependency and rollout

The first transaction imports `tasks_state.json` and marks migration complete
atomically. The original file is retained, not rewritten. A version-1 SQLite
database gains its revision column transactionally and is marked version 2;
existing results are preserved. Invalid migration input leaves the transaction
uncommitted and can be repaired and retried.

The driver is pinned to `modernc.org/sqlite v1.58.0`; the tested embedded engine
is SQLite 3.53.4. Building the Hub now requires Go 1.25 or newer. This includes
the upstream WAL-reset fix relevant to concurrent writers/checkpointers. See
https://www.sqlite.org/wal.html#walresetbug and
https://pkg.go.dev/modernc.org/sqlite@v1.58.0 .

The opener does not open/close an existing database through `os.File` before
handing it to SQLite: an unrelated close on that inode can release POSIX record
locks belonging to another connection in the same process. Initial file creation
is serialized and uses exclusive creation; thereafter SQLite owns its handles.

Back up configuration and upgrade **all Hub writers sharing it together**.
Do not mix old JSON-only, version-1 SQLite and version-2 writers. ShellMCP does
not need to be upgraded for this storage change. A frozen pre-migration JSON
file is not a current rollback snapshot after new jobs run. Back up a live
SQLite database through SQLite's backup API, or stop writers and include all
journals; do not copy only an active main database file while WAL is present.

This follow-up was validated with isolated fixtures. Production Hub services
were not restarted by the architecture follow-up.

## Proof and reproduction

```sh
cd go-hub
go test ./...
go test -race ./internal/hub -run 'TestArchitecture|TestTaskStore|TestTaskState|TestStandby|TestSameIdempotency|TestCancellationOn|TestConcurrentHubState' -count=1
go test ./internal/hub -run '^TestArchitectureTwoHubProcessesHTTP$' -count=1 -v
```

The HTTP canary starts separate Hub processes using a fresh shared directory,
loopback listeners and synthetic credentials. It proves live-owner protection,
same-key replay, dispatch through the creator, result ingestion through standby,
visibility from primary and recovery after killing the creator process. No real
shell command or production credential is used.

Failure-injection tests use SQLite triggers to fail writes **after successful
reads**, checking rollback of task state, group children, cancellation controls
and consumed approvals. Other tests cover WAL recovery, migration, stale writes,
configured retention, long-running request TTL, lost cancellation delivery,
maintenance retry and preserved timeouts. The existing browser acceptance is
`python tests/e2e/unified_admin_ui.py` from the repository root.

### Retry after a known storage failure

A known task-creation failure before the task association commits releases only
that unassociated request reservation, so the same idempotency key can retry
after the storage problem is fixed. A failure reading an already-associated
task leaves its association pending, so retry returns the original task instead
of caching the temporary error or creating a duplicate. This does not weaken
the separate unknown-outcome rule after a process crash. These cases are covered
by `task_request_retry_test.go` with an actual failing SQLite insert.
