# Periodic auth synchronization

`scripts/gptadmin_auth_sync.py` runs beside the **reader**, so loss of the writer
does not stop the runner. It copies the writer's existing owner-only
`POST /admin/api/auth-snapshot/export` response to the reader's existing
`POST /admin/api/auth-snapshot/apply`. No execution request is sent or replayed.
The reader remains read-only; this tool never promotes a node or changes DNS.

The writer identity must match the enrolled `GPTADMIN_AUTH_SOURCE_ID`. The
snapshot generation must increase, and the reader must acknowledge exactly that
identity and generation before `last_success` advances. Failed exports, failed
applies, malformed replies and TLS failures do not refresh success. A lost apply
acknowledgment is conservative: the next cycle exports a newer generation.

## Freshness contract

`check` exits 0 only when a successful sync is at most `--max-age` seconds old;
otherwise it exits 1 (configuration/state errors exit 2). Its JSON includes
`promotion_eligible`, `age_seconds`, `last_error`, identity and generation.
Eligibility requires a complete version-1 status (writer identity, positive
integer generation, bounded snapshot size and finite success time) and an exact
match to the expected writer origin, writer connect-to IP, reader origin and
source identity. Supply these through the same configuration as `run`. An
unconfigured `check` is informational only and always exits 1; a status with
different endpoint/source configuration cannot qualify the expected reader.
This is a trusted local status file, not portable authority: two hosts may have
identical loopback reader URLs and source settings. Promotion must separately
verify the physical reader's existing node identity/target through a real probe.
Changing deployment
configuration requires a new status path or explicit operator removal of the old
status, followed by a successful sync; the runner never silently rebinds it.
This is **one prerequisite for an explicit promotion decision**, not proof that
the old writer is fenced, the reader is healthy, or split brain is impossible.

Freshness never disables existing reader commands. During a prolonged writer
outage a reader continues admitting credentials under its last applied snapshot,
including after reader restart. Revocations made elsewhere after that snapshot
may remain unknown indefinitely. Existing issued-JWT expiry/revocation semantics
are unchanged. Reader issue/register/refresh/mutation restrictions remain in
force. There is no automatic writer promotion or failback.

Age starts at the successful export request's **start**, not its delayed apply
receipt. One atomic `0600` status file persists success across runner restarts;
failures preserve it. Wall-clock age assumes a trustworthy local clock: a time
earlier than `last_success` fails closed, but a backwards adjustment that remains
later than that timestamp can understate age. Restart never sets age to zero.
When evaluating a new promotion after a long outage, an expired status is not
eligible; do not reinterpret this check as an ongoing command-admission gate.

## Configuration

The generic service template is `deploy/systemd/gptadmin-auth-sync@.service`.
Install the script at `/opt/gptadmin/scripts/gptadmin_auth_sync.py` explicitly;
this slice does not change the legacy installer/updater. The service reads the
existing node environment and `/etc/gptadmin/nodes/<instance>-auth-sync.env`.
Create a private durable status directory before starting it. Only the status
file, one fixed lock file and a transient atomic-write file live there; snapshot
bundles remain in memory. The existing auth store retains at most two slots.

Example **additional** environment for the current VUSA reader; the instance's
existing environment supplies `CTL_TOKEN` and `GPTADMIN_AUTH_SOURCE_ID`:

The `u-f…` writer URL below deliberately selects the VUSA transport and is not
the OAuth issuer/resource identity used by the shared ChatGPT/Codex connector.

```dotenv
GPTADMIN_AUTH_SYNC_WRITER_URL=https://your-subdomain.t.became.bezrabotnyi.com
GPTADMIN_AUTH_SYNC_WRITER_CONNECT_TO=203.0.113.10
GPTADMIN_AUTH_SYNC_READER_URL=http://127.0.0.1:19002
GPTADMIN_AUTH_SYNC_WRITER_TOKEN_ENV=CTL_TOKEN
GPTADMIN_AUTH_SYNC_READER_TOKEN_ENV=CTL_TOKEN
GPTADMIN_AUTH_SYNC_STATUS_FILE=/var/lib/gptadmin/nodes/canary/auth-sync/status.json
GPTADMIN_AUTH_SYNC_INTERVAL_S=30
GPTADMIN_AUTH_SYNC_TIMEOUT_S=30
GPTADMIN_AUTH_SYNC_MAX_AGE_S=120
GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES=262144
```

