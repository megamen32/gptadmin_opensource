# Feedback loop

The feedback loop turns support and incident evidence into bounded roadmap
changes without inventing adoption metrics. Every report should identify the
affected path, preserve an immutable evidence reference and say explicitly
when there is `no_data_yet`.

## Intake

- Bugs use the bug template's reproduction steps, environment, product signal
  and immutable evidence fields.
- Features describe the problem first, then classify the signal as activation,
  support or incident. A feature without an observed signal is valid, but its
  status must remain `no_data_yet` until evidence exists.
- Never request secrets, raw customer data or unredacted private URLs in an
  issue. Keep sensitive evidence in the approved private incident channel and
  link only a sanitized immutable artifact.

## Design partner loop

For each design partner, record only consented, aggregate observations:

1. Choose one supported path from `docs/DOCUMENTATION_MAP.md`.
2. Run the documented harmless action and record whether activation completed.
3. Record support friction and incidents separately from retention; a single
   successful smoke test is not a retention measurement.
4. Link the sanitized run artifact, summarize the decision and assign one next
   action. Do not publish names, private paths or credentials.

## Quarterly review

Create one review record per quarter with this minimal table:

| Signal | Evidence reference | Result | Status |
| --- | --- | --- | --- |
| Activation | immutable run or `no_data_yet` | first connection and harmless action | `no_data_yet` until observed |
| Retention | aggregate, consented data or `no_data_yet` | repeat use over the agreed window | `no_data_yet` until measured |
| Support | issue IDs and sanitized evidence | recurring friction and owner | `no_data_yet` if no reports |
| Incident | incident ID and recovery artifact | severity, recovery and follow-up | `no_data_yet` if none |

The review may change the roadmap only when the evidence reference is
immutable and the resulting decision has one owner and one next action. The
repository currently provides the process and templates; it does not claim
real design-partner, activation, retention, support or incident outcomes.
