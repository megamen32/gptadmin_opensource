#!/usr/bin/env bash
set -euo pipefail

# Register the phone-local Android Remote Control MCP as a dedicated GPTAdmin
# child and keep the only required transport (SSH) boot-persistent.

ACTION="${1:-apply}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIT_NAME="android-s21-network-mcp-tunnel.service"
UNIT_SOURCE="$SCRIPT_DIR/systemd/$UNIT_NAME"
SYSTEMD_DIR="${S21_MCP_SYSTEMD_DIR:-/etc/systemd/system}"
HUB_DROPIN_DIR="${S21_MCP_HUB_DROPIN_DIR:-/etc/systemd/system/gptadmin-hub.service.d}"
HUB_DROPIN="$HUB_DROPIN_DIR/90-s21-network-mcp.conf"
HUB_CONFIG="${S21_MCP_HUB_CONFIG:-/etc/gptadmin/mcp-supervisor.json}"
TOKEN_FILE="${S21_MCP_TOKEN_FILE:-/etc/gptadmin/android-s21-mcp.env}"
BACKUP_FILE="${S21_MCP_BACKUP_FILE:-$HUB_CONFIG.pre-s21-network-mcp}"
SYSTEMCTL="${S21_MCP_SYSTEMCTL:-systemctl}"
CHILD_REF="S21NetworkAgentDevice"

log() {
    printf 'android_s21_network_mcp %s\n' "$*"
}

validate_token_file() {
    [[ -r "$TOKEN_FILE" ]] || {
        log "status=error reason=token_file_unreadable path=$TOKEN_FILE" >&2
        return 1
    }
    awk -F= '$1 == "ANDROID_S21_MCP_TOKEN" && $2 ~ /^[A-Fa-f0-9]{64}$/ {found=1} END {exit !found}' "$TOKEN_FILE" || {
        log "status=error reason=invalid_token_file path=$TOKEN_FILE" >&2
        return 1
    }
}

merge_child() {
    python3 - "$HUB_CONFIG" "$BACKUP_FILE" "$CHILD_REF" <<'PY'
import json
import os
import shutil
import sys
import tempfile

config_path, backup_path, child_ref = sys.argv[1:]
with open(config_path, encoding="utf-8") as handle:
    agents = json.load(handle)
if not isinstance(agents, list):
    raise SystemExit("GPTAdmin MCP config must contain a JSON array")
if not os.path.exists(backup_path):
    shutil.copy2(config_path, backup_path)
child = {
    "ref": child_ref,
    "name": "S21 Network Agent Device (Android Remote Control MCP)",
    "transport": "streamable-http",
    "url": "http://127.0.0.1:18080/mcp",
    "headers": {"Authorization": "Bearer ${ANDROID_S21_MCP_TOKEN}"},
    "enabled": True,
}
agents = [agent for agent in agents if agent.get("ref") != child_ref]
agents.append(child)
directory = os.path.dirname(config_path) or "."
mode = os.stat(config_path).st_mode & 0o777
fd, temporary = tempfile.mkstemp(prefix=".s21-network-mcp.", dir=directory, text=True)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(agents, handle, indent=2, sort_keys=True)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, mode)
    os.replace(temporary, config_path)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
}

apply_runtime() {
    validate_token_file
    [[ -f "$UNIT_SOURCE" ]] || {
        log "status=error reason=unit_source_missing path=$UNIT_SOURCE" >&2
        return 1
    }
    [[ -f "$HUB_CONFIG" ]] || {
        log "status=error reason=hub_config_missing path=$HUB_CONFIG" >&2
        return 1
    }
    install -d -m 0755 "$SYSTEMD_DIR" "$HUB_DROPIN_DIR"
    install -m 0644 "$UNIT_SOURCE" "$SYSTEMD_DIR/$UNIT_NAME"
    printf '[Service]\nEnvironmentFile=%s\n' "$TOKEN_FILE" >"$HUB_DROPIN"
    chmod 0644 "$HUB_DROPIN"
    merge_child
    "$SYSTEMCTL" daemon-reload
    "$SYSTEMCTL" enable --now "$UNIT_NAME"
    "$SYSTEMCTL" restart gptadmin-hub.service
    log "status=ok action=apply ref=$CHILD_REF transport=ssh-network url=http://127.0.0.1:18080/mcp"
}

rollback_runtime() {
    "$SYSTEMCTL" disable --now "$UNIT_NAME" || true
    if [[ -f "$BACKUP_FILE" ]]; then
        cp -p "$BACKUP_FILE" "$HUB_CONFIG"
        rm -f "$BACKUP_FILE"
    else
        python3 - "$HUB_CONFIG" "$CHILD_REF" <<'PY'
import json
import os
import sys
import tempfile

config_path, child_ref = sys.argv[1:]
with open(config_path, encoding="utf-8") as handle:
    agents = json.load(handle)
agents = [agent for agent in agents if agent.get("ref") != child_ref]
directory = os.path.dirname(config_path) or "."
mode = os.stat(config_path).st_mode & 0o777
fd, temporary = tempfile.mkstemp(prefix=".s21-network-mcp.rollback.", dir=directory, text=True)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(agents, handle, indent=2, sort_keys=True)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, mode)
    os.replace(temporary, config_path)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
    fi
    rm -f "$SYSTEMD_DIR/$UNIT_NAME" "$HUB_DROPIN"
    "$SYSTEMCTL" daemon-reload
    "$SYSTEMCTL" restart gptadmin-hub.service
    log "status=ok action=rollback ref=$CHILD_REF"
}

status_runtime() {
    "$SYSTEMCTL" is-active --quiet "$UNIT_NAME"
    python3 - "$HUB_CONFIG" "$CHILD_REF" <<'PY'
import json
import sys

config_path, child_ref = sys.argv[1:]
with open(config_path, encoding="utf-8") as handle:
    agents = json.load(handle)
matches = [agent for agent in agents if agent.get("ref") == child_ref]
if len(matches) != 1:
    raise SystemExit(f"expected one {child_ref} definition, found {len(matches)}")
child = matches[0]
if child.get("transport") != "streamable-http" or child.get("url") != "http://127.0.0.1:18080/mcp":
    raise SystemExit("S21 network MCP definition does not match the expected transport")
PY
    log "status=ok action=status ref=$CHILD_REF"
}

case "$ACTION" in
    apply) apply_runtime ;;
    rollback|--rollback) rollback_runtime ;;
    status|--status) status_runtime ;;
    *)
        printf 'usage: %s [apply|status|rollback]\n' "$0" >&2
        exit 2
        ;;
esac
