#!/usr/bin/env python3
import requests, base64, json, os, crypt

ROUTER_URL = "http://203.0.113.10"
COOKIE_FILE = "router_cookies.json"
USERNAME = "admin"
SERIAL = "5452535223232AB2"

# Пароль начинается со знака $
PLAIN_PASSWORD = f"${SERIAL}"


def calc_post_security_flag(data: dict) -> int:
    # Сформировать строку inputVal так же, как в JS: name=value&...
    items = []
    for k, v in data.items():
        if k in ("postSecurityFlag", "csrftoken"):
            continue
        items.append(f"{k}={v}")
    inputVal = "&".join(items) + "&"

    csum = 0
    i = 0
    while i < len(inputVal):
        if i + 4 > len(inputVal):
            if i < len(inputVal):
                csum += (ord(inputVal[i]) << 24)
            if i + 1 < len(inputVal):
                csum += (ord(inputVal[i+1]) << 16)
            if i + 2 < len(inputVal):
                csum += (ord(inputVal[i+2]) << 8)
            break
        else:
            csum += (ord(inputVal[i]) << 24) + (ord(inputVal[i+1]) << 16) + (ord(inputVal[i+2]) << 8) + ord(inputVal[i+3])
            i += 4

    csum = (csum & 0xffff) + (csum >> 16)
    csum = csum & 0xffff
    csum = (~csum) & 0xffff
    return csum


def load_cookies():
    if os.path.exists(COOKIE_FILE):
        with open(COOKIE_FILE, "r") as f:
            return requests.utils.cookiejar_from_dict(json.load(f))
    return None


def save_cookies(cj):
    with open(COOKIE_FILE, "w") as f:
        json.dump(requests.utils.dict_from_cookiejar(cj), f)


def login():
    # md5-crypt от "$SERIAL"
    md5hash = crypt.crypt(PLAIN_PASSWORD, "$1$")
    encpass = base64.b64encode(md5hash.encode()).decode()

    s = requests.Session()

    # 1. Получаем login.asp
    r1 = s.get(f"{ROUTER_URL}/admin/login.asp")
    print("GET login.asp status:", r1.status_code)

    headers = {
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
        "Accept-Encoding": "gzip, deflate",
        "Accept-Language": "ru,en-US;q=0.9,en;q=0.8,ru-RU;q=0.7,zh-CN;q=0.6,zh;q=0.5",
        "Cache-Control": "max-age=0",
        "Connection": "keep-alive",
        "Content-Type": "application/x-www-form-urlencoded",
        "Host": "203.0.113.10",
        "Origin": "http://203.0.113.10",
        "Referer": "http://203.0.113.10/admin/login.asp",
        "Upgrade-Insecure-Requests": "1",
        "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36",
    }

    data = {
        "challenge": "",
        "username": USERNAME,
        "save": "Авторизоваться",
        "encodePassword": encpass,
        "submit-url": "/admin/login.asp",
    }

    # посчитать правильный postSecurityFlag
    data["postSecurityFlag"] = str(calc_post_security_flag(data))

    print("Computed postSecurityFlag:", data["postSecurityFlag"])

    r2 = s.post(f"{ROUTER_URL}/boaform/admin/formLogin", headers=headers, data=data, allow_redirects=True)

    print("POST status:", r2.status_code)
    print("Final URL:", r2.url)
    if r2.status_code in (200, 302) and "Ошибка" not in r2.text:
        save_cookies(s.cookies)
        print("✅ Cookies saved to", COOKIE_FILE)
    else:
        print("❌ Login failed")
        print(r2.text[:500])


def main():
    cj = load_cookies()
    s = requests.Session()
    if cj:
        s.cookies = cj
        r = s.get(f"{ROUTER_URL}/admin/login.asp")
        if r.status_code == 200 and "formLogin" not in r.text:
            print("Using saved cookies")
            return
        else:
            print("Cookies expired, re-login...")
    login()

if __name__ == "__main__":
    main()
