# Session-scoped native Python MCP over HTTP

`tools/python_mcp_session_http.py` runs one authenticated owner-local HTTP
process while keeping the original tool module's globals separate for each
native MCP session. It is a candidate for Touchpoint's aliases, last-window
state and snapshot baselines; the stateless TGC/ATV adapter remains unchanged.

The existing protected five-field binding format from `python_mcp_http.py`
seals the original module bytes and a dedicated upstream credential. The
existing project interpreter supplies the original package and native MCP SDK.
Use an owner-local finite systemd budget and loopback `/mcp`; do not replace an
existing client entry before the managed owner route and actual backend work.

Only a valid initialize allocates state. At most16 logical sessions exist;
capacity refusal precedes body/module/SDK allocation and never evicts a live
or uncertain session. Each actor owns one SDK manager lifecycle and one module
namespace. Native public list/call/resource/prompt APIs delegate directly;
no tool schema translation, SDK patch, subprocess or action replay is added.
Physical tool calls are serialized across actors. DELETE closes only its own
actor and SDK transport bookkeeping. Idle cleanup runs every30 seconds after
1800 seconds without a short request or backend callback; active and uncertain
work is retained. Shutdown owns only these actor lifecycles.

Each actor permits one SSE GET. Sixteen streams cannot consume the two short
request slots needed for calls and DELETE; duplicate streams return409, short
request overload503, unknown sessions404. The outer owner auth admits at most
18 requests and the server bounds connection concurrency22. These counts do
not authorize a RAM/CPU budget increase.

Compatibility is not claimed for arbitrary FastMCP servers, custom lifespans,
callbacks requiring a different native server context, inherited workspace
semantics, or physical desktop ownership. Those need separate real proofs.

## Current evidence and remaining delivery

Nine narrow local units cover finite admission, cancellation, malformed JSON,
owned cleanup and SSE request starvation. Real installed MCP SDK1.28.1 plus
original Touchpoint0.3 source in a finite candidate process verified27 exact
original schemas,16 independent alias namespaces, two side-effect-free
`diagnostics(probe=false)` reads, seventeenth503, owned DELETE/reusable slot,
foreign401 and complete actor cleanup. A separate real native SSE check
verified two open streams plus tools/list200, duplicate GET409 and DELETE
closing only its own stream/actor. No GUI action, model, ZCode context reload,
production install or global client switch was performed by these checks.

The source candidate used the existing TGC SDK interpreter only for testing.
The original Touchpoint interpreter lacks `mcp`; installing its missing pinned
dependency, finite production singleton, managed owner registration, actual
backend operation and native client consumption remain required. A candidate
HTTP test does not close the fleet-wide STDIO migration.

Local evidence: `.tmp/managed-peer-delivery/session-http-source/` contains the
native case/attempt/result/cleanup receipts, schema digests and safe metadata
proofs. The initial candidate harness syntax failure is retained separately;
its failed run performed no SDK or backend operation.

### Installed server-100 Touchpoint slice

The finite owner service was installed from published source. Its original
Touchpoint interpreter/package remain unchanged; a private dedicated carrier
supplies the30 pinned dependencies (MCP1.28.1, uvicorn0.46.0). The native unit
is enabled, uses loopback9433 and256/512MiB/CPU1/tasks128/swap0, and measured
approximately51MiB with one process. No Hub/Herder/SDK restart occurred.

The managed `Touchpoint-ZCode100` connection preserves all27 exact unique-name
tool descriptors, original backend identity and session state isolation. A real
`diagnostics(probe=false)` call via its own owner profile completed in194ms;
foreign auth401, cross-profile403 and managed DELETE204 passed. Only ZCode's
existing enabled `touchpoint` entry changed from STDIO to owner HTTP/legacy,
with protected rollback/CAS and all other settings preserved. Duplicate token
packets were removed after verifying the final runtime/config storage.

The direct native SDK acknowledged DELETE200. The initial verifier wrongly
required204; that verification error and acknowledged result were retained,
without repeating the call. Another initial managed verifier rejected a whole
array digest before any backend call; separate descriptor-map comparison
confirmed the original definitions, followed by the first managed backend
operation. Earlier failed staging on internal wheel-cache links was retained;
only the existing within-lease symlink policy changed on a new unused case,
with resource bounds intact.

Current native ZCode context consumption and physical desktop interaction are
still unverified. This installed slice does not complete all enabled MCP,
plugin or fleet migrations. Protected receipts are under
`.tmp/managed-peer-delivery/touchpoint-http-production/`.
