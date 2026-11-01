# Native MCP Tasks from Code Mode

The Code Mode bridge can start long executor / child-MCP operations without
waiting for their command runtime. It uses the existing durable MCP Tasks
ledger and the ordinary authorized dispatch path. No second queue, task store,
privileged bearer, or new worker process is created.

## Use

```javascript
const task = await mcp.tasks.start(
  'shell:admin-node-100', 'shell_exec',
  {cmd:'sleep 3; printf task-ready', timeout:10},
  {idempotency_key:'a-unique-key-for-this-business-operation'}
);
// task.resultType === 'task'; task.taskId is durable.
const independent = await mcp.call('hub','status',{});
return {task, independent};
```

After doing independent work, collect the existing task rather than replaying
its command:

```javascript
return mcp.tasks.wait('the-returned-task-id', {timeout_ms:30000});
```

- `start(target, tool, args, opts)` returns a native Task-shaped receipt quickly.
  Equivalent: `mcp.call(target,tool,args,{task:true})`.
- `get(taskId)` reads the canonical `tasks/get` receipt once.
- `wait(taskId,{timeout_ms})` performs protocol-cadenced reads within one
  connector call, up to 0..55 seconds and the remaining outer Code Mode budget.
  It returns the current Task if still working. It never resubmits a command.
- `cancel(taskId)` and `update(taskId,inputResponses)` reuse native task
  cancellation and approved-input handling. Update cannot grant an approval.

The wait helper is synchronous inside the JS runtime: issue independent work
before waiting, rather than putting waits first inside Promise.all arguments.
The existing `mcp.call` still waits for a final result by default, so dependent
scripts preserve their earlier semantics. An explicit task receipt is not a
claim that its work succeeded. Check `status` and final `result` / `error`.

## Deadlines and errors

The command's `timeout` remains independent of the short outer Code Mode
`timeout_ms`. Closing the Code Mode request after durable admission does not
resubmit or erase the accepted operation. A wait timeout returns `working`,
not a command failure. Failed / cancelled / input_required states return as
native task states. Polling cadence follows the server's `pollIntervalMs`.

For a caller using raw MCP 2026-07-28, continue advertising
`io.modelcontextprotocol/tasks` in per-request capabilities and use
`tasks/get`, `tasks/update`, `tasks/cancel`. The JS helpers expose that same
ledger to connectors which currently only offer exec/ro tools; they do not
change or spoof the external connector's negotiated protocol version.

## Security and compatibility

Every start reuses target/tool authorization, instruction context, schema,
profile, approval, bounded autonomy, node routing, audit and idempotency.
Every read/control resolves the native receipt with the actual submitting
identity and current profile before returning output or mutating it.
Read-only Code Mode may read its authorized receipts but cannot start,
cancel or update a task. A task ID is not an authorization bypass.

Old job endpoints remain for consumers which have not migrated. No automatic
retry of a business command occurs on a timeout or lost response. No client
poll loop is added to the LLM instructions; after start, perform useful work,
then one bounded wait or a later status read.

Official extension reference:
https://tasks.extensions.modelcontextprotocol.io/specification/draft/tasks

## Acceptance

Unit coverage tests immediate task admission, simultaneous starts, same-key
idempotency, preserved ownership, forbidden read/control, bounded waits and
returning the existing native result. These fixtures are not evidence of
production execution. Release acceptance must execute a real delayed command,
obtain a task handle before it finishes, execute independent work, collect the
exact output without resubmission, and verify a separate cancellation.
