#!/usr/bin/env bash
#set -euo pipefail

DOMAIN="gptadmin.bezrabotnyi.com"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEBROOT="$REPO_DIR/public"
CONF_DIR="/etc/nginx/sites-available"
ENABLED_DIR="/etc/nginx/sites-enabled"

# 1. Установка nginx и certbot
if ! command -v nginx >/dev/null; then
  echo "[*] Installing nginx and certbot..."
  sudo apt update
  sudo apt install -y nginx python3-certbot-nginx
fi

# 2. Подготовка конфигурации Nginx
SITE_CONF="$CONF_DIR/${DOMAIN}.conf"
cat <<EOF | sudo tee "$SITE_CONF"
server {
    listen 80;
    server_name $DOMAIN;

    root $WEBROOT;
    index index.html;

    location /.well-known/acme-challenge/ {
        allow all;
    }

    location / {
        # статические файлы (openapi.json, ai-plugin.json, логотип)
                try_files \$uri @proxy;
    }

    location @proxy {
        proxy_pass http://127.0.0.1:9001;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
EOF

# 3. Включение сайта
sudo ln -sf "$SITE_CONF" "$ENABLED_DIR/"
sudo nginx -t
sudo systemctl reload nginx

# 4. Получение TLS сертификата
if [ ! -f "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" ]; then
  sudo certbot --nginx -d $DOMAIN --non-interactive --agree-tos -m admin@$DOMAIN
else
  echo "[*] Certificate for $DOMAIN already exists."
fi

# 5. Разрешаем редирект на HTTPS
sudo sed -i "s/listen 80;/listen 80;
    return 301 https:\/\/${DOMAIN}\$request_uri;/" "$SITE_CONF"
sudo nginx -t
sudo systemctl reload nginx

echo "[*] Nginx настроен и HTTPS включен на $DOMAIN"
