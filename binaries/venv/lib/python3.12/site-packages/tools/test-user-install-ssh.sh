#!/usr/bin/env bash
set -Eeuo pipefail

SSH_TARGET="${SSH_TARGET:-user@127.0.0.1}"
SSH_PORT="${SSH_PORT:-2222}"
INSTALL_URL="${INSTALL_URL:-https://became.bezrabotnyi.com/install.sh}"
REMOTE_TIMEOUT="${REMOTE_TIMEOUT:-420}"
USER_CHOICES="${USER_CHOICES:-$'\n\n\n\nn\n\n\n'}"
SSH_OPTS=(-p "$SSH_PORT" -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no -o UserKnownHostsFile="${SSH_KNOWN_HOSTS:-$HOME/.ssh/known_hosts_gptadmin_install_test}" -o ConnectTimeout="${SSH_CONNECT_TIMEOUT:-10}")

if [[ -n "${SSH_PASSWORD:-}" ]]; then
  command -v sshpass >/dev/null 2>&1 || { echo "ERROR: SSH_PASSWORD set but sshpass not found" >&2; exit 127; }
  SSH=(sshpass -p "$SSH_PASSWORD" ssh "${SSH_OPTS[@]}" "$SSH_TARGET")
else
  SSH=(ssh "${SSH_OPTS[@]}" "$SSH_TARGET")
fi

remote() { "${SSH[@]}" "$@"; }
installer_ssh_tty() {
  if [[ -n "${SSH_PASSWORD:-}" ]]; then
    sshpass -p "$SSH_PASSWORD" ssh -tt "${SSH_OPTS[@]}" "$SSH_TARGET" "$@"
  else
    ssh -tt "${SSH_OPTS[@]}" "$SSH_TARGET" "$@"
  fi
}

cleanup_remote_user_install='set -euo pipefail
TS=$(date +%Y%m%d_%H%M%S)
BACKUP="$HOME/gptadmin-install-test-backups/automated-$TS"
mkdir -p "$BACKUP"
for p in "$HOME/.config/gptadmin" "$HOME/.local/share/gptadmin" "$HOME/.local/bin/gptadmin" "$HOME/Library/LaunchAgents/com.gptadmin.shellmcp.plist" "$HOME/Library/LaunchAgents/com.gptadmin.auto-update.plist" "$HOME/Library/LaunchAgents/com.gptadmin.hub.plist" "$HOME/Library/LaunchAgents/com.gptadmin.tunnel-frpc.plist" "$HOME/Library/Logs/gptadmin"; do
  if [ -e "$p" ]; then
    base=$(basename "$p"); parent=$(basename "$(dirname "$p")")
    tar -czf "$BACKUP/${parent}_${base}.tgz" -C "$(dirname "$p")" "$base"
  fi
done
UID_NOW=$(id -u)
for label in com.gptadmin.shellmcp com.gptadmin.auto-update com.gptadmin.hub com.gptadmin.tunnel-frpc com.gptadmin.frpc com.gptadmin.cloudflared; do
  launchctl bootout "gui/$UID_NOW/$label" >/dev/null 2>&1 || true
  launchctl remove "$label" >/dev/null 2>&1 || true
done
for plist in "$HOME"/Library/LaunchAgents/com.gptadmin*.plist; do
  [ -e "$plist" ] && launchctl bootout "gui/$UID_NOW" "$plist" >/dev/null 2>&1 || true
done
pkill -f "$HOME/.local/share/gptadmin/bin/shellmcp" >/dev/null 2>&1 || true
pkill -f "$HOME/.local/share/gptadmin/bin/gptadmin_hub" >/dev/null 2>&1 || true
pkill -f "$HOME/.local/share/gptadmin/bin/frpc" >/dev/null 2>&1 || true
sleep 1
rm -rf "$HOME/.config/gptadmin" "$HOME/.local/share/gptadmin" "$HOME/.local/bin/gptadmin" "$HOME/Library/Logs/gptadmin"
rm -f "$HOME"/Library/LaunchAgents/com.gptadmin*.plist
echo "BACKUP_DIR=$BACKUP"'

