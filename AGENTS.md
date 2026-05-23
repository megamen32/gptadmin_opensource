# AGENTS instructions

## Обзор
В репозитории два основных Python‑сервиса:

1. **services/rootd.py** – небольшой FastAPI сервер, который запускается от имени root и выполняет низкоуровневые задачи.
2. **services/hub_proxy.py** – прокси, через который клиенты могут обращаться к нескольким экземплярам rootd. Он хранит их метаданные и маршрутизирует вызовы.

`public/openapi.yaml` содержит полное описание API hub_proxy (и через него – rootd). Скрипты `deploy/install_*.sh` и systemd‑юниты демонстрируют развёртывание сервисов. `deploy/setup_nginx.sh` настраивает доступ по HTTPS.

Тесты (`tests/test_rootd.py`, `tests/test_hub.py`) отправляют простые HTTP‑запросы и служат примером использования.

## services/rootd.py
- Эндпоинты: `/exec`, `/file`, `/dir`, `/systemd/...`, `/venv/...`, `/system/info`, `/system/health`, `/heartbeat`.
- Аутентификация – Bearer token (`ROOTD_TOKEN`).
- Параметры логирования и тайм‑аутов задаются через переменные окружения (`LOG_LIMIT_B`, `EXEC_TIMEOUT`, и др.).
- Функция `heartbeat()` периодически отправляет POST на `HUB_URL` для регистрации в hub_proxy.
- Все операции логируются через `logging` в файл `rootd.log` и stdout.

## services/hub_proxy.py
- Принимает `POST /heartbeat` от rootd и сохраняет информацию о сервере (URL, токен, время).
- `GET /servers` возвращает список зарегистрированных rootd с флагом `alive`.
- Все клиентские вызовы имеют форму `/srv/{path}?server=name` и перенаправляются к нужному rootd с подстановкой его токена.
- Проверяет авторизацию через `CTL_TOKEN`.

## Работа c кодом
- Соблюдайте уже используемый стиль (f‑строки, явное логирование).
- Логи пишутся через `logging.getLogger('rootd')` и `logging.basicConfig(...)`.
- Перед отправкой изменений запускайте простые тесты:
  ```bash
  python tests/test_rootd.py
  python tests/test_hub.py
  ```
  При необходимости добавьте полноценные тесты на `pytest` и запускайте `pytest -q`.
