SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTD_UNIT="$SCRIPT_DIR/systemd/rootd.service"
HUB_UNIT="$SCRIPT_DIR/systemd/hub_proxy.service"
WATCHDOG_SERVICE="$SCRIPT_DIR/systemd/gptadmin-hub-watchdog.service"
WATCHDOG_TIMER="$SCRIPT_DIR/systemd/gptadmin-hub-watchdog.timer"

# Копируем юниты
sudo cp "$ROOTD_UNIT" /etc/systemd/system/
sudo cp "$HUB_UNIT" /etc/systemd/system/
sudo cp "$WATCHDOG_SERVICE" /etc/systemd/system/
sudo cp "$WATCHDOG_TIMER" /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable rootd
sudo systemctl enable hub_proxy
sudo systemctl enable gptadmin-hub-watchdog.timer
sudo systemctl restart rootd
sudo systemctl restart hub_proxy
sudo systemctl restart gptadmin-hub-watchdog.timer

# Проверка
sudo systemctl status rootd
sudo systemctl status hub_proxy
sudo systemctl status gptadmin-hub-watchdog.timer
