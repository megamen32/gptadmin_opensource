# Work: восстановление user-facing computer-use surface

## Original request

Пользователь сообщил, что Touchpoint показал себя плохо, и попросил установить тот же способ, который используется для проверки на Mac, а также объяснить, как проверяется Mac.

## Objective

Дать Tester рабочую user-facing computer-use поверхность, совместимую с канонической Mac-проверкой, не подменяя её HTTP/API-проверкой и не отправляя Telegram-сообщения или Hermes egress.

## Business canary

Свежая black-box сессия без исходников может открыть пользовательский Health-путь, увидеть состояние и пройти безопасный no-send canary; при недоступной поверхности результат остаётся `STOP_MISSING_REAL_SURFACE`.

## Confirmed scope

- Read-only audit канонической Mac-поверхности BrowserClaw.
- Установка/настройка только подтверждённого эквивалента в пределах user-facing computer-use, с обратимым изменением.
- Проверка реальным user-facing способом после установки.

## Explicit exclusions

- Не использовать Touchpoint как доказательство, пока его transport не восстановлен.
- Не отправлять Telegram.
- Не запускать Hermes egress.
- Не менять ChatGPT MFA, cookies, секреты, production endpoint или Custom GPT.
- Не выполнять restart/deploy на Mac, пока не установлен точный target и необходимая граница.

## Estimate

- Initial active estimate: 20 minutes for Mac topology audit; 60 minutes for a local reversible install; 180 minutes if the Mac-only dependency cannot be safely reproduced locally.
- Revisions are append-only below.

## Plan (RU)

1. Проверить Mac BrowserClaw topology и способ проверки.
2. Сопоставить с доступным локальным transport и найти канонический installer/runner.
3. Выполнить только обратимую установку подтверждённого компонента.
4. Проверить user-facing surface, затем передать свежему Tester.

## Progress (English)

Work started; no external send or Hermes egress permitted.

### Read-only Mac and Linux evidence (2026-08-10)

- Mac `whitetransport-mac-mini-2012` is `mac-mini-2012.lan`; `/Applications/BrowserClaw.app` is running under its launch agent and serves loopback MCP on port 9010.
- Mac BrowserClaw version is 0.48.1, universal x86_64/arm64; MCP `initialize` with protocol `2025-03-26` returned 200 and a session, followed by `tools/list` 200 with 17 UI tools.
- Linux already has canonical BrowserOS running as `admin`; MCP `http://127.0.0.1:9000/mcp` returned 200, initialized with protocol `2025-03-26`, and exposed 23 tools including `tabs`, `snapshot`, `act`, `read`, `grep`, and `wait`.
- Linux is not a target for copying the macOS `.app`; the existing BrowserOS MCP is the platform-appropriate equivalent. Touchpoint remains a separate failed transport.
- The Codex MCP registration was stale: `/home/admin/.codex/config.toml` pointed `browseros` at dead `127.0.0.1:9200/mcp` while the canonical daemon answered on `127.0.0.1:9000/mcp`. Created backup `/home/admin/.codex/config.toml.bak.browseros-9000-20260810` and switched only that URL to port 9000. No secrets or browser profile data changed.
- Mac BrowserClaw semantic read-only canary through a temporary localhost-only SSH forward succeeded: `initialize` 200 with session, `tools/list` 200 with 17 tools, and `tabs(action=list)` 200 with 33 browser pages. No prompt, click, message, or external send was performed.
- Direct Linux BrowserOS semantic read-only probe also succeeded for the existing `tgb.bezrabotnyi.com` tab: `tabs(list)` and `snapshot` returned data, but the rendered page contained no visible Health/Telegram/topic labels. This is surface reachability only, not proof of the required Health topic.
- The Hermes capability check initially failed only because its skill expected obsolete CDP port 9103. Current `browseros-shared.service`, Hermes runtime config, and BrowserOS tests all use CDP 9223 with MCP 9000. Updated the secret-safe skill topology/check script to the observed canonical ports; rerun now passes service, SearXNG, STT, and BrowserOS CDP checks.
- Codex `mcp get browseros` now reports enabled Streamable HTTP at `http://127.0.0.1:9000/mcp`, but the already-running Codex harness still exposes no `mcp__browseros` tools to fresh subagents. A new harness load is required before Tester can use the registered surface.

