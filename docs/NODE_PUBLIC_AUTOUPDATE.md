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

Reviewed source candidate, NOT yet activated fleet-wide. Install the existing
manager and gptadmin_node_autoupdate.py into /usr/local/lib/gptadmin root-owned,
nonwritable by users. Configuration /etc/gptadmin-node/autoupdate.json must have
{"enabled":true,"names":["exact-existing-node-name"]}. Every active Node@ instance
must be enrolled because /opt/gptadmin-node/current is a shared code pointer.
No role, environment, profile, token, disabled MCP or foreign service is changed.
The accepted package is verified with --check before enabling its timer.

Automatic update requires supported drain and ready_to_stop, then uses the native
manager's switch/restart/health/rollback path. SIGALRM is a catchable 50s operation
deadline followed by 80s cleanup reserve. The parent allows 145s and starts no
slice without that reserve in its overall 165s run. A lost drain-begin response
is cancelled in the cleanup reserve. Unsupported drain, failed cancel, update,
rollback or unknown parent timeout creates a protected hold.json: later timer
runs refuse mutation until the owner verifies recovery. Healthy completed
instances are recorded separately; a later refusal never claims whole success.
No action or request is replayed to resolve an UNKNOWN outcome.

Finite proposed service budget: RAM256/384MiB, swap0, CPU1/GOMAXPROCS1,
Tasks64, IOWeight10, systemd wall180s, downloader128MiB/archive payload256MiB,
temporary disk384MiB plus 512MiB available disk reserve. Downloads accept only
fixed HTTPS public GitHub hosts, no credentials/cookies, bounded redirects,
20s socket timeout, bounded bytes and remaining wall. Native Node service budgets
are separate and preserved. Measure host/UID reserve before runtime installation.
Timer6h plus up to30min jitter, first boot10min; no timer is currently enabled.

A managed Node updater cannot replace legacy Hub/HAOS/ShellMCP or the primary's
nonstandard service. Those roles need their own supported package/controller.
HAOS patched writable layer also needs a published image and persistent finite
caps before image recreation. Public channel publication is not fleet update
acceptance; record installed source and genuine MCP execution for every role.

## Validation

Fast units: archive/auth/URL/tag and lifecycle failure logic; expected3/max25s,
192MiB/CPU15s/temp16MiB. Real SIGALRM unit proves catchable control flow, not
native service rollback. Real controller/package/service acceptance remains a
separate gate. The first cloud run passed all9 product checks but publication
failed draft-tag lookup404; preserve that failed receipt. No secrets printed.
