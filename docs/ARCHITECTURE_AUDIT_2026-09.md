# GPTAdmin architecture audit — 2026-09-05

Scope: task persistence and the split administrator experience. The task-store
change is commit `e158cca`; the unified console is the accompanying UI change.
Production Hub services were not restarted during this work.

## Fixed and verified

| Problem | Implementation | Regression evidence |
| --- | --- | --- |
| Every task transition serialized and rewrote all historical results | Individual SQLite records, transactions, WAL/FULL, explicit changed task IDs | 3080-task fixture; selected-row/no-op writes; full Go tests and targeted race tests |
| Restart or another Hub writer could interact with persistence | Transactional JSON migration, lifecycle conflict check, preserved idempotency and recovery behavior | Committed WAL recovered after abrupt process exit; two Hub instances; rollback/migration tests |
| Failed result persistence was acknowledged to the transport | Return 503 instead of acknowledgement; failed dispatch is put back in the queue | Result-commit and dispatch-commit failure tests |
| Main UI and operations lived at different entrypoints | One React application at `/admin/`; operations are a native component; `/admin/legacy/` redirects | Browser navigation through 13 sections, one navigation and no iframe |
| MCP manager called helpers outside their JavaScript scope | Hoist shared helpers into the operations component lifecycle | Nonempty MCP list rendered in the component regression test |
| Operational refresh timers and requests had no component lifecycle | Mount/unmount controller, abort requests and clear interval on exit | StrictMode remount and cleanup tests |
| Background refresh overwrote unsaved failover fields | Dirty form guard, reset after successful save | Draft URL survives refresh |
| UI only recognized old JWTs for managed-token operations | Support current `durable` tokens as well as `managed_jwt` | Existing client action tests; real Hub issues an opaque token in browser acceptance |
| Issued token disappeared just by switching sections | Keep it in application state and display it on the connection screen | Clients → connections → clients retains the value |
| Empty client list told the user to create a client but hid the issuance form | Render issuance controls in the empty state too | Red/green empty-client test and first-token issuance against a real isolated Hub |

The controlled persistence fixture recorded 1,507,328 bytes for 20 updates with
3080 historical task results (~16.45 MiB). This is not a post-deployment NVMe or
production-process measurement. Do not present it as one.

## Functional preservation and migration boundary

The unified application includes server and task inspection, tool calls,
resources, MCP management, failover, audit, activity and raw diagnostics alongside
instructions, profiles, clients, webhooks, virtual MCP and authentication.
The Hub version and update control remain in the overview. Old bookmarks still
work. The old static payload now contains only a compatibility redirect.

Operational markup/JavaScript was moved under `admin-ui/src/operations`, not
rewritten wholesale. It is a transitional imperative component with scoped
styles and lifecycle management; it still has inline action handlers and broad
overview refreshes. There is one user-facing application, not yet a completely
React-native implementation of every operational widget.

No security preset or existing enforcement default was changed. Owner-facing
product controls and redaction in diagnostic/public responses are different
concerns: hiding a feature is not an acceptable substitute for an explicit
opt-in policy. Future UI migrations must preserve a feature inventory before
removing the previous implementation.

### Token and administrator follow-up

Shared-access changes now preserve independent token/OAuth-client writes and
reload authoritative state on validation. Explicit stored `admin`/`owner` roles
allow native MCP administration without equating all execution tokens to owners.
New token values survive reload/restart and have an admin-only retrieval action;
old hash-only values still need explicit rotation. See ACCESS_OPERATIONS.md.
The remaining coarse-lock/UI/queued-execution work is not implied complete.

## Task-runtime follow-up — 2026-09-05

The first audit findings about local primary/standby task ownership and durable
acknowledgement now have an implemented, tested follow-up. Creator OS locks keep
standby startup and maintenance from failing a live creator's tasks. Record
revisions reject stale updates. Durable request reservations and task linkage
prevent duplicate task creation across the local Hubs. Mutation rollback covers
failed creation, cancellation, result and approval transitions; cancellation
delivery survives a lost poll response. Maintenance follows the same commit
boundary and preserves restored execution timeouts. The obsolete task JSON
merge implementation was removed; registry merging remains in use.

Eight lifecycle functions were moved out of `server.go` into `task_lifecycle.go`.
The responsibilities are now split into `task_owner.go`, `task_mutation.go`,
`task_reconcile.go`, `task_maintenance.go`, `task_idempotency.go`,
`task_controls.go` and the storage module. See [TASK_PERSISTENCE.md](TASK_PERSISTENCE.md)
for the transaction, recovery, migration and dependency contracts.

These are source/test results, including separate-process HTTP acceptance, not
a production rollout. Security presets, access-profile semantics and owner UI
credential policy were not changed by this follow-up.

## Remaining architecture work, in priority order

1. **Cross-machine execution ownership and recovery.** Local shared-disk Hubs
   now coordinate their task records, but executable queued arguments still
   live with the creator. An exited creator's undispatched task fails visibly;
   this is not a replicated scheduler with transferable execution ownership.
   Exactly-once external side effects require executor-specific contracts.
2. **Coarse locking and other state stores.** Many task commits still hold the
   broad Server mutex. Other JSON state subsystems retain their own write and
   merge patterns. Narrow locks and migrate storage only with measurements and
   subsystem-specific regression evidence, not blanket debounce.
3. **Owner UX and remaining UI legacy.** A single admin app exists, but the
   operational component is still partly imperative JavaScript. Split feature
   modules without deleting controls. The durable owner credential inventory
   described above remains separate work; access policy is not a substitute for
   restoring useful owner controls.
4. **Outdated contracts and dormant assets.** The unserved `public/admin_dashboard.html` and its active snapshot/test
   dependencies are retired by the access-operations follow-up. Continue
   auditing remaining compatibility endpoints by actual consumers.

## Reproducing verification

From the repository root:

```sh
(cd go-hub && go test ./...)
(cd go-hub && go test -race ./internal/hub -run 'TestTaskStore|TestTaskSave|TestTaskResultDoes|TestTaskDispatch|TestConcurrentHubState' -count=1)
(cd admin-ui && npm test && npm run lint && npm run build)
python -m pytest tests/test_admin_dashboard_js.py tests/test_admin_ui.py tests/test_admin_ui_build_contract.py tests/test_admin_ui_release_contract.py -q
python tests/e2e/unified_admin_ui.py
```

The browser script requires the Python Playwright package and its Chromium
browser. It builds a loopback-only Go fixture, uses fresh temporary configuration,
never loads the production environment, creates its token only in that fixture,
and terminates its child server in `finally`. Screenshots and result artifacts
are under `.tmp/unified-admin-ui`. It is an explicit acceptance command, not a
claim that browser E2E has already been added to CI.

For rollout, follow [TASK_PERSISTENCE.md](TASK_PERSISTENCE.md): old JSON-only and
new SQLite writers must not run concurrently against the same configuration.
Upgrade both Hub writers together after a backup. The frozen migration JSON is
not a current rollback snapshot after new tasks have been accepted.

## AI-first access and operations follow-up

Owner management is now available through typed `access_profiles`,
`access_clients` and `operations` tools reusing the existing authenticated
HTTP handlers. The profile UI preserves actual Go field names, workspace
references, instruction sets and approval modes. Read-only profiles no longer
inherit write access from full tokens. Access changes are visible through a
restart-surviving operation journal and a single UI screen. The duplicate
1144-line HTML dashboard is removed after consumer inspection. See
[ACCESS_OPERATIONS.md](ACCESS_OPERATIONS.md) for precise authority boundaries
and outstanding credential-value/delegated-admin/queued-execution work.
