# Stable executor routing and compact targets

The persisted ShellMCP identity owns the queue address. Its `server_id`, key,
and original `name` survive changes to the OS hostname or configured display
name. Every poll, result callback and executor ACL check uses that same persisted
address. The current label is separately advertised as `hostname`; it is not an
identity selector. Existing registry addresses, approvals, jobs, ACL policies
and history are not migrated or rewritten. Existing duplicate registrations
need the separate scoped cleanup operation.

The public AI facade publishes deterministic aliases: `h` plus 12 hexadecimal
SHA-256 digits derived from persisted `server_id` and key `fingerprint`; child
`c` targets use the parent's same identity tuple plus the child's exact `ref`.
A versioned JSON tuple frames these fields and separates `h`, `c`, `f` (files),
and `k` (control) domains. Neither hostname nor canonical route name enters the
hash. `hub` and `control:hub` stay unchanged. Missing identity metadata keeps the
legacy canonical address; it never falls back to a hash of the hostname.
These aliases are derived views, not a second identity store.
Old canonical addresses remain valid. Schema, search, direct calls and code-mode
calls resolve aliases before ACL checks, approvals, routing or reference storage.
Unknown aliases fail normal target validation; ambiguous aliases fail closed.

## Profile authorization across route changes

The compact alias is a routing hint; authorization bindings use the full
domain-prefixed SHA-256 digest of the same versioned tuple. On profile PUT, Hub
pins eligible `allowed_targets`, tool-rule targets and workspace shell targets
to the identity of an unambiguously resolved, approved executor. Retained
target labels keep their existing bindings. For each request, bound labels are
resolved on a profile copy to the unique currently approved canonical target;
the persisted profile policy and client bindings are not renamed. Missing or
ambiguous identity matches are removed from the request's resolved
`allowed_targets`, and a different key cannot inherit the binding. An unbound legacy label continues to use its existing
name-based behavior; it must not be mistaken for an identity-pinned grant.

Existing profiles gain eligible bindings when resaved through the normal
authenticated GET and PUT with the returned `ETag` in `If-Match`, retaining all
writable policy fields. Do this while the original target still resolves to the
approved identity, then read the profile back. If a legacy name has vanished,
the Hub cannot infer the intended identity: do not guess it or directly write
registry or auth-continuity state. The detailed upgrade and conflict handling
are in [Admin Profiles](../ADMIN_PROFILES.md#stable-target-bindings-and-existing-profiles).

Updating the Hub alone enables compact targets. Preventing hostname-induced
queue divergence requires updating the executor too, using its existing identity
directory. Deleting/recreating that directory is identity replacement, not rename;
a different key cannot inherit a pinned local executor registration.

Validation: `TestStableTargetNodeRenameAndForeignKey` starts the actual embedded
Node over HTTP, executes a shell command, restarts with a different configured
name using the same identity and registry, reads the previous job and executes
again via the same compact target. It also rejects a new key using the old name.
This is isolated integration evidence, not proof of a fleet deployment.
