# Stopped ordinary authority handoff

`go-hub/cmd/authority-handoff` performs an offline role transfer using the Hub's actual file formats and validators. It is separate from quorum bootstrap. Each physical node retains its execution database, task owner state, pending ciphertext, output storage and executor identity.

The Python launcher `scripts/gptadmin_authority_handoff.py` pins the exact executable SHA256 and executes this implementation. It no longer accepts the previous descriptor-only phase commands. Creating a marker file does not stop admission or a service manager.

## Transferred authority

The writer image contains all eleven canonical domains:

| Domain | Continuity |
| --- | --- |
| `auth/bundle` | Managed tokens, OAuth clients, access profiles and their existing identities |
| `instruction_sets_named/current` | Exact named instruction content and versions |
| `instruction_sets_default/current` | Actual effective default instruction content |
| `membership/writer` | Nodes, grants, IDs and enrolled node keys; only reviewed role and routing transitions change |
| `registry/current` | Agents, settings/history, policies, tombstones, relay enrollment and self-enrolled remote credential bindings |
| `virtual_mcp/current` | Enabled virtual capabilities |
| `webhook_routes/current` | Existing route and authorization data |
| `webhooks/current` | Existing webhook receipts |
| `security/settings` | Security settings, re-encoded for the destination's existing local encryption inputs |
| `webauthn/credentials` | Existing registered credentials |
| `secret_ingress/current` | Pending ingress requests and ready values, re-encrypted under the destination's existing secret-store key |

The private bundle contains credentials and decrypted one-time ingress values. Its directory is0700 and its files are0600. Do not log its contents, pass them on a command line or publish them with the public digest receipt.

Persisted `local_executor_tokens` belong to self-enrolled remote agents. They are preserved with their exact agent bindings. The in-process physical executor credential is not transferred. The target's physical executor ID/public key/fingerprint and secret-store key remain unchanged.

The writer plan also preserves the source's public issuer/resource, JWT key ID, legacy origins, callback policy, instruction receipt policy and enrollment/security flags. Configured MCP bearer environment bindings are carried privately only when they match retained authoritative token digests. A source control credential excluded from the auth bundle is not reintroduced through an environment alias. Old target configured bearer bindings are replaced by the source set, preventing writer startup from reconciling an obsolete local value over the transferred client identity. Per-node control/admin inputs remain local.

## Preconditions

The operator must establish a real maintenance window and stop/fence the affected service manager before export, preparation and apply. Each operation additionally obtains the real `auth-continuity/runtime.lock`. A helper launched inside the stopped node's own service cgroup is not a valid controller. Use an independent SSH process, native agent or supervised controller whose cgroup is outside every affected GPTAdmin service.

Drain admitted work through its ordinary execution path. The exporter rejects every nonterminal source shell/relay record and every outstanding control. UNKNOWN operations are resolved through their original receipts and the actual ledger. Never clear them to make a gate pass.

Record effective configuration paths, service environment precedence, installed package hashes, node IDs, membership, live authority generation, shared-signer mode and direct management routes. Stage the exact helper build for each platform. Keep the original service/package settings and a concrete restore path.

This implementation does not promise compatibility with an active quorum store. Keep ordinary mode explicit. Old Raft directories and task-quorum tables are retained. No task database snapshot is restored over later admissions.

## Source export and target preparation

Run the helper with the actual service's effective environment. `--config-dir` must match it exactly. The commands below illustrate argument contracts; substitute paths and identity values from the reviewed manifest.

```sh
authority-handoff export \
  --config-dir /etc/gptadmin \
  --target shell:admin-node-100 \
  --expect-writer SOURCE_WRITER_ID \
  --output /private/handoff/source-image

authority-handoff prepare-writer \
  --config-dir /etc/gptadmin-node/node-44 \
  --target shell:node-44 \
  --expect-local-id TARGET_PHYSICAL_ID \
  --bundle /private/handoff/source-image \
  --bundle-sha256 REVIEWED_BUNDLE_SHA256 \
  --source-peer-url https://s100-node.t.became.bezrabotnyi.com \
  --environment-file /etc/gptadmin-node/node-44/node.env \
  --output /private/handoff/writer-stage
```

The former writer needs a physical owner route that cannot loop back into the new public writer. An independently verified dedicated origin can use its public route. Reusing the public writer origin requires a verified physical `--source-connect-to` IP pin. A pin keeps TLS hostname verification; it is not an IP-and-port override.

