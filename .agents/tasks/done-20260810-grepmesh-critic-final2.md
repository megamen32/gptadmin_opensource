# GrepMesh final adversarial gate rerun

Role: Critic

## Immutable objective and handoff claim

The selected Normal goal implements local GrepMesh MCP plus direct peer
federation, persistent per-root index/watchers, cached/periodic topology, and a
minimal GPTAdmin control-only projection. The handoff claim is local canary
readiness only. Five-host enrollment, production auth/mTLS/firewall/ACL,
restart/deploy, public bind, and official `rmcp` crate wiring remain separate
gates.

## Done condition under audit

The final user-surface proof must use an independent MCP CLI consumer, send a
`search_text` request with the `hosts` field omitted entirely, capture the raw
consumer-visible response containing local and peer canaries, discover paths,
read a remote file, and after peer failure capture local results plus
`partial=true`. It must not replace the consumer with curl or a hand-written
HTTP script.

## Current implementation evidence

Attribution base: `HEAD=073ed2b12ecabb65b4d15755fd3be350adc5adb9`.
Selected GrepMesh plus seam aggregate SHA-256:
`da84cf31b3b9f7e9b9653f91162035236ec1927e4b10bc0c0a2afc9d5463a3db`.
`cargo clippy --all-targets -- -D warnings`, `cargo test --all-targets`, and
`go test ./...` in `go-hub` passed. Rust totals are 4 unit, 7 index, 3 mesh,
3 search/root, and 10 topology-cache tests. The fresh Reviewer rerun returned
`APPROVE` and verified the three previous correctness fixes.

The prior Critic `RETHINK` was only that the Tester assignment did not state
the omitted-host request and raw independent-client evidence strongly enough.
The Tester task is now explicitly amended with both requirements and the same
production exclusions.

## Proposed next action

Run the amended fresh `only-new` CLI Tester. If it returns `PASS`, hand off the
local implementation with the exclusions above; if no independent MCP CLI
consumer exists, preserve the Tester `STOP_MISSING_REAL_SURFACE` verdict.

## Explicit exclusions

No implementation edits, production mutation, deployment, secret use,
security rollout, or unrelated GPTAdmin audit.

Append decisive evidence, questions, alternatives for any non-PASS route, and
exactly one verdict: `PASS`, `RETHINK`, `STOP`, `STOP_SCOPE_DRIFT`, or
`STOP_MISSING_CONTEXT`.

## Critic decision receipt — 2026-08-10

### Independently reconstructed done condition

This is not a test-suite completion claim.  The local-canary handoff can be
made only after a fresh, independent MCP CLI consumer has (1) submitted
`search_text` with `hosts` absent from the request, (2) exposed its raw
response showing both the local and peer canaries, (3) discovered the returned
paths, (4) read a peer-owned file, and (5) after the peer is made unavailable,
exposed local results together with `partial=true`.  Curl, a hand-written HTTP
client, and an in-process facade cannot establish that consumer contract.

### Decisive evidence and failure-domain check

- The task explicitly limits the claim to local canary readiness.  It does not
  infer enrollment, authentication, network policy, public exposure, restart,
  or deployment readiness from the local evidence.
- The current automated evidence is useful regression coverage but is correctly
  not offered as the final business-path proof.
- The proposed fresh `only-new` Tester gate names both prior missing
  requirements: omission (not an empty value) of `hosts`, and preservation of
  the raw response from an independent MCP CLI consumer.
- Its stated negative route is fail-closed: absence of a qualifying consumer
  must remain `STOP_MISSING_REAL_SURFACE`, rather than being substituted with
  curl or a custom script.  This excludes the principal proof-quality failure
  mode from the earlier audit.

### QUESTIONS_FOR_L

None before the proposed Tester run.  The Tester receipt must nevertheless
identify the executable/client invocation and include the raw responses for
both the healthy-peer and failed-peer cases; otherwise it does not answer the
contract and must not be treated as PASS.

### Verdict

PASS
