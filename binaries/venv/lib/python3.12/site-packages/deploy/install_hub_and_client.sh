SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SHELLMCP_UNIT="$SCRIPT_DIR/systemd/shellmcp.service"
HUB_UNIT="$SCRIPT_DIR/systemd/gptadmin_hub.service"
WATCHDOG_SERVICE="$SCRIPT_DIR/systemd/gptadmin-hub-watchdog.service"
WATCHDOG_TIMER="$SCRIPT_DIR/systemd/gptadmin-hub-watchdog.timer"

# Копируем юниты
sudo cp "$SHELLMCP_UNIT" /etc/systemd/system/
sudo cp "$HUB_UNIT" /etc/systemd/system/
sudo cp "$WATCHDOG_SERVICE" /etc/systemd/system/
sudo cp "$WATCHDOG_TIMER" /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable shellmcp
sudo systemctl enable gptadmin_hub
sudo systemctl enable gptadmin-hub-watchdog.timer
sudo systemctl restart shellmcp
sudo systemctl restart gptadmin_hub
sudo systemctl restart gptadmin-hub-watchdog.timer

# Проверка
sudo systemctl status shellmcp
sudo systemctl status gptadmin_hub
sudo systemctl status gptadmin-hub-watchdog.timer
