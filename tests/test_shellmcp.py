#!/usr/bin/env python3
import os

import pytest

pytestmark = pytest.mark.skipif(
    os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1",
    reason="legacy live-service integration test; set GPTADMIN_INTEGRATION_TESTS=1",
)

import requests

# Параметры (можно задать через экспорт переменных)
SHELLMCP_URL = os.getenv("SHELLMCP_URL", "http://localhost:25900")
TOKEN     = os.getenv("SHELLMCP_TOKEN", "srv_secret")
HEADERS   = {"Authorization": f"Bearer {TOKEN}"}


def test_system_info():
    r = requests.get(f"{SHELLMCP_URL}/system/info", headers=HEADERS)
    print("GET /system/info →", r.status_code, r.json())


def test_exec():
    payload = {"cmd": "echo hello_shellmcp", "timeout": 5}
    r = requests.post(f"{SHELLMCP_URL}/exec", json=payload, headers=HEADERS)
    print("POST /exec →", r.status_code, r.json())


def test_exec_stream():
    payload = {"cmd": "echo stream_hello"}
    r = requests.post(f"{SHELLMCP_URL}/exec/stream", json=payload, headers=HEADERS, stream=True)
    print("POST /exec/stream →", r.status_code)
    print(r.text)


if __name__ == "__main__":
    print("=== Testing shellmcp on", SHELLMCP_URL, "===\n")
    test_system_info()
    test_exec()
    test_exec_stream()