Preparation materializes real canonical state in a private empty-task stage. It does not instantiate a runtime over the source or a copy of a production task database. The actual target stays unchanged. The resulting plan binds every before/after digest, physical key/identity guards, environment changes and private reader-transition digest.

The logical writer ID becomes the target's physical local auth ID. An older separate enrollment for that same host is retained offline with its ID and key. The new writer self-entry does not duplicate the old enrollment key. The former writer keeps its enrolled identity; a previously keyless writer self-entry receives a private member identity for the same node ID.

## Retained readers

Changing the public URL alone cannot update a reader's pinned writer identity. Every retained active reader requires a coherent stopped transition of:

- `nodeIdentity.WriterID` and `WriterURL`, preserving `NodeID` and `NodeKey`;
- explicit `GPTADMIN_AUTH_SOURCE_ID`, if the launcher provides it;
- the validated auth generation and named instruction sets;
- `membership/member-view.json`, using the canonical wire revision/SHA and clearing node key hashes;
- stale writer IP pins and static bootstrap routes.

The former writer's `GPTADMIN_NODE_PUBLIC_URL` changes to its reviewed physical owner endpoint. Otherwise its next heartbeat would advertise the public authority URL and undo the routing correction.

```sh
authority-handoff prepare-reader \
  --config-dir READER_CONFIG \
  --target READER_SHELL_TARGET \
  --expect-local-id READER_PHYSICAL_ID \
  --transition /private/handoff/writer-stage/reader-transition.json \
  --transition-sha256 REVIEWED_TRANSITION_SHA256 \
  --environment-file EFFECTIVE_ENV_FILE \
  --output /private/handoff/reader-stage
```

Both dotenv files and native JSON string-map environment files are supported. Unknown values remain intact. On macOS, the canonical `node.env.json` and the generated LaunchAgent are separate artifacts. While the real job and updater are stopped, use the exact installed manager's `write_plist(root, plist)` to regenerate the launcher from the staged canonical environment. Verify its fixed path values and that existing CTL/SHELL credentials are present before invoking it. Preserve the reverse SSH and unrelated bridge jobs. Do not use uninstall for this temporary transition.

An explicitly reviewed `--listen-host` can change the listener IP without changing its port. For the current88 deployment, `0.0.0.0:9088` preserves both existing127.0.0.1 consumers and the HAOS LAN ingress. Binding only the LAN address would break those loopback consumers. Verify anonymous `/mcp` is still401 through both paths; keep existing Xray and NGINX configurations unchanged.

HAOS member startup can derive `AUTH_SOURCE_ID` directly from `node-identity.json`. In that reviewed case use `--identity-derived-entrypoint /run.sh --entrypoint-sha256 EXACT_IMAGE_ENTRYPOINT_SHA256`. Preparation accepts this only when the role is already reader, the canonical seed URL is already correct and there are no remaining writer pins, static peers or quorum configuration to change. The exact entrypoint digest becomes an apply/restore guard. A derived source is not a substitute for an absent configuration adapter.

Shared signer equality is required for writer promotion and for readers explicitly configured to verify tokens offline with the shared signer. Ordinary proof-based readers retain their local signing/control inputs and obtain authorization from the new enrolled writer through the existing node protocol.

An offline reader retains its identity and requires the same supported transition before reconnecting. Do not report it migrated merely because its old membership entry is preserved. Disabled nodes remain disabled; joining nodes are not automatically approved.

## Apply, activation and recovery

```sh
authority-handoff apply \
  --config-dir ACTUAL_CONFIG \
  --prepared /private/handoff/writer-stage \
  --plan-sha256 REVIEWED_PLAN_SHA256 \
  --backup /private/handoff/writer-backup
```

Apply verifies every staged and existing byte digest under the real runtime lock. It first saves private originals and a durable rollback journal, then publishes a pending-auth marker. It writes dependencies and the effective environment before committing the final nonpending auth manifest. A crash between those steps leaves admission closed. A real operator must still keep the service manager fenced across phases.

The immutable legacy receipt importer uses the canonical task database transaction/schema layer. It appends exact terminal payload/revision/caller/idempotency rows and refuses ID conflicts, nonterminal input, pending ciphertext, cancellation controls and unpreserved spilled output paths. It never loads the ledger into runtime workers, prunes it, rewrites old revisions or merges live queues. The current source may have zero legacy exceptions; the actual export receipt is authoritative.

