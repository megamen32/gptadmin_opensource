# GPT Admin: AI tool ergonomics

This change builds on the existing MCP tasks, typed JSON Schema validation,
instruction receipts, and principal-scoped result store. It does not add a
background service, a second task queue, or a policy engine.

## Search: compact first

```js
const matches = search({query:"file editor", limit:5});
return matches.items.map(i => ({path:i.path,description:i.description,required:i.required}));
```

The default output includes exact `tools["target"]["tool"]` paths and a
short coverage summary. Coverage still reports incomplete results instead of
pretending they are exhaustive. Retrieve full schemas and per-host failure
details with `search({query:"...",detail:"full"})` or
`mcp.schema(target,tool)`. Aliases that match multiple online hosts still fail
rather than choosing one arbitrarily.

## Correct input types within the same Code Mode run

```js
let args = {cmd:123};
try {
  await tools["shell:host"].shell_exec(args);
} catch (e) {
  // Before dispatch only: e.code === "invalid_arguments".
  // e.validation.path identifies the wrong field, e.g. "/cmd".
  args={cmd:"printf valid"};
}
return await tools["shell:host"].shell_exec(args);
```

Invalid JSON Schema arguments are recoverable **only before dispatch**, with
`code`, `path`, `expected`, and `actual`. Failed/unknown outcomes after a
real dispatch, approvals and permission refusals remain sticky. Preflight does
not burn an additional tool-call slot. It has its own bound of two times the
Code Mode call budget.

## Durable task lookup (no replay)

```js
const previous = mcp.tasks.list({limit:10});
return previous.tasks.map(t => ({
  id:t.task_id, status:t.status,
  trace:t.trace_id, get:t.get
}));
```

- `mcp.tasks.list({limit,cursor})` lists bounded canonical task receipts for
  the **same verified credential and current access profile**, with a
  `next_cursor` for subsequent pages. The caller must also be allowed to use
  Hub's `job` read. It never sends commands, retries actions or acknowledges
  existing work.
- `mcp.tasks.get(task_id)` and `mcp.tasks.wait(task_id)` resume *observation*
  of already accepted work. `parent_task_id` and `trace_id` provide
  provenance; `receipt_path` points to the authenticated admin task endpoint.
- ChatGPT conversation URLs are not available in MCP task metadata. No URL
  to an unrelated chat is inferred or fabricated. New credentials or changed
  profiles must satisfy existing authorization again.

## Instruction delivery

The first authenticated discovery or required-instructions refusal delivers
the current instruction text and a signed delivery receipt. A subsequent call
on the same authorized credential gets `instructions_status:"current"`
rather than another full copy. Profile and document-digest checks remain
mandatory; 428 remains appropriate before any verified delivery. Delivery
does not assert that a model understood the text.

## Complete large results, not hand-written chunk loops

```js
const saved = await mcp.content.index("long ...",{name:"example"});
const full = await mcp.content.readAll(saved.source,{max_bytes:2097152});
return {bytes:full.bytes,complete:full.complete,text:full.text};
```

The same read works for automatically saved oversized Code Mode results.
`readAll` verifies the exact byte count and principal/origin permissions and
rejects incomplete sources unless `allow_partial:true` is explicitly given.
The default budget is 2 MiB (ceiling 8 MiB). The Code Mode output budget
still applies; oversized final output uses the existing persistent result
descriptor, which now advertises `mcp.content.readAll`. Source TTL, quota,
and privacy controls are unchanged.

## Testing

```sh
cd go-hub
go test ./internal/hub -run 'Test(CodeMode|Tasks|Task|Content|Search|Instruction|Readonly|ReadOnly|Access|MCP)' -count=1
```

No production service restart is required to run the tests.
