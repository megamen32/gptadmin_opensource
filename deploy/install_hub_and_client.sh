SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTD_UNIT="$SCRIPT_DIR/systemd/rootd.service"
HUB_UNIT="$SCRIPT_DIR/systemd/hub_proxy.service"

# Копируем юниты
sudo cp "$ROOTD_UNIT" /etc/systemd/system/
sudo cp "$HUB_UNIT" /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable rootd
sudo systemctl enable hub_proxy
sudo systemctl restart rootd
sudo systemctl restart hub_proxy

# Проверка
sudo systemctl status rootd
sudo systemctl status hub_proxy