Active `exec-content.sqlite` references are copied using a native SQLite snapshot including committed WAL. IDs, principal isolation, origins, expiry, completeness and search indexes survive. If a target already has logically identical active sources/chunks/search rows, its existing SQLite/WAL is retained unchanged under byte guards; SQLite page layout differences alone do not require replacement. Truly different active reference sets are refused instead of overwritten or silently merged. These references retain their normal expiration; they are not durable business history.

If a response is lost, inspect the same operation instead of resubmitting:

```sh
authority-handoff inspect --config-dir ACTUAL_CONFIG \
  --backup /private/handoff/writer-backup --plan-sha256 REVIEWED_PLAN_SHA256
```

Before any service manager starts a changed runtime, retire snapshot rollback:

```sh
authority-handoff authorize-activation \
  --config-dir ACTUAL_CONFIG \
  --backup /private/handoff/writer-backup --plan-sha256 REVIEWED_PLAN_SHA256
```

Activation and restoration acquire the same actual runtime lock. Activation rechecks every applied after-state byte and physical guard, and refuses a prior restoration. Exclusive journals and their directory entries are durably synced before the next phase. Before that boundary, `restore-before-activation` validates the exact journal and all current before/after/pending hashes, records the recovery direction, writes a pending auth fence, restores originals and commits the old auth manifest last. Appended immutable historical receipts remain present. After activation authorization, restoration from that snapshot is refused. Recover by draining and exporting a fresh reverse authority image, preserving all newer accepted state. Do not run two writers or overwrite newer authority with an old backup.

## Acceptance

Require actual authenticated control operations through the public ingress, execution on the intended physical44/88 owners, retained node membership/heartbeat, managed token continuity, active content readback and original receipt readback. Verify old shell targets do not execute locally on44 by accident. Check effective launcher settings after restart and then a subsequent writer generation on retained readers.

Only after local state and authentication acceptance should the persistent ingress select the new ordinary writer. Verify WAN ingress reaches HAOS independently of100. A bounded failure test may stop only the old100 controller after all nonterminal operations and rollback ownership are understood. Never reboot the100 operating system for this test.

The functional regression suite is:

```sh
go test ./internal/hub -run '^TestOfflineAuthority' -count=1
```

It exercises real domain files, native auth handlers, SQLite content and task receipts, crash ordering, preactivation restoration, protected activation, retained-reader startup/heartbeat and ordinary next-generation synchronization. It does not replace live deployment acceptance.

## Physical proof and retained reverse history

The CLI binds each target to its configured `shellmcp_identity.json` and matching Ed25519 PKCS8 key. The controller passes the actual physical path explicitly. A registry heartbeat can replace transient `local_executor` metadata; that flag is not a durable identity proof. Identity and key digests remain apply guards.

Before forward activation, the stopped destination may capture an exact `capture-legacy-baseline`. This private metadata file records each existing nonlocal immutable terminal ID, revision, payload/row hash, request hash, and the verified physical executor identity. It contains no task bodies. Freeze its SHA in the operation journal before authorizing any runtime.

A fresh reverse export may name that baseline using `--retained-legacy-baseline` and `--retained-legacy-baseline-sha256`. Only unchanged exact matches stay exclusively in their existing node-local ledger. Changed/new records still use the original bounded immutable import checks; a missing baseline record fails closed. No queue, SQLite row, output file, or history is deleted or merged by this operation. The public receipt identifies the count, baseline digest, physical owner, and existing authenticated `/admin/api/tasks/{id}` history adapter. It does not claim these foreign records are readable as jobs owned by that physical executor. The import budget remains 4096 records.

## External lifecycle controller

`scripts/gptadmin_authority_controller.py` uses a pinned private per-node manifest and durable per-step receipts. Its preflight only reads the actual service and helper configuration. Capture saves the effective environment privately for the single operation; credentials are never printed or passed as argument values. Linux requires a settled systemd stop including job/control PID and cgroup state. macOS uses the existing launchd manager and regenerates its plist from the actual canonical environment. HAOS runs lifecycle commands from the separate SSH add-on and the offline helper in the pinned installed image, with no network and only the node data and operation directory mounted.

