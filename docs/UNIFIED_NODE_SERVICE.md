# Служба единого узла

`deploy/systemd/gptadmin-node@.service` запускает один `gptadmin-node`:
маршрутизатор и локальный исполнитель работают в одном процессе. Имя экземпляра
определяет файл окружения и рабочий каталог. Это не заменяет службы старой
установки автоматически.

Для экземпляра `canary`:

- бинарник: `/opt/gptadmin/bin/gptadmin-node`;
- окружение: `/etc/gptadmin/nodes/canary.env`, доступ только root;
- рабочий каталог: `/var/lib/gptadmin/nodes/canary`;
- служба: `gptadmin-node@canary.service`.

Каталоги создаются до запуска. Пример несекретной части окружения:

```dotenv
GPTADMIN_CONFIG_DIR=/var/lib/gptadmin/nodes/canary
GPTADMIN_ROOT=/var/lib/gptadmin/nodes/canary
GPTADMIN_ENV_FILE=/etc/gptadmin/nodes/canary.env
GPTADMIN_HUB_HOST=127.0.0.1
GPTADMIN_HUB_PORT=19002
SHELL_NAME=relay02-unified
SHELL_IDENTITY_DIR=/var/lib/gptadmin/nodes/canary
SHELL_SPOOL_DIR=/var/lib/gptadmin/nodes/canary/spool
SHELL_OUTBOX_DIR=/var/lib/gptadmin/nodes/canary/spool/outbox
SHELL_DEFAULT_USER=gptadmin-canary
SHELL_DEFAULT_CWD=/var/lib/gptadmin/workspaces/canary
SHELL_DEFAULT_HOME=/var/lib/gptadmin/workspaces/canary
SHELLMCP_SELF_REPAIR_DISABLE=1
```

Существующие cluster credentials и общий issuer/resource настраиваются
отдельно доверенным provisioning. Не копируйте private identity другого узла,
его outbox или task DB. Состояние авторизации должно пройти проверку перед
публикацией listener. Обычный managed bearer не заменяется CTL в проверке.

До запуска создайте пользователя исполнения и принадлежащий ему workspace.
Каталог состояния Node остаётся доступен только root. Служба запускается от
root, чтобы исполнитель мог переключить пользователя; без `SHELL_DEFAULT_USER`
обычная shell-команда завершится отказом. Привилегированное выполнение требует
явного выбора `run_as_user=root` в разрешённом запросе.

После установки файлов:

```sh
systemd-analyze verify /etc/systemd/system/gptadmin-node@.service
systemctl daemon-reload
systemctl start gptadmin-node@canary.service
```

Сначала проверяются реальный MCP-вызов на нужном executor, запрет недопустимой
операции и restart. Затем измеряются RSS и размер состояния на данной машине.
Автозапуск включается `systemctl enable gptadmin-node@canary.service` после
этой проверки. Изменение публичного маршрута — отдельный проверяемый шаг.

Повторяемая проверка кандидата использует существующий клиентский токен из
окружения и настоящую команду `printf` на явно указанном исполнителе:

Здесь `u-f…` намеренно выбирает transport конкретного node-canary. Это не
OAuth identity общего ChatGPT/Codex connector, для которого используется
`https://mcp.bezrabotnyi.com/mcp`.

```sh
python3 scripts/gptadmin_node_probe.py \
  --url https://your-subdomain.t.became.bezrabotnyi.com \
  --connect-to 185.240.120.152 --target shell:relay02-unified \
  --expect-commit 0672047
```

`--connect-to` сохраняет hostname, SNI и обычную проверку сертификата.
По умолчанию токен берётся из `GPTADMIN_CODEX_MCP_BEARER`; другое имя задаётся
через `--token-env`. Команда отправляется один раз, затем проверяется её receipt.
Успех — JSON с `ok: true` и ожидаемым stdout; ошибка даёт ненулевой exit code.
Это проверка перед переключением, а не частый health-poll: она создаёт настоящий
job. Свежесть реплики и согласование writer проверяются отдельно. На Unix probe
имеет общий deadline; при запуске на другой ОС задавайте внешний process timeout.

`systemctl stop` отправляет SIGTERM одному процессу; runtime останавливает
локальные callback-и и группы команд перед завершением HTTP-сервера.

## Переход с отдельных Hub и Shell

Проверенный порядок для обновления той же установки:

1. Приостановить поступление новых заданий, оставив старые Hub и Shell работающими.
2. Дождаться завершения текущих заданий и доставки их результатов в Hub;
   проверить сохранённые receipts и пустой outbox. При SQLite task store
   проверять `tasks_state.sqlite` в одной read-only транзакции: nonterminal
   `task_records` и pending `task_controls`. `tasks_state.json` после импорта
   может быть устаревшим и не доказывает отсутствие текущих заданий.
3. Остановить старый Shell, затем старый Hub.
4. Запустить Node на прежнем порту с теми же `GPTADMIN_CONFIG_DIR`,
   `SHELL_NAME`, `SHELL_IDENTITY_DIR`, `SHELL_SPOOL_DIR` и `SHELL_OUTBOX_DIR`.
5. Проверить сохранность identity и старых receipts, выполнить безопасную новую
   команду и проверить её результат. После этого возобновить поступление заданий.

