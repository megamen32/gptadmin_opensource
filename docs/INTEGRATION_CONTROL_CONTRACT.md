# Integration Control Contract

This is the GPTAdmin design reference for integrations that control a
connected external client or session. GPTAdmin already has the same basic
shape in its Hub relay; this document names the existing mapping and the
additional guarantees needed by session-oriented adapters.

## Contract

Every session-oriented integration follows three explicit calls:

1. `discover` lists connected sessions and the surface-specific operations
   supported by each session.
2. `schema` returns the exact input schema and version for one selected
   operation.
3. `execute` runs one operation against the selected session with a
   caller-stable `idempotency_key`.

The caller must select the session from `discover`, use the operation and
version returned by that session, and construct arguments only after `schema`.
Retrying the same logical operation reuses the same idempotency key; a new
operation gets a new key.

## Current Hub implementation

| Control pattern | Existing Hub operation |
| --- | --- |
| `discover` | `discover` (legacy aliases: `list_mcp_agents`, `list_mcp_servers`) |
| `schema` | `schema` for the selected `target` (legacy alias: `list_mcp_tools`) |
| `execute` | `execute` with the same `target` and `tool` (legacy alias: `call_mcp_tool`) |

The selected `target` is the current stable agent/server identity. A separate
executor session and schema version are not currently part of the ordinary
Hub relay contract because MCP tool schemas are fetched directly from the
selected server.

The current Hub implementation exposes the stable `discover -> schema ->
execute` flow through the MCP and relay facades. The request-scoped policy
decision is applied before schema lookup and execution, and the same target
identity is carried through queued calls and results.
In short, the implemented contract is `discover -> schema -> execute`.

Every schema response now includes schema version/digest metadata: a
`schema_version` and a deterministic
`schema_digest_sha256` for the effective, policy-filtered tool list. An
`execute` call may carry both values; the Hub rejects an incomplete or stale
pair with `409 schema_mismatch` before invoking the selected tool. Transport
shortcut hints are added after digest calculation and therefore cannot make a
fresh schema appear stale.

## Remaining Gaps

`execute` accepts an optional caller-stable `idempotency_key`. The Hub
fingerprints `target`, `tool_name`, and `arguments` under the authenticated
caller scope. A retry with the same key and fingerprint reuses the original
job/result; reusing the key for a different operation returns `409 Conflict`.
The bounded record is in-memory for the current Hub process and expires after a
short TTL, so this is retry safety, not a claim of exactly-once execution after
a Hub restart. Background `job_id` remains the completion handle.

The schema identity is recomputed against the selected target for each
version-bound execute call. It is a freshness guard, not a claim of durable
exactly-once execution or a replacement for the existing idempotency key.

## GPTAdmin Scope

This contract documents the existing Hub baseline for session-oriented
adapters. It does not change the stable MCP `tools/list` surface and it does
not replace ordinary Hub, MCP client or Tunnel calls.

The Universal connection page and third-party extension path reuse the Hub flow
above. A future session adapter may add session-specific schema lookup only
where a client actually requires it; it must preserve the target policy,
idempotency and audit guarantees already covered by this contract.

Codex Document Control is the external reference that motivated this contract.
GPTAdmin adopts the interaction shape, not Codex-specific names or behavior.

## Verification

The current Hub implementation and policy path are covered by:

```bash
cd go-hub && go test ./internal/hub -run 'TestMCPIntegrationDiscoverSchemaExecuteConformance' -count=1
```
