#!/usr/bin/env bash
set -euo pipefail

CONFIG_FILE="${GPTADMIN_CONFIG_FILE:-/etc/gptadmin/gptadmin.env}"
TOKEN_FILE="${ANDROID_S21_MCP_TOKEN_FILE:-/etc/gptadmin/android-s21-mcp.env}"
ADB_BIN="${ADB_BIN:-/usr/local/bin/adb}"
ADB_USER="${ADB_USER:-roomhacker}"
ADB_HOME="${ADB_HOME:-/home/roomhacker}"
CONFIGURED_SERIAL="${ANDROID_ADB_SERIAL:-}"
APP_PACKAGE="com.danielealbano.androidremotecontrolmcp"
APP_RECEIVER="$APP_PACKAGE/com.danielealbano.androidremotecontrolmcp.services.mcp.AdbConfigReceiver"
APP_TRAMPOLINE="$APP_PACKAGE/com.danielealbano.androidremotecontrolmcp.services.mcp.AdbServiceTrampolineActivity"
ACCESSIBILITY_COMPONENT="$APP_PACKAGE/$APP_PACKAGE.services.accessibility.McpAccessibilityService"
PHONE_MCP_BIND="127.0.0.1"
PHONE_MCP_PORT="8080"
FULL_TOOL_PERMISSIONS='{"disabledTools":[],"disabledParams":{}}'
EXPECTED_APP_VERSION="1.10.0"
EXPECTED_APK_SHA256="b1a7cf0836c232776449367ab797ecd1c04ee174daa68b948ebcadafb71b53be"
EXPECTED_TOOL_COUNT="58"
EXPECTED_TOOLS_SHA256="cfaa792fa4a9585a461922fd51d9d61000f7ae4b7273b2d2eac3cb42e8198bfa"
BASE="/data/local/tmp/gptadmin"
CONFIG_DIR="$BASE/config"
RUN="$BASE/run.sh"
RUN_ONCE="$BASE/run-once.sh"
PHONE_TOKEN_FILE="$CONFIG_DIR/android-s21-mcp.env"
PHONE_MCP_CONFIG="$CONFIG_DIR/mcp-supervisor.json"
RUN_ONCE_BACKUP="$BASE/backups/run-once.pre-android-mcp-polling.sh"
MCP_CONFIG_BACKUP="$BASE/backups/mcp-supervisor.pre-android-mcp-polling.json"
MCP_CONFIG_ABSENT="$BASE/backups/mcp-supervisor.pre-android-mcp-polling.absent"
ACTION="${1:-apply}"

log() {
    printf 'android_s21_polling_bootstrap %s\n' "$*"
}

env_value() {
    local path="$1" key="$2"
    awk -F= -v key="$key" '$1==key {sub(/^[^=]*=/, ""); print; exit}' "$path" 2>/dev/null
}

if [[ -z "$CONFIGURED_SERIAL" ]]; then
    CONFIGURED_SERIAL="$(env_value "$CONFIG_FILE" ANDROID_ADB_SERIAL || true)"
fi
ANDROID_S21_MCP_TOKEN=""

adb_base() {
    local adb_uid adb_gid
    if [[ "${ADB_RUN_DIRECT:-0}" == "1" || "$(id -un)" == "$ADB_USER" ]]; then
        "$ADB_BIN" "$@"
    else
        adb_uid="$(id -u "$ADB_USER")"
        adb_gid="$(id -g "$ADB_USER")"
        setpriv --reuid="$adb_uid" --regid="$adb_gid" --init-groups \
            env HOME="$ADB_HOME" "$ADB_BIN" "$@"
    fi
}

SERIAL=""
adb_dev() {
    adb_base -s "$SERIAL" "$@"
}

phone_sh() {
    adb_dev shell "$@"
}

resolve_serial() {
    SERIAL="$(adb_base devices -l | awk '$2=="device" && /model:SM_G998B/ && /usb:/ {print $1; exit}')"
    if [[ -z "$SERIAL" && -n "$CONFIGURED_SERIAL" ]]; then
        if adb_base devices | awk -v s="$CONFIGURED_SERIAL" '$1==s && $2=="device" {found=1} END {exit !found}'; then
            SERIAL="$CONFIGURED_SERIAL"
        fi
    fi
    [[ -n "$SERIAL" ]]
}

