#!/usr/bin/env python3
import requests, hashlib, base64, json, os

ROUTER_URL = "http://203.0.113.10"
COOKIE_FILE = os.path.expanduser("router_cookies.json")
USERNAME = "admin"
SERIAL = "5452535223232AB2"  # можно автоматом парсить со страницы, пока хардкод
PASSWORD = f"${SERIAL}"

# --- PHP_CRYPT_MD5 (эмуляция простая: md5($1$ + password)) ---
def php_crypt_md5(password: str, salt: str = "$1$") -> str:
    return hashlib.md5((salt + password).encode()).hexdigest()


def load_cookies():
    if os.path.exists(COOKIE_FILE):
        with open(COOKIE_FILE, "r") as f:
            return requests.utils.cookiejar_from_dict(json.load(f))
    return None


def save_cookies(cj):
    with open(COOKIE_FILE, "w") as f:
        json.dump(requests.utils.dict_from_cookiejar(cj), f)


def login():
    # подготовим пароль
    md5pass = php_crypt_md5(PASSWORD)
    encpass = base64.b64encode(md5pass.encode()).decode()

    data = {
        "username": USERNAME,
        "password": PASSWORD,  # оригинальный
        "encodePassword": encpass,
    }
    
    s = requests.Session()
    r = s.post(f"{ROUTER_URL}/boaform/admin/formLogin", data=data)
    print("Status:", r.status_code)
    print("Headers:", r.headers)
    if r.status_code == 200:
        save_cookies(s.cookies)
        print("Cookies saved to", COOKIE_FILE)
    else:
        print("Login failed")


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
