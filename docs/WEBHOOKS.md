# Webhook Gateway

GPTAdmin Hub exposes a universal, route-configured event ingress. A webhook
does not choose a target from the request body. The route selects one explicit
MCP target, prompt action, or Shell target, so Agent Herder is optional and is
handled exactly like any other registered MCP server.

## Endpoint

```text
POST /webhooks/v1/{route}
GET  /webhook-jobs/{job_id}
GET  /admin/api/webhook-jobs/{job_id}
```

The POST body may be any single JSON value up to 1 MiB. The endpoint returns
`202 Accepted` with a `job_id`; the action runs asynchronously. Repeating the
same `Idempotency-Key`, provider delivery ID, or raw body on a route returns
the original job instead of dispatching a second action.

## Authentication

Each route must configure exactly one of:

- `token`: send `Authorization: Bearer <token>` or `X-Webhook-Token`.
- `hmac_secret`: send `X-Webhook-Timestamp` and
  `X-Webhook-Signature: sha256=<hex>`. Legacy `v1` signs
  `<timestamp>.<raw request body>`. New routes should set
  `signature_version: "v2"`; v2 signs the newline-separated method, escaped
  path, timestamp, `Idempotency-Key`, and SHA-256 of the raw body. The default
  replay window is five minutes.

Do not put route credentials in URLs or event JSON.

## Configuration

Set `GPTADMIN_WEBHOOK_CONFIG_FILE` or create
`$GPTADMIN_CONFIG_DIR/webhooks.json`:

The file contains route credentials and must be readable only by the Hub
service account (normally mode `0600`).

Routes can also be managed with Hub control authentication through
`GET/POST /webhook-routes` and `PUT/DELETE /webhook-routes/{route}`. Responses
contain route metadata only; token and HMAC secret values are never returned.
Route updates are persisted atomically to `GPTADMIN_WEBHOOK_CONFIG_FILE` with
mode `0600`. Completed jobs and replay keys are stored in
`GPTADMIN_WEBHOOK_STATE_FILE` so restart does not lose delivery identity.

The admin console page **Вебхуки и агенты** uses the same operator endpoints.
It can list, create, replace, and delete routes and inspect a durable job by
ID. Route credentials are write-only: the UI and every read API return only
secret-free metadata.

The same five operations are available to AI clients through MCP:

- `webhook_routes_list`
- `webhook_route_create`
- `webhook_route_replace`
- `webhook_route_delete` (`confirm=true` is mandatory)
- `webhook_job_get`

They are also described by `/actions/openapi.yaml` and `public/openapi.yaml`
for Custom GPT Actions. Read operations require `gptadmin.read`; route writes
require `gptadmin.exec` and remain subject to the selected access profile and
approval policy. When an `ask_before_write` profile returns an approval ID,
approve it in GPTAdmin and repeat the write with the
`X-GPTAdmin-Approval-ID` header; an approval is bound to the exact operation
and payload and is consumed once.

```json
{
  "routes": [
    {
      "id": "build-finished",
      "token": "operator-managed-secret",
      "action": {
        "kind": "shell",
        "target": "shell:runner",
        "approval_mode": "bounded_autonomous",
        "command": "/usr/local/bin/process-build '{{event.repository.name}}' '{{event.number}}'",
        "cwd": "/srv/project"
      },
      "callback": {
        "url": "https://automation.example.invalid/gptadmin-result",
        "hmac_secret": "callback-secret"
      }
    }
  ]
}
```

Supported actions:

- `mcp`: calls the configured `target` and `tool` with recursively rendered
  `arguments`.
- `prompt`: calls the configured MCP `tool` and writes the rendered `prompt`
  into `prompt_arg` (default `message`). This can target Agent Herder or any
  other MCP server exposing the desired operation.
- `shell`: queues `shell_exec` on the configured `shell:<name>` target and
  renders `command` and `cwd`.

Write-capable webhook actions are policy-controlled. The default
`approval_mode` is `ask_before_write`, so the action is rejected with an
approval-required result and no job is queued until an explicit workflow is
added. Routes that are intentionally allowed to run unattended may set
`approval_mode: "bounded_autonomous"`; the Hub still applies the bounded
per-actor write budget and records the policy decision in the audit trail.
Read-only actions do not consume the write budget.

Templates use `{{event.path.to.value}}` for nested fields and `{{json}}` for
the complete event. The target and operation are always taken from the route
configuration, never from the webhook payload.

The callback receives the terminal job state and result. Callback delivery is
also configuration-owned; the webhook cannot supply a callback URL, which
prevents an event from turning the Hub into an arbitrary SSRF relay.

## Completion model

The gateway waits for the configured action's own completion contract: Hub MCP
relay jobs, Shell queues, or the selected MCP server's response. Target-specific
event streams such as OpenCode `/event` belong in that MCP server's adapter;
the gateway remains transport-neutral and only exposes the normalized job
state and callback.
