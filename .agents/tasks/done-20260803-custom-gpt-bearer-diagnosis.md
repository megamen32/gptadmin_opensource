# Custom GPT Bearer diagnosis — 2026-08-03

Status: complete
Classification: Full (authentication contract and production configuration ambiguity)

## Original request

Determine why `gptadmin issue-token custom-gpt-test --no-save` yields a Bearer token rejected by Hub with audience/resource/signature errors, and provide the official Custom GPT Actions setup and safe diagnostics.

## Objective

Produce evidence-backed, secret-safe diagnosis and exact remediation/diagnostic commands for the installed GPTAdmin Hub and CLI.

## Business canary

A newly issued custom-GPT Bearer token is accepted by the intended Hub endpoint (`/mcp-relay/servers`) through the public origin.

## Confirmed scope

Read-only source and configuration-contract investigation. No secret disclosure, token publication, service restart, or production configuration mutation.

## Explicit exclusions

No deployment, no key rotation, no Custom GPT configuration change, no unrelated service audit.

## Initial estimate (immutable)

- Optimistic: 20 active minutes
- Likely: 45 active minutes
- Pessimistic: 90 active minutes

## План (русский)

1. Сопоставить выпуск `issue-token` с проверкой JWT в Hub и определить точные claims.
2. Проверить, какие env/config реально читает CLI и запущенный Hub, не выводя секреты.
3. Подготовить минимальную последовательность диагностики и настройки Custom GPT Actions.
4. Зафиксировать доказательства, ограничения и безопасный путь исправления.

## Progress (English)

- 2026-08-03: task created; read-only diagnosis started.
- 2026-08-03: source contract confirmed. CLI signs HS256 with `OAUTH_CLIENT_SECRET`; it emits `iss=PUBLIC_ORIGIN`, `aud=MCP_RESOURCE`, and does not emit `resource`. Hub requires both `aud` and `resource`, therefore a stock CLI-issued token cannot pass this Hub's verifier. This is a product compatibility defect, independently of any deployment drift.
- 2026-08-03: Hub only reads process environment. `PUBLIC_ORIGIN` determines the OAuth issuer; `MCP_RESOURCE` determines accepted audience and required `resource`; `OAUTH_CLIENT_SECRET` is the HMAC key. `HUB_PUBLIC_URL` is CLI-side input only.
- 2026-08-03: no production mutation performed. Prepared secret-safe diagnostics and recommended OAuth Actions path; task complete.

## Self-improve retrospective

- What slowed or confused L? Contract is split: `cli.py` emits `aud` but no `resource`, while `server.go` requires both.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One compatibility mismatch, evidenced by `make_mcp_bearer_token` and `verifyJWTForRequest`; guard: regression test for CLI-issued JWT against Hub.
- State: fixed now (diagnosis); implementation needs human decision.