verify_apk() {
    local apk_path observed version
    version="$(phone_sh dumpsys package "$APP_PACKAGE" 2>/dev/null | awk -F= '/versionName=/{print $2; exit}' | tr -d '\r')"
    [[ "$version" == "$EXPECTED_APP_VERSION" ]] || {
        log "status=error reason=unexpected_app_version observed=${version:-missing}"
        return 1
    }
    apk_path="$(phone_sh pm path "$APP_PACKAGE" | sed -n 's/^package://p' | tr -d '\r' | head -n 1)"
    [[ -n "$apk_path" ]] || return 1
    observed="$(adb_dev exec-out cat "$apk_path" | sha256sum | awk '{print $1}')"
    [[ "$observed" == "$EXPECTED_APK_SHA256" ]] || {
        log "status=error reason=unexpected_apk_sha256 observed=$observed"
        return 1
    }
}

backup_phone_state() {
    local rollback_sha256
    rollback_sha256="$(phone_sh "umask 077; mkdir -p '$BASE/backups'; \
        if [ ! -f '$RUN_ONCE_BACKUP' ]; then cp -p '$RUN_ONCE' '$RUN_ONCE_BACKUP'; chmod 0600 '$RUN_ONCE_BACKUP'; fi; \
        if [ ! -f '$MCP_CONFIG_BACKUP' ] && [ ! -f '$MCP_CONFIG_ABSENT' ]; then \
            if [ -f '$PHONE_MCP_CONFIG' ]; then cp -p '$PHONE_MCP_CONFIG' '$MCP_CONFIG_BACKUP'; chmod 0600 '$MCP_CONFIG_BACKUP'; \
            else : >'$MCP_CONFIG_ABSENT'; chmod 0600 '$MCP_CONFIG_ABSENT'; fi; \
        fi; sha256sum '$RUN_ONCE_BACKUP' | awk '{print \$1}'" | tr -d '\r')"
    [[ -n "$rollback_sha256" ]] || { log 'status=error reason=phone_backup_failed'; return 1; }
    log "rollback_sha256=$rollback_sha256 rollback_path=$RUN_ONCE_BACKUP"
}

install_phone_token() {
    # The credential travels on stdin. It is never part of sudo/setpriv/adb argv.
    printf 'export ANDROID_S21_MCP_TOKEN=%s\n' "$ANDROID_S21_MCP_TOKEN" | \
        adb_dev shell "umask 077; mkdir -p '$CONFIG_DIR'; tmp='$PHONE_TOKEN_FILE.tmp'; cat > \"\$tmp\"; chmod 0600 \"\$tmp\"; mv \"\$tmp\" '$PHONE_TOKEN_FILE'"
}

configure_shellmcp_launcher() {
    local hub_before hub_after
    hub_before="$(phone_sh "grep '^export HUB_URL=' '$RUN_ONCE' | sha256sum | awk '{print \$1}'" | tr -d '\r')"
    phone_sh "tmp='$RUN_ONCE.tmp'; awk '
        /^\\. \\/data\\/local\\/tmp\\/gptadmin\\/config\\/android-s21-mcp\\.env$/ {next}
        /^export SHELLMCP_MCP_CONFIG=/ {next}
        /^exec / && !inserted {
            print \". $PHONE_TOKEN_FILE\"
            print \"export SHELLMCP_MCP_CONFIG=$PHONE_MCP_CONFIG\"
            inserted=1
        }
        {print}
    ' '$RUN_ONCE' >\"\$tmp\"; chmod 0700 \"\$tmp\"; mv \"\$tmp\" '$RUN_ONCE'"
    hub_after="$(phone_sh "grep '^export HUB_URL=' '$RUN_ONCE' | sha256sum | awk '{print \$1}'" | tr -d '\r')"
    [[ -n "$hub_before" && "$hub_before" == "$hub_after" ]] || {
        log 'status=error reason=hub_url_changed'
        return 1
    }
}

