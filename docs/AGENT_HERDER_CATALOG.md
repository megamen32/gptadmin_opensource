# Agent Herder in the GPTAdmin catalog

The node-100 child `AgentHerder` connects to the existing Herder service at
`http://127.0.0.1:18787/mcp` using `streamable-http` and its authorized Bearer
header. Do not launch `agent-herder/dist/index.js` as another stdio instance:
that checkout can point at an obsolete release, and Herder owns a singleton.
The running systemd service's executable is the installed source of truth.

The Herder environment may contain a secret **reference**, not the token itself.
Resolve `AGENT_HERDER_HTTP_TOKEN` through the installed Herder `SettingsStore`
(`readSecretValue`), never pass the marker as a Bearer token. Persist updates
with the owning Node's `mcp_manage upsert`; this updates the live supervisor and
invalidates its old connection. Keep credentials only in protected configuration,
never in this document, command-line arguments, logs or tool output.

Verification uses ordinary GPTAdmin MCP operations:

1. Search `fleet_node_info` with target
   `mcp:shell:admin-node-100:AgentHerder`.
2. Call that tool with `{}` and verify `hostId=admin-node-100`.
3. Repeat search without an explicit target to check catalog discoverability.

## Last successful catalog

A transient schema read failure keeps the previous successful tools for the
same principal and connection definition. Each read rechecks current access.
Search reports `coverage.stale`, `failure_reasons`, and
`last_successful_refresh` for stale child catalogs. `loaded_at` is the last
successful read; `attempted_at` controls the shorter failure retry interval.
The disk snapshot retains the failure and both timestamps across restart.

A failed first read has no fallback and cannot claim complete coverage.
Configuration/generation changes, removal and access revocation invalidate the
corresponding old schema. Cached metadata never substitutes for tool execution.
