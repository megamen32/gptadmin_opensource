#!/usr/bin/env bash
set -u

CONFIG_FILE="/etc/gptadmin/gptadmin.env"
ADB_BIN="${ADB_BIN:-/usr/bin/adb}"
ADB_USER="${ADB_USER:-roomhacker}"
ADB_HOME="${ADB_HOME:-/home/roomhacker}"
BASE="/data/local/tmp/gptadmin"
RUN="$BASE/run.sh"
RUN_ONCE="$BASE/run-once.sh"
LOG="$BASE/logs/shellmcp.log"
NOHUP="$BASE/logs/nohup.log"
PIDFILE="$BASE/shellmcp.pid"

config_get() {
    awk -F= -v key="$1" '$1==key {sub(/^[^=]*=/, ""); print; exit}' "$CONFIG_FILE" 2>/dev/null
}

SERIAL="${ANDROID_ADB_SERIAL:-$(config_get ANDROID_ADB_SERIAL)}"
PUBLIC_HUB_URL="${HUB_PUBLIC_URL:-$(config_get HUB_PUBLIC_URL)}"

log() {
    printf '[%s] %s\n' "$(date -Is)" "$*"
}

adb_base() {
    local adb_uid adb_gid
    if [[ "$(id -un)" == "$ADB_USER" ]]; then
        "$ADB_BIN" "$@"
    else
        adb_uid="$(id -u "$ADB_USER")"
        adb_gid="$(id -g "$ADB_USER")"
        setpriv --reuid="$adb_uid" --regid="$adb_gid" --init-groups \
            env HOME="$ADB_HOME" "$ADB_BIN" "$@"
    fi
}

adb_dev() {
    adb_base -s "$SERIAL" "$@"
}

phone_sh() {
    adb_dev shell "$@"
}

start_shellmcp() {
    log "consolidating/restarting shellmcp"
    if ! phone_sh "for pid in \$(ps -A -o PID,PPID,ARGS | awk '\$3==\"sh\" && \$4==\"$RUN\" {print \$1}'); do kill \"\$pid\" 2>/dev/null || true; done; \
        pkill -x shellmcp 2>/dev/null || true; sleep 1; mkdir -p '$BASE/logs'; \
        nohup '$RUN' >'$NOHUP' 2>&1 & echo \$! >'$PIDFILE'; sleep 7; \
        test \"\$(ps -A -o PID,PPID,ARGS | awk '\$3==\"sh\" && \$4==\"$RUN\" {print \$1}' | wc -l)\" -eq 1; \
        test \"\$(ps -A -o USER,PID,PPID,NAME | awk '\$1==\"shell\" && \$4==\"shellmcp\" {n++} END {print n+0}')\" -eq 1"; then
        log "status=error reason=duplicate_shellmcp_parents_or_children"
        return 1
    fi
}

log "starting S21 ShellMCP polling maintainer on roomhacker-server-100"
adb_base start-server || { log "adb start-server failed"; exit 0; }
if [[ "$SERIAL" == *:* ]]; then
    adb_base connect "$SERIAL" >/dev/null 2>&1 || true
fi
state="$(adb_base devices | awk -v s="$SERIAL" '$1==s {print $2; exit}')"
if [[ "$state" != "device" ]]; then
    discovered="$(adb_base devices -l | awk '$2=="device" && /model:SM_G998B/ {print $1; exit}')"
    if [[ -n "$discovered" ]]; then
        SERIAL="$discovered"
        state="device"
        log "selected Samsung SM-G998B at $SERIAL"
    fi
fi
log "adb state=${state:-missing}"
if [[ "$state" != "device" ]]; then
    log "device is not connected/authorized via USB; polling runtime is left untouched"
    exit 0
fi

if [[ -z "$PUBLIC_HUB_URL" ]]; then
    PUBLIC_HUB_URL="$(phone_sh "grep '^export HUB_URL=' '$RUN_ONCE' 2>/dev/null | cut -d= -f2-" | tr -d '\r')"
