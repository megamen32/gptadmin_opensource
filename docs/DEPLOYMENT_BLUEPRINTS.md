# Reference deployment blueprints

This document is the canonical starting point for choosing a GPTAdmin layout.
It describes repository-testable architectures and their trade-offs. A passing
local or Docker verification command is not evidence that a host is deployed,
that a public Tunnel is reachable, or that a physical failover has succeeded.

Every blueprint uses one Hub URL for MCP clients. The Tunnel is the public
ingress boundary; the Hub should not be exposed directly to the public network.
Keep AdminPassword and any deployment credentials in the operator's secret
store, and use progressive security presets after the first connection.

## small-team

### Topology

Run one Hub and one ShellMCP on a dedicated Linux host or VM. Put the host
behind the operator's existing HTTPS Tunnel and keep the Hub bound to its
private interface. Store versioned backups on a separate encrypted volume.
MCP clients use the Hub connection page; capabilities are enabled through the
profile and policy surfaces rather than by sharing service credentials.

### Security trade-offs

This is the smallest operational surface, so loss of the host is a service
outage until restore. Use the Working default preset for a private network,
then Private access when the Tunnel is enabled. Prefer read-only or
ask-before-write approval for shared profiles, and keep the backup volume
outside the Hub service account's write path.

### Cost trade-offs

One small VM or home server has the lowest cost and maintenance burden. A
second encrypted backup target adds modest storage cost; a second live Hub,
external identity provider and collector are intentionally out of scope for
this profile.

### Incident runbook

1. Run `gptadmin doctor --json` locally and record the immutable output path.
2. Check the Hub, ShellMCP and Tunnel service state without changing secrets.
3. If the host is healthy, use `docs/NETWORK_PROXY.md` and the Tunnel runbook
   to restore ingress; if the host is lost, follow `docs/BACKUP_RESTORE.md`.
4. Validate policy, MCP forwarding and the first harmless tool call before
   re-enabling write-capable profile tools.

### Verification command

```bash
python3 -m pytest tests/test_completion_matrix.py tests/test_deployment_blueprints.py -q
```

This verifies the local product contracts; it does not prove physical failover
or a public Tunnel.

## home-lab

### Topology

Run the Hub on a home-lab VM or container host, with ShellMCP on the same
trusted LAN or on a separately restricted worker. The Tunnel terminates public
access and forwards only the Hub route. Keep file sharing roots and backups on
dedicated directories with explicit ownership and bounded retention.

### Security trade-offs

LAN convenience increases blast radius if the home network is compromised.
Use Private access, narrow profile target/tool allowlists, and Locked down only
after MFA and external identity checks are genuinely available. Do not turn a
router port-forward into a substitute for the Tunnel or policy layer.

### Cost trade-offs

Existing hardware and a local collector keep recurring cost low. The trade-off
is operator time for patching, power and backup recovery. A disposable Docker
failover drill is useful evidence, but it is not a second physical host.

### Incident runbook

1. Run the failover drill and inspect `docs/FAILOVER.md` for the expected rank,
   fencing and reclaim sequence.
2. If file access is involved, stop the affected profile and verify the managed
   root before restoring anything.
3. Restore the Hub first, then ShellMCP and the Tunnel; re-check `/healthz`,
   `/version`, OAuth discovery and the MCP harmless action.
4. Review the audit trail and the SLO alert guidance in `docs/SLO_ALERTS.md`.

### Verification command

```bash
bash tests/e2e/failover/run.sh
```

The Docker drill verifies failover orchestration and does not prove physical
failover, router behavior or external client connectivity.

## production

### Topology

Use a primary Hub with at least two independently operated fallback hosts,
fencing and signed reclaim. Keep ShellMCP workers and file roots segmented from
the public ingress. Export traces and metrics to a real OTLP collector with a
documented retention and access policy. Release artifacts must pass manifest,
SBOM, vulnerability and provenance gates before a canary.

### Security trade-offs

The extra hosts, identity integration, audit retention and staged rollout
reduce outage and compromise impact but increase configuration and review cost.
Locked down requires MFA and external verification; least-privilege profiles
must be reviewed together with network proxy grants and adapter capabilities.

### Cost trade-offs

This profile needs multiple hosts or zones, encrypted backup storage, an OTLP
collector and an operator rotation. It is appropriate when recovery time,
auditability and controlled upgrades are more valuable than minimum monthly
cost. A hosted service is optional; the self-hosted core remains the source of
truth.

### Incident runbook

1. Freeze release promotion and preserve manifest, SBOM, attestation and audit
   references; do not use a diagnostic bypass as a repair mechanism.
2. Follow `docs/FAILOVER.md` for fencing, ranked fallback promotion and signed
   reclaim. Record which physical host served traffic.
3. Use `docs/BACKUP_RESTORE.md` for restore and `docs/SLO_ALERTS.md` for owner,
   symptom, diagnosis and recovery evidence.
4. Run a signed canary with client reconnect and rollback before broad release.
5. Reconcile policy/audit events, file-sharing access and proxy grants before
   returning profiles to normal autonomy.

### Verification command

```bash
python3 -m pytest tests/test_release_workflow_contract.py tests/test_supply_chain_policy.py -q
```

This verifies the checked-in delivery contract. It does not prove a tagged
GitHub run, a retained OTLP trace, a public MCP client, or two physical hosts.
