#!/usr/bin/env python3
"""Integration tests for gptadmin_hub."""

import json
import os
import socket
import threading
import time
from pathlib import Path

import pytest
import requests
from cryptography.hazmat.primitives import serialization

from gptadmin_security import sign_request

pytestmark = pytest.mark.skipif(
    os.environ.get("GPTADMIN_INTEGRATION_TESTS") != "1",
    reason="legacy live-service integration test; set GPTADMIN_INTEGRATION_TESTS=1",
)

HUB_URL = os.getenv("HUB_URL", "http://localhost:9001")
CTL_TOKEN = os.getenv("CTL_TOKEN", "chatgpt_secret")
SHELLMCP_URL = os.getenv("SHELLMCP_URL", "http://localhost:25900")
SHELLMCP_TOKEN = os.getenv("SHELLMCP_TOKEN", "srv_secret")
SERVER_NAME = os.getenv("SHELLMCP_NAME") or socket.gethostname()
HEADERS_HUB = {"Authorization": f"Bearer {CTL_TOKEN}"}
REQUEST_TIMEOUT_S = 30.0


def _load_shellmcp_identity() -> tuple[dict, object]:
    """Load the managed shellmcp identity used for signed heartbeats."""
    identity_dir = Path(os.environ["SHELLMCP_IDENTITY_DIR"])
    identity = json.loads((identity_dir / "shellmcp_identity.json").read_text(encoding="utf-8"))
    private_key = serialization.load_pem_private_key((identity_dir / "shellmcp_ed25519").read_bytes(), password=None)
    return identity, private_key


def send_heartbeat(mode: str = "webhook") -> None:
    """Register the shellmcp instance with the hub."""
    identity, private_key = _load_shellmcp_identity()
    payload = {
        "name": SERVER_NAME,
        "server_id": identity["server_id"],
        "public_key": identity["public_key"],
        "fingerprint": identity["fingerprint"],
        "base_url": SHELLMCP_URL,
        "shellmcp_token": SHELLMCP_TOKEN,
        "time": int(time.time()),
        "mode": mode,
        "default_home": os.getenv("SHELLMCP_DEFAULT_HOME"),
        "default_cwd": os.getenv("SHELLMCP_DEFAULT_CWD"),
        "default_user": os.getenv("SHELLMCP_DEFAULT_USER"),
        "backend": "local",
    }
    body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    signed = sign_request(private_key, "POST", "/heartbeat", body)
    headers = {
        "Content-Type": "application/json",
        "X-GPTAdmin-Server": SERVER_NAME,
        "X-GPTAdmin-Server-ID": identity["server_id"],
        "X-GPTAdmin-Timestamp": signed["timestamp"],
        "X-GPTAdmin-Nonce": signed["nonce"],
        "X-GPTAdmin-Signature": signed["signature"],
    }
    response = requests.post(f"{HUB_URL}/heartbeat", data=body, headers=headers, timeout=REQUEST_TIMEOUT_S)
    assert response.status_code == 200, response.text
    assert response.json().get("status") == "active", response.text


def test_proxy_system_info() -> None:
    """Test proxying /system/info through hub."""
    send_heartbeat()
    time.sleep(0.5)

    response = requests.get(
        f"{HUB_URL}/srv/system/info",
        params={"server": SERVER_NAME},
        headers=HEADERS_HUB,
        timeout=REQUEST_TIMEOUT_S,
    )
    assert response.status_code == 200, response.text
    data = response.json()
    assert data.get("host") or data.get("platform")


def test_bulk_exec() -> None:
    """Test bulk exec through hub."""
    send_heartbeat()
    time.sleep(0.5)

    payload = {"servers": [SERVER_NAME], "cmd": "echo bulk"}
    response = requests.post(f"{HUB_URL}/bulk/exec", json=payload, headers=HEADERS_HUB, timeout=REQUEST_TIMEOUT_S)
    assert response.status_code == 200, response.text
    data = response.json()
    assert isinstance(data.get("results"), dict), data
    assert SERVER_NAME in data["results"], data


