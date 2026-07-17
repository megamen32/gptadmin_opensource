#!/usr/bin/env python3
"""Black-box AirShell MCP and HTTP contract tests.

These tests intentionally exercise shellmcp through its HTTP API instead of importing
implementation internals. Any implementation can run this suite (Python, Go, Rust,
etc.) by setting SHELLMCP_CONTRACT_COMMANDS to one or more newline-separated commands
that start a shellmcp-compatible process.

Example:
  SHELLMCP_CONTRACT_COMMANDS=$'go run ./cmd/shellmcp-go\n./target/release/airshell-mcp' pytest tests/test_shellmcp_contract.py
"""
from __future__ import annotations

import contextlib
import json
import os
import pwd
import shlex
import shutil
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator

import pytest

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_COMMANDS = ["go run ./cmd/shellmcp-go"]


def _contract_commands() -> list[str]:
    raw = os.getenv("SHELLMCP_CONTRACT_COMMANDS", "").strip()
    if not raw:
        return DEFAULT_COMMANDS
    commands = [line.strip() for line in raw.splitlines() if line.strip() and not line.strip().startswith("#")]
    return commands or DEFAULT_COMMANDS


CONTRACT_COMMANDS = _contract_commands()


pytestmark = pytest.mark.skipif(
    CONTRACT_COMMANDS == DEFAULT_COMMANDS and shutil.which("go") is None,
    reason="Go is unavailable for the default AirShell MCP contract command",
)


def _free_port() -> int:
    with contextlib.closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def _header(headers: dict[str, str], name: str) -> str | None:
    """Read an HTTP header without depending on client-side casing normalization."""
    wanted = name.lower()
    return next((value for key, value in headers.items() if key.lower() == wanted), None)


def _http_json(method: str, url: str, token: str | None, payload: dict[str, Any] | None = None, timeout: float = 5.0) -> tuple[int, dict[str, Any], dict[str, str]]:
    """Send one direct JSON request without inheriting workstation proxy settings."""
    data = None
    headers = {"Connection": "close"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=timeout) as resp:
            body = resp.read()
            return resp.status, json.loads(body.decode("utf-8") or "{}"), dict(resp.headers.items())
    except urllib.error.HTTPError as e:
        body = e.read()
        try:
            parsed = json.loads(body.decode("utf-8") or "{}")
        except Exception:
            parsed = {"raw": body.decode("utf-8", "replace")}
        return e.code, parsed, dict(e.headers.items())


@dataclass
class ShellmcpProcess:
    command: str
    port: int
    token: str
    proc: subprocess.Popen[bytes]
    log_path: Path

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def request(self, method: str, path: str, payload: dict[str, Any] | None = None) -> tuple[int, dict[str, Any], dict[str, str]]:
        return _http_json(method, f"{self.base_url}{path}", self.token, payload)

    def mcp(self, method: str, params: dict[str, Any], request_id: int) -> tuple[dict[str, Any], dict[str, str]]:
        """Call one MCP JSON-RPC method and return its successful response."""
        status, body, headers = self.request(
            "POST",
            "/mcp",
            {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params},
        )
        assert status == 200, body
        assert body.get("jsonrpc") == "2.0", body
        assert body.get("id") == request_id, body
        assert "error" not in body, body
        return body["result"], headers

    def stop(self) -> None:
        if self.proc.poll() is not None:
            return
        try:
            os.killpg(os.getpgid(self.proc.pid), signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(os.getpgid(self.proc.pid), signal.SIGKILL)
            self.proc.wait(timeout=5)


def _command_id(command: str) -> str:
    """Create a compact pytest id for a process command."""
    return Path(shlex.split(command)[0]).name


def _root_test_user() -> str | None:
    """Return a real non-root account usable for an execution ownership test."""
    if os.geteuid() != 0:
        try:
            return pwd.getpwuid(os.geteuid()).pw_name
        except KeyError:
            return None
    sudo_user = os.getenv("SUDO_USER", "")
    if sudo_user and sudo_user != "root":
        return sudo_user
    for entry in pwd.getpwall():
        if entry.pw_uid >= 1000 and entry.pw_name != "nobody":
            return entry.pw_name
    return None


def _can_start_root_process() -> bool:
    """Check whether this test environment can start an isolated root daemon."""
    if os.geteuid() == 0:
        return True
    try:
        return subprocess.run(["sudo", "-n", "true"], check=False, capture_output=True, timeout=2).returncode == 0
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return False


def _cleanup_root_test_dir(path: Path) -> None:
    """Remove root-owned test state after a privileged black-box process exits."""
    if os.geteuid() == 0:
        shutil.rmtree(path, ignore_errors=True)
        return
    subprocess.run(["sudo", "-n", "rm", "-rf", str(path)], check=True, timeout=5)


def _stop_root_shellmcp(shellmcp: ShellmcpProcess) -> None:
    """Stop the launcher and any go-run child still holding the test port."""
    shellmcp.stop()
    if os.geteuid() == 0:
        subprocess.run(["fuser", "-k", f"{shellmcp.port}/tcp"], check=False, capture_output=True, timeout=5)
    else:
        subprocess.run(["sudo", "-n", "fuser", "-k", f"{shellmcp.port}/tcp"], check=False, capture_output=True, timeout=5)


def _root_contract_command(command: str, tmp_path: Path) -> str:
    """Build the default Go implementation before launching it as root.

    Go keeps toolchains and module downloads in the invoking user's cache.  A
    root ``go run`` on a clean CI runner would download those dependencies
    during the server readiness window, making this black-box ownership test
    depend on network timing rather than the AirShell contract.
    """
    if command != DEFAULT_COMMANDS[0]:
        return command

    binary = tmp_path / "shellmcp-contract"
    subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/shellmcp-go"],
        cwd=str(ROOT / "go-shellmcp"),
        check=True,
        timeout=120,
    )
    return str(binary)


