#!/usr/bin/env bash
set -euo pipefail

OPTIONS=/data/options.json

for key in SUPERVISOR_TOKEN HASSIO_TOKEN; do
  env_file="/run/s6/container_environment/${key}"
  if [[ -f "$env_file" ]]; then
    export "$key=$(cat "$env_file")"
  fi
done

opt() {
  local key="$1" default="$2"
  if [[ -f "$OPTIONS" ]]; then
    jq -er --arg key "$key" '.[$key] // empty' "$OPTIONS" 2>/dev/null || printf '%s' "$default"
  else
    printf '%s' "$default"
  fi
}

HUB_URL="$(opt hub_url 'https://gptadmin.bezrabotnyi.com')"
SHELL_NAME="$(opt shell_name 'server-01')"
SHELL_URL="$(opt shell_url 'http://203.0.113.10:25900')"
SHELL_TOKEN="$(opt shell_token '')"
SHELL_PORT="$(opt port '25900')"
SHELL_QUEUE="$(opt queue 'true')"
SHELL_HEARTBEAT="$(opt heartbeat 'true')"
SHELL_DEFAULT_CWD="$(opt default_cwd '/config')"
EXEC_TIMEOUT="$(opt exec_timeout '300')"
LOG_LIMIT_B="$(opt log_limit_b '8192')"

if [[ -z "$SHELL_TOKEN" ]]; then
  if [[ -f /data/shell_token ]]; then
    SHELL_TOKEN="$(cat /data/shell_token)"
  else
    SHELL_TOKEN="$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(32))
PY
)"
    printf '%s' "$SHELL_TOKEN" > /data/shell_token
    chmod 0600 /data/shell_token
  fi
fi

ID_DIR=/data/identity
mkdir -p "$ID_DIR" /data/spool /data/outbox
chmod 0700 "$ID_DIR"

if [[ ! -f "$ID_DIR/shellmcp_ed25519" ]]; then
  openssl genpkey -algorithm ED25519 -out "$ID_DIR/shellmcp_ed25519" >/dev/null 2>&1
  chmod 0600 "$ID_DIR/shellmcp_ed25519"
fi

cp /opt/gptadmin/hub_ed25519.pub "$ID_DIR/hub_ed25519.pub"
chmod 0644 "$ID_DIR/hub_ed25519.pub"

if [[ ! -f "$ID_DIR/shellmcp_identity.json" ]]; then
  openssl pkey -in "$ID_DIR/shellmcp_ed25519" -pubout -outform DER -out "$ID_DIR/shellmcp_pub.der" >/dev/null 2>&1
  SHELL_NAME="$SHELL_NAME" python3 - <<'PY'
import base64, hashlib, json, os, time, uuid
id_dir = '/data/identity'
der = open(id_dir + '/shellmcp_pub.der', 'rb').read()
pub = der[-32:]
public_key = base64.urlsafe_b64encode(pub).decode().rstrip('=')
fingerprint = 'SHA256:' + base64.urlsafe_b64encode(hashlib.sha256(pub).digest()).decode().rstrip('=')
identity = {
    'created_at': int(time.time()),
    'fingerprint': fingerprint,
    'name': os.environ.get('SHELL_NAME') or 'server-01',
    'public_key': public_key,
    'server_id': 'haos-' + str(uuid.uuid4()),
}
open(id_dir + '/shellmcp_identity.json', 'w').write(json.dumps(identity, indent=2, sort_keys=True) + '\n')
PY
  rm -f "$ID_DIR/shellmcp_pub.der"
  chmod 0600 "$ID_DIR/shellmcp_identity.json"
fi

export HUB_URL SHELL_NAME SHELL_URL SHELL_TOKEN SHELL_PORT EXEC_TIMEOUT LOG_LIMIT_B
export SHELL_HOST="0.0.0.0"
export SHELL_SPOOL_DIR="/data/spool"
export SHELL_OUTBOX_DIR="/data/outbox"
export SHELL_IDENTITY_DIR="$ID_DIR"
export HUB_PUBLIC_KEY_FILE="$ID_DIR/hub_ed25519.pub"
export SHELL_MODE="long_poll"
export SHELL_QUEUE="$( [[ "$SHELL_QUEUE" == true || "$SHELL_QUEUE" == 1 ]] && echo 1 || echo 0 )"
export SHELL_HEARTBEAT="$( [[ "$SHELL_HEARTBEAT" == true || "$SHELL_HEARTBEAT" == 1 ]] && echo 1 || echo 0 )"
export SHELL_DEFAULT_USER="root"
export SHELL_DEFAULT_HOME="/root"
export SHELL_DEFAULT_CWD
export SHELLMCP_DEFAULT_USER="root"
export SHELLMCP_DEFAULT_CWD="$SHELL_DEFAULT_CWD"

printf 'Starting GPTAdmin ShellMCP: name=%s url=%s hub=%s queue=%s heartbeat=%s\n' \
  "$SHELL_NAME" "$SHELL_URL" "$HUB_URL" "$SHELL_QUEUE" "$SHELL_HEARTBEAT"
exec /usr/local/bin/shellmcp-go
