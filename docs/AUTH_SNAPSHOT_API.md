# Explicit auth continuity

This slice transfers existing credentials to another unified node. It does not
elect a primary or promote a reader automatically.

`GPTADMIN_AUTH_MODE` defaults to `legacy`, preserving the previous file layout
and behavior. Set it explicitly to `writer` or `reader` to enable snapshots.
Both modes require the unified node's pinned local executor identity.

- Writer: auth issuance, registration, rotation, revocation and profile changes
  remain available through the existing APIs and MCP management tools.
- Reader: existing credentials can execute locally. Authority mutations are
  rejected, including OAuth registration/authorization-code exchange/refresh
  and calls through MCP management wrappers.
- Reader bootstrap requires `GPTADMIN_AUTH_SOURCE_ID` equal to the writer's
  existing `server_id` from `shellmcp_identity.json`. This is a public identity,
  not a new credential.
- `GPTADMIN_AUTH_SNAPSHOT_MAX_BYTES` defaults to 262144 (256 KiB); the maximum
  is 10485760 (10 MiB). Existing per-store limits also apply. Oversized bundles
  are rejected whole, without trimming credentials or revocations.

Provision the existing OAuth signing secret, key ID, logical `PUBLIC_ORIGIN`
and `MCP_RESOURCE` separately. Physical listener addresses can differ from the
logical issuer/resource. Neither signing secrets nor CTL credentials travel in
snapshots. Node private identity, jobs, registry, audit and command history are
also excluded. Profile-referenced instruction sets must already exist locally.

## Owner API

Both endpoints require `Authorization: Bearer <existing CTL_TOKEN>` for that
node. An ordinary managed client or delegated admin cannot use these endpoints.
Responses use `Cache-Control: no-store`; bundles contain private token-store
values and must use the owner's existing trusted transport.

`POST /admin/api/auth-snapshot/export` on the writer returns:

```json
{
  "version": 1,
  "writer_id": "existing-node-server-id",
  "generation": 2,
  "tokens": {"tokens": {}},
  "clients": {"clients": {}},
  "profiles": null
}
```

The actual bundle includes the existing managed token and OAuth registration
records. `profiles: null` explicitly declares the optional profile store absent;
omitting the field is invalid. Export advances and persists the generation.

`POST /admin/api/auth-snapshot/apply` on the reader takes that entire JSON body.
Success returns `ok`, `writer_id` and `generation`. Foreign writers and
generations less than or equal to the last applied generation return 409.
Oversized request bodies return 413. The writer cannot apply an incoming bundle.

## Persistence and handoff

Enabled nodes use the existing store formats under
`<config-dir>/auth-continuity/slot-0/` and `slot-1/`. The active slot is selected
by an atomically written `auth-continuity/state.json`. At most two generation
slots are created. Original legacy files are preserved but are no longer the
active stores while this mode is enabled.

Apply writes a durable pending marker, writes the inactive slot through the
existing file locks and atomic writers, then commits the manifest and switches
the active stores. Authentication/admission and authority mutations are excluded
during this operation. Size-bounded request bodies are read before taking
either generation lock, so incomplete client input cannot block auth sync.
An admitted execution keeps its captured claims/profile
and releases the gate before executor or peer waits. Revocation does not cancel
already accepted commands, but new requests use the applied generation. An
interrupted apply remains fail-closed after restart; repair the cause and retry
the newer bundle using the owner endpoint. Missing mandatory files, unexpected
optional files and modified reader-generation contents cannot silently become
empty/default authorization state. Health and the local executor queue remain
available for bootstrap and receipt delivery.

After stopping or otherwise fencing the old writer, the owner can explicitly
restart a seeded reader with `GPTADMIN_AUTH_MODE=writer`. It keeps the imported
credentials and generation, and uses its own existing node identity for future
exports. A returning old node must be configured and synchronized as a reader
before it receives authority again. Starting two writers is not made safe by
this slice; there is no quorum, lease, partition-freshness guarantee or automatic
handoff. Authority changes are visible remotely only after successful apply.

OAuth refresh credentials remain single-use and survive restarts. Already
issued access JWTs retain their existing expiry behavior; this slice does not
add immediate access-JWT revocation. Unredeemed authorization codes remain
process-local and are not included in snapshots.

## Real acceptance

```sh
python3 tests/e2e/node/auth_run.py /absolute/path/to/candidate/gptadmin-node
```

The canary starts two real binaries with separate config directories and CTL
credentials. It issues an ordinary `role=client` token through the owner API,
performs real OAuth registration, password authorization, PKCE and Basic code
exchange with `offline_access`, and executes commands through B's `/mcp` using
those credentials. It repeats after B restart, verifies managed-token revocation
after resync and restart. A five-second synchronous command remains running
while a revocation snapshot applies and the next request is denied within two
seconds. The canary then stops A, explicitly restarts B as writer, and verifies
Basic refresh, a new JWT command and rejection of the old refresh credential
across another restart. CTL credentials are used only for provisioning/control.

## Read-only legacy bootstrap before primary migration

Build `go build ./cmd/auth-snapshot-export` from `go-hub`. The one-shot command
uses the existing native file locks and JSON parsers without constructing a Hub,
starting a daemon, or writing to the source configuration directory:

```sh
# CTL_TOKEN must already be present in the command environment; do not pass it
# as an argument. writer-id is the primary's existing shell server_id.
auth-snapshot-export --config-dir /etc/gptadmin \
  --writer-id EXISTING_PRIMARY_SERVER_ID \
  --output /secure-staging/bootstrap-auth.json
```

The output must be a new file outside the source directory; existing files and
symlinks are rejected. It contains secrets, is created with mode0600, and is
never printed to stdout. `--max-bytes` defaults to262144 and is capped at10MiB.
Mandatory token/client stores and their existing `.lock` files must be readable;
the helper does not create missing locks. An absent optional profile store is
explicitly represented as null. Referenced instruction sets are validated on the
receiving node and must be provisioned separately. A source already containing
`auth-continuity/` is rejected: use its writer API instead of stale legacy files.

This helper emits **generation1 only**, for an unseeded reader configured with
that source identity. An already seeded reader rejects a second generation1
apply. It is a bootstrap, not a periodic synchronization mechanism. After the
old primary is drained and stopped, the unified writer can initialize from its
unchanged legacy stores with the same existing node identity; its first API
export is generation2 and advances beyond this bootstrap. Do not run parallel
production writers. Ordinary clients continue using separately provisioned
existing signer/keyID/issuer/resource settings.

Native executable/filesystem validation:

```sh
python3 tests/e2e/node/auth_export_run.py /absolute/path/to/auth-snapshot-export
```

The canary holds each native file lock to prove the actual CLI waits, verifies
source contents/modes/mtimes remain unchanged, checks private output, retained
revocation/token values, CTL exclusion, and refusal to overwrite existing output
or create missing source locks. All fixtures live under the project's `.tmp/`.
