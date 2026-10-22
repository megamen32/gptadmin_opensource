# Admin Profiles

An admin profile is the user-facing unit that connects instructions, access
policy, MCP capabilities and clients. It must remain easy to start with while
keeping each part independently configurable.

## Profile contract

A persisted profile will contain:

- a stable profile ID and display name;
- a versioned instruction-set reference;
- an access mode and allowed MCP targets/tools;
- an approval mode (`read_only`, `ask_before_write` or
  `bounded_autonomous`);
- client bindings;
- zero or more external workspace references.

The Hub provides a versioned `default` instruction set plus named instruction
sets through the authenticated `/admin/api/instruction-sets` CRUD surface.
Profiles may reference an existing named set; unknown references are rejected
and an instruction set cannot be deleted while a profile uses it. Updates to a
selected set affect subsequent MCP initialization and the startup resource
without restarting Hub. Instruction text remains guidance only: permissions,
approvals and authentication are authoritative.

## Fine-grained tool rules

A profile may narrow its `allowed_targets` and `allowed_tools` further with
`tool_rules`. Rules only narrow: a rule never grants a target or a tool that
the profile does not already allow.

```json
{
  "target": "file:admin-node-100",
  "tool": "file_editor",
  "actions": ["view"],
  "read_only": true
}
```

- `target` and `tool` name one canonical target/tool pair. `*` is accepted as a
  wildcard for either field, and a wildcard pair may not claim `read_only`.
- `actions` restricts the tool to the listed `action` values. A missing action,
  a non-string action and an action outside the list are all refused. A rule
  without `actions` does not restrict the action.
- `read_only` marks the operation as a read. It may only be claimed for
  operations that really are reads: a known write operation (`shell_exec`,
  `mcp_call`, `mcp_request`, `fleetExec`) and a write action of a mixed tool
  (`file_editor` `create`, `file_checkpoint` `restore`, `mcp_manage` `restart`)
  are rejected when a rule claims them safe. The check happens when the profile
  is written, not only when it is used.

When a profile declares rules, a business call that is not named by a rule is
denied. The routing facades `ro`, `exec`, `search`, `schema`, `discover`, `job`
and `expand` keep their own facade permission, and `execute` remains a routing
facade whose resolved target/tool is checked against the rules in turn — so a
fine-grained profile stays usable without becoming unrestricted.

The same args-aware check applies to every execution path: the JS bridge, the
direct `execute` facade, the per-agent MCP endpoint, the Actions/relay route
and the virtual MCP route. A rule is always evaluated against the canonical
target and tool, after aliases and `ref` resolution.

Approval and the bounded-autonomous budget follow the real operation, not the
tool name. Reading `file_editor` with `action=view` does not consume write
budget, and a script container (`exec`, `ro`) never consumes a write approval
for the whole script — every nested operation is checked on its own target,
tool and action.

Search and schema listings publish the restriction with the tool
(`allowed_actions`, `read_only_rule`) instead of hiding a mixed tool, and the
restriction is enforced strictly at call time.

Rules are persisted with the profile, deep-copied on every copy path, and the
profile version is incremented on every update that changes them.

## Network Tunnel capability boundary

The future Network Tunnel is an opt-in capability, not a consequence of
selecting a ShellMCP target. A profile that does not explicitly allow it grants
no network access. When policy enforcement is implemented, a Network Tunnel
allowance must identify the selected `agent_id`, exactly one mode (`lan` or
`internet_egress`), finite target CIDRs and TCP ports, the fixed v1 protocol
set, a finite lease, and finite stream limits. It must require the normal
approval policy before activation.

Profile policy must not expose raw proxy credentials, a generic
`connect(host, port)` permission, or an unrestricted network scope to MCP
clients. `lan` and `internet_egress` have separate approval and audit
boundaries; neither is a fallback for the other. Revoking an applicable
profile permission must revoke the capability and reset its streams when the
Network Tunnel exists. The Hub controller and isolated data-plane vertical
slice now implement the core contract; profile persistence and the semantic
`network-access` MCP facade remain integration work. The normative protocol is
documented in
[`NETWORK_PROXY.md`](./NETWORK_PROXY.md).

## External workspace reference

An infrastructure workspace remains an independent source of truth. GPTAdmin
stores only the information required to select it at execution time:

```json
{
  "machine_id": "<registered-machine>",
  "workspace_path": "<absolute-path-on-that-machine>",
  "startup_document": "AGENTS.md",
  "shell_target": "shell:<registered-machine>"
}
```

When a profile is selected, Hub resolves the registered machine and ShellMCP
target. The MCP client reads the referenced startup document before workspace
actions. The repository itself, its Git credentials and its configuration are
not copied, vendored or added as a submodule to GPTAdmin.

Instance-specific machine IDs and paths are private configuration. Examples in
the public repository stay generic; the public mirror must never receive a
private workspace checkout or its contents.

## Cloud boundary

GPTAdmin Cloud may store device identity, route and health metadata needed to
reach a registered Hub. It does not store external workspace repositories,
their credentials or their instruction files.

## Runtime requirements

- `read_only` profiles reject write-capable tools. `ask_before_write` profiles
  return a short-lived opaque approval request for each write; an operator
  approves or rejects it through the admin API, and an approved request is
  bound to the profile, actor, target, tool and argument digest and consumed
  once. Raw arguments are never stored in or returned by approval metadata.
- Approval requests are intentionally in-memory and expire after five minutes;
  a restart invalidates outstanding requests.
- `bounded_autonomous` is still profile-scoped and allowlist-scoped, and its
  write-capable calls are limited to 32 calls per actor/profile in a five-minute
  window. The response exposes only a retry time and limit, never arguments.
- Profile and workspace-reference updates use version checks to prevent stale
  writes.
- Instruction updates affect subsequent MCP initialization without restarting
  Hub.
- Tool discovery filters capabilities using the selected profile.
- Execution rejects a target or tool that the selected profile does not allow,
  even if a client constructs the call manually.
- Failover Hubs observe the same committed profile version before accepting a
  write.
