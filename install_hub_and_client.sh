# Копируем юниты
sudo cp rootd.service /etc/systemd/system/
sudo cp hub_proxy.service /etc/systemd/system/

# Перечитываем systemd и запускаем оба
sudo systemctl daemon-reload
sudo systemctl enable rootd
sudo systemctl enable hub_proxy
sudo systemctl restart rootd
sudo systemctl restart hub_proxy

# Проверка
sudo systemctl status rootd
sudo systemctl status hub_proxy
