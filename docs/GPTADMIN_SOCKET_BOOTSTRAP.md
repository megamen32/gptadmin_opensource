# GPTAdmin: one-time zero-gap socket bootstrap

This procedure migrates the primary Hub from a legacy process-owned listener on `127.0.0.1:9001` to the canonical `gptadmin-hub.socket`. It is deliberately separate from normal Node updates: after this bootstrap, the ordinary socket-primary release controller owns all later releases.

## Safety contract

1. One production writer at a time. A temporary green writer may start only after the old writer's `MainPID` has exited.
2. Every public request is either served or held by the bounded bridge. The controller never replays requests.
3. Local direct clients are redirected to the bridge only during HOLD windows. The bridge's dynamic UID is exempt so it reaches the active writer without recursion.
4. The package is verified by the already-installed trusted manager. The extracted closure, manifest digests, native `/version`, and handoff capability must match the candidate commit before traffic changes.
5. `current` and `previous` point only to immutable, manifest-verified version directories. Every phase is written to a root-owned `0600` receipt.
6. The canonical socket stays active while its `Service=` target changes from the temporary green service to `gptadmin-hub.service`; the kernel listener and backlog are not closed.
7. Secrets are never copied to argv, receipts, stdout, or bridge logs. Existing environment files and auth state are reused in place.

## Required evidence

- Valid Agent Herder project release lease and receipt ID.
- Clean checkout whose `HEAD` equals the candidate package commit.
- Package SHA-256 from that exact checkout.
- `gptadmin-hub.service` active and `gptadmin-hub.socket` inactive.
- Verified old fallback slot and current manifest revision.
- Current live `/version` value, supplied independently from the old manifest revision.
- Continuous public/authenticated canary already running.

## Migration command

Run from the clean candidate checkout as root, replacing placeholders only with read-back receipts:

```bash
python3 scripts/gptadmin_socket_bootstrap.py migrate \
  --package /absolute/path/gptadmin-node-linux-amd64.tar.gz \
  --package-sha256 <64-hex> \
  --expected-revision <verified-old-manifest-sha> \
  --expected-live-version <current-/version-git_commit> \
  --candidate-commit <exact-clean-HEAD> \
  --controller-commit <same-exact-clean-HEAD> \
  --previous-dir <verified-old-version-directory-name> \
  --release-lease-id <Agent-Herder-receipt-id> \
  --release-lease-proof /root/gptadmin-release-proof.json \
  --release-lease-fence <fence> \
  --release-lease-owner-harness <harness> \
  --release-lease-owner-session <session> \
  --operation-id gptadmin-bootstrap:<candidate-sha> \
  --public-origin https://became.bezrabotnyi.com
```

The command refuses a dirty checkout, package mismatch, unknown nginx upstream, occupied bridge port, active canonical socket, foreign/incomplete receipt, or release lease mismatch.

## Durable phases

`preflight` → `candidate-installed` → `bridge-forward-old` → `nginx-bridge` → `public-via-bridge` → `old-quiesced` → `cutover-prepared` → `old-stop-intent` → `old-stopped` → `slot-switch-intent` → `slot-switched` → `green-active` → `canonical-socket-retargeted` → `canonical-active` → `committed`.

`cutover-prepared` is written, with unit-file backups and the intended `current`/`previous` pointers, before the old writer is stopped and before any unit or symlink mutation. `old-stop-intent` and `slot-switch-intent` are the durable markers for those two mutations. A process death leaves the last phase in `/var/lib/gptadmin-node/primary/socket-bootstrap-receipt.json`. Recovery reconstructs from that phase: it does not stop the old writer at `cutover-prepared`, and after `old-stop-intent` it starts the verified old writer only when both writers and the canonical socket are proven stopped.

## Release lease

The ledger is the live Agent Herder document `version: 1` at `~/.local/state/agent-herder/project-release-leases.json`. There is no separate schema string. Admission uses the nested `projects[<canonical .git path>].lease` object exactly as `release_gate.ReleaseGate.check` does: project path, owner (`hostId`, `harness`, `nativeSessionId`), fence, `expiresAt`, `tokenHash` of the proof token, and the full `deploymentTarget`. A receipt id alone is not a lease. Pass the proof in a mode `0600` file via `--release-lease-proof`; the token is not accepted on the command line and is not written to the receipt. Migration requires at least 10 minutes of remaining lease at preflight and rechecks the same identity before every irreversible boundary. Recovery may present a new active lease with the same project, owner, and deployment target and a fence greater than or equal to the original. The receipt records both the original and recovery lease summaries, never the token or token hash.

## Reboot

`gptadmin-bootstrap-bridge.service` is enabled, `Restart=no`, and ordered `Before=nginx.service`. `gptadmin-bootstrap-reconcile.service` is also enabled and ordered before nginx. On boot it starts the bridge only when the receipt still needs it, and it never restarts an active bridge. If the bridge unit is gone, reconciliation exits without starting a writer; `recover` still restores from the receipt.

## Rollback

Rollback holds the bridge when it is active. It starts the verified old writer only after `systemctl stop` has succeeded for the primary service, the temporary green service, and the canonical socket, and only after both service MainPIDs are 0 and their cgroups are empty. If that proof fails, the receipt becomes `manual-required`, the bridge stays in HOLD, and no writer is started. A missing bridge does not block recovery once the stop proof succeeds.

## Package snapshot

The candidate archive is copied through an `O_NOFOLLOW` file descriptor into a root-owned mode `0444` snapshot under `/var/lib/gptadmin-node/primary/packages/`. Extraction reopens that snapshot, not the caller path, and refuses a changed inode, size, or digest. The receipt records both paths. Symlinks and concurrent mutation are rejected.

## Relay

The bridge sends each accepted buffer once. A hard error after any byte is counted in `partial_write_errors` / `partial_write_bytes` and the connection is dropped. Those bytes are not replayed and the bridge does not redial that request.

## Recovery

Acquire a still-valid lease, or a renewed lease that passes the continuity rules, and run:

```bash
sudo python3 /usr/local/lib/gptadmin/bootstrap/<candidate-sha>/gptadmin_socket_bootstrap.py \
  recover --release-lease-id <Agent-Herder-receipt-id> \
  --release-lease-proof /root/gptadmin-release-proof.json \
  --release-lease-fence <fence> \
  --release-lease-owner-harness <harness> \
  --release-lease-owner-session <session>
```

Recovery is rollback-only. It restores the verified old slot, unit files, nginx upstream, and local redirect, then writes `rolled-back`. It refuses a foreign owner, project, target, or regressed fence. `manual-required` means automated recovery could not prove an invariant; preserve the bridge before any further change.

## Acceptance

- public canary: zero 5xx, connection refused, and timeouts; bounded latency;
- `gptadmin-hub.socket` and `gptadmin-hub.service` active;
- exactly one production writer;
- `/version.git_commit` equals the merge commit and build version;
- `current` is candidate and `previous` is verified old;
- bootstrap and normal release receipts are `committed`;
- cold and warm broad search are bounded, `complete=true`, `pending=0`, and no retry hint is emitted when only stale/offline children are unavailable;
- targeted AgentHerder and Entity Finder searches are complete and fast;
- shell and temporary file write/read/delete work through GPTAdmin;
- autoupdate performs one verified no-op before its timer is re-enabled.
