# Управление зависимостями в GPTAdmin

## Компоненты и их зависимости

GPTAdmin состоит из нескольких компонентов с разными требованиями к зависимостям:

### Без зависимостей (только стандартная библиотека Python)

- **`rootd_pure.py`** — минимальный root-демон для голой ОС
  - Использует только `http.server`, `urllib`, `json`, `subprocess`
  - Запуск: `python3 rootd_pure.py`
  
- **`gptadmin.py`** — CLI утилита
  - Использует только стандартную библиотеку
  - Запуск: `python3 gptadmin.py`

### С минимальными зависимостями

- **`rootd_linux.py`** — Linux root-демон
  - Зависимости: `psutil`
  - Запуск: `uv run python rootd_linux.py`
  
- **`rootd_win.py`** — Windows root-демон
  - Зависимости: `psutil`
  - Запуск: `uv run python rootd_win.py`

### С полным набором зависимостей

- **`rootd.py`** — полный root-демон с FastAPI
  - Зависимости: `fastapi`, `uvicorn`, `pydantic`, `requests`, `starlette`
  - Запуск: `uv run python rootd.py`
  
- **`hub_proxy.py`** — Hub прокси-сервер
  - Зависимости: `fastapi`, `httpx`, `pydantic`, `cryptography`, `starlette`
  - Запуск: `uv run python hub_proxy.py`

## Установка и использование uv

### Установка uv
```bash
curl -LsSf https://astral.sh/uv/install.sh | sh
```

### Инициализация проекта
```bash
uv sync
```

Это создаст виртуальное окружение `.venv/` и установит все зависимости.

### Запуск компонентов через uv

```bash
# Запуск с автоматической активацией виртуального окружения
uv run python rootd.py
uv run python hub_proxy.py
uv run python rootd_linux.py

# Запуск тестов
uv run pytest tests/

# Запуск CLI
uv run gptadmin --help
```

### Альтернатива: ручная активация venv

```bash
source .venv/bin/activate
python rootd.py
```

## Для голой ОС (без зависимостей)

Если нужно запустить GPTAdmin на системе без установленных зависимостей:

```bash
# Копируем только rootd_pure.py
curl -O https://your-server/rootd_pure.py

# Запускаем без установки зависимостей
python3 rootd_pure.py
```

Это работает на любой системе с Python 3.10+ без дополнительных пакетов.

## Обновление зависимостей

```bash
# Обновить все зависимости до последних совместимых версий
uv sync --upgrade

# Добавить новую зависимость
uv add package-name

# Удалить зависимость
uv remove package-name
```

## Lock-файл

`uv.lock` содержит точные версии всех установленных пакетов. Этот файл должен быть в Git для воспроизводимости сборок.

```bash
git add uv.lock pyproject.toml
git commit -m "Update dependencies"
```
