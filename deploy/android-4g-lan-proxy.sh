#!/usr/bin/env bash
set -euo pipefail

CONFIG_FILE="${GPTADMIN_CONFIG_FILE:-/etc/gptadmin/gptadmin.env}"
ADB_BIN="${ADB_BIN:-/usr/bin/adb}"
ADB_USER="${ADB_USER:-roomhacker}"
ADB_HOME="${ADB_HOME:-/home/roomhacker}"
SERIAL="${ANDROID_ADB_SERIAL:-$(awk -F= '$1=="ANDROID_ADB_SERIAL" {print $2; exit}' "$CONFIG_FILE" 2>/dev/null || true)}"
ANDROID_PROXY_BIN="${ANDROID_PROXY_BIN:-/data/local/tmp/gptadmin/bin/android4gproxy}"
ANDROID_PROXY_PORT="${ANDROID_PROXY_PORT:-3126}"
ADB_FORWARD_PORT="${ADB_FORWARD_PORT:-3127}"
PORT_STATE="${PORT_STATE:-/etc/gptadmin/android-4g-proxy.env}"
LAN_BIND="${LAN_BIND:-$(ip -4 -o addr show scope global | awk '$4 ~ /^192\.168\.2\./ {sub(/\/.*$/, "", $4); print $4; exit}')}"
LAN_PROXY_ALLOWED_CIDRS="${LAN_PROXY_ALLOWED_CIDRS:-192.168.2.0/24}"
LAN_PROXY_FIREWALL="${LAN_PROXY_FIREWALL:-1}"

log() { printf '[%s] %s\n' "$(date -Is)" "$*"; }
adb() { sudo -u "$ADB_USER" env HOME="$ADB_HOME" "$ADB_BIN" -s "$SERIAL" "$@"; }

port_free() {
    local port="$1"
    if ss -ltnH 2>/dev/null | grep -Eq "(:|\\])${port}[[:space:]]"; then
        return 1
    fi
    return 0
}

choose_port() {
    local saved=""
    if [[ -r "$PORT_STATE" ]]; then
        saved="$(awk -F= '$1=="LAN_PROXY_PORT" {print $2; exit}' "$PORT_STATE")"
        if [[ "$saved" =~ ^[0-9]+$ ]] && port_free "$saved"; then
            printf '%s\n' "$saved"
            return
        fi
    fi
    local port
    for ((port=3126; port>=1024; port--)); do
        if port_free "$port"; then
            printf '%s\n' "$port"
            return
        fi
    done
    log 'no free LAN proxy port in range 3126..1024' >&2
    return 1
}

write_state() {
    local port="$1"
    local tmp="${PORT_STATE}.tmp"
    install -d -m 0755 "$(dirname "$PORT_STATE")"
    {
        printf 'LAN_PROXY_PORT=%s\n' "$port"
        printf 'LAN_PROXY_ADB_FORWARD_PORT=%s\n' "$ADB_FORWARD_PORT"
        printf 'LAN_PROXY_PROTOCOLS=socks5,http-connect\n'
        printf 'LAN_PROXY_TRANSPORT=tcp-only\n'
    } >"$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$PORT_STATE"
}

configure_firewall() {
    local port="$1"
    [[ "$LAN_PROXY_FIREWALL" == "1" || "$LAN_PROXY_FIREWALL" == "true" || "$LAN_PROXY_FIREWALL" == "yes" ]] || return 0
    command -v ufw >/dev/null 2>&1 || return 0
    ufw status 2>/dev/null | grep -q '^Status: active' || return 0

    local cidr
    for cidr in $LAN_PROXY_ALLOWED_CIDRS; do
        if ! ufw status 2>/dev/null | awk -v port="${port}/tcp" -v cidr="$cidr" '$1 == port && index($0, cidr) { found = 1 } END { exit !found }'; then
            ufw allow from "$cidr" to any port "$port" proto tcp comment 'gptadmin Android 4G proxy' >/dev/null
        fi
    done
}

ensure_android_proxy() {
    [[ -n "$SERIAL" ]] || { log 'ANDROID_ADB_SERIAL is not configured'; return 1; }
    [[ -n "$LAN_BIND" ]] || { log 'LAN_BIND could not be detected'; return 1; }
    [[ "$(adb get-state 2>/dev/null || true)" == 'device' ]] || { log 'Android ADB device is unavailable'; return 1; }
    if [[ -z "$(adb shell pidof android4gproxy 2>/dev/null | tr -d '\r')" ]]; then
        adb shell "mkdir -p /data/local/tmp/gptadmin/logs; nohup '$ANDROID_PROXY_BIN' -listen 127.0.0.1:$ANDROID_PROXY_PORT -dns 1.1.1.1:53 >/data/local/tmp/gptadmin/logs/android4gproxy.log 2>&1 &"
        sleep 1
    fi
    adb forward --remove "tcp:$ADB_FORWARD_PORT" >/dev/null 2>&1 || true
    adb forward "tcp:$ADB_FORWARD_PORT" "tcp:$ANDROID_PROXY_PORT"
}

main() {
    local port
    port="$(choose_port)"
    configure_firewall "$port"
    write_state "$port"
    log "sharing Android 4G proxy on ${LAN_BIND}:$port; protocols=socks5,http-connect transport=tcp-only"
    while true; do
        if ensure_android_proxy; then
            socat "TCP-LISTEN:$port,bind=$LAN_BIND,fork,reuseaddr,nodelay" "TCP:127.0.0.1:$ADB_FORWARD_PORT" || true
        else
            sleep 5
        fi
        sleep 2
    done
}

main "$@"
