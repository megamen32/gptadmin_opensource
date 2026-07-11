# Дизайн: автообновление — явный выбор при установке, подсказка в CLI, версии и кнопка в /admin

Дата: 2026-07-10 · Статус: одобрен, ревизия 2 (code-review)

## 1. Установка — явный промпт про автообновление

В `setup_interactive()` добавить один промпт перед записью env-файла:

> Включить автообновление (проверка раз в 6ч, systemd timer / launchd)? [Y/n]

- **Дефолт Yes** (Enter = да).
- Ответ `n` → `GPTADMIN_AUTO_UPDATE=false`, таймер не создаётся; но **service-unit (oneshot) остаётся** — он нужен для кнопки «Обновить» в /admin (см. раздел 3). Только timer/plist-interval включается/выключается.
- Ответ `y`/Enter → `GPTADMIN_AUTO_UPDATE=true`, таймер создаётся и стартует как сейчас.
- **Неинтерактивный режим** (CI, `GPTADMIN_NONINTERACTIVE=1`): промпт пропускается, автообновление включено по умолчанию, если env не задаёт `false`.
- **Повторный setup с существующим `.env`**: если `GPTADMIN_AUTO_UPDATE` уже записан в env-файл от предыдущего запуска, показать текущий выбор как дефолт (например `[y/N]` если было `false`). Если переменная пришла из process env (не из `.env`) — не спрашивать.

### Разделение service-unit и timer

Сейчас `svc_autoupdate_disable_stop()` удаляет **оба** файла: и service, и timer (`cli.py:2487-2490`). Это надо исправить:

- `write_autoupdate_service_unit()` → **вызывать всегда при setup**, независимо от флага автообновления. Service-unit всегда присутствует на диске.
- `svc_autoupdate_enable_start()` → записывает и включает только **timer**.
- `svc_autoupdate_disable_stop()` → останавливает и удаляет только **timer**; service-unit остаётся.
- `cmd_update` уже перезаписывает все юниты при обновлении — убедиться, что он тоже пишет service-unit всегда.

## 2. Подсказка при запуске CLI

### Кэш-файл

`~/.gptadmin/update_check.json`:

```json
{
  "last_success_ts": 1752100000,
  "last_attempt_ts": 1752100000,
  "remote_version": 120,
  "remote_sha256": "abc..."
}
```

- `last_success_ts` — время последнего успешного фетча манифеста.
- `last_attempt_ts` — время последней **попытки** (успешной или нет).
- Раздельные поля, чтобы после сетевой ошибки не долбиться в сеть каждую команду.

### Атомарная запись

Писать в `update_check.json.tmp`, затем `os.replace(src, dst)`. Права файла `0600` (чувствительных данных почти нет, но дисциплина).

### Повреждённый JSON

`json.load` + исключение → «кэша нет», без traceback. Логгировать в debug, продолжать без подсказки.

### Clock skew

```python
age = now - last_success_ts
fresh = 0 <= age < 86400
```

`age < 0` (часы переведены назад) → не свежий.

### Логика `maybe_update_hint(args)`

Вызывается рано в `main()`. **Пропускается** когда:
- `GPTADMIN_AUTO_UPDATE=true` (подсказка только при выключенном автообновлении);
- `args.auto` True;
- `args.command in ('update', 'auto-update')`;
- `--help`/`-h`.

**Не** пропускается для `version`, `doctor`, `status` — там подсказка уместна.

### Шаг 1: нужна ли проверка вообще

- Если `last_success_ts` свежий (< 24ч) → использовать кэшированный `remote_version`, перейти к сравнению (шаг 3). Сеть не трогаем.
- Если `last_attempt_ts` был менее 1 часа назад и `last_success_ts` протух → **не делаем сетевой запрос** (cooldown после неудачи). Подсказку не показываем, выходим тихо.
- Иначе → шаг 2 (сетевой запрос).

### Шаг 2: сетевой запрос манифеста

- `_remote_artifact_build_info()` с timeout **3 секунды**.
- При ошибке сети / таймауте: обновить `last_attempt_ts` (атомарно), выйти тихо.
- При успехе: обновить и `last_success_ts`, и `last_attempt_ts`, записать `remote_version`/`remote_sha256`.

### Шаг 3: сравнение версий

- Прочитать локальную версию через `_installed_build_info()`.
- **Молча выйти**, если:
  - локальная версия не parseable / `None` / `0` / dev-сборка (`"go-dev"`, `"worktree"`, `"dev"`);
  - `remote_version` не parseable как int;
  - локальная версия >= remote и sha256 совпадает (уже обновлено);
  - локальная версия > remote (dev-сборка новее релиза).
- Если `remote_version > installed_version` → печатать подсказку.

### Шаг 4: подсказка (stderr)

