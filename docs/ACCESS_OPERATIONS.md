# AI-first access administration and operation history

## One management contract

The Hub exposes `access_profiles`, `access_clients`, `instruction_sets`, and `operations` through
`schema(target="hub")` and the `exec` facade. The tools call the same
owner HTTP handlers as the web UI, forwarding the caller's existing verified
credentials. They do not require shell commands or manufacture an owner session.

Examples of tool arguments:

```json
{"action":"create","id":"inspection","profile":{"name":"Inspection","access_mode":"readonly","allowed_targets":["shell:host"],"allowed_tools":["system_inspect"],"instruction_set_id":"default"}}
```

```json
{"action":"issue","client_id":"inspection-client","profile_id":"inspection","access_mode":"readonly","ttl_days":7}
```

`access_profiles` supports list, get, create, update. Updates require the `etag`
returned by get; creating an existing ID is rejected rather than overwritten.
The `instruction_sets` tool supports list, get and update for the shared
`default` and existing named instruction sets. Get returns the content and
ETag; update requires that ETag and the full replacement content, reuses the
quorum-backed admin handlers, and returns an operation receipt. Existing
admin/owner authority and profile target/tool/action rules remain required.

```js
const set = await mcp.call("hub", "instruction_sets", {action: "get", id: "default"});
return await mcp.call("hub", "instruction_sets", {
  action: "update", id: "default", etag: set.etag,
  content: set.content + "\n\nNew instruction."
});
```

`access_clients` supports list, issue, bind, unbind, and revoke for an exact ID.
These clients are named MCP/OAuth connections, not local OS accounts or a new
human-login directory. Owner/admin-cookie or existing CTL authentication is
still required. An ordinary MCP execution token does not automatically become
an access administrator. Delegated named admin roles remain separate work.

## Profile fields and explicit limits

The Go contract uses `workspace_refs`. The UI now reads and writes that name,
preserves `instruction_set_id` and `approval_mode`, and omits wholly empty
optional workspace rows. It retains compatibility when reading older UI-shaped
fixtures using `external_workspace_refs`, but never writes the old field name.

A read-only profile takes precedence over a token's execution capability.
`allowed_targets:["*"]` and `allowed_tools:["*"]` explicitly allow all; empty
lists still allow none. `approval_mode:"unrestricted"` disables the additional
approval/budget gate for that profile. Existing profiles and defaults are not
silently broadened or reset. The UI exposes these choices and displays their
meaning instead of concealing behavior in missing fields.

Filesystem roots are optional, independently enforced fields on each profile:
`read_roots` limits file reads and `write_roots` limits file changes. The Hub
preserves the difference between an omitted field and an explicit empty array.
If both fields are omitted, the profile uses the legacy filesystem behavior.
If either field is present, filesystem ACL enforcement is active: an omitted
other field means deny that operation, and `[]` explicitly denies every path
for that direction. A write root does not imply a read root. Read/modify
operations such as `file_editor` replace or batch-edit also require the path to
be inside `read_roots` because they inspect the original content.

The roots are absolute paths on the selected ShellMCP host. The executor
requires canonical roots and checks the resolved target against the applicable
read or write roots. The Hub binds each queued ACL envelope to the authenticated
profile ID and version, exact `shell:` or `file:` target, tool, arguments and
root digest; the node accepts it only from its authenticated Hub queue and
revalidates the profile before execution. These policy fields are not accepted
from MCP tool arguments. Scoped ACL calls require an approved ShellMCP executor
that advertises `filesystem_acl_version: 1`; otherwise the Hub denies the call.

With either root field configured, execution is deliberately narrow. The
filesystem-bound callback exposes only `system_inspect`, `file_editor`, and
`system_resource_summary`; shell execution, child-MCP calls, code mode, MCP
`resources/list` and `resources/read`, `file_backup`, and `file_checkpoint` are
denied. This also prevents backup or checkpoint actions from becoming alternate
paths around the root policy. The existing `access_mode` still applies
independently: `readonly` cannot write even when `write_roots` contains paths,
while `full` remains subject to the configured roots and the profile's other
target/tool/approval rules.

The Hub performs these checks only when the request is bound to that profile's
authenticated bearer. It does not trust root values supplied as tool arguments
or by an unrelated caller.

A new connection can be issued with `profile_id` in its original persisted
record. It does not pass through a temporary unbound state. A delayed profile
list response cannot erase a new profile draft the operator has begun editing.

## Department scopes

An operator can ask an agent to let a manager operate the devices of a department
and its subdepartments. Use the existing `hub.access_profiles` tool on a trusted
administrative connection. Select `schema` with `target=hub` and
`tool=access_profiles` to discover its arguments. Create each role as a normal
profile, initially with empty `allowed_targets` and the exact tools that role
may use. Bind each department client to its profile at issuance.

For example, pass this configuration to `access_profiles`:

```json
{
  "action": "plan_tree",
  "id": "company",
  "groups": {
    "company": {},
    "engineering": {"parent": "company"},
    "field": {"parent": "engineering", "targets": ["shell:field-worker"]},
    "finance": {"parent": "company", "targets": ["shell:office-node"]}
  }
}
```

The preview lists the previous and proposed exact targets of every affected
profile. Submit the same configuration with `action=apply_tree` and its
`etag`. The Hub writes the profiles in one existing access-state transaction.
The engineering client can use the field executor, finance can use the office
node, and company covers both. Each role retains its own tools, filesystem
limits and approval policy. A manager uses an ordinary profile-bound client
credential. Changing the department configuration requires access administration.