Этот порядок прошёл изолированный canary 2026-09-09: реальные Hub и Shell
v1002 → Node `b1b2aee`, общие каталоги состояния и identity. Старый receipt
остался `completed`, команда выполнилась ровно один раз, новая команда прошла.

Перекрытие старого Shell и нового Node не обеспечивает немедленную доставку
результата: в canary команда уже выполнялась при замене Hub, завершилась один
раз, но callback старого Shell получил HTTP **401** из-за нового локального
credential процесса. Результат сохранился в общем outbox, а receipt оставался
`running`. После остановки старого Shell Node доставил результат; `completed`
и пустой outbox наблюдались на **+96,1 с от начала canary**, не от остановки
Shell. Это наблюдаемая задержка, а не гарантированный срок восстановления;
проверка восстановления использовала общий outbox. Не повторять такую команду
с новым ключом только из-за задержки receipt — сначала проверить её результат.

Локальные доказательства: `.tmp/node-upgrade-review/summary.json`,
`case-aqvzz11b/report.json` (перекрытие) и `case-tfc9v301/report.json` (drain)
в том же каталоге. Эти временные артефакты не входят в поставку.

### Production-переход 2026-09-09

Primary переведён с `17f8945` на один Node `3ba016d`. Публичный admission
приостанавливался на 9,972 секунды; доставка результатов оставалась разрешена.
После запуска две независимые проверки выполнили настоящие команды через
canonical HTTPS/MCP текущим managed credential. Identity сохранилась.

Первоначальный JSON-preflight был ошибочным: active store уже был SQLite.
Ретроспективный аудит cold SQLite, снятого после остановки старых процессов,
и live SQLite установил 2139 неизменных общих receipts, две завершённые записи,
истёкшие по существующей 24-часовой retention, и три новых успешных probe jobs.
В окне перехода нет созданий, стартов, завершений или обновлений старых jobs;
последнее завершение было за 219,930 секунды до pause. Cancelled и nonterminal
в cold store отсутствовали. Этот аудит заменяет ошибочный JSON-preflight,
а не делает его корректной процедурой для следующего обновления.

Старые spool, quarantine и dead-outbox оставлены на месте; новый runtime
использует отдельные пустые spool/outbox, чтобы штатная очистка не затронула
исторические файлы. Такой переход допустим только после проверки пустой
активной очереди доставки; непереданный receipt требует сохранения рабочего
пути outbox до подтверждения доставки.

В описанном здесь некворумном переходе перезапуск не восстанавливает queued-команду автоматически: отдельный реальный
canary подтвердил `hub_restarted_before_dispatch` и ноль исполнений. Поэтому
пустая очередь перед остановкой — обязательная часть этого порядка обновления.
Сбой машины не означает разрешение повторно выполнить неизвестно завершённую
команду на другом узле.

### Последующее обновление Node

При обновлении primary до `30428c2` обнаружена конкуренция с FRP watchdog:
он перезапускал tunnel service, а оставшийся `BindsTo=gptadmin-hub.service`
снова запускал Hub и отменял его stop job. Пустое присваивание `BindsTo=`
в drop-in на этой системе не удаляло зависимость из base unit. Base unit
исправлен: tunnel следует nginx, а effective dependencies проверены через
`systemctl show`. CLI сохраняет независимый lifecycle только для unified
primary; прежний legacy failover-контракт остаётся в legacy-режиме.

Перед такой остановкой приостанавливается соответствующий watchdog и
проверяется завершение его service. После stop проверяются `MainPID=0`,
`ActiveState=inactive|failed` и пустой service cgroup; один returncode команды
не заменяет эти проверки. Runtime со старым кодом и открытым poll может
завершиться по 10-секундному shutdown deadline. Затем проверяется cold SQLite.

Identity сравнивается по `server_id`, public key, fingerprint и имени. Сравнение
всего JSON побайтово было ошибкой проверки: `created_at` в этом файле меняется
при restart и не означает смену ключа. Успешное обновление сохраняло 2122
проверенных receipts без изменений и заняло 13,523 секунды public admission pause.
После него отдельно прошли canonical MCP-команда и live idle-poll/export canary.
VUSA обновлён тем же Node artifact; восемь прежних receipts и identity сохранены.

Следующее обновление primary до `e754316` добавило peer-маршрут с физическим
IP и проверяемым HTTPS. Admission pause составил 13,429 секунды; identity
сохранена, 2094 receipts не изменились, 19 завершённых записей истекли по
действующей 24-часовой retention. Необъяснённых изменений нет. Реальная
команда через canonical MCP прошла после обновления, watchdog возобновлён.

При переходе на объектные значения `GPTADMIN_NODE_PEERS` обновляются binary
и конфигурация вместе: предыдущий `30428c2` понимает только строки. Rollback
возвращает совместимую пару binary/config, сохраняя актуальные auth и SQLite.

VUSA также обновлён до `e754316` вместе с reciprocal peer config. Все 12
прежних SQLite payloads и identity сохранены. Node и auth-sync активны;
после обновления наблюдалось поколение 128 возрастом 3,841 секунды.
Public peer canary в обоих направлениях подтвердил правильный host/user,
три чтения одного receipt через исходный вход и один side effect.