```
ℹ Доступно обновление: build 119 → 120.
  Обновить:          gptadmin update
  Включить авто:     gptadmin auto-update enable
```

В stderr, чтобы не ломать `gptadmin tokens | jq` и подобные парсеры stdout.

## 3. /admin — версии хаба + шелла + кнопка «Обновить»

### 3.1. Бэкенд хаба (`go-hub/internal/hub/server.go`)

#### Состояние обновления — файловое, не в памяти

Файл: `~/.gptadmin/update_state.json` (или `{INSTALL_DIR}/update_state.json`):

```json
{
  "current": {"status": "idle"},
  "last_result": {
    "status": "done",
    "message": "Обновлено: build 119 → 120",
    "started_at": 1752100000,
    "finished_at": 1752100043,
    "from_version": 119,
    "to_version": 120
  }
}
```

`current.status ∈ {"idle", "running"}`. `last_result.status ∈ {"done", "error"}`.

Разделение на `current` и `last_result` принципиально:
- `current` — есть ли **прямо сейчас** активный update (чтобы показать кнопку disabled и защитить от повторного запуска).
- `last_result` — чем закончился **последний** update (даже после рестарта хаба видно).

#### Защита от одновременного запуска

Два уровня:
1. **Файловый lock**: `flock` на `~/.gptadmin/update.lock`. Hub пытается взять эксклюзивную блокировку перед запуском. Занят → 409.
2. **systemd active state** (Linux): перед запуском проверить `systemctl is-active gptadmin-auto-update.service`. Если `active` → 409.
3. При старте хаба: если обнаружен stale lock (процесс-владелец умер), снять блокировку, сбросить `current.status` в `idle`.

#### Запуск обновления — внешний supervisor, не goroutine

**Критическое изменение**: hub НЕ запускает `gptadmin update --auto` как дочерний процесс через `exec.Command`. Вместо этого передаёт выполнение внешнему supervisor, который переживёт рестарт хаба.

**Linux** (systemd):
```bash
systemctl --user start gptadmin-auto-update.service
# или systemctl start gptadmin-auto-update.service для system-установки
```
Это существующий Type=oneshot unit (уже рендерится в `AUTO_UPDATE_SERVICE_TPL`). Поскольку service-unit теперь **всегда установлен** (см. раздел 1), этот вызов работает и при выключенном автообновлении. Timer unit отдельно; его active state не важен для ручного запуска.

**macOS** (launchd):
```bash
nohup /opt/gptadmin/bin/run_auto_update.sh \
  >> ~/.gptadmin/auto-update.log 2>&1 </dev/null &
```
С `setsid()` (новый сеанс/process group), чтобы не зависеть от сигналов родительскому hub.

#### Кто пишет результат

Результат пишет **wrapper `run_auto_update.sh`** (или сам `cmd_update` с флагом `--auto`), **не** hub после `cmd.Wait()`:

```
gptadmin update --auto >> "$LOG" 2>&1
rc=$?
# атомарно записать update_state.json
python3 /opt/gptadmin/client/write_update_result.py \
  --exit-code "$rc" --from "$OLD_VERSION" --to "$NEW_VERSION"
```

Атомарная запись: `update_state.json.tmp` + `os.rename()`.

Перед запуском wrapper обновляет `update_state.json`: `current.status = "running"`. Hub этого не делает — hub только проверяет, что `current.status == "idle"` и запускает wrapper.

#### Ответ `adminOverview`

Расширить текущий `adminOverview` (`server.go:1726`):

- `build` — версия хаба (уже есть).
- `shell_builds` — агрегат по heartbeat:
  ```json
  "shell_builds": {
    "latest": 119,
    "oldest": 118,
    "versions": {"119": 4, "118": 1}
  }
  ```
  Хаб хранит актуальные beat-данные по серверам — агрегируем по `BuildVersion`.
- `update` — текущее состояние из файла `update_state.json` (читается на каждый запрос overview):
  ```json
  "update": {
    "current": {"status": "idle"},
    "last_result": {
      "status": "done",
      "message": "Обновлено: build 119 → 120",
      "started_at": 1752100000,
      "finished_at": 1752100043,
      "from_version": 119,
      "to_version": 120
    }
  }
  ```

#### Новый эндпоинт `POST /admin/api/update`

- Auth: CTL-токен (как у остальных admin-эндпоинтов).
- **Не принимает body** (фикс. команда, не пользовательский ввод).
- Проверка CSRF: CTL-токен в заголовке (не cookie) — дополнительная защита.
- Статусы:
  - `flock` взят + `current.status == "idle"` → запуск supervisor, ответ `202 {"ok":true, "status":"running"}`.
  - `current.status == "running"` или lock занят → `409 {"detail":"update already running"}`.
  - Ошибка запуска supervisor → `500 {"detail":"failed to start update"}`, без раскрытия путей/полного stderr.

