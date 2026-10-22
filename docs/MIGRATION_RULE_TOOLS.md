# Migration rule for management tools

## The rule

**Do not disable an existing management tool before the replacement has been
verified through a real client and an independent recovery path exists.**

This rule exists because of a concrete failure. A release retired the public
direct tool execution (`execute`) while it kept the node-side handler. The
handler was still there, but the tool was no longer in the catalog a client
loads. ChatGPT refuses to call a tool that is not in the loaded catalog, even
when the server would have handled it. The result was a release that looked
complete on the server and was unusable from the client that mattered.

The node-side test suite did not catch it either, because the tests were
written against the new behaviour. The product contract changed without the
acceptance criteria changing with it.

## What the rule requires before a tool is disabled

1. **A real client has loaded the new contract.** The replacement must be
   present in the catalog that the actual client loads, and the client must
   have called it successfully. A node-side handler, a `tools/list` answer or
   a unit test is not a client.
2. **An independent recovery path exists.** Rolling back must not depend on
   the component being replaced. The Node package manager keeps every installed
   version and can switch back to the previous one; that path must be verified
   before the switch, not after a failure.
3. **The acceptance criteria were updated in the same change.** If the tool is
   being removed or renamed, the tests that describe the contract change with
   it, in the same commit. A test suite that was already failing before the
   change is not evidence of anything — fix the baseline first.
4. **The instruction set says what actually exists.** The startup instructions
   must not describe a tool as retired while the server still handles it, and
   must not describe a tool as available while the catalog omits it.
5. **The retirement is detectable from the client side.** A client that still
   calls the old spelling must receive a deterministic, documented answer that
   names the replacement, instead of a transport-level "unknown tool" that the
   client cannot act on.

## What the rule requires before a tool is *restored*

The same rule applies in reverse. A restored tool must not be a hidden
handler:

- it must be reachable in a catalog that the client actually loads;
- the way it is published must be chosen **explicitly** and be verifiable by
  the operator, not inferred from a user-agent or a connection shape;
- every entry point the product promises to work must stay in the catalog, and
  a change must never remove a working entry point.

## How it is applied in this repository

- The public tool catalog has **one** explicit surface. An ordinary caller
  loads `discover search schema exec ro job expand`; the Apps SDK / full
  catalog adds the administrative and reader tools; a read-only identity loads
  `ui resource_receipt discover demo schema inspect job expand search ro`. The
  surface is not selected by a mode switch and never guessed from a request
  header — there is no `tool_catalog_mode` setting any more.
- The direct `execute(target, tool, args)` facade is **not published to
  anybody**. A caller that still sends it receives a deterministic answer
  instead of a silent miss:

      HTTP 200, no JSON-RPC error
      structuredContent = {
        "status": "failed",
        "error": {
          "code": "execute_facade_retired",
          "message": "the direct execute facade is no longer public; use search, then exec for writes and ro for reads"
        }
      }

  The migration path is therefore observable from the client side, which is
  what rule 5 asks for.
- The working `exec` and `ro` facades are present for every caller that is
  allowed to use them, and the task readers `job` and `expand` stay published,
  because a client must be able to read the result of a task it started.
- The code-only spelling `execute({code})` keeps working as the old transport
  form of `exec`; a call that mixes `code` with direct routing arguments is
  refused before anything runs.
- A read-only client is never offered a write-capable routing facade.
- The internal dispatcher (`executeMCPTool`), the authenticated
  `/admin/api/mcp/execute` endpoint and the node-peer transport stay exactly as
  they are, with their ACL, ownership, idempotency and drain behaviour. They
  are not the public client surface, and they are what the internal consumers
  (`ro`, `exec`, relay, agent routes, peer hops) depend on. Keeping them is
  also the independent recovery path the rule asks for: a rollback does not
  depend on the retired public spelling.
- Every installed Node version stays on disk and the manager can switch back
  to the previous one, so a bad release has an independent recovery path.
