# Profile instructions and compact MCP results

## Client workflow

Use `discover` or `schema(target, tool)` before the first operation in an agent's
current context. The response contains the effective bound profile's full
`instructions` and a `context` receipt. A selected schema returns one `call_ref`
which carries that context into `execute({ref, args})`.

GoHub remembers that document was delivered to the same authenticated
credential, owner and profile. After the first delivery, omit `context` from
later calls; GoHub reuses its receipt without resending the document or token.
The in-memory receipt is refreshed while used, expires after two idle hours, and
is invalidated when the profile or instruction document changes. It is not an
account-wide or profile-wide read flag.

A missing, expired or outdated receipt rejects a new operation before submission:

```json
{
  "status": "failed",
  "executed": false,
  "error": {"code": "instructions_required", "message": "Read the attached profile instructions..."},
  "instructions": "The current effective profile document...",
  "context": "ic1_..."
}
```

Read that document and make the intended, unexecuted call again. Include the
returned context only if the Hub still requests it, or fetch a new exact schema.
No extra acknowledge/initialization call is needed.
A timeout, dropped connection or failed tool is different: inspect the existing
job/operation receipt before replaying it.

**A delivery receipt does not prove understanding or compliance by a model.**
It proves that the exact current document was issued to the authenticated caller
and its receipt was returned. Keep the actual instruction text with the receipt.
After compaction/task transfer, fetch again if the text was lost. A client cannot
reliably infer a conversation boundary from OAuth identity or MCP transport alone.

## Transport and lifetime

`context` is a GPTAdmin extension, not a new MCP standard requirement. Routing
facades advertise it as an optional top-level argument. Direct native tools with
strict business schemas can carry it in MCP `params._meta["io.gptadmin/context"]`
or the `X-GPTAdmin-Context` HTTP header. Reserved outer control metadata is removed
before downstream tool arguments; unrelated metadata/business fields remain.

Delivery receipts expire after two idle hours. They bind the caller's bearer,
owner and profile, and the
exact instruction-set ID and content digest. Rule edits, profile changes and
credential changes therefore require fresh delivery. Schema references remain
scoped to their existing caller/operation store; an expired reference must be
refetched. The separately signed context is portable between nodes with the same
signing authority and effective profile. It is domain-separated from OAuth,
contains no authorization scopes, and cannot authenticate a request.

The existing OAuth signing key (or configured control key fallback) is used
internally; no extra key store is created. The public client never receives the
key. Missing selected instruction documents fail closed, not to a different
profile. A spoofed peer-hop header is not an exemption.

## Execution and recovery boundaries

The delivery gate runs before execution admission, approval consumption or
idempotency reservation in agent-facing execution paths. It covers the ordinary
facade, Code Mode's nested calls, fleet, direct MCP/Actions and task acceptance.
Policy, connection ownership, approval and bounded-autonomy checks still apply;
a context receipt grants no extra permissions.

Reading existing jobs, expanding retained operations, secret status and cancelling
or declining existing work stay usable without refreshed instructions. Resuming
an approved task is a new execution step and requires a current context. Calls
already accepted by an external executor retain their ordinary lifecycle; a
context change does not retroactively undo an effect.

Human operator dashboard handlers and deterministic configured webhook actions
are not LLM instruction consumers. Narrow private in-process markers preserve
these existing workflows after their normal authentication/policy checks.
CloudOS browser compatibility requires a valid admin cookie and no bearer
Authorization header. Bearer CloudOS operations still require context. External
headers/profile fields cannot enable the internal marker.

## Output contract

Only GPTAdmin-owned successful wrappers are compacted. Upstream business objects,
media, errors, partial output, nonzero exits, pending approvals and running tasks
retain their required fields. MCP-level protocol envelopes remain intact, and
instruction refusals are marked as tool errors rather than successful values
that violate a strict output schema.

`schema(target, tool, detail="full")` means the full schema of that exact tool,
not every tool in the target. Its `call_ref` also works with `expand`; the duplicate
`operation_id` is omitted. A pending job retains its job ID; a second diagnostic
ID is not added when the existing receipt already suffices.

A completed, untruncated shell response keeps the exit code and useful output.
A successful compact Code Mode response keeps the selected `result`, not its
full limits/timing wrapper. Pruned diagnostics remain available by `expand` or
`detail="full"`. Default compaction is not a claim that the underlying job/audit
store stopped retaining intermediate data.

A measured candidate example returning `{"answer":42}` produced 65 bytes in
compact output versus 236 bytes in full output, including its diagnostic handle.
This is JSON response size, not a universal token-saving claim. Initial instruction
delivery adds its own document and receipt; retained contexts avoid that repeat.

## Configuration, packaging and validation

`GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED=1` is the real runtime `FromEnv` default.
An operator may set it to `0` as a temporary migration/rollback option; it does not
change existing authorization/approval gates. Programmatically constructed Go
`Config` retains its zero-value false for embedded callers and legacy unit tests;
new context tests and the real executable canary explicitly enable the gate.

Plugin package 1.3.0 includes Agent Plugins 1.0 `plugin.json`/`mcp.json`, plus the
existing Codex-specific files. The workflow is a thin loader, not a duplicated
mutable profile. It does not rewrite `AGENTS.md`, and packaging alone does not
claim that every already-running native client has reloaded the skill.

Run the real isolated executable canary against a built candidate:

```sh
GOMAXPROCS=2 go build -C go-hub -p 2 -o "$PWD/.tmp/instruction-context/bin/gptadmin-node" ./cmd/gptadmin-node
python3 scripts/test_instruction_context.py --binary "$PWD/.tmp/instruction-context/bin/gptadmin-node"
```

The script starts a separate loopback Node with its own temporary state and
credentials, issues real bearers, uses its embedded ShellMCP and real browser
login, checks actual side effects, and stops only that candidate. Its report is
`.tmp/instruction-context/lead-report.json`. It never copies production credentials
or stops the production service. Add `--expect-commit <sha>` to bind release
acceptance to the embedded build identity.
