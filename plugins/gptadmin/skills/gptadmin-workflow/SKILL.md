---
name: gptadmin-workflow
description: Use GPTAdmin for server or child-MCP work; obtain the effective profile instructions once and reuse its current context with compact results.
---

# GPTAdmin workflow

Before the first GPTAdmin operation in this task, call `discover` or the exact
`schema(target, tool)` that the work needs. Read the returned `instructions`.
The authenticated connection selects the profile; do not invent or substitute it.
Keep the instruction text with its `context`. A schema's `call_ref` already carries
that context; use `execute({ref, args})` for the next operation. Explicit target
calls use `execute({target, tool, args, context})`. Reuse `context` with subsequent
discovery/schema calls so the same instructions are not sent repeatedly.

`instructions_required` with `executed:false` means this attempt did not start.
Read its updated instructions and use the returned context, or get a new exact
schema. No separate acknowledgement command is needed. A timeout or missing
response is different: inspect the existing `job` or `expand` receipt before
retrying, and reuse the action's idempotency key where supported.

After compaction or switching tasks, retain both the instruction text and receipt.
If the text is no longer available, omit context and obtain it again. A receipt
without the text is not a substitute for reading. Instructions guide the workflow;
user intent, permissions and approvals remain authoritative.

Default responses are compact. Use `detail:"full"` or `expand(id)` only for needed
diagnostics. A complete schema uses one `call_ref`, which also works with `expand`.
Never remove an error, pending approval, partial-execution warning or output
continuation from your interpretation merely because the rest is concise.

When the effective profile names a memory MCP, use that exact authorized target
for relevant reads. Write memory only when the user or existing workflow grants
it. A missing memory service is not permission to switch providers or export data.

The plugin does not copy full profiles into project files, install a global
"already read" flag, or overwrite `AGENTS.md`. Do not add these behaviours.
Never record passwords, bearer tokens or private keys in the task or memory.
