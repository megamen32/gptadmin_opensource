# Web token Custom GPT live canary — 2026-08-03

Status: complete
Classification: Direct

## Original request

Verify whether the supplied web-issued Bearer works as a Custom GPT credential.

## Objective

Run a read-only public Hub canary with the supplied credential while preventing secret output or persistence.

## Business canary

Bearer-authenticated relay discovery and a non-mutating Custom GPT Action return success at the public origin.

## Confirmed scope

Read-only public requests only; no source, service, token, or Custom GPT mutations.

## Explicit exclusions

No tool execution, no token revocation, no configuration patch, no secret output.

## Initial estimate (immutable)

- Optimistic: 3 active minutes
- Likely: 6 active minutes
- Pessimistic: 12 active minutes

## План (русский)

1. Проверить Bearer на публичном relay discovery.
2. Выполнить безопасный Action discovery без побочных эффектов.
3. Сообщить результат и необходимость отзыва опубликованного токена.

## Progress (English)

- 2026-08-03: public read-only canary started; token will not be printed or written.
- 2026-08-03: public `GET /mcp-relay/servers` with the supplied Bearer returned HTTP 200 and JSON key `servers`; public `/actions/openapi.yaml` returned HTTP 200. The schema exposes `/mcp-relay/servers`, `/mcp-relay/tools`, `/mcp-relay/call`, and job polling, so the successful relay discovery is a real Custom GPT Action-path canary.
- 2026-08-03: attempted `/actions/discover` returned 404 because it is not a schema operation; this is expected and does not affect the verified Action route.

## Self-improve retrospective

- What slowed or confused L? The global Action schema uses relay paths, not a `/actions/discover` endpoint.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One endpoint-assumption correction; guard: inspect live OpenAPI paths before invoking an Action canary.
- State: fixed now (live canary); exposed token needs human-authorized revocation.
