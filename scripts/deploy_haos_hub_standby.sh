#!/usr/bin/env bash
set -euo pipefail

DEPLOY=0
if [[ "${1:-}" == "--deploy" ]]; then
  DEPLOY=1
elif [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  cat <<'USAGE'
Usage: scripts/deploy_haos_hub_standby.sh [--deploy]

Builds the HAOS GPTAdmin Hub Standby add-on context from repo sources.
With --deploy, copies it to HAOS and installs/starts via Supervisor API over Advanced SSH.

Environment:
  ENV_FILE=/etc/gptadmin/gptadmin.env
  OUT_DIR=build/haos-gptadmin-hub-standby
  HAOS_HOST=192.168.2.101
  HAOS_SSH_PORT=2228
  HAOS_SSH_USER=root
  HAOS_SSH_KEY=/home/roomhacker/.ssh/id_rsa
  HAOS_ADDON_DIR=/addons/gptadmin_hub_standby
USAGE
  exit 0
fi

ROOT=$(git rev-parse --show-toplevel)
SRC="$ROOT/deploy/homeassistant/gptadmin_hub_standby"
OUT_DIR="${OUT_DIR:-$ROOT/build/haos-gptadmin-hub-standby}"
ENV_FILE="${ENV_FILE:-/etc/gptadmin/gptadmin.env}"
GO_BIN="${GO_BIN:-/usr/local/go/bin/go}"
HAOS_HOST="${HAOS_HOST:-192.168.2.101}"
HAOS_SSH_PORT="${HAOS_SSH_PORT:-2228}"
HAOS_SSH_USER="${HAOS_SSH_USER:-root}"
HAOS_SSH_KEY="${HAOS_SSH_KEY:-/home/roomhacker/.ssh/id_rsa}"
HAOS_ADDON_DIR="${HAOS_ADDON_DIR:-/addons/gptadmin_hub_standby}"
BUILD_VERSION="${BUILD_VERSION:-haos-standby}"
GIT_COMMIT="${GIT_COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD)}"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR/build" "$OUT_DIR/public"
cp "$SRC/Dockerfile" "$SRC/run.sh" "$OUT_DIR/"
chmod 0755 "$OUT_DIR/run.sh"
cat > "$OUT_DIR/public/index.html" <<'HTML'
<!doctype html><title>GPTAdmin HAOS Standby Hub</title><h1>GPTAdmin HAOS Standby Hub</h1>
HTML

for f in gptadmin-shellmcp.tar.gz rootd.json shellmcp.json; do
  [[ -f "/opt/gptadmin/build/$f" ]] && cp -a "/opt/gptadmin/build/$f" "$OUT_DIR/build/" || true
done

cd "$ROOT/go-hub"
"$GO_BIN" test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build \
  -ldflags "-X github.com/megamen32/gptadmin/go-hub/internal/hub.BuildVersion=$BUILD_VERSION -X github.com/megamen32/gptadmin/go-hub/internal/hub.GitCommit=$GIT_COMMIT" \
  -o "$OUT_DIR/gptadmin_hub" ./cmd/gptadmin-hub
chmod 0755 "$OUT_DIR/gptadmin_hub"

python3 - "$SRC/config.yaml.template" "$OUT_DIR/config.yaml" "$ENV_FILE" <<'PY'
import os
import shlex
import subprocess
import sys
from pathlib import Path

template, out, env_path = map(Path, sys.argv[1:4])
vals = {}
if env_path.exists():
    try:
        env_text = env_path.read_text(encoding='utf-8', errors='ignore')
    except PermissionError:
        env_text = subprocess.check_output(['sudo', '-n', 'cat', str(env_path)], text=True)
    for raw in env_text.splitlines():
        line = raw.strip()
        if not line or line.startswith('#') or '=' not in line:
            continue
        k, v = line.split('=', 1)
        try:
            parts = shlex.split(v)
            v = parts[0] if parts else ''
        except Exception:
            pass
        vals[k] = v

def get(key, default=''):
    return vals.get(key, default)

public = get('HUB_PUBLIC_URL') or get('PUBLIC_ORIGIN') or 'https://u-f1102930.t.gptadmin.bezrabotnyi.com'
repl = {
    '__PUBLIC_ORIGIN__': public,
    '__MCP_RESOURCE__': get('MCP_RESOURCE', public),
    '__HUB_PUBLIC_URL__': public,
    '__HUB_URL__': get('HUB_URL', public),
    '__CTL_TOKEN__': get('CTL_TOKEN'),
    '__MCP_RELAY_AGENT_TOKEN__': get('MCP_RELAY_AGENT_TOKEN'),
    '__SHELLMCP_TOKEN__': get('SHELLMCP_TOKEN'),
    '__OAUTH_CLIENT_SECRET__': get('OAUTH_CLIENT_SECRET') or get('ADMIN_PASSWORD') or get('CTL_TOKEN'),
    '__ADMIN_PASSWORD__': get('ADMIN_PASSWORD'),
    '__MCP_BRIDGE_KEY__': get('MCP_BRIDGE_KEY') or get('CTL_TOKEN'),
    '__OAUTH_PERMISSIVE_REDIRECTS__': get('OAUTH_PERMISSIVE_REDIRECTS', '1'),
    '__OAUTH_PERMISSIVE_RESOURCES__': get('OAUTH_PERMISSIVE_RESOURCES', '1'),
    '__MCP_RELAY_DEFAULT_TIMEOUT__': get('MCP_RELAY_DEFAULT_TIMEOUT', '30'),
    '__MCP_RELAY_POLL_MAX_TIMEOUT__': get('MCP_RELAY_POLL_MAX_TIMEOUT', '55'),
}
s = template.read_text()
for k, v in repl.items():
    s = s.replace(k, v)
out.write_text(s)
PY

python3 - "$OUT_DIR/config.yaml" <<'PY'
import re, sys
s = open(sys.argv[1], encoding='utf-8').read()
s = re.sub(r'((token|secret|password|key): ")[^"]+', r'\1***redacted***', s, flags=re.I)
print(s)
PY
sha256sum "$OUT_DIR/gptadmin_hub"

echo "built=$OUT_DIR"

if [[ "$DEPLOY" != "1" ]]; then
  exit 0
fi

SSH=(ssh -i "$HAOS_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -p "$HAOS_SSH_PORT" "$HAOS_SSH_USER@$HAOS_HOST")
SCP=(scp -q -i "$HAOS_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -P "$HAOS_SSH_PORT" -r)

"${SSH[@]}" "set -e; ts=\$(date +%Y%m%d_%H%M%S); if [ -d '$HAOS_ADDON_DIR' ]; then mkdir -p /backup/gptadmin-hub-standby-src-backups; cp -a '$HAOS_ADDON_DIR' /backup/gptadmin-hub-standby-src-backups/gptadmin_hub_standby.\$ts; fi; rm -rf '$HAOS_ADDON_DIR'; mkdir -p '$HAOS_ADDON_DIR'"
"${SCP[@]}" "$OUT_DIR/"* "$HAOS_SSH_USER@$HAOS_HOST:$HAOS_ADDON_DIR/"

REMOTE=/tmp/haos-gptadmin-hub-standby-install.sh
cat > /tmp/haos-gptadmin-hub-standby-install.sh <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
TOK=${SUPERVISOR_TOKEN:-${HASSIO_TOKEN:-}}
[ -n "$TOK" ] || { echo no_supervisor_token; exit 2; }
api(){
  local method=$1 path=$2 data=${3-}
  local h=/tmp/gptadmin-ha-api.h b=/tmp/gptadmin-ha-api.b
  rm -f "$h" "$b"
  local args=(-sS -D "$h" -o "$b" --connect-timeout 5 --max-time 600 -X "$method" -H "Authorization: Bearer $TOK")
  [[ -n "$data" ]] && args+=(-H "Content-Type: application/json" --data "$data")
  curl "${args[@]}" "http://supervisor$path" || true
  python3 - "$b" "$method" "$path" "$(head -n 1 "$h" 2>/dev/null || true)" <<'PY'
import json, sys
b, method, path, status = sys.argv[1:]
s = open(b, errors='replace').read() if b else ''
out = {'method': method, 'path': path, 'status': status}
try:
    j = json.loads(s) if s else {}
    out['result'] = j.get('result')
    if 'message' in j:
        out['message'] = j.get('message')
    data = j.get('data') if isinstance(j, dict) else None
    if isinstance(data, dict):
        for k in ['slug', 'name', 'state', 'installed', 'version', 'version_latest', 'build', 'available']:
            if k in data:
                out[k] = data[k]
except Exception:
    out['body_head'] = s[:200]
print(json.dumps(out, ensure_ascii=False))
PY
}
api POST /addons/reload '{}'
api GET /addons/local_gptadmin_hub_standby/info
api POST /addons/local_gptadmin_hub_standby/install '{}'
sleep 3
api POST /addons/local_gptadmin_hub_standby/start '{}'
sleep 5
api GET /addons/local_gptadmin_hub_standby/info
REMOTE_SCRIPT
chmod 0755 /tmp/haos-gptadmin-hub-standby-install.sh
scp -q -i "$HAOS_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -P "$HAOS_SSH_PORT" /tmp/haos-gptadmin-hub-standby-install.sh "$HAOS_SSH_USER@$HAOS_HOST:$REMOTE"
"${SSH[@]}" "bash '$REMOTE'"

for i in $(seq 1 60); do
  body=$(curl -sS --connect-timeout 2 --max-time 5 "http://$HAOS_HOST:9001/version" 2>/dev/null || true)
  echo "attempt=$i body=$body"
  grep -q "$BUILD_VERSION" <<<"$body" && break
  sleep 2
done
curl -fsS "http://$HAOS_HOST:9001/healthz"; echo
