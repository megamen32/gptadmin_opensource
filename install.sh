#!/usr/bin/env bash
set -euo pipefail

# Simple installer for the hub proxy service.
# Generates a control token, installs python dependencies and starts the hub.

ROOT_DIR=$(dirname "$0")
cd "$ROOT_DIR"

if ! command -v python3 >/dev/null; then
  echo "python3 is required" >&2
  exit 1
fi

TOKEN=$(python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
)

echo "Generated token: $TOKEN"

export CTL_TOKEN="$TOKEN"
pip3 install -r requirements.txt >/dev/null

# start hub proxy in background
nohup uvicorn hub_proxy:app --host 0.0.0.0 --port 9001 >/tmp/hub_proxy.log 2>&1 &

IP=$(hostname -I | awk '{print $1}')
echo "Hub running at http://$IP:9001"
echo "Use CTL_TOKEN=$TOKEN for authorization"