Use a complete group map when moving devices or entire branches. A previously
included profile omitted from the map loses its allowed targets in the same
transaction. An empty map releases the entire tree. Existing client bindings
remain in place. A stale plan fails with HTTP 412; read the changed state and
plan again. Each profile belongs to at most one tree. Device assignments accept
concrete shell, file, control or child MCP targets. Inspect the source of an
effective target list through the profile's `target_group` (root, parent and
direct targets). This groups authorization at the Hub; network routes, peer
trust and Raft membership are configured independently. When upgrading a
replicated deployment, write tree-managed policies only through versions that
preserve the target group fields.

The same authenticated operations are available as POST (preview) and PUT
(apply with `If-Match`) at `/admin/api/access-profile-trees/<root-profile-id>`.
Run the isolated product scenario with
`python3 tests/e2e/node/access_tree_run.py /path/to/gptadmin-node /path/to/shellmcp-go`.
It checks the original credentials before and after a department transfer on
a real unified Node and a separate ShellMCP.

## Durable access operations

`GET /admin/api/operations` and the `operations` tool list access changes newest
first, with pagination. The "Операции доступа" screen uses that same API.
Operation records are appended to the existing `audit.jsonl`; this is not a
second logging service. Every tracked change records a synced started event
before applying the handler and a synced completed/failed event before replying.

Recorded fields: operation ID, actor, method, object path, timestamps, outcome
and HTTP status. Request bodies, token values and result bodies are excluded.
A started operation with no completion stays visible after restart. It means
inspect the object before retrying, not that the action should automatically be
executed again. Failures to record the initial event prevent the mutation;
failures to record completion explicitly state that the underlying action may
already have succeeded. The reader scans the existing log on demand; it is not
an indexed analytics engine or an unbounded background poll.

Command execution and output remain under Tasks. This change preserves profiles,
bindings and access-operation history across restart; it does not yet persist
creator-local executable queue arguments for automatic command resumption.

## Verified retirement

`public/admin_dashboard.html` was an unserved 1144-line duplicate. Repository
references were inspected: the active remnants were text tests and the Mac
source-snapshot script, not the Go runtime. The duplicate is removed; the Mac
snapshot now includes the built canonical `admin-ui/dist` as `public/admin`, and
tests target the actual operational component. Historical work logs retain
historical references. No active token or runtime service was removed by this
source cleanup.

## Shared token state and explicit administrators

Managed-token and OAuth-client files are shared between the local Hubs. Writers
hold an OS file lock, reread the current file, and merge only changed records
against their original snapshot. A stale change to the same record conflicts;
independent issuers are preserved. Unique temporary files, file sync, atomic
replacement and directory sync replace the old common `.tmp` writer. The small
configuration files remain JSON; no new credential service was introduced.
Authentication reloads authoritative token, registration and profile state.
New issuance, profile edits, role changes and revocation are visible to an
already-running standby without restarting it. Explicit revocation and token
kind always apply; optional expiry/issuer hardening remains configurable.

`access_clients` adds `whoami`, `set_role` and `token`. A connection role is
`client`, `admin` or `owner`. Both administrative roles can manage access and
view saved credential values. Full execution permission alone, or a JWT subject
named `admin`, does not grant that role. The role comes from persisted connection
metadata. OAuth connections can also receive a role on their existing client ID;
no separate OS-user or human-login directory is created. An explicit read-only
mode/profile and target/tool restrictions still apply to a delegated admin.

Example: `access_clients(action="set_role", id="<exact connection id>", role="admin")`
requires an already-authorized administrator or owner browser session. Changing
one's own role is possible only while already authorized; `whoami` reports the
current identity without promoting it or revealing credentials.

New managed bearers and refresh-token values are retained in the existing
private state file (0600). Normal inventory and diagnostic serialization excludes
the values. Admin/owner retrieval is `access_clients(action="token", id="...")`
or `GET /admin/api/mcp/tokens/{id}/value` with no-store caching. Configured
migration credentials can be viewed from existing configuration when their
current digest matches. Old hash-only values remain unavailable; viewing never
silently rotates, revokes or replaces them. Explicit rotation writes revocation
and replacement together, retaining role, binding and non-expiring lifetime.
Old binaries that do not preserve these new fields are not compatible writers.

The UI exposes role selection, role editing and "Показать сохранённый токен".
Reloading the page or restarting Hub no longer loses a newly stored value.
Bulk revocation of historical production connections was not part of this change.

## Proof

`access_management_test.go` checks owner MCP creation, connection binding,
restart, operation history, interrupted operations and denial for ordinary MCP
clients. `access_effective_mode_test.go` proves the read-only profile rule.
Frontend contract tests use actual Go fields. `tests/e2e/unified_admin_ui.py`
creates/edits a profile in a real browser, issues a bound connection, restarts
its isolated real Hub, and verifies restored fields and visible history.

Shared-access regressions are `access_shared_state_test.go` and
`access_shared_http_test.go`: concurrent issuance, same-record conflicts,
immediate revocation/binding/role edits, atomic rotation failure, stored values,
ordinary-client denial, and independent HTTP Hub processes. The real browser
script also reads the same stored token and role after restarting its fixture.

## Deployment checkpoint — 2026-09-05

Both local production Hub services were verified running `acb80d0`, build 193.
The independent systemd deployment completed successfully, with a private
configuration backup and compatible `835b240` fallback. Connector command
execution and each process's version were checked after the switch. No existing
production connection was bulk-revoked. Delegated-administrator behavior is
proven in independent-process HTTP/native MCP tests and the browser fixture;
activating the particular current AI connection remains an owner action because
the platform rejected that live administrative call before execution. Do not
confuse this with an unavailable Hub or claim the connection is already admin.
