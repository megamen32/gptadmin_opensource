# Public Node release and automatic update

Only megamen32/gptadmin_opensource is the binary origin. Standard public GitHub
runners build Linux amd64 and arm64; publication runs after the full product gate.
The job uses only the ephemeral built-in github.token with contents:write. No
private checkout, PAT, SSH key, fleet credential or custom GitHub secret is read.
The sanitized source publisher excludes private/planning/task data and scans the
committed public closure before pushing. Node tags do not replace legacy latest.

The channel is node-v<build>-<source-prefix>. Both architectures must bind the
same clean full source SHA. Exact archive closure, internal payload hashes,
external checksum and GitHub asset size/digest are checked. The lightweight tag
is created and verified before draft assets are uploaded; drafts are inspected by
release ID, not the published-only tag endpoint. Existing tags/assets are never
overwritten. A interrupted draft is resumed only with identical verified inputs.

## Managed Linux Node updater

The updater and its manager are part of each public Node package. It is an explicit
per-host opt-in: run `gptadmin node autoupdate enable`; inspect with
`gptadmin node autoupdate status`, run a one-shot with `gptadmin node autoupdate run`,
or disable with `gptadmin node autoupdate disable`. Enable enrolls every active
Node instance on that host in `/etc/gptadmin-node/autoupdate.json`; the updater
refuses to run if the live instance set differs from enrollment. The config is
root-owned and explicitly stores `enabled: true` only after opt-in.

The primary server's `gptadmin-hub.service` is also a unified Node process, despite
its historical unit name. Opt-in recognizes only the live `gptadmin-node` binary
with its protected `/etc/gptadmin/nodes/primary.env`, seeds the existing binary as
the rollback version, and changes the service's next start to the shared verified
version pointer. It does not restart the service while enabling. A package change
uses the same native drain, ready-to-stop, switch, restart, health and rollback path
as ordinary `gptadmin-node@` instances.

The updater uses the GitHub Releases API when available. On an explicit API rate
limit response it uses the public Releases Atom feed for ordering, verifies the
immutable lightweight tag through unauthenticated `git ls-remote`, downloads only
the fixed release asset URLs, checks the published SHA-256 sidecar, and validates
the package's internal manifest and source binding. It never falls back to an
unverified binary. A confirmed busy drain with confirmed cancellation is deferred
to the next timer run; unknown cancellation, update or rollback state remains held
for owner recovery.

Automatic update requires supported drain and ready_to_stop, then uses the native
manager's switch/restart/health/rollback path. SIGALRM is a catchable 50s operation
deadline followed by 80s cleanup reserve. The parent allows 145s and starts no
slice without that reserve in its overall 165s run. A lost drain-begin response
is cancelled in the cleanup reserve. Unsupported drain, failed cancel, update,
rollback or unknown parent timeout creates a protected hold.json: later timer
runs refuse mutation until the owner verifies recovery. Healthy completed
instances are recorded separately; a later refusal never claims whole success.
An atomic600 fsynced hold is persisted BEFORE the manager call and cleared only
after live verification and a fsynced partial receipt. Crash/power loss retains
the hold. No action or request is replayed to resolve an UNKNOWN outcome.

Finite proposed service budget: RAM256/384MiB, swap0, CPU1/GOMAXPROCS1,
Tasks64, IOWeight10, systemd wall180s, downloader128MiB/archive payload256MiB,
temporary disk384MiB plus 512MiB available disk reserve. Downloads accept only
fixed HTTPS public GitHub hosts, no credentials/cookies, bounded redirects,
20s socket timeout, bounded bytes and remaining wall. Native Node service budgets
are separate and preserved. Measure host/UID reserve before runtime installation.
Timer6h plus up to30min jitter, first boot10min. The primary plus each active
managed Node instance must be explicitly enrolled; the fleet audit receipt records
which hosts are enabled. Offline or non-Node roles are never reported as updated.

This channel covers the unified Linux Node runtime. It does not update HAOS add-on
images or retired VPN-only hosts. Public channel publication is not fleet update
acceptance; record installed source and genuine MCP execution for every active
Node host.

## Validation

Fast units: archive/auth/URL/tag and lifecycle failure logic; expected3/max25s,
192MiB/CPU15s/temp16MiB. Real SIGALRM unit proves catchable control flow, not
native service rollback. Real controller/package/service acceptance remains a
separate gate. The first cloud run passed all9 product checks but publication
failed draft-tag lookup404; preserve that failed receipt. No secrets printed.
