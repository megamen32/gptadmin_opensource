# Health Incident Autopilot — три полных плана реализации

Дата: 2026-08-09
Общий результат для всех вариантов: доказанный путь от деградации на одной
машине до выбранного пользователем исправления, наблюдаемого прогресса и
resolved NoticePlace receipt с elapsed time и trace IDs.

## Сравнение

| План | Активные минуты (опт./вер./песс.) | Относительная стоимость | Рекомендация |
|---|---:|---|---|
| Максимально идеальный | 720 / 1500 / 2700 | высокая; больше компонентов и миграции | Для долгосрочной платформы |
| Нормальный | 360 / 780 / 1500 | средняя; максимальное переиспользование текущих seams | **Рекомендуется** |
| YAGNI 80/20 — полный результат | 240 / 480 / 960 | низкая; только нужный business path | Если важнее всего быстрое доказательство |

Все оценки — active minutes, без ожидания фоновых агентов и внешних human
approvals. Ни один вариант не включает production deploy/restart или реальный
Telegram send без отдельного approval на точной границе действия.

## 1. Максимально идеальный

### User-facing preview

Пользователь получает единый incident card: host, signal, severity, redacted
evidence, diagnosis, три плана и кнопки выбора. После выбора видит live timeline
с heartbeat/progress, текущим агентом, этапом и trace IDs; в конце — resolved
receipt и ссылку на полный trace bundle.

### Объём

- Fleet-wide health collector с host metrics, systemd failure state и
  configurable log rules/cursors; нормализация и privacy redaction до
  отправки.
- Durable incident/correlation service с versioned schema, deduplication,
  reconciliation и retention policy.
- NoticePlace/GPTAdmin/Agent Herder adapters для diagnosis, plan generation,
  user selection и terminal receipts.
- Hermes plugin + OpenCode adapter + OmniRoute telemetry bridge для heartbeat,
  pause/resume/approval, progress events и resolved business signal.
- Стадийный rollout по всем хостам, replayable synthetic canary и операторская
  health view.

### Что сознательно не входит

Никаких автоматических действий вне allowlist; не строится собственный LLM
provider и не форкаются Hermes/OpenCode. Dashboard не является acceptance
proof и может быть добавлен только после канарейки.

### Trade-offs / риски

Самая сильная наблюдаемость и recovery, но самый большой blast radius,
стоимость миграции и число мест, где может появиться drift. Потребуются
согласование retention/privacy и последовательная эксплуатационная rollout
процедура.

### Проверка и migration cost

Сначала вводится versioned envelope в shadow/read-only режиме, затем один
тестовый host, затем небольшие группы. Acceptance — полный canary из current
state документа плюс reconciliation после искусственного stall. Migration —
добавление collector/bridge на каждый host и перенос существующих Telegram-only
alerts через compatibility adapter.

### Parallel-work graph

- Signal lane: collector и log/systemd adapters; owned paths — новый health
  adapter/fixtures.
- Incident lane: envelope, NoticePlace/GPTAdmin correlation и dedup; owned
  paths — integration modules.
- Execution lane: Herder/Hermes/OpenCode/OmniRoute progress adapter; owned
  paths — supported plugin/config seams.
- Operations lane: rollout manifests и canary harness; owned paths — deploy
  docs/fixtures.
- Join: envelope contract → adapters → one vertical canary. Reviewer/Critic
  идут после join, Tester — только последним sequential gate.

## 2. Нормальный — рекомендуемый

### User-facing preview

В NoticePlace появляется health incident с host/evidence и тремя планами. После
явного выбора GPTAdmin запускает allowlisted Agent Herder session; в Telegram
топике здоровья видны start/heartbeat/progress/resolved сообщения, а в финале
пользователь получает один trace bundle ID.

### Объём

- Один fleet-compatible health event adapter, который переиспользует текущие
  recovery/external-site/systemd/log sources и добавляет CPU/RAM/disk/service/
  keyword checks без отдельной time-series платформы.
- Versioned correlation envelope и reconciliation worker, связывающий
  NoticePlace incident, GPTAdmin job, Herder session, OpenCode/Hermes IDs и
  OmniRoute request ID.
