"""Runtime MCP-client handshake checks for locally installed clients."""

from __future__ import annotations

import json
import os
import signal
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]
CLIENTS = ("codex", "claude", "opencode")


def _free_port() -> int:
    """Reserve a currently free loopback port for the disposable Hub."""

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _run_client(command: list[str], env: dict[str, str], timeout: int = 45) -> subprocess.CompletedProcess[str]:
    """Run a client command without exposing its environment or raw output."""

    return subprocess.run(command, env=env, cwd=ROOT, capture_output=True, text=True, timeout=timeout)


@pytest.fixture
def disposable_hub(tmp_path: Path):
    """Build and start an isolated Hub for real MCP-client handshake checks."""

    binary = tmp_path / "gptadmin-hub"
    subprocess.run(
        ["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/gptadmin-hub"],
        cwd=ROOT / "go-hub",
        check=True,
        timeout=120,
    )
    port = _free_port()
    config_dir = tmp_path / "hub-config"
    config_dir.mkdir()
    env = os.environ.copy()
    env.update(
        {
            "GPTADMIN_HUB_HOST": "127.0.0.1",
            "GPTADMIN_HUB_PORT": str(port),
            "PORT": str(port),
            "CTL_TOKEN": "ctl",
            "ADMIN_PASSWORD": "pw",
            "PUBLIC_ORIGIN": f"http://127.0.0.1:{port}",
            "MCP_RESOURCE": f"http://127.0.0.1:{port}",
            "GPTADMIN_CONFIG_DIR": str(config_dir),
            "GPTADMIN_ROOT": str(ROOT),
            "NO_PROXY": "localhost,127.0.0.1",
            "no_proxy": "localhost,127.0.0.1",
            "HTTP_PROXY": "",
            "HTTPS_PROXY": "",
            "ALL_PROXY": "",
        }
    )
    process = subprocess.Popen(
        [str(binary)],
        cwd=ROOT,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    base_url = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"{base_url}/version", timeout=1) as response:
                if response.status == 200:
                    break
        except (OSError, urllib.error.URLError):
            time.sleep(0.1)
    else:
        process.kill()
        process.wait(timeout=5)
        pytest.fail("disposable Hub did not become ready")
    try:
        yield base_url
    finally:
        if process.poll() is None:
            os.killpg(os.getpgid(process.pid), signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                process.wait(timeout=5)


def test_installed_mcp_clients_connect_to_canonical_hub(disposable_hub: str, tmp_path: Path) -> None:
    """Prove real local client CLIs connect without touching user config."""

    missing = [client for client in CLIENTS if shutil.which(client) is None]
    if missing:
        pytest.skip(f"real MCP clients unavailable: {', '.join(missing)}")

    mcp_url = disposable_hub + "/mcp"
    common_env = os.environ.copy()
    common_env.update({"NO_PROXY": "localhost,127.0.0.1", "no_proxy": "localhost,127.0.0.1", "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": ""})

    codex_home = tmp_path / "codex-home"
    codex_home.mkdir()
    codex_env = {**common_env, "CODEX_HOME": str(codex_home), "GPTADMIN_TEST_BEARER": "ctl"}
    codex_add = _run_client(["codex", "mcp", "add", "gptadmin", "--url", mcp_url, "--bearer-token-env-var", "GPTADMIN_TEST_BEARER"], codex_env)
    assert codex_add.returncode == 0
    codex_get = _run_client(["codex", "mcp", "get", "gptadmin"], codex_env)
    assert codex_get.returncode == 0
    assert mcp_url in codex_get.stdout
    assert "GPTADMIN_TEST_BEARER" in codex_get.stdout

    claude_config = tmp_path / "claude-config"
    claude_env = {**common_env, "CLAUDE_CONFIG_DIR": str(claude_config)}
    claude_add = _run_client(["claude", "mcp", "add", "--transport", "http", "gptadmin", mcp_url, "--header", "Authorization: Bearer ctl"], claude_env)
    assert claude_add.returncode == 0
    claude_list = _run_client(["claude", "mcp", "list"], claude_env)
    assert claude_list.returncode == 0
    assert "gptadmin" in claude_list.stdout
    assert "Connected" in claude_list.stdout

    opencode_config = tmp_path / "opencode-config"
    opencode_env = {**common_env, "XDG_CONFIG_HOME": str(opencode_config)}
    opencode_add = _run_client(["opencode", "mcp", "add", "gptadmin", "--url", mcp_url, "--header", "Authorization=Bearer ctl"], opencode_env)
    assert opencode_add.returncode == 0
    opencode_list = _run_client(["opencode", "mcp", "list"], opencode_env)
    assert opencode_list.returncode == 0
    assert "gptadmin" in opencode_list.stdout
    assert "connected" in opencode_list.stdout.lower()

    request = urllib.request.Request(
        mcp_url,
        data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "demo", "arguments": {}}}).encode(),
        headers={"Authorization": "Bearer ctl", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        payload = json.loads(response.read().decode("utf-8"))
    assert response.status == 200
    assert payload.get("error") is None
    assert "demo" in json.dumps(payload)
