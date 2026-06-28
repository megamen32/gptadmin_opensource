#!/usr/bin/env python3
"""Integration tests for hub_proxy (requires running hub server)"""
import os

import pytest

pytestmark = pytest.mark.skipif(
    os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1",
    reason="legacy live-service integration test; set GPTADMIN_INTEGRATION_TESTS=1",
)

import time
import socket
import threading
import pytest
import requests

# Skip all tests if hub is not running
HUB_URL      = os.getenv("HUB_URL", "http://localhost:9001")
CTL_TOKEN    = os.getenv("CTL_TOKEN", "chatgpt_secret")
SHELLMCP_URL    = os.getenv("SHELLMCP_URL", "http://localhost:25900")
SHELLMCP_TOKEN  = os.getenv("SHELLMCP_TOKEN", "srv_secret")
SERVER_NAME  = socket.gethostname()

HEADERS_HUB  = {"Authorization": f"Bearer {CTL_TOKEN}"}


def is_hub_running():
    """Check if hub server is accessible"""
    try:
        r = requests.get(f"{HUB_URL}/servers", timeout=2)
        return r.status_code == 200
    except:
        return False


# Skip all tests in this module if hub is not running
pytestmark = pytest.mark.skipif(not is_hub_running(), reason="hub server not running")


def send_heartbeat(mode="webhook"):
    payload = {
        "name":        SERVER_NAME,
        "base_url":    SHELLMCP_URL,
        "shellmcp_token": SHELLMCP_TOKEN,
        "time":        int(time.time()),
        "mode":        mode,
    }
    r = requests.post(f"{HUB_URL}/heartbeat", json=payload)
    return r.status_code == 200


def test_proxy_system_info():
    """Test proxying /system/info through hub"""
    # First send heartbeat to register server
    send_heartbeat()
    time.sleep(0.5)
    
    r = requests.get(f"{HUB_URL}/srv/system/info", params={"server": SERVER_NAME}, headers=HEADERS_HUB)
    assert r.status_code == 200
    data = r.json()
    assert 'host' in data or 'platform' in data


def test_bulk_exec():
    """Test bulk exec through hub"""
    send_heartbeat()
    time.sleep(0.5)
    
    payload = {"servers": [SERVER_NAME], "cmd": "echo bulk"}
    r = requests.post(f"{HUB_URL}/bulk/exec", json=payload, headers=HEADERS_HUB)
    assert r.status_code == 200
    data = r.json()
    assert SERVER_NAME in data or 'results' in data


def test_proxy_exec_polling():
    """Test exec with polling mode"""
    send_heartbeat("polling")
    time.sleep(0.5)

    def worker():
        for _ in range(50):  # Try for 5 seconds
            r = requests.get(f"{HUB_URL}/queue/{SERVER_NAME}", params={"token": SHELLMCP_TOKEN})
            job = r.json()
            if job.get("cmd"):
                os.system(job["cmd"])
                res = {"id": job["id"], "result": {"returncode": 0, "stdout": "polled\n", "stderr": ""}}
                requests.post(
                    f"{HUB_URL}/queue/{SERVER_NAME}/result",
                    params={"token": SHELLMCP_TOKEN},
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
    assert r.status_code == 200
    data = r.json()
    assert 'id' in data or 'result' in data


def test_bulk_exec_polling():
    """Test bulk exec with polling mode"""
    send_heartbeat("polling")
    time.sleep(0.5)

    def worker():
        for _ in range(50):  # Try for 5 seconds
            r = requests.get(f"{HUB_URL}/queue/{SERVER_NAME}", params={"token": SHELLMCP_TOKEN})
            job = r.json()
            if job.get("cmd"):
                os.system(job["cmd"])
                res = {"id": job["id"], "result": {"returncode": 0, "stdout": "bulk polled\n", "stderr": ""}}
                requests.post(
                    f"{HUB_URL}/queue/{SERVER_NAME}/result",
                    params={"token": SHELLMCP_TOKEN},
                    json=res,
                )
                break
            time.sleep(0.1)

    threading.Thread(target=worker, daemon=True).start()

    payload = {"servers": [SERVER_NAME], "cmd": "echo bulk polled"}
    r = requests.post(f"{HUB_URL}/bulk/exec", json=payload, headers=HEADERS_HUB)
    assert r.status_code == 200
    data = r.json()
    assert SERVER_NAME in data or 'results' in data


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
