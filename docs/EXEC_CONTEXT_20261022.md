# Exec / search / opt-in content indexing: 2026-10-22

## Public workflow

The public catalog is deliberately short: `search` finds the capability, `exec({code, context?, timeout_ms?, max_calls?, max_output_bytes?})` runs the script that batches the calls, and the read-only `ro` runs the same script under a hard read-only ceiling. Use `search` for bounded tool-name/description FTS5 discovery, then process results in the QuickJS/Wazero sandbox. The task readers `job` and `expand` stay published, because a client has to read the result of a task it started.

The direct `execute(target,tool,args)` facade is no longer public: it invites one tool call per request instead of one script that batches the calls, and its name costs more tokens per call than `exec` and `ro`. A client that still calls it receives the explicit `execute_facade_retired` answer with the migration path; the code-only spelling `execute({code})` remains as the old transport alias, canonicalizes to `exec` before policy and audit, and a call that mixes `code` with direct routing arguments is refused before execution. The internal dispatcher stays: `ro`, `exec`, the relay and the agent routes use it.

The read-only executor `ro` is bounded node-side per operation: it may compute, read permitted files and services, and search results the caller can already reach; it refuses arbitrary shell, file writes, configuration and MCP changes, restarts, external mutations, escalation through nested `exec`/`execute` and raw wrappers, and it never writes into the caller's stored sources. A refusal is sticky. Details: `docs/CODE_MODE.md` and `docs/MIGRATION_RULE_TOOLS.md`.

Inside a script, `call(target,tool,args,opts?)` and `search(query,opts?)` retain the authenticated caller's authorization, approvals, connection ownership, idempotency and audit. Registered independent root MCPs, ShellMCP child MCPs and safe Hub helpers are supported. Native applications visible only to a ChatGPT client are not automatically registered in this Hub. Recursion and retired direct-execution aliases are rejected. Execution is sequential, not a transaction. Failed or pending calls stop the chain, including nested MCP protocol errors. Completed-operation receipts survive later serialization/output errors; no automatic replay occurs.

## Files without shell boilerplate

```js
const f = file('admin-node-100');
const r = await f.read('/path/to/file', {end_line:1000,max_bytes:262144});
if (r.truncated || !r.text_exact) throw Error('Incomplete or legacy view');
return {lines:r.line_count, bytes:r.bytes};
```

Bound methods include `ls`, `read`, `readMany`, `create`, `replace`, `batchEdit` and `checkpoint`. Methods take `(path, options)`; for example `f.create(path,{text:'hello\n'})`, `f.replace(path,{old_text:'hello',new_text:'hi'})`, and `f.batchEdit(path,{operations:[...]})`. The existing callable form `f('read',path,options)` and `mcp.files.*` remain available. Exact raw reads preserve final newline and CRLF; line IDs are separate metadata. Older nodes' reconstructed annotated views are marked `text_exact:false`, not presented as byte-exact. Selected ranges/byte caps report `truncated`. Defaults remain 200 lines/64 KiB; explicit reads are capped at 256 KiB per call and editable source files at 8 MiB. No filesystem is mounted into the JS sandbox. The 50-file aggregate requests max_calls=64 (default32, maximum64).

## Explicit content retention only

```js
const d = await call(target,'large_log',{}, {index:true,text:'text',name:'build-log'});
return d; // compact source handle, format, size and expiry, not the log
```

A later script can query the same handle without calling the MCP again:

```js
const hit = (await search('ERROR',{source:sourceID,limit:3})).hits[0];
return await read(sourceID,hit.chunk_id,{before:1,after:1});
```

`index(text,{name})` explicitly indexes text already inside the script. `drop(sourceID)` deletes it. The equivalent `mcp.content.index/search/read/delete` API remains. Tool-catalog search and retained-content search are distinct; content selection requires `{source:...}` or `{scope:'content'}`. Ordinary calls do not index their output. Index-on-call preserves text lines rather than serializing logs into one escaped JSON line. Partial file/tool payloads are rejected for indexing. Content search and reads recheck origin permissions, disabled targets and principal ownership. Sources are not global shared search results.

Defaults: 2 MiB/source, 32 MiB and128 sources/principal, 24-hour TTL; service-wide limits128 MiB/1000 sources and a SQLite page limit of512 MiB at the default4096-byte page size. Expiry is checked on access. Chunks target4000 UTF-8 bytes and prefer newline boundaries; individual longer lines require adjacent bounded reads. The return+console budget is enforced together (default16 KiB, maximum64 KiB). TTL is not a promise of secure erasure from filesystem backups or old audit records.

## Large results at the exec boundary

