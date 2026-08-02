# Задача: завершить HAOS OAuth deploy и настроить agent-resume

## Исходный запрос

«а сейчас? ты поставь agent-resume чтобы тебя возобновоял я заебался ручками»

## Цель

Подтвердить фактическое обновление HAOS standby до текущего OAuth metadata и
настроить одноразовое автоматическое возобновление task без ручного ожидания.

## Business canary

HAOS listener на `192.168.2.101:9001` возвращает ожидаемый commit и OAuth
metadata с `refresh_token` и `offline_access`; harness может создать и
наблюдать одноразовый resume wake.

## Scope

- Проверить результат `gptadmin-haos-oauth-6352926-r4.service`.
- Устранить только подтверждённый HAOS deployment blocker.
- Установить или восстановить `agent-resume` и провести bounded wake smoke test.

## Explicit exclusions

- Не менять работающий primary Hub.
- Не перезаписывать OAuth state или существующие credentials.
- Не выполнять reboot или offline filesystem repair.

## Классификация и оценка

- Classification: Short / P0 recovery.
- Initial active-minute estimate: 18 minutes.

## План (RU)

1. Проверить r4 и live HAOS identity.
2. Настроить agent-resume с одноразовым wake и проверить его.
3. Зафиксировать только claim-relevant live evidence.

## Progress (EN, append-only)

- 2026-08-02: Task opened. Previous r1-r3 failures were pre-cutover build
  environment failures; r4 was launched with explicit Go cache and FRPC paths.
- 2026-08-02: Overseer checkpoint requested; no independent delegate transport
  is exposed in this session, so the Lead will keep evidence and scope bounded.
- 2026-08-02: Public primary remains deployed with offline_access, but the
  existing GPTADMIN app connection now reports oauth_refresh_token_missing.
- 2026-08-02: HAOS r4 reported a successful systemd result; live identity must
  still be checked separately. agent-resume executable has not yet been found;
  only /home/roomhacker/.local/state/agent-resume exists.
- 2026-08-02: Live HAOS still reports build 136 / commit 6d344622 and lacks
  offline_access. The deploy wrapper had swallowed Supervisor HTTP 400 errors
  and did not fail when the requested listener build was absent; both guards
  are corrected and pass bash syntax/diff validation.
- 2026-08-02: Codex agent-resume registration pointed to a deleted checkout.
  It now targets the live checkout with AGENT_RESUME_AGENT=codex. MCP initialize
  and a 3-second non-executing timer captured only the current thread id and
  generated a safe explicit resume command. A 120-second executing one-shot
  wake is armed as job 20260801T235443Z-d429bd2a.
- 2026-08-02: HAOS Supervisor 2026.07.3 reports 2026.07.5 available; its update
  command is running before the blocked add-on deploy can be retried.
- 2026-08-02: Supervisor upgraded successfully to 2026.07.5. r5 exposed the
  next deployment defect: POST install correctly rejected an already-installed
  app. The deploy script now skips install when app info includes an installed
  version, raises the local app manifest from 1.0.4 to 1.0.6, and remains
  strict about rejected API calls and absent listener identities.
- 2026-08-02: r6 has stopped the old 1.0.5 image and is rebuilding 1.0.6;
  listener port 9001 is intentionally unavailable during that transition.
  agent-resume now watches r6 PID 850236 as job 20260802T000335Z-33e01f25 and
  will resume this exact Codex thread on completion or after its hard timeout.
- 2026-08-02: Complete. r6 exited 0; Supervisor reports the standby app
  started at 1.0.6/1.0.6. Live HAOS listener returns build 134 and commit
  6352926ad319b29540969d20e1bd1983ea42718b; OAuth metadata advertises both
  refresh_token and offline_access; /healthz returns ok. agent-resume also
  executed both verification wakes for this exact thread without use_last.