`scripts/gptadmin_authority_fleet.py` runs on independent44 orchestration. It fences100 admission first, stops all six active nodes, freezes the44 retained-history baseline, prepares/applies all exact CAS plans, then durably retires snapshot rollback before starting any role. It verifies44/88 locally before calling the separately reviewed ingress dispatcher through ordinary SSH. No MCP or GitHub request is required after the first fence. All remaining readers must pass authenticated public membership convergence before acceptance; ordinary public MCP execution remains an independent final check.

Before activation, recovery uses the exact original journals. After activation, recovery exports the complete current44 authority, retains the exact frozen node-local history, and performs a fresh reverse transfer. It pauses restored updater jobs again. An uncertain start is reconciled using actual service state and normal stop, never a repeated start. An uncertain ingress dispatch is read through its durable status; an installed file with failed reload is not acceptance. Unresolved ingress state stops automation with one writer and retains receipts for independent reconciliation. No old snapshot rollback is attempted after activation.

## External launch acceptance

The fleet runner is dispatched once into its own systemd unit on44. Its immutable manifest binds the exact per-node manifests, controller/helper hashes, SSH hops, ingress dispatcher, operation directory and unit. The launcher durably creates an exclusive dispatch claim before starting that unit. An uncertain launch is inspected through its original task and unit; it is never dispatched again.

Before capture or the first admission fence, the external process waits for a private operator gate that identifies the exact completed100 MCP launcher task. It independently reads that canonical task row over strict SSH with SQLite in read-only mode. The complete ID, shell owner/tool, terminal status, zero return code and stdout operation/manifest/unit/PID binding must match this running process. No controller task may stop the service executing its own launcher.

HAOS live preflight executes the exact pinned helper in the existing node container, with environment values passed privately by names. This permits SQLite's normal WAL sidecar bookkeeping while the helper uses a read-only query connection. A stopped container is refused for this live-only action. Stopped prepare/apply operations still use the isolated pinned image with no network and native runtime locks.

An identity-derived HAOS reader normally requires the canonical seed. A reviewed existing HTTPS bootstrap alias may be retained with `--identity-derived-seed-alias`, `--identity-derived-options` and `--options-sha256` only when the actual seed, pinned canonical identity URL, entrypoint digest and persistent `failover.primary_public_url` all match. The options file becomes an apply guard. The effective seed is preserved in the plan; native `memberWriterURL()` continues to prefer the canonical identity URL. Confirm that precedence against the installed runtime source before accepting such an alias.

Before main publication can trigger a new release, capture the final pinned node manifests and original updater states, then use `pause-auxiliaries`. Systemd timers and launchd updaters are restored to those original states after completion or recovery. The HAOS adapter changes only the official Supervisor `auto_update` flag and waits for existing Supervisor jobs to settle, without canceling an installation or changing application options. An aborted external launch restores the captured updater states before any admission fence. If publication is abandoned before launch, run the same `resume-auxiliaries` action through the independent node controllers.

## Operation with the former writer offline

After the transfer to node-44, clients keep the public MCP URL. The writer and other enrolled executors can accept new work while the former writer is offline. Jobs owned exclusively by the disconnected executor require that executor to return. This single-writer deployment still depends on node-44 and the ingress serving the public origin.

On the former writer, the systemd reader repair job used to restore a fixed private address for the writer. The private HTTPS frontend and the canonical public URL can use different ports. The reader dials the port in its writer URL when it applies an IP pin. To follow the deployed public route, install `scripts/gptadmin_reader_route.sh` at `/usr/local/lib/gptadmin/gptadmin_reader_route.sh` and install `scripts/systemd/gptadmin-admission-autoheal-reader-route.conf` as a drop-in for the existing reader's `gptadmin-admission-autoheal.service`. Set `GPTADMIN_WRITER_ORIGIN` to the same HTTPS origin used by clients. The job passes the IPv4 address observed on a certificate-verified request to the existing repair command. If the probe fails, it keeps the saved reader route.

The existing failover CI exercises watchdog and proxy switching. The member-outage test starts three Node processes and a TLS ingress to exercise the deployed writer/reader topology. It keeps the original MCP client connected, stops the former writer, waits for its membership lease to expire and opens a second client using the same URL and credentials. Both clients execute commands on the surviving nodes. The new client reads a result created before the interruption. After rejoin, the test executes on the returned reader and checks that the original task ran once.

For an installed fleet, confirm that the former writer's process and listener are down when testing the public client. On return, check the new heartbeat and run a command on that executor through the public endpoint. The membership can already report `ready` while the executor is still establishing its connection.
