# GrepMesh MCP architecture reconnaissance

Role: Adviser
Status: todo
Parent task: /home/admin/gptadmin/.agents/tasks/work-20260810-grepmesh-mcp.md

## Original request

Assess the approved GrepMesh MCP design: one identical MCP server on each of
five hosts, local indexing, direct MCP-to-MCP fan-out for `hosts="*"`, four
tools (`search_text`, `find_paths`, `read_text`, `search_status`), and GPTAdmin
as installer/discovery/health control plane only.

## Bounded objective

Produce an independent architecture recommendation for the implementation
plans. Identify the minimum viable local vertical slice, protocol invariants,
and the highest-risk assumptions that must be proven before implementation.

## Allowed scope and paths

- Read repository documentation and MCP-related source as needed.
- Read the user-provided architecture in the parent task only.
- Do not edit source, docs, ROADMAP, production state, or task files other
  than appending the final report to this file.
- Do not design or modify secrets, ACLs, mTLS, firewall, permissions, or
  deployment actions.

## Acceptance proof for this lane

Report concrete file/line evidence where possible; separate confirmed facts,
inferences, and open decisions. Compare the three implementation plan shapes
that can deliver the same business canary, with active-minute estimates and
join/dependency notes. Stop after the bounded report.

## Expected report

Append a detailed evidence report and finish with a short TL;DR for L:
recommendation, blockers, and exact next decision.

## Adviser report — 2026-08-10

### Bounded recommendation

Use a protocol/federation-first vertical slice, with `rg` as the first local
backend and the persistent index behind the same internal interface. Run two
instances of the identical GrepMesh binary on loopback using distinct ports and
a static topology fixture. The canary must enter node A through MCP, fan out
directly to node B through MCP, merge deterministic results, report one failed
peer as `partial=true`, and perform a remote `read_text`. Only after this slice
is green should the implementation add the watcher/reconciliation index and a
GPTAdmin-backed cached topology provider.

This ordering isolates the novel distributed contract from indexing mechanics.
It also avoids treating the existing GPTAdmin executable relay as if it already
were the approved control-plane-only discovery seam.

### Confirmed repository facts

1. GPTAdmin's canonical integration contract is `discover -> schema -> execute`,
   and its selected target is carried into execution
   (`docs/INTEGRATION_CONTROL_CONTRACT.md:10-22,24-41`). Schema responses have a
   version/digest freshness guard, while idempotency is only bounded in-memory
   retry safety (`docs/INTEGRATION_CONTROL_CONTRACT.md:43-63`).
2. Hub heartbeat registration is currently ShellMCP-specific: `POST /heartbeat`
   creates `shell:<name>` and records a `virtual_shell`
   (`go-hub/internal/hub/server.go:2172-2222`).
3. Enabled MCP descriptors advertised in ShellMCP heartbeat metadata are
   projected as executable `child_mcp` targets with `tools/list` and
   `tools/call` capabilities (`go-hub/internal/hub/server.go:2778-2816,
   2819-2858`). Calling such a target is routed through the parent ShellMCP's
   `mcp_call` (`go-hub/internal/hub/server.go:3116-3124`).
4. Standalone `/mcp-relay/register` is also a data-plane registration: a
   registered non-shell target falls through to the Hub relay queue on execute
   (`go-hub/internal/hub/server.go:2409-2465,3126-3137`). Therefore neither
   current registration path is, as-is, a pure GrepMesh installer/discovery/
   health record that guarantees search traffic bypasses GPTAdmin.
5. A reusable direct MCP client already exists in ShellMCP and supports
   `streamable-http`, including initialize/initialized negotiation, session ID
   retention, and one recovery attempt after a stale session
   (`go-shellmcp/internal/mcpclient/remote.go:56-102,105-159`). This is evidence
   for protocol behavior and tests, not a recommendation to couple GrepMesh to
   ShellMCP.
