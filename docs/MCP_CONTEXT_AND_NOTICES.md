# Low-context MCP and required migration notices

This is the implementation contract derived from
[`PHILOSOPHY.md`](./PHILOSOPHY.md). It synthesizes the proposer and adversarial
review: preserve the current compact Hub routing contract and add notices as a
small control-plane feature, not a protocol rewrite.

## What stays unchanged

The global `/mcp` endpoint keeps its small deterministic facade:

1. list explicit servers;
2. list tools for one selected server;
3. call one selected tool;
4. read a background job;
5. render the optional dashboard.

Full native schemas remain available through a pinned server endpoint when a
client genuinely needs them. The Hub must not replace this with generic
`capability_*` tools until measurements across supported clients prove a real
benefit.

## Connection identity

New client credentials contain both:

- `connection_id`: stable across renewal and rotation;
- `jti`: unique to one JWT and used as delivery or request evidence.

Notice and policy state keys use `(campaign_id, connection_id)`. Existing JWTs
without `connection_id` may temporarily use `jti`; this is a bounded migration,
not a permanent second identity model.

## Notice delivery

V1 stores a small durable ledger in the existing managed Hub state:

```text
campaign_id
connection_id
status
last_offered_at
acknowledged_at
deferred_until
satisfied_at
evidence[]
```

The Hub may include one compact `required_notices` bundle after the normal
`list_mcp_servers` result when all conditions hold:

- a campaign applies to this connection;
- it is not satisfied or currently deferred;
- this active Hub has not offered a bundle to the connection in the previous
  rolling 24 hours.

The bundle contains IDs, short titles and short user messages. Full migration
instructions are loaded only by an explicit Hub tool call. No notice is added
to command output, job polling or unrelated calls. Tool definitions stay
stable throughout the session.

V1 guarantees at-most-once-per-24-hours on one active Hub. Cross-Hub delivery
deduplication requires replicated control-plane state or a fenced primary
writer and belongs to V2; the product must not claim global exactly-once before
that exists.

## Agent protocol

The full instruction returned for a due notice tells the AI:

```md
Read the notice and explain the required user action in plain language.
Do not request or repeat passwords, tokens, browser sessions or private keys.
Do not claim completion from this instruction alone.

After explaining it, call the Hub tool `ack_notice` with the notice ID and a
short summary of what you told the user. If the user's current task is urgent,
call `defer_notice` instead; the current task must continue normally.
```

`ack_notice` is a stable tool of the explicit `hub` target, reached through the
existing `call_mcp_tool` facade. Acknowledgement changes the state to
`waiting_for_verification`, never directly to `satisfied`.

## Verification

V1 supports a small fixed set of Hub evaluators, not arbitrary scripts or a
general predicate language:

- `credential_issued`;
- `credential_used`;
- `legacy_credential_inactive`;
- `endpoint_success_observed`;
- `client_version_at_least`.

For a Custom GPT migration from a legacy credential to JWT, verified completion
requires all of:

1. a new credential issued for the intended `connection_id`;
2. a successful request observed with its `jti` on an expected endpoint;
3. the legacy credential revoked or expired.

A request from another credential, CLI test or unrelated endpoint does not
close the campaign. If the Hub cannot observe the required fact, it reports
`manual_confirmation_required` instead of inventing success.

## Notice safety

Notices are control-plane data and therefore a prompt-injection boundary.

- Plain text only with strict byte and line limits.
- No raw secrets, credential requests or arbitrary shell/browser commands.
- Links are limited to the canonical Hub origin or signed project docs.
- Built-in campaigns come from a signed release manifest; local campaigns
  require an authenticated administrator and are visibly marked local.
- The AI follows the typed completion rule, never an instruction embedded in
  the message body.

## Context-budget contract

V1 uses deterministic byte and item limits because client tokenizers differ.
Approximate token counts are telemetry, not an interoperability requirement.

- Stable global tools only; no session-specific tool additions.
- Default list page is small and deterministically ordered.
- One selected server schema set is loaded at a time.
- Notice bundle is short and appears no more than daily by default.
- Full notice detail is loaded only after selection.
- Large results are bounded, marked `truncated`, and stored through the existing
  output path for explicit retrieval.
- The same full payload is not duplicated in text and structured output.

Before changing the current facade, capture a client matrix baseline for Codex,
Claude Code, OpenCode and VS Code: serialized `tools/list` bytes, schemas loaded,
tool-call count, input tokens where observable, and time to first useful call.

## Delivery stages

### V1

1. Add `connection_id` and preserve it through JWT rotation.
2. Add the local durable notice ledger and daily lease.
3. Add stable Hub tools for notice detail, acknowledgement and deferral.
4. Add the five fixed evaluators and evidence records.
5. Add admin visibility for due, deferred, verifying and satisfied campaigns.
6. Add language-neutral and black-box tests.

Required tests cover daily deduplication, parallel sessions, restart recovery,
identity across JWT rotation, acknowledgement without false completion,
credential-specific evidence, non-blocking urgent calls, bounded output and a
client with a cached static tool list.

### V2, only after V1 measurements

- Replicate notice state across failover Hubs or enforce one fenced writer.
- Unify durable jobs, audit, credential and notice storage if operational data
  justifies a database migration.
- Add bounded artifact retention and schema digests.
- Add stable connection profiles only where the client matrix demonstrates a
  material context reduction.
- Expand protocol revision support only with a conformance matrix.

