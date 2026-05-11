SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTD_UNIT="$SCRIPT_DIR/systemd/rootd.service"

# Копируем юниты
sudo cp "$ROOTD_UNIT" /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable rootd
sudo systemctl restart rootd

# Проверка
sudo systemctl status rootd
