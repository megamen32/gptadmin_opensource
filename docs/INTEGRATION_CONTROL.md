# Integration control contract

This document is the support and retry contract for the adapters listed in
[`INTEGRATIONS.md`](./INTEGRATIONS.md). It separates local protocol evidence
from certification that requires a real public client or deployment.

## Canonical flow

Every MCP-capable adapter must be able to perform:

1. `discover` — enumerate explicit Hub targets;
2. `schema` — retrieve the selected target's tools; and
3. `execute` — call one selected tool through the same policy and audit path.

An adapter must not guess a target or silently fall back to a default server.
The local conformance test exercises this flow against the built-in Hub target;
public ChatGPT/browser certification remains an external evidence class.

## Retry and idempotency

- Read-only discovery/schema requests may be retried when the adapter can prove
  that no write was dispatched.
- A write or an operation with an ambiguous timeout must carry a bounded,
  caller-supplied `idempotency_key`. The Hub deduplicates the key and rejects a
  conflicting payload; adapters must not invent an unbounded retry loop.
- A retry with the same key may return the original completed result. It must
  not create a second job or duplicate a side effect.
- Errors, arguments, credentials and private endpoint URLs are not support
  evidence and must not be copied into adapter metadata or logs.

## Evidence classes

`local` means a reproducible repository test can prove the contract.  
`external_required` means the repository can validate configuration and
protocol shape, but completion also requires the named client, public HTTPS
origin, or deployment runner.

The machine-readable inventory in
[`tests/fixtures/integration-support-matrix.json`](../tests/fixtures/integration-support-matrix.json)
is the source of truth for supported adapter names, version policy, smoke
commands and evidence class. Adding an adapter requires an entry there and a
conformance or external-runner result; documentation alone is insufficient.
