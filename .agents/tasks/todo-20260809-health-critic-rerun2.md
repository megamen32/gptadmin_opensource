# Fresh Critic gate 2: adversarial health incident contract

Role: Critic
Status: todo
Owner: fresh independent Critic gate 2
Parent task: work-20260809-health-monitoring-orchestration.md

Fresh adversarial read-only pass. Do not edit product code, secrets, runtime
state, or external systems; do not send Telegram. Append a concise PASS or
RETHINK verdict with exact reproductions.

Try to falsify the durable contract with bounded local evidence:

- race plan selection from two independent NoticePlace instances;
- select before plans are attached;
- submit empty heartbeat-only progress and attempt resolution;
- preserve an original fingerprint, then submit a different healthy one;
- inject a 10,000-character Hermes session actor and `token:value` fields;
- issue GET on the Hub progress endpoint;
- inspect canary provenance and verify no external send is claimed.

Use only the same selected paths named in the Reviewer gate 2 task. No
production restart/deploy, secret changes, or external delivery.

## Critic result (final bounded pass, 2026-08-09)

Verdict: RETHINK

Decisive evidence:

- Signed selection before plan attachment was rejected with `ValidationError:
  unknown health plan`. Two independent `NotificationCenter` instances raced
  different signed callbacks; exactly one durable winner (`repair`) remained
  and the durable selection count was `1`.
- Normalized intake preserved `source_fingerprint=orig-fp`; a healthy
  verification carrying `different-fp` was rejected at resolution with
  `health verification receipt must match the original source fingerprint and
  remain independent`.
- The required current-code heartbeat falsification used
  `step=heartbeat`, empty evidence/fingerprint, and `heartbeat_at=1`. The
  NoticePlace loopback endpoint returned HTTP `200` and persisted the receipt;
  its response incorrectly reported `useful_progress=true` while the durable
  value was empty. A matching healthy verification could not resolve the
  incident (`400`, useful progress required), but accepting and persisting the
  heartbeat violates the stated API contract and does not prove the claimed
  fix is active in this checkout.
- Hermes rejected `token:value` in plan/step/fingerprint/correlation/evidence
  fields and rejected a 10,000-character resolution session actor, but the
  health-progress bridge emitted successfully with a 10,000-character session
  actor; captured wire data had actor length `10000`. The bounded receipt
  boundary therefore remains incomplete.
- Hub progress checks passed: duplicate progress was non-useful, terminal
  updates were rejected, and GET `/webhook-jobs/{id}/progress` returned `405`
  without mutation. The disposable vertical canary reported one incident,
  three plans, useful progress `[true,false]`, resolved status, explicit
  `real_local` versus `fake` transports, and `external_sends=false`.
- Focused suites were green: health producer `5 passed`, NoticePlace health/API
  `26 passed`, Hermes bridge `10 passed`, Agent Herder progress/API `4 passed`,
  and the selected Hub tests passed. These are supporting checks only; they do
  not override the two contract violations above.

QUESTIONS_FOR_L:

- The claimed heartbeat fix is not observable through the selected public
  NoticePlace endpoint in the final bounded reproduction. Completion is blocked
  until the current consumer path actually rejects that receipt and the
  response/persistence semantics agree.

Excluded hypotheses and scope:

- No production restart/deploy, secret change, external delivery, or Telegram
  send was performed. GPTAdmin/OmniRoute/Agent Herder/Telegram in the canary
  were deterministic fakes, so it is not live downstream proof.
- The race, pre-attachment guard, normalized fingerprint guard, Hub read-only
  behavior, and canary provenance were independently bounded; no unrelated
  repository paths were investigated.

Two safe routes:

1. Apply the bounded NoticePlace heartbeat rejection and Hermes progress-actor
   bound in the selected paths, then rerun this exact adversarial set and the
   business canary.
2. Keep the health incident release/completion claim blocked and document the
   two known gaps; do not claim the full business canary until current-path
   evidence passes.

Minimum proof to proceed: HTTP rejection (and no persisted event) for the
heartbeat-only receipt with `step=heartbeat`, empty evidence/fingerprint, and
`heartbeat_at`; plus a bounded Hermes progress wire receipt that cannot carry
an oversized session actor, followed by a fresh canary with the same explicit
transport provenance and no external send.
