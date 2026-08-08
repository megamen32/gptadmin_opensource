# Documentation map

This is the canonical entry-point map for the supported product paths. Start
with [Getting Started](./GETTING_STARTED.md); the other pages are contracts or
operational references, not competing quickstarts. Changes to a supported path
must update the relevant section here and the [Changelog](../CHANGELOG.md).

| Product path | Canonical page | What it covers |
| --- | --- | --- |
| First install and Hub URL | [Getting Started](./GETTING_STARTED.md) | Install, Tunnel, first connection and harmless action |
| MCP clients | [Integrations](./INTEGRATIONS.md) | Codex, Claude-compatible clients and MCP connection flow |
| Browser extension | [Extension SDK](./EXTENSION_SDK.md) and [Integrations](./INTEGRATIONS.md) | OAuth browser handoff and extension-facing MCP contract |
| ChatGPT Custom GPT | [Integrations](./INTEGRATIONS.md) | Canonical `/actions/openapi.yaml`, `/mcp-relay/*`, OAuth/Bearer and external certification boundary |
| Integration control contract | [Integration control](./INTEGRATION_CONTROL_CONTRACT.md) | Current discover/schema/execute mapping, policy and retry semantics |
| Admin profiles | [Admin profiles](./ADMIN_PROFILES.md) | Instructions, targets, tools, client bindings and private references |
| Network proxy and Tunnel | [Network proxy](./NETWORK_PROXY.md) | Grants, tickets, revoke and data-plane threat model |
| Observability | [Observability](./OBSERVABILITY.md) | Request correlation, bounded metrics, OTLP export and evidence boundary |
| Security policies | [Security](./SECURITY_DOCS.md) and [Auth simplification](./AUTH_SIMPLIFICATION.md) | AdminPassword, OAuth bearer, legacy CTL migration, MFA, policy and audit boundaries |
| File sharing and recovery | [File backups](./FILE_BACKUPS.md) and [Backup/restore](./BACKUP_RESTORE.md) | Managed roots, backup metadata, restore and cleanup |
| Reference deployments | [Deployment blueprints](./DEPLOYMENT_BLUEPRINTS.md) | Small-team, home-lab and production trade-offs/runbooks |
| Feedback and roadmap evidence | [Feedback loop](./FEEDBACK_LOOP.md) | Design-partner intake, support/incident signals and quarterly review |
| Open-core boundary | [Open-core plan](./OPEN_CORE_PLAN.md) | Self-hosted core, optional hosted/support convenience and portability guarantees |
| Release and updates | [Supply chain](./SUPPLY_CHAIN.md) and [Failover](./FAILOVER.md) | Manifest, SBOM, provenance, canary and rollback evidence |
| Safe delivery canary | [Canary acceptance](./CANARY_ACCEPTANCE.md) | Disposable real-Hub version swap, reconnect and bad-candidate rollback |
| Live deployment acceptance | [Live acceptance runner](./LIVE_ACCEPTANCE.md) | Secret-safe endpoint, OAuth, OpenAPI and MCP smoke before deeper host/client gates |

## Evidence boundary

The repository tests and Docker drills validate implementation contracts. They
do not silently certify a real macOS/Windows/Android host, public Tunnel,
ChatGPT client, external OIDC provider, OTLP collector, tagged GitHub run or
physical fallback host. Those results belong in an immutable acceptance
artifact and must be linked from the worklog before a milestone is marked
complete.
