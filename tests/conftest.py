from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import IO, Final

import pytest
import requests

from gptadmin_security import load_or_create_identity

INTEGRATION_FLAG: Final[str] = "GPTADMIN_INTEGRATION_TESTS"
DEFAULT_HOST: Final[str] = "127.0.0.1"
DEFAULT_CTL_TOKEN: Final[str] = "chatgpt_secret"
DEFAULT_SHELLMCP_TOKEN: Final[str] = "srv_secret"
REQUEST_TIMEOUT_S: Final[float] = 1.0
STARTUP_TIMEOUT_S: Final[float] = 20.0
REPO_DIR = Path(__file__).resolve().parents[1]
BUILD_DIR = REPO_DIR / "build" / "pytest-integration"


@dataclass(slots=True)
class ManagedProcess:
    """Bookkeeping for a subprocess started by the legacy integration harness."""

    name: str
    process: subprocess.Popen[str]
    log_file: IO[str]
    log_path: Path


def _allocate_port() -> int:
    """Return a free local TCP port."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind((DEFAULT_HOST, 0))
        return int(sock.getsockname()[1])


def _bootstrap_legacy_env() -> bool:
    """Populate environment variables before test modules import them.

    Returns True when this session should start local services itself.
    """
    if os.environ.get(INTEGRATION_FLAG) != "1":
        return False

    manage_local = "HUB_URL" not in os.environ or "SHELLMCP_URL" not in os.environ
    os.environ.setdefault("CTL_TOKEN", DEFAULT_CTL_TOKEN)
    os.environ.setdefault("SHELLMCP_TOKEN", DEFAULT_SHELLMCP_TOKEN)

    if manage_local:
        hub_port = _allocate_port()
        shellmcp_port = _allocate_port()
        while shellmcp_port == hub_port:
            shellmcp_port = _allocate_port()
        server_name = os.environ.get("SHELLMCP_NAME") or socket.gethostname()
        identity_dir = BUILD_DIR / "shellmcp-identity"
        identity = load_or_create_identity(identity_dir, server_name, prefix="shellmcp")
        approved_file = BUILD_DIR / "approved_servers.json"
        current_user = os.environ.get("USER") or os.environ.get("LOGNAME") or ""
        default_home = str(Path.home())
        default_cwd = str(REPO_DIR)

        os.environ["HUB_PORT"] = str(hub_port)
        os.environ["HUB_URL"] = f"http://{DEFAULT_HOST}:{hub_port}"
        os.environ["GPTADMIN_APPROVED_SERVERS_FILE"] = str(approved_file)
        os.environ["GPTADMIN_PENDING_SERVERS_FILE"] = str(BUILD_DIR / "pending_servers.json")
        os.environ["GPTADMIN_SERVERS_STATE_FILE"] = str(BUILD_DIR / "servers_state.json")
        os.environ["GPTADMIN_TASKS_STATE_FILE"] = str(BUILD_DIR / "tasks_state.json")
        os.environ["GPTADMIN_MCP_AGENTS_STATE_FILE"] = str(BUILD_DIR / "mcp_agents_state.json")
        os.environ["GPTADMIN_MCP_JOBS_STATE_FILE"] = str(BUILD_DIR / "mcp_jobs_state.json")
        os.environ["GPTADMIN_AUTH_CLIENTS_STATE_FILE"] = str(BUILD_DIR / "auth_clients_state.json")
        os.environ["GPTADMIN_PORT_FORWARDS_FILE"] = str(BUILD_DIR / "port_forwards.json")
        os.environ["GPTADMIN_TRANSFERS_DIR"] = str(BUILD_DIR / "transfers")
        os.environ["SHELLMCP_PORT"] = str(shellmcp_port)
        os.environ["PORT"] = str(shellmcp_port)
        os.environ["SHELLMCP_URL"] = f"http://{DEFAULT_HOST}:{shellmcp_port}"
        os.environ["SHELLMCP_NAME"] = server_name
        os.environ["SHELL_NAME"] = server_name
        os.environ["SHELLMCP_IDENTITY_DIR"] = str(identity_dir)
        os.environ["SHELL_IDENTITY_DIR"] = str(identity_dir)
        os.environ["SHELLMCP_DEFAULT_HOME"] = default_home
        os.environ["SHELL_DEFAULT_HOME"] = default_home
        os.environ["SHELLMCP_DEFAULT_CWD"] = default_cwd
        os.environ["SHELL_DEFAULT_CWD"] = default_cwd
        if current_user:
            os.environ["SHELLMCP_DEFAULT_USER"] = current_user
            os.environ["SHELL_DEFAULT_USER"] = current_user

        approved_file.parent.mkdir(parents=True, exist_ok=True)
        approved_payload = {
            server_name: {
                "name": server_name,
                "status": "approved",
                "approved_at": time.time(),
                "approved_by": "pytest-fixture",
                "approved_via": "tests/conftest.py",
                "approved_subject": server_name,
                "base_url": os.environ["SHELLMCP_URL"],
                "server_id": identity["identity"]["server_id"],
                "public_key": identity["public_key_b64"],
                "fingerprint": identity["fingerprint"],
                "default_cwd": default_cwd,
                "backend": "local",
            }
        }
        approved_file.write_text(
            json.dumps(approved_payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )

    return manage_local


_MANAGE_LOCAL_SERVICES = _bootstrap_legacy_env()


def _wait_for_http(
    url: str,
    *,
    process: subprocess.Popen[str],
    log_path: Path,
    timeout_s: float = STARTUP_TIMEOUT_S,
    headers: dict[str, str] | None = None,
) -> None:
    """Wait until a service responds successfully or exits early."""
    deadline = time.monotonic() + timeout_s
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"{log_path.name} exited early with code {process.returncode}; see {log_path}")
        try:
            response = requests.get(url, headers=headers, timeout=REQUEST_TIMEOUT_S)
            if response.status_code == 200:
                return
            last_error = RuntimeError(f"unexpected HTTP {response.status_code}")
        except Exception as exc:  # service may still be starting
            last_error = exc
        time.sleep(0.2)
    raise RuntimeError(f"timed out waiting for {url}; last_error={last_error}; see {log_path}")


def _start_service(name: str, script: Path, env: dict[str, str]) -> ManagedProcess:
    """Start a helper service and tee its output into a log file."""
    BUILD_DIR.mkdir(parents=True, exist_ok=True)
    log_path = BUILD_DIR / f"{name}.log"
    log_file = log_path.open("a", encoding="utf-8")
    process = subprocess.Popen(
        [sys.executable, str(script)],
        cwd=str(REPO_DIR),
        env=env,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    return ManagedProcess(name=name, process=process, log_file=log_file, log_path=log_path)


def _stop_service(managed: ManagedProcess) -> None:
    """Terminate a managed service and close its log file."""
    process = managed.process
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    managed.log_file.close()


@pytest.fixture(scope="session", autouse=True)
def start_services():
    """Legacy Python hub autostart was removed; Go hub tests manage their own process."""
    if _MANAGE_LOCAL_SERVICES:
        raise RuntimeError("GPTADMIN_MANAGE_TEST_SERVICES used to start the removed Python hub; use Go hub test harness instead")
    yield