merge_phone_child_config() {
    local temp_dir current merged
    temp_dir="$(mktemp -d)"
    chmod 0755 "$temp_dir"
    current="$temp_dir/current.json"
    merged="$temp_dir/merged.json"
    if ! phone_sh "test -f '$PHONE_MCP_CONFIG'" >/dev/null 2>&1; then
        printf '[]\n' >"$current"
    elif ! adb_dev exec-out cat "$PHONE_MCP_CONFIG" >"$current" 2>/dev/null; then
        log 'status=error reason=phone_mcp_config_read_failed'
        rm -rf "$temp_dir"
        return 1
    fi
    python3 - "$current" "$merged" <<'PY'
import json
import os
import sys

source, target = sys.argv[1:]
raw = open(source, encoding="utf-8").read().strip()
agents = json.loads(raw) if raw else []
if not isinstance(agents, list):
    raise SystemExit("phone MCP config must use the JSON array shape")
child = {
    "ref": "AndroidRemoteControl-S21",
    "name": "Android Remote Control MCP on S21",
    "transport": "streamable-http",
    "url": "http://127.0.0.1:8080/mcp",
    "headers": {"Authorization": "Bearer ${ANDROID_S21_MCP_TOKEN}"},
    "enabled": True,
}
agents = [agent for agent in agents if agent.get("ref") != child["ref"]]
agents.append(child)
with open(target, "w", encoding="utf-8") as handle:
    json.dump(agents, handle, indent=2, sort_keys=True)
    handle.write("\n")
os.chmod(target, 0o600)
PY
    chmod 0644 "$merged"
    adb_dev push "$merged" "$PHONE_MCP_CONFIG.tmp" >/dev/null
    phone_sh "chmod 0600 '$PHONE_MCP_CONFIG.tmp'; mv '$PHONE_MCP_CONFIG.tmp' '$PHONE_MCP_CONFIG'"
    rm -rf "$temp_dir"
}

configure_phone_mcp() {
    phone_sh "token=\$(sed -n 's/^export ANDROID_S21_MCP_TOKEN=//p' '$PHONE_TOKEN_FILE'); [ -n \"\$token\" ]; \
        am broadcast -a '$APP_PACKAGE.ADB_CONFIGURE' -n '$APP_RECEIVER' \
        --ez bearer_token_enabled true --es bearer_token \"\$token\" \
        --ez oauth_enabled false --es binding_address '$PHONE_MCP_BIND' --ei port '$PHONE_MCP_PORT' \
        --ez auto_start_on_boot true --ez https_enabled false --ez tunnel_enabled false \
        --es device_slug s21 --es tool_permissions '$FULL_TOOL_PERMISSIONS' >/dev/null"
    phone_sh am start -n "$APP_TRAMPOLINE" --es action stop >/dev/null
    sleep 2
    phone_sh am start -n "$APP_TRAMPOLINE" --es action start >/dev/null
}

ensure_full_debug_accessibility() {
    phone_sh appops set "$APP_PACKAGE" ACCESS_RESTRICTED_SETTINGS allow
    phone_sh "current=\$(settings get secure enabled_accessibility_services); \
        case \"\$current\" in \
            *'$ACCESSIBILITY_COMPONENT'*) ;; \
            null|'') settings put secure enabled_accessibility_services '$ACCESSIBILITY_COMPONENT' ;; \
            *) settings put secure enabled_accessibility_services \"\$current:$ACCESSIBILITY_COMPONENT\" ;; \
        esac; settings put secure accessibility_enabled 1"
}

restart_shellmcp_polling() {
    # Historical maintainers could leave several run.sh parents. Consolidate
    # them before reload so exactly one long-poll client survives.
    if ! phone_sh "for pid in \$(ps -A -o PID,PPID,ARGS | awk '\$3==\"sh\" && \$4==\"$RUN\" {print \$1}'); do kill \"\$pid\" 2>/dev/null || true; done; \
        pkill -x shellmcp 2>/dev/null || true; sleep 1; mkdir -p '$BASE/logs'; \
        nohup '$RUN' >'$BASE/logs/nohup.log' 2>&1 & sleep 7; \
        test \"\$(ps -A -o PID,PPID,ARGS | awk '\$3==\"sh\" && \$4==\"$RUN\" {print \$1}' | wc -l)\" -eq 1 || exit 41; \
        test \"\$(ps -A -o USER,PID,PPID,NAME | awk '\$1==\"shell\" && \$4==\"shellmcp\" {n++} END {print n+0}')\" -eq 1 || exit 42"; then
        log 'status=error reason=duplicate_shellmcp_parents_or_children'
        return 1
    fi
    phone_sh "pid=\$(ps -A -o USER,PID,PPID,NAME | awk '\$1==\"shell\" && \$4==\"shellmcp\" {print \$2; exit}'); tr '\\0' '\\n' </proc/\$pid/environ | \
        grep -qx 'SHELLMCP_MODE=long_poll' && \
        tr '\\0' '\\n' </proc/\$pid/environ | grep -qx 'SHELLMCP_MCP_CONFIG=$PHONE_MCP_CONFIG' && \
        tr '\\0' '\\n' </proc/\$pid/environ | grep -q '^ANDROID_S21_MCP_TOKEN='"
}

