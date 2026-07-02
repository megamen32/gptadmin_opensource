#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="/opt/gptadmin"
ARCHIVE_URL=${PACKAGE_URL:-"https://became.bezrabotnyi.com/gptadmin.tar.gz"}
LOG_DIR="/var/log/gptadmin"

read -rp "Enter Bearer token for API: " BEARER_TOKEN
read -rp "Enter ngrok auth token: " NGROK_TOKEN

# Download and unpack release
TMP_DIR=$(mktemp -d)
echo "Downloading package..."
curl -fsSL "$ARCHIVE_URL" -o "$TMP_DIR/gptadmin.tar.gz"
sudo mkdir -p "$INSTALL_DIR"
sudo tar -xzf "$TMP_DIR/gptadmin.tar.gz" -C "$INSTALL_DIR"
rm -rf "$TMP_DIR"
sudo chmod +x "$INSTALL_DIR/shellmcp/dist/shellmcp" "$INSTALL_DIR/gptadmin_hub/dist/gptadmin_hub"

# Prepare logs
sudo mkdir -p "$LOG_DIR"
sudo chown "$(whoami)" "$LOG_DIR"

# shellmcp service
sudo tee /etc/systemd/system/shellmcp.service >/dev/null <<EOR
[Unit]
Description=Root Daemon for GPT Control (shellmcp)
After=network.target

[Service]
ExecStart=$INSTALL_DIR/shellmcp/dist/shellmcp
Environment=SHELLMCP_TOKEN=$BEARER_TOKEN
Environment=SHELLMCP_URL=http://\$(hostname):25900
Environment=HUB_URL=http://127.0.0.1:8000/heartbeat
Environment=LOG_DIR=$LOG_DIR
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOR

# gptadmin_hub service
sudo tee /etc/systemd/system/gptadmin_hub.service >/dev/null <<EOH
[Unit]
Description=Hub Proxy for GPT Server Management (gptadmin_hub)
After=network.target

[Service]
ExecStart=$INSTALL_DIR/gptadmin_hub/dist/gptadmin_hub
Environment=CTL_TOKEN=$BEARER_TOKEN
Environment=LOG_DIR=$LOG_DIR
Restart=always
RestartSec=5
User=$(whoami)

[Install]
WantedBy=multi-user.target
EOH

# ngrok service for gptadmin_hub
sudo tee /etc/systemd/system/ngrok-hub.service >/dev/null <<EON
[Unit]
Description=Expose gptadmin_hub via ngrok
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/ngrok http 8000 --log=stdout
Restart=on-failure
User=$(whoami)

[Install]
WantedBy=multi-user.target
EON

# Authorise ngrok
ngrok config add-authtoken "$NGROK_TOKEN"

# Enable and start services
sudo systemctl daemon-reload
sudo systemctl enable shellmcp gptadmin_hub ngrok-hub
sudo systemctl restart shellmcp gptadmin_hub ngrok-hub

# Obtain public URL
sleep 5
PUBLIC_URL=$(curl -s http://127.0.0.1:4040/api/tunnels | python -c "import sys, json; print(json.load(sys.stdin)['tunnels'][0]['public_url'])")
echo "ngrok public URL: $PUBLIC_URL"
echo "$PUBLIC_URL" | sudo tee "$INSTALL_DIR/ngrok_url.txt"

# Verify services
sudo systemctl status shellmcp --no-pager
sudo systemctl status gptadmin_hub --no-pager
