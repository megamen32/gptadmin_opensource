# Проверка передачи Hub и обновления Node

MCP-инструмент `handover_status` сохраняет поле `state` со старым состоянием
из `/run/gptadmin-handover.json`. Поля `state_path` и `state_scope` явно указывают
его источник: это только локальная передача между Hub на одном хосте.
Отсутствие файла по совместимости даёт `state.status: idle`; это не проверка
готовности обновления или работоспособности standby.

Поле `runtime` показывает путь исполняемого файла текущего процесса, его
`build_version` и `git_commit`. Эти сведения относятся к ответившему процессу;
они не подтверждают содержимое пакета на диске, состояние другого узла или
прохождение реального MCP-вызова после обновления.

В `hints.node_update` возвращаются команды для установленного versioned Node.
На хосте primary обе команды выполняют только чтение:

```sh
sudo gptadmin node status --name primary
sudo gptadmin node autoupdate status
```

Первая показывает текущий пакет, службу, health/version и membership. Вторая
показывает opt-in, зарегистрированные экземпляры и состояние таймера.
Историческое имя `gptadmin-hub.service` может принадлежать единому Node runtime.
При наличии `/var/lib/gptadmin-node/autoupdate/hold.json` сначала выясните исход
предыдущего действия и восстановления. Не удаляйте hold и не повторяйте
операцию с неизвестным исходом вслепую.

Команда `sudo gptadmin node autoupdate run` в `manual_update_action` — отдельное
действие: запускает обновление и может перезапустить службу. Сам
`handover_status` не запускает команды, обновления или сетевые проверки.
Порядок доставки описан в [Node updater](NODE_PUBLIC_AUTOUPDATE.md).

HAOS standby использует отдельный promotion controller. Перед обслуживанием
проверьте его состояние и `failover.maintenance_until` в настройках HAOS add-on.
Локальный файл handover и Node updater не координируют этот контроллер и не
подавляют promotion автоматически. Подсказка `hints.haos_failover` не означает,
что HAOS был опрошен или что окно обслуживания уже установлено.
