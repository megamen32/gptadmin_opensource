# Optional MCP schema resources

GPTAdmin exposes tool definitions as optional JSON resources. The compact
execution facade remains the execution surface. Reading a schema documents the
tools currently visible to the caller; it does not register tools, grant access,
or require the client to attach the document to a model prompt.

## Resources

| Resource | Content |
| --- | --- |
| `gptadmin://schemas/active` | Compact index of authorized online servers, stable schema URIs, freshness, size and availability metadata. Does not fetch child schemas. |
| `gptadmin://schemas/server/<full-enrolled-identity>` | Full authorized tool definitions for one server, fetched when read. |
| `gptadmin://schemas/active/all` | Explicit snapshot of authorized online server schemas, within the limits below. |

The catalog uses the Russian titles **«Схемы активных MCP: индекс»**,
**«Схемы MCP: …»**, and **«Схемы активных MCP: все серверы»**.
`resources/list` returns metadata only. Listing and reading the index do not
fan out to child servers. Per-server reads and the explicitly selected snapshot
use the full schema route, not truncated search results.

The document format is `gptadmin.active-mcp-schemas/v1`. Tool objects preserve
their input/output schemas, descriptions, annotations, titles and other supplied
fields. Definition text is untrusted documentation.

## Identity and authorization

Enrolled server and child URIs derive from the complete stable identity, not the
mutable hostname or a short alias. Local Hub and virtual facades have fixed
identities. A missing or ambiguous enrolled identity is reported as unavailable;
no unstable replacement URI is invented.

The caller's target/tool profile and personal connection ownership apply to
listing, reading and cache reuse. The cache key includes the authenticated
principal, current profile and managed connection scope. A profile or connection
change is checked again before a slow read returns. Knowing a previous URI does
not preserve access after revocation.

The existing `gptadmin://servers` and `gptadmin://agents` inventory resources
also obey current target and connection visibility. A direct server resource
route enforces that server's target permission.

## Completeness and freshness

Each server records its status, completeness and any failure reason. Loaded
definitions include a SHA-256 version, byte size, tool count, retrieval time and
freshness. Cache hits do not relax authorization.

A successful child enumeration follows every `tools/list` cursor before it
can declare `schema_complete=true`. Empty intermediate pages are allowed;
repeated cursors, malformed pages, failed requests or exhausted budgets fail the
enumeration instead of returning a successful first page. Opaque cursors are
preserved. Completeness markers survive direct and legacy child transports.

An older remote Node without a completeness marker is explicitly
`partial / upstream_completeness_unverified`. Full local catalogs are
identified separately. Failed, stale, oversized and unavailable schemas are
visible as such. Registry changes during a snapshot can also make it partial.

Definitions are never silently cut to make a full result fit. When a snapshot
cannot include a server's definitions, it retains a reason and the individual
URI. `complete=true` is a claim about all currently authorized definitions
within the represented scope, not about inaccessible tools.

## Resource budgets

| Budget | Limit |
| --- | ---: |
| Servers in an index or snapshot | 128 |
| Server fetches per snapshot | 64 |
| Concurrent fetches | 4 |
| Total read deadline | 8 seconds; a shorter caller deadline wins |
| Tools per server | 4,096 |
| Serialized definitions per server | 512 KiB |
| Serialized resource content | 2 MiB |
| Cache entries / stored definitions | 128 / 8 MiB |
| Fresh cache lifetime | 60 seconds |
| Stale retention since the last successful read | 10 minutes |
| Failed refresh retry interval | 5 seconds |

The ShellMCP child enumerator independently limits enumeration to 64 pages,
4,096 tools, 8 MiB of serialized decoded result payloads and 30 seconds.
The byte budget is a result-processing bound, not a pre-decode network memory
limit. Resource reads use the shorter eight-second deadline above.

Responses use private caching metadata with `ttlMs: 0`; the internal schema
cache reports its own freshness. No resource subscription or list-change
notification capability is advertised by this feature.

## Verification boundary

Source regressions cover metadata-only discovery, complete definitions beyond
search limits, both direct and legacy paths, scoped caches, current-profile
revocation, owner isolation, stable identity, deadlines and bounded partial
results. Sanitized RED/GREEN results are in
[`evidence/mcp-resources-20261108`](../evidence/mcp-resources-20261108).

Release acceptance separately checks the clean package, an installed Node and a
native MCP client's catalog and resource reads. The ChatGPT phone resource
picker and attachment of a selected resource to a conversation require an
observed client check; node-side publication alone is not that evidence.
The active fullcycle plan records delivery and client acceptance separately.

Child resource/template federation, per-profile source selection and the wider
modern-MCP capability matrix remain subsequent roadmap work.

## Protocol references

- [MCP resources, 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/server/resources)
- [MCP tools, 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
