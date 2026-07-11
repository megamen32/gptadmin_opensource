#!/bin/bash
set -euo pipefail
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/Library/Frameworks/Python.framework/Versions/3.11/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH"
section() { printf '\n===== %s =====\n' "$1"; }

section 'START FULL GPTADMIN REINSTALL ON MAC'
echo "host=$(hostname) user=$(whoami) date_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
uname -a
sw_vers 2>/dev/null || true

echo
section 'DELETE OLD GPTADMIN LAUNCHD SERVICES'
UIDN=$(id -u)
for label in $(launchctl list | awk '/com\.gptadmin/ {print $3}' | sort -u); do
  echo "bootout gui/$UIDN/$label"
  launchctl bootout "gui/$UIDN/$label" 2>&1 || true
done
for plist in "$HOME"/Library/LaunchAgents/com.gptadmin*.plist; do
  [ -e "$plist" ] || continue
  label=$(basename "$plist" .plist)
  echo "bootout/remove plist $label $plist"
  launchctl bootout "gui/$UIDN/$label" 2>&1 || true
  rm -f "$plist"
done

echo
section 'KILL LEFTOVER GPTADMIN PROCESSES'
pkill -f gptadmin_hub 2>/dev/null || true
pkill -f shellmcp 2>/dev/null || true
pkill -f frpc 2>/dev/null || true
sleep 1
ps auxww | grep -E 'gptadmin_hub|shellmcp|frpc|run_hub|run_shellmcp|run_frpc' | grep -v grep || echo 'no leftover processes'

echo
section 'DELETE OLD GPTADMIN FILES'
for p in \
  "$HOME/.local/share/gptadmin" \
  "$HOME/.config/gptadmin" \
  "$HOME/.gptadmin" \
  "$HOME/Library/Logs/gptadmin" \
  "$HOME/.local/bin/gptadmin"; do
  if [ -e "$p" ]; then
    echo "rm -rf $p"
    rm -rf "$p"
  else
    echo "absent $p"
  fi
done
mkdir -p "$HOME/Library/Logs/gptadmin"

echo
section 'VERIFY CLEAN STATE BEFORE INSTALL'
launchctl list | grep -E 'com\.gptadmin' || echo 'no com.gptadmin launchd jobs'
find "$HOME/.local/share" "$HOME/.config" "$HOME/Library/LaunchAgents" -maxdepth 2 \( -name '*gptadmin*' -o -name 'com.gptadmin*' \) -print 2>/dev/null || true

echo
section 'RUN PUBLIC WEBSITE INSTALLER: FULL HUB + SHELLMCP + FRP + POLLING'
echo 'site command: curl -s https://became.bezrabotnyi.com/install.sh | bash'
echo 'answers sent to installer TTY: 1=hub+shellmcp, 1=FRP, 1=polling, y=import Claude MCP servers'
PY=/Library/Frameworks/Python.framework/Versions/3.11/bin/python3
[ -x "$PY" ] || PY=$(command -v python3)
export GPTADMIN_DOWNLOAD_QUIET=1
"$PY" <<'PY_RUN_PUBLIC_INSTALLER'
import os
import pty
import re
import select
import sys
import time

cmd = "curl -s https://became.bezrabotnyi.com/install.sh | bash"
answers = ["1\n", "1\n", "1\n", "y\n"]
answer_idx = 0
buffer = ""

def mask_sensitive(text: str) -> str:
    text = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "***JWT_MASKED***", text)
    text = re.sub(r"((?:Authorization:\s*Bearer|GPTADMIN_[A-Z0-9_]*(?:TOKEN|BEARER)|CTL_TOKEN|ROOTD_TOKEN)\s*(?:=>|=|:)\s*)\S+", r"\1***MASKED***", text)
    return text

pid, fd = pty.fork()
if pid == 0:
    os.environ["GPTADMIN_DOWNLOAD_QUIET"] = "1"
    os.execl("/bin/bash", "bash", "-lc", cmd)

os.set_blocking(fd, False)
exit_status = None
last_output = time.time()
try:
    while True:
        r, _, _ = select.select([fd], [], [], 0.1)
        if fd in r:
            try:
                data = os.read(fd, 4096)
            except BlockingIOError:
                data = b""
            except OSError:
                data = b""
            if data:
                last_output = time.time()
                text = data.decode("utf-8", "replace")
                sys.stdout.write(mask_sensitive(text))
                sys.stdout.flush()
                buffer += text
                # install.sh uses /dev/tty for prompts under curl|bash, so answers must go to the PTY.
                while answer_idx < len(answers) and ("Ваш выбор" in buffer or "Импортировать" in buffer):
                    os.write(fd, answers[answer_idx].encode())
                    sys.stdout.write(answers[answer_idx])
                    sys.stdout.flush()
                    answer_idx += 1
                    buffer = ""
            else:
                pass
        try:
            waited_pid, status = os.waitpid(pid, os.WNOHANG)
        except ChildProcessError:
            waited_pid, status = pid, 0
        if waited_pid == pid:
            exit_status = status
            break
        if time.time() - last_output > 900:
            raise TimeoutError("installer produced no output for 900s")
