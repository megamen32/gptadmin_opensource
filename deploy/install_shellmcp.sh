#!/usr/bin/env bash
set -euo pipefail

PACKAGE_URL=${PACKAGE_URL:-https://github.com/megamen32/gptadmin_opensource/releases/latest/download/gptadmin.tar.gz}

ROOT_DIR=$(dirname "$0")
cd "$ROOT_DIR"

HUB_URL=${HUB_URL:-http://localhost:9001}
GPTADMIN_CONFIG_DIR=${GPTADMIN_CONFIG_DIR:-${HOME}/.config/gptadmin}
SHELLMCP_TOKEN_FILE=${SHELLMCP_TOKEN_FILE:-$GPTADMIN_CONFIG_DIR/shellmcp.token}

if ! command -v curl >/dev/null; then
  echo "curl is required" >&2
  exit 1
fi

if [[ -z "${SHELLMCP_TOKEN:-}" && -r "$SHELLMCP_TOKEN_FILE" ]]; then
  SHELLMCP_TOKEN=$(cat "$SHELLMCP_TOKEN_FILE")
fi
if [[ -z "${SHELLMCP_TOKEN:-}" ]]; then
  SHELLMCP_TOKEN=$(python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
  )
fi
mkdir -p "$(dirname "$SHELLMCP_TOKEN_FILE")"
umask 077
printf '%s\n' "$SHELLMCP_TOKEN" > "$SHELLMCP_TOKEN_FILE"

TMP_DIR=$(mktemp -d)
echo "Downloading package..."
curl -fsSL "$PACKAGE_URL" -o "$TMP_DIR/gptadmin.tar.gz"
tar -xzf "$TMP_DIR/gptadmin.tar.gz" -C "$TMP_DIR"
SHELLMCP_BIN="$TMP_DIR/shellmcp/linux_amd64/shellmcp-go"
[[ -x "$SHELLMCP_BIN" ]] || SHELLMCP_BIN="$TMP_DIR/go-shellmcp/linux_amd64/shellmcp-go"
chmod +x "$SHELLMCP_BIN"

export SHELLMCP_TOKEN
export HUB_URL="$HUB_URL"

nohup "$SHELLMCP_BIN" >/tmp/shellmcp.log 2>&1 &

echo "shellmcp running and registered to $HUB_URL"
echo "ShellMCP credential is persisted at $SHELLMCP_TOKEN_FILE"
