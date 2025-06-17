#!/usr/bin/env python3
import os
import requests

# Параметры (можно задать через экспорт переменных)
ROOTD_URL = os.getenv("ROOTD_URL", "http://localhost:25900")
TOKEN     = os.getenv("ROOTD_TOKEN", "srv_secret")
HEADERS   = {"Authorization": f"Bearer {TOKEN}"}


def test_system_info():
    r = requests.get(f"{ROOTD_URL}/system/info", headers=HEADERS)
    print("GET /system/info →", r.status_code, r.json())


def test_exec():
    payload = {"cmd": "echo hello_rootd", "timeout": 5}
    r = requests.post(f"{ROOTD_URL}/exec", json=payload, headers=HEADERS)
    print("POST /exec →", r.status_code, r.json())


def test_file_crud():
    test_path = "/tmp/test_rootd.txt"
    # — write
    r = requests.put(f"{ROOTD_URL}/file", params={"path": test_path},
                     json={"content": "abc123", "mode": "w"},
                     headers=HEADERS)
    print("PUT  /file (write) →", r.status_code, r.json())
    # — read
    r = requests.get(f"{ROOTD_URL}/file", params={"path": test_path}, headers=HEADERS)
    print("GET  /file (read) →", r.status_code, r.json())
    # — delete
    r = requests.delete(f"{ROOTD_URL}/file", params={"path": test_path}, headers=HEADERS)
    print("DEL  /file →", r.status_code, r.json())


def test_dir_list():
    r = requests.get(f"{ROOTD_URL}/dir", params={"path": "."}, headers=HEADERS)
    print("GET  /dir →", r.status_code, r.json())


if __name__ == "__main__":
    print("=== Testing rootd on", ROOTD_URL, "===\n")
    test_system_info()
    test_exec()
    test_file_crud()
    test_dir_list()