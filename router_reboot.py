#!/usr/bin/env python3
import requests, json, os

ROUTER_URL = "http://203.0.113.10"
COOKIE_FILE = os.path.expanduser("router_cookies.json")


def load_cookies():
    if os.path.exists(COOKIE_FILE):
        with open(COOKIE_FILE, "r") as f:
            return requests.utils.cookiejar_from_dict(json.load(f))
    raise RuntimeError("No cookies file found, please run router_login.py first")


def reboot():
    cj = load_cookies()
    s = requests.Session()
    s.cookies = cj

    headers = {
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
        "Accept-Encoding": "gzip, deflate",
        "Accept-Language": "ru,en-US;q=0.9,en;q=0.8,ru-RU;q=0.7,zh-CN;q=0.6,zh;q=0.5",
        "Cache-Control": "max-age=0",
        "Connection": "keep-alive",
        "Content-Type": "application/x-www-form-urlencoded",
        "Host": "203.0.113.10",
        "Origin": "http://203.0.113.10",
        "Referer": "http://203.0.113.10/admin/reboot.asp?v=1756942913000",
        "Upgrade-Insecure-Requests": "1",
        "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36",
    }

    data = {
        "postSecurityFlag": "65535"
    }

    url = f"{ROUTER_URL}/boaform/admin/formReboot"
    r = s.post(url, headers=headers, data=data)

    text = r.text

    if "Устройство перезагружается" in text or "Настройки применены" in text:
        print("✅ Роутер уходит в перезагрузку...")
    elif "Ошибка аутентификации" in text:
        print("❌ Ошибка авторизации! Нужно заново выполнить router_login.py")
    else:
        print("⚠️ Неожиданный ответ от роутера")
        print(text[:500])


if __name__ == "__main__":
    reboot()
