#!/usr/bin/env python3
import os
import time
import requests

# Параметры (можно задать через экспорт переменных)
HUB_URL      = os.getenv("HUB_URL", "http://localhost:9001")
CTL_TOKEN    = os.getenv("CTL_TOKEN", "chatgpt_secret")
ROOTD_URL    = os.getenv("ROOTD_URL", "http://localhost:25900")
ROOTD_TOKEN  = os.getenv("ROOTD_TOKEN", "srv_secret")

HEADERS_HUB  = {"Authorization": f"Bearer {CTL_TOKEN}"}


def send_heartbeat():
    payload = {
        "name":        "local-test",
        "base_url":    ROOTD_URL,
        "rootd_token": ROOTD_TOKEN,
        "time":        int(time.time())
    }
    r = requests.post(f"{HUB_URL}/heartbeat", json=payload)
    print("POST /heartbeat →", r.status_code, r.json())


def list_servers():
    r = requests.get(f"{HUB_URL}/servers")
    print("GET  /servers →", r.status_code, r.json())


def test_proxy_system_info():
    r = requests.get(f"{HUB_URL}/srv/local-test/system/info", headers=HEADERS_HUB)
    print("GET  /srv/local-test/system/info →", r.status_code, r.json())


def test_bulk_exec():
    payload = {"servers": ["local-test"], "cmd": "echo bulk"}
    r = requests.post(f"{HUB_URL}/bulk/exec", json=payload, headers=HEADERS_HUB)
    print("POST /bulk/exec →", r.status_code, r.json())


if __name__ == "__main__":
    print("=== Testing hub_proxy on", HUB_URL, "===\n")
    send_heartbeat()
    time.sleep(1)  # даём секунду, чтобы hub зарегистрировал heartbeat
    list_servers()
    test_proxy_system_info()
    test_bulk_exec()


