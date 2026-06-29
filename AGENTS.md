# AGENTS instructions

## Обзор
В репозитории два основных Python‑сервиса:

1. **services/shellmcp.py** – небольшой FastAPI сервер, который запускается от имени root и выполняет низкоуровневые задачи.
2. **services/gptadmin_hub.py** – прокси, через который клиенты могут обращаться к нескольким экземплярам shellmcp. Он хранит их метаданные и маршрутизирует вызовы.

`public/openapi.yaml` содержит полное описание API gptadmin_hub (и через него – shellmcp). Скрипты `deploy/install_*.sh` и systemd‑юниты демонстрируют развёртывание сервисов. `deploy/setup_nginx.sh` настраивает доступ по HTTPS.

Тесты (`tests/test_shellmcp.py`, `tests/test_hub.py`) отправляют простые HTTP‑запросы и служат примером использования.

## services/shellmcp.py
- Эндпоинты: `/exec`, `/file`, `/dir`, `/systemd/...`, `/venv/...`, `/system/info`, `/system/health`, `/heartbeat`.
- Аутентификация – Bearer token (`SHELLMCP_TOKEN`).
- Параметры логирования и тайм‑аутов задаются через переменные окружения (`LOG_LIMIT_B`, `EXEC_TIMEOUT`, и др.).
- Функция `heartbeat()` периодически отправляет POST на `HUB_URL` для регистрации в gptadmin_hub.
- Все операции логируются через `logging` в файл `shellmcp.log` и stdout.

## services/gptadmin_hub.py
- Принимает `POST /heartbeat` от shellmcp и сохраняет информацию о сервере (URL, токен, время).
- `GET /servers` возвращает список зарегистрированных shellmcp с флагом `alive`.
- Все клиентские вызовы имеют форму `/srv/{path}?server=name` и перенаправляются к нужному shellmcp с подстановкой его токена.
- Проверяет авторизацию через `CTL_TOKEN`.

## Команда "обнови сайт"
Полный цикл обновления website (Next.js):
```bash
bash scripts/update-website.sh
```
Что делает:
1. `git pull` в `website/`
2. `bun run build`
3. `sudo systemctl restart gptadminwebsite-next.service`

## Работа c кодом
- Соблюдайте уже используемый стиль (f‑строки, явное логирование).
- Логи пишутся через `logging.getLogger('shellmcp')` и `logging.basicConfig(...)`.
- Перед отправкой изменений запускайте простые тесты:
  ```bash
  python tests/test_shellmcp.py
  python tests/test_hub.py
  ```
  При необходимости добавьте полноценные тесты на `pytest` и запускайте `pytest -q`.
