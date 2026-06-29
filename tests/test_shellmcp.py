#!/usr/bin/env python3
import os

import pytest
import requests

pytestmark = pytest.mark.skipif(
    os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1",
    reason="legacy live-service integration test; set GPTADMIN_INTEGRATION_TESTS=1",
)

SHELLMCP_URL = os.getenv("SHELLMCP_URL", "http://localhost:25900")
TOKEN = os.getenv("SHELLMCP_TOKEN", "srv_secret")
HEADERS = {"Authorization": f"Bearer {TOKEN}"}
REQUEST_TIMEOUT_S = 10.0


def test_system_info() -> None:
    response = requests.get(f"{SHELLMCP_URL}/system/info", headers=HEADERS, timeout=REQUEST_TIMEOUT_S)
    assert response.status_code == 200, response.text
    data = response.json()
    assert data.get("host") or data.get("hostname") or data.get("name")
    assert data.get("platform")


def test_exec() -> None:
    payload = {"cmd": "echo hello_shellmcp", "timeout": 5}
    response = requests.post(f"{SHELLMCP_URL}/exec", json=payload, headers=HEADERS, timeout=REQUEST_TIMEOUT_S)
    assert response.status_code == 200, response.text
    data = response.json()
    assert data.get("returncode") == 0, data
    assert "hello_shellmcp" in data.get("stdout", "")


def test_exec_stream() -> None:
    payload = {"cmd": "echo stream_hello"}
    response = requests.post(
        f"{SHELLMCP_URL}/exec/stream",
        json=payload,
        headers=HEADERS,
        stream=True,
        timeout=REQUEST_TIMEOUT_S,
    )
    assert response.status_code == 200, response.text
    assert "stream_hello" in response.text


if __name__ == "__main__":
    print("=== Testing shellmcp on", SHELLMCP_URL, "===\n")
    test_system_info()
    test_exec()
    test_exec_stream()
