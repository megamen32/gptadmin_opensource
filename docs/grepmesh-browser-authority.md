# GrepMesh browser authority through GPT-Node

The browser path uses the current GPT-Node authentication, AccessProfile and
native MCP Tasks. The Console forwards the original SSO cookie to the Hub
adapter. The Hub verifies that cookie, resolves its explicit existing client
binding, and dispatches through the ordinary authorized Node path.

This is the authority contract for the browser candidate integrated after VERSION 1109. The historical isolated acceptance below does not establish production deployment of this adapter.
The earlier [Console UI plan](grepmesh-web-ui-plan-ru.md) remains a product plan;
its service-credential sketch is superseded by this contract.

## Configuration and identity

The Node process hosting Hub accepts these environment settings:

```text
GREPMESH_BROWSER_SSO_URL=http://127.0.0.1:18991/check
GREPMESH_BROWSER_ORIGIN=https://grepmesh.example
GREPMESH_BROWSER_TOKEN_BINDINGS='{"example-reader":"existing-managed-token-id"}'
```

The binding contains existing managed-token IDs, never token values. Each SSO
subject and each token ID occurs once. The existing managed token must be
durable, available in the canonical private store, current, and bound to a
profile with non-empty read roots. Missing bindings, revoked or expired tokens,
and unavailable profiles fail closed. Configuration does not grant new rights.

The verifier URL is a fixed literal loopback HTTP address with path `/check`.
The Hub sends the configured origin host as `X-Forwarded-Host` and reads the
verified response `X-GPTAdmin-User`. Legacy `X-Auth-User` responses are accepted
only without a conflicting identity. Incoming identity, authorization and ACL
headers cannot choose the client. The generic `/_services/` proxy does not
establish browser file authority.

The adapter accepts loopback ingress. POST requests must carry the configured
Origin; a conflicting Origin is rejected on every route. The original browser
cookie is verified again for each operation and each native Task receipt.

## Supported surface

| Route | Operation |
| --- | --- |
| `GET /api/browser/grepmesh/session` | Verified principal, profile ID/version, roots digest, authorization fingerprint, read roots and concrete authorized GrepMesh targets |
| `POST /api/browser/grepmesh/server/<slug>/mcp` | `tools/call` for `search`, `search_text`, or `read_text` on an existing registered `child_ref=GrepMesh` |
| `POST /api/browser/grepmesh/mcp` | Native `tasks/get`, `tasks/cancel`, or `subscriptions/listen` with only 1 to 64 `taskIds` |

Tool calls must advertise the native `io.modelcontextprotocol/tasks` capability.
Both `MCP-Protocol-Version` and
`params._meta["io.modelcontextprotocol/protocolVersion"]` must be `2026-07-28`.
Standard routing headers, including `Mcp-Name=taskId` for Task methods, retain
their ordinary validation. Missing capability or a protocol downgrade is denied
before queue admission. The public legacy MCP API retains its existing behavior.

The adapter rejects supplied `arguments.filesystem_acl`, `arguments._meta`,
`origin_host`, `hop_count`, `request_id`, and
`params._meta["io.gptadmin/filesystem-acl"]`, including explicit null values.
Standard protocol, client capability and idempotency metadata is retained.

## Authorization and lifetime

The Hub uses the stored credential internally with ordinary `mcpAuth` and
current profile resolution. Target, tool, argument, owner and filesystem policy
checks remain on the native dispatch path. Read-only profiles may call GrepMesh
`search_text` and `read_text` only with bound non-empty read roots. The Node
queue carries the existing caller and filesystem policy envelope to the child;
the executor enforces canonical path access.

The session fingerprint covers the verified subject, token/client identity,
token digest, complete current profile, configured verifier/origin and concrete
permitted targets. Native Task ownership also retains token/client and profile
revision binding. An open subscription rechecks the SSO grant, token and
fingerprint before returning each receipt. Revocation, profile narrowing or
binding changes prevent reuse of cached browser handles and Task receipts.

## Recorded acceptance

The [sanitized acceptance evidence](grepmesh-browser-acceptance-20261108.json)
records 39 successful checks from a real isolated HTTP run completed at
2026-11-08T02:47:56Z. The native runner task was
`516df23f8ab50580f0180aa5cd7decae`, with return code 0.

The run used two real isolated non-admin SSO accounts, distinct read-only native
profiles, exact GrepMesh child targets, and no shell grants. It covered native
state persistence over Node restart, search, SSE, match/document reads, foreign
handles, forged identity and transport policy, profile narrowing, site-grant
removal and restoration, and token revocation. The SSO implementation was the
deployed implementation operating on isolated private state.

The tested Node source was `10d9145c65af981192af32458d4b38883ef76011`.
Its executable SHA256 and the exact GrepMesh/SSO hashes are preserved in the
evidence. Production was untouched; private QA state was removed and the
isolated processes stopped. Deployment acceptance is recorded separately.

Source integration also preserves origin/main commit
`f1f36a5b53ee6ef91ae76e7b30ddc9471644d2d8`. Its two added Code Mode byte-bridge
tests passed on the integrated source without skips. The unchanged browser
suite and HTTP acceptance were not replayed for the documentation/version change.

## Task owner topology boundary (2026-11-08 integration)

Supported dispatch designs are the browser endpoint on the executor-owning local
Node, and a verified quorum leader queue polled by the enrolled executor.
BrowserSubject and BrowserBinding are persisted in the ingress task ledger before
queue publication; receipt reads/cancellation/subscriptions authorize that ledger.
Quorum eligibility is checked by quorumExecutorUsesLeaderQueue. This source-level
support statement is not a new installed local/quorum acceptance result.

Standalone remote browser Tasks via an HTTP/Node peer are currently unsupported.
A known remote route/connection without a pinned local owner or verified quorum
queue returns HTTP 503 browser_task_owner_unavailable before creating a task.
The browser guard is not delegated through peer HTTP. This guard prevents false
acceptance into an ingress queue; it does not implement remote task execution,
remote receipt routing, cancellation or subscriptions. Deploying browser admission
on the actual owner requires that Node's own verified SSO/binding configuration.
Existing legacy poll-only operation is unchanged.

The guard applies only to browser tools/call admission. Ordinary remote shell,
file and control routes remain independent. Focused BrowserGrepMesh, TypedPeerRoute
and NodePeers tests passed in integration (12.641s); these are fixture/source
checks, not installed remote proof. See fullcycle evidence/integration-20261108/
browser-peer-review.md. No full remote browser path is claimed.

## Persisted browser ownership boundary

Browser task callers persist the verified SSO subject and authorization binding
fingerprint alongside native token/client/profile identity. A fresh request after
assigning the same token to another SSO subject cannot read or cancel the original
task. Old browser receipts without these fields fail closed for browser clients.
Restoring exactly the original subject and binding restores identity equality;
this is not a monotonic binding-generation or permanent-revocation mechanism.
Source TDD receipts are in the fullcycle integration-20261108 evidence directory;
no new installed SSO/Node acceptance is implied.
