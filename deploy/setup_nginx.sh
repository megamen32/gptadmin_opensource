#!/usr/bin/env bash
set -euo pipefail

DOMAIN="${GPTADMIN_DOMAIN:-gptadmin.bezrabotnyi.com}"
SITE_DOMAIN="${GPTADMIN_SITE_DOMAIN:-became.bezrabotnyi.com}"
CONF_DIR="/etc/nginx/sites-available"
ENABLED_DIR="/etc/nginx/sites-enabled"
SITE_CONF="$CONF_DIR/${DOMAIN}.conf"
ACME_ROOT="${GPTADMIN_ACME_ROOT:-/var/www/letsencrypt-gptadmin}"
CERT_DIR="/etc/letsencrypt/live/$DOMAIN"

if ! command -v nginx >/dev/null; then
  echo "[*] Installing nginx and certbot..."
  sudo apt update
  sudo apt install -y nginx certbot
fi

sudo mkdir -p "$CONF_DIR" "$ENABLED_DIR" "$ACME_ROOT"

write_bootstrap_http() {
  cat <<NGINX | sudo tee "$SITE_CONF" >/dev/null
server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN www.$DOMAIN;

    location ^~ /.well-known/acme-challenge/ {
        root $ACME_ROOT;
        default_type text/plain;
        try_files \$uri =404;
    }

    location / {
        return 301 https://$SITE_DOMAIN\$request_uri;
    }
}
NGINX
}

write_final_split() {
  cat <<NGINX | sudo tee "$SITE_CONF" >/dev/null
# Stable GPTAdmin compatibility/API hostname.
# Protocol/API traffic goes to the active MCP2 Hub; browser/site traffic stays
# on $SITE_DOMAIN. OAuth issuer/resource may remain a per-user origin during
# the compatibility migration, so clients should follow returned metadata.
server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN www.$DOMAIN;

    location ^~ /.well-known/acme-challenge/ {
        root $ACME_ROOT;
        default_type text/plain;
        try_files \$uri =404;
    }

    location ~ ^/(?:mcp(?:\$|-relay/|-prompt/)|server/|agent/|actions/|oauth/|\\.well-known/oauth-|register\$|authorize\$|token\$|connect(?:\$|\\.json\$|/)|artifacts/|heartbeat\$|queue/|tasks/|version\$|healthz\$|api/v1/cloud-os/|secret-input/|_services/|proxy-control/|proxy-agent/) {
        return 308 https://$DOMAIN\$request_uri;
    }

    location / {
        return 301 https://$SITE_DOMAIN\$request_uri;
    }
}

server {
    listen 127.0.0.1:8444 ssl;
    server_name $DOMAIN www.$DOMAIN;

    ssl_certificate     $CERT_DIR/fullchain.pem;
    ssl_certificate_key $CERT_DIR/privkey.pem;

    location ~ ^/(?:mcp(?:\$|-relay/|-prompt/)|server/|agent/|actions/|oauth/|\\.well-known/oauth-|register\$|authorize\$|token\$|connect(?:\$|\\.json\$|/)|artifacts/|heartbeat\$|queue/|tasks/|version\$|healthz\$|api/v1/cloud-os/|secret-input/|_services/|proxy-control/|proxy-agent/) {
        proxy_pass http://gptadmin_hub_active;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_buffering off;
    }

    location / {
        return 301 https://$SITE_DOMAIN\$request_uri;
    }
}
NGINX
}

sudo ln -sfn "$SITE_CONF" "$ENABLED_DIR/${DOMAIN}.conf"

if [ ! -f "$CERT_DIR/fullchain.pem" ] || [ ! -f "$CERT_DIR/privkey.pem" ]; then
  echo "[*] Bootstrapping ACME for $DOMAIN..."
  write_bootstrap_http
  sudo nginx -t
  sudo systemctl reload nginx
  sudo certbot certonly --webroot -w "$ACME_ROOT" -d "$DOMAIN" \
    --non-interactive --agree-tos -m "admin@$DOMAIN"
fi

write_final_split
sudo nginx -t
sudo systemctl reload nginx

echo "[*] $DOMAIN: MCP/API -> gptadmin_hub_active; web -> https://$SITE_DOMAIN/"
