# M18L4 — bounded child tools/list enumeration

- Bug: M18-CHILD-SCHEMA-PAGES
- Base: e94c6cef899af446d8e43fd51935ba09d4887be1
- Worktree branch: lhc/schema-pagination-worker-20261108
- Scope: go-shellmcp client pagination and mcpChildTools completeness markers.
- Acceptance environment: local HTTP MCP protocol fixtures and Go package tests. No live third-party MCP, production deployment, or ChatGPT picker acceptance is claimed.

## Reproduction and repair

Before production edits, the added tests failed against the base revision: ListTools, ListToolsScoped with an empty scope, and scoped ListToolsScoped returned only the first page. A permitted tool on page two was omitted by DirectChildTools while the call still returned success.

Both entry points now share bounded enumeration. The initial request omits the cursor; continuations preserve the opaque cursor exactly and continue through empty pages. Tool definitions remain intact, including outputSchema, annotations, and long descriptions.

Limits are 64 pages, 4096 tools, 8 MiB in aggregate JSON-encoded result payloads, and a 30 second overall context deadline (an earlier caller deadline remains effective). The byte budget covers decoded result payloads, not raw transport allocation or a schema cache. Missing or malformed tools arrays, non-object tools, non-string cursors, repeated cursors, transport errors, cancellation, or exceeded limits return an error and no partial catalog.

mcpChildTools applies the existing tool allowlist after complete enumeration. Only successful complete results contain schema_complete=true and schema_completeness="complete"; errors return no successful result. The go-hub forwarding changes belong to the separate schema worker.

## Evidence

Commands and timings are recorded in pagination-checks.json. All test output uses disposable local fixture data and is sanitized.

- pagination-red.txt: focused regression tests fail on the unchanged production source at the base revision.
- pagination-green.txt: the same focused command passes after the fix.
- pagination-related.txt: both complete affected Go packages pass.
- git diff --check: passed.

No runtime, deployment, VERSION, roadmap, common plan, or go-hub files were changed. Commit remains local pending the root agent's publication gate.
