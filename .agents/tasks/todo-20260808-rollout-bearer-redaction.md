# Follow-up: rollout config output redacts bearer fields

## Symptom

The HAOS rollout script's config preview redacts keys matching token/secret/password/key but not `*_bearer`, so bearer values can appear in local command output.

## Smallest evidence

Observed during the authorized rollout preparation on 2026-08-08; the generated config preview included bearer-valued fields while other secrets were masked.

## Blocker / scope

Not changed during the rollout recovery. Requires a narrow redaction fix and a regression test for bearer/oauth/access-token field names before the next release.
