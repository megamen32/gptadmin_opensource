# GPTAdmin execution plan

> Internal execution plan. The public product roadmap is
> [`ROADMAP.md`](./ROADMAP.md). Agent coordination rules are in
> [`WORKLOG.md`](./WORKLOG.md).

## North star

GPTAdmin is the self-hosted control plane that lets a person or team connect
AI clients to real infrastructure safely: install once, authorize explicitly,
execute observable actions, and recover from failures without losing control.

Primary outcome: a new operator can go from a clean machine to one working,
audited MCP action through their chosen AI client in under 15 minutes, without
reading topology or authentication documentation.

Product decisions are governed by [`PHILOSOPHY.md`](./PHILOSOPHY.md): easy to
install, flexible to configure, convenience and resilience by default, and the
minimum practical MCP context loaded only when the current task needs it.

## Operating rules

- A stage is complete only when every listed exit gate has current evidence.
- A milestone has one owner and a short acceptance test. A feature branch or
  implementation claim is not evidence by itself.
- Prefer a thin vertical slice through installer, hub, agent, client and docs
  over isolated subsystem work.
- Security boundaries fail closed, while the normal product path prioritizes a
  working, recoverable default and offers stronger restrictions progressively.
- Preserve backward compatibility only for a documented migration window.

## Current baseline

| Area | Current evidence | Status |
| --- | --- | --- |
| Go hub and Go ShellMCP | Unit, HTTP/MCP contract and cross-platform CI coverage | In progress |
| Installation and updates | Linux, macOS, Windows and Android packaging checks | In progress |
| Failover | Docker black-box coverage for tunnel, hub, combined outage, reclaim and ranked fallback promotion | In progress |
| Auth and relay target safety | OAuth/OpenAPI paths and explicit MCP target contract | In progress |
| Product activation | No canonical end-to-end golden path or activation measurement | Not started |
| Policy, observability and ecosystem | Partial primitives; no coherent operator product | Not started |

## Stage 0 - Product contract and engineering baseline

**Objective:** make the supported promise, compatibility boundaries and release
evidence unambiguous before adding surface area.

| Milestone | Deliverable | Exit gate | Status |
| --- | --- | --- | --- |
| S0.1 Supported golden paths | One matrix for Linux/macOS/Windows/Android and Codex/ChatGPT/Claude-style clients | Each path has install, auth, first tool call and uninstall/rollback commands | Planned |
| S0.2 Contract suite | Language-neutral Hub and ShellMCP contract suite with implementation matrix | Go is required green; alternative implementation can be run by env-configured command | In progress |
| S0.3 Release provenance | Version, commit, checksum and platform architecture are observable from the artifact | CI verifies every release artifact, manifest and installer link before publish | In progress |
| S0.4 Engineering governance | This plan, worklog and agent handoff discipline | New work records scope, evidence, CI link and next owner action | Active |
| S0.5 One-password product contract | Implementable design and migration from visible token sprawl to AdminPassword + scoped JWT connections | Security model, migration phases and black-box acceptance suite are approved before runtime migration | Active |

## Stage 1 - Time to first safe value

**Objective:** reduce installation-to-first-audited-action time to less than
15 minutes on the primary paths.

| Milestone | Deliverable | Exit gate | Status |
| --- | --- | --- | --- |
| S1.1 One-command working Hub | Idempotent install/update that starts Hub, creates/verifies Tunnel and outputs one Hub URL | Fresh supported host completes with minimal questions and no manual topology/token configuration | Planned |
| S1.2 `gptadmin doctor` | Structured local and remote readiness diagnosis | Detects version, service health, Tunnel reachability, auth, clock and permissions; has machine-readable JSON | Planned |
| S1.3 Universal connection page | One Hub URL that detects/configures local MCP clients and derives protocol details | Codex, Claude-compatible and ChatGPT-compatible paths complete a harmless action without documentation lookup | Planned |
| S1.4 Safe demo capability | Built-in read-only demo/diagnostics tool and explicit destructive-action boundary | A user can validate connection without shell execution or credentials beyond setup | Planned |
| S1.5 Activation telemetry | Opt-in, privacy-preserving local event summary | Operator can see funnel failures without sending command contents or secrets | Planned |
| S1.6 Progressive security presets | Working default, Private access and Locked down presets applied after first connection | Working default has HTTPS/rate limits/no direct public port; Locked down requires MFA and external verification | Planned |

The vocabulary of every Stage 1 surface is **Hub**, **MCP clients** and
**Tunnel**. Protocol/transport names are advanced diagnostics, not setup
requirements. Setup must automate unknowns, output one Hub URL and defer
security hardening to progressive presets. See
[`AUTH_SIMPLIFICATION.md`](./AUTH_SIMPLIFICATION.md).

## Stage 2 - Trustworthy agent access

**Objective:** make least privilege, approval and audit the default product
experience, not an expert-only configuration exercise.

