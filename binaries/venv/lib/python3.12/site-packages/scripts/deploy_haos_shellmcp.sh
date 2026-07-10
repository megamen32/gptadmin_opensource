#!/usr/bin/env bash
set -euo pipefail

DEPLOY=0
if [[ "${1:-}" == "--deploy" ]]; then
  DEPLOY=1
elif [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  cat <<'USAGE'
Usage: scripts/deploy_haos_shellmcp.sh [--deploy]

Builds the HAOS GPTAdmin ShellMCP add-on context from repo sources.
With --deploy, copies it to HAOS and installs/rebuilds/starts via Supervisor API over Advanced SSH.

Environment:
  ENV_FILE=/etc/gptadmin/gptadmin.env
  OUT_DIR=build/haos-gptadmin-shellmcp
  HAOS_HOST=192.168.2.101
  HAOS_SSH_PORT=2228
  HAOS_SSH_USER=root
  HAOS_SSH_KEY=/home/roomhacker/.ssh/id_rsa
  HAOS_ADDON_DIR=/addons/gptadmin_shellmcp
  HUB_PUBLIC_KEY_FILE=/etc/gptadmin/hub_ed25519.pub
USAGE
  exit 0
fi

ROOT=$(git rev-parse --show-toplevel)
SRC="$ROOT/deploy/homeassistant/gptadmin_shellmcp"
OUT_DIR="${OUT_DIR:-$ROOT/build/haos-gptadmin-shellmcp}"
ENV_FILE="${ENV_FILE:-/etc/gptadmin/gptadmin.env}"
GO_BIN="${GO_BIN:-/usr/local/go/bin/go}"
HAOS_HOST="${HAOS_HOST:-192.168.2.101}"
HAOS_SSH_PORT="${HAOS_SSH_PORT:-2228}"
HAOS_SSH_USER="${HAOS_SSH_USER:-root}"
HAOS_SSH_KEY="${HAOS_SSH_KEY:-/home/roomhacker/.ssh/id_rsa}"
HAOS_ADDON_DIR="${HAOS_ADDON_DIR:-/addons/gptadmin_shellmcp}"
HUB_PUBLIC_KEY_FILE="${HUB_PUBLIC_KEY_FILE:-/etc/gptadmin/hub_ed25519.pub}"
BUILD_VERSION="${BUILD_VERSION:-108}"
GIT_COMMIT="${GIT_COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD)}"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"
cp "$SRC/Dockerfile" "$SRC/run.sh" "$OUT_DIR/"
chmod 0755 "$OUT_DIR/run.sh"

if [[ ! -f "$HUB_PUBLIC_KEY_FILE" ]]; then
  echo "missing hub public key: $HUB_PUBLIC_KEY_FILE" >&2
  exit 2
fi
cp "$HUB_PUBLIC_KEY_FILE" "$OUT_DIR/hub_ed25519.pub"

cd "$ROOT/go-shellmcp"
"$GO_BIN" test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build \
  -ldflags "-X github.com/megamen32/gptadmin/go-shellmcp/internal/server.BuildVersion=$BUILD_VERSION -X github.com/megamen32/gptadmin/go-shellmcp/internal/server.GitCommit=$GIT_COMMIT" \
  -o "$OUT_DIR/shellmcp-go" ./cmd/shellmcp-go
chmod 0755 "$OUT_DIR/shellmcp-go"

python3 - "$SRC/config.yaml.template" "$OUT_DIR/config.yaml" "$ENV_FILE" "$HAOS_HOST" <<'PY'
import shlex
import subprocess
import sys
from pathlib import Path

template, out, env_path, haos_host = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3]), sys.argv[4]
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

hub = get('HUB_PUBLIC_URL') or get('HUB_URL') or get('PUBLIC_ORIGIN') or 'https://gptadmin.bezrabotnyi.com'
shell_url = f'http://{haos_host}:25900'
repl = {
    '__HUB_URL__': hub,
    '__SHELL_URL__': shell_url,
    '__SHELLMCP_TOKEN__': get('SHELLMCP_TOKEN') or get('SHELL_TOKEN'),
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
sha256sum "$OUT_DIR/shellmcp-go" "$OUT_DIR/hub_ed25519.pub"
echo "built=$OUT_DIR"

if [[ "$DEPLOY" != "1" ]]; then
  exit 0
fi

SSH=(ssh -i "$HAOS_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -p "$HAOS_SSH_PORT" "$HAOS_SSH_USER@$HAOS_HOST")
SCP=(scp -q -i "$HAOS_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -P "$HAOS_SSH_PORT" -r)

"${SSH[@]}" "set -e; ts=\$(date +%Y%m%d_%H%M%S); if [ -d '$HAOS_ADDON_DIR' ]; then mkdir -p /backup/gptadmin-shellmcp-src-backups; cp -a '$HAOS_ADDON_DIR' /backup/gptadmin-shellmcp-src-backups/gptadmin_shellmcp.\$ts; fi; rm -rf '$HAOS_ADDON_DIR'; mkdir -p '$HAOS_ADDON_DIR'"
"${SCP[@]}" "$OUT_DIR/"* "$HAOS_SSH_USER@$HAOS_HOST:$HAOS_ADDON_DIR/"

REMOTE=/tmp/haos-gptadmin-shellmcp-install.sh
cat > /tmp/haos-gptadmin-shellmcp-install.sh <<'REMOTE_SCRIPT'
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
api GET /addons/local_gptadmin_shellmcp/info
api POST /addons/local_gptadmin_shellmcp/install '{}'
sleep 3
api POST /addons/local_gptadmin_shellmcp/rebuild '{}'
sleep 5
api POST /addons/local_gptadmin_shellmcp/start '{}'
sleep 5
api GET /addons/local_gptadmin_shellmcp/info
REMOTE_SCRIPT
chmod 0755 /tmp/haos-gptadmin-shellmcp-install.sh
scp -q -i "$HAOS_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -P "$HAOS_SSH_PORT" /tmp/haos-gptadmin-shellmcp-install.sh "$HAOS_SSH_USER@$HAOS_HOST:$REMOTE"
"${SSH[@]}" "bash '$REMOTE'"

for i in $(seq 1 60); do
  body=$(curl -sS --connect-timeout 2 --max-time 5 "http://$HAOS_HOST:25900/version" 2>/dev/null || true)
  echo "attempt=$i body=$body"
  grep -q 'shellmcp-go' <<<"$body" && break
  sleep 2
done
curl -fsS "http://$HAOS_HOST:25900/version"; echo
