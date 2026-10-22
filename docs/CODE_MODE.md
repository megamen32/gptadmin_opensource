# GPTAdmin `exec`

`search` finds tools visible to the authenticated caller. `exec({code})` runs a
short JavaScript program in the existing QuickJS/Wazero sandbox. Intermediate
MCP answers stay there; return only the requested value. `code_exec` and direct
`execute(target,tool,args)` are retired and return migration errors. For clients
that cached an older connector schema, transport accepts the exact name
`execute` only with `{code, context?, timeout_ms?, max_calls?, heap_bytes?,
max_output_bytes?}` and canonicalizes it to `exec` before policy checks. Any
direct-routing or extra argument is rejected. This alias does not reduce tokens
for a client still sending its cached longer name.

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