| Milestone | Deliverable | Exit gate | Status |
| --- | --- | --- | --- |
| S2.1 One-password identity and connection hygiene | `AdminPassword` for humans; hidden internal keys and short-lived scoped JWT connections | Black-box tests reject wrong audience, expired token and token forwarding; normal user flows reveal no raw token | Planned |
| S2.1a Admin MFA | WebAuthn/passkeys first, TOTP fallback, recovery codes and optional OIDC proxy identity | Locked down admin sessions cannot be enabled without MFA; enrollment, recovery and sensitive-setting re-auth are black-box tested | Planned |
| S2.2 Capability policy | Per-agent, per-server and per-tool allow rules with explicit deny behavior | Policy decision is included in every audit event and covered by API/MCP tests | Planned |
| S2.3 Progressive autonomy | Approval modes: read-only, ask-before-write, bounded autonomous | Dangerous calls require the configured approval and cannot bypass it through aliases or relay targets | In progress |
| S2.4 Operator audit trail | Searchable immutable-enough action log with actor, target, policy, arguments digest and result reference | Incident drill can answer who did what, where, why and with what result | Planned |

## Stage 3 - Operable and recoverable control plane

**Objective:** let a small team operate GPTAdmin confidently through normal
upgrades and realistic failures.

| Milestone | Deliverable | Exit gate | Status |
| --- | --- | --- | --- |
| S3.1 Standard telemetry | OpenTelemetry traces, metrics and structured logs spanning client, hub, relay and ShellMCP | One trace correlates an AI request, policy decision, tool call, retries and durable result without secret payloads | Planned |
| S3.2 SLO and alerts | Operator-facing health model, error budget and actionable alerts | Documented SLOs; alert runbook includes owner, symptom, diagnosis and recovery | Planned |
| S3.3 Backup/restore drill | Versioned backup, restore verification and rollback procedure | Clean-host restore passes a scripted drill with integrity check and no root-owned user files | Planned |
| S3.4 HA maturity | Multi-fallback configuration, fencing, reclaim, upgrade and partition scenarios | Docker black-box suite covers rank 1/rank 2 selection; a deployment recipe verifies two physical fallback hosts | In progress |
| S3.5 Safe delivery | Signed/checksummed release artifacts, staged rollout and rollback | Canary update proves version, health, client reconnection and rollback before broad release | Planned |

## Stage 4 - Interoperable ecosystem

**Objective:** make GPTAdmin the easiest safe way to operate heterogeneous MCP
tools, rather than another isolated server.

| Milestone | Deliverable | Exit gate | Status |
| --- | --- | --- | --- |
| S4.1 Integration certification and control contract | Certify the existing `discover -> schema -> execute` flow and define adapter rules for session-oriented integrations | Every listed integration has automated smoke evidence and a known-version support policy; write retries have explicit idempotency semantics where the current call contract lacks them | Planned |
| S4.2 Curated capability catalog | Signed/attributed MCP definitions with scopes, network needs, risk level and maintenance owner | Install flow displays requested capabilities and provenance before activation | Planned |
| S4.3 Supply-chain controls | Artifact digest verification, SBOM and dependency/update policy | CI publishes provenance; installer rejects mismatched artifacts; vulnerability response policy is documented | Planned |
| S4.4 Developer extension path | Stable plugin/adapter SDK and reference implementation | A third party can add a capability without editing hub internals and passes the conformance suite | Planned |

## Stage 5 - Adoption and sustainable product delivery

**Objective:** turn verified technical value into repeatable adoption while
keeping the self-hosted core trustworthy.

| Milestone | Deliverable | Exit gate | Status |
| --- | --- | --- | --- |
| S5.1 Reference deployments | Small-team, home-lab and production deployment blueprints | Each blueprint has a tested architecture, cost/security tradeoffs and incident runbook | Planned |
| S5.2 Feedback loop | Public issue templates, design-partner program and quarterly outcome review | Roadmap changes cite observed activation, retention, support or incident evidence | Planned |
| S5.3 Documentation as product | Versioned docs, tested snippets, translation review and changelog mapping | Docs CI checks snippets/links; every supported path has a single canonical page | Planned |
| S5.4 Sustainable offering | Clear open-core boundary and optional hosted/support offer | No self-hosted core regression; hosted value is operational convenience, not forced lock-in | Planned |

## Stage 6 - 1.0 release gate

**Objective:** declare a stable contract only after the product has earned it.

All of the following are required:

- Stage 1 golden paths pass on supported platforms and clients.
- Stage 2 policy and audit controls are enabled by default for privileged work.
- Stage 3 restore and HA drills have current, reproducible evidence.
- Stage 4 compatibility and supply-chain evidence are published with releases.
- SemVer compatibility policy, deprecation policy, security response policy and
  operator migration guide are public.

## Metrics and review cadence

Review monthly; update a metric only from reproducible data or a documented
manual sample.

| Metric | Target direction | Initial target |
| --- | --- | --- |
| Time to first safe action | Down | <= 15 minutes on a fresh supported host |
| Golden-path completion rate | Up | >= 90% in recorded test/install samples |
| Privileged actions with policy + audit event | Up | 100% |
| Recovery drill success | Up | 100% of scheduled drills |
| Mean time to diagnose a failed action | Down | <= 10 minutes using shipped evidence |
| Client/server compatibility regressions | Down | Zero in supported matrix per release |
