# Managed HTTP client migration

Currently enabled MCP registrations are migrated individually, preserving the
consumer's name, tools, credential authority, project/workspace and host. An
existing supported HTTP upstream is reused. Disabled registrations remain
disabled. Global configuration is not proof that already-loaded native clients
have reconnected; verify a fresh owned native consumer separately.

An operator keeps an owner-only backup and uses a configuration CAS before
changing one client entry. A failed or unknown external operation is inspected
through its existing receipt rather than submitted again. Personal credentials
are never copied between Codex and ZCode. The Hub access token is distinct from
the downstream service credential stored in its protected owner registry.

Managed `/connection/<slug>/mcp` endpoints use the legacy MCP initialize and
session-ID lifecycle. A pre-initialize `server/discover` probe receives an
authenticated, owner-scoped JSON-RPC `-32601` response with its exact request
ID, so modern clients can fall back to that lifecycle. The probe allocates no
session or upstream job and is answered locally before peer forwarding. It
does not advertise a modern stateless handshake. An explicit native client
`protocolVersion: "legacy"` is also supported; it skips negotiation without
changing authentication, tools, or session isolation.

## Personal upstreams

`owner_profile_id` optionally binds one child definition to an exact verified
access profile. Broad target permission or an unbound client cannot substitute
for that owner. Owner-bound definitions require their `/connection/<slug>/mcp`
HTTP initialize/session/DELETE lifecycle. Parent `mcp_call`, generic schema,
legacy child endpoints and legacy OpenAPI do not allocate an unscoped personal
connection. Administrative definition maintenance uses actual stored authority;
it does not grant the administrator another profile's business tool session.

The empty field preserves existing operator/shared definitions. Updating a
definition changes its generation/fingerprint; old sessions cannot silently
revive under new credentials. Configuration/tool names are not an authorization
boundary. Real foreign-credential and owner-profile denials are required before
accepting a personal consumer rollout.

## AgentMemory REST adapter

An enabled HTTP definition can set `protocol_adapter: agentmemory-rest-v1` and
point `url` at the authenticated AgentMemory HTTPS base. `env.AGENT_ID` retains
the corresponding original client attribution; `project` remains the explicit
tool argument. The adapter does not invent a fixed project from the Hub's
working directory. The existing Go singleton handles MCP while calling the
native REST backend, without a stdio subprocess or local KV fallback.

The adapter follows the installed AgentMemory0.9.28 force-proxy contract:
native tool names and schemas, bounded sessions, bounded synthesized export,
and an honest degraded audit response. Generic backend `isError` is preserved.
Project and agent attribution must be verified through the real consumer.
Existing plugin hooks stay installed; no unrelated hook/config rewrite is part
of this transport migration.

Unit mocks prove only mapping, bounded bookkeeping and rejection behavior.
Acceptance requires a real backend call through the installed candidate and
real Codex/ZCode tool receipts, plus confirmed scoped teardown. SharedSTDIO is
disabled; stateful workspace/browser/PTY tools require a supported HTTP adapter
that retains their actual owner and locality before their client entries move.
