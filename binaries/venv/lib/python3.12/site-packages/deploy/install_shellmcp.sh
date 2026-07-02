#!/usr/bin/env bash
set -euo pipefail

PACKAGE_URL=${PACKAGE_URL:-https://became.bezrabotnyi.com/gptadmin.tar.gz}

ROOT_DIR=$(dirname "$0")
cd "$ROOT_DIR"

HUB_URL=${HUB_URL:-http://localhost:9001}

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
SHELLMCP_BIN="$TMP_DIR/shellmcp/dist/shellmcp"
chmod +x "$SHELLMCP_BIN"

echo "Generated SHELLMCP token: $TOKEN"

export SHELLMCP_TOKEN="$TOKEN"
export HUB_URL="$HUB_URL"

nohup "$SHELLMCP_BIN" >/tmp/shellmcp.log 2>&1 &

echo "shellmcp running and registered to $HUB_URL"
echo "Use SHELLMCP_TOKEN=$TOKEN for authorization"
