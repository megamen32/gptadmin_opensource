# GPTAdmin `exec`

`search` finds tools visible to the authenticated caller. `exec({code})` runs a
short JavaScript program in the existing QuickJS/Wazero sandbox. Intermediate
MCP answers stay there; return only the requested value. `code_exec` is retired
and returns a migration error.

The public catalog is deliberately short:

- `search` — find the capability that is needed.
- `ro` — read and analyse without changing anything.
- `exec` — run JavaScript, including several MCP calls and the processing of
  their results.

The task readers `job` and `expand` stay published, because a client has to be
able to read the result of a task it started.

The direct `execute(target,tool,args)` facade is **not public**. It invites one
tool call per request instead of one script that batches the calls, and its
name costs more tokens per call than `exec` and `ro` (`exec` is one token in
`o200k_base`, `code_exec` is two; inside JavaScript `call` is one token and
`mcp.call` is three). A client that still calls it receives the explicit
`execute_facade_retired` answer with the migration path. The code-only
spelling `execute({code})` stays as the old transport alias and canonicalizes
to `exec` before policy checks; a call that mixes `code` with direct routing
arguments is refused before anything is executed. The internal dispatcher
(`executeMCPTool`) stays: `ro`, `exec`, the relay and the agent routes use it.

```javascript
const hits = await search("file editor", {target: "file:node"});
const t = hits.results[0];
const r = await call(t.target, t.name, {action:"view", path:"/tmp/note.txt"});
return r;
```

`await mcp.call(target, tool, args)` and `await call(target, tool, args)` use the
same request identity, target/tool ACL, schema contract, approval, autonomous
budget, idempotency and audit path as a normal Hub call. The object form and
`call(target, tool, args, {index:true})` are supported. A failed, denied,
pending or nonterminal call stops later calls; receipts are retained and no
call is automatically replayed. Recursive `exec` is denied.

The `mcp` facade has `search`, `discover`, `schema`, `job` and `expand` helpers.
Root Hub tools, registered independent MCP roots and registered child MCP tools
are searchable/callable through `mcp.call`; this does not make native ChatGPT
apps visible inside GPTAdmin. Root MCP schemas refresh on an explicit target
search and are cached per principal/target with a five-minute TTL. Unfiltered
search uses warm root schemas only and reports incomplete coverage when root
schemas are cold. The per-server root cache is capped at 512 entries. Each query
rechecks registry availability, owner connection and caller ACL. Child refresh
remains bounded to four cold targets. Results
include compact necessary argument schemas and a short JavaScript call.

## File helpers

File helpers require an explicit `file:<host>` target and route to the existing
file MCP tools. The sandbox never receives a host filesystem handle.

```javascript
const rows = await mcp.files.readMany("file:node", ["/tmp/a.txt", "/tmp/b.txt"],
                                      {max_bytes: 32768});
return rows.map(x => ({path:x.path, status:x.status, truncated:x.truncated}));
```

Available methods: `mcp.files.ls(target,path,max_bytes)`, `mcp.files.read(target,
path,{start_line,end_line,max_bytes,index})`, `mcp.files.readMany(target,paths,opts)`,
`mcp.files.create(target,path,text)`, `mcp.files.replace(target,path,old_text,
new_text,opts)`, `mcp.files.batchEdit(target,path,operations,opts)`, and
`mcp.files.checkpoint(target,path,opts)`. For repeated operations on one host,
bind the target once: `const f=file("file:node"); const r=await f("read",
"/tmp/note.txt",{max_bytes:32768});`. The bound function accepts the helper
name, path and options. `f("read",path,{index:true})` opts that read
into the content store. They preserve
the file MCP's bounded reads and stale-line/exact-text guards. Reads do not
claim completeness beyond the reported range or byte limit; inspect truncation
metadata or request another bounded range. Checkpoints are explicit rather
than created before every edit.

`readMany` makes one authorized MCP call per path. For 50 paths, set
`max_calls:64` on `exec`; this includes search and other bridge calls in the
same script.

## Read-only executor (`ro`)

`ro` runs the same short JavaScript in the same sandbox as `exec`, with the same
budgets, under a hard node-side read-only ceiling. The ceiling is enforced per
operation before the operation reaches the executor — not by an annotation, not
by the method name, not by parsing the script, and not by an instruction to the
model.

`ro` may:

- compute, filter and aggregate;
- read permitted files (`file_editor` `view`, `system_inspect`
  `read_file`/`list_directory`, `file_checkpoint` `list`/`diff`, `file_backup`
  `list`);
- use permitted service reads (`mcp_manage` `list`/`status`/`config`,
  `control_catalog`, `mcp_tools`, and the read-only hub facades);
- read and search results the caller can already reach.

`ro` refuses, before anything is executed:

- arbitrary shell — including `cat` and `echo`;
- creating, changing or deleting files;
- configuration changes, enabling or disabling an MCP, restarts;
- sending messages and any other external mutation;
- escalation through nested `exec`/`execute`, raw `mcp_call`/`mcp_request`,
  `fleetExec` and similar wrappers;
- `index()`, `drop()` and `index:true` — a read-only run does not change the
  caller's stored sources.

A refusal is **sticky**: catching the exception inside the script does not let
the script continue with forbidden operations. The next call is refused with
`run_stopped` and the run does not report `completed`.

External MCP servers are not read-only by default. A read-only run may use one
only when an administrator declared an explicit trusted rule for that exact
`target`+`tool` pair with `read_only: true`. An upstream `readOnlyHint` and a
`get`/`list`/`search` name prefix are not enough.

A read-only run never writes into the caller's stored sources. It may build the
search cache of a source it is allowed to read — building a cache is not a
change to the data — but it may not create, replace or delete a stored source.

