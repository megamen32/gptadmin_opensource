# Agent Wake API v1

`agent-wake-v1` is the legacy machine-to-agent ingress. New integrations should use [Agent Deliver API](AGENT_DELIVER_API.md). It reuses existing infrastructure rather than introducing another webhook service.

## Responsibilities

- **GPTAdmin Webhooks**: authenticated event ingress, HMAC verification, replay window, idempotency, durable job/result, routing.
- **Agent Herder**: stable named agent sessions and wake/message delivery.
- **NoticePlace**: human-facing notifications, incidents, approvals and escalation. It is not the transport for every machine event.

Flow:

```text
producer / MCP / cron / service
  -> GPTAdmin POST /webhooks/v1/agent-wake-v1
  -> local Agent Herder adapter
  -> Agent Herder named session (queue)
  -> AI runtime
  -> the agent calls its MCP tools as needed
```

## Event contract

```json
{
  "schema": "gptadmin.agent-wake.v1",
  "event_id": "telegram:5453051466:1524",
  "source": {
    "system": "telegram",
    "type": "message.created",
    "ref": "telegram:5453051466:1524"
  },
  "target": {
    "harness": "opencode",
    "name": "secretary-excode",
    "cwd": "/home/roomhacker/excode"
  },
  "subject": "New message in ИИ-Бенчмарки",
  "payload": {
    "chat_id": "5453051466",
    "message_id": 1524
  }
}
```

Required fields are `schema`, `event_id`, `source`, `target.harness`, `target.name`, `target.cwd`, `subject`, and `payload`.

Current target restrictions:

- schema must equal `gptadmin.agent-wake.v1`;
- harness is one of `opencode`, `codex`, `zcode`;
- `cwd` must be absolute and resolve below `/home/roomhacker/`, including symlink resolution;
- target name <= 128 chars, subject <= 512 chars;
- rendered wake message <= 32,000 UTF-8 bytes.

The ingress is for trusted producers. Payload content is event data for the agent, not an authorization to bypass the agent's normal MCP/tool policies.

## Authentication and idempotency

The live route uses HMAC signature version `v2`. The route secret is operator-managed and must never be included in event bodies or logs.

Required headers:

```text
Content-Type: application/json
Idempotency-Key: <stable unique delivery key>
X-Webhook-Timestamp: <unix seconds>
X-Webhook-Signature: sha256=<hex hmac>
```

The canonical string signed with HMAC-SHA256 is:

```text
POST
/webhooks/v1/agent-wake-v1
<timestamp>
<idempotency-key>
<sha256-hex-of-body>
```

A repeated request with the same idempotency key and identical body returns the original `job_id` with `duplicate: true` and is not delivered twice. Reusing an idempotency key for different content is rejected.

The live secret location on server-100 is documented operationally as `/etc/gptadmin/secrets/agent-wake-v1.env` (root-only). Do not copy the value into source control.

## Response model

Ingress returns `202 Accepted` with a GPTAdmin webhook `job_id`. A completed webhook job confirms queue acceptance, not completion of the agent's model turn. Consumers that need turn completion evidence must inspect the runtime result or use the Deliver API with `mode=sync` and verify `delivery=completed`.

The Herder delivery mode is `queue`: ingress should stay fast and should not block on a long model turn.

## Runtime notes

OpenCode is the verified working runtime for the live v1 canary. The full path was verified through GPTAdmin Webhooks -> Agent Herder -> OpenCode -> assistant response.

Codex currently reaches the model turn but its local login is unhealthy: the native session reports a revoked refresh token (`unauthorized`). Re-authenticate Codex before choosing `harness: codex` for production wake events.

Agent Herder retries OpenCode adapter initialization during startup so a short OpenCode startup race does not permanently remove that harness from the registry.

## NoticePlace integration

Use NoticePlace when the outcome belongs in a human notification/incident flow. It may itself trigger a GPTAdmin agent job when policy requires diagnosis/remediation, but ordinary machine events should go directly to `agent-wake-v1` rather than being converted into fake incidents.