6. No GrepMesh implementation exists in the repository according to the parent
   reconnaissance. The repository does already publish child MCP health and
   transport descriptors through ShellMCP capabilities
   (`go-shellmcp/internal/server/server.go:524-537`), which can inform but does
   not satisfy the required control-only topology contract.

### Minimum viable local vertical slice

The smallest useful slice is two GrepMesh processes, not one. A one-node slice
can prove MCP and local search but cannot prove the defining business behavior:
direct MCP-to-MCP fan-out, loop prevention, timeout/partial aggregation, remote
path identity, and remote `read_text`.

Required slice:

- one binary and one config schema; node A and node B differ only by `host_id`,
  roots, listen addresses, and topology fixture;
- Streamable HTTP MCP on separate test ports, with the four approved tools and
  identical schemas on both nodes;
- a local backend interface implemented first by bounded `rg` execution;
- `hosts="local"`, `hosts="*"`, and explicit host-list normalization;
- node A changes an inbound external request into per-peer local-only calls;
  peers must never recursively expand `hosts="*"`;
- deterministic merge/order and bounded result/context/output limits;
- a hard per-peer deadline plus overall deadline; one dead peer yields usable
  results, a peer error record, and `partial=true`;
- `read_text` routes by canonical `host_id`, returns an explicit file identity
  or freshness marker, and never silently substitutes another host/file;
- topology fixture remains readable after its provider is disabled, proving the
  cached-topology behavior without mutating GPTAdmin.

Acceptance should be black-box MCP traffic: initialize, `tools/list`, then
`tools/call` through node A. Direct package calls are supporting tests only.

### Protocol invariants

1. **Stable host identity:** every result and error carries canonical `host_id`;
   display names and URLs are not identities.
2. **Absolute path semantics:** every hit carries the absolute path as resolved
   on its owning host. The aggregator does not reinterpret remote paths.
3. **Single expansion:** only the ingress coordinator expands `hosts="*"`.
   Forwarded calls carry the same `request_id`, incremented `hop_count`, and a
   normalized local-only target. Reject or localize any forwarded call above
   the allowed hop count.
4. **Deduplication scope:** deduplicate by `(request_id, destination_host_id)`,
   not by query text. Repeated independent searches may legitimately have the
   same query.
5. **Partial is first-class:** `partial=true` whenever any selected host lacks a
   terminal successful response; return per-host status/error without replacing
   successful results with a top-level failure.
6. **Deterministic bounded output:** define stable sorting and explicit limits
   for hosts, matches, context bytes, line length, file bytes, and response
   bytes. Truncation must be signaled.
7. **Deadline propagation:** peer timeout cannot exceed the remaining ingress
   deadline. Cancellation must stop local `rg`, index reads, and HTTP bodies.
8. **Backend equivalence:** indexed and `rg` fallback paths obey the same
   matching, line-number, encoding, exclusion, ordering, and truncation
   contract; `search_status` states which backend and index generation served a
   response.
9. **Read consistency:** `read_text` uses `(host_id, absolute_path, range)` and
   should accept an optional freshness token from search results; changed or
   vanished files produce an explicit state rather than stale/mixed content.
10. **Topology cache semantics:** topology records need a generation, fetched
    time, expiry/staleness state, and last refresh error. Cached topology may be
    used while GPTAdmin is unavailable, but `search_status` must reveal that it
    is stale.

### Highest-risk assumptions to prove before broad implementation

1. **Reachability is unresolved and blocks real federation.** The business
   canary says local clients use `127.0.0.1:9419/mcp`, but a peer cannot reach
   another host's loopback address. A routable peer MCP endpoint (possibly a
   separate bind/address for the same process) must exist in topology. Binding,
   authentication, ACL, mTLS, firewall, and secrets are expressly outside this
   lane, so no safe production transport can be inferred here.
2. **GPTAdmin has no confirmed pure control-plane GrepMesh registry.** Current
   child and standalone MCP discovery paths both imply Hub-mediated execution.
   Reusing them without a new non-routable/control-only descriptor would blur
   the approved data-plane boundary.
