# One-way automatic ingress failover

`scripts/gptadmin_ingress_failover.py` runs on the surviving candidate node.
It changes one configured DNS record after repeated real primary command
failures, a successful real candidate command, and a fresh authorization sync.
It never promotes an auth reader to writer, changes credentials, starts/stops
Node services, fails back, or replays a user's execution request.

Install it beside the existing `gptadmin_node_probe.py` and
`gptadmin_auth_sync.py`. Use the existing environment variables containing
ordinary managed probe credentials and the provider's existing credentials.
The only child commands are the existing harmless command probe and the
explicit provider executable. No shell evaluates provider arguments.

```sh
python3 /opt/gptadmin/scripts/gptadmin_ingress_failover.py once --config /etc/gptadmin/nodes/ingress-failover.json
python3 /opt/gptadmin/scripts/gptadmin_ingress_failover.py run --config /etc/gptadmin/nodes/ingress-failover.json
```

`once` performs one observation/transition; `run` repeats after `interval`
seconds following completion. One observation may take two probe deadlines
plus two provider deadlines. Install exactly **one controller on candidate B**;
a local lock prevents duplicate processes for the same state path, not
controllers on different machines. Disable any competing record writer.

Example configuration (documentation IPs; provision the parent directories):

```json
{
  "primary": {
    "url": "https://canonical.example",
    "connect_to": "192.0.2.10",
    "target": "shell:primary",
    "token_env": "GPTADMIN_CODEX_MCP_BEARER"
  },
  "candidate": {
    "url": "https://node-b.example",
    "connect_to": "192.0.2.20",
    "target": "shell:nodeB",
    "token_env": "GPTADMIN_CODEX_MCP_BEARER"
  },
  "auth_sync": {
    "writer_url": "https://canonical.example",
    "writer_connect_to": "192.0.2.10",
    "reader_url": "http://127.0.0.1:19002",
    "source_id": "EXISTING_PRIMARY_SERVER_ID",
    "status_file": "/var/lib/gptadmin/nodes/reader/auth-sync/status.json",
    "max_age": 120,
    "allow_loopback_http": true
  },
  "provider_argv": [
    "/usr/bin/python3", "/opt/gptadmin/scripts/gptadmin_dns_sweb.py",
    "--zone", "example", "--name", "failover-canary.example",
    "--transport", "/usr/local/bin/swebmimic", "--creds", "/root/.sweb-creds"
  ],
  "name": "failover-canary.example",
  "expected": "192.0.2.10",
  "value": "192.0.2.20",
  "failure_threshold": 3,
  "interval": 10,
  "probe_timeout": 15,
  "provider_timeout": 95,
  "state_file": "/var/lib/gptadmin/nodes/reader/ingress-failover/state.json"
}
```

Origins require verified HTTPS. Optional literal-IP `connect_to` retains
logical hostname/TLS/SNI and must equal the respective DNS expected/value IP.
Without a pin, provision an independently stable physical origin; the controller
cannot prove DNS stability. Explicit `allow_loopback_http: true` per probe
allows only literal loopback HTTP for isolated process tests. The sync source
and primary probe may use different logical HTTPS hostnames only when both
have the same explicit IP pin and effective port (omitted HTTPS port means443).
This permits an isolated canary hostname at the primary's existing physical
endpoint while auth sync continues using the canonical hostname. Without both
pins, their origins must match exactly; unequal pins or ports are rejected.
TLS still verifies each requested hostname separately. This establishes the
same TCP endpoint, not identical virtual-host routing: the operator provisions
the canary vhost to the actual primary Node. The existing trusted auth-sync
writer URL/status binding is retained unchanged, never rewritten to the alias.
The complete
`auth_sync` object is passed to the existing configuration/freshness functions;
there is no second interpretation of the freshness binding. Its reader URL
must describe the same local B deployment tested by the candidate probe.
As with auth sync, a trusted local status file is not remote identity proof.

## Provider contract

The configured argv reads one small JSON object from stdin and emits one small
JSON object on stdout, then exits. A separate invocation handles each action:

```json
{"action":"read","name":"failover-canary.example","expected":"192.0.2.10","value":"192.0.2.20"}
```

For mutation the action is `compare_and_set`; all other fields are identical.
Success must exit zero and return `{"ok":true,"value":"192.0.2.20"}` (the
current IP for reads). The adapter must compare against `expected` before its
single mutation, reject ambiguous/multiple records, and preserve unrelated DNS
records. The adapter's actual concurrency guarantees are provider-specific;
the controller does not turn a read-then-write API into atomic CAS.

The default provider deadline is95 seconds, allowing the SpaceWeb adapter's
three sequential RPCs bounded at25 seconds each; it is configurable up to300.
Each subprocess has a deadline, at most 16 KiB stdout, discarded stderr and no
shell. Timeout/error kills its owned process group. Provider output and probe
stdout are never copied to controller logs or state. Transport uncertainty is
not a reason to repeat a mutation.

## Durable state and decisions