Using `CTL_TOKEN` for both ends is valid only when provisioning already shares
that credential. Otherwise set the writer token environment name to an existing
separately provisioned writer-owner credential variable; do not pass token values
as command-line arguments. The reader's config/signer/issuer remain separately
provisioned as in the snapshot API contract.

`WRITER_CONNECT_TO` optionally selects a literal physical IP while keeping the
URL hostname/port for TLS SNI and certificate verification. It is HTTPS-only,
does not disable verification, and prevents canonical DNS promotion from making
the runner contact itself. Generic deployments may use a stable per-node HTTPS
URL instead. HTTP requires explicit `--allow-loopback-http` and a literal
loopback IP; remote plaintext, URL credentials, redirects and proxies are not
used. Both endpoints reuse their connections; a failed POST is not replayed
within the attempt. The next periodic attempt requests a new snapshot only.

The default snapshot budget is 256 KiB, configurable up to 10 MiB. Oversize
snapshots fail whole; revocations are never truncated. Default polling is every
30 seconds **after completion**, with a total attempt deadline of 10 seconds.
Each export currently commits a new generation even without auth changes. This
causes bounded-size disk writes; it is not a change-stream protocol. An exported
snapshot and its apply result never appear in status/error logs.

Read status without credentials:

```sh
python3 /opt/gptadmin/scripts/gptadmin_auth_sync.py check \
  --status-file /var/lib/gptadmin/nodes/canary/auth-sync/status.json --max-age 120 \
  --writer-url https://your-subdomain.t.became.bezrabotnyi.com \
  --writer-connect-to 203.0.113.10 --reader-url http://127.0.0.1:19002 \
  --source-id bd0bf8c4-f7fe-4d1b-a82c-aa0862e11bd8 --allow-loopback-http
```

Run `once` for a controlled initial sync and `run` for continuous operation.
Stop the sync service independently of the node; no service command is executed
by the runner. No production installation or activation is performed by tests.

## Real acceptance

```sh
python3 -m unittest discover -s tests -p test_auth_sync.py
python3 tests/e2e/node/auth_sync_run.py /absolute/path/to/gptadmin-node
```

The latter starts two actual Node processes and the actual sync CLI, plus local
TLS termination forwarding to the real writer. It verifies correct TLS/SNI and
wrong-host rejection, ordinary managed execution, periodic revocation convergence,
reader restart, writer outage without success extension, expired promotion status
while ordinary reader execution still works, writer recovery, two auth slots,
bounded private status files and absence of credentials in helper logs.

## Verified deployment, 2026-09-09

Primary and VUSA run Node `30428c2`; the sync runner is the reviewed `b2fdbb8`
script. VUSA runs it beside the local reader, independently of the home server.
The service synchronized generations 16–19, survived a runner restart and
continued executing with the existing managed credential; reader registration
remained 503 and anonymous MCP remained 401. Successful post-fix transfers took
0.7–1.3 seconds for 41,835 bytes. Observed systemd memory was about 10.8 MB for
the Python runner and 10.1 MB for Node after these operations, not peak limits.

The initial live attempt exposed a real lock bug: the auth gate exempted the
wrong relay-poll path, so an idle poll delayed export for up to its timeout.
This was not network transfer latency. The fix exempts the actual authenticated
poll route and releases snapshot locks before sending the response. The real
public regression kept a poll open while export completed in 0.086 seconds and
a managed command plus receipt in 0.615 seconds. Increasing the timeout alone
was not accepted as a fix. The deployed 30-second deadline remains bounded;
the generic runner default is still 10 seconds.

This proves periodic authorization continuity on these two installations.
It does not prove automatic DNS switching, writer election, or physical-node
identity from the status file alone.
