# Install GPTAdmin on Mac mini 192.168.2.4

Status: complete
Original user request: «Установи на мак мини 192.168.2.4 гптадмин тоже»
Objective: Установить клиент GPTAdmin/ShellMCP на Mac mini 192.168.2.4, подключить его к существующему GPTAdmin Hub и обеспечить автозапуск.
Business canary: Mac mini виден как online-агент в существующем Hub, а безопасная команда идентификации выполняется через реальный Hub → ShellMCP маршрут на этом Mac.
Confirmed scope: Mac mini 192.168.2.4; пользовательская установка GPTAdmin/ShellMCP; сохранение существующей конфигурации машины; автозапуск; проверка через Hub.
Explicit exclusions: отдельный Hub на Mac; обновление macOS/OCLP; WhiteTransport; изменения других хостов; очистка или откат чужих изменений репозитория.
Acceptance: установлен нативный darwin-артефакт правильной архитектуры; конфигурация указывает на действующий Hub без раскрытия токенов; процесс переживает штатный перезапуск launchd-службы; Hub показывает агент online; E2E-команда возвращает идентичность Mac mini.
Initial estimate (optimistic / likely / pessimistic active minutes): 10 / 20 / 40
Estimate revisions (append-only; trigger and evidence): 2026-08-03 — 15 / 30 / 60 минут; trigger: строгая SSH-проверка обнаружила новый неподтверждённый ED25519 host key, а NanoKVM не дал независимый канал подтверждения.
- 2026-08-03 — 25 / 45 / 70 минут; trigger: macOS 15 Local Network consent сначала блокировал launchd-процесс, затем shell-only installer потребовал синхронизировать Hub `SHELLMCP_TOKEN`; оба состояния доказаны переходами `no route to host` → `401` → `awaiting_approval` → `online`.
Cycle: full (promoted from direct after host-key identity risk was found)
Workflow: YAGNI завершён как минимальный production-ready vertical slice: user-confirmed host-key rotation → strict SSH → user-mode install → macOS permission → Hub approval → post-restart E2E.
Current stage: YAGNI

## План

1. Проверить SSH-идентичность, архитектуру macOS, существующую установку и доступный способ регистрации.
2. Подготовить точную резервную копию затрагиваемых файлов и установить штатный darwin-клиент с launchd-автозапуском.
3. Проверить локальный процесс, регистрацию в Hub и безопасную E2E-команду через Hub.
4. Провести независимый Overseer-аудит и зафиксировать результат.

## Overseer decision history (append-only)

- Timestamp: pending
  Stage: pre-implementation
  Evidence: pending
  Current user P0: GPTAdmin на Mac mini 192.168.2.4
  Business delta: task recorded; host not yet inspected
  P0 distance: SAME
  Questions for L: pending
  Decision: pending

- Timestamp: 2026-08-03
  Stage: pre-implementation
  Evidence: strict SSH stopped before authentication; pinned `SHA256:s0TX…`, current endpoint twice `SHA256:tiXh…`; matching LAN MAC/mDNS; NanoKVM login failed; no remote mutation.
  Current user P0: безопасно сделать Mac mini 192.168.2.4 управляемым агентом GPTAdmin через существующий Hub
  Business delta: 0 из 1 Mac подключён; установка не начата
  P0 distance: SAME
  Questions for L: Получить независимое подтверждение нового fingerprint и явное решение пользователя о принятии ротации ключа с причиной/датой.
  Decision: STOP

- Timestamp: 2026-08-03
  Stage: pre-implementation re-audit after user correction
  Evidence: raw user confirmation «Господи да он это он»; matching LAN MAC/mDNS; dedicated known-hosts updated to current key; strict SSH subsequently authenticated `roomhacker` using the surviving Mac-specific key; live identity is `Macmini6,2`, macOS `15.7.8`, `x86_64`.
  Current user P0: установить GPTAdmin/ShellMCP на подтверждённый Mac mini 192.168.2.4 и доказать Hub E2E
  Business delta: endpoint identity accepted and strict authentication recovered; installation not yet started
  P0 distance: CLOSER
  Questions for L: нет
  Decision: APPROVE