### 3.2. Фронтенд (`public/admin/index.html` + `app.js`)

#### Отображение версий (sideMeta / brand area)

```
hub: build 119 (3b22fa3)   shells: 119×4, 118×1
[ Обновить этот узел ]
```

Поясняющая подпись мелким шрифтом: «Обновляет hub и локальный shell на этом сервере».

В `renderAll()` (app.js:138) заполнять из:
- `state.build.build_version` / `state.build.git_commit` → hub.
- `state.shell_builds.versions` → shells (если пусто — показать «—»).

#### Кнопка «Обновить этот узел»

- Клик → `POST /admin/api/update` с CTL-токеном.
- На `202`:
  - Кнопка disabled с текстом «Обновляю…»;
  - Сохранить версию ДО обновления (`updateStartedFromBuild`).
- На `409` → показать «Обновление уже идёт», кнопка disabled.

#### Обработка ожидаемого downtime при рестарте

После старта обновления хаб перезапустится. Polling (15с) получит network error / 502. Это **не фатальная ошибка**:

- Вместо красного alert — muted текст «Сервис перезапускается…».
- Polling продолжается (15с интервал).
- Когда hub отвечает снова — сравнить версию:
  - `newBuild > updateStartedFromBuild` → «Обновлено: build 119 → 120».
  - Иначе → «Обновление запущено, дождитесь результата».
- Показать `last_result.message` из `state.update.last_result` (если `done` / `error`).

#### Текст/стили

- `current.status == "running"` → кнопка `.btn-disabled` с текстом «Обновляю…».
- `last_result.status == "done"` → muted текст с результатом, через 60с скрывается.
- `last_result.status == "error"` → красный текст с `last_result.message`.

## 4. Тестирование

### CLI (`cli.py`)

- Кэш свежий — нет сети, подсказка из кэша.
- Кэш протух — сетевой запрос, обновление кэша, подсказка.
- Повреждённый cache JSON → тихий выход (кэш игнорируется, проверка по cooldown).
- Сетевая ошибка → обновляется `last_attempt_ts`, cooldown 1ч, подсказки нет.
- Таймаут 3с → та же ветка.
- `remote_version` < `installed_version` → тихо.
- `remote_version` == `installed_version`, sha256 совпадает → тихо.
- `installed_version` == `None`/`0`/`"go-dev"` → тихо (dev-сборка).
- `last_check_ts` из будущего (clock skew) → не свежий, сетевой запрос.
- Подсказка в stderr, stdout чистый.
- Команды-исключения: `update`, `auto-update`, `--auto`, `--help` → подсказки нет.
- `GPTADMIN_AUTO_UPDATE=true` → подсказки нет.
- `GPTADMIN_AUTO_UPDATE=false` → подсказка есть (при наличии апдейта).
- Атомарная запись кэша: tmp-файл + rename.
- Права кэша `0600`.
- Формат подсказки соответствует шаблону.

### Hub (`go-hub/internal/hub/server_test.go`)

- `adminOverview` содержит `shell_builds` (пустой / с несколькими версиями / с одной).
- `adminOverview` содержит `update` (читается из файла).
- `POST /admin/api/update`:
  - `202` при `idle` + успешный запуск supervisor.
  - `409` при `running` или занятом lock-файле.
  - `401` без CTL-токена.
  - Без body, игнорирует лишние поля.
- Stale lock detection: после старта хаба lock файл занят умершим процессом → снимается.
- Повреждённый `update_state.json` → читается как `idle` (fail-safe).

### Интеграционный (mock supervisor)

1. `POST /admin/api/update` → `202`.
2. Supervisor (mock) обновляет `update_state.json`: `current.status = "running"`.
3. Повторный `POST` → `409`.
4. Supervisor завершается, пишет `current.status = "idle"`, `last_result.status = "done"`.
5. Новый запрос `overview` видит `idle` + `last_result.done`.
6. Повторный `POST` → снова `202`.

### Ручная проверка

- `gptadmin setup` → промпт про автообновление с дефолтом Yes.
- `GPTADMIN_AUTO_UPDATE=false` → запуск любой команды показывает подсказку (если апдейт доступен).
- `/admin` → версии хаба и шелла отображаются.
- Кнопка «Обновить этот узел» → запускает systemd-oneshot / launchd; UI показывает «Обновляю…», переживает рестарт, показывает результат.

## 5. Что НЕ делаем (YAGNI)

- In-process Go self-update хаба.
- Баннер «new version available» в /admin с авто-фетчем манифеста при каждом поллинге.
- Push-уведомления / Telegram / email о новых версиях.
- Обновление удалённых shell-серверов через кнопку в /admin (только локальный узел).
