# Open-core and sustainable offering boundary

GPTAdmin's self-hosted core is the product contract. It must remain useful,
auditable and operable without a hosted account, a proprietary control plane or
a forced subscription. This document defines the boundary; it is not a claim
that a hosted service or commercial support package is currently available.

## Self-hosted core

The AGPL-licensed self-hosted core includes the Hub, ShellMCP, ProxyRelay,
CLI/installer, MCP routing and policy surfaces, profiles, file-sharing and
backup contracts, release metadata, and the tests and documentation needed to
operate them. A user must be able to install, configure, update, back up,
restore and remove the core using repository-owned artifacts and documented
interfaces.

## Optional hosted or support convenience

Future hosted or support offerings may provide operational convenience such as
managed availability, upgrades, external observability retention, identity
integration, migration assistance or response coordination. They must not
replace the self-hosted API contract or make the Hub, MCP clients, Tunnel,
profiles or policy controls unusable without the service.

Any hosted feature needs a separate versioned contract covering data location,
retention, access control, export and deletion. Support response targets are a
separate commercial decision and must not be implied by the open-source core.

## Data portability and no forced lock-in

- Configuration, profiles, audit references, backups and release manifests use
  documented or inspectable formats.
- A hosted user can export the data needed to operate the self-hosted core.
- Hosted availability is not required for local health checks, policy decisions,
  MCP execution, file restore or rollback.
- Hosted value is operational convenience, not a hidden dependency or forced
  lock-in.
- Every hosted change is reviewed against the invariant **no self-hosted core
  regression** and must preserve the security and least-privilege boundary.
The release gate treats **no self-hosted core regression** as one continuous
acceptance invariant.

## Evidence and decision process

The feedback loop in [`FEEDBACK_LOOP.md`](./FEEDBACK_LOOP.md) records observed
activation, retention, support and incident evidence. If no evidence exists,
the status is `no_data_yet`; roadmap language must not present a hypothetical
hosted feature as a shipped product. Any future offering decision should cite
an immutable design, security and portability review artifact.