### Product-boundary check (2026-08-10)

- The live Mac check is BrowserClaw 0.48.1: BrowserClaw app plus its bundled `browseros-claw-server`, with CDP `9112`, direct server `9210`, and MCP proxy `9010`. The read-only Mac canary uses MCP initialize/tools discovery and semantic tab listing through a localhost-only SSH tunnel; it does not use Touchpoint.
- The vendor-supported BrowserClaw/BrowserOS neo desktop app targets macOS and Windows. Linux is supported by BrowserOS, not by the BrowserClaw desktop app. The Linux BrowserOS installation already running here is therefore the supported platform equivalent; copying the macOS `.app` would be invalid and a second browser would create an unnecessary profile/port boundary.
- The exact BrowserClaw server has a separate Linux x64 artifact, but installing that sidecar alone would not install the Mac user-facing BrowserClaw browser/dashboard and would introduce a second CDP/MCP owner. It is not installed without a proven business canary requiring it.
- Current action: keep the existing Linux BrowserOS runtime, repair/reload its Codex MCP registration, and verify it with a fresh black-box Tester. No Telegram send, Hermes egress, profile migration, or production restart was performed for this comparison.

### Overseer audit (2026-08-10T01:56:22+03:00)

- Result: `ASK_USER`.
- Eligibility evidence is insufficient: this task file contains no prior Overseer-audit timestamp or attested elapsed interval, and no separately recorded material trigger for this audit. The invocation/stage change alone is not a qualifying trigger under the Overseer gate.
- Business delta: the recorded topology audit narrows the canary route to the existing Linux BrowserOS MCP, but it does not by itself provide the fresh Tester user-facing canary required by the objective.
- Avoidable spend: further install, topology, or verification work before the audit gate is established risks process spend without moving the business canary.
- Minimum next action: provide the missing audit eligibility evidence, then reassess the proposed next action against the canary.
- Direct question: What was the time of the previous Overseer audit and what material trigger authorizes this one?

### Overseer eligibility correction (2026-08-10)

- Previous Overseer checkpoint is bounded by the runtime review wave: after Fleet release commit `ea625ef` at `2026-08-10T01:20:32+03:00` and before the safety-fix commit `df33a3d` at `2026-08-10T01:31:27+03:00`; the exact child invocation timestamp was not preserved when the parent task crossed branches. The checkpoint produced the three runtime safety findings later fixed in `df33a3d`.
- Material trigger for a new checkpoint: the fresh context-free Tester completed with `STOP_MISSING_REAL_SURFACE` because its tool surface exposed only Touchpoint, while an independent read-only MCP handshake against Linux BrowserOS remained healthy. This is a new acceptance mismatch, not a stage-name change.

### Overseer audit (2026-08-10T01:59:51+03:00)

- Result: `ASK_USER`.
- Eligibility is still closed: the task records the last Overseer audit at `2026-08-10T01:56:22+03:00`, only 3m29s before this checkpoint; the required 30-minute interval is not met. The later correction supplies a material trigger but not an attested timestamp that can override the cooldown.
- Business delta: the material trigger confirms an acceptance mismatch—Linux BrowserOS is healthy, but the fresh Tester still sees only Touchpoint—without yet producing the required real user-facing canary.
- Avoidable spend: another install, topology pass, or verification attempt before the cooldown expires cannot move the canary and risks process spend.
- Minimum next action: do not start new work; re-invoke Overseer after `2026-08-10T02:26:22+03:00`, unless an attested harness record proves the `01:56:22` entry was not a qualifying audit.
- Direct question: Can L provide that attestation, or re-invoke this audit after `02:26:22+03:00`?