def _start_root_shellmcp(command: str, tmp_path: Path, default_user: str | None) -> ShellmcpProcess:
    """Start a contract daemon as root while keeping test configuration explicit."""
    port = _free_port()
    token = "root-contract-token"
    cwd = tmp_path / "root-default-cwd"
    cwd.mkdir(mode=0o777, parents=True)
    cwd.chmod(0o777)
    identity_dir = tmp_path / "identity"
    spill_dir = tmp_path / "spool"
    log_path = tmp_path / "root-shellmcp.log"
    env = {
        "SHELLMCP_TOKEN": token,
        "SHELL_TOKEN": token,
        "SHELLMCP_PORT": str(port),
        "SHELL_PORT": str(port),
        "SHELLMCP_BIND": "127.0.0.1",
        "SHELL_HOST": "127.0.0.1",
        "SHELLMCP_NAME": "root-contract-shellmcp",
        "SHELL_NAME": "root-contract-shellmcp",
        "SHELLMCP_URL": f"http://127.0.0.1:{port}",
        "SHELL_URL": f"http://127.0.0.1:{port}",
        "SHELLMCP_IDENTITY_DIR": str(identity_dir),
        "SHELL_IDENTITY_DIR": str(identity_dir),
        "SHELLMCP_SPILL_DIR": str(spill_dir),
        "SHELL_SPILL_DIR": str(spill_dir),
        "SHELLMCP_DEFAULT_CWD": str(cwd),
        "SHELL_DEFAULT_CWD": str(cwd),
        "SHELLMCP_QUEUE": "0",
        "SHELL_QUEUE": "0",
        "SHELLMCP_HEARTBEAT": "0",
        "SHELL_HEARTBEAT": "0",
        "HUB_URL": "",
        "QUEUE_URL": "",
    }
    if default_user:
        env["SHELLMCP_DEFAULT_USER"] = default_user
        env["SHELL_DEFAULT_USER"] = default_user

    launcher = tmp_path / "run-root-contract.sh"
    exports = "\n".join(f"export {key}={shlex.quote(value)}" for key, value in env.items())
    launcher.write_text(f"#!/bin/sh\nset -eu\n{exports}\nexec {command}\n")
    launcher.chmod(0o755)
    root_command = ["/bin/sh", str(launcher)] if os.geteuid() == 0 else ["sudo", "-n", "/bin/sh", str(launcher)]
    with log_path.open("wb") as log:
        proc = subprocess.Popen(
            root_command,
            cwd=str(ROOT / "go-shellmcp") if command == DEFAULT_COMMANDS[0] else str(ROOT),
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    shellmcp = ShellmcpProcess(command=command, port=port, token=token, proc=proc, log_path=log_path)
    deadline = time.time() + 15
    while time.time() < deadline:
        if proc.poll() is not None:
            raise AssertionError(f"root shellmcp exited early rc={proc.returncode}; log:\n{log_path.read_text(errors='replace')}")
        try:
            if shellmcp.request("GET", "/system/info")[0] == 200:
                return shellmcp
        except Exception:
            pass
        time.sleep(0.1)
    shellmcp.stop()
    raise AssertionError(f"root shellmcp did not become ready; log:\n{log_path.read_text(errors='replace')}")


@pytest.fixture(params=CONTRACT_COMMANDS, ids=_command_id)
def shellmcp_contract(request: pytest.FixtureRequest, tmp_path: Path) -> Iterator[ShellmcpProcess]:
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
            cwd=str(ROOT / "go-shellmcp") if command == DEFAULT_COMMANDS[0] else str(ROOT),
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
            status, _, _ = shellmcp.request("GET", "/system/info")
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


def test_shellmcp_contract_requires_auth(shellmcp_contract: ShellmcpProcess) -> None:
    """Require the agent token for both legacy HTTP and MCP requests."""
    status, _, _ = _http_json("GET", f"{shellmcp_contract.base_url}/system/info", None)
    assert status == 401

    status, body, _ = _http_json(
        "POST",
        f"{shellmcp_contract.base_url}/mcp",
        None,
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
    )
    assert status == 401, body


def test_shellmcp_contract_system_endpoints_advertise_identity_and_defaults(shellmcp_contract: ShellmcpProcess) -> None:
    """Keep system metadata consistent for all compatible implementations."""
    status, info, _ = shellmcp_contract.request("GET", "/system/info")
    assert status == 200
    assert info.get("host") or info.get("hostname") or info.get("name")

    # Defaults are part of the shellmcp health/metadata contract. Implementations in
    # any language should expose them here so the hub can reason about execution.
    status, health, _ = shellmcp_contract.request("GET", "/system/health")
    assert status == 200
    assert health.get("default_cwd")
    assert health.get("default_home")
    if os.getenv("USER") or os.getenv("LOGNAME"):
        assert health.get("default_user") in {os.getenv("USER"), os.getenv("LOGNAME")}


def test_shellmcp_contract_exec_supports_cwd_env_and_default_user(shellmcp_contract: ShellmcpProcess) -> None:
    cmd = "printf 'user=%s\n' \"$(whoami)\"; printf 'pwd=%s\n' \"$(pwd)\"; printf 'env=%s\n' \"$SHELLMCP_CONTRACT_VAR\"; printf 'home=%s\n' \"$HOME\""
    status, res, _ = shellmcp_contract.request(
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


def test_shellmcp_contract_exec_allows_explicit_cwd(shellmcp_contract: ShellmcpProcess, tmp_path: Path) -> None:
    custom_cwd = tmp_path / "request-cwd"
    custom_cwd.mkdir()
    status, res, _ = shellmcp_contract.request("POST", "/exec", {"cmd": "pwd", "timeout": 5, "cwd": str(custom_cwd)})
    assert status == 200
    assert res.get("returncode") == 0, res
    assert str(custom_cwd) in res.get("stdout", "")


def test_shellmcp_contract_exec_reports_failures(shellmcp_contract: ShellmcpProcess) -> None:
    """Preserve a command's exit status and stderr through the HTTP API."""
    status, res, _ = shellmcp_contract.request("POST", "/exec", {"cmd": "echo bad >&2; exit 23", "timeout": 5})
    assert status == 200
    assert res.get("returncode") == 23
    assert "bad" in res.get("stderr", "")


@pytest.mark.parametrize("command", CONTRACT_COMMANDS, ids=_command_id)
def test_shellmcp_contract_root_daemon_never_uses_implicit_root(command: str, tmp_path: Path) -> None:
    """Prove ownership through the public API, not implementation-specific hooks."""
    if not _can_start_root_process():
        pytest.skip("passwordless sudo or a root test runner is required")
    default_user = _root_test_user()
    if not default_user:
        pytest.skip("no non-root account is available for the ownership contract")

    root_command = _root_contract_command(command, tmp_path)
    guarded = _start_root_shellmcp(root_command, tmp_path / "guarded", None)
    try:
        status, denied, _ = guarded.request("POST", "/exec", {"cmd": "id -u", "timeout": 5})
        assert status >= 400, denied
        assert "default_user" in str(denied).lower(), denied
    finally:
        _stop_root_shellmcp(guarded)
        _cleanup_root_test_dir(tmp_path / "guarded")

    shellmcp = _start_root_shellmcp(root_command, tmp_path / "default-user", default_user)
    try:
        marker = "user-owned-marker"
        status, res, _ = shellmcp.request("POST", "/exec", {"cmd": f"touch {marker}; stat -c '%U' {marker}", "timeout": 5})
        assert status == 200, res
        assert res.get("returncode") == 0, res
        assert res.get("stdout", "").strip() == default_user, res
        assert res.get("run_as_user") == default_user, res

        executed, _ = shellmcp.mcp(
            "tools/call",
            {"name": "shell_exec", "arguments": {"cmd": "id -un", "timeout": 5}},
            90,
        )
        result = executed.get("structuredContent", {}).get("result", {})
        assert result.get("returncode") == 0, executed
        assert result.get("stdout", "").strip() == default_user, executed

        status, root_res, _ = shellmcp.request(
            "POST", "/exec", {"cmd": "id -un", "timeout": 5, "run_as_user": "root"}
        )
        assert status == 200, root_res
        assert root_res.get("returncode") == 0, root_res
        assert root_res.get("stdout", "").strip() == "root", root_res
    finally:
        _stop_root_shellmcp(shellmcp)
        _cleanup_root_test_dir(tmp_path / "default-user")


def test_shellmcp_contract_mcp_protocol_and_tools(shellmcp_contract: ShellmcpProcess) -> None:
    """Exercise the public AirShell MCP server independently of its implementation language."""
    initialized, headers = shellmcp_contract.mcp("initialize", {}, 10)
    assert initialized.get("protocolVersion")
    assert initialized.get("serverInfo", {}).get("name")
    assert _header(headers, "MCP-Protocol-Version") == initialized["protocolVersion"]
    assert _header(headers, "Mcp-Session-Id")

    listed, _ = shellmcp_contract.mcp("tools/list", {}, 11)
    tools = listed.get("tools")
    assert isinstance(tools, list)
    names = {tool.get("name") for tool in tools if isinstance(tool, dict)}
    assert {"shell_exec", "system_info", "mcp_manage", "mcp_tools", "mcp_call", "tasks"} <= names

    system_info, _ = shellmcp_contract.mcp("tools/call", {"name": "system_info", "arguments": {}}, 12)
    assert system_info.get("structuredContent", {}).get("system")

    executed, _ = shellmcp_contract.mcp(
        "tools/call",
        {"name": "shell_exec", "arguments": {"cmd": "printf airshell_mcp_contract", "timeout": 5}},
        13,
    )
    result = executed.get("structuredContent", {}).get("result", {})
    assert result.get("returncode") == 0, executed
    assert result.get("stdout") == "airshell_mcp_contract", executed


def test_shellmcp_contract_mcp_resources_and_polling(shellmcp_contract: ShellmcpProcess) -> None:
    """Expose resources and streamable-HTTP polling metadata through `/mcp`."""
    resources, _ = shellmcp_contract.mcp("resources/list", {}, 20)
    listed = resources.get("resources")
    assert isinstance(listed, list)
    assert any(resource.get("uri") == "shellmcp://system/health" for resource in listed if isinstance(resource, dict))

    read, _ = shellmcp_contract.mcp("resources/read", {"uri": "shellmcp://system/health"}, 21)
    contents = read.get("contents")
    assert isinstance(contents, list) and contents
    assert contents[0].get("mimeType") == "application/json"

    status, descriptor, headers = shellmcp_contract.request("GET", "/mcp?session_id=contract-session")
    assert status == 200
    assert descriptor.get("transport", {}).get("post_path") == "/mcp"
    assert _header(headers, "MCP-Protocol-Version")
    assert _header(headers, "Mcp-Session-Id") == "contract-session"
