# Custom GPT OAuth container proof — 2026-08-03

Status: complete
Classification: Direct

## Original request

Verify whether the OAuth flow works specifically for Custom GPT.

## Objective

Run the Hub's Custom-GPT-compatible OAuth Authorization Code + PKCE proof in an isolated container and report its boundary.

## Business canary

OAuth authorize and token exchange issue an access token accepted by the protected Hub endpoint.

## Confirmed scope

Container-only test and report; no source or production changes.

## Explicit exclusions

No real ChatGPT browser session, deployment, or patch.

## Initial estimate (immutable)

- Optimistic: 4 active minutes
- Likely: 8 active minutes
- Pessimistic: 18 active minutes

## План (русский)

1. Найти тест OAuth с redirect URI ChatGPT.
2. Запустить его в неизменяемом контейнере.
3. Разделить доказанный OAuth-контракт Hub и непроверенный внешний UI ChatGPT.

## Progress (English)

- 2026-08-03: container OAuth proof started; no patch applied.
- 2026-08-03: Docker `golang:1.24` ran `TestOAuthAndMCPJSONRPC` successfully from the immutable repository mount. It performs Authorization Code + S256 PKCE using `https://chatgpt.com/connector/oauth/cb`, exchanges the code, then authenticates `/mcp` with the issued access token.
- 2026-08-03: strict redirect policy also explicitly permits HTTPS `chatgpt.com` / subdomain redirect paths beginning `/connector/oauth/`; strict resource validation requires exact `MCP_RESOURCE`.

## Self-improve retrospective

- What slowed or confused L? The OAuth integration test enables permissive flags even though strict redirect code has a ChatGPT allowlist.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One distinction between unit-flow proof and external ChatGPT UI proof; guard: label the boundary explicitly.
- State: fixed now (container proof); external UI remains out of scope.