- `watching`: healthy primary resets the consecutive failure count. At the
  threshold, a failed candidate command or stale/malformed/unbound auth sync
  prevents DNS mutation. DNS must still equal `expected`. Freshness is checked
  after the candidate command and again after the DNS read.
- `pending`: written and fsynced **before** the sole compare-and-set invocation.
  Timeout, malformed output, controller interruption or lost acknowledgment
  leaves the durable decision pending. Every later cycle/restart only reads DNS.
  The expected old IP leaves it pending; the desired IP resolves it to promoted;
  another IP blocks it. A crash between persistence and invocation can therefore
  require manual recovery even though no write occurred. This is intentional.
- `promoted`: latched; no further DNS/probe actions and no automatic failback.
  It means the adapter acknowledged/observed the configured value, **not** that
  authoritative servers and client caches have converged.
- `blocked`: latched DNS conflict; operator investigation required. Explicit
  adapter `ok:false` errors `other_records_changed` and `record_conflict` are
  allowlisted semantic conflicts and immediately persisted as blocked, including
  after a write. A later read of the desired IP cannot erase this conflict:
  blocked controllers make no provider calls, also after restart. Other adapter
  errors such as `write_unresolved` leave a write pending; raw error text is
  never logged or persisted.

Configuration and state are bounded JSON objects. State is config-digest-bound,
atomic, mode0600, and retained across restart. Only one state file, one lock,
and a transient fixed atomic-write file are used. Unchanged polling state is
not rewritten. Missing state starts a new controller decision history: never
delete pending/promoted state or point at a new state path to retry automatically.
Rearming or changing config is an explicit operator action after reconciling the
provider and stopping the controller. `once` exits0 for a healthy primary or
latched promoted state,1 for unsuccessful/pending/blocked checks,2 for config,
state, lock or persistence errors. `run` stays active through observation errors.

## Availability boundary and real acceptance

B stays an auth-reader. Existing credentials continue operating with its last
snapshot after primary loss, even after freshness expires or B restarts.
Revocations after that snapshot can remain unknown. OAuth refresh, registration
and authority mutations remain unavailable; issued JWT expiry still applies.
Freshness governs the new decision, not ongoing command admission.

A DNS timeout cannot prove the primary is dead. A partition may leave both
public ingresses serving, but only the original writer retains auth authority.
Jobs stay at their explicit executor; unknown execution outcomes are inspected
at that same owner using the existing job ID/owner target, never replayed on
another machine. Probe calls are separate harmless commands with new keys.

Logic checks: `python3 -m unittest discover -s tests -p test_ingress_failover.py`.
Their mocked decisions and subprocess I/O checks are not DNS acceptance.

The real acceptance uses a dedicated DNS hostname with valid TLS on both
actual Nodes and the real adapter. Execute on B through A, stop only the canary
A ingress, let B's controller switch the record, then use an external client
with ordinary DNS (no IP pin) and the unchanged hostname to read the previous
receipt and execute a new command on B. Verify one side effect, anonymous401,
reader mutation rejection, restart persistence and no automatic return when A
recovers. Record detection, provider acknowledgment, all-authoritative
convergence and client recovery separately. Existing SpaceWeb TTL600 and
observed multi-minute propagation are not a short-RTO guarantee.

## Verified deployment, 2026-09-09

The controller (`c4b7ad0`) and SpaceWeb adapter (`bf0201b`) ran on VUSA,
independently of the primary host. Its runtime `swebmimic` dependency requires
the connection-level no-replay fix from `megamen32/sweb-dns-mcp` commit
`3d53b63`: disabling only outer retries is insufficient. A real local TLS
connection regression reproduced two accepted POSTs with the old request and
one with the corrected request after the server dropped its response.

The isolated public hostname used actual primary/VUSA Nodes and verified TLS.
After a marker disabled only its primary ingress, the controller updated the
existing A record in place. The external VPN2 client used ordinary DNS on every
new connection, with no IP pin or cache flush. It recovered the previous VUSA
receipt, then executed one new command there; original/new side-effect counts
were both one. Anonymous401 and reader-registration503 were verified on the
actual VUSA connection. The primary canonical path remained operational.

Measured from the isolated fault: provider confirmation51.381s, first full
four-authority TCP/AA observation363.260s, first external receipt388.637s,
complete consumer check623.086s. The last value includes correcting/resuming
the observer when consecutive DNS resolutions selected different IPs; it is
not pure recovery latency or an RTO guarantee. Negative checks hitting the old
IP were not counted as evidence for the reader.

The promoted state survived a controller restart. Restoring the primary test
ingress did not change DNS back; all four authorities still returned VUSA.
The test watcher was then stopped with its state retained. The production
canonical watcher uses the same controller with its own state/config. Its
watcher is enabled on VUSA and reports healthy `watching`, failures0, while
the production A record remains primary95. Its readiness was checked with a
real managed command at the canonical hostname pinned to VUSA. A fault of that
production hostname or the entire primary machine was not performed here.