- Три стадии: diagnosis, plan generation, selected remediation; user choice
  остаётся обязательной перед write-capable action.
- Per-job heartbeat/progress/terminal contract с stall timeout, retry и
  escalation через NoticePlace.
- Один controlled synthetic canary, затем controlled rollout на текущий host
  inventory.

### Что сознательно не входит

Не строится полноценная historical metrics database, anomaly detection, новый
dashboard или multi-provider failover. Для доказательства результата достаточно
текущих host checks, bounded log evidence и append-only traces.

### Trade-offs / риски

Требует небольшого нового control/correlation слоя, но закрывает все блокеры
business canary с минимальным изменением существующих owners. Главный риск —
реальная topology/authorization может отличаться от документов; это закрывается
runtime inspection и canary до rollout.

### Проверка и migration cost

Добавить adapters в shadow mode, проверить dedup и redaction, затем провести
один неproduction canary. Migration ограничивается health adapter, correlation
fields, Hermes/OpenCode plugin hooks и Herder progress endpoint; existing
NoticePlace/GPTAdmin delivery remains the consumer.

### Parallel-work graph

- Signals: health adapter and redaction — independent.
- Correlation: NoticePlace/GPTAdmin envelope and reconciliation — independent
  until the shared type is fixed.
- Supervision: Herder/Hermes/OpenCode progress and terminal receipts —
  independent after envelope fields are agreed.
- Join: shared envelope review → one vertical incident flow → Reviewer → Critic
  → Tester. No overlapping writes before the join.

## 3. YAGNI 80/20 — полный результат

### User-facing preview

Один выбранный health topic и один incident flow: NoticePlace показывает host,
причину, три плана и кнопки выбора; выбранный plan запускает текущий named
session, который шлёт короткие start/heartbeat/resolved notices с одним
correlation ID.

### Объём

- Маленький host-local collector/cron/systemd adapter на всех текущих host
  entries, ограниченный CPU/RAM/disk, `systemctl --failed` и явным списком log
  keywords.
- Один redacted JSON event contract и deterministic dedup key.
- Переиспользовать существующие NoticePlace `POST /v1/events`, GPTAdmin
  webhook и Agent Herder `new-or-resume`; progress можно доставлять тем же
  NoticePlace channel с bounded cadence.
- Один Hermes/OpenCode plugin/config seam для plan/session IDs и terminal
  receipt; OmniRoute request ID сохраняется, если runtime его отдаёт.
- Доказать полный canary на одной разрешённой цели и включить тот же adapter
  для остального текущего inventory без исторического хранения.

### Что сознательно не входит

Нет метрик за длительный период, anomaly scoring, web dashboard, сложного
автоматического reconciliation, multi-host correlation analytics и provider
fallback orchestration. Это low-value слой, не нужный для полного incident
business canary.

### Trade-offs / риски

Самая быстрая доставка, но меньше аналитики и weaker recovery при долгом stall.
Если текущий runtime не отдаёт нужные IDs, потребуется opaque fallback receipt;
это должно быть явно видно пользователю, а не выдаваться за полную трассу.

### Проверка и migration cost

Один synthetic test signal, три плана, explicit choice, start/heartbeat/final
receipt. Migration — добавить один adapter и конфигурацию списка host/log rules;
production rollout всё равно требует отдельного approval.

### Parallel-work graph

- Collector/event contract и NoticePlace/GPTAdmin wiring — parallel.
- Minimal progress hook в Herder/Hermes — parallel после фиксации envelope.
- Join на одном canary; затем Reviewer, Critic и только потом fresh user-facing
  Tester.

## Решение и следующие gates

Рекомендую **«Нормальный»**: он сохраняет текущих владельцев и transports,
закрывает не только happy-path delivery, но и liveness/correlation, и не
вынуждает строить отдельную metrics platform.

Для продолжения нужен выбор: `Максимально идеальный`, `Нормальный` или
`YAGNI 80/20 — полный результат`. После выбора я покажу полный technical
preview: call-stack, file-tree diff, ключевые типы/сигнатуры, pseudocode,
migration, canary, authorization boundaries и execution graph. Только после
второго явного approval начнётся implementation.
