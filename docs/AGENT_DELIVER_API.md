# Agent Deliver API v1

`agent-deliver-v1` is the preferred machine-to-agent ingress. It reuses GPTAdmin Webhooks and Agent Herder; it does not introduce another webhook service or queue.

## Admin console setup

In the admin console, open **Вебхуки и агенты** (`#webhooks`). This page manages
the same operator-owned routes as `GET/POST /webhook-routes` and
`PUT/DELETE /webhook-routes/{id}`. The action target and tool fields are
free-form; there is no Agent Deliver target picker or test-sink catalog. The
console does not return saved route secrets, and replacing a route requires
entering its secret again.

The existing adapter contract in this repository is a Shell action on
`shell:admin-node-100`, invoking
`python3 /home/admin/gptadmin/scripts/agent_deliver.py '{{json}}'` with
`cwd` set to `/home/admin/gptadmin`. The webhook gateway substitutes the
event as a data-only environment value for the quoted `{{json}}` placeholder;
the script validates the schema and target before calling Agent Herder's
`deliver` tool. This is the fixed adapter path documented for the
`agent-deliver-v1` route, not a dynamically discovered MCP target.

Route presence and the installed adapter revision are runtime facts: first
check the secret-free route list and confirm the Shell host has the adapter
from the intended checkout. Do not create or replace the production
`agent-deliver-v1` route as a test. To create an isolated route, use a distinct
route ID, a newly managed HMAC v2 secret, and a disposable Agent Herder target
whose exact name and workspace are owned by the test. Set `delivery.create` to
`never` when the target must already exist. The event schema and delivery
policies below define the payload contract. A `202` webhook response proves
only that a job was accepted; inspect its terminal job result to prove the
adapter's delivery outcome.

## Flow

```text
producer / MCP / cron / service
  -> GPTAdmin POST /webhooks/v1/agent-deliver-v1
  -> Agent Herder deliver(...)
  -> named agent session
  -> AI runtime
  -> optional terminal callback from GPTAdmin webhook job
```

Responsibilities:

- **GPTAdmin Webhooks**: HMAC auth, replay window, idempotency, durable job state, fixed routing, callback.
- **Agent Herder**: named session resolution/creation, activation policy, deferred mailbox and actual model delivery.
- **NoticePlace**: human notifications/incidents/approval/escalation, not generic machine event transport.

## Event

```json
{
  "schema": "gptadmin.agent-deliver.v1",
  "event_id": "telegram:5453051466:1524",
  "source": {
    "system": "telegram",
    "type": "message.created",
    "ref": "telegram:5453051466:1524"
  },
  "target": {
    "harness": "opencode",
    "name": "secretary-excode",
    "cwd": "/home/admin/excode"
  },
  "subject": "Новое сообщение в ИИ-Бенчмарки",
  "payload": {},
  "delivery": {
    "create": "if_missing",
    "activation": "always",
    "mode": "sync"
  }
}
```

`delivery.create`: `if_missing` or `never`.

`delivery.activation`:

- `always`: process now and wake/start if needed;
- `if_running`: deliver only if the session is currently running, otherwise `skipped_inactive`;
- `defer`: persist for the next Herder-delivered turn without waking an inactive agent.

`delivery.mode`: `queue` or `sync`. Producers should normally submit asynchronously to GPTAdmin; using Herder `sync` does not block the producer because GPTAdmin returns `202` with a durable webhook job immediately. A successful webhook job/callback confirms the adapter's result, not necessarily a completed model turn: require `delivery=completed` for that claim. `skipped_inactive` and `deferred` are successful policy outcomes without a completed model turn; `queue` confirms enqueue acceptance.

The local adapter requires an absolute CWD resolving below `/home/admin/` (including symlink resolution), a target name of at most 128 characters, the policy values listed above, and a rendered message of at most 32,000 UTF-8 bytes. Invalid inputs fail before calling Herder.

## Authentication and dedup

The live route uses HMAC signature v2 and `Idempotency-Key`. Producers should use one stable event identity for both `event_id` and `Idempotency-Key` when the source event has a durable ID.

The canonical HMAC input is:

```text
POST
/webhooks/v1/agent-deliver-v1
<unix timestamp>
<idempotency key>
<sha256 hex of request body>
```

Secrets are operator-managed and are not stored in source control.

## Telegram production producer

The existing Universal UserIO Telegram QR connector is the producer for chat `5453051466` (`ИИ-Бенчмарки`). The actual UserIO message IDs use Telegram peer form `-5453051466:<message id>`; the connector normalizes the peer ID before matching.

Only live `NewMessage` events are forwarded to the agent. Historical reconciliation/backfill is still written to UserIO but does not wake the agent. Outgoing Telegram messages are ignored by the existing `message.out` guard, preventing a secretary self-loop.

Target session:

```text
harness=opencode
name=secretary-excode
cwd=/home/admin/excode
```

The production session is created once with its bootstrap instructions deferred; the first real live event flushes that bootstrap together with the event.

The Telegram producer uses `mode=sync`; GPTAdmin itself returns `202` immediately and executes the agent turn in its background webhook job. Terminal webhook callback receipts are HMAC-authenticated and persisted by the connector at:

```text
/var/lib/universal-userio/telegram-qr/agent-deliver-callbacks.jsonl
```

## ChatGPT targets

Existing ChatGPT conversations are available as Agent Herder sessions with `harness: "chatgpt"`. Their Herder identity workspace is `/home/admin/.chatgpt`; this is an identity anchor, not a source-code working directory. The webhook adapter accepts named targets only: use exact title + that CWD with `create: "never"`. Addressing a known ChatGPT `sessionId` is supported by Herder directly, not by this webhook adapter. Automatic creation of a missing ChatGPT conversation is intentionally not enabled yet.

For `chatgpt`, `activation: "always"` / `resume_agent` continue the existing `/c/...` conversation through the same BrowserClaw-owned page. `activation: "if_running"` and `defer` keep the same generic Agent Herder semantics. The separate ChatGPT token driver is the preferred fast read path; BrowserClaw/CDP is the write/resume path.

The BrowserClaw transport is reached through node-100's localhost-only SSH tunnel at `127.0.0.1:39479` → Mac `127.0.0.1:9010`; do not bind integrations to the Mac app's changing internal `9210/9211` ports. The current app is `BrowserOS neo` (`com.browseros.BrowserClaw`). Herder retries the adapter every 30 seconds when the browser side is unavailable. A Mac GUI login is still required for the BrowserOS neo application itself; SSH cannot create an Aqua session at `loginwindow`.

## Telegram quiet-period trigger

The production Universal UserIO Telegram QR connector debounces agent delivery for the secretary instead of waking it on every message. The configured targets are `ИИ-Бенчмарки` (confirmed Telegram peer id `5453051466`) and exact Telegram title `EE Frontier`.

For either target, every incoming live `NewMessage` resets a 300-second quiet timer. Historical backfill does not schedule delivery, and outgoing Telegram messages are ignored by the existing `message.out` guard. After five minutes with no newer incoming message in that chat, the connector emits one `gptadmin.agent-deliver.v1` event to `secretary-excode`.

The trigger does not copy message text into the event. It provides the chat identity, the last real Telegram author extracted from routing metadata, the last message ID, the burst size, and a concrete UserIO MCP read request. That request carries `ignored_chats`, so this consumer can omit its own agent conversations without changing the shared UserIO store or other consumers' views.

The task contract requires the secretary to fetch the current context and return an AI triage rather than a raw forward: source/chat/real author, a short intent and urgency assessment, and three ready-to-use reply variants (short, neutral, and warm). It must wait for the owner's choice before sending anything. Before `hermes send` delivers that triage, the connector helper records it in the destination Hermes session with `append_delegation_delivery`; this keeps follow-ups such as "reply with the second option" attached to the proactive notification without another LLM call.

Debounce state is persisted in `/var/lib/universal-userio/telegram-qr/agent-deliver-debounce.json` with mode 0600. If the connector restarts during a quiet window, it restores the pending burst and fires when the remaining quiet period expires. A failed `agent-deliver` attempt is retained and retried after another quiet interval instead of dropping the event.

## Legacy

`agent-wake-v1` remains temporarily available for compatibility. New integrations should use `agent-deliver-v1` and Agent Herder's `deliver` semantics. The word "wake" is no longer the preferred public abstraction because activation is only one delivery policy.

## Known relay issue

As of 2026-09-08, GPTAdmin's webhook dispatcher -> exposed child-MCP relay path can report a completed parent `mcp_call` while returning the child tool inventory/null instead of the child tool result. The production `agent-deliver-v1` route therefore uses GPTAdmin's fixed shell action to a narrow local adapter (`scripts/agent_deliver.py`), which calls the already-running local Agent Herder MCP directly and validates `ok=true`. This adapter is not a daemon, queue or alternative webhook service. Remove it after the generic child-MCP relay bug is fixed and revalidated.

BrowserOS neo 0.49.3.1 currently bundles BrowserClaw server 0.0.26 (migrations through `m0013`). On 2026-09-08 the local BrowserClaw DB had `m0014`–`m0016` applied by a newer server while those migration files were absent from 0.0.26, causing the embedded server to exit and proxy `9010` to return 503. After backing up the DB, the three empty/new schema changes were rolled back (`skills`, `skill_runs`, `skill_run_marks`, and empty `tasks.task_summary`), preserving historical sessions/dispatches; `PRAGMA integrity_check` returned `ok`. DB backup: `/Users/admin/.gptadmin/file-backups/browserclaw-db-20260908-052917`.