def test_proxy_exec_polling() -> None:
    """Test exec with polling mode."""
    send_heartbeat("polling")
    time.sleep(0.5)

    worker_done = threading.Event()
    worker_errors: list[str] = []

    def worker() -> None:
        try:
            for _ in range(50):
                response = requests.get(
                    f"{HUB_URL}/queue/{SERVER_NAME}",
                    params={"token": SHELLMCP_TOKEN},
                    timeout=1.0,
                )
                job = response.json()
                if job.get("cmd"):
                    os.system(job["cmd"])
                    result = {
                        "id": job["id"],
                        "result": {"returncode": 0, "stdout": "polled\n", "stderr": ""},
                    }
                    post_response = requests.post(
                        f"{HUB_URL}/queue/{SERVER_NAME}/result",
                        params={"token": SHELLMCP_TOKEN},
                        json=result,
                        timeout=REQUEST_TIMEOUT_S,
                    )
                    if post_response.status_code != 200:
                        worker_errors.append(post_response.text)
                    break
                time.sleep(0.1)
        except Exception as exc:
            worker_errors.append(str(exc))
        finally:
            worker_done.set()

    threading.Thread(target=worker, daemon=True).start()

    payload = {"cmd": "echo polled"}
    response = requests.post(
        f"{HUB_URL}/srv/exec",
        params={"server": SERVER_NAME},
        json=payload,
        headers=HEADERS_HUB,
        timeout=REQUEST_TIMEOUT_S,
    )
    assert response.status_code == 200, response.text
    data = response.json()
    assert data.get("status") in {"running", "completed"} or data.get("background") or "returncode" in data, data
    if data.get("status") == "running" or data.get("background") or data.get("task_id"):
        assert worker_done.wait(REQUEST_TIMEOUT_S), "polling worker did not finish"
        assert not worker_errors, worker_errors


def test_bulk_exec_polling() -> None:
    """Test bulk exec with polling mode."""
    send_heartbeat("polling")
    time.sleep(0.5)

    worker_done = threading.Event()
    worker_errors: list[str] = []

    def worker() -> None:
        try:
            for _ in range(50):
                response = requests.get(
                    f"{HUB_URL}/queue/{SERVER_NAME}",
                    params={"token": SHELLMCP_TOKEN},
                    timeout=1.0,
                )
                job = response.json()
                if job.get("cmd"):
                    os.system(job["cmd"])
                    result = {
                        "id": job["id"],
                        "result": {"returncode": 0, "stdout": "bulk polled\n", "stderr": ""},
                    }
                    post_response = requests.post(
                        f"{HUB_URL}/queue/{SERVER_NAME}/result",
                        params={"token": SHELLMCP_TOKEN},
                        json=result,
                        timeout=REQUEST_TIMEOUT_S,
                    )
                    if post_response.status_code != 200:
                        worker_errors.append(post_response.text)
                    break
                time.sleep(0.1)
        except Exception as exc:
            worker_errors.append(str(exc))
        finally:
            worker_done.set()

    threading.Thread(target=worker, daemon=True).start()

    payload = {"servers": [SERVER_NAME], "cmd": "echo bulk polled"}
    response = requests.post(f"{HUB_URL}/bulk/exec", json=payload, headers=HEADERS_HUB, timeout=REQUEST_TIMEOUT_S)
    assert response.status_code == 200, response.text
    data = response.json()
    assert isinstance(data.get("results"), dict), data
    server_result = data["results"].get(SERVER_NAME)
    assert server_result is not None, data
    assert server_result.get("status") in {"running", "completed"} or server_result.get("background") or "returncode" in server_result, server_result
    if server_result.get("status") == "running" or server_result.get("background") or server_result.get("task_id"):
        assert worker_done.wait(REQUEST_TIMEOUT_S), "polling worker did not finish"
        assert not worker_errors, worker_errors


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
