# Копируем юниты
sudo cp rootd.service /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable rootd
sudo systemctl restart rootd

# Проверка
sudo systemctl status rootd
