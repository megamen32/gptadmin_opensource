# Вернуть строгие Bearer/OAuth проверки

## Symptom

`GPTADMIN_RELAX_AUTH_CHECKS=1` временно отключает дополнительные проверки, чтобы восстановить работу разъехавшегося canonical ingress/auth-state.

## Deferred checks

После восстановления общего auth-state и canonical ingress вернуть под feature gate и покрыть live-canary:

- expiry/`exp` и managed-token `ExpiresAt`;
- revocation state;
- issuer, audience и resource binding;
- scope, subject, issued-at и key-id claims;
- OAuth authorization-code TTL and client/redirect binding;
- PKCE challenge/verifier validation;
- refresh-token expiry and rotation checks.

## Acceptance

Сначала подтвердить, что canonical `u-f...` и legacy/secondary ingress используют согласованный auth-state; затем выключить relaxed mode и повторить Custom GPT OAuth, Bearer MCP и BrowserClaw canaries.
