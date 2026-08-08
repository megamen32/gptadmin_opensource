# Web-issued JWT container proof — 2026-08-03

Status: complete
Classification: Direct

## Original request

Before any patch, verify quickly in an isolated container whether a token issued by the web UI/API is accepted by Hub; state what to tell the reporter.

## Objective

Obtain a reproducible black-box comparison between `/admin/api/mcp/issue-token` and CLI `issue-token` against one Hub process.

## Business canary

The web-issued Bearer is accepted by `/mcp-relay/servers` in the same isolated Hub instance.

## Confirmed scope

Container-only verification and a response draft. No source, service, or production configuration changes.

## Explicit exclusions

No patch, no deploy, no key rotation, no external Custom GPT changes.

## Initial estimate (immutable)

- Optimistic: 5 active minutes
- Likely: 12 active minutes
- Pessimistic: 25 active minutes

## План (русский)

1. Найти существующий контейнерный тестовый маршрут Hub.
2. Выпустить токен через web API и через CLI из одинаковой env-конфигурации.
3. Проверить оба токена на защищённом relay endpoint и сравнить claims без публикации секретов.

## Progress (English)

- 2026-08-03: isolated proof started; no patch applied.
- 2026-08-03: Docker `golang:1.24` ran `TestAdminIssueMCPTokenUsesPublicOriginAndWorksForRelay` successfully against the immutable repository mount. The web API returns a managed `gptk_<id>_<secret>` bearer, and the test verified HTTP 200 on `/mcp-relay/servers` with it.

## Self-improve retrospective

- What slowed or confused L? Web and CLI issuance use different token formats despite similar naming.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One API-versus-CLI compatibility discrepancy; guard: retain the container regression test and add a CLI parity test if patching.
- State: fixed now (proof); implementation needs human decision.