finally:
    try:
        os.close(fd)
    except OSError:
        pass

if exit_status is None:
    raise SystemExit(1)
if os.WIFEXITED(exit_status):
    raise SystemExit(os.WEXITSTATUS(exit_status))
if os.WIFSIGNALED(exit_status):
    raise SystemExit(128 + os.WTERMSIG(exit_status))
raise SystemExit(1)
PY_RUN_PUBLIC_INSTALLER

echo
section 'POST-INSTALL ENV SUMMARY'
ENV="$HOME/.config/gptadmin/gptadmin.env"
if [ -f "$ENV" ]; then
  grep -E '^(INSTALL_HUB|INSTALL_SHELLMCP|FRP_ENABLE|TUNNEL_MODE|HUB_PUBLIC_URL|HUB_URL|QUEUE_URL|FRP_SUBDOMAIN|FRP_DOMAIN|FRP_SERVER_ADDR|FRP_SERVER_PORT|GPTADMIN_HOME|GPTADMIN_CONFIG_DIR|HUB_PORT|SHELLMCP_TRANSPORT)=' "$ENV" || true
else
  echo 'MISSING env file'
fi

echo
section 'POST-INSTALL LAUNCHD STATUS'
launchctl list | grep -E 'com\.gptadmin\.(hub|shellmcp|frpc)' || true
mask_sensitive_output() {
  python3 -c '
import re, sys
text = sys.stdin.read()
text = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "***JWT_MASKED***", text)
text = re.sub(r"((?:GPTADMIN_[A-Z0-9_]*(?:TOKEN|BEARER)|CTL_TOKEN|ROOTD_TOKEN)\s*(?:=>|=)\s*)\S+", r"\1***MASKED***", text)
sys.stdout.write(text)
'
}

for label in com.gptadmin.hub com.gptadmin.shellmcp com.gptadmin.frpc; do
  echo "--- $label ---"
  launchctl print "gui/$UIDN/$label" 2>&1 | mask_sensitive_output | sed -n '1,55p' || true
done

echo
section 'POST-INSTALL BINARIES'
ls -la "$HOME/.local/share/gptadmin/bin" || true
for f in "$HOME/.local/share/gptadmin/bin/gptadmin_hub" "$HOME/.local/share/gptadmin/bin/run_hub.sh" "$HOME/.local/share/gptadmin/bin/run_shellmcp.sh" "$HOME/.local/share/gptadmin/bin/run_frpc.sh" "$HOME/.local/share/gptadmin/bin/frpc"; do
  echo "--- $f ---"
  [ -e "$f" ] && { ls -lh "$f"; file "$f" 2>/dev/null || true; } || echo 'MISSING'
done

echo
section 'POST-INSTALL LOCAL HEALTH'
curl -fsS -i --max-time 10 http://127.0.0.1:9001/version | sed -n '1,20p' || true

echo
section 'POST-INSTALL PUBLIC HEALTH'
PUB=$(grep '^HUB_PUBLIC_URL=' "$ENV" | sed 's/^HUB_PUBLIC_URL=//' | tail -1)
echo "public_url=$PUB"
for i in $(seq 1 60); do
  code=$(curl -k -sS -o /tmp/gptadmin-public-version.out -w '%{http_code}' --max-time 5 "$PUB/version" 2>/tmp/gptadmin-public-version.err || true)
  echo "attempt=$i http_code=$code"
  if [ "$code" = "200" ]; then
    cat /tmp/gptadmin-public-version.out
    break
  fi
  sleep 2
done

echo
section 'POST-INSTALL MCP 401 WITHOUT BEARER'
curl -sS -i --max-time 10 "$PUB/mcp" | sed -n '1,14p' || true

echo
section 'POST-INSTALL LOCAL SERVERS VIA CTL TOKEN'
set -a; . "$ENV"; set +a
curl -fsS --max-time 10 -H "Authorization: Bearer $CTL_TOKEN" http://127.0.0.1:9001/servers | python3 -m json.tool 2>/dev/null | sed -n '1,160p' || true

echo
section 'DONE FULL GPTADMIN REINSTALL ON MAC'
date -u '+date_utc=%Y-%m-%dT%H:%M:%SZ'