A result that does not fit the exec output budget is stored **in full** and the model receives a small descriptor instead of the payload: `source`, `bytes`, `format`, `complete`, `indexed`, `expires_at` and how to read or search it. Saving creates **no** FTS record — the stored source starts unindexed, the first `search` over it builds the index, and an explicit `index:true` / `mcp.content.indexSource` builds it immediately. A repeated search does not rebuild it. The stored result is bound to every target/tool the run read, so withdrawing a right from any of them withdraws the stored result. A storage failure is reported honestly and never re-runs the script. When a ShellMCP answer is a stdout tail plus the path of the complete spool file, indexing uses the confirmed full source of the already executed operation — the command is not re-run and the tail is never indexed as if it were the full log. See `docs/CODE_MODE.md`.

## What was actually tested

Real isolated candidate Node, local ShellMCP/files, an independently registered HTTP root-MCP relay process, and a stdio child-MCP process. One JS script found/called root+child+Hub helpers. Temporary files were created, read exactly, updated by line ID, rejected on stale ID, and indexed/read back. A1,860,032-byte log's beginning/middle/end markers were retrieved as full exact lines in later exec calls. Opted-out calls, source expiry/principal separation/revocation/disabled-root denial, output budgets, migration rejection and serialization stress have focused regressions. A retained JS string workaround avoids reading the freed CString in pinned qjs v0.0.6 `QJS_JSONStringify`; shared module-cache files are untouched.

## Token measurements, not billing claims

`scripts/exec_token_benchmark.py` runs the old direct-MCP facade and new exec in two real isolated processes with identical fixtures:50 files/925,329 raw bytes and one1,860,032-byte log. It counts serialized call names+arguments and connector structured responses, including initial discovery/schema/instruction cost. Wire JSON counts are reported separately. Tokenizer: tiktoken0.14.0, o200k_base. No model inference bill is measured. Baseline full MCP envelopes include duplicated content/structured fields; ratios describe this particular direct-read workflow, not all clients or optimal baseline scripts. The baseline log is fetched once and retained in context; no artificial repeated refetch is charged. The new path indexes once and searches from separate execs.

| Scenario | Direct tokens, bootstrap included | Exec tokens, bootstrap included | Ratio |
|---|---:|---:|---:|
| 50 files | 674,278 | 3,630 | 185.752x |
| Large log + three queries | 902,780 | 4,365 | 206.822x |
| One tiny demo call | 2,597 | 3,554 | 0.731x |

These are the recorded candidate-run measurements, not a guaranteed production saving. Small calls can cost more through exec. Exact isolated names in both o200k_base and cl100k_base: exec=1, code_exec=2, call=1, mcp.call=3. Returned handles and transport prefixes add further tokens.

## Source-contract comparison, not competitor speed benchmark

Reviewed pinned sources, without copying their implementation: context-mode `a92cf6a77ac02971fb5fafe2155e5ee229491884` (`src/store.ts`, `src/executor.ts`); Airlock `e605529334a250d3158d546b5702c7651eb052c6` (`service/chat/executor.go`); Sol `3b47827311500f1fe58fc7157ac1f6223744fc06` (`tools/read.go`, `tools/edit.go`, `tools/apply_patch.go`); OpenCode `7456ad872eecd5f7f857e2cbe9135c1407b12795` (`packages/codemode/README.md`). Comparable scenarios verified here: in-script aggregation, reusable indexed large-output sources, bounded source retrieval, exact guarded file edits, and root+child MCP composition. No claim that all17 clients, OpenClaw, all languages, concurrency or every competitor capability were executed head-to-head. Root schemas use bounded per-target cold refresh; coverage reports incomplete caches/offline nodes. Some daemon/proxy/failover settings remain cataloged administrative-API surfaces rather than new MCP setters.

Reproduce: `bash scripts/run_exec_context_e2e.sh`; run the fair benchmark with `--baseline`, `--candidate`, `--fixture`, `--report`. Raw state, private credentials, candidate binaries and agent logs are intentionally not committed.


## Complete files and spilled stdout

The live shell transport can spill large stdout into a file and return only a tail. Indexing that tail is rejected with content_index_partial; do not rerun an accepted command. Read its receipt to obtain the complete spool path, then use await file(host).index(path).

file(host).readAll(path,{max_bytes}) reads bounded ranges through the authorized file MCP and returns exact text and source_sha256. Each range carries the complete-file digest; changes between ranges fail closed. The default complete-read budget is 2 MiB, maximum 8 MiB. A single line larger than the per-read budget is explicitly rejected. file(host).index(path,{name,max_bytes}) performs the complete read and explicitly indexes it (content source limit remains 2 MiB), returning only metadata. Source permissions are retained. File hashes are separate from editing line IDs.

FTS5 snippets are centered on actual query matches and bounded to 512 UTF-8 bytes. Complete-file acceptance: 920,023 bytes with CRLF and Unicode, exact SHA-256 match, indexing and a later independent exec search finding the last marker. Existing file edits and content regressions remain green.
