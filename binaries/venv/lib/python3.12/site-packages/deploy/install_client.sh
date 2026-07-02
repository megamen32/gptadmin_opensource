SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SHELLMCP_UNIT="$SCRIPT_DIR/systemd/shellmcp.service"

# Копируем юниты
sudo cp "$SHELLMCP_UNIT" /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable shellmcp
sudo systemctl restart shellmcp

# Проверка
sudo systemctl status shellmcp