verify_phone_local_state() {
    phone_sh "dumpsys activity services '$APP_PACKAGE' | grep -q 'services.mcp.McpServerService'"
    phone_sh "grep -q '\"ref\": \"AndroidRemoteControl-S21\"' '$PHONE_MCP_CONFIG'; \
        grep -q '\"url\": \"http://127.0.0.1:8080/mcp\"' '$PHONE_MCP_CONFIG'; \
        ! grep -q 'disabledTools.*[^[]' '$PHONE_MCP_CONFIG'"
}

rollback_phone_state() {
    phone_sh "set -e; test -f '$RUN_ONCE_BACKUP'; cp -p '$RUN_ONCE_BACKUP' '$RUN_ONCE'; chmod 0700 '$RUN_ONCE'; \
        if [ -f '$MCP_CONFIG_BACKUP' ]; then cp -p '$MCP_CONFIG_BACKUP' '$PHONE_MCP_CONFIG'; chmod 0600 '$PHONE_MCP_CONFIG'; \
        elif [ -f '$MCP_CONFIG_ABSENT' ]; then rm -f '$PHONE_MCP_CONFIG'; fi; \
        rm -f '$PHONE_TOKEN_FILE'"
    # Never restore the compromised predecessor bearer. Leave Android MCP
    # stopped while restoring only the pre-change ShellMCP polling state.
    phone_sh am start -n "$APP_TRAMPOLINE" --es action stop >/dev/null || true
    phone_sh "pkill -x shellmcp 2>/dev/null || true; sleep 7; pidof shellmcp >/dev/null"
    phone_sh "pid=\$(ps -A -o USER,PID,PPID,NAME | awk '\$1==\"shell\" && \$4==\"shellmcp\" {print \$2; exit}'); tr '\\0' '\\n' </proc/\$pid/environ | grep -qx 'SHELLMCP_MODE=long_poll'"
    log 'status=rolled_back shellmcp_transport=long_poll android_mcp=stopped compromised_token_restored=false'
}

main() {
    adb_base start-server >/dev/null
    if ! resolve_serial; then
        log 'status=error reason=s21_usb_bootstrap_unavailable'
        exit 3
    fi
    if [[ "$ACTION" == "--rollback" ]]; then
        rollback_phone_state
        return
    fi
    [[ "$ACTION" == "apply" ]] || { log 'status=error reason=unknown_action'; exit 2; }
    ANDROID_S21_MCP_TOKEN="$(env_value "$TOKEN_FILE" ANDROID_S21_MCP_TOKEN || true)"
    [[ -n "$ANDROID_S21_MCP_TOKEN" ]] || { log 'status=error reason=missing_token'; exit 2; }
    [[ "$ANDROID_S21_MCP_TOKEN" =~ ^[A-Fa-f0-9]{64}$ ]] || { log 'status=error reason=invalid_token_format'; exit 2; }
    verify_apk
    backup_phone_state
    install_phone_token
    configure_shellmcp_launcher
    merge_phone_child_config
    ensure_full_debug_accessibility
    configure_phone_mcp
    restart_shellmcp_polling
    verify_phone_local_state
    log "status=ok transport=long_poll runtime_usb=false serial_prefix=${SERIAL:0:6}... child=AndroidRemoteControl-S21 phone_mcp=$PHONE_MCP_BIND:$PHONE_MCP_PORT expected_tool_count=$EXPECTED_TOOL_COUNT expected_tools_sha256=$EXPECTED_TOOLS_SHA256"
}

main "$@"
