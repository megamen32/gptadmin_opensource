# bezhook

Публичный туннель для локального порта одной командой — на своём железе,
почти бесплатно. `ngrok`/`localtunnel`, только шлюзы — наши серверы
(node-100, server01, server01), а URL живёт на нашем wildcard-домене
`t.became.bezrabotnyi.com`.

```
bezhook 3000 --sub myapp
  URL:      https://myapp.t.became.bezrabotnyi.com
  local:    127.0.0.1:3000
  gateways: became.bezrabotnyi.com:7000, server01.bezrabotnyi.com:27000, server01.bezrabotnyi.com:27001
  ttl:      72h0m0s  (--ttl 0 = unlimited)
  [ok] became.bezrabotnyi.com:7000 online
  [ok] server01.bezrabotnyi.com:27000 online
  [ok] server01.bezrabotnyi.com:27001 online
```

## Установка одной командой

Linux / macOS:

```sh
curl -fsSL https://dl.t.became.bezrabotnyi.com/bezhook/install.sh | sh
```

Windows (PowerShell):

```powershell
iwr -useb https://dl.t.became.bezrabotnyi.com/bezhook/install.ps1 | iex
```

Один статический бинарник, никаких зависимостей (npx/pipx/npm не нужны).
Ставится в `~/.local/bin` (Linux/macOS) или `%USERPROFILE%\.local\bin` (Windows).

## Использование

```
bezhook <порт>                     случайный субдомен u-fNNNNNNN, TTL 72ч
bezhook 3000 --sub myapp           фиксированный субдомен
bezhook 3000 --sub myapp --ttl 0   без авто-остановки
bezhook 8080 --host 203.0.113.10    пробросить порт другого хоста в LAN
bezhook --port 3000                флаг вместо позиционного аргумента
```

### Другие провайдеры под капотом: cloudflared и ngrok

`--backend` выбирает основной провайдер (по умолчанию `frp` — наш флот),
`--also` запускает дополнительные одновременно:

```
bezhook 3000 --also cloudflared          # frp-флот + quick tunnel Cloudflare
bezhook 3000 --backend cloudflared       # только Cloudflare (без нашего флота)
bezhook 3000 --backend ngrok             # только ngrok (нужен NGROK_AUTHTOKEN)
```

- **cloudflared** (бесплатные quick tunnels): бинарник ищется в PATH,
  `~/.bezhook/bin`, `~/.local/bin`; URL вида `https://x-y-z.trycloudflare.com`
  печатается по мере получения. ПЕРЕД ЗАПУСКОМ quick-туннеля в этой сети
  (домашний провайдер) он у нас стабильно отдаёт 530/1033 — известная беда
  CF free tier; именованные туннели Cloudflare (как у хаба) работают. На
  «здоровых» к CF сетях провайдер работает как есть.
- **ngrok** (free for personal use): нужен установленный `ngrok` и
  `NGROK_AUTHTOKEN` в окружении (бесплатный аккаунт). URL берётся из
  локального agent API (:4040). У free-тарифа браузерам показывается
  warning-страница — для API-запросов добавляй заголовок
  `ngrok-skip-browser-warning`.
- Случайные URL: `--sub` работает только на `frp`; cloudflared/ngrok выдают
  свой случайный адрес, который печатается строкой `[ok] <provider> <URL>`.
- TTL и Ctrl-C действуют на все провайдеры сразу.

Флаги: `--sub`, `--port`, `--host`, `--ttl` (напр. `30m`, `72h`, `0`), `--endpoints host:port,...`,
`--token`, `--domain`, `--config`, `--quiet` (печатает только URL).

Переменные окружения: `BEZHOOK_TOKEN`, `BEZHOOK_ENDPOINTS`, `BEZHOOK_DOMAIN`,
`BEZHOOK_TLS_SERVER_NAME`. Конфиг-файл `~/.bezhook.toml` (все поля опциональны):

```toml
token = "..."
domain = "t.became.bezrabotnyi.com"
endpoints = ["became.bezrabotnyi.com:7000", "server01.bezrabotnyi.com:27000", "server01.bezrabotnyi.com:27001"]
default_ttl = "72h"
```

Приоритет: флаги > переменные окружения > `~/.bezhook.toml` > встроенные дефолты.

## Как это работает

- Под капотом — клиент **frp v0.64.0, слинкованный как библиотека** (без
  внешнего процесса `frpc` и без скачивания бинарников при установке).
  Версия обязана совпадать с frps на шлюзах (`/etc/frp*/frps.toml`, флот
  обновляется вместе).
- **Fanout-фейловер**: клиент держит по одному соединению к каждому шлюзу
  (node-100/домашний вход — best-effort, server01 и server01 — always-on)
  (node-100/домашний вход, server01, server01) и регистрирует один и тот же
  субдомен на всех. Пока жив любой шлюз — туннель работает. Публичный URL
  один: `https://<sub>.t.became.bezrabotnyi.com` (wildcard-сертификат на
  всех трёх nginx).
- **TTL**: туннель живёт, пока запущен процесс (heartbeat-ы frp держат
  соединение даже без трафика). По умолчанию через 72 ч процесс завершится
  сам (`--ttl 0` — без лимита). Остановил/Ctrl-C — субдомен мгновенно
  освобождается (проверено: шлюзы отдают 404).
- Токен и адреса шлюзов зашиты в дефолты (те же, что в `cli.py`,
  `FRPC_SERVER_ENDPOINTS`); для чужого flota переопределяются
  `--endpoints/--token/--domain`.

## Ограничения (честно)

- **HTTP/HTTPS vhost** (включая websockets) — как у frp `type=http`. TCP-порты
  вне HTTP не пробрасывает (можно добавить `--tcp` поверх frp `type=tcp`).
- Канонический URL с самой машины node-100 может не открываться — домашний
  NAT не делает hairpin. Из интернета работает; с сервера проверяй напрямую:
  `curl --resolve <sub>.t.became.bezrabotnyi.com:443:185.240.120.152 https://<sub>.t.became.bezrabotnyi.com/`
  (или 212.192.31.128 для server01).
- Вход DNS `*.t.became.bezrabotnyi.com` → 203.0.113.10 (домашний). Если
  домашний канал лёг, туннели живы на server01/server01 и включаются, как только
  ingress-failover (node-mesh) переключит DNS; вручную — `--resolve`.
- Windows-бинарник собирается и подписывается checksum-ами, но
  runtime-тестов на Windows из этой среды не было — только Linux-прогон.

## Разработка

```
cd bezhook
make test      # go test ./...
make dist      # кросс-компиляция в dist/dl + SHA256SUMS
make deploy    # rsync на server01:/var/www/bezhook (nginx отдаёт /bezhook/)
```

Исходники: `cmd/bezhook/main.go` (CLI, TTL, сигналы), `internal/bezhook/config.go`
(дефолты/слои конфига), `internal/bezhook/fanout.go` (frp-сервис на каждый шлюз,
мониторинг фаз прокси).
