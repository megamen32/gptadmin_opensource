#!/usr/bin/env python3
import os
import time
import requests
import socket
import threading

# Параметры (можно задать через экспорт переменных)
HUB_URL      = os.getenv("HUB_URL", "http://localhost:9001")
CTL_TOKEN    = os.getenv("CTL_TOKEN", "chatgpt_secret")
ROOTD_URL    = os.getenv("ROOTD_URL", "http://localhost:25900")
ROOTD_TOKEN  = os.getenv("ROOTD_TOKEN", "srv_secret")
SERVER_NAME  = socket.gethostname()

HEADERS_HUB  = {"Authorization": f"Bearer {CTL_TOKEN}"}


def send_heartbeat(mode="webhook"):
    payload = {
        "name":        SERVER_NAME,
        "base_url":    ROOTD_URL,
        "rootd_token": ROOTD_TOKEN,
        "time":        int(time.time()),
        "mode":        mode,
    }
    r = requests.post(f"{HUB_URL}/heartbeat", json=payload)
    print("POST /heartbeat →", r.status_code, r.json())


def list_servers():
    r = requests.get(f"{HUB_URL}/servers")
    print("GET  /servers →", r.status_code, r.json())


def test_proxy_system_info():
    r = requests.get(f"{HUB_URL}/srv/system/info", params={"server": SERVER_NAME}, headers=HEADERS_HUB)
    print(f"GET  /srv/system/info?server={SERVER_NAME} →", r.status_code, r.json())


def test_bulk_exec():
    payload = {"servers": [SERVER_NAME], "cmd": "echo bulk"}
    r = requests.post(f"{HUB_URL}/bulk/exec", json=payload, headers=HEADERS_HUB)
    print("POST /bulk/exec →", r.status_code, r.json())


def test_proxy_exec_polling():
    send_heartbeat("polling")

    def worker():
        while True:
            r = requests.get(f"{HUB_URL}/queue/{SERVER_NAME}", params={"token": ROOTD_TOKEN})
            job = r.json()
            if job.get("cmd"):
                os.system(job["cmd"])
                res = {"id": job["id"], "result": {"returncode": 0, "stdout": "polled\n", "stderr": ""}}
                requests.post(
                    f"{HUB_URL}/queue/{SERVER_NAME}/result",
                    params={"token": ROOTD_TOKEN},
                    json=res,
                )
                break
            time.sleep(0.1)

    threading.Thread(target=worker, daemon=True).start()

    payload = {"cmd": "echo polled"}
    r = requests.post(
        f"{HUB_URL}/srv/exec",
        params={"server": SERVER_NAME},
        json=payload,
        headers=HEADERS_HUB,
    )
    print("POST /srv/exec (polling) →", r.status_code, r.json())


def test_bulk_exec_polling():
    send_heartbeat("polling")

    def worker():
        while True:
            r = requests.get(f"{HUB_URL}/queue/{SERVER_NAME}", params={"token": ROOTD_TOKEN})
            job = r.json()
            if job.get("cmd"):
                os.system(job["cmd"])
                res = {"id": job["id"], "result": {"returncode": 0, "stdout": "bulk polled\n", "stderr": ""}}
                requests.post(
                    f"{HUB_URL}/queue/{SERVER_NAME}/result",
                    params={"token": ROOTD_TOKEN},
                    json=res,
                )
                break
            time.sleep(0.1)

    threading.Thread(target=worker, daemon=True).start()

    payload = {"servers": [SERVER_NAME], "cmd": "echo bulk polled"}
    r = requests.post(
        f"{HUB_URL}/bulk/exec",
        json=payload,
        headers=HEADERS_HUB,
    )
    print("POST /bulk/exec (polling) →", r.status_code, r.json())


if __name__ == "__main__":
    print("=== Testing hub_proxy on", HUB_URL, "===\n")
    send_heartbeat()
    time.sleep(1)  # даём секунду, чтобы hub зарегистрировал heartbeat
    list_servers()
    test_proxy_system_info()
    test_bulk_exec()
    test_proxy_exec_polling()
    test_bulk_exec_polling()