validate_remote_install='set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
[ -x "$HOME/.local/bin/gptadmin" ]
[ -x "$HOME/.local/share/gptadmin/bin/gptadmin_hub" ]
[ -x "$HOME/.local/share/gptadmin/bin/shellmcp" ]
[ -x "$HOME/.local/share/gptadmin/bin/frpc" ]
[ -f "$HOME/.config/gptadmin/shellmcp_ed25519" ]
[ -f "$HOME/.config/gptadmin/shellmcp_ed25519.pub" ]
[ -f "$HOME/.config/gptadmin/shellmcp_identity.json" ]
gptadmin status | tee /tmp/gptadmin-install-status.txt
grep -q "com.gptadmin.hub.*running" /tmp/gptadmin-install-status.txt
grep -q "com.gptadmin.shellmcp.*running" /tmp/gptadmin-install-status.txt
grep -q "com.gptadmin.tunnel-frpc.*running" /tmp/gptadmin-install-status.txt
file "$HOME/.local/share/gptadmin/bin/shellmcp" | grep -q "Mach-O"
TOKEN=$(grep ^CTL_TOKEN= "$HOME/.config/gptadmin/gptadmin.env" | cut -d= -f2-)
SHELL_TOKEN=$(grep ^SHELLMCP_TOKEN= "$HOME/.config/gptadmin/gptadmin.env" | cut -d= -f2-)
HUB_PUBLIC_URL=$(grep ^HUB_PUBLIC_URL= "$HOME/.config/gptadmin/gptadmin.env" | cut -d= -f2-)
curl -fsS http://127.0.0.1:9001/version >/tmp/gptadmin-install-hub-version.json
curl -fsS http://127.0.0.1:9001/servers -H "Authorization: Bearer $TOKEN" >/tmp/gptadmin-install-servers.json
python3 - <<PY
import json
s=json.load(open("/tmp/gptadmin-install-servers.json"))
assert len(s.get("servers", [])) >= 1, s
assert not s.get("pending"), s
assert any(x.get("status") == "active" and x.get("alive") for x in s.get("servers", [])), s
PY
curl -fsS http://127.0.0.1:25900/version >/tmp/gptadmin-install-shell-version.json
curl -fsS http://127.0.0.1:25900/system/health -H "Authorization: Bearer $SHELL_TOKEN" >/tmp/gptadmin-install-shell-health.json
python3 - <<PY
import json
h=json.load(open("/tmp/gptadmin-install-shell-health.json"))
assert h.get("ok") is True and h.get("queue") is True and h.get("heartbeat") is True, h
PY
curl -k -fsS "$HUB_PUBLIC_URL/version" >/tmp/gptadmin-install-public-version.json
printf "OK user install: %s\n" "$HUB_PUBLIC_URL"'

echo "== SSH smoke =="
remote 'hostname; id; uname -a'

echo "== Remove previous user install =="
remote "$cleanup_remote_user_install"

echo "== Normal user installer =="
if command -v timeout >/dev/null 2>&1; then
  if [[ -n "${SSH_PASSWORD:-}" ]]; then
    printf '%s' "$USER_CHOICES" | timeout "$REMOTE_TIMEOUT" sshpass -p "$SSH_PASSWORD" ssh -tt "${SSH_OPTS[@]}" "$SSH_TARGET" "bash -lc 'curl -fsSL $INSTALL_URL | bash'"
  else
    printf '%s' "$USER_CHOICES" | timeout "$REMOTE_TIMEOUT" ssh -tt "${SSH_OPTS[@]}" "$SSH_TARGET" "bash -lc 'curl -fsSL $INSTALL_URL | bash'"
  fi
else
  printf '%s' "$USER_CHOICES" | installer_ssh_tty "bash -lc 'curl -fsSL $INSTALL_URL | bash'"
fi

echo "== Validate install =="
remote "$validate_remote_install"
