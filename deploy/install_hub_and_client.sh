#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SHELLMCP_UNIT="$SCRIPT_DIR/systemd/shellmcp.service"
HUB_UNIT="$SCRIPT_DIR/systemd/gptadmin-hub.service"
WATCHDOG_SERVICE="$SCRIPT_DIR/systemd/gptadmin-hub-watchdog.service"
WATCHDOG_TIMER="$SCRIPT_DIR/systemd/gptadmin-hub-watchdog.timer"

# Копируем юниты
sudo cp "$SHELLMCP_UNIT" /etc/systemd/system/
sudo install -m 0644 "$HUB_UNIT" /etc/systemd/system/gptadmin-hub.service
sudo cp "$WATCHDOG_SERVICE" /etc/systemd/system/
sudo cp "$WATCHDOG_TIMER" /etc/systemd/system/
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-hub-standby.service" /etc/systemd/system/gptadmin-hub-standby.service
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-handover@.service" /etc/systemd/system/gptadmin-handover@.service
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_handover_local.sh" /usr/local/sbin/gptadmin-handover
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_failover_controller.sh" /usr/local/sbin/gptadmin-failover-controller
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_primary_down_switch.sh" /usr/local/sbin/gptadmin-primary-down-switch
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_primary_stop.sh" /usr/local/sbin/gptadmin-primary-stop
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_standby_stop.sh" /usr/local/sbin/gptadmin-standby-stop
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_hub_standby_run.sh" /usr/local/sbin/gptadmin-hub-standby-run
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_failover_reclaim_push.py" /usr/local/bin/gptadmin-failover-reclaim-push
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_frp_watchdog.py" /usr/local/bin/gptadmin-frp-watchdog
sudo install -m 0755 "$SCRIPT_DIR/../scripts/gptadmin_wanb_health.py" /usr/local/sbin/gptadmin-wanb-health
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-wanb-health.service" /etc/systemd/system/gptadmin-wanb-health.service
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-wanb-health.timer" /etc/systemd/system/gptadmin-wanb-health.timer
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-failover-controller.service" /etc/systemd/system/gptadmin-failover-controller.service
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-failover-controller.timer" /etc/systemd/system/gptadmin-failover-controller.timer
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-frp-watchdog.service" /etc/systemd/system/gptadmin-frp-watchdog.service
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-frp-watchdog.timer" /etc/systemd/system/gptadmin-frp-watchdog.timer
sudo mkdir -p /etc/systemd/system/gptadmin-hub.service.d /etc/systemd/system/gptadmin-hub-standby.service.d /etc/systemd/system/gptadmin-tunnel-frpc.service.d /etc/systemd/system/gptadmin-auto-update.service.d
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-hub-primary-ha.conf" /etc/systemd/system/gptadmin-hub.service.d/96-self-repair-trigger.conf
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-hub-standby-self-repair.conf" /etc/systemd/system/gptadmin-hub-standby.service.d/98-self-repair-trigger.conf
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-hub-standby-invariants.conf" /etc/systemd/system/gptadmin-hub-standby.service.d/99-ha-invariants.conf
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-tunnel-frpc-ha.conf" /etc/systemd/system/gptadmin-tunnel-frpc.service.d/99-ha-invariants.conf
sudo install -m 0644 "$SCRIPT_DIR/systemd/gptadmin-auto-update-disabled.conf" /etc/systemd/system/gptadmin-auto-update.service.d/99-self-repair-disable.conf

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable gptadmin-hub-standby.service gptadmin-failover-controller.timer gptadmin-tunnel-frpc.service gptadmin-frp-watchdog.timer gptadmin-wanb-health.timer >/dev/null
sudo systemctl disable --now gptadmin-auto-update.timer 2>/dev/null || true
sudo systemctl restart gptadmin-failover-controller.timer
sudo systemctl restart gptadmin-frp-watchdog.timer
sudo systemctl enable shellmcp
sudo systemctl disable --now gptadmin_hub.service 2>/dev/null || true  # remove legacy underscore unit
sudo systemctl enable gptadmin-hub.service
sudo systemctl disable --now gptadmin-hub-watchdog.timer 2>/dev/null || true
sudo systemctl restart shellmcp
sudo systemctl restart gptadmin-hub.service
true # unified failover controller timer is authoritative

# Проверка
sudo systemctl status shellmcp
sudo systemctl status gptadmin-hub.service
sudo systemctl status gptadmin-hub-watchdog.timer