- Timestamp: 2026-08-03
  Stage: post-YAGNI implementation acceptance
  Evidence: strict SSH exact identity; official build 141 x86_64; valid/running launchd; one canonical PID; primary Hub online/approved; post-restart E2E job `648ac6fc4c7af3b6955569b237351ed0` completed returncode 0; exact rollback backups; manifest warning isolated to auto-update parsing.
  Current user P0: установить GPTAdmin/ShellMCP на новый Mac mini 192.168.2.4, обеспечить автозапуск и доказать Hub→Mac E2E
  Business delta: 1 из 1 Mac установлен, online и выполнил business canary
  P0 distance: CLOSER
  Questions for L: нет
  Decision: APPROVE

## Critic decision history (append-only)

Не требуется для обратимой Direct-установки без релиза или необратимого решения.

## Decision

Research: Публичный installer поддерживает `darwin-amd64`; целевой путь — user-mode GPTAdmin/ShellMCP с launchd и существующим Hub. SSH alias использует отдельный strict known-hosts. Сохранённый подтверждённый fingerprint `SHA256:s0TX…`, текущий endpoint дважды показал `SHA256:tiXh…`; MAC `68:5b:35:81:df:8f` и `mac-mini-2012.lan` совпадают, но out-of-band подтверждения нового ключа нет.
Plans: Максимально идеальный — сверить fingerprint на физической/NanoKVM-консоли; Нормальный — пользователь сверяет и подтверждает fingerprint из Terminal на Mac; YAGNI MVP — явное принятие нового ключа только по совпавшим LAN MAC/mDNS, с остаточным MITM/IP-подмены риском.
Human selection: Пользователь прямо выбрал установку на 192.168.2.4 и затем подтвердил новый endpoint/host key фразой «Господи да он это он»; это выбирает YAGNI-путь точечной ротации dedicated known-hosts.
Selected-plan WSFF: Минимальный безопасный вертикальный срез: preflight → backup → install → launchd → Hub E2E.

## Work

Current: Complete.
Next: Отдельно исправить центральный manifest type contract только по новому пользовательскому запросу.
Blocked by: нет
Evidence: current host key `SHA256:tiXhDUkRVB7va5PooklfqEKkQQkaBFpqPp3V462s8z4`; strict SSH identity `mac-mini-2012.lan / Macmini6,2 / macOS 15.7.8 / x86_64 / roomhacker`; binary build 141 commit `04980acfd3d6f9924177683dac86dee5727eb959`; Hub target `shell:mac-mini-2012.lan` online/approved; post-restart E2E job `648ac6fc4c7af3b6955569b237351ed0` completed with returncode 0.

## Result

Summary: Установлен user-mode GPTAdmin ShellMCP build 141 (`darwin amd64`) на `mac-mini-2012.lan`; создан и загружен `com.gptadmin.shellmcp` LaunchAgent; через NanoKVM одобрен macOS Local Network; синхронизированы существующие Hub queue/relay credentials без вывода; новая identity одобрена в Hub.
Tests: plist `OK`; launchd restart PID `48009` → `48191`; после restart ровно один canonical process; Hub target online/approved; E2E job `648ac6fc4c7af3b6955569b237351ed0` вернул `Macmini6,2`, macOS `15.7.8`, `x86_64`, `roomhacker`, Mach-O x86_64, returncode 0.
Review: Post-stage Overseer APPROVE; exact identity, secret handling, rollback, launchd restart, sole PID, Hub online status and E2E receipt independently rechecked.
Commit: remote runtime only; local task/self-improve receipts remain uncommitted in shared dirty worktree.
Unresolved: Auto-update manifest на текущем Hub отдаёт `build_version` строкой, а build 141 ожидает integer; startup log фиксирует parse error, но основной polling/Hub E2E работает. Центральный manifest не менялся как вне scope.
