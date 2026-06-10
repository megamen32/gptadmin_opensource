#!/usr/bin/env python3
<<<<<<< HEAD
<<<<<<< HEAD
"""Integration tests for rootd (requires running rootd server)"""
import os
import pytest
import requests

# Skip all tests if rootd is not running
=======
=======
>>>>>>> headroom-spill-integration
import os

import pytest

pytestmark = pytest.mark.skipif(
    os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1",
    reason="legacy live-service integration test; set GPTADMIN_INTEGRATION_TESTS=1",
)

import requests

# Параметры (можно задать через экспорт переменных)
<<<<<<< HEAD
>>>>>>> headroom-spill-integration
=======
>>>>>>> headroom-spill-integration
ROOTD_URL = os.getenv("ROOTD_URL", "http://localhost:25900")
TOKEN     = os.getenv("ROOTD_TOKEN", "srv_secret")
HEADERS   = {"Authorization": f"Bearer {TOKEN}"}


<<<<<<< HEAD
<<<<<<< HEAD
def is_rootd_running():
    """Check if rootd server is accessible"""
    try:
        r = requests.get(f"{ROOTD_URL}/system/info", headers=HEADERS, timeout=2)
        return r.status_code == 200
    except:
        return False


# Skip all tests in this module if rootd is not running
pytestmark = pytest.mark.skipif(not is_rootd_running(), reason="rootd server not running")


def test_system_info():
    """Test /system/info endpoint"""
    r = requests.get(f"{ROOTD_URL}/system/info", headers=HEADERS)
    assert r.status_code == 200
    data = r.json()
    assert 'host' in data
    assert 'platform' in data
    assert 'cores' in data


def test_exec():
    """Test /exec endpoint with simple command"""
    payload = {"cmd": "echo hello_rootd", "timeout": 5}
    r = requests.post(f"{ROOTD_URL}/exec", json=payload, headers=HEADERS)
    assert r.status_code == 200
    data = r.json()
    assert 'returncode' in data
    assert data['returncode'] == 0
    assert 'hello_rootd' in data['stdout']


def test_exec_stream():
    """Test /exec/stream endpoint"""
    payload = {"cmd": "echo stream_hello"}
    r = requests.post(f"{ROOTD_URL}/exec/stream", json=payload, headers=HEADERS, stream=True)
    assert r.status_code == 200
    # Stream should contain the output
    content = r.text
    assert 'stream_hello' in content


def test_exec_failure():
    """Test /exec endpoint with failing command"""
    payload = {"cmd": "exit 1", "timeout": 5}
    r = requests.post(f"{ROOTD_URL}/exec", json=payload, headers=HEADERS)
    assert r.status_code == 200
    data = r.json()
    assert data['returncode'] == 1


def test_exec_with_timeout():
    """Test /exec endpoint with timeout"""
    payload = {"cmd": "sleep 10", "timeout": 1}
    r = requests.post(f"{ROOTD_URL}/exec", json=payload, headers=HEADERS)
    assert r.status_code == 200
    data = r.json()
    # Should timeout and return error
    assert 'error' in data or data.get('returncode') != 0


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
=======
=======
>>>>>>> headroom-spill-integration
def test_system_info():
    r = requests.get(f"{ROOTD_URL}/system/info", headers=HEADERS)
    print("GET /system/info →", r.status_code, r.json())


def test_exec():
    payload = {"cmd": "echo hello_rootd", "timeout": 5}
    r = requests.post(f"{ROOTD_URL}/exec", json=payload, headers=HEADERS)
    print("POST /exec →", r.status_code, r.json())


def test_exec_stream():
    payload = {"cmd": "echo stream_hello"}
    r = requests.post(f"{ROOTD_URL}/exec/stream", json=payload, headers=HEADERS, stream=True)
    print("POST /exec/stream →", r.status_code)
    print(r.text)


if __name__ == "__main__":
    print("=== Testing rootd on", ROOTD_URL, "===\n")
    test_system_info()
    test_exec()
    test_exec_stream()

<<<<<<< HEAD
>>>>>>> headroom-spill-integration
=======
>>>>>>> headroom-spill-integration
