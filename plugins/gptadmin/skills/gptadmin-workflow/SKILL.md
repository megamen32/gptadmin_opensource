---
name: gptadmin-workflow
description: Use GPTAdmin for server or child-MCP work; obtain the effective profile instructions once and reuse its current context with compact results.
---

# GPTAdmin workflow

Before ordinary GPTAdmin work, call `search` with a specific query and bounded
filters supported by its schema. Read the returned `instructions` and use the
result's argument schema plus short JavaScript example or scoped handle. The
authenticated connection selects the profile; do not invent or substitute it.
Keep the instruction text with its `context`. Run the operation through
`exec({code})`, using `call(target, tool, args)` or the returned handle.
Aggregate, filter, and count in JavaScript, then explicitly return only the
selected result. Do not use direct public `execute`; it has been retired.

`discover` and `schema` remain read-only compatibility tools for cases where
their target/schema detail is needed. `ui`, `job`, and `expand` remain available
for the interface and existing operation receipts. Reuse the current context
when refreshing read-only search/discovery/schema results.

`instructions_required` with `executed:false` means this attempt did not start.
Read its updated instructions and use the returned context, or refresh search or
the exact read-only schema. A timeout or missing response is different: inspect
the existing `job` or `expand` receipt before retrying. Never replay a write
whose outcome may already have been accepted.

After compaction or switching tasks, retain both the instruction text and receipt.
If the text is no longer available, omit context and obtain it again. A receipt
without the text is not a substitute for reading. Instructions guide the workflow;
user intent, permissions and approvals remain authoritative.

Default responses are compact. Use `detail:"full"` or `expand(id)` only for needed
diagnostics. A search result may supply a scoped handle that avoids another
schema lookup. `expand` inspects an existing receipt/result and never re-executes it.
Never remove an error, pending approval, partial-execution warning or output
continuation from your interpretation merely because the rest is concise.

When the effective profile names a memory MCP, use that exact authorized target
for relevant reads. Write memory only when the user or existing workflow grants
it. A missing memory service is not permission to switch providers or export data.

The plugin does not copy full profiles into project files, install a global
"already read" flag, or overwrite `AGENTS.md`. Do not add these behaviours.
Never record passwords, bearer tokens or private keys in the task or memory.