fi
if [[ -z "$PUBLIC_HUB_URL" ]] || ! curl -fsS --max-time 3 "$PUBLIC_HUB_URL/actions/openapi.yaml" >/dev/null 2>&1; then
    log "configured Hub is not reachable; polling runtime is left untouched"
    exit 0
fi

# Remove only the obsolete ShellMCP reverse. This never creates a USB runtime
# route; the unrelated Android 4G proxy forward remains untouched.
adb_dev reverse --remove tcp:9001 >/dev/null 2>&1 || true
phone_sh "if [ -f '$RUN_ONCE' ]; then \
    sed -i 's#^export HUB_URL=.*#export HUB_URL=$PUBLIC_HUB_URL#; s#^export SHELLMCP_HEARTBEAT=.*#export SHELLMCP_HEARTBEAT=0#' '$RUN_ONCE' 2>/dev/null || true; \
    if grep -q '^export GODEBUG=' '$RUN_ONCE'; then sed -i 's#^export GODEBUG=.*#export GODEBUG=netdns=cgo#' '$RUN_ONCE'; \
    else sed -i '/^exec /i export GODEBUG=netdns=cgo' '$RUN_ONCE'; fi; fi" || true

# Preserve the full-debug background grants and the user-selected five-minute
# display timeout. Never reintroduce the old 15-second writer.
phone_sh 'for p in com.termux moe.shizuku.privileged.api; do
  pm path "$p" >/dev/null 2>&1 || continue
  cmd deviceidle whitelist +"$p" >/dev/null 2>&1 || true
  am set-inactive "$p" false >/dev/null 2>&1 || true
  am set-standby-bucket "$p" active >/dev/null 2>&1 || true
  for op in RUN_IN_BACKGROUND RUN_ANY_IN_BACKGROUND WAKE_LOCK SCHEDULE_EXACT_ALARM START_FOREGROUND REQUEST_INSTALL_PACKAGES; do
    appops set "$p" "$op" allow >/dev/null 2>&1 || true
  done
done
settings put global stay_on_while_plugged_in 0
settings put system screen_off_timeout 300000
settings put system screen_brightness 1
svc power stayon false' || true

running="$(phone_sh 'pidof shellmcp 2>/dev/null || true' | tr -d '\r')"
child_count="$(phone_sh "ps -A -o USER,PID,PPID,NAME | awk '\$1==\"shell\" && \$4==\"shellmcp\" {n++} END {print n+0}'" | tr -d '\r')"
parent_count="$(phone_sh "ps -A -o PID,PPID,ARGS | awk '\$3==\"sh\" && \$4==\"$RUN\" {n++} END {print n+0}'" | tr -d '\r')"
current_hub="$(phone_sh "grep '^export HUB_URL=' '$RUN_ONCE' 2>/dev/null | cut -d= -f2-" | tr -d '\r')"
recent_bad="$(phone_sh "now=\$(date +%s); mt=\$(stat -c %Y '$LOG' 2>/dev/null || echo 0); last=\$(tail -30 '$LOG' 2>/dev/null | grep -E 'heartbeat failed|queue poll failed|EOF|connection refused|lookup .*:53' | tail -1 || true); if [ -n \"\$last\" ] && [ \$((now-mt)) -le 180 ]; then echo yes; else echo no; fi" | tr -d '\r')"
if [[ -z "$running" || "$child_count" != "1" || "$parent_count" != "1" || "$recent_bad" == "yes" || "$current_hub" != "$PUBLIC_HUB_URL" ]]; then
    log "shellmcp health=bad child_count=$child_count parent_count=$parent_count recent_bad=$recent_bad"
    start_shellmcp || exit 1
else
    log "shellmcp health=ok child_count=1 parent_count=1 recent_bad=no"
fi

phone_sh "echo phone_date=\$(date); echo shellmcp_pid=\$(pidof shellmcp 2>/dev/null || true); echo timeout=\$(settings get system screen_off_timeout); tail -8 '$LOG' 2>/dev/null || true" || true
log "done"
