# Туннели

Хаб должен быть доступен из Интернета (чтобы ваши клиенты AI/MCP могли
подключить). Вам не нужен собственный домен — GPT‑Админ поддерживает два автотуннеля.

## FRP (по умолчанию)

[FRP](https://github.com/fatedier/frp) — быстрый обратный прокси-сервер. GPT‑Админ запускает
общедоступный FRP-сервер; установщик может автоматически зарегистрироваться с ним.

### Настройка

Во время `gptadmin setup` выберите вариант **1** (автотуннелирование через FRP). Установщик:
1. Загружает клиент FRP.
2. Регистрирует случайный поддомен на общедоступном сервере FRP.
3. Запускает клиент FRP как службу рядом с хабом.
4. Распечатывает общедоступный URL-адрес: `https://random-sub.frp.bezrabotnyi.com`.

### Плюсы/минусы

- ✅ Домен не требуется, настройка DNS не требуется.
- ✅ Быстрый (прямой TCP-туннель)
- ⚠️ URL находится на `frp.bezrabotnyi.com` (общий домен)
- ⚠️ Бесплатный FRP-сервер имеет ограничения по скорости

## Туннель Cloudflare

[Туннель Cloudflare](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
создает безопасный исходящий туннель к границе Cloudflare. Вам нужен Cloudflare
учетная запись и домен на Cloudflare.

### Настройка

Во время `gptadmin setup` выберите опцию Cloudflare. Или настройте позже:

```bash
gptadmin tunnel cloudflare
```

Вам понадобится:
- `CLOUDFLARE_TOKEN` — токен API Cloudflare с разрешениями туннеля.
- Домен, управляемый Cloudflare.

Интерфейс командной строки:
1. Устанавливает `cloudflared`
2. Создает туннель
3. Привязывает его к поддомену вашего домена Cloudflare.
4. Запускает `cloudflared` как службу.
5. Распечатывает общедоступный URL-адрес: `https://hub.yourdomain.com`.

### Плюсы/минусы

- ✅ Свой домен
- ✅ Защита от DDoS-атак Cloudflare + пограничное кэширование
- ✅ На вашем сервере не нужны входящие порты
- ⚠️ Требуется учетная запись Cloudflare + домен.

## Свой домен (nginx + Certbot)

Если у вас уже есть сервер с публичным IP и доменом:

1. Направьте запись DNS A на свой сервер.
2. Используйте предоставленный шаблон конфигурации nginx: `deploy/nginx/` (скопируйте и отредактируйте).
3. Получите сертификат: `certbot --nginx -d hub.yourdomain.com`.
4. Запускаем хаб на локальном хосте, к нему проксирует nginx

```bash
# Example nginx location block
location / {
    proxy_pass http://127.0.0.1:25900;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";  # for MCP SSE
}
```

## Что выбрать?

| Вариант использования | Рекомендуется |
|----------|-------------|
| Быстрый старт, без домена | FRP (автотоннель) |
| Есть домен, нужна защита от DDoS | Туннель Cloudflare |
| Уже есть сервер + домен | nginx + Certbot |
| Только локальные разработчики | нет (используйте `localhost:25900`) |

## См. также

- [Начало работы](./GETTING_STARTED.md)
- [Конфигурация](./CONFIGURATION.md) — `PUBLIC_ORIGIN` и т. д.
- [Хаб](./HUB.md)
