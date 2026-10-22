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

## What the rule requires before a tool is *restored*

The same rule applies in reverse. A restored tool must not be a hidden
handler:

- it must be reachable in a catalog that the client actually loads;
- the way it is published must be chosen **explicitly** and be verifiable by
  the operator, not inferred from a user-agent or a connection shape;
- the modern catalog and the compatibility catalog must both keep every tool
  that the product promises to work, and a mode switch must never remove a
  working entry point.

## How it is applied in this repository

- The tool catalog has two explicit modes, `tool_catalog_mode` in the typed
  Hub settings registry: `compat` publishes the compatibility `execute`
  facade next to the code-mode facades, `modern` publishes the code-mode
  facades and the search facade. The mode is an operator setting, it is
  readable through the same settings API the client already uses, and it is
  never guessed from a request header.
- The working `exec` facade is present in **both** modes. Switching the mode
  never removes a working entry point.
- The code-only spelling `execute({code})` keeps working as the old transport
  form of `exec`; a call that mixes `code` with direct routing arguments is
  refused before anything runs.
- A read-only client is never offered a write-capable routing facade, in
  either mode.
- Every installed Node version stays on disk and the manager can switch back
  to the previous one, so a bad release has an independent recovery path.
