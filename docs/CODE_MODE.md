# Code Mode

Code Mode runs a short JavaScript function body in the Hub. A script can read tool schemas, pass one tool's result to another, and return only the JSON value the assistant needs. Intermediate responses remain inside the run and the existing tool/job audit path; they are not automatically appended to the assistant's conversation.

This is an internal Hub tool, not a replacement client or a new top-level connector tool. Existing `discover`, `schema`, `execute`, `job` and `expand` calls keep their interfaces.

## Calling it

Fetch the exact schema with `schema({target: "hub", tool: "code_mode"})`. Use its `call_ref` in `execute({ref, args})`, or use the existing explicit route:

```json
{
  "target": "hub",
  "tool": "code_mode",
  "args": {
    "code": "const tools = await mcp.schema('hub'); return tools.tools;",
    "timeout_ms": 30000,
    "max_calls": 32,
    "max_output_bytes": 16384
  }
}
```

`code` is an async function body: `return` and `await` are supported. It is not a Node.js program. Return a JSON-serializable value; no return value becomes `null`.

The script API has three functions:

| Function | Result |
| --- | --- |
| `mcp.discover(query?)` | Caller-visible targets in `{servers: [...]}`; optional substring filter. |
| `mcp.schema(target, tool?)` | Tool names when `tool` is omitted; otherwise `{tool, inputSchema, description, ref}` and available schema-contract metadata. |
| `mcp.call({ref, args, ...})` | One authorized call. Alternatively supply `target` and `tool` instead of `ref`. |

A successful `mcp.call` returns `{ok: true, status: "completed", result, target, tool}`. `result` is the underlying MCP response, not a flattened stdout string. Optional call fields are `idempotency_key`, `schema_version` and `schema_digest_sha256`. A schema reference is scoped to the same authenticated principal and expires like a reference fetched outside Code Mode.

Callbacks execute sequentially in this version. `await` supports composing the script; it does not turn `Promise.all` into parallel tool dispatch.

## Example: two real shell calls, one small answer

Replace `shell:your-host` with a target from discovery. This is the Go ShellMCP response shape, so other MCP tools may expose their data differently.

```javascript
const schema = await mcp.schema('shell:your-host', 'shell_exec');

const first = await mcp.call({
  ref: schema.ref,
  args: {
    cmd: "python3 -c 'print(chr(120)*20000,end=str())'",
    timeout: 5
  }
});
const text = first.result.structuredContent.result.stdout;

const second = await mcp.call({
  ref: schema.ref,
  args: {
    cmd: "printf '%s' '" + text.length + "'",
    timeout: 5
  }
});

return {
  input_bytes: text.length,
  verified: Number(second.result.structuredContent.result.stdout)
};
```

The selected result is `{"input_bytes":20000,"verified":20000}`. The 20,000-character intermediate output is not returned with it. The Hub also returns small execution metadata: `status`, `calls`, `elapsed_ms` and `limits`. `calls` counts bridge operations, including schema/discovery and attempted calls, so this example reports three.

This demonstrates response-size reduction, not a guaranteed token saving for arbitrary workloads. Full tool responses can still be retained by GPTAdmin's normal job/audit storage.

## Permissions and failure behaviour

The run retains the original HTTP request's authenticated identity and access profile. Entry into `code_mode`, discovery/schema access and each nested tool call are checked. A profile needs the relevant facade and Hub-tool permissions as well as permissions for every target/tool it calls. Code Mode is not exposed as read-only: a script may invoke state-changing tools.

Calls use existing connection ownership, schema-contract validation, admission, approval, bounded-autonomy, idempotency and audit handling. No new permissions are granted. An `idempotency_key` applies to the nested call that supplies it; a script as a whole is not magically idempotent.

The first failed, denied, approval-required or still-running tool call stops the chain. Catching a JavaScript exception does not clear that stop: subsequent bridge operations are refused and the run remains failed. A bounded receipt preserves available job/approval identifiers so the caller can use the ordinary follow-up tools instead of replaying a write.

**A run is not a transaction.** Successful earlier calls are not rolled back. Ending a script or reaching its time budget does not necessarily cancel a job already accepted by an external executor. Inspect its receipt before retrying; there is no automatic retry or resume of the script.

A script cannot recursively invoke `code_mode`.

## Runtime and limits

Each run gets a fresh QuickJS/Wazero instance using the pinned `github.com/fastschema/qjs v0.0.6` dependency. Arguments and results cross the host bridge as JSON, not Go objects. Direct host filesystem, inherited environment, HTTP clients and process-spawning APIs are not supplied. Access through an authorized MCP tool remains possible and is governed by that tool's permissions.

The library's default working-directory mount is not used: the filesystem root is set to the non-directory `os.DevNull`. Standard output/error are discarded. QuickJS built-in module names can still exist, but their presence is not permission to access the host. A Wazero host-call guard rejects WASI `poll_oneoff`, preventing `qjs:os` sleep/timer calls from holding the host beyond the JavaScript deadline.

| Budget | Default | Maximum |
| --- | --- | --- |
| Script/run time | 30 seconds | 60 seconds; minimum 100 ms |
| Bridge operations | 32 | 64 |
| Selected JSON output | 16 KiB | 64 KiB; minimum 256 bytes |
| JavaScript heap | 64 MiB | Fixed per run |
| JavaScript source | — | 64 KiB |
| Concurrent runs per Hub process | — | 8 |
| One intermediate bridge JSON payload | — | 8 MiB |

The heap limit is the JavaScript allocator limit, not a claim that total Go/WASM process memory equals 64 MiB. VM execution is interrupted on cancellation; synchronous relay waits use the remaining request budget. Already-submitted external jobs retain their own lifecycle.

## Verification

From `go-hub`:

```sh
GOMAXPROCS=2 go test -p 2 -count=1 -run '^TestCodeMode' ./internal/hub
GOMAXPROCS=2 go build -p 2 ./cmd/gptadmin-node
```

Tests cover JSON primitive/array/object returns, run isolation, real QuickJS built-in filesystem/environment denial, synchronous and asynchronous host-timer denial, loop and memory limits, access profiles, schema references/contracts, sticky failures, receipts and intermediate/output budgets.

For an executable canary, run a separate `gptadmin-node` with a loopback listener, temporary config/state, its own credentials and an explicitly named local executor. Obtain a bearer through that candidate's `/admin/api/mcp/issue-token`, then execute the example through `/mcp`. Verify the old `execute` path too. Bind a second candidate-issued bearer to a profile that allows Hub Code Mode but denies the real local shell target, and verify that a denied command never creates its test marker and cannot be followed by another call.

Do not restart an active production Hub merely to run this canary. Deployment is a separate step and must preserve in-flight jobs and use the verified commit's binary.

## Profile context and compact output

The Hub's [instruction-context workflow](INSTRUCTION_CONTEXT.md) applies before
Code Mode execution and to nested operations. Get a fresh `schema(hub, code_mode)`
and use its `call_ref`, or pass a current `context` alongside explicit routing.
The script inherits the original caller's receipt; it does not need to repeat
profile instructions for each nested call.

The default compact response is `{result: <selected value>, operation_id: ...}`.
Use that diagnostic handle with `expand`, or request `detail:"full"` on the outer
execute, for the full status/calls/elapsed/limits envelope described above. Failed,
partial or approval-required runs retain their recovery information.
