#!/usr/bin/env bash
set -euo pipefail

# Installer for the rootd agent. Requires HUB_URL environment variable.
# Generates a token and starts rootd which will connect to the hub.

ROOT_DIR=$(dirname "$0")
cd "$ROOT_DIR"

HUB_URL=${HUB_URL:-http://localhost:9001}

if ! command -v python3 >/dev/null; then
  echo "python3 is required" >&2
  exit 1
fi

TOKEN=$(python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
)

echo "Generated ROOTD token: $TOKEN"

export ROOTD_TOKEN="$TOKEN"
export HUB_URL="$HUB_URL"
pip3 install -r requirements.txt >/dev/null

nohup python3 rootd.py >/tmp/rootd.log 2>&1 &

echo "rootd running and registered to $HUB_URL"
echo "Use ROOTD_TOKEN=$TOKEN for authorization"
