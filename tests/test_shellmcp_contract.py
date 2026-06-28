#!/usr/bin/env python3
"""Black-box shellmcp HTTP contract tests.

These tests intentionally exercise shellmcp through its HTTP API instead of importing
implementation internals. Any implementation can run this suite (Python, Go, Rust,
etc.) by setting SHELLMCP_CONTRACT_COMMANDS to one or more newline-separated commands
that start a shellmcp-compatible process.

Example:
  SHELLMCP_CONTRACT_COMMANDS=$'python3 client/shellmcp_pure.py\n./target/release/shellmcp-rs' pytest tests/test_shellmcp_contract.py
"""
from __future__ import annotations

import contextlib
import json
import os
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_COMMANDS = [f"{sys.executable} {ROOT / 'client' / 'shellmcp_pure.py'}"]


def _contract_commands() -> list[str]:
    raw = os.getenv("SHELLMCP_CONTRACT_COMMANDS", "").strip()
    if not raw:
        return DEFAULT_COMMANDS
    commands = [line.strip() for line in raw.splitlines() if line.strip() and not line.strip().startswith("#")]
    return commands or DEFAULT_COMMANDS


CONTRACT_COMMANDS = _contract_commands()


def _free_port() -> int:
    with contextlib.closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def _http_json(method: str, url: str, token: str, payload: dict | None = None, timeout: float = 5.0) -> tuple[int, dict]:
    data = None
    headers = {"Authorization": f"Bearer {token}", "Connection": "close"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
            return resp.status, json.loads(body.decode("utf-8") or "{}")
    except urllib.error.HTTPError as e:
        body = e.read()
        try:
            parsed = json.loads(body.decode("utf-8") or "{}")
        except Exception:
            parsed = {"raw": body.decode("utf-8", "replace")}
        return e.code, parsed


@dataclass
class ShellmcpProcess:
    command: str
    port: int
    token: str
    proc: subprocess.Popen
    log_path: Path

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def request(self, method: str, path: str, payload: dict | None = None) -> tuple[int, dict]:
        return _http_json(method, f"{self.base_url}{path}", self.token, payload)

    def stop(self) -> None:
        if self.proc.poll() is not None:
            return
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait(timeout=5)


@pytest.fixture(params=CONTRACT_COMMANDS, ids=lambda cmd: Path(cmd.split()[0]).name)
def shellmcp_contract(request, tmp_path: Path):
    command = request.param
    port = _free_port()
    token = "contract-token"
    current_user = os.getenv("USER") or os.getenv("LOGNAME") or ""
    home = str(Path.home())
    cwd = str(tmp_path / "default-cwd")
    Path(cwd).mkdir()
    identity_dir = tmp_path / "identity"
    spill_dir = tmp_path / "spool"
    log_path = tmp_path / "shellmcp.log"

    env = os.environ.copy()
    env.update(
        {
            "SHELLMCP_TOKEN": token,
            "SHELL_TOKEN": token,
            "SHELLMCP_PORT": str(port),
            "SHELL_PORT": str(port),
            "SHELLMCP_BIND": "127.0.0.1",
            "SHELL_HOST": "127.0.0.1",
            "SHELLMCP_NAME": "contract-shellmcp",
            "SHELL_NAME": "contract-shellmcp",
            "SHELLMCP_URL": f"http://127.0.0.1:{port}",
            "SHELL_URL": f"http://127.0.0.1:{port}",
            "SHELLMCP_IDENTITY_DIR": str(identity_dir),
            "SHELL_IDENTITY_DIR": str(identity_dir),
            "SHELLMCP_SPILL_DIR": str(spill_dir),
            "SHELL_SPILL_DIR": str(spill_dir),
            "SHELLMCP_DEFAULT_CWD": cwd,
            "SHELL_DEFAULT_CWD": cwd,
            "SHELLMCP_DEFAULT_HOME": home,
            "SHELL_DEFAULT_HOME": home,
            # Disable background hub/queue behavior for deterministic local contract tests.
            "HUB_URL": "",
            "QUEUE_URL": "",
            "SHELLMCP_QUEUE": "0",
            "SHELL_QUEUE": "0",
            "SHELLMCP_HEARTBEAT": "0",
            "SHELL_HEARTBEAT": "0",
        }
    )
    if current_user:
        env["SHELLMCP_DEFAULT_USER"] = current_user
        env["SHELL_DEFAULT_USER"] = current_user

    with log_path.open("wb") as log:
        proc = subprocess.Popen(
            command,
            shell=True,
            cwd=str(ROOT),
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )

    shellmcp = ShellmcpProcess(command=command, port=port, token=token, proc=proc, log_path=log_path)
    deadline = time.time() + 10
    last_error = None
    while time.time() < deadline:
        if proc.poll() is not None:
            raise AssertionError(f"shellmcp exited early rc={proc.returncode}; log:\n{log_path.read_text(errors='replace')}")
        try:
            status, _ = shellmcp.request("GET", "/system/info")
            if status == 200:
                break
            last_error = f"HTTP {status}"
        except Exception as e:  # server may not be listening yet
            last_error = repr(e)
        time.sleep(0.1)
    else:
        shellmcp.stop()
        raise AssertionError(f"shellmcp did not become ready: {last_error}; log:\n{log_path.read_text(errors='replace')}")

    try:
        yield shellmcp
    finally:
        shellmcp.stop()


def test_shellmcp_contract_requires_auth(shellmcp_contract: ShellmcpProcess):
    req = urllib.request.Request(f"{shellmcp_contract.base_url}/system/info", headers={"Connection": "close"})
    with pytest.raises(urllib.error.HTTPError) as ei:
        urllib.request.urlopen(req, timeout=5)
    assert ei.value.code == 401


def test_shellmcp_contract_system_endpoints_advertise_identity_and_defaults(shellmcp_contract: ShellmcpProcess):
    status, info = shellmcp_contract.request("GET", "/system/info")
    assert status == 200
    assert info.get("host") or info.get("hostname") or info.get("name")

    # Defaults are part of the shellmcp health/metadata contract. Implementations in
    # any language should expose them here so the hub can reason about execution.
    status, health = shellmcp_contract.request("GET", "/system/health")
    assert status == 200
    assert health.get("default_cwd")
    assert health.get("default_home")
    if os.getenv("USER") or os.getenv("LOGNAME"):
        assert health.get("default_user") in {os.getenv("USER"), os.getenv("LOGNAME")}


def test_shellmcp_contract_exec_supports_cwd_env_and_default_user(shellmcp_contract: ShellmcpProcess):
    cmd = "printf 'user=%s\n' \"$(whoami)\"; printf 'pwd=%s\n' \"$(pwd)\"; printf 'env=%s\n' \"$SHELLMCP_CONTRACT_VAR\"; printf 'home=%s\n' \"$HOME\""
    status, res = shellmcp_contract.request(
        "POST",
        "/exec",
        {"cmd": cmd, "timeout": 5, "env": {"SHELLMCP_CONTRACT_VAR": "ok-env"}},
    )
    assert status == 200
    assert res.get("returncode") == 0, res
    out = res.get("stdout", "")
    assert "env=ok-env" in out
    assert "pwd=" in out and "default-cwd" in out
    assert "home=" in out and str(Path.home()) in out
    if os.getenv("USER") or os.getenv("LOGNAME"):
        assert f"user={os.getenv('USER') or os.getenv('LOGNAME')}" in out


def test_shellmcp_contract_exec_allows_explicit_cwd(shellmcp_contract: ShellmcpProcess, tmp_path: Path):
    custom_cwd = tmp_path / "request-cwd"
    custom_cwd.mkdir()
    status, res = shellmcp_contract.request("POST", "/exec", {"cmd": "pwd", "timeout": 5, "cwd": str(custom_cwd)})
    assert status == 200
    assert res.get("returncode") == 0, res
    assert str(custom_cwd) in res.get("stdout", "")


def test_shellmcp_contract_exec_reports_failures(shellmcp_contract: ShellmcpProcess):
    status, res = shellmcp_contract.request("POST", "/exec", {"cmd": "echo bad >&2; exit 23", "timeout": 5})
    assert status == 200
    assert res.get("returncode") == 23
    assert "bad" in res.get("stderr", "")
