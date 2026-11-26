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

## One-time transition from a pinned private CLI

An old installation may have `/usr/local/bin/gptadmin` pointing into a fixed
private version slot. Its old manager can reject public packages before an
automatic update starts. Do not modify its validator or overwrite the slot.
For the one-time transition, obtain an independently verified public CLI and
its matching bundled manager, and the public Node archive. Verify the release
tag, full source revision, archive checksum and manifest closure before use.
Run that public CLI's `node update --name NAME --package ARCHIVE
--expected-revision INSTALLED_REVISION --require-drain` with the normal service
privileges. It retains the previous version for rollback and refreshes the
installed manager. Review the result before proceeding; do not retry an unknown
outcome.

Replace a pinned CLI entry atomically with the CLI from a verified public
release containing the installed-helper selection fix. Create the temporary
file in the entry's directory and use `os.replace`; never write through the old
symlink. For example, after verifying `PUBLIC_CLI` against its release:

```sh
sudo python3 - "$PUBLIC_CLI" /usr/local/bin/gptadmin <<'PY'
import os, shutil, sys, tempfile
src, dst = sys.argv[1:]
fd, tmp = tempfile.mkstemp(prefix='.gptadmin-', dir=os.path.dirname(dst))
try:
    with os.fdopen(fd, 'wb') as out, open(src, 'rb') as inp:
        shutil.copyfileobj(inp, out)
    os.chmod(tmp, 0o755)
    os.replace(tmp, dst)
finally:
    if os.path.exists(tmp):
        os.unlink(tmp)
PY
```

The corrected installed CLI selects
`/usr/local/lib/gptadmin/gptadmin_node_manage.py`, while an explicitly invoked
portable CLI keeps its bundled manager for bootstrap. The Node timer invokes
the managed updater, which uses the same installed manager. The public bootstrap
installer also atomically replaces an existing file or link. The legacy pinned
CLI itself cannot acquire this fix until its entry is replaced.

The named-instance KVM transition was verified with an independently checked
public 1145 CLI/manager and public 1147 archive, followed by rollback and another
update using the refreshed helper. This does not establish socket-primary or a
network timer migration; verify the relevant installed entry and timer locally.

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

### Publish the admin UI from the same Node package

`tools/build.sh node` builds `admin-ui/dist` from the same source SHA and includes
every static file under `public/admin/` in the Node package manifest. The updated
manager publishes these manifest-verified files to `GPTADMIN_PUBLIC_DIR` as part
of an ordinary primary `node update`, after the new Hub passes health checks. If
the currently installed manager predates this hook, first install the package,
then run its new bounded publisher once:

```sh
sudo /usr/bin/python3 /opt/gptadmin-node/current/cli.py node ui-publish --name primary
```

Both paths require a clean source build and check every UI file against its
manifest digest before changing the served tree. They atomically swap the static
directory and retain the previous UI beside it for rollback. A first-time
`ui-publish` does not edit environment files, change the unit, or restart the Hub.
Verify the exact `index.html` and asset files through the real `/admin/` browser
after publication.

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

## Managed macOS Node updater

The standard macOS install enables automatic updates when the user has no saved
preference. An explicit disable remains in effect across installs. The launchd
job checks GitHub at login and every ten minutes using the updater from the
active version. Network or package validation errors leave the running Node
available and are retried at the next scheduled check.

Publication runs the previous released macOS installer against the new Darwin
package to catch incompatible changes to the archive before users receive it.
The check uses the actual archive and the previous installer's validation code.

## Validation

Fast units: archive/auth/URL/tag and lifecycle failure logic; expected3/max25s,
192MiB/CPU15s/temp16MiB. Real SIGALRM unit proves catchable control flow, not
native service rollback. Real controller/package/service acceptance remains a
separate gate. The first cloud run passed all9 product checks but publication
failed draft-tag lookup404; preserve that failed receipt. No secrets printed.


## Native lifecycle reconciliation

The root-managed Linux Node can reconcile its own native timer, but only from
its exact systemd service cgroup and with a root-owned, protected opt-in and
installed helper. It invokes the existing updater in `--reconcile-only` mode;
it never downloads a package, restarts the Node, or starts an updater process
inside an MCP task. The native timer remains the sole update scheduler.
An enabled but inactive timer is restored only after verifying exact enrolled
instances and their running versions. Disabled/masked units and unknown holds
are not overridden. An operator pause uses the existing `autoupdate disable`
command. Logs expose fixed `native_lifecycle_reconcile code=...` categories.

After a successful manager run the updater stores a terminal receipt before
post-update readback. A subsequent native run reconciles that receipt only
after matching the exact source, slot, physical executable, service readiness,
and cleared drain (or inherited idle socket). An older in-progress hold can
use the protected, newer exact successful manager receipt from its existing
private log. The recovery writes a durable receipt before clearing the hold;
unknown outcomes and failed rollbacks remain held. No command is replayed.

The existing socket-primary controller is selected only for the verified
inherited listener. An ordinary voting member uses native quorum-aware rolling
updates; do not install a second competing listener or scheduler. A legacy
primary without an inherited socket still requires its supported socket
migration, not an implicit stop/start by the reconciler.
