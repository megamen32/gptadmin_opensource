# GrepMesh adversarial completion audit

Role: Critic

## Assignment

Independently audit whether the selected Normal GrepMesh goal and current
evidence justify completion or handoff. Reconstruct the real done condition
from this task file and the implementation evidence. Do not implement fixes,
do not direct the route, and do not inspect unrelated dirty work.

## Objective and business canary

An AI agent connects to one local GrepMesh MCP, uses default `hosts="*"`, the
local node directly fans out to known peers, results retain host and absolute
path, remote `read_text` works, one failed peer yields a useful partial result,
and topology discovery failure falls back to cached/static peers.

## Current evidence and known gates

Rust and Go full suites are green. A two-process black-box canary proves
modern-header MCP calls, wildcard search, path discovery, remote read, and a
failed-peer partial search. Persistent indexes/watcher, named roots, topology
cache, periodic refresh code, and a read-only GPTAdmin projection are present.
Production five-host enrollment, authentication/mTLS/firewall/ACL, and official
`rmcp` transport wiring are explicitly not claimed as completed.

## Explicit exclusions

No production mutation, no deployment approval, no unrelated architecture
audit, and no implementation changes.

Append decisive evidence, `QUESTIONS_FOR_L` when needed, alternatives for any
non-PASS route, and exactly one verdict: `PASS`, `RETHINK`, `STOP`,
`STOP_SCOPE_DRIFT`, or `STOP_MISSING_CONTEXT`.

## Critic audit — 2026-08-10

### Reconstructed done condition

Completion requires a reproducible, black-box MCP proof that a default
`hosts="*"` local node fans out to known peers; search results preserve each
peer's host and absolute path; `read_text` can read a remote result; one failed
peer still returns a useful partial result; and failed topology discovery uses
cached or static peers. The proof must identify the tested revision and the
actual two-process topology. Green language suites and the existence of code
cannot substitute for that outcome.

### Decisive evidence

- The task contains only assertions that suites and a two-process canary are
  green. It provides no command transcript, test/canary artifact, revision,
  peer configuration, request/response samples, or failure injection evidence.
- It does not identify L's proposed next action (completion, handoff, or a
  further gate), so the safety and adequacy of that action cannot be audited.
- The claimed canary could establish the selected local Normal goal, but the
  listed production exclusions mean it cannot establish five-host rollout,
  security controls, or official `rmcp` transport completion. Those exclusions
  are acceptable only if the handoff/completion claim stays explicitly local.

### Excluded hypotheses

- No evidence in this record shows that production rollout was attempted or
  that it is required for the selected local Normal goal.
- No evidence in this record shows that a failed peer should make the overall
  query fail rather than return a partial result; the stated objective requires
  the latter.

### QUESTIONS_FOR_L

1. Provide the immutable evidence delta: tested revision, exact black-box
   canary command/output or durable artifact, two-process peer topology, and
   captured proof for every stated business-canary clause, including injected
   peer failure and topology-discovery fallback.
2. State the exact proposed next action and the precise claim it would make;
   confirm it does not imply production enrollment, security hardening, or
   `rmcp` transport completion.

### Alternatives

1. Treat the current work as an evidence-collection handoff: preserve the
   local implementation claim, run or cite the bounded canary, and return for
   a fresh audit before declaring the selected goal complete.
2. If the durable canary cannot be produced, hand off only a clearly labelled
   implementation/status report with the business canary unproven; do not make
   a completion claim.

### Minimum proof to proceed

An attributable black-box artifact at a named revision that shows all five
local user outcomes, plus the intended completion/handoff wording and explicit
scope boundary.

**VERDICT: STOP_MISSING_CONTEXT**
