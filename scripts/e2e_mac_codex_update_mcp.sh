#!/usr/bin/env bash
# E2E regression test for macOS GPTAdmin update + Codex MCP registration.
# It intentionally runs the real updater and real codex CLI, so it is opt-in.
set -euo pipefail
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/Library/Frameworks/Python.framework/Versions/3.11/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH"

log() { printf '\n[%s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { echo "ERROR: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

[[ "$(uname -s)" == "Darwin" ]] || die "macOS only"
need python3
need curl
need codex
need gptadmin

CONFIG_DIR="${GPTADMIN_CONFIG_DIR:-$HOME/.config/gptadmin}"
ENV_FILE="$CONFIG_DIR/gptadmin.env"
[[ -f "$ENV_FILE" ]] || die "missing $ENV_FILE; install GPTAdmin first"

# shellcheck disable=SC1090
set -a; . "$ENV_FILE"; set +a
HUB_PORT="${HUB_PORT:-9001}"
LOCAL_HUB="http://127.0.0.1:${HUB_PORT}"
PKG_ALL="${GPTADMIN_E2E_PKG_ALL:-https://became.bezrabotnyi.com/gptadmin.tar.gz}"
PKG_HUB="${GPTADMIN_E2E_PKG_HUB:-https://became.bezrabotnyi.com/gptadmin-hub.tar.gz}"
PKG_SHELLMCP="${GPTADMIN_E2E_PKG_SHELLMCP:-https://became.bezrabotnyi.com/gptadmin-shellmcp.tar.gz}"

auth_url() {
  local base="$1"
  printf '%s/authorize?response_type=code&client_id=chatgpt-dynamic&state=e2e&code_challenge=e2e_challenge&code_challenge_method=S256&redirect_uri=http%%3A%%2F%%2F127.0.0.1%%3A60239%%2Fcallback&scope=gptadmin.read+gptadmin.exec' "${base%/}"
}

http_status() {
  curl -sS -o "$2" -w '%{http_code}' --max-time "${3:-15}" "$1"
}

log "1/8 update GPTAdmin in-place"
gptadmin update --user --pkg-all "$PKG_ALL" --pkg-hub "$PKG_HUB" --pkg-shellmcp "$PKG_SHELLMCP"

# reload env after update: installer may change HUB_PUBLIC_URL/PUBLIC_ORIGIN/OAUTH_RESOURCE.
# shellcheck disable=SC1090
set -a; . "$ENV_FILE"; set +a
HUB_PORT="${HUB_PORT:-9001}"
LOCAL_HUB="http://127.0.0.1:${HUB_PORT}"
PUBLIC_HUB="${HUB_PUBLIC_URL:-${PUBLIC_ORIGIN:-}}"
[[ -n "$PUBLIC_HUB" ]] || die "HUB_PUBLIC_URL/PUBLIC_ORIGIN is empty after update"
PUBLIC_HUB="${PUBLIC_HUB%/}"

log "2/8 check launchd services are loaded"
wait_launchd_loaded() {
  local label="$1"
  for i in {1..30}; do
    if launchctl list | grep -q "${label}"; then
      return 0
    fi
    sleep 1
  done
  launchctl list | grep -E 'com[.]gptadmin' || true
  die "${label} is not loaded"
}
if command -v launchctl >/dev/null 2>&1; then
  wait_launchd_loaded 'com[.]gptadmin[.]hub'
  if [[ "${INSTALL_SHELLMCP:-false}" == "true" ]]; then
    wait_launchd_loaded 'com[.]gptadmin[.]shellmcp'
  fi
  if [[ "${FRP_ENABLE:-false}" == "true" ]]; then
    wait_launchd_loaded 'com[.]gptadmin[.]frpc'
  fi
fi

log "3/8 check local hub health"
for i in {1..30}; do
  if curl -fsS --max-time 3 "$LOCAL_HUB/version" >/dev/null; then break; fi
  sleep 1
  [[ "$i" != 30 ]] || die "local hub did not become healthy at $LOCAL_HUB/version"
done
curl -fsS --max-time 5 "$LOCAL_HUB/.well-known/oauth-authorization-server" | python3 -m json.tool >/dev/null

log "4/8 check public OAuth authorize is routed to hub, not FRP 404"
PUB_AUTH_BODY="$(mktemp)"
PUB_AUTH_STATUS="$(http_status "$(auth_url "$PUBLIC_HUB")" "$PUB_AUTH_BODY" 20 || true)"
[[ "$PUB_AUTH_STATUS" == "200" ]] || {
  echo "authorize status=$PUB_AUTH_STATUS url=$(auth_url "$PUBLIC_HUB")" >&2
  sed -n '1,80p' "$PUB_AUTH_BODY" >&2 || true
  die "public /authorize is not HTTP 200"
}
grep -q 'GPTAdmin MCP Authorization' "$PUB_AUTH_BODY" || {
  sed -n '1,120p' "$PUB_AUTH_BODY" >&2 || true
  die "public /authorize did not return GPTAdmin auth page"
}
if grep -qi 'server is powered by frp\|page you requested was not found' "$PUB_AUTH_BODY"; then
  sed -n '1,120p' "$PUB_AUTH_BODY" >&2 || true
  die "public /authorize returned FRP fallback page"
fi

log "5/8 remove/add Codex MCP using public OAuth URL and verify OAuth discovery starts"
codex mcp remove gptadmin >/dev/null 2>&1 || true
CODX_OUT="$(mktemp)"
set +e
python3 - "$PUBLIC_HUB/mcp" "$CODX_OUT" <<'PY'
import subprocess, sys
url, out_path = sys.argv[1], sys.argv[2]
try:
    p = subprocess.run(['codex', 'mcp', 'add', 'gptadmin', '--url', url], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=18)
    rc = p.returncode
    out = p.stdout
except subprocess.TimeoutExpired as e:
    rc = 124
    parts = []
    for value in (e.stdout, e.stderr):
        if isinstance(value, bytes):
            parts.append(value.decode('utf-8', 'replace'))
        elif isinstance(value, str):
            parts.append(value)
    out = ''.join(parts)
open(out_path, 'w').write(out)
raise SystemExit(rc if rc != 124 else 0)
PY
CODX_RC=$?
set -e
if [[ "$CODX_RC" != 0 ]]; then
  cat "$CODX_OUT" >&2
  die "codex mcp add failed before OAuth flow"
fi
grep -q 'Detected OAuth support' "$CODX_OUT" || { cat "$CODX_OUT" >&2; die "Codex did not detect OAuth support"; }
AUTH_FROM_CODEX="$(grep -Eo 'https?://[^ ]+/authorize[^ ]+' "$CODX_OUT" | head -1 || true)"
[[ -n "$AUTH_FROM_CODEX" ]] || { cat "$CODX_OUT" >&2; die "Codex did not print authorize URL"; }
AUTH_BODY2="$(mktemp)"
AUTH_STATUS2="$(http_status "$AUTH_FROM_CODEX" "$AUTH_BODY2" 20 || true)"
[[ "$AUTH_STATUS2" == "200" ]] || { cat "$CODX_OUT" >&2; sed -n '1,80p' "$AUTH_BODY2" >&2 || true; die "Codex authorize URL is not HTTP 200"; }
grep -q 'GPTAdmin MCP Authorization' "$AUTH_BODY2" || { sed -n '1,120p' "$AUTH_BODY2" >&2 || true; die "Codex authorize URL is not GPTAdmin auth page"; }

log "6/8 configure local Codex MCP with bearer env var"
TOKEN_ENV_FILE="$CONFIG_DIR/codex-mcp-token.env"
python3 - "$ENV_FILE" > "$TOKEN_ENV_FILE" <<'PY'
from pathlib import Path
import base64, hashlib, hmac, json, time, sys

def read_env(path):
    env = {}
    for line in Path(path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith('#') or '=' not in line:
            continue
        k, v = line.split('=', 1)
        env[k] = v.strip().strip('"').strip("'")
    return env

def b64url(data):
    return base64.urlsafe_b64encode(data).rstrip(b'=').decode()

env = read_env(sys.argv[1])
secret = env.get('OAUTH_CLIENT_SECRET')
if not secret:
    raise SystemExit('missing OAUTH_CLIENT_SECRET in gptadmin.env')
origin = (env.get('HUB_PUBLIC_URL') or env.get('PUBLIC_ORIGIN') or 'http://127.0.0.1:9001').rstrip('/')
resource = (env.get('MCP_RESOURCE') or origin).rstrip('/')
now = int(time.time())
header = {'alg': 'HS256', 'typ': 'JWT'}
payload = {'sub': 'admin', 'scope': 'gptadmin.read gptadmin.exec', 'client_id': 'codex-local-e2e', 'iss': origin, 'aud': resource, 'iat': now, 'exp': now + 365 * 24 * 3600}
msg = f"{b64url(json.dumps(header, separators=(',', ':')).encode())}.{b64url(json.dumps(payload, separators=(',', ':')).encode())}".encode()
token = msg.decode() + '.' + b64url(hmac.new(secret.encode(), msg, hashlib.sha256).digest())
print('export GPTADMIN_CODEX_MCP_BEARER=' + json.dumps(token))
PY
chmod 600 "$TOKEN_ENV_FILE"
# shellcheck disable=SC1090
. "$TOKEN_ENV_FILE"
launchctl setenv GPTADMIN_CODEX_MCP_BEARER "$GPTADMIN_CODEX_MCP_BEARER" 2>/dev/null || true
codex mcp remove gptadmin >/dev/null 2>&1 || true
codex mcp add gptadmin --url "$LOCAL_HUB/mcp" --bearer-token-env-var GPTADMIN_CODEX_MCP_BEARER >/dev/null

log "7/8 validate Codex config"
codex mcp list --json > /tmp/gptadmin-codex-mcp-list.json
GPTADMIN_E2E_LOCAL_MCP_URL="$LOCAL_HUB/mcp" python3 - <<'PY'
import json
items=json.load(open('/tmp/gptadmin-codex-mcp-list.json'))
g=[x for x in items if x.get('name')=='gptadmin']
assert g, 'gptadmin MCP entry missing'
entry=g[0]
tr=entry.get('transport') or {}
assert tr.get('type') == 'streamable_http', tr
import os
expected_url = os.environ.get('GPTADMIN_E2E_LOCAL_MCP_URL', 'http://127.0.0.1:9001/mcp')
assert tr.get('url') == expected_url, tr
assert tr.get('bearer_token_env_var') == 'GPTADMIN_CODEX_MCP_BEARER', tr
assert entry.get('auth_status') == 'bearer_token', entry
print('codex_config_ok')
PY

log "8/8 validate /mcp initialize with generated bearer"
GPTADMIN_E2E_LOCAL_MCP_URL="$LOCAL_HUB/mcp" python3 - "$TOKEN_ENV_FILE" <<'PY'
from pathlib import Path
import json, urllib.request, sys, os
line=Path(sys.argv[1]).read_text().strip()
token=json.loads(line.split('=',1)[1])
req=urllib.request.Request(os.environ.get('GPTADMIN_E2E_LOCAL_MCP_URL', 'http://127.0.0.1:9001/mcp'), data=json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"codex-update-e2e","version":"1"}}}).encode(), headers={"Authorization":"Bearer "+token,"Content-Type":"application/json","Accept":"application/json, text/event-stream"}, method='POST')
with urllib.request.urlopen(req, timeout=10) as r:
    body=r.read().decode('utf-8','replace')
    assert r.status == 200, r.status
    assert 'gptadmin-hub' in body, body
print('mcp_initialize_ok')
PY

log "OK: update + codex remove/add + OAuth route + bearer MCP passed"