## Saving a large result, indexing on demand

Saving and indexing are separate. A large result is **stored in full**; the
FTS5 index is built **on demand**.

- Intermediate MCP results stay inside the script. An ordinary `mcp.call()`
  returns the expected object or stdout, never a handle.
- At the `exec` result boundary a small result is returned exactly as before:
  no descriptor, no store write.
- A result that does not fit the output budget is stored in full, and the model
  receives a small descriptor instead:

  ```json
  {"saved": true, "source": "…", "name": "exec-result", "bytes": 200000,
   "format": "text", "complete": true, "indexed": false, "chunks": 50,
   "created_at": "…", "expires_at": "…",
   "origin": {"target": "…", "tool": "…"},
   "read":  {"call": "mcp.content.read",   "args": {"source": "…", "chunk_id": 1}},
   "search":{"call": "mcp.content.search", "args": {"source": "…", "query": "…"}},
   "index": {"call": "mcp.content.indexSource", "args": {"source": "…"}}}
  ```

- **Saving alone creates no FTS record.** The stored source starts unindexed
  (`"indexed": false`). The first `search` over that source builds the index;
  an explicit `index:true` / `mcp.content.indexSource` builds it immediately.
  A repeated search does not rebuild it.
- `console.log`, the results of executed actions and the receipts are never
  lost: a stored result still returns its logs and the descriptor names the
  stored source.
- A storage failure never re-runs the script. It is reported honestly, with the
  number of operations that already executed and the last receipt.
- The stored result is bound to **every** target/tool the run read, not only the
  last call. Withdrawing the right from any of them withdraws the stored
  result: a saved result is not a way around a revoked permission.
- The handle survives a normal restart until its TTL expires. It is not bound
  to a PID, a one-shot runtime or an instruction receipt.

## Reading back the confirmed full log

ShellMCP can answer with a stdout tail plus the path of the complete spool
file. The tail is not the log, and indexing the tail would produce an index
that looks complete but is not.

When indexing is requested for such a result, the Hub uses the **confirmed
full source of the already executed operation**:

- the original command is never re-run;
- the spool path is never taken from the MCP text — only from the Hub's own
  node-side job record;
- the source must be a confirmed resource with an owner, an access check and a
  task-ownership check (`gptadmin://task/<job>/stdout`);
- the output is read in bounded chunks and the completeness is proven from the
  size metadata (the total size may not change, every chunk must continue at
  the expected offset, and the accumulated bytes must reach the reported total
  exactly).

If there is no confirmed full source, the result reports `complete: false` with
the reason and the metadata of the available part. A partial index is only
stored with explicit consent and carries a permanent incompleteness marker.
Reading a saved result is not a re-execution of the original tool.

## Opt-in content store

The content store is separate from tool-schema search and is opened only when
the script explicitly indexes content. Ordinary calls create no source.

```javascript
const source = await index(largeText, {name:"worker log"});
return {source:source.source, bytes:source.bytes, chunks:source.chunks,
        expires_at:source.expires_at};
```

For a tool result, `await call(target, tool, args, {index:true})` returns the
same compact source metadata instead of the raw result. Select a known text
field explicitly with `{index:true,text:"stdout"}`. Shell stdout, file text,
and MCP text blocks are extracted as raw text. Other objects are stored as
JSON with `format:"json"`. Spilled shell output and truncated file reads are
rejected instead of indexing a partial tail. The 2 MiB/source limit applies to
raw text bytes or serialized JSON bytes, including JSON envelope overhead.
Pass `{name:"..."}` and optional `{target,tool}` origin to `index(text,opts)`
when indexing a file read; source reads then recheck that target/tool access.
A later `exec` can use
`mcp.content.search(query,{source,limit})`, then
`mcp.content.read(source,chunk_id,{before,after})`; deletion is
`mcp.content.delete(source)`. Search is Unicode/punctuation tolerant, source
scoped, BM25 ranked and returns chunk/line ids with bounded snippets. Read
requires the same principal and rechecks the originating target/tool access
and current target availability. Text indexed directly with `index(text)` is
owned by that principal and has no external origin.

Defaults are 2 MiB/source, 32 MiB/principal, 128 sources/principal and 24-hour
TTL; hard maxima are 8 MiB/source, 128 MiB and 1,000 sources, and 7 days.
Expired rows are excluded and pruned during store operations. Store creation is
lazy under the configured Hub state directory and uses mode 0600. The returned
source handle is opaque; metadata omits content. No hidden truncation is used.

## Isolation and budgets

The existing QuickJS/Wazero runtime remains isolated: no host file, environment,
process, network or timer access is exposed. Tool operations are individually
authorized. The executor enforces wall time, heap, call count, source length,
intermediate payload and combined returned-value/log budgets. Inspect the live
`exec` schema for current values; do not infer tokenizer cost from bytes.

Native Codex/ChatGPT apps are not automatically imported into Hub search or
`mcp.call`. MCP composition applies to Hub tools and registered root/child MCPs.

## Verification

Run the focused tests and the reusable candidate harness documented in
`.agents/exec-context-20261022/final.md`. The harness launches a local candidate
and real fixture MCP process, exercises the actual `file:<host>` path against
temporary files, and records byte counts, calls and latency. Tokenizer counts
are left blank when no actual tokenizer is available. Competitor comparisons
are contract references only; no unrun competitor savings are claimed.


## Release evidence and exact file/content contracts

See `EXEC_CONTEXT_20261022.md` for short bound file methods, source-scoped search/read, limits, source-contract comparisons and the reproducible direct-MCP token benchmark.