3. **Topology ownership and cache contract are unspecified.** The producer,
   read endpoint/tool, canonical five host IDs, peer URLs, generation, TTL, and
   removal behavior must be named before cached topology can be implemented or
   tested.
4. **`request_id` and hop metadata transport is unspecified.** They must be
   explicit tool arguments or a documented MCP metadata field that survives all
   supported clients. Assuming opaque HTTP headers survive MCP stacks is unsafe.
5. **Index/fallback equivalence is expensive.** Watcher loss, rename storms,
   symlinks, binary/invalid UTF-8 files, ignore rules, and files changing between
   search and read can produce observably different answers. Start with one
   contract and differential tests before optimizing.
6. **Resource bounds are not yet selected.** Five-host fan-out can multiply
   matches, context, open files, subprocesses, and latency. Without explicit
   ceilings, a read-only tool can still exhaust a node or exceed MCP/client
   response limits.

### Three complete implementation-plan shapes

All estimates are active minutes for implementation plus automated local proof;
production installation, network/security decisions, and five-host deployment
remain excluded.

| Shape | Sequence and joins | Optimistic / likely / pessimistic | Main trade-off |
| --- | --- | ---: | --- |
| A. Federation/protocol first | Contract + two-node harness -> rg local backend and four MCP tools can proceed in parallel -> join at black-box fan-out/partial/read canary -> index + differential tests -> topology-provider seam | 90 / 150 / 260 | Earliest proof of the highest-risk distributed behavior; some rg code may be replaced internally, but public semantics stabilize first. |
| B. Local index first | Define contract -> watcher/reconciliation/index + local tools -> join at one-node MCP canary -> add peer client/fan-out -> add topology cache and five-node simulation | 110 / 190 / 320 | Earlier indexing depth, but federation, cancellation, and partial semantics arrive late and may force backend/API rework. |
| C. GPTAdmin seam first | Define control-only registration/topology contract -> implement Hub registration/readback and cache client -> join with one-node GrepMesh health -> local search/index -> federation harness and partial/read canary | 130 / 230 / 380 | Earliest control-plane visibility, but currently depends on the largest open decision and risks accidentally creating a Hub data plane. |

For A, the rg backend lane and MCP/federation lane can run independently after
the shared schemas are frozen; they join at the two-node canary. Index work then
depends on that backend contract. The GPTAdmin seam depends on a separate
topology decision but need not block the two-node fixture.

For B, watcher/index and MCP framing can be parallel, but federation cannot be
accepted until their local result model joins. This creates a late integration
point around deadlines and result bounds.

For C, almost all useful GrepMesh work is downstream of a currently absent
control-only Hub contract. A temporary fixture could unblock it, but doing so
effectively turns C into A.

### Inferences and open decisions

**Inference:** a single identical binary can expose both the client loopback
endpoint and a routable peer endpoint, but that requires an explicit listen/
advertise model; the approved text does not confirm one.

**Inference:** the existing ShellMCP streamable-HTTP client is a good source of
MCP conformance cases, but importing ShellMCP as a GrepMesh runtime dependency
would conflict with the stated separation from existing ShellMCP/tgrep daemons.

**Open decision (exact next decision):** define and approve the GrepMesh
topology record and endpoint model before source implementation: canonical
`host_id`; local client URL; routable peer MCP URL; topology generation and TTL;
cached-stale behavior; and whether GPTAdmin exposes these records through a new
control-only API/tool that cannot execute GrepMesh search. Security values and
network policy can remain deferred, but the address/identity/data-plane shape
cannot.

### TL;DR for L

Recommend Shape A: two-process, black-box MCP federation first, using bounded
`rg`, then add the index and finally the GPTAdmin cached-topology provider.
Blockers are the impossible cross-host use of loopback URLs and the absence of a
confirmed GPTAdmin control-only registry; current discovery routes are
executable relays. The exact next decision is the topology/endpoint contract
(`host_id`, local URL, routable peer URL, generation/TTL/stale semantics, and a
non-executable GPTAdmin readback seam).
