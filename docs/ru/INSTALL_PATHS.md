# Пути установки

Где GPT‑Админ находится в каждой ОС, в пользовательском и системном режиме.

## Режимы

Установщик автоматически определяет режим:

- **user-mode** (по умолчанию) — устанавливается в домашний каталог пользователя, запускается от имени пользователя.
  сервис. Никакого sudo/администратора не требуется.
- **system-mode** — устанавливается в масштабе всей системы, запускается от имени пользователя root/system. Используйте только тогда, когда
  вам нужны привилегированные операции (привязка к порту 80, управление системными службами
  для других пользователей и т. д.).

## Пути по ОС

### Линукс

| | пользовательский режим | системный режим |
|---|-----------|-------------|
| Двоичный | `~/.local/share/gptadmin/` | `/opt/gptadmin/` |
| Конфигурация | `~/.config/gptadmin/` | `/etc/gptadmin/` |
| Сервис | `systemctl --user` | `systemctl` (системный модуль) |
| интерфейс командной строки | `~/.local/bin/gptadmin` | `/usr/local/bin/gptadmin` |

### macOS

| | пользовательский режим | системный режим |
|---|-----------|-------------|
| Двоичный | `~/.local/share/gptadmin/` | `/opt/gptadmin/` |
| Конфигурация | `~/.config/gptadmin/` | `/etc/gptadmin/` |
| Сервис | Агенты запуска (`~/Library/LaunchAgents/`) | LaunchDaemons (`/Library/LaunchDaemons/`) |
| интерфейс командной строки | `~/.local/bin/gptadmin` | `/usr/local/bin/gptadmin` |

### Окна

| | пользовательский режим | системный режим |
|---|-----------|-------------|
| Двоичный | `%LOCALAPPDATA%\gptadmin\` | `C:\Program Files\gptadmin\` |
| Конфигурация | `%LOCALAPPDATA%\gptadmin\config\` | `C:\ProgramData\gptadmin\` |
| Сервис | Запланированное задание (при входе пользователя в систему) | Служба Windows (Администратор) |
| интерфейс командной строки | `%LOCALAPPDATA%\gptadmin\gptadmin.exe` | `C:\Program Files\gptadmin\gptadmin.exe` |

## Команды установки

```bash
# Linux / macOS — user-mode (default)
curl -s https://became.bezrabotnyi.com/install.sh | bash

# Linux / macOS — system-mode (when you need root)
curl -s https://became.bezrabotnyi.com/install.sh | sudo bash
```

```powershell
# Windows — user-mode (no Administrator)
iwr -UseBasicParsing https://became.bezrabotnyi.com/install_win.ps1 | iex
```

## Что делает установщик

1. Загружает CLI (`gptadmin.py`) и пакеты.
2. Запускает `gptadmin setup --user` (или `--system`) — интерактивный мастер.
3. Вы выбираете, что устанавливать: хаб + агент, только хаб или только агент.
4. Вы выбираете туннель: автотуннель (FRP/Cloudflare) или собственный домен.
5. Записывает сервисные модули и запускает их.
6. Распечатает ваш **URL-адрес концентратора** и URL-адрес подключения `/connect`. Внутренний агент
   учетные данные хранятся на стороне сервера и никогда не распечатываются для копирования/вставки.

## Удалить

```bash
gptadmin uninstall
```

Удаляет двоичные файлы, конфигурации и сервисные модули. Резервные копии, созданные с помощью
`file_backup` сохраняются в `~/.gptadmin/file-backups/` (или
`/var/lib/gptadmin/file-backups/` в системном режиме).

## См. также

- [Начало работы](./GETTING_STARTED.md)
- [Конфигурация](./CONFIGURATION.md)
- [Хаб](./HUB.md)
