# Управление зависимостями в GPTAdmin

## Компоненты и их зависимости

GPTAdmin состоит из нескольких компонентов с разными требованиями к зависимостям:

### Без зависимостей (Go binaries)

- **`go-shellmcp/`** — Go ShellMCP root-демон (Linux/macOS/Windows/Android)
  - Использует только Go stdlib + минимальный набор сторонних пакетов
  - Сборка: `tools/build.sh shellmcp` → `build/go-shellmcp/<platform>/<arch>/shellmcp-go`
  - Запуск собранного бинаря: `./build/go-shellmcp/linux_amd64/shellmcp-go`

- **`gptadmin.py`** — CLI утилита (Python)
  - Использует только стандартную библиотеку
  - Запуск: `python3 gptadmin.py`

### Go-сервисы

- **`go-hub/`** — Go Hub прокси-сервер
  - Зависимости: `fastapi`, `httpx`, `pydantic`, `cryptography`, `starlette` (используются тестами хаба на Python)
  - Сборка: `tools/build.sh hub` → `build/gptadmin_hub/dist/gptadmin_hub`
  - Запуск собранного бинаря: `./build/gptadmin_hub/dist/gptadmin_hub`

> **Примечание.** Legacy Python `shellmcp*.py` (`shellmcp.py`, `shellmcp_pure.py`,
> `shellmcp_linux.py`, `shellmcp_win.py`, `shellmcp_mac.py`, `shellmcp_ssh.py`)
> удалены из дерева исходников; единственный путь развертывания ShellMCP — Go-бинарь.

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
go run ./go-shellmcp/cmd/shellmcp-go
go run ./go-hub/cmd/gptadmin-hub

# Запуск тестов
uv run pytest tests/

# Запуск CLI
uv run gptadmin --help
```

### Альтернатива: ручная активация venv

```bash
source .venv/bin/activate
```

## Для голой ОС (без зависимостей)

ShellMCP поставляется как статически собранный Go-бинарь и не требует Python
на хосте. Достаточно одного бинаря `shellmcp-go` (`go-shellmcp/<platform>/<arch>/`)
— никаких дополнительных зависимостей.

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
