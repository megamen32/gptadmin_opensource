# Custom GPT OpenAPI import warnings — 2026-08-03

Status: complete
Classification: Direct

## Original request

Diagnose Custom GPT importer warnings about ignored `X-GPTAdmin-Approval-ID` headers and multiple security schemes.

## Objective

Determine whether the warnings block the confirmed Custom GPT Action paths and identify the smallest schema correction.

## Business canary

The imported Action has exactly one compatible Bearer security scheme and retains safe relay operations.

## Confirmed scope

Read-only schema/source diagnosis. No schema or deployment patch.

## Explicit exclusions

No webhook mutation, no Custom GPT edit, no release.

## Initial estimate (immutable)

- Optimistic: 4 active minutes
- Likely: 8 active minutes
- Pessimistic: 18 active minutes

## План (русский)

1. Извлечь security schemes и проблемные операции из живой схемы.
2. Сопоставить их с ограничениями импортера Custom GPT.
3. Подготовить минимальный безопасный ответ и патч-план.

## Progress (English)

- 2026-08-03: read-only schema diagnosis started.
- 2026-08-03: live schema proof: global `bearerAuth` secures relay operations, while the second scheme `webhookToken` is used only by webhook ingress and webhook job polling. Custom GPT accepts one scheme, so it warns about this split.
- 2026-08-03: `X-GPTAdmin-Approval-ID` is an optional application approval header on three mutating webhook-route operations. The importer ignoring it does not affect relay discovery/calls, but means Custom GPT cannot complete these approval-gated operations.
- 2026-08-03: recommended immediate scope is the relay operations already verified as Bearer-compatible, or a per-server `/server/{slug}/actions/openapi.yaml` schema. A dedicated Custom-GPT schema should omit webhook-token operations and approval-gated webhook mutations.

## Self-improve retrospective

- What slowed or confused L? Global Action schema aggregates independently authenticated webhook ingress with normal client operations.
- Which instruction should change? none.
- Which skill, MCP, or tool is missing? none.
- What operation or error repeated? One importer compatibility warning with two causes; guard: publish a Custom-GPT-only schema profile.
- State: Proposed; no patch requested.
