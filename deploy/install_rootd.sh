#!/usr/bin/env bash
set -euo pipefail

PACKAGE_URL=${PACKAGE_URL:-https://became.bezrabotnyi.com/gptadmin.tar.gz}

ROOT_DIR=$(dirname "$0")
cd "$ROOT_DIR"

HUB_URL=${HUB_URL:-http://localhost:48653}

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
ROOTD_BIN="$TMP_DIR/rootd/dist/rootd"
chmod +x "$ROOTD_BIN"

echo "Generated ROOTD token: $TOKEN"

export ROOTD_TOKEN="$TOKEN"
export HUB_URL="$HUB_URL"

nohup "$ROOTD_BIN" >/tmp/rootd.log 2>&1 &

echo "rootd running and registered to $HUB_URL"
echo "Use ROOTD_TOKEN=$TOKEN for authorization"
