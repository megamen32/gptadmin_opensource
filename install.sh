#!/usr/bin/env bash
set -euo pipefail

PACKAGE_URL=${PACKAGE_URL:-https://became.bezrabotnyi.com/gptadmin.tar.gz}

ROOT_DIR=$(dirname "$0")
cd "$ROOT_DIR"

if ! command -v curl >/dev/null; then
  echo "curl is required" >&2
  exit 1
fi

TOKEN=$(python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
)

TMP_DIR=$(mktemp -d)
echo "Downloading package..."
curl -fsSL "$PACKAGE_URL" -o "$TMP_DIR/gptadmin.tar.gz"
tar -xzf "$TMP_DIR/gptadmin.tar.gz" -C "$TMP_DIR"
HUB_BIN="$TMP_DIR/hub_proxy/dist/hub_proxy"
chmod +x "$HUB_BIN"

echo "Generated token: $TOKEN"

export CTL_TOKEN="$TOKEN"

# start hub proxy in background
nohup "$HUB_BIN" >/tmp/hub_proxy.log 2>&1 &

IP=$(hostname -I | awk '{print $1}')
echo "Hub running at http://$IP:9001"
echo "Use CTL_TOKEN=$TOKEN for authorization"
