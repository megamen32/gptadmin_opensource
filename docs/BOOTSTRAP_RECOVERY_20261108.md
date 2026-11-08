# Bootstrap recovery, 8 November 2026

Production creation returned 503 before minting access because the configured
LHC 1.17.1 directory no longer contained AGENTS.md. The active native package
is 1.17.2. The service's last EnvironmentFile, /etc/gptadmin/nodes/primary.env,
overrides /etc/gptadmin/gptadmin.env: check the running process value after
restart, not only the manager's merged configuration.

Owner deferred the permanent design: selectable MCP Resources as startup
context in access profiles, optionally refined when issuing a link. LHC should
be an optional context source, not a mandatory hard-coded dependency. Context
selection must not expand access permissions. Do not infer the active package
by sorting cache versions. Existing secret links must not be replayed or logged.

Immediate restoration pins the explicitly active 1.17.2 package. This is a
configuration repair, not protection against later cache removal. UI regression
coverage preserves unknown-outcome/no-replay behavior, exposes the server reason
and removes the oversized alert. UI source fix is saved but not yet deployed;
owner prioritized receiving the working link over the permanent implementation.
